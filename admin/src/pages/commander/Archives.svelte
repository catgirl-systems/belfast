<script lang="ts">
  // The commander's archives, each section with its own actions: the ship
  // archive, the collection milestones, the medals, the memories, the look
  // (secretaries, oath ship, special secretary), the frames and wallpapers,
  // what it saw and unlocked with the music player, the handbook.
  import { untrack } from 'svelte'
  import { createQuery } from '@tanstack/svelte-query'
  import { createVirtualizer } from '@tanstack/svelte-virtual'
  import { createDebouncedValue } from '@tanstack/svelte-pacer'
  import CommanderPage from '../../components/CommanderPage.svelte'
  import Icon from '../../components/Icon.svelte'
  import Modal from '../../components/Modal.svelte'
  import Picker, { type PickerItem } from '../../components/Picker.svelte'
  import VirtualGrid from '../../components/VirtualGrid.svelte'
  import Seg from '../../components/Seg.svelte'
  import { bounded } from '../../lib/bounded'
  import { count } from '../../lib/format'
  import { fuzzy } from '../../lib/fuzzy'
  import { link } from '../../lib/router.svelte'
  import { shipFramed } from '../../lib/players'
  import { createCommanderWrite, get, keys } from '../../lib/query'
  import type { LifeArchives, LifeLook } from '../../lib/life'
  import {
    frameKinds,
    musicFavor,
    musicModes,
    musicNone,
    type ArchiveCollection,
    type ArchiveCryptolalia,
    type ArchiveDetails,
    type ArchiveFrame,
    type ArchiveMedal,
    type ArchiveMemory,
    type ArchiveMemoryGroup,
    type ArchiveShip,
    type ArchiveStep,
    type ArchiveTier,
  } from '../../lib/archives'

  let { id }: { id: number } = $props()

  const archives = createQuery(() => ({
    queryKey: keys.screen(id, 'archives'),
    queryFn: get<LifeArchives>(`/commanders/${id}/archives`),
  }))
  const details = createQuery(() => ({
    queryKey: keys.screen(id, 'archives', 'details'),
    queryFn: get<ArchiveDetails>(`/commanders/${id}/archives/details`),
  }))
  const data = $derived(archives.data)
  const more = $derived(details.data)

  const { mutation: edit, run } = createCommanderWrite(() => id)
  const busy = $derived(edit.isPending)

  // --- ship archive: hundreds of groups, fuzzy-searched and virtualized
  let query = $state('')
  const [debounced] = createDebouncedValue(() => query, { wait: 150 })
  const sortedShips = $derived([...(more?.ships ?? [])].sort((a, b) => b.level - a.level || a.group - b.group))
  const ships = $derived(fuzzy(sortedShips, debounced(), (ship) => [ship.name, ship.group]))

  const rowHeight = 59
  let scroller = $state<HTMLDivElement | null>(null)
  const virtualizer = createVirtualizer<HTMLDivElement, HTMLDivElement>({
    count: 0,
    getScrollElement: () => scroller,
    estimateSize: () => rowHeight,
    overscan: 6,
  })
  $effect(() => {
    const count = ships.length
    const element = scroller
    untrack(() => {
      $virtualizer.setOptions({ count, getScrollElement: () => element })
      $virtualizer.measure()
    })
  })
  $effect(() => {
    void debounced()
    scroller?.scrollTo({ top: 0 })
  })
  const columns = 'grid grid-cols-[minmax(0,1fr)_68px_80px_64px_56px_84px_52px_44px] items-center'

  // the archive entry edited in place
  let editingShip = $state<number | null>(null)
  let level = $state(1)
  let affinity = $state(0)
  let star = $state(1)
  let married = $state(false)
  let liked = $state(false)
  let retrofit = $state(false)
  function startShip(ship: ArchiveShip) {
    level = ship.level
    affinity = ship.intimacy / 100
    star = ship.star
    married = ship.married
    liked = ship.liked
    retrofit = ship.retrofit
    editingShip = ship.group
  }
  function saveShip(event: SubmitEvent, ship: ArchiveShip) {
    event.preventDefault()
    editingShip = null
    const intimacy = Math.round(affinity * 100)
    if (level === ship.level && intimacy === ship.intimacy && star === ship.star && married === ship.married && liked === ship.liked && retrofit === ship.retrofit) return
    run(`/archives/ships/${ship.group}`, `${ship.name} saved`, { level, intimacy, star, married, liked, retrofit }, 'PUT')
  }
  const escapeShip = (event: KeyboardEvent) => event.key === 'Escape' && (editingShip = null)
  function removeShip(ship: ArchiveShip) {
    if (confirm(`Take ${ship.name} out of the archives?`)) run(`/archives/ships/${ship.group}`, `${ship.name} removed`, undefined, 'DELETE')
  }
  let groupPicker = $state<Picker>()
  const groupItems = $derived<PickerItem[]>((more?.groups ?? []).map((g) => ({ id: g.group, name: g.name, icon: g.icon, rarity: g.rarity, ship: true, sub: `#${g.group}` })))
  const addGroup = (item: PickerItem) => run(`/archives/ships/${item.id}`, `${item.name} added`)

  // --- collection milestones and medals: steps claimed in order
  function toggleStep(collection: ArchiveCollection, step: ArchiveStep) {
    run(`/archives/collections/${collection.id}/${step.index}`, step.claimed ? `${collection.name}: rewards from ${step.index} unclaimed` : `${collection.name}: rewards up to ${step.index} claimed`, { claimed: !step.claimed })
  }
  function toggleTier(medal: ArchiveMedal, tier: ArchiveTier, i: number) {
    run(`/archives/medal-tiers/${tier.id}`, tier.claimed ? `${medal.name}: tiers from ${i + 1} unclaimed` : `${medal.name}: tiers up to ${i + 1} claimed`, { claimed: !tier.claimed })
  }
  const shownTier = (medal: ArchiveMedal, display: number[]) => medal.tiers.find((tier) => display.includes(tier.id))
  function toggleShown(medal: ArchiveMedal, display: number[]) {
    const shown = shownTier(medal, display)
    const ids = medal.tiers.map((tier) => tier.id)
    if (shown) {
      run('/archives/medal-display', `${medal.name} hidden`, { ids: display.filter((d) => !ids.includes(d)) }, 'PUT')
      return
    }
    const best = [...medal.tiers].reverse().find((tier) => tier.claimed)
    if (best) run('/archives/medal-display', `${medal.name} shown`, { ids: [...display, best.id] }, 'PUT')
  }
  const current = (medal: ArchiveMedal) => medal.tiers.find((tier) => !tier.claimed) ?? medal.tiers[medal.tiers.length - 1]

  // --- memories: a group's memories in a dialog
  let memoryQuery = $state('')
  const memoryGroups = $derived(fuzzy(more?.memories ?? [], memoryQuery, (group) => [group.title, group.id]))
  const memoryTotals = $derived((more?.memories ?? []).reduce((sum, group) => [sum[0] + group.seen, sum[1] + group.total], [0, 0]))
  let memoryModal = $state<Modal>()
  let openGroup = $state<ArchiveMemoryGroup | null>(null)
  const memories = createQuery(() => ({
    queryKey: keys.screen(id, 'archives', 'memories', openGroup?.id ?? 0),
    queryFn: get<ArchiveMemory[]>(`/commanders/${id}/archives/memories/${openGroup?.id ?? 0}`),
    enabled: openGroup !== null,
  }))
  function showGroup(group: ArchiveMemoryGroup) {
    openGroup = group
    memoryModal?.show()
  }
  const markGroup = (group: ArchiveMemoryGroup, seen: boolean) => run(`/archives/memories/${group.id}`, seen ? `${group.title}: every memory seen` : `${group.title}: memories cleared`, { memory: 0, seen })
  const markMemory = (group: ArchiveMemoryGroup, memory: ArchiveMemory) => run(`/archives/memories/${group.id}`, memory.seen ? `${memory.title} cleared` : `${memory.title} seen`, { memory: memory.id, seen: !memory.seen })

  const markAllMemories = () => run('/archives/memories/all', 'Every memory seen')

  // --- cryptolalia: unlocked for free, whether on sale or not
  const cryptolalia = createQuery(() => ({
    queryKey: keys.screen(id, 'archives', 'cryptolalia'),
    queryFn: get<ArchiveCryptolalia[]>(`/commanders/${id}/archives/cryptolalia`),
  }))
  const unlockedStories = $derived((cryptolalia.data ?? []).filter((story) => story.unlocked).length)
  const toggleStory = (story: ArchiveCryptolalia) => run(`/archives/cryptolalia/${story.id}`, story.unlocked ? `${story.name} locked` : `${story.name} unlocked`, { unlocked: !story.unlocked })

  // --- look: every change saved at once with the rest of the look
  const ownShips = $derived(new Map((data?.options.ships ?? []).map((ship) => [ship.id, ship])))
  const saveLook = (look: LifeLook, done: string) => run('/archives/look', done, look)
  let secretaryPicker = $state<Picker>()
  let oathPicker = $state<Picker>()
  let specialPicker = $state<Picker>()
  const secretaryItems = $derived<PickerItem[]>(
    (data?.options.ships ?? []).map((ship) => ({ id: ship.id, name: ship.name, icon: ship.icon, rarity: ship.rarity ?? 2, ship: true, sub: `Lv.${ship.level ?? 1}`, disabled: data?.look.secretaries.includes(ship.id) })),
  )
  const oathItems = $derived<PickerItem[]>([
    { id: 0, name: 'None' },
    ...(data?.options.oath ?? []).map((ship) => ({ id: ship.id, name: ship.name, icon: ship.icon, rarity: ship.rarity ?? 2, ship: true, sub: `Lv.${ship.level ?? 1}` })),
  ])
  const specialItems = $derived<PickerItem[]>([{ id: 0, name: 'None' }, ...(data?.options.specials ?? []).map((option) => ({ id: option.id, name: option.name, icon: option.icon }))])
  const randomOptions = [
    { value: -1, label: 'Off' },
    { value: 1, label: 'Frequent' },
    { value: 2, label: 'Locked' },
    { value: 3, label: 'Custom pool' },
  ]
  const randomValue = (look: LifeLook) => (look.random_enabled ? look.random_mode || 1 : -1)
  function pickRandom(look: LifeLook, value: number) {
    const label = randomOptions.find((option) => option.value === value)?.label ?? ''
    saveLook({ ...look, random_enabled: value > 0, random_mode: value > 0 ? value : look.random_mode }, `Random secretary ${label.toLowerCase()}`)
  }

  // --- frames and wallpapers
  let framePicker = $state<Picker>()
  const kindOf = (type: number) => frameKinds.find((kind) => kind.type === type)
  const frameKey = (frame: { type: number; id: number }) => frame.type * 1_000_000 + frame.id
  const ownedFrames = $derived(new Set((more?.frames ?? []).map(frameKey)))
  const frameItems = $derived<PickerItem[]>(
    (more?.catalog ?? []).map((frame) => ({ id: frameKey(frame), name: frame.name, icon: frame.icon, sub: kindOf(frame.type)?.label, disabled: ownedFrames.has(frameKey(frame)) })),
  )
  const frameFacets = [{ label: 'Kind', value: (item: PickerItem) => item.sub }]
  function addFrame(item: PickerItem) {
    const frame = more?.catalog.find((f) => frameKey(f) === item.id)
    if (frame) run('/archives/frames', `${frame.name} given`, { type: frame.type, id: frame.id })
  }
  const wear = (frame: ArchiveFrame) => run('/archives/frames/wear', frame.worn ? `${frame.name} taken off` : `${frame.name} worn`, { type: frame.type, id: frame.worn ? 0 : frame.id })
  function removeFrame(frame: ArchiveFrame) {
    if (confirm(`Take ${frame.name} back?`)) run('/archives/frames/remove', `${frame.name} removed`, { type: frame.type, id: frame.id })
  }

  // --- seen lists and the music player
  function confirmSeen(entry: { key: string; label: string }) {
    if (confirm(`Clear ${entry.label.toLowerCase()}?`)) run(`/archives/seen/${entry.key}/reset`, `${entry.label} cleared`)
  }
  let editingMusic = $state(false)
  let album = $state(0)
  let mode = $state(0)
  function startMusic(music: ArchiveDetails['music']) {
    album = music.album
    mode = music.mode
    editingMusic = true
  }
  function saveMusic(event: SubmitEvent, music: ArchiveDetails['music']) {
    event.preventDefault()
    editingMusic = false
    if (album !== music.album || mode !== music.mode) run('/archives/music', 'Music player set', { album, mode })
  }
  const escapeMusic = (event: KeyboardEvent) => event.key === 'Escape' && (editingMusic = false)
  const albumName = (music: ArchiveDetails['music'], no: number) =>
    no === musicNone ? 'Off' : no === musicFavor ? 'Liked songs' : (music.albums.find((a) => a.id === no)?.name ?? `Song ${no}`)
</script>

{#snippet toggle(on: boolean, label: string, onclick: () => void, disabled = false)}
  <button type="button" aria-pressed={on} aria-label={label} class={['ip-tag cursor-pointer border-0 hover:shadow-[inset_0_0_0_2px_var(--color-cyan)] disabled:cursor-default disabled:opacity-50', on ? 'bg-[#2f9a55] text-white' : 'bg-white/10 text-dim']} {disabled} {onclick}>{on ? 'Yes' : 'No'}</button>
{/snippet}

{#snippet steps(items: { key: number; claimed: boolean; reached: boolean; label: string; text: string; award?: ArchiveStep['award'] }[], onclick: (i: number) => void)}
  <span class="flex flex-wrap gap-1.5">
    {#each items as item, i (item.key)}
      <button
        type="button"
        aria-pressed={item.claimed}
        aria-label={item.label}
        title={item.label}
        class={['flex cursor-pointer items-center gap-1.5 rounded-[3px] border-0 px-1.5 py-1 text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)] disabled:cursor-not-allowed disabled:opacity-40', item.claimed ? 'bg-[#2f9a55]' : 'bg-black/30']}
        disabled={busy || (!item.claimed && !item.reached)}
        onclick={() => onclick(i)}
      >
        {#if item.award}<Icon icon={item.award.icon} rarity={item.award.rarity} ship={shipFramed(item.award.type)} name={item.award.name} count={item.award.count} size={32} label={false} />{/if}
        <span class="num text-[15px]">{item.text}</span>
      </button>
    {/each}
  </span>
{/snippet}

<CommanderPage {id} title="Archives">
  {#snippet children()}
    {#if data && more}
      {@const look = data.look}
      {@const oath = data.options.oath.find((ship) => ship.id === look.marry_ship)}
      {@const special = data.options.specials.find((option) => option.id === look.child_display)}
      <div class="flex flex-wrap items-start gap-5">
        <div class="flex min-w-[600px] flex-[1.7] flex-col gap-5">
          <section aria-label="Ship archive" class="ip-panel overflow-hidden">
            <div class="ip-head items-center">
              <h2>Ship archive</h2>
              <span class="num text-[22px] text-soft">{more.ships.length}</span>
              <label class="relative ml-auto">
                <span class="sr-only">Find a ship</span>
                <svg aria-hidden="true" viewBox="0 0 24 24" class="absolute top-1/2 left-3 size-[18px] -translate-y-1/2" fill="none" stroke="#6b7386" stroke-width="2.4"><circle cx="11" cy="11" r="7" /><path d="M16 16l5 5" stroke-linecap="round" /></svg>
                <input type="search" class="ip-field h-10 w-[200px] pl-[38px]" placeholder="Ship" bind:value={query} />
              </label>
              <button type="button" class="ip-btn2 ip-sm" disabled={busy || !more.groups.length} onclick={() => groupPicker?.show()}>Add</button>
            </div>
            <div role="table" aria-label="Ship archive" aria-rowcount={ships.length + 1}>
              <div role="row" class={[columns, 'mx-2.5 pt-2 pb-1']}>
                <span role="columnheader" class="ip-sub px-2.5">Ship</span>
                <span role="columnheader" class="ip-sub px-1.5 whitespace-nowrap">Best Lv.</span>
                <span role="columnheader" class="ip-sub px-1.5">Affinity</span>
                <span role="columnheader" class="ip-sub px-1.5">Stars</span>
                <span role="columnheader" class="ip-sub px-1.5">Oath</span>
                <span role="columnheader" class="ip-sub px-1.5">Retrofit</span>
                <span role="columnheader" class="ip-sub px-1.5">Liked</span>
                <span role="columnheader" class="sr-only">Actions</span>
              </div>
              <div bind:this={scroller} role="rowgroup" class="max-h-[560px] overflow-y-auto px-2.5 pb-3">
                {#if ships.length === 0}
                  <p class="m-0 px-2.5 py-4 text-soft">{more.ships.length ? 'No match' : 'No ships'}</p>
                {:else}
                  <div class="relative w-full" style:height="{$virtualizer.getTotalSize()}px">
                    {#each $virtualizer.getVirtualItems() as row (row.key)}
                      {@const ship = ships[row.index]}
                      {#if ship}
                        <div role="row" aria-rowindex={row.index + 2} class="absolute top-0 left-0 w-full pt-[5px]" style:height="{rowHeight}px" style:transform="translateY({row.start}px)">
                          {#if editingShip === ship.group}
                            <form class={[columns, 'ip-sel h-full rounded-[3px] bg-white/10']} onsubmit={(e) => saveShip(e, ship)}>
                              <span role="rowheader" class="flex min-w-0 items-center gap-3 px-2.5">
                                <Icon icon={ship.icon} rarity={ship.rarity} ship name={ship.name} size={44} />
                                <span class="min-w-0 flex-1 truncate">{ship.name}</span>
                                <button type="submit" class="ip-btn ip-sm h-9 px-3" disabled={busy}>Save</button>
                              </span>
                              <span role="cell" class="px-1">
                                <!-- svelte-ignore a11y_autofocus -->
                                <input class="ip-field num h-9 w-full min-w-0 px-2 text-lg" type="number" aria-label="{ship.name} best level" onkeydown={escapeShip} autofocus bind:value={level} use:bounded={{ min: ship.min_level, max: ship.max_level }} />
                              </span>
                              <span role="cell" class="px-1">
                                <input class="ip-field num h-9 w-full min-w-0 px-2 text-lg" type="number" step="0.01" aria-label="{ship.name} affinity" onkeydown={escapeShip} bind:value={affinity} use:bounded={{ min: ship.min_intimacy / 100, max: (married ? ship.max_intimacy_oath : ship.max_intimacy) / 100, step: 0.01 }} />
                              </span>
                              <span role="cell" class="px-1">
                                <input class="ip-field num h-9 w-full min-w-0 px-2 text-lg" type="number" aria-label="{ship.name} stars" onkeydown={escapeShip} bind:value={star} use:bounded={{ min: ship.min_star, max: ship.max_star }} />
                              </span>
                              <span role="cell" class="px-1.5">{@render toggle(married, `${ship.name} oath`, () => (married = !married), ship.min_married)}</span>
                              <span role="cell" class="px-1.5">{@render toggle(retrofit, `${ship.name} retrofit`, () => (retrofit = !retrofit), !ship.can_retrofit)}</span>
                              <span role="cell" class="px-1.5">{@render toggle(liked, `${ship.name} liked`, () => (liked = !liked))}</span>
                              <span role="cell" class="flex justify-end pr-2">
                                <button type="button" aria-label="Cancel" class="grid size-8 cursor-pointer place-items-center rounded-full border-0 bg-white/10 text-white hover:bg-white/20" onclick={() => (editingShip = null)}>
                                  <svg aria-hidden="true" viewBox="0 0 24 24" class="size-4" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18" /></svg>
                                </button>
                              </span>
                            </form>
                          {:else}
                            <div class={[columns, 'h-full rounded-[3px] bg-white/6']}>
                              <span role="rowheader" class="flex min-w-0 items-center gap-3 px-2.5">
                                <Icon icon={ship.icon} rarity={ship.rarity} ship name={ship.name} size={44} />
                                {#if ship.ship}
                                  <a href="/commanders/{id}/dock?ship={ship.ship}" use:link class="truncate text-link">{ship.name}</a>
                                {:else}
                                  <span class="truncate">{ship.name}</span>
                                {/if}
                              </span>
                              <button type="button" role="cell" aria-label="Edit {ship.name}" class="col-span-6 grid h-full cursor-pointer grid-cols-[68px_80px_64px_56px_84px_52px] items-center rounded-[3px] border-0 bg-transparent p-0 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => startShip(ship)}>
                                <span class="num px-1.5 text-[21px]">{ship.level}</span>
                                <span class="num px-1.5 text-[21px]">{count(Math.floor(ship.intimacy / 100))}</span>
                                <span class="px-1.5 whitespace-nowrap text-[#ffd84a]" aria-label="{ship.star} stars"><span class="num text-[21px]">{ship.star}</span> ★</span>
                                <span class="px-1.5"><span class={['ip-tag', ship.married ? 'bg-[#e0559b] text-white' : 'bg-white/10 text-dim']}>{ship.married ? 'Oath' : 'No'}</span></span>
                                <span class="px-1.5"><span class={['ip-tag', ship.retrofit ? 'bg-cyan text-[#0b2a3a]' : 'bg-white/10 text-dim']}>{ship.retrofit ? 'Done' : 'No'}</span></span>
                                <span class={['px-1.5 text-lg', ship.liked ? 'text-[#ff7aa8]' : 'text-white/25']} aria-label={ship.liked ? 'Liked' : 'Not liked'}>♥</span>
                              </button>
                              <span role="cell" class="flex justify-end pr-2">
                                <button type="button" aria-label="Remove {ship.name}" class="grid size-8 cursor-pointer place-items-center rounded-full border-0 bg-white/10 text-white hover:bg-red disabled:cursor-default disabled:opacity-30 disabled:hover:bg-white/10" disabled={busy || ship.ship !== 0} onclick={() => removeShip(ship)}>×</button>
                              </span>
                            </div>
                          {/if}
                        </div>
                      {/if}
                    {/each}
                  </div>
                {/if}
              </div>
            </div>
            <Picker bind:this={groupPicker} trigger={false} label="Ship" items={groupItems} placeholder="Name or id" value={null} onselect={addGroup} />
          </section>

          <section aria-label="Collection rewards" class="ip-panel overflow-hidden">
            <div class="ip-head"><h2>Collection rewards</h2></div>
            <ul class="m-0 flex list-none flex-col gap-1.5 p-2.5">
              {#each more.collections as collection (collection.id)}
                <li class="ip-row grid grid-cols-[minmax(0,220px)_80px_minmax(0,1fr)] items-center gap-3 px-3 py-1.5">
                  <span class="truncate">{collection.name}</span>
                  <span class="whitespace-nowrap text-[#ffd84a]"><span class="num text-xl">{collection.stars}</span> ★</span>
                  {@render steps(
                    collection.steps.map((step) => ({ key: step.index, claimed: step.claimed, reached: collection.stars >= step.level, label: `${step.level} stars: ${step.award.name ?? ''} x${step.award.count}`, text: `${step.level}★`, award: step.award })),
                    (i) => toggleStep(collection, collection.steps[i]),
                  )}
                </li>
              {/each}
            </ul>
          </section>

          <section aria-label="Medals" class="ip-panel overflow-hidden">
            <div class="ip-head items-center">
              <h2>Medals</h2>
              <span class="ml-auto text-sm text-soft">Shown <span class="num text-[19px] text-white">{more.display.length}/{more.display_max}</span></span>
            </div>
            {#if more.medals.length === 0}
              <p class="m-0 px-5 py-6 text-soft">No medal progress</p>
            {:else}
              <ul class="m-0 flex list-none flex-col gap-1.5 p-2.5">
                {#each more.medals as medal (medal.id)}
                  {@const tier = current(medal)}
                  {@const shown = shownTier(medal, more.display)}
                  <li class="ip-row grid grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_auto] items-center gap-4 px-3 py-2">
                    <span class="flex min-w-0 flex-col gap-1.5">
                      <span class="truncate">{medal.name || `#${medal.id}`}</span>
                      <span class="flex items-center gap-2">
                        <span class="num min-w-[96px] text-lg">{count(tier.progress)}/{count(tier.target)}</span>
                        <span aria-hidden="true" class="h-1.5 flex-1 rounded-[3px] bg-black/40"><span class="block h-full rounded-[3px] bg-cyan" style:width="{Math.min(100, (tier.progress / Math.max(1, tier.target)) * 100)}%"></span></span>
                      </span>
                    </span>
                    {@render steps(
                      medal.tiers.map((t, i) => ({ key: t.id, claimed: t.claimed, reached: t.reached, label: `Tier ${i + 1}: ${count(t.target)}`, text: count(t.target) })),
                      (i) => toggleTier(medal, medal.tiers[i], i),
                    )}
                    <button
                      type="button"
                      aria-pressed={!!shown}
                      aria-label="Show {medal.name} on the profile"
                      class={['ip-tag cursor-pointer border-0 hover:shadow-[inset_0_0_0_2px_var(--color-cyan)] disabled:cursor-default disabled:opacity-40', shown ? 'bg-cyan text-[#0b2a3a]' : 'bg-white/10 text-dim']}
                      disabled={busy || medal.hidden || (!shown && (!medal.tiers.some((t) => t.claimed) || more.display.length >= more.display_max))}
                      onclick={() => toggleShown(medal, more.display)}>Shown</button
                    >
                  </li>
                {/each}
              </ul>
            {/if}
          </section>

          <section aria-label="Memories" class="ip-panel overflow-hidden">
            <div class="ip-head items-center">
              <h2>Memories</h2>
              {#if more.memories}<span class="text-[15px] text-soft"><span class="num text-xl text-white">{count(memoryTotals[0])}</span> / <span class="num">{count(memoryTotals[1])}</span> seen</span>{/if}
              <label class="relative ml-auto">
                <span class="sr-only">Find a memory group</span>
                <svg aria-hidden="true" viewBox="0 0 24 24" class="absolute top-1/2 left-3 size-[18px] -translate-y-1/2" fill="none" stroke="#6b7386" stroke-width="2.4"><circle cx="11" cy="11" r="7" /><path d="M16 16l5 5" stroke-linecap="round" /></svg>
                <input type="search" class="ip-field h-10 w-[200px] pl-[38px]" placeholder="Group" bind:value={memoryQuery} disabled={!more.memories} />
              </label>
              <button type="button" class="ip-btn2 ip-sm" disabled={busy || !more.memories || memoryTotals[0] === memoryTotals[1]} onclick={markAllMemories}>See all</button>
            </div>
            {#if more.memories}
              <VirtualGrid items={memoryGroups} rowHeight={44} key={(g) => g.id} label="Memory groups" class="max-h-[440px] px-2.5 pt-2.5 pb-2.5">
                {#snippet cell(group)}
                  <div class="ip-row flex items-center gap-3 pr-2 pl-1">
                    <button type="button" class="flex h-full min-w-0 flex-1 cursor-pointer items-center gap-3 rounded-[3px] border-0 bg-transparent px-2 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => showGroup(group)}>
                      <span class="min-w-0 flex-1 truncate text-[16px]">{group.title || `#${group.id}`}</span>
                      <span class={['num text-lg', group.seen === group.total ? 'text-ok' : 'text-soft']}>{group.seen} / {group.total}</span>
                    </button>
                    <button type="button" class="ip-btn2 ip-sm h-8 px-2.5 text-sm" disabled={busy || group.seen === group.total} onclick={() => markGroup(group, true)}>See all</button>
                    <button type="button" class="ip-btn2 ip-sm h-8 px-2.5 text-sm" disabled={busy || group.seen === 0} onclick={() => markGroup(group, false)}>Clear</button>
                  </div>
                {/snippet}
                {#snippet empty()}
                  <div class="ip-row px-3 py-2.5 text-dim">No match</div>
                {/snippet}
              </VirtualGrid>
            {:else}
              <p class="m-0 px-5 py-4 text-bad">{more.memories_error}</p>
            {/if}
            <Modal bind:this={memoryModal} title={openGroup?.title || 'Memories'} width="w-[min(620px,92vw)]">
              {#if memories.data && openGroup}
                {@const group = openGroup}
                <ul class="m-0 flex list-none flex-col gap-1.5 p-0">
                  {#each memories.data as memory (memory.id)}
                    <li class="ip-row flex items-center gap-3 px-3 py-1.5">
                      <span class="min-w-0 flex-1 truncate">{memory.title || `#${memory.id}`}</span>
                      <button
                        type="button"
                        aria-pressed={memory.seen}
                        aria-label="{memory.title} seen"
                        class={['ip-tag cursor-pointer border-0 hover:shadow-[inset_0_0_0_2px_var(--color-cyan)] disabled:cursor-default disabled:opacity-60', memory.seen ? 'bg-[#2f9a55] text-white' : 'bg-white/10 text-dim']}
                        disabled={busy || memory.open}
                        onclick={() => markMemory(group, memory)}>{memory.open ? 'Open' : memory.seen ? 'Seen' : 'Not seen'}</button
                      >
                    </li>
                  {/each}
                </ul>
              {:else if memories.error}
                <p class="m-0 text-bad">{memories.error.message}</p>
              {:else}
                <p class="m-0 text-dim">Loading</p>
              {/if}
            </Modal>
          </section>

          <section aria-label="Cryptolalia" class="ip-panel overflow-hidden">
            <div class="ip-head items-center">
              <h2>Cryptolalia</h2>
              {#if cryptolalia.data}<span class="text-[15px] text-soft"><span class="num text-xl text-white">{count(unlockedStories)}</span> / <span class="num">{count(cryptolalia.data.length)}</span> unlocked</span>{/if}
              <button type="button" class="ip-btn2 ip-sm ml-auto" disabled={busy || !cryptolalia.data || unlockedStories === cryptolalia.data.length} onclick={() => run('/archives/cryptolalia/all', 'Every story unlocked')}>Unlock all</button>
            </div>
            {#if cryptolalia.data}
              <VirtualGrid items={cryptolalia.data} rowHeight={44} key={(s) => s.id} label="Cryptolalia stories" class="max-h-[440px] px-2.5 pt-2.5 pb-2.5">
                {#snippet cell(story)}
                  <div class="ip-row flex items-center gap-3 px-3">
                    <span class="min-w-0 flex-1 truncate text-[16px]">{story.name || `#${story.id}`}</span>
                    <button
                      type="button"
                      aria-pressed={story.unlocked}
                      aria-label="{story.name} unlocked"
                      class={['ip-tag cursor-pointer border-0 hover:shadow-[inset_0_0_0_2px_var(--color-cyan)] disabled:cursor-default disabled:opacity-60', story.unlocked ? 'bg-[#2f9a55] text-white' : 'bg-white/10 text-dim']}
                      disabled={busy}
                      onclick={() => toggleStory(story)}>{story.unlocked ? 'Unlocked' : 'Locked'}</button
                    >
                  </div>
                {/snippet}
                {#snippet empty()}
                  <div class="ip-row px-3 py-2.5 text-dim">No story</div>
                {/snippet}
              </VirtualGrid>
            {:else if cryptolalia.error}
              <p class="m-0 px-5 py-4 text-bad">{cryptolalia.error.message}</p>
            {/if}
          </section>
        </div>

        <div class="flex w-[360px] min-w-[330px] flex-1 flex-col gap-5">
          <section aria-label="Look" class="ip-panel overflow-hidden">
            <div class="ip-head items-center">
              <h2 class="text-[22px]">Look</h2>
              <button type="button" class="ip-btn2 ip-sm ml-auto" disabled={busy || look.secretaries.length >= (look.max_secretaries ?? 1)} onclick={() => secretaryPicker?.show()}>Add secretary</button>
            </div>
            <div class="flex flex-col gap-3 p-2.5">
              <ol class="m-0 flex list-none flex-col gap-1.5 p-0">
                {#each look.secretaries as secretary, i (secretary)}
                  {@const ship = ownShips.get(secretary)}
                  {@const skin = more.skins[secretary]}
                  <li class="ip-row flex items-center gap-2.5 px-2.5 py-1.5">
                    <span class="num w-[18px] text-lg text-dim">{i + 1}</span>
                    <Icon icon={ship?.icon} ship rarity={ship?.rarity ?? 2} size={40} name={ship?.name ?? ''} />
                    <span class="flex min-w-0 flex-1 flex-col">
                      <a href="/commanders/{id}/dock?ship={secretary}" use:link class="truncate text-link">{ship?.name ?? `#${secretary}`}</a>
                      {#if skin}<a href="/commanders/{id}/dock?skin={skin.skin}" use:link class="truncate text-[13px] text-link">{skin.name || `Skin ${skin.skin}`}</a>{/if}
                    </span>
                    {#if ship?.level}<span class="text-sm text-dim">Lv.<span class="num text-lg text-soft">{ship.level}</span></span>{/if}
                    <button
                      type="button"
                      aria-label="Remove {ship?.name ?? secretary}"
                      class="grid size-8 cursor-pointer place-items-center rounded-full border-0 bg-white/10 text-white hover:bg-red disabled:cursor-default disabled:opacity-30 disabled:hover:bg-white/10"
                      disabled={busy || look.secretaries.length <= 1}
                      onclick={() => saveLook({ ...look, secretaries: look.secretaries.filter((s) => s !== secretary) }, `${ship?.name ?? secretary} removed`)}>×</button
                    >
                  </li>
                {/each}
              </ol>
              <div class="flex flex-col gap-1.5">
                <span class="ip-sub">Random secretary</span>
                <Seg label="Random secretary" options={randomOptions} value={randomValue(look)} onchange={(value) => pickRandom(look, value)} />
              </div>
              <div class="grid grid-cols-2 gap-2">
                <button type="button" aria-label="Change the oath ship shown" disabled={busy} class="ip-row flex cursor-pointer items-center gap-2.5 border-0 px-3 py-2 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => oathPicker?.show()}>
                  {#if oath}<Icon icon={oath.icon} ship rarity={oath.rarity ?? 2} size={36} name={oath.name} />{/if}
                  <span class="flex min-w-0 flex-col">
                    <span class="ip-sub">Oath ship</span>
                    <span class={['truncate', !oath && 'text-dim']}>{oath?.name ?? 'None'}</span>
                  </span>
                </button>
                <button type="button" aria-label="Change the special secretary" disabled={busy} class="ip-row flex cursor-pointer flex-col border-0 px-3 py-2 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => specialPicker?.show()}>
                  <span class="ip-sub">Special secretary</span>
                  <span class={['truncate', !special && 'text-dim']}>{special?.name ?? 'None'}</span>
                </button>
              </div>
            </div>
            <Picker bind:this={secretaryPicker} trigger={false} label="Secretary" items={secretaryItems} placeholder="Name or id" value={null} onselect={(item) => saveLook({ ...look, secretaries: [...look.secretaries, item.id] }, `${item.name} added`)} />
            <Picker bind:this={oathPicker} trigger={false} label="Oath ship" items={oathItems} placeholder="Name or id" value={look.marry_ship} onselect={(item) => item.id !== look.marry_ship && saveLook({ ...look, marry_ship: item.id }, `Oath ship ${item.name}`)} />
            <Picker bind:this={specialPicker} trigger={false} label="Special secretary" items={specialItems} placeholder="Name or id" value={look.child_display} onselect={(item) => item.id !== look.child_display && saveLook({ ...look, child_display: item.id }, `Special secretary ${item.name}`)} />
          </section>

          <section aria-label="Frames" class="ip-panel overflow-hidden">
            <div class="ip-head items-center">
              <h2 class="text-[22px]">Frames</h2>
              <span class="num text-[22px] text-soft">{more.frames.length}</span>
              <button type="button" class="ip-btn2 ip-sm ml-auto" disabled={busy} onclick={() => framePicker?.show()}>Add</button>
            </div>
            <ul class="m-0 flex max-h-[360px] list-none flex-col gap-1.5 overflow-y-auto p-2.5">
              {#each more.frames as frame (frameKey(frame))}
                {@const kind = kindOf(frame.type)}
                <li class={['ip-row flex items-center gap-1 pr-2', frame.worn && 'ip-sel']}>
                  <button type="button" aria-pressed={frame.worn} aria-label="Wear {frame.name}" disabled={busy} class="flex min-w-0 flex-1 cursor-pointer items-center gap-3 rounded-[3px] border-0 bg-transparent px-2 py-1.5 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => wear(frame)}>
                    <Icon icon={frame.icon} size={40} name={frame.icon ? frame.name : ''} />
                    <span class={['ip-tag w-[78px] justify-center text-[10px] text-white', kind?.color]}>{kind?.label.toUpperCase()}</span>
                    <span class="min-w-0 flex-1 truncate text-[16px]">{frame.name}</span>
                    {#if frame.worn}<span class="text-sm text-link">Worn</span>{/if}
                  </button>
                  <button type="button" aria-label="Remove {frame.name}" class="grid size-8 flex-none cursor-pointer place-items-center rounded-full border-0 bg-white/10 text-white hover:bg-red" disabled={busy} onclick={() => removeFrame(frame)}>×</button>
                </li>
              {:else}
                <li class="px-1 py-1 text-soft">No frame</li>
              {/each}
            </ul>
            <Picker bind:this={framePicker} trigger={false} label="Frame" items={frameItems} facets={frameFacets} placeholder="Name or id" value={null} onselect={addFrame} />
          </section>

          <section aria-label="Seen and unlocked" class="ip-panel overflow-hidden">
            <div class="ip-head"><h2 class="text-[22px]">Seen and unlocked</h2></div>
            <ul class="m-0 flex list-none flex-col gap-1.5 p-2.5">
              {#each data.seen as entry (entry.key)}
                <li class="ip-row flex items-center gap-2.5 py-1.5 pr-2 pl-3">
                  <span class="flex-1">{entry.label}</span>
                  {#if entry.liked}<span class="text-sm text-soft">♥ <span class="num text-lg">{entry.liked}</span></span>{/if}
                  <span class="num text-[21px]">{count(entry.count)}</span>
                  <button type="button" class="ip-btn2 ip-sm h-8 px-2.5 text-sm" aria-label="Reset {entry.label}" disabled={busy || (entry.count === 0 && entry.liked === 0)} onclick={() => confirmSeen(entry)}>Reset</button>
                </li>
              {/each}
              <li>
                {#if editingMusic}
                  <form class="ip-row ip-sel flex items-center gap-2 py-1.5 pr-2 pl-3" onsubmit={(e) => saveMusic(e, more.music)}>
                    <span class="flex-1">Music player</span>
                    <!-- svelte-ignore a11y_autofocus -->
                    <select class="ip-field h-9 w-[150px]" aria-label="Album" autofocus onkeydown={escapeMusic} bind:value={album}>
                      <option value={musicNone}>Off</option>
                      <option value={musicFavor}>Liked songs</option>
                      {#each more.music.albums as a (a.id)}<option value={a.id}>{a.name}</option>{/each}
                    </select>
                    <select class="ip-field h-9 w-[104px]" aria-label="Loop" onkeydown={escapeMusic} bind:value={mode}>
                      {#each musicModes.slice(0, more.music.modes) as label, i (i)}<option value={i}>{label}</option>{/each}
                    </select>
                    <button type="submit" class="ip-btn ip-sm h-9" disabled={busy}>Save</button>
                  </form>
                {:else}
                  <button type="button" aria-label="Edit the music player" disabled={busy} class="ip-row flex w-full cursor-pointer items-center gap-2.5 border-0 py-2 pr-3 pl-3 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]" onclick={() => startMusic(more.music)}>
                    <span class="flex-1">Music player</span>
                    <span class="truncate text-soft">{albumName(more.music, more.music.album)} · {musicModes[more.music.mode] ?? more.music.mode}</span>
                  </button>
                {/if}
              </li>
            </ul>
          </section>

          <section aria-label="Handbook" class="ip-panel overflow-hidden">
            <div class="ip-head"><h2 class="text-[22px]">Handbook</h2></div>
            {#if data.handbook.length === 0}
              <p class="m-0 px-5 py-4 text-soft">No pages</p>
            {:else}
              <ul class="m-0 flex list-none flex-col gap-1.5 p-2.5">
                {#each data.handbook as page (page.id)}
                  <li class="ip-row flex items-center gap-2.5 px-3 py-2">
                    <span class="flex min-w-0 flex-1 flex-col">
                      <span class="truncate">{page.name.split(' · ')[1] ?? (page.name || `#${page.id}`)}</span>
                      {#if page.name.includes(' · ')}<span class="truncate text-[13px] text-dim">{page.name.split(' · ')[0]}</span>{/if}
                    </span>
                    <span class="text-sm whitespace-nowrap text-soft"><span class="num text-[19px] text-white">{page.done}/{page.tasks}</span> tasks · reward <span class="num text-[19px] text-white">{page.awards}/{page.awards_total}</span></span>
                  </li>
                {/each}
              </ul>
            {/if}
          </section>
        </div>
      </div>
    {:else if archives.error || details.error}
      <p class="m-0 text-bad">{(archives.error ?? details.error)?.message}</p>
    {/if}
  {/snippet}
</CommanderPage>
