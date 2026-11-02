<script lang="ts">
  // A commander's profile as the game's main screen shows it: the secretary
  // in the skin they wear over the whole screen, the commander's level,
  // status, data and greetings over it (hidden with Overlays), the actions
  // on the right, and the commander's other screens as buttons below.
  import { createQuery, useQueryClient } from '@tanstack/svelte-query'
  import CommanderPage from '../../components/CommanderPage.svelte'
  import Modal from '../../components/Modal.svelte'
  import SkinArt from '../../components/SkinArt.svelte'
  import Stepper from '../../components/Stepper.svelte'
  import { toast } from '../../components/Toast.svelte'
  import { api } from '../../lib/api'
  import { ago, stamp } from '../../lib/format'
  import { playerKeys, type PlayerProfile } from '../../lib/players'
  import { createCommanderWrite, createWrite, get, keys } from '../../lib/query'
  import { link, navigate } from '../../lib/router.svelte'

  let { id }: { id: number } = $props()

  const query = createQuery(() => ({
    queryKey: playerKeys.profile(id),
    queryFn: get<PlayerProfile>(`/commanders/${id}/profile`),
  }))
  const profile = $derived(query.data)

  // the fields start at the commander's name and level, again after a save
  const savedName = $derived(profile?.name ?? '')
  const savedLevel = $derived(profile?.level ?? 1)
  let name = $derived(savedName)
  let level = $derived(savedLevel)

  const { mutation: write } = createCommanderWrite(() => id)

  let renameModal = $state<Modal>()
  let levelModal = $state<Modal>()
  const rename = () => write.mutate({ path: '/rename', body: { name: name.trim() }, done: `Renamed to ${name.trim()}`, commander: profile?.name }, { onSuccess: () => renameModal?.close() })
  const setLevel = () => write.mutate({ path: '/level', body: { level: Number(level) }, done: `Level set to ${level}`, commander: profile?.name }, { onSuccess: () => levelModal?.close() })

  let bagsModal = $state<Modal>()
  let shipBag = $state(0)
  let equipBag = $state(0)
  let commanderBag = $state(0)
  function openBags() {
    if (!profile) return
    ;({ ship_bag_max: shipBag, equip_bag_max: equipBag, commander_bag_max: commanderBag } = profile.bags)
    bagsModal?.show()
  }
  const setBags = () =>
    write.mutate(
      { path: '/bags', body: { ship_bag_max: shipBag, equip_bag_max: equipBag, commander_bag_max: commanderBag }, done: 'Bag capacity set', commander: profile?.name },
      { onSuccess: () => bagsModal?.close() },
    )

  const resetPassword = () => write.mutate({ method: 'DELETE', path: '/secondary-password', done: 'Secondary password removed', commander: profile?.name })

  let exporting = $state(false)
  async function exportSave() {
    exporting = true
    try {
      const save = await api<Record<string, unknown[]>>('GET', `/commanders/${id}/export`)
      const url = URL.createObjectURL(new Blob([JSON.stringify(save, null, 2)], { type: 'application/json' }))
      const a = document.createElement('a')
      a.href = url
      a.download = `commander-${id}-${profile?.name ?? ''}.json`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      toast((err as Error).message, true)
    } finally {
      exporting = false
    }
  }

  // deleting a commander drops its queries and refetches the lists it was in
  const client = useQueryClient()
  const remove = createWrite((_: void) => api('DELETE', `/commanders/${id}`), [keys.commanders, keys.online], {
    success: () => `${profile?.name} deleted`,
    onSuccess: () => {
      client.removeQueries({ queryKey: keys.commander(id) })
      navigate('/commanders')
    },
  })
  function confirmRemove() {
    if (profile && confirm(`Delete ${profile.name} (UID ${id}) and everything it has? This can't be undone.`)) remove.mutate()
  }
  const busy = $derived(write.isPending || remove.isPending || exporting)

  const duties: Record<number, string> = { 1: 'commander', 2: 'deputy', 3: 'elite', 4: 'member', 5: 'recruit' }

  const status = $derived.by(() => {
    if (!profile) return []
    return [
      profile.online
        ? { label: 'Online now', detail: profile.ip ? `from ${profile.ip}` : '', light: 'bg-ok' }
        : { label: 'Offline', detail: profile.last_online ? `last seen ${ago(profile.last_online)}` : 'never played', light: 'bg-grey' },
      profile.secondary_password ? { label: 'Secondary password set', detail: '', light: 'bg-warn' } : { label: 'No secondary password', detail: '', light: 'bg-grey' },
      profile.guild ? { label: profile.guild.name, detail: `guild ${duties[profile.guild.duty] ?? ''}`, light: 'bg-cyan' } : { label: 'No guild', detail: '', light: 'bg-grey' },
    ]
  })

  // the commander's things, each opening its screen
  const data = $derived.by(() => {
    if (!profile) return []
    const c = profile.counts
    return [
      { k: 'Ships', v: `${c.ships}/${c.ship_bag_max}`, to: 'dock' },
      { k: 'Gear', v: `${c.equipments}/${c.equip_bag_max}`, to: 'gear' },
      { k: 'Skins', v: String(c.skins), to: 'dock?view=skins' },
      { k: 'Augments', v: String(c.spweapons), to: 'items' },
      { k: 'Meowfficers', v: String(c.meowfficers), to: 'meowfficers' },
      { k: 'Furniture', v: String(c.furniture), to: 'backyard' },
      { k: 'Friends', v: String(c.friends), to: 'friends' },
      { k: 'Oaths', v: String(c.oaths), to: 'dock' },
    ]
  })

  const painting = $derived(profile?.secretary?.icon?.split('/').pop() ?? '')
  let overlays = $state(true)
  const glass = 'rounded bg-[rgb(20_24_34/0.82)] shadow-[0_8px_24px_rgb(0_0_0/0.35)]'
  const shade = '[text-shadow:0_1px_2px_rgb(0_0_0/0.9),0_2px_12px_rgb(0_0_0/0.75)]'
  const editable = 'cursor-pointer border-0 text-left text-white hover:shadow-[inset_0_0_0_2px_var(--color-cyan)]'
</script>

<CommanderPage {id} title="Profile">
  {#snippet children()}
    {#if profile}
      {#snippet overlay()}
        {#if overlays}
          <!-- keeps the overlays readable over light art -->
          <div aria-hidden="true" class="pointer-events-none absolute inset-y-0 left-0 w-[560px] bg-[linear-gradient(90deg,rgb(12_15_24/0.8)_0%,rgb(12_15_24/0.55)_60%,transparent_100%)]"></div>
          <div aria-hidden="true" class="pointer-events-none absolute inset-y-0 right-0 w-[400px] bg-[linear-gradient(270deg,rgb(12_15_24/0.6)_0%,transparent_100%)]"></div>

          <div class="absolute top-5 bottom-5 left-6 flex w-[380px] flex-col gap-3">
            <button type="button" aria-label="Rename {profile.name}" class={[editable, 'flex flex-col gap-0.5 rounded bg-transparent px-2 py-1']} onclick={() => ((name = profile!.name), renameModal?.show())}>
              <span class="truncate text-[44px] leading-tight text-gold {shade}">{profile.name}</span>
              <span class="flex items-baseline gap-4 {shade}">
                <span class="num text-[26px]">UID:{profile.id}</span>
                <span class="text-sm text-soft">Account <span class="num text-lg text-white">{profile.account_id}</span></span>
              </span>
            </button>

            <button type="button" aria-label="Set the level of {profile.name}" class={[glass, editable, 'flex flex-col gap-2 px-4 py-3']} onclick={() => ((level = profile!.level), levelModal?.show())}>
              <span class="flex w-full items-baseline gap-2">
                <span class="num text-[44px] leading-none">Lv.{profile.level}</span>
                <span class="num ml-auto text-xl text-soft">{profile.level_exp ? `${profile.exp}/${profile.level_exp}` : 'MAX'}</span>
              </span>
              <span role="progressbar" aria-label="Experience to level {profile.level + 1}" aria-valuenow={profile.exp} aria-valuemin={0} aria-valuemax={profile.level_exp || profile.exp} class="block h-1.5 w-full overflow-hidden rounded-full bg-white/12">
                <span class="block h-full bg-cyan" style:width="{profile.level_exp ? Math.min(100, (profile.exp / profile.level_exp) * 100) : 100}%"></span>
              </span>
            </button>

            <ul aria-label="Status" class={[glass, 'm-0 flex list-none flex-col gap-1.5 px-3 py-2.5']}>
              {#each status as s (s.label)}
                <li class="flex items-center gap-3">
                  <span aria-hidden="true" class={['h-[26px] w-2 flex-none rounded-sm', s.light]}></span>
                  <span class="truncate text-[17px]">{s.label}</span>
                  {#if s.detail}<span class="ml-auto truncate text-[13px] text-dim">{s.detail}</span>{/if}
                </li>
              {/each}
            </ul>

            <dl class={[glass, 'm-0 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 px-4 py-2.5']}>
              <dt class="text-[15px] text-dim">Registered</dt>
              <dd class="num m-0 text-right text-xl">{profile.registered_at ? stamp(profile.registered_at) : '-'}</dd>
              <dt class="text-[15px] text-dim">Last online</dt>
              <dd class="num m-0 text-right text-xl">{profile.online ? 'Now' : profile.last_online ? stamp(profile.last_online) : 'Never'}</dd>
            </dl>

            <blockquote aria-label="Greetings" class={['m-0 mt-auto px-2 text-lg leading-[1.45] break-words', shade, !profile.manifesto && 'text-dim']}>
              {profile.manifesto ? `“${profile.manifesto}”` : 'No greetings'}
            </blockquote>
            {#if profile.secretary}
              <a href="/commanders/{id}/dock?ship={profile.secretary.ship_id}" use:link class="self-start rounded-3xl bg-[rgb(30_33_40/0.88)] px-4 py-2 text-base text-white no-underline hover:bg-[rgb(50_54_64/0.95)]">
                <span class="text-soft">Secretary</span> {profile.secretary.name} <span class="num text-lg">Lv.{profile.secretary.level}</span>
              </a>
            {/if}
          </div>

          <div class={[glass, 'absolute top-[84px] right-5 bottom-5 flex w-[320px] flex-col gap-3 p-4']}>
            <dl aria-label="Data" class="m-0 grid grid-cols-2 gap-1.5">
              {#each data as d (d.k)}
                <a href="/commanders/{id}/{d.to}" use:link class="flex flex-col rounded bg-white/6 px-3 py-1.5 text-white no-underline hover:bg-white/12">
                  <dt class="text-[14px] text-soft">{d.k}</dt>
                  <dd class="num m-0 text-[26px] leading-tight">{d.v}</dd>
                </a>
              {/each}
            </dl>
            <div class="mt-auto flex flex-col gap-2.5">
              <button type="button" class="ip-btn2 h-[46px] text-[19px]" disabled={busy} onclick={openBags}>Bag capacity</button>
              <button type="button" class="ip-btn h-[46px] text-[19px]" disabled={busy} onclick={exportSave}>Export save</button>
              <button type="button" class="ip-btn2 h-[46px] text-[19px]" disabled={busy || !profile.secondary_password} onclick={resetPassword}>Reset 2nd password</button>
              <button type="button" class="ip-danger h-[46px] text-[19px]" disabled={busy} onclick={confirmRemove}>Delete commander</button>
            </div>
          </div>
        {/if}
      {/snippet}

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

      {#if painting}
        <SkinArt {painting} name={profile.secretary?.name ?? profile.name} class="h-[760px]" {tools}>{@render overlay()}</SkinArt>
      {:else}
        <div class="relative h-[760px] overflow-hidden rounded bg-[radial-gradient(ellipse_at_42%_38%,#3a4670_0%,#222a44_45%,#121624_100%)]">
          <div class="absolute top-5 right-5">{@render tools()}</div>
          {@render overlay()}
        </div>
      {/if}

      <Modal bind:this={renameModal} title="Rename {profile.name}">
        <form class="flex items-end gap-2.5" onsubmit={(e) => (e.preventDefault(), rename())}>
          <label class="flex min-w-0 flex-1 flex-col gap-1.5 text-[15px] text-soft">New name <input class="ip-field w-full" bind:value={name} maxlength="14" required /></label>
          <button type="submit" class="ip-btn h-[46px] text-[19px]" disabled={busy || !name.trim() || name.trim() === profile.name}>Rename</button>
        </form>
      </Modal>
      <Modal bind:this={levelModal} title="{profile.name}: level">
        <form class="flex items-end gap-2.5" onsubmit={(e) => (e.preventDefault(), setLevel())}>
          <div class="flex min-w-0 flex-1 flex-col gap-1.5 text-[15px] text-soft">
            <span>Level <span class="num text-dim">1-{profile.max_level}</span></span>
            <Stepper label="Level" bind:value={level} min={1} max={profile.max_level} />
          </div>
          <button type="submit" class="ip-btn h-[52px] text-[19px]" disabled={busy || level === profile.level}>Set level</button>
        </form>
      </Modal>
      <Modal bind:this={bagsModal} title="{profile.name}: bag capacity">
        <form class="flex flex-col gap-3" onsubmit={(e) => (e.preventDefault(), setBags())}>
          {#each [{ label: 'Dock', limits: profile.bags.ship_limits }, { label: 'Gear depot', limits: profile.bags.equip_limits }, { label: 'Meowfficers', limits: profile.bags.commander_limits }] as bag, i (bag.label)}
            <div class="flex flex-col gap-1.5 text-[15px] text-soft">
              <span>{bag.label} <span class="num text-dim">{bag.limits[0]}-{bag.limits[1]}</span></span>
              {#if i === 0}
                <Stepper label={bag.label} bind:value={shipBag} min={bag.limits[0]} max={bag.limits[1]} step={1} />
              {:else if i === 1}
                <Stepper label={bag.label} bind:value={equipBag} min={bag.limits[0]} max={bag.limits[1]} step={1} />
              {:else}
                <Stepper label={bag.label} bind:value={commanderBag} min={bag.limits[0]} max={bag.limits[1]} step={1} />
              {/if}
            </div>
          {/each}
          <button type="submit" class="ip-btn h-[52px] self-end text-[19px]" disabled={busy || (shipBag === profile.bags.ship_bag_max && equipBag === profile.bags.equip_bag_max && commanderBag === profile.bags.commander_bag_max)}>Set capacity</button>
        </form>
      </Modal>
    {:else if query.error}
      <p class="text-bad">{query.error.message}</p>
    {/if}
  {/snippet}
</CommanderPage>
