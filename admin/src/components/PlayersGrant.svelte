<script lang="ts">
  // The inventory screens' grant card: pick drops into a list, grant the
  // list. Granting refetches the commander: the screen and the header.
  import { api } from '../lib/api'
  import { createCommanderMutation, done, type Kicked } from '../lib/query'
  import { drop, type Queued } from '../lib/players'
  import PlayersDrops, { type DropKind } from './PlayersDrops.svelte'
  import PlayersQueue from './PlayersQueue.svelte'

  let {
    id,
    name,
    kinds,
    owned,
    all = [],
  }: {
    id: number
    name: string
    kinds: DropKind[]
    // what the commander has, by `${type}:${id}`, tagged in the pickers
    owned?: Set<string>
    // whole catalogues granted in one call (/grant-all)
    all?: { label: string; body: { type: number; count?: number; best?: boolean } }[]
  } = $props()

  // granted with grant-cosmetics, /grant refuses them
  const cosmetic = new Set<number>([drop.iconFrame, drop.chatFrame, drop.spweapon])

  let queue = $state<Queued[]>([])

  const grant = createCommanderMutation(
    () => id,
    (drops: Queued[]) => {
      const body = { drops: drops.map(({ type, id, count }) => ({ type, id, count })) }
      const path = drops.some((d) => cosmetic.has(d.type)) ? 'grant-cosmetics' : 'grant'
      return api<Kicked>('POST', `/commanders/${id}/${path}`, body)
    },
    { success: (answer) => done('Granted', name)(answer), onSuccess: () => (queue = []) },
  )

  const grantAll = createCommanderMutation(
    () => id,
    ({ body }: { label: string; body: unknown }) => api<Kicked>('POST', `/commanders/${id}/grant-all`, body),
    { success: (answer, { label }) => done(`${label} granted`, name)(answer) },
  )
</script>

<section aria-labelledby="grant-h" class="ip-card">
  <div class="ip-card-head"><h2 id="grant-h" class="truncate">Grant to {name}</h2></div>
  <div class="ip-card-body">
    <PlayersDrops {kinds} {owned} bind:drops={queue} />
    {#if queue.length}
      <div class="ip-strip">TO GRANT</div>
      <PlayersQueue bind:drops={queue} />
    {/if}
    <div class="grid grid-cols-2 gap-3">
      <button type="button" class="ip-btn2" disabled={!queue.length} onclick={() => (queue = [])}>Clear</button>
      <button type="button" class="ip-btn" disabled={grant.isPending || !queue.length} onclick={() => grant.mutate($state.snapshot(queue))}>Grant</button>
    </div>
    {#if all.length}
      <div class="grid grid-cols-2 gap-3">
        {#each all as a (a.label)}
          <button type="button" class="ip-btn2" disabled={grantAll.isPending} onclick={() => grantAll.mutate(a)}>{a.label}</button>
        {/each}
      </div>
    {/if}
  </div>
</section>
