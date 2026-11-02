<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query'
  import { createDebouncedValue } from '@tanstack/svelte-pacer'
  import CommanderPage from '../../components/CommanderPage.svelte'
  import Icon from '../../components/Icon.svelte'
  import PlayersGrant from '../../components/PlayersGrant.svelte'
  import VirtualGrid from '../../components/VirtualGrid.svelte'
  import PlayersTabs from '../../components/PlayersTabs.svelte'
  import Stepper from '../../components/Stepper.svelte'
  import { api } from '../../lib/api'
  import { fuzzy } from '../../lib/fuzzy'
  import { createCommanderMutation, done, get, type Kicked } from '../../lib/query'
  import { drop, playerKeys, type PlayerItem, type PlayerWardrobe } from '../../lib/players'
  import { bounded, clamp } from '../../lib/bounded'

  let { id }: { id: number } = $props()

  const items = createQuery(() => ({
    queryKey: playerKeys.items(id),
    queryFn: get<PlayerItem[]>(`/commanders/${id}/items`),
  }))

  let query = $state('')
  const [debounced] = createDebouncedValue(() => query, { wait: 150 })
  const shown = $derived(fuzzy(items.data ?? [], debounced(), (item) => [item.name, item.id]))

  let selectedID = $state<number | null>(null)
  const selected = $derived(items.data?.find((item) => item.id === selectedID) ?? null)
  // the stepper starts at the count the commander has, again after a save
  let count = $derived(selected?.count ?? 0)

  const save = createCommanderMutation(
    () => id,
    ({ item, next }: { item: PlayerItem; next: number; commander: string }) => api<Kicked>('PUT', `/commanders/${id}/items/${item.id}`, { count: next }),
    { success: (answer, { item, next, commander }) => done(next ? `${item.name} set to ${next}` : `${item.name} removed`, commander)(answer) },
  )

  // the points (virtual items) come with the wardrobe
  const wardrobe = createQuery(() => ({
    queryKey: playerKeys.wardrobe(id),
    queryFn: get<PlayerWardrobe>(`/commanders/${id}/wardrobe`),
  }))
  // the points' fields, as the commander has them after each fetch
  const owned = $derived(wardrobe.data?.points)
  const points = $derived.by(() => {
    const fields = $state<Record<number, number>>(Object.fromEntries((owned ?? []).map((p) => [p.id, p.count])))
    return fields
  })
  const savePoints = createCommanderMutation(
    () => id,
    ({ item, amount }: { item: number; amount: number; name: string; commander: string }) => api<Kicked>('PUT', `/commanders/${id}/virtual-items/${item}`, { amount }),
    { success: (answer, { name, amount, commander }) => done(`${name} set to ${amount}`, commander)(answer) },
  )
</script>

<CommanderPage {id} title="Items">
  {#snippet children(summary)}
    <div class="flex flex-wrap items-start gap-6">
      <section aria-label="Depot" class="ip-panel min-w-0 flex-[1_1_560px] overflow-hidden">
        <PlayersTabs {id} current="Items" />
        <div class="ip-head items-center">
          <h2>Depot</h2>
          <span class="ip-sub">ITEMS</span>
          <span class="num text-2xl">{items.data?.length ?? ''}</span>
          <label class="ml-auto flex items-center gap-2.5 text-[17px]">
            Find
            <input type="search" class="ip-field w-60" placeholder="Name or id" bind:value={query} />
          </label>
        </div>
        <VirtualGrid items={shown} key={(item) => item.id} min={150} rowHeight={172} gap={18} pad={24} overscan={3} label="Items" class="max-h-[calc(100vh-250px)] min-h-[400px]">
          {#snippet cell(item)}
            <button
              type="button"
              aria-pressed={selectedID === item.id}
              class={['flex h-full w-full cursor-pointer flex-col items-center gap-2.5 rounded-[3px] border-0 bg-card px-2 pt-3.5 pb-3 text-ink shadow-[0_2px_8px_rgb(0_0_0/0.25)]', selectedID === item.id && 'shadow-[inset_0_0_0_3px_var(--color-cyan),0_2px_8px_rgb(0_0_0/0.25)]']}
              onclick={() => (selectedID = item.id)}
            >
              <Icon icon={item.icon} rarity={item.rarity} count={item.count} size={90} name={item.name} />
              <span class="line-clamp-2 text-center text-[15px] leading-tight">{item.name || `#${item.id}`}</span>
            </button>
          {/snippet}
          {#snippet empty()}
            {#if items.data}<p class="m-0 text-lg text-dim">No item</p>{:else if items.error}<p class="m-0 text-bad">{items.error.message}</p>{/if}
          {/snippet}
        </VirtualGrid>
      </section>

      <div class="flex min-w-[320px] flex-[0_1_380px] flex-col gap-5">
        <PlayersGrant
          {id}
          name={summary.name}
          kinds={[
            { type: drop.item, label: 'Item' },
            { type: drop.resource, label: 'Resource' },
            { type: drop.vitem, label: 'Virtual' },
            { type: drop.islandItem, label: 'Island item' },
            { type: drop.islandDress, label: 'Island outfit' },
          ]}
          all={[{ label: 'Every item', body: { type: drop.item } }]}
        />
        {#if selected}
          <section aria-labelledby="edit-h" class="ip-card">
            <div class="ip-card-head"><h2 id="edit-h" class="truncate">{selected.name || `#${selected.id}`}</h2></div>
            <div class="ip-card-body">
              <div class="flex items-center gap-3.5">
                <Icon icon={selected.icon} rarity={selected.rarity} size={64} name={selected.name} />
                <span class="flex flex-col">
                  <span class="num text-lg text-[#6b7386]">#{selected.id}</span>
                  {#if selected.max}<span class="text-sm text-[#6b7386]">at most <span class="num text-base">{selected.max}</span></span>{/if}
                </span>
              </div>
              <div class="ip-strip">COUNT</div>
              <Stepper label="Count" bind:value={count} min={0} max={selected.max || undefined} />
              <div class="grid grid-cols-2 gap-3">
                <button type="button" class="ip-danger" disabled={save.isPending} onclick={() => save.mutate({ item: selected, next: 0, commander: summary.name })}>Remove</button>
                <button type="button" class="ip-btn" disabled={save.isPending || count === selected.count} onclick={() => save.mutate({ item: selected, next: count, commander: summary.name })}>Save</button>
              </div>
            </div>
          </section>
        {/if}
        <section aria-label="Points" class="ip-panel overflow-hidden">
          <div class="ip-head items-center"><h2>Points</h2><span class="ip-sub">VIRTUAL ITEMS</span></div>
          <ul class="m-0 flex max-h-[420px] list-none flex-col gap-1.5 overflow-y-auto p-3">
            {#each wardrobe.data?.points ?? [] as p (p.id)}
              <li class="ip-row flex items-center gap-3 px-2 py-1.5">
                <Icon icon={p.icon} rarity={p.rarity} size={40} name={p.name} />
                <span class="flex min-w-0 flex-1 flex-col">
                  <span class="line-clamp-2 text-[17px] leading-tight">{p.name || `#${p.id}`}</span>
                  <span class="num text-sm text-dim">#{p.id}</span>
                </span>
                <input
                  class="ip-field num h-10 w-24 text-right text-xl"
                  type="number"
                  min="0"
                  aria-label="{p.name} amount"
                  bind:value={points[p.id]} use:bounded={{ min: 0 }}
                  onchange={() => savePoints.mutate({ item: p.id, amount: clamp(points[p.id], {}), name: p.name, commander: summary.name })}
                  disabled={savePoints.isPending}
                />
              </li>
            {:else}
              {#if wardrobe.data}<li class="px-1 text-lg text-dim">No points</li>{:else if wardrobe.error}<li class="px-1 text-bad">{wardrobe.error.message}</li>{/if}
            {/each}
          </ul>
        </section>
      </div>
    </div>
  {/snippet}
</CommanderPage>
