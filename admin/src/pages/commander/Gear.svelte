<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query'
  import { createDebouncedValue } from '@tanstack/svelte-pacer'
  import CommanderPage from '../../components/CommanderPage.svelte'
  import Icon from '../../components/Icon.svelte'
  import PlayersGrant from '../../components/PlayersGrant.svelte'
  import VirtualGrid from '../../components/VirtualGrid.svelte'
  import PlayersTabs from '../../components/PlayersTabs.svelte'
  import Seg from '../../components/Seg.svelte'
  import Stepper from '../../components/Stepper.svelte'
  import type { Amount } from '../../lib/api'
  import { fuzzy } from '../../lib/fuzzy'
  import { createCommanderWrite, get } from '../../lib/query'
  import { drop, playerKeys, statLabels, type GearRow, type PlayerGear, type SpWeapon } from '../../lib/players'
  import { bounded } from '../../lib/bounded'

  let { id }: { id: number } = $props()

  const gear = createQuery(() => ({
    queryKey: playerKeys.gear(id),
    queryFn: get<PlayerGear>(`/commanders/${id}/gear`),
  }))

  let query = $state('')
  const [debounced] = createDebouncedValue(() => query, { wait: 150 })
  let show = $state<'all' | 'worn' | 'bag'>('all')

  // what the edit card shows, found again in each refetch
  let pick = $state<{ kind: 'gear' | 'skin' | 'augment'; id: number } | null>(null)
  const selected = $derived.by(() => {
    const data = gear.data
    if (!pick || !data) return null
    const { kind, id } = pick
    if (kind === 'gear') {
      const row = data.equipments.find((r) => r.id === id)
      return row ? { kind, row } : null
    }
    if (kind === 'skin') {
      const row = data.equip_skins.find((r) => r.id === id)
      return row ? { kind, row } : null
    }
    const row = data.spweapons.find((r) => r.id === id)
    return row ? { kind, row } : null
  }) as { kind: 'gear'; row: GearRow } | { kind: 'skin'; row: Amount } | { kind: 'augment'; row: SpWeapon } | null
  const isPicked = (kind: string, row: { id: number }) => pick?.kind === kind && pick.id === row.id

  // the fields start at what the commander has, again after a save (the
  // row stays the same object while unchanged)
  const row = $derived(selected?.row)
  let count = $derived(row && 'count' in row ? row.count : 0)
  let level = $derived(row && 'levels' in row ? row.level : 0)
  let attr1 = $derived(row && 'levels' in row ? row.attr_1 : 0)
  let attr2 = $derived(row && 'levels' in row ? row.attr_2 : 0)

  const shownGear = $derived(fuzzy(gear.data?.equipments ?? [], debounced(), (g) => [g.name, g.id]))
  const total = $derived((gear.data?.equipments ?? []).reduce((sum, g) => sum + g.count, 0))
  const shownAugments = $derived((gear.data?.spweapons ?? []).filter((w) => show === 'all' || (show === 'worn') === !!w.ship_id))
  const augmentLevel = $derived(selected?.kind === 'augment' ? selected.row.levels[level] : undefined)

  const { mutation: write } = createCommanderWrite(() => id)

  function saveCount(next: number, commander: string) {
    if (!selected || selected.kind === 'augment') return
    const path = selected.kind === 'gear' ? '/gear' : '/equip-skins'
    const what = next ? `${selected.row.name} set to ${next}` : `${selected.row.name} removed`
    write.mutate({ method: 'PUT', path: `${path}/${selected.row.id}`, body: { count: next }, done: what, commander })
  }

  function saveAugment(commander: string) {
    if (selected?.kind !== 'augment' || !augmentLevel) return
    write.mutate({ method: 'PUT', path: `/spweapons/${selected.row.id}`, body: { template_id: augmentLevel.template_id, attr_1: attr1, attr_2: attr2 }, done: `${selected.row.name} saved`, commander })
  }

  function deleteAugment(commander: string) {
    if (selected?.kind !== 'augment' || !confirm(`Delete ${selected.row.name}?`)) return
    write.mutate({ method: 'DELETE', path: `/spweapons/${selected.row.id}`, done: `${selected.row.name} deleted`, commander })
  }

  const stat = (attribute: string) => statLabels[attribute] ?? attribute.toUpperCase()
</script>

<CommanderPage {id} title="Gear">
  {#snippet children(summary)}
    <div class="flex flex-wrap items-start gap-6">
      <div class="flex min-w-0 flex-[1_1_560px] flex-col gap-6">
        <section aria-label="Depot gear" class="ip-panel overflow-hidden">
          <PlayersTabs {id} current="Gear" />
          <div class="ip-head items-center">
            <h2>Depot gear</h2>
            <span class="ip-sub">STACKS</span>
            <span class="num text-2xl">{gear.data?.equipments.length ?? ''}</span>
            {#if gear.data}<span class="num text-2xl text-soft">{total}/{gear.data.equip_bag_max}</span>{/if}
            <label class="ml-auto flex items-center gap-2.5 text-[17px]">
              Find
              <input type="search" class="ip-field w-56" placeholder="Name or id" bind:value={query} />
            </label>
          </div>
          <VirtualGrid items={shownGear} key={(row) => row.id} min={150} rowHeight={160} gap={16} pad={24} overscan={3} label="Depot gear" class="max-h-[560px]">
            {#snippet cell(row)}
              <button
                type="button"
                aria-pressed={isPicked('gear', row)}
                class={['relative flex h-full w-full cursor-pointer flex-col items-center gap-2 rounded-[3px] border-0 bg-card px-2 pt-3 pb-3 text-ink shadow-[0_2px_8px_rgb(0_0_0/0.25)]', isPicked('gear', row) && 'shadow-[inset_0_0_0_3px_var(--color-cyan),0_2px_8px_rgb(0_0_0/0.25)]']}
                onclick={() => (pick = { kind: 'gear', id: row.id })}
              >
                <div class="relative">
                  <Icon icon={row.icon} rarity={row.rarity} count={row.count} size={80} name={row.name} />
                  <span class="num absolute top-0.5 left-1 text-lg text-gold [text-shadow:0_1px_2px_rgb(0_0_0/0.8)]">+{row.enhance}</span>
                </div>
                <span class="line-clamp-2 text-center text-[15px] leading-tight">{row.name || `#${row.id}`}</span>
              </button>
            {/snippet}
            {#snippet empty()}
              {#if gear.data}<p class="m-0 text-lg text-dim">No gear in the depot</p>{:else if gear.error}<p class="m-0 text-bad">{gear.error.message}</p>{/if}
            {/snippet}
          </VirtualGrid>
        </section>

        <section id="augments" aria-label="Augment modules" class="ip-panel scroll-mt-4 overflow-hidden">
          <div class="ip-head items-center">
            <h2>Augment modules</h2>
            <span class="ip-sub">SPWEAPON</span>
            <span class="num text-2xl">{gear.data?.spweapons.length ?? ''}</span>
            <div class="ml-auto">
              <Seg
                label="Show"
                bind:value={show}
                options={[
                  { value: 'all', label: 'All' },
                  { value: 'worn', label: 'Worn' },
                  { value: 'bag', label: 'In bag' },
                ]}
              />
            </div>
          </div>
          <ul class="m-0 flex max-h-[480px] list-none flex-col gap-2 overflow-y-auto p-3">
            {#each shownAugments as w (w.id)}
              <li>
                <button
                  type="button"
                  aria-pressed={isPicked('augment', w)}
                  class={['ip-row grid w-full cursor-pointer grid-cols-[56px_minmax(0,1fr)_auto_auto] items-center gap-4 border-0 px-2 py-2 text-left text-white', isPicked('augment', w) && 'ip-sel']}
                  onclick={() => (pick = { kind: 'augment', id: w.id })}
                >
                  <div class="relative">
                    <Icon icon={w.icon} rarity={w.rarity} size={56} name={w.name} />
                    <span class="num absolute top-0 left-1 text-base text-gold [text-shadow:0_1px_2px_rgb(0_0_0/0.8)]">+{w.levels[w.level]?.plus ?? 0}</span>
                  </div>
                  <span class="flex min-w-0 flex-col">
                    <span class="truncate text-lg">{w.name}</span>
                    <span><span class={['ip-tag text-[10px]', w.unique ? 'bg-[#a46be0]' : 'bg-[#4f7fe0]']}>{w.unique ? 'UNIQUE' : 'GENERAL'}</span></span>
                  </span>
                  <span class="flex gap-4 text-sm whitespace-nowrap">
                    <span>{stat(w.attribute_1)} <span class="num text-xl">{w.value_1 + w.attr_1}</span></span>
                    <span>{stat(w.attribute_2)} <span class="num text-xl">{w.value_2 + w.attr_2}</span></span>
                    <span class="text-dim">PT <span class="num text-xl text-white">{w.pt}</span></span>
                  </span>
                  <span class={['w-28 truncate pr-2 text-right', w.ship_id ? 'text-gold' : 'text-dim']}>{w.ship_name || (w.ship_id ? `#${w.ship_id}` : 'In bag')}</span>
                </button>
              </li>
            {:else}
              {#if gear.data}<li class="px-2 py-1 text-lg text-dim">No augment module</li>{/if}
            {/each}
          </ul>
        </section>

        <section aria-label="Gear skin box" class="ip-panel overflow-hidden">
          <div class="ip-head items-center">
            <h2>Gear skin box</h2>
            <span class="ip-sub">EQUIP SKINS</span>
            <span class="num text-2xl">{gear.data?.equip_skins.length ?? ''}</span>
          </div>
          <ul class="m-0 flex list-none flex-wrap gap-2.5 p-4">
            {#each gear.data?.equip_skins ?? [] as skin (skin.id)}
              <li>
                <button
                  type="button"
                  aria-pressed={isPicked('skin', skin)}
                  class={['ip-row flex cursor-pointer items-center gap-2.5 border-0 py-1.5 pr-3.5 pl-1.5 text-[17px] text-white', isPicked('skin', skin) && 'ip-sel']}
                  onclick={() => (pick = { kind: 'skin', id: skin.id })}
                >
                  <Icon icon={skin.icon} rarity={skin.rarity} size={40} name={skin.name} />
                  {skin.name || `#${skin.id}`}
                  <span class="num min-w-6 rounded-xl bg-white/12 px-1.5 text-center text-lg">{skin.count}</span>
                </button>
              </li>
            {:else}
              {#if gear.data}<li class="text-lg text-dim">No gear skin</li>{/if}
            {/each}
          </ul>
        </section>
      </div>

      <div class="flex min-w-[320px] flex-[0_1_380px] flex-col gap-5">
        <PlayersGrant
          {id}
          name={summary.name}
          kinds={[
            { type: drop.equip, label: 'Gear' },
            { type: drop.spweapon, label: 'Augment' },
            { type: drop.equipSkin, label: 'Gear skin' },
          ]}
          all={[
            { label: 'Every gear', body: { type: drop.equip } },
            { label: 'Best of each', body: { type: drop.equip, best: true } },
          ]}
        />
        {#if selected}
          <section aria-labelledby="edit-h" class="ip-card">
            <div class="ip-card-head"><h2 id="edit-h" class="truncate">{selected.row.name || `#${selected.row.id}`}</h2></div>
            <div class="ip-card-body">
              {#if selected.kind === 'augment'}
                {@const w = selected.row}
                <div class="flex items-center gap-3.5">
                  <Icon icon={w.icon} rarity={w.rarity} size={64} name={w.name} />
                  <span class="flex min-w-0 flex-col">
                    <span class="text-xl">{w.unique ? 'Unique' : 'General'}</span>
                    <span class="text-sm text-[#6b7386]"><span class="num text-base">#{w.id}</span>{w.ship_id ? ` · on ${w.ship_name}` : ' · in bag'}</span>
                  </span>
                </div>
                <div class="ip-strip">LEVEL</div>
                <Stepper label="Level" bind:value={level} min={0} max={w.levels.length - 1} />
                {#if augmentLevel}<span class="text-[17px]"><span class="num text-xl">+{augmentLevel.plus}</span> {augmentLevel.name}</span>{/if}
                <div class="ip-strip">ROLLS</div>
                <div class="grid grid-cols-2 gap-3">
                  <label class="ip-lab">
                    {stat(w.attribute_1)} +0 to {augmentLevel?.max_1 ?? 0}
                    <input class="ip-field num text-[22px]" type="number" min="0" max={augmentLevel?.max_1 ?? 0} bind:value={attr1} use:bounded={{ min: 0, max: augmentLevel?.max_1 ?? 0 }} />
                  </label>
                  <label class="ip-lab">
                    {stat(w.attribute_2)} +0 to {augmentLevel?.max_2 ?? 0}
                    <input class="ip-field num text-[22px]" type="number" min="0" max={augmentLevel?.max_2 ?? 0} bind:value={attr2} use:bounded={{ min: 0, max: augmentLevel?.max_2 ?? 0 }} />
                  </label>
                </div>
                <div class="grid grid-cols-2 gap-3">
                  <button type="button" class="ip-danger" disabled={write.isPending} onclick={() => deleteAugment(summary.name)}>Delete</button>
                  <button type="button" class="ip-btn" disabled={write.isPending || (level === w.level && attr1 === w.attr_1 && attr2 === w.attr_2)} onclick={() => saveAugment(summary.name)}>Save</button>
                </div>
              {:else}
                {@const row = selected.row}
                <div class="flex items-center gap-3.5">
                  <Icon icon={row.icon} rarity={row.rarity} size={64} name={row.name} />
                  <span class="num text-lg text-[#6b7386]">#{row.id}</span>
                </div>
                <div class="ip-strip">COUNT</div>
                <Stepper label="Count" bind:value={count} min={0} />
                <div class="grid grid-cols-2 gap-3">
                  <button type="button" class="ip-danger" disabled={write.isPending} onclick={() => saveCount(0, summary.name)}>Remove</button>
                  <button type="button" class="ip-btn" disabled={write.isPending || count === row.count} onclick={() => saveCount(count, summary.name)}>Save</button>
                </div>
              {/if}
            </div>
          </section>
        {/if}
      </div>
    </div>
  {/snippet}
</CommanderPage>
