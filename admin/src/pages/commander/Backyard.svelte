<script lang="ts">
  // The commander's backyard, each section with its own actions: the dorm
  // (name, level, floors, slots, food), the ships training and resting, the
  // furniture owned and the saved theme templates.
  import { createQuery } from '@tanstack/svelte-query'
  import CommanderPage from '../../components/CommanderPage.svelte'
  import Icon from '../../components/Icon.svelte'
  import Picker, { type PickerItem } from '../../components/Picker.svelte'
  import VirtualGrid from '../../components/VirtualGrid.svelte'
  import { bounded } from '../../lib/bounded'
  import { count, day, short } from '../../lib/format'
  import { link } from '../../lib/router.svelte'
  import { InlineEdit } from '../../lib/inline.svelte'
  import { createCommanderWrite, get, keys } from '../../lib/query'
  import type { BackyardDetail, BackyardDetailShip, BackyardFurniture, FurnitureEntry } from '../../lib/backyard'

  let { id }: { id: number } = $props()

  const query = createQuery(() => ({
    queryKey: keys.screen(id, 'backyard', 'detail'),
    queryFn: get<BackyardDetail>(`/commanders/${id}/backyard/detail`),
  }))
  const yard = $derived(query.data)
  const catalog = createQuery(() => ({
    queryKey: keys.catalog('furniture'),
    queryFn: get<FurnitureEntry[]>('/catalog/furniture'),
    staleTime: Infinity,
  }))

  const { mutation: edit, run } = createCommanderWrite(() => id)
  const busy = $derived(edit.isPending)

  // a value edited in place; a furniture being added goes with its field
  const inline = new InlineEdit({ onstop: () => (adding = null) })
  let text = $state('')
  // a select saves on Enter as the number fields do
  function submitOnEnter(event: KeyboardEvent & { currentTarget: HTMLSelectElement }) {
    if (event.key !== 'Enter') return inline.escape(event)
    event.preventDefault()
    event.currentTarget.form?.requestSubmit()
  }
  // a field opened from a dialog: focused once the dialog has given the
  // focus back to its opener
  const focusLater = (node: HTMLInputElement) => {
    setTimeout(() => node.focus(), 0)
  }
  function save(event: SubmitEvent, changed: boolean, path: string, done: string, body: unknown, method = 'POST') {
    inline.submit(event)
    if (changed) run(path, done, body, method)
  }

  const training = $derived(yard?.ships.filter((ship) => ship.floor === 1) ?? [])
  const resting = $derived(yard?.ships.filter((ship) => ship.floor === 2) ?? [])
  const capacityAt = (n: number) => (yard ? (yard.levels.find((row) => row.id === yard.level)?.capacity ?? 0) + (yard.food_capacities[n] ?? 0) : 0)

  // the ship picker, for the floor it adds to
  let shipPicker = $state<Picker>()
  let addFloor = $state(1)
  const candidates = $derived<PickerItem[]>(
    [...(yard?.candidates ?? [])]
      .sort((a, b) => b.level - a.level || b.rarity - a.rarity)
      .map((ship) => ({ id: ship.id, name: ship.name, icon: ship.icon, rarity: ship.rarity, ship: true, sub: ship.busy ?? `Lv.${ship.level}`, disabled: !!ship.busy })),
  )
  function openShips(floor: number) {
    addFloor = floor
    shipPicker?.show()
  }
  function pickShip(item: PickerItem) {
    run('/backyard/ships', `${item.name} ${addFloor === 1 ? 'training' : 'resting'}`, { ship: item.id, floor: addFloor })
  }

  // the furniture picker; a furniture not owned yet is edited before it is
  let furniturePicker = $state<Picker>()
  let adding = $state<BackyardFurniture | null>(null)
  const ownedByID = $derived(new Map((yard?.furniture_list ?? []).map((f) => [f.id, f])))
  const furnitureItems = $derived<PickerItem[]>(
    (catalog.data ?? []).map((f) => {
      const owned = ownedByID.get(f.id)
      return { id: f.id, name: f.name || `#${f.id}`, icon: f.icon, rarity: f.rarity, sub: owned ? `Owned ${owned.count}/${f.max}` : `#${f.id}`, disabled: !!owned && owned.count >= f.max }
    }),
  )
  function pickFurniture(item: PickerItem) {
    const owned = ownedByID.get(item.id)
    const entry = catalog.data?.find((f) => f.id === item.id)
    if (owned) {
      adding = null
      inline.start(`fur:${owned.id}`, owned.count)
    } else if (entry) {
      adding = { id: entry.id, name: entry.name, icon: entry.icon, rarity: entry.rarity, count: 0, max: entry.max, placed: 0, comfortable: entry.comfortable }
      inline.start(`fur:${entry.id}`, 1)
    }
  }
  const furnitureShown = $derived(adding && !ownedByID.has(adding.id) ? [adding, ...(yard?.furniture_list ?? [])] : (yard?.furniture_list ?? []))
  const placedTotal = $derived((yard?.furniture_list ?? []).reduce((sum, f) => sum + f.placed, 0))

  function deleteTheme(pos: number, name: string) {
    if (confirm(`Delete the theme ${name}?`)) run(`/backyard/themes/${pos}`, `${name} deleted`, undefined, 'DELETE')
  }
  function clearFloor(floor: number) {
    if (confirm(`Take every piece of furniture off floor ${floor}?`)) run(`/backyard/floors/${floor}/clear`, `Floor ${floor} cleared`)
  }
  function starterFloor() {
    if (confirm('Put floor 1 back to the starter arrangement?')) run('/backyard/floors/1/starter', 'Floor 1 set to the starter arrangement')
  }
</script>

{#snippet numberForm(label: string, min: number, max: number, onsubmit: (event: SubmitEvent) => void)}
  <form class="ip-row flex flex-col gap-1.5 px-4 py-3" {onsubmit}>
    <span class="ip-sub">{label}</span>
    <span class="flex items-center gap-1.5">
      <!-- svelte-ignore a11y_autofocus -->
      <input class="ip-field num h-9 min-w-0 flex-1 text-lg" type="number" aria-label={label} autofocus {min} {max} bind:value={inline.value} use:bounded={{ min, max }} onkeydown={inline.escape} />
      <button type="submit" class="ip-btn ip-sm h-9" disabled={busy}>Save</button>
    </span>
  </form>
{/snippet}

{#snippet shipRow(ship: BackyardDetailShip, other: number, otherOpen: boolean, otherFull: boolean)}
  <div class="ip-row flex flex-col gap-2 px-2.5 py-2">
    <div class="flex items-center gap-3">
      <Icon icon={ship.icon} rarity={ship.rarity} ship name={ship.name} size={52} />
      <div class="flex min-w-0 flex-1 flex-col gap-0.5">
        <span class="flex items-baseline gap-2">
          <a href="/commanders/{id}/dock?ship={ship.id}" use:link class="truncate text-[17px] text-link">{ship.name}</a>
          <span class="text-sm text-dim">Lv.<span class="num">{ship.level}</span></span>
        </span>
        {#if inline.editing === `exp:${ship.id}`}
          <form class="flex items-center gap-1.5" onsubmit={(e) => save(e, inline.value !== ship.exp, `/backyard/ships/${ship.id}/exp`, `${ship.name}: ${count(inline.value)} exp`, { exp: inline.value })}>
            <!-- svelte-ignore a11y_autofocus -->
            <input class="ip-field num h-8 w-28 min-w-0 text-base" type="number" aria-label="{ship.name} exp" autofocus min="0" max={ship.exp_max} bind:value={inline.value} use:bounded={{ min: 0, max: ship.exp_max }} onkeydown={inline.escape} />
            <span class="num text-sm text-dim">/{count(ship.exp_max)}</span>
            <button type="submit" class="ip-btn ip-sm h-8" disabled={busy}>Save</button>
          </form>
        {:else}
          <span class="flex items-center gap-2 text-sm text-dim">
            <button type="button" aria-label="Edit {ship.name}'s exp" disabled={busy || ship.exp_max === 0} class="cursor-pointer rounded border-0 bg-transparent px-1 text-dim hover:shadow-[inset_0_0_0_2px_var(--color-cyan)] disabled:cursor-default disabled:hover:shadow-none" onclick={() => inline.start(`exp:${ship.id}`, ship.exp)}>
              Exp <span class="num text-base text-soft">{count(ship.exp)}</span>
            </button>
            {#if ship.floor === 1 && ship.pending_exp}<span class="num text-base text-ok">+{count(ship.pending_exp)}</span>{/if}
          </span>
        {/if}
      </div>
      <span class="flex flex-none gap-1.5">
        <button type="button" class="ip-btn2 ip-sm" disabled={busy || !otherOpen || otherFull} onclick={() => run(`/backyard/ships/${ship.id}/floor`, `${ship.name} ${other === 1 ? 'training' : 'resting'}`, { floor: other })}>{other === 1 ? 'Train' : 'Rest'}</button>
        <button type="button" aria-label="Remove {ship.name}" class="grid size-[34px] cursor-pointer place-items-center rounded-full border-0 bg-grey text-white hover:bg-grey-hi disabled:opacity-50" disabled={busy} onclick={() => run(`/backyard/ships/${ship.id}`, `${ship.name} left the backyard`, undefined, 'DELETE')}>
          <svg aria-hidden="true" viewBox="0 0 24 24" class="size-4" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18" /></svg>
        </button>
      </span>
    </div>
    {#if inline.editing === `pop:${ship.id}`}
      <form class="flex flex-wrap items-center gap-2 pl-[64px]" onsubmit={(e) => save(e, inline.value !== ship.pop_icon || inline.second !== ship.pop_intimacy, `/backyard/ships/${ship.id}/pop`, `${ship.name}: ${inline.value} coins, ${inline.second} affection waiting`, { coins: inline.value, intimacy: inline.second })}>
        <label class="flex items-center gap-1.5 text-sm text-dim">Coins
          <!-- svelte-ignore a11y_autofocus -->
          <input class="ip-field num h-8 w-20 text-base" type="number" autofocus min="0" max={yard?.pop_coins_max} bind:value={inline.value} use:bounded={{ min: 0, max: yard?.pop_coins_max }} onkeydown={inline.escape} />
        </label>
        <label class="flex items-center gap-1.5 text-sm text-dim">Affection
          <input class="ip-field num h-8 w-20 text-base" type="number" min="0" max={yard?.pop_intimacy_max} bind:value={inline.second} use:bounded={{ min: 0, max: yard?.pop_intimacy_max }} onkeydown={inline.escape} />
        </label>
        <button type="submit" class="ip-btn ip-sm h-8" disabled={busy}>Save</button>
      </form>
    {:else}
      <span class="flex items-center gap-2 pl-[64px] text-sm text-dim">
        <button type="button" aria-label="Edit what {ship.name} popped" disabled={busy} class="cursor-pointer rounded border-0 bg-transparent px-1 text-dim hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => inline.start(`pop:${ship.id}`, ship.pop_icon, ship.pop_intimacy)}>
          Coins <span class="num text-base text-gold">{ship.pop_icon}</span> · Affection <span class="num text-base text-[#ff9ec8]">{ship.pop_intimacy}</span>
        </button>
        <button type="button" class="ip-btn2 ip-sm ml-auto h-7 px-2.5 text-sm" disabled={busy || (ship.pop_icon === 0 && ship.pop_intimacy === 0)} onclick={() => run(`/backyard/ships/${ship.id}/collect`, `${ship.name}: collected`)}>Collect</button>
      </span>
    {/if}
  </div>
{/snippet}

{#snippet floorPanel(title: string, floor: number, ships: BackyardDetailShip[], open: boolean)}
  {@const other = floor === 1 ? 2 : 1}
  {@const otherShips = floor === 1 ? resting : training}
  <div class="flex min-w-0 flex-1 flex-col gap-2">
    <h3 class="m-0 flex items-center gap-2.5 text-xl">
      {title}
      {#if yard}<span><span class="num text-xl">{ships.length}</span><span class="num text-base text-dim"> /{yard.slots}</span></span>{/if}
      <button type="button" class="ip-btn2 ip-sm ml-auto" disabled={busy || !open || !yard || ships.length >= yard.slots} onclick={() => openShips(floor)}>Add</button>
    </h3>
    {#each ships as ship (ship.id)}
      {@render shipRow(ship, other, !!yard && other <= yard.floors, !!yard && otherShips.length >= yard.slots)}
    {:else}
      <div class="ip-row px-3 py-2.5 text-dim">{open ? 'Empty' : 'Locked'}</div>
    {/each}
  </div>
{/snippet}

{#snippet furnitureTile(f: BackyardFurniture)}
  {#if inline.editing === `fur:${f.id}`}
    <form class="ip-row flex h-full items-center gap-2 px-2 py-1" onsubmit={(e) => save(e, inline.value !== f.count, `/backyard/furniture/${f.id}`, inline.value ? `${f.name}: ${inline.value}` : `${f.name} removed`, { count: inline.value }, 'PUT')}>
      <Icon icon={f.icon} rarity={f.rarity} size={40} name={f.name} label={false} />
      <span class="min-w-0 flex-1 truncate text-[15px]">{f.name || `#${f.id}`}</span>
      <!-- svelte-ignore a11y_autofocus -->
      <input class="ip-field num h-9 w-16 text-right text-lg" type="number" aria-label="{f.name} count" autofocus use:focusLater min="0" max={f.max} bind:value={inline.value} use:bounded={{ min: 0, max: f.max }} onkeydown={inline.escape} />
      <button type="submit" class="ip-btn ip-sm h-9" disabled={busy}>Save</button>
    </form>
  {:else}
    <button type="button" aria-label="Edit {f.name}" disabled={busy} class="ip-row flex h-full w-full cursor-pointer items-center gap-3 border-0 px-2 py-1 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => inline.start(`fur:${f.id}`, f.count)}>
      <Icon icon={f.icon} rarity={f.rarity} size={40} name={f.name} label={false} />
      <span class="flex min-w-0 flex-1 flex-col">
        <span class="truncate text-[16px]">{f.name || `#${f.id}`}</span>
        <span class="text-[13px] text-dim">Placed <span class="num text-sm text-soft">{f.placed}</span> · Comfort <span class="num text-sm text-soft">{f.comfortable}</span></span>
      </span>
      <span class="pr-1.5"><span class="num text-xl">{f.count}</span><span class="num text-sm text-dim">/{f.max}</span></span>
    </button>
  {/if}
{/snippet}

<CommanderPage {id} title="Backyard">
  {#snippet children(summary)}
    {#if yard}
      {@const capacity = yard.levels.find((row) => row.id === yard.level)?.capacity ?? 0}
      <div class="flex flex-col gap-5">
        <section aria-label="Dorm" class="ip-panel">
          <div class="ip-head flex-wrap items-center gap-y-2">
            {#if inline.editing === 'name'}
              <form class="flex items-center gap-1.5" onsubmit={(e) => save(e, text !== yard.name, '/backyard/name', text ? `Renamed ${text}` : 'Name reset', { name: text })}>
                <!-- svelte-ignore a11y_autofocus -->
                <input class="ip-field h-9 w-64 text-lg" aria-label="Backyard name" autofocus maxlength={yard.name_max} pattern={'\\S*'} bind:value={text} onkeydown={inline.escape} />
                <button type="submit" class="ip-btn ip-sm h-9" disabled={busy}>Save</button>
              </form>
            {:else}
              <h2><button type="button" aria-label="Rename the backyard" class="cursor-pointer rounded border-0 bg-transparent px-1 text-inherit text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => ((text = yard.name), (inline.editing = 'name'))}>{yard.name || `${summary.name}'s Backyard`}</button></h2>
            {/if}
            <a href="/commanders/{id}/items" use:link class="ml-auto text-base text-link">Supplies in Items</a>
            <span class="text-[15px] text-soft">Next settle <span class="num text-xl text-white">{short(yard.next_settle, '/')}</span></span>
          </div>
          <div class="grid grid-cols-[repeat(5,minmax(0,1fr))] gap-3 px-5 pt-4">
            {#if inline.editing === 'level'}
              <form class="ip-row flex flex-col gap-1.5 px-4 py-3" onsubmit={(e) => save(e, inline.value !== yard.level, '/backyard/level', `Level ${inline.value}`, { level: inline.value })}>
                <span class="ip-sub">Level</span>
                <span class="flex items-center gap-1.5">
                  <!-- svelte-ignore a11y_autofocus -->
                  <select class="ip-field h-9 min-w-0 flex-1" aria-label="Level" autofocus bind:value={inline.value} onkeydown={submitOnEnter}>
                    {#each yard.levels as row (row.id)}<option value={row.id} disabled={row.id < yard.level}>Lv. {row.id}</option>{/each}
                  </select>
                  <button type="submit" class="ip-btn ip-sm h-9" disabled={busy}>Save</button>
                </span>
              </form>
            {:else}
              <button type="button" aria-label="Edit the level" disabled={busy || yard.level >= yard.max_level} class="ip-row flex cursor-pointer flex-col gap-1 border-0 px-4 py-3 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)] disabled:cursor-default disabled:hover:shadow-none" onclick={() => inline.start('level', yard.level)}>
                <span class="ip-sub">Level</span>
                <span><span class="num text-[34px]">{yard.level}</span><span class="num text-xl text-dim"> /{yard.max_level}</span></span>
                <span class="text-[13px] text-dim">Comfort <span class="num text-base text-soft">{yard.comfort}</span></span>
              </button>
            {/if}

            <div class="ip-row relative flex flex-col gap-1 px-4 py-3">
              <span class="ip-sub">Floors</span>
              <span><span class="num text-[34px]">{yard.floors}</span><span class="num text-xl text-dim"> /{yard.max_floors}</span></span>
              {#if yard.floors < yard.max_floors}<span class="text-[13px] text-dim">Floor 2 at Lv. <span class="num text-base text-soft">{yard.floor_level}</span></span>{/if}
              {#if yard.floors < yard.max_floors}
                <button type="button" class="ip-btn2 ip-sm absolute top-2 right-2 h-8 px-2.5 text-sm" disabled={busy || yard.level < yard.floor_level} onclick={() => run('/backyard/floors', 'Floor 2 open', { floors: yard.floors + 1 })}>Unlock</button>
              {/if}
            </div>

            {#if inline.editing === 'slots'}
              {@render numberForm('Slots', yard.min_slots, yard.max_slots, (e) => save(e, inline.value !== yard.slots, '/backyard/slots', `${inline.value} slots`, { slots: inline.value }))}
            {:else}
              <button type="button" aria-label="Edit the slots" disabled={busy} class="ip-row flex cursor-pointer flex-col gap-1 border-0 px-4 py-3 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => inline.start('slots', yard.slots)}>
                <span class="ip-sub">Slots</span>
                <span><span class="num text-[34px]">{yard.slots}</span><span class="num text-xl text-dim"> /{yard.max_slots}</span></span>
              </button>
            {/if}

            {#if inline.editing === 'food'}
              {@render numberForm('Food', 0, yard.food_max, (e) => save(e, inline.value !== yard.food, '/backyard/food', `Food ${count(inline.value)}`, { food: inline.value }))}
            {:else}
              <div class="ip-row relative flex hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]">
                <button type="button" aria-label="Edit the food" disabled={busy} class="flex flex-1 cursor-pointer flex-col gap-1 border-0 bg-transparent px-4 py-3 text-left text-white" onclick={() => inline.start('food', yard.food)}>
                  <span class="ip-sub">Food</span>
                  <span><span class="num text-[34px]">{count(yard.food)}</span><span class="num text-base text-dim"> /{count(yard.food_max)}</span></span>
                  <span aria-hidden="true" class="h-1.5 w-full rounded-[3px] bg-black/40"><span class="block h-full rounded-[3px] bg-[#5fd38d]" style:width="{yard.food_max ? Math.min(100, (yard.food / yard.food_max) * 100) : 0}%"></span></span>
                  <span class="text-[13px] text-dim">{yard.food_until ? `Lasts until ${short(yard.food_until, '/')}` : 'Nobody training'}</span>
                </button>
                <button type="button" class="ip-btn2 ip-sm absolute top-2 right-2 h-8 px-2.5 text-sm" disabled={busy || yard.food >= yard.food_max} onclick={() => run('/backyard/fill', 'Food filled')}>Fill</button>
              </div>
            {/if}

            {#if inline.editing === 'ext'}
              <form class="ip-row flex flex-col gap-1.5 px-4 py-3" onsubmit={(e) => save(e, inline.value !== yard.food_extensions, '/backyard/food-extensions', `${inline.value} capacity extensions`, { count: inline.value })}>
                <span class="ip-sub">Capacity extensions</span>
                <span class="flex items-center gap-1.5">
                  <!-- svelte-ignore a11y_autofocus -->
                  <input class="ip-field num h-9 min-w-0 flex-1 text-lg" type="number" aria-label="Capacity extensions" autofocus min="0" max={yard.food_extensions_max} bind:value={inline.value} use:bounded={{ min: 0, max: yard.food_extensions_max }} onkeydown={inline.escape} />
                  <button type="submit" class="ip-btn ip-sm h-9" disabled={busy}>Save</button>
                </span>
                <span class="text-[13px] text-dim">Capacity <span class="num text-base text-soft">{count(capacityAt(inline.value))}</span></span>
              </form>
            {:else}
              <button type="button" aria-label="Edit the capacity extensions" disabled={busy} class="ip-row flex cursor-pointer flex-col gap-1 border-0 px-4 py-3 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => inline.start('ext', yard.food_extensions)}>
                <span class="ip-sub">Capacity extensions</span>
                <span><span class="num text-[34px]">{yard.food_extensions}</span><span class="num text-xl text-dim"> /{yard.food_extensions_max}</span></span>
                <span class="text-[13px] text-dim">Base <span class="num text-base text-soft">{count(capacity)}</span></span>
              </button>
            {/if}
          </div>
          <div class="grid grid-cols-2 gap-3 px-5 pt-3 pb-5">
            {#each yard.floor_list as f (f.floor)}
              <div class="ip-row flex items-center gap-3 px-4 py-2">
                <span class="text-[17px]">Floor {f.floor}</span>
                <span class="text-[15px] text-soft"><span class="num text-xl text-white">{f.placed}</span> placed</span>
                {#if f.starter}<span class="ip-tag bg-white/10 text-dim">Starter</span>{/if}
                <span class="ml-auto flex gap-1.5">
                  {#if f.floor === 1 && !f.starter}<button type="button" class="ip-btn2 ip-sm" disabled={busy} onclick={starterFloor}>Starter</button>{/if}
                  <button type="button" class="ip-danger ip-sm" disabled={busy || f.placed === 0} onclick={() => clearFloor(f.floor)}>Clear</button>
                </span>
              </div>
            {/each}
          </div>
        </section>

        <section aria-label="Ships" class="ip-panel">
          <div class="ip-head flex-wrap items-center gap-y-2">
            <h2>Ships</h2>
            <span class="text-[15px] text-soft">Every <span class="num text-xl text-white">{yard.tick_seconds}</span>s <span class="num text-xl text-white">+{yard.tick_exp.toFixed(1)}</span> exp, <span class="num text-xl text-white">{yard.tick_food}</span> food</span>
            <span class="text-[15px] text-soft">Furniture coins <a href="/commanders/{id}/items" use:link class="num text-xl text-link">{count(yard.dorm_money)}</a></span>
            <span class="ml-auto flex gap-2">
              <button type="button" class="ip-btn2 ip-sm" disabled={busy || yard.pending_exp === 0} onclick={() => run('/backyard/settle', 'Training settled')}>Settle{yard.pending_exp ? ` +${count(yard.pending_exp)}` : ''}</button>
              <button type="button" class="ip-btn2 ip-sm" disabled={busy || !yard.ships.some((s) => s.pop_icon || s.pop_intimacy)} onclick={() => run('/backyard/collect', 'Collected')}>Collect all</button>
            </span>
          </div>
          <div class="flex items-start gap-5 px-5 pt-4 pb-5">
            {@render floorPanel('Training', 1, training, true)}
            {@render floorPanel('Resting', 2, resting, yard.floors >= 2)}
          </div>
          <Picker bind:this={shipPicker} trigger={false} label={addFloor === 1 ? 'Training ship' : 'Resting ship'} items={candidates} placeholder="Name or id" value={null} onselect={pickShip} />
        </section>

        <section aria-label="Furniture" class="ip-panel overflow-hidden">
          <div class="ip-head items-center">
            <h2>Furniture</h2>
            <span class="text-[15px] text-soft"><span class="num text-xl text-white">{count(yard.furniture)}</span> owned · <span class="num text-xl text-white">{count(placedTotal)}</span> placed</span>
            <button type="button" class="ip-btn2 ip-sm ml-auto" disabled={busy} onclick={() => run('/grant-all', 'Every furniture granted', { type: 5 })}>Add all</button>
            <button type="button" class="ip-btn2 ip-sm" disabled={busy || !catalog.data} onclick={() => furniturePicker?.show()}>Add</button>
          </div>
          <VirtualGrid items={furnitureShown} key={(f) => f.id} min={300} rowHeight={54} gap={6} pad={12} overscan={3} label="Furniture" class="max-h-[420px]">
            {#snippet cell(f)}
              {@render furnitureTile(f)}
            {/snippet}
            {#snippet empty()}
              <p class="m-0 px-1 text-lg text-dim">None</p>
            {/snippet}
          </VirtualGrid>
          <Picker bind:this={furniturePicker} trigger={false} label="Furniture" items={furnitureItems} loading={catalog.isPending} placeholder="Name or id" value={null} onselect={pickFurniture} />
        </section>

        <section aria-label="Saved themes" class="ip-panel">
          <div class="ip-head items-center">
            <h2>Saved themes</h2>
            <span class="text-[15px] text-soft"><span class="num text-xl text-white">{yard.themes.filter((t) => t.shared_at).length}</span><span class="num text-base text-dim"> /{yard.shared_max}</span> shared</span>
          </div>
          <div class="flex flex-col gap-1.5 p-2.5">
            {#each yard.themes as theme (theme.pos)}
              {@const name = theme.name || `Theme ${theme.pos}`}
              <div class="ip-row flex items-center gap-4 px-3 py-2">
                <span class="num w-6 text-xl text-soft">{theme.pos}</span>
                <div class="flex min-w-0 flex-col">
                  <span class="truncate text-[17px]">{name}</span>
                  <span class="text-sm">{#if theme.shared_at}<span class="text-ok">Shared since {day(theme.shared_at)}</span>{:else}<span class="text-dim">Not shared</span>{/if}</span>
                </div>
                <span class="ml-auto text-sm text-soft"><span class="num text-lg text-white">{theme.pieces}</span> pieces · <span class="num text-lg text-[#ff9ec8]">{theme.likes}</span> likes · <span class="num text-lg text-white">{theme.favs}</span> saved</span>
                <button type="button" class="ip-btn2 ip-sm" disabled={busy || (!theme.shared_at && yard.themes.filter((t) => t.shared_at).length >= yard.shared_max)} onclick={() => run(`/backyard/themes/${theme.pos}/share`, theme.shared_at ? `${name} unshared` : `${name} shared`, { shared: !theme.shared_at })}>{theme.shared_at ? 'Unshare' : 'Share'}</button>
                <button type="button" class="ip-danger ip-sm" disabled={busy} onclick={() => deleteTheme(theme.pos, name)}>Delete</button>
              </div>
            {:else}
              <div class="ip-row px-3 py-2.5 text-dim">None</div>
            {/each}
          </div>
        </section>
      </div>
    {:else if query.isError}
      <p class="text-bad">{query.error.message}</p>
    {/if}
  {/snippet}
</CommanderPage>
