<script lang="ts">
  // The dock as the game's ship screen shows it: the selected ship's art
  // (the skin it wears, or one previewed) over most of the screen, its level,
  // affinity, stats, skills and gear over it (hidden with Overlays), its
  // skins on the right to preview, wear or grant, and below a search,
  // filters and a carousel of the commander's ships. Its Skins view
  // (DockSkins, ?view=skins or ?skin=) is the same screen over every skin
  // of the game.
  import { createQuery } from '@tanstack/svelte-query'
  import { createDebouncedValue } from '@tanstack/svelte-pacer'
  import Carousel, { pickLinked } from '../../components/Carousel.svelte'
  import CommanderPage from '../../components/CommanderPage.svelte'
  import DockShipCard from '../../components/DockShipCard.svelte'
  import Icon from '../../components/Icon.svelte'
  import Seg from '../../components/Seg.svelte'
  import SkinArt from '../../components/SkinArt.svelte'
  import Live2DBadge from '../../components/Live2DBadge.svelte'
  import { createLive2D } from '../../lib/live2d.svelte'
  import ShipEditors from './ShipEditors.svelte'
  import DockSkins from './DockSkins.svelte'
  import type { CatalogSkin } from '../../components/SkinSelect.svelte'
  import { fleetName, type Dock, type DockShip, type ShipDetail } from '../../lib/dock'
  import { repairPercent, type MetaShip } from '../../lib/metaships'
  import { fuzzy } from '../../lib/fuzzy'
  import { cardArt, drop } from '../../lib/players'
  import { createCommanderWrite, get, keys } from '../../lib/query'
  import { link, navigate, param, route } from '../../lib/router.svelte'

  // ship: a ship's own path (/dock/:ship), as ?ship= selects it
  let { id, ship: pathShip }: { id: number; ship?: number } = $props()

  const view = $derived.by(() => {
    const search = new URLSearchParams(route.search)
    if (search.get('view') === 'skins' || search.has('skin')) return 'skins'
    if (search.get('view') === 'ships' || search.has('ship')) return 'ships'
    return 'ships'
  })
  const show = (to: 'ships' | 'skins') => navigate(`/commanders/${id}/dock${to === 'skins' ? '?view=skins' : '?view=ships'}`)

  const dock = createQuery(() => ({
    queryKey: keys.screen(id, 'dock'),
    queryFn: get<Dock>(`/commanders/${id}/dock`),
  }))
  const catalog = createQuery(() => ({
    queryKey: keys.catalog('skins'),
    queryFn: get<CatalogSkin[]>('/catalog/skins'),
    staleTime: Infinity,
  }))

  let query = $state('')
  let lockedOnly = $state(false)
  let sort = $state<'level' | 'rarity' | 'new'>('level')
  const [debounced] = createDebouncedValue(() => query, { wait: 150 })

  const by = {
    level: (a: DockShip, b: DockShip) => b.level - a.level || b.rarity - a.rarity || a.id - b.id,
    rarity: (a: DockShip, b: DockShip) => b.rarity - a.rarity || b.level - a.level || a.id - b.id,
    new: (a: DockShip, b: DockShip) => b.id - a.id,
  }

  // a search ranks by match, otherwise the chosen order
  const shown = $derived.by(() => {
    const ships = (dock.data?.ships ?? []).filter((ship) => !lockedOnly || ship.locked)
    const q = debounced().trim()
    return q ? fuzzy(ships, q, (ship) => [ship.name, ship.type, ship.nation, ship.template_id]) : ships.sort(by[sort])
  })

  // the ship in the URL (?ship=, from links elsewhere), the clicked one, else
  // the first one shown
  let selectedID = $state<number | null>(null)
  pickLinked(() => param('ship') ?? pathShip ?? null, (linked) => (selectedID = linked))
  const selected = $derived(shown.find((ship) => ship.id === selectedID) ?? shown[0] ?? null)

  const detail = createQuery(() => ({
    queryKey: keys.screen(id, 'dock', selected?.id ?? 0),
    queryFn: get<ShipDetail>(`/commanders/${id}/dock/${selected?.id}`),
    enabled: !!selected,
  }))
  const ship = $derived(detail.data?.id === selected?.id ? detail.data : undefined)
  // a META ship's repairs and research, {meta: false} for any other ship
  const metaQuery = createQuery(() => ({
    queryKey: keys.screen(id, 'dock', selected?.id ?? 0, 'meta'),
    queryFn: get<MetaShip>(`/commanders/${id}/dock/${selected?.id}/meta`),
    enabled: !!selected,
  }))
  const meta = $derived(metaQuery.data?.meta ? metaQuery.data : undefined)
  const studied = $derived(meta?.tactics?.skills.find((s) => s.id === meta?.tactics?.skill_id))

  // the ship's skins: its default look, then the game's skins of its group;
  // owned are the ones it can wear
  interface ShipSkin {
    id: number
    name: string
    icon?: string
    owned: boolean
  }
  const skins = $derived.by((): ShipSkin[] => {
    if (!ship) return []
    const wearable = new Set(ship.skins.map((s) => s.id || ship.default_skin))
    const base = ship.skins[0]
    const list: ShipSkin[] = [{ id: ship.default_skin, name: base?.name || 'Default', icon: base?.icon, owned: true }]
    for (const skin of catalog.data ?? []) {
      if (skin.group === ship.group && skin.id !== ship.default_skin) list.push({ id: skin.id, name: skin.name, icon: skin.icon, owned: wearable.has(skin.id) })
    }
    // wearable skins the catalog doesn't list still show
    for (const skin of ship.skins.slice(1)) if (!list.some((s) => s.id === skin.id)) list.push({ ...skin, owned: true })
    return list
  })
  const worn = $derived(ship ? ship.skin_id || ship.default_skin : 0)

  // the skin on the stage: the worn one until another is clicked
  let previewID = $derived.by(() => (selected?.id, 0))
  const preview = $derived(skins.find((s) => s.id === previewID) ?? skins.find((s) => s.id === worn) ?? skins[0])
  const painting = $derived((preview?.icon ?? selected?.icon ?? '').split('/').pop() ?? '')

  let overlays = $state(true)
  const live2d = createLive2D()
  let editors = $state<ShipEditors>()

  const { mutation: write } = createCommanderWrite(() => id)

  function wear(skin: ShipSkin, commander: string) {
    if (!ship) return
    const body = { skin_id: skin.id === ship.default_skin ? 0 : skin.id }
    write.mutate({ path: `/dock/${ship.id}`, body, done: `${ship.name} wears ${skin.name}`, commander })
  }

  let duration = $state<'permanent' | '7' | '30'>('permanent')
  function grant(skin: ShipSkin, commander: string) {
    const timed = duration !== 'permanent'
    const drops = [timed ? { type: drop.skinTimed, id: skin.id, count: Number(duration) * 86400 } : { type: drop.skin, id: skin.id, count: 1 }]
    write.mutate({ path: '/grant', body: { drops }, done: timed ? `${skin.name} for ${duration} days` : `${skin.name} granted`, commander })
  }

  const statLabels: Record<string, string> = {
    durability: 'HP', cannon: 'FP', torpedo: 'TRP', antiaircraft: 'AA', air: 'AVI', reload: 'RLD', hit: 'ACC', dodge: 'EVA', speed: 'SPD', luck: 'LCK', antisub: 'ASW',
  }
  const skillColors = ['#9aa6bd', '#ff5c5c', '#3dc6ff', '#ffde38']
  const rarityTag: Record<number, [string, string]> = { 2: ['N', '#9aa3ad'], 3: ['R', '#4f9be8'], 4: ['SR', '#9b62e8'], 5: ['SSR', '#e0a420'], 6: ['UR', '#e0508f'] }
  // an overlay part that opens its editor
  const editable = 'w-full cursor-pointer border-0 bg-transparent text-left text-white hover:bg-white/6 hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]'
  const glass = 'rounded bg-[rgb(20_24_34/0.82)] shadow-[0_8px_24px_rgb(0_0_0/0.35)]'
  const shade = '[text-shadow:0_1px_2px_rgb(0_0_0/0.9),0_2px_12px_rgb(0_0_0/0.75)]'
</script>

{#snippet switcher()}
  <Seg
    label="View"
    bind:value={() => view, show}
    options={[
      { value: 'ships', label: 'Ships' },
      { value: 'skins', label: 'Skins' },
    ]}
  />
{/snippet}

<CommanderPage {id} title="Dock">
  {#snippet children(summary)}
    {#if view === 'skins'}
      <DockSkins {id} commander={summary.name} {switcher} />
    {:else}
    <section aria-label="Dock" class="flex flex-col gap-3.5">
      {#if selected && painting}
        <SkinArt {painting} name={preview?.name ?? selected.name} class="h-[720px]">
          {#snippet tools()}
            <button
              type="button"
              aria-pressed={overlays}
              class="flex h-12 cursor-pointer items-center gap-2.5 rounded-3xl border-0 bg-[rgb(30_33_40/0.88)] px-4 text-base text-white hover:bg-[rgb(50_54_64/0.95)]"
              onclick={() => (overlays = !overlays)}
            >
              <svg aria-hidden="true" viewBox="0 0 24 24" class="size-5" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
                {#if overlays}<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="3" />{:else}<path d="M3 3l18 18M10.6 5.1A10 10 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-3.2 4M6.6 6.6A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4.4-1" />{/if}
              </svg>
              Overlays
            </button>
          {/snippet}

          {#if overlays}
            <!-- keeps the overlays readable over light art -->
            <div aria-hidden="true" class="pointer-events-none absolute inset-y-0 left-0 w-[520px] bg-[linear-gradient(90deg,rgb(12_15_24/0.78)_0%,rgb(12_15_24/0.55)_60%,transparent_100%)]"></div>
            <div aria-hidden="true" class="pointer-events-none absolute inset-y-0 right-0 w-[380px] bg-[linear-gradient(270deg,rgb(12_15_24/0.6)_0%,transparent_100%)]"></div>
            <div class="absolute top-5 bottom-5 left-6 flex w-[350px] flex-col gap-2.5">
              <div class="flex flex-col gap-1">
                <span class="flex items-center gap-2 text-lg text-soft {shade}">
                  {#if rarityTag[selected.rarity]}<span class="rounded-[3px] px-2 text-[13px] text-white" style:background={rarityTag[selected.rarity][1]}>{rarityTag[selected.rarity][0]}</span>{/if}
                  {selected.type} · {selected.nation}
                </span>
                <h2 class="m-0 text-[40px] leading-tight {shade}">{selected.name}</h2>
                {#if preview}<span class="text-lg text-soft {shade}">{preview.name}</span>{/if}
                <span aria-label="{selected.star} of {selected.max_star} stars" class="text-xl tracking-[2px]">
                  <span class="text-[#ffd84a] [text-shadow:0_0_4px_#7a5600]">{'★'.repeat(selected.star)}</span><span class="text-white/30">{'★'.repeat(Math.max(0, selected.max_star - selected.star))}</span>
                </span>
                {#if selected.fleet}
                  <a href="/commanders/{id}/fleets?fleet={selected.fleet}" use:link class="ip-tag self-start bg-cyan text-white no-underline hover:brightness-110">{fleetName(selected.fleet).toUpperCase()}{ship?.slot ? ` #${ship.slot}` : ''}</a>
                {/if}
              </div>

              {#if ship}
                <div class={[glass, 'flex flex-col overflow-hidden']}>
                  <button type="button" aria-label="Edit the level of {ship.name}" class="{editable} flex flex-col gap-2.5 px-4 pt-3 pb-2" onclick={() => editors?.edit('level')}>
                    <span class="flex w-full items-baseline gap-2">
                      <span class="num text-[38px] leading-none">Lv.{ship.level}</span>
                      {#if ship.retrofit}
                        <span class={['ip-tag self-center text-white', ship.retrofit.full ? 'bg-cyan' : 'bg-charcoal/85']}>{ship.retrofit.full ? 'RETROFITTED' : `RETROFIT ${ship.retrofit.done}/${ship.retrofit.nodes}`}</span>
                      {/if}
                      <span class="num ml-auto text-lg text-soft">{ship.level >= ship.max_level ? 'MAX' : `${ship.exp}/${ship.exp_next}`}</span>
                    </span>
                    <span class="block h-1.5 w-full overflow-hidden rounded-full bg-white/12">
                      <span class="block h-full bg-cyan" style:width="{ship.level >= ship.max_level ? 100 : Math.min(100, (ship.exp / Math.max(1, ship.exp_next)) * 100)}%"></span>
                    </span>
                  </button>
                  <button type="button" aria-label="Edit the status of {ship.name}" class="{editable} flex flex-wrap items-center gap-x-4 gap-y-1.5 px-4 pt-1.5 pb-3 text-base" onclick={() => editors?.edit('status')}>
                    <span class="flex items-center gap-1.5" aria-label="Affinity {ship.intimacy / 100}">
                      <svg aria-hidden="true" viewBox="0 0 24 24" class={['size-5', ship.propose ? 'fill-[#ff5c93]' : 'fill-[#ff9cc0]/70']}><path d="M12 20s-7-4.4-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 10c0 5.6-7 10-7 10Z" /></svg>
                      <span class="num text-2xl text-[#ff9cc0]">{ship.intimacy / 100}</span>
                    </span>
                    <span class="text-soft">Morale <span class="num text-2xl text-white">{ship.energy}</span></span>
                  </button>
                </div>

                {#if ship.stats.length}
                  <dl aria-label="Stats" class={[glass, 'm-0 grid grid-cols-3 gap-x-4 px-4 py-2']}>
                    {#each ship.stats as stat (stat.name)}
                      <div class="flex items-baseline justify-between border-b border-white/8 py-px">
                        <dt class="ip-sub text-link">{statLabels[stat.name] ?? stat.name}</dt>
                        <dd class="num m-0 text-xl">{stat.value}</dd>
                      </div>
                    {/each}
                  </dl>
                {/if}

                {#if meta?.repair}
                  <button type="button" aria-label="Edit the META progress of {ship.name}" class={[glass, editable, 'flex items-center gap-3 px-4 py-2']} onclick={() => editors?.edit('meta')}>
                    <span class="ip-tag bg-[#6b4fd8] text-white">META</span>
                    <span class="text-soft">Repair <span class="num text-2xl text-white">{repairPercent(meta.repair)}%</span></span>
                    {#if studied}
                      <span class="min-w-0 flex-1 truncate text-right text-base">{studied.name || `Skill ${studied.id}`}</span>
                      <span class="num text-xl">Lv.{studied.level}</span>
                    {/if}
                  </button>
                {/if}

                <div class="mt-auto flex flex-col gap-2.5">
                  {#if ship.skills.length}
                    <button type="button" aria-label="Edit the skills of {ship.name}" class={[glass, editable, 'flex flex-col gap-0.5 px-3 py-2']} onclick={() => editors?.edit('skills')}>
                      {#each ship.skills as skill (skill.id)}
                        <span class="flex w-full items-center gap-2.5">
                          <span aria-hidden="true" class="h-5 w-1.5 rounded-[3px]" style:background={skillColors[skill.type] ?? skillColors[0]}></span>
                          <span class="min-w-0 flex-1 truncate text-base">{skill.name || `Skill ${skill.id}`}</span>
                          <span class="num text-xl">Lv.{skill.level}</span>
                        </span>
                      {/each}
                    </button>
                  {/if}
                  <ul aria-label="Gear" class={[glass, 'm-0 flex list-none justify-between gap-1.5 p-2.5']}>
                    {#each ship.gear as slot (slot.pos)}
                      {@const name = slot.equip ? `${slot.equip.name}${slot.equip.plus ? ` +${slot.equip.plus}` : ''}` : 'Empty'}
                      <li>
                        <button type="button" title={name} aria-label="Slot {slot.pos}: {name}" class="cursor-pointer rounded border-0 bg-transparent p-0 hover:shadow-[0_0_0_2px_var(--color-cyan)]" onclick={() => editors?.gear(slot.pos)}>
                          <Icon icon={slot.equip?.icon ?? ''} rarity={slot.equip?.rarity ?? 1} size={54} count={slot.equip?.plus ? `+${slot.equip.plus}` : undefined} name={slot.equip?.name ?? ''} label={false} />
                        </button>
                      </li>
                    {/each}
                  </ul>
                </div>
              {/if}
            </div>

            <div class={[glass, 'absolute top-[84px] right-5 bottom-5 flex w-[300px] flex-col']}>
              <div class="flex items-center gap-2 px-4 pt-3 pb-2">
                <h3 class="m-0 text-lg">Skins</h3>
                {#if ship}<span class="num text-lg text-soft">{skins.filter((s) => s.owned).length}/{skins.length}</span>{/if}
                {#if ship}
                  <button
                    type="button"
                    aria-label="Max the enhancement of {ship.name}"
                    title="Max enhancement"
                    class="ml-auto grid size-10 cursor-pointer place-items-center rounded-full border-0 bg-white/10 text-white hover:bg-white/20 disabled:cursor-not-allowed disabled:opacity-50"
                    disabled={write.isPending || ship.in_chapter}
                    onclick={() => write.mutate({ path: `/dock/${ship!.id}/strength`, done: `${ship!.name} enhanced`, commander: summary.name })}
                  >
                    <svg aria-hidden="true" viewBox="0 0 24 24" class="size-5" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5 12l7-7 7 7" /></svg>
                  </button>
                  <button
                    type="button"
                    aria-pressed={ship.locked}
                    aria-label={ship.locked ? 'Unlock' : 'Lock'}
                    title={ship.locked ? 'Locked' : 'Unlocked'}
                    class={['grid size-10 cursor-pointer place-items-center rounded-full border-0 text-white disabled:cursor-not-allowed disabled:opacity-50', ship.locked ? 'bg-cyan' : 'bg-white/10 hover:bg-white/20']}
                    disabled={write.isPending || (ship.locked && !!ship.propose)}
                    onclick={() => write.mutate({ path: `/dock/${ship!.id}`, body: { locked: !ship!.locked }, done: `${ship!.name} ${ship!.locked ? 'unlocked' : 'locked'}`, commander: summary.name })}
                  >
                    <svg aria-hidden="true" viewBox="0 0 24 24" class="size-5 fill-current">
                      {#if ship.locked}<path d="M7 10V8a5 5 0 0 1 10 0v2h1v11H6V10h1Zm2 0h6V8a3 3 0 0 0-6 0v2Z" />{:else}<path d="M7 10V8a5 5 0 0 1 9.6-2l-1.8.9A3 3 0 0 0 9 8v2h9v11H6V10h1Z" />{/if}
                    </svg>
                  </button>
                  <button
                    type="button"
                    aria-label="Remove {ship.name} from the dock"
                    title="Remove from the dock"
                    class="grid size-10 cursor-pointer place-items-center rounded-full border-0 bg-red/85 text-white hover:bg-red disabled:opacity-50"
                    disabled={write.isPending || ship.locked}
                    onclick={() => confirm(`Remove ${ship!.name} from the dock?`) && write.mutate({ method: 'DELETE', path: `/dock/${ship!.id}`, done: `${ship!.name} removed`, commander: summary.name })}
                  >
                    <svg aria-hidden="true" viewBox="0 0 24 24" class="size-5" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 11v6M14 11v6" /></svg>
                  </button>
                {/if}
              </div>
              <ul aria-label="Skins of {selected.name}" class="m-0 flex min-h-0 flex-1 list-none flex-col gap-1.5 overflow-y-auto px-2.5 pb-2.5 [scrollbar-width:thin]">
                {#each skins as skin (skin.id)}
                  <li>
                    <button
                      type="button"
                      aria-pressed={preview?.id === skin.id}
                      class={[
                        'flex w-full cursor-pointer items-center gap-2.5 rounded border-0 bg-white/6 p-1.5 text-left text-white hover:bg-white/12',
                        preview?.id === skin.id && 'bg-white/14 shadow-[inset_0_0_0_2px_var(--color-cyan)]',
                      ]}
                      onclick={() => (previewID = skin.id)}
                    >
                      <span class="relative block size-14 flex-none overflow-hidden rounded-[3px] bg-[#1b2238]">
                        {#if skin.icon}<img src="/api/assets/{cardArt(skin.icon)}.png" alt="" loading="lazy" class={['size-full object-cover object-top', !skin.owned && 'opacity-55 grayscale-[0.4]']} />{/if}
                        {#if live2d.has(skin.icon)}<Live2DBadge class="right-0 bottom-0 px-1 text-[9px]" />{/if}
                      </span>
                      <span class="flex min-w-0 flex-1 flex-col gap-1">
                        <span class="line-clamp-2 text-[15px] leading-tight">{skin.name}</span>
                        <span>
                          {#if skin.id === worn}<span class="ip-tag bg-cyan text-white">WORN</span>
                          {:else if skin.owned}<span class="ip-tag bg-charcoal/85 text-white">OWNED</span>
                          {:else}<span class="ip-tag bg-strip text-white">NOT OWNED</span>{/if}
                        </span>
                      </span>
                    </button>
                  </li>
                {/each}
              </ul>
              {#if preview && ship}
                <div class="flex flex-col gap-2.5 border-t border-white/10 p-3">
                  {#if !preview.owned}
                    <div role="group" aria-label="Duration" class="grid grid-cols-[1.3fr_1fr_1fr] gap-2">
                      {#each [{ value: 'permanent', label: 'Permanent' }, { value: '7', label: '7 days' }, { value: '30', label: '30 days' }] as option (option.value)}
                        <button type="button" class="ip-opt px-2 text-base" aria-pressed={duration === option.value} onclick={() => (duration = option.value as typeof duration)}>{option.label}</button>
                      {/each}
                    </div>
                    <button type="button" class="ip-btn h-[46px] text-[19px]" disabled={write.isPending} onclick={() => grant(preview!, summary.name)}>Grant to {summary.name}</button>
                  {:else}
                    <button type="button" class="ip-btn h-[46px] text-[19px]" disabled={write.isPending || preview.id === worn} onclick={() => wear(preview!, summary.name)}>
                      {preview.id === worn ? 'Worn' : `Wear on ${selected.name}`}
                    </button>
                  {/if}
                  {#if preview.id !== ship.default_skin}
                    <a href="/commanders/{id}/dock?skin={preview.id}" use:link class="ip-btn2 h-[42px] text-[17px] no-underline">Open in Skins</a>
                  {/if}
                </div>
              {/if}
            </div>
          {/if}
        </SkinArt>
        {#if ship}<ShipEditors bind:this={editors} {id} {ship} {meta} />{/if}
      {:else}
        <div class="ip-panel grid h-[720px] place-items-center text-lg text-dim">
          {#if dock.error}<span class="text-bad">{dock.error.message}</span>{:else if dock.data}No ship{:else}Loading…{/if}
        </div>
      {/if}

      <div class="flex flex-wrap items-center gap-3">
        {@render switcher()}
        <label class="relative flex items-center">
          <span class="sr-only">Find a ship</span>
          <svg aria-hidden="true" viewBox="0 0 24 24" class="absolute left-3 size-5" fill="none" stroke="#6b7386" stroke-width="2.4"><circle cx="11" cy="11" r="7" /><path d="M16 16l5 5" stroke-linecap="round" /></svg>
          <input type="search" class="ip-field w-[260px] pl-10" placeholder="Name, hull or faction" bind:value={query} />
        </label>
        <Seg
          label="Sort"
          bind:value={sort}
          options={[
            { value: 'level', label: 'Lv.' },
            { value: 'rarity', label: 'Rarity' },
            { value: 'new', label: 'New' },
          ]}
        />
        <label class="flex h-12 cursor-pointer items-center gap-2.5 rounded-3xl bg-[rgb(30_33_40/0.88)] px-4 text-base">
          <input type="checkbox" class="size-5 accent-cyan" bind:checked={lockedOnly} />
          Locked
        </label>
        <button
          type="button"
          class="ip-btn h-12 px-4 text-base"
          disabled={write.isPending || !dock.data?.ships.length}
          onclick={() => confirm('Max the enhancement of every ship?') && write.mutate({ path: '/dock/strength', done: 'Every ship enhanced', commander: summary.name })}
        >
          Max enhancement
        </button>
        {#if dock.data}
          <span class="ml-auto text-base text-soft"
            ><span class="num text-2xl text-white">{shown.length}</span> shown · <span class="num text-2xl text-white">{dock.data.ships.length}</span><span class="num text-xl text-dim">/{dock.data.capacity}</span> in the dock</span
          >
        {/if}
      </div>

      <Carousel
        items={shown}
        key={(one) => one.id}
        bind:selected={selectedID}
        label="Ships"
        width={140}
        height={195}
        empty={dock.data ? 'No match' : ''}
        cardLabel={(one) => `${one.name}, level ${one.level}`}
        cardClass="cursor-pointer rounded-[3px] border-0 bg-transparent p-0"
        selectedClass="shadow-[0_0_0_3px_var(--color-cyan)]"
      >
        {#snippet card(one)}
          <DockShipCard icon={one.icon} name={one.name} rarity={one.rarity} level={one.level} star={one.star} maxStar={one.max_star} locked={one.locked} married={one.married} fleet={one.fleet} />
        {/snippet}
      </Carousel>
    </section>
    {/if}
  {/snippet}
</CommanderPage>
