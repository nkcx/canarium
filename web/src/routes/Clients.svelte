<script>
  import { clients, loading } from '../lib/stores/api.js';
  import { stateLabel, stateTone, stateNote } from '../lib/events.js';
  import Card from '../lib/components/Card.svelte';
  import Chip from '../lib/components/Chip.svelte';
  import StatusDot from '../lib/components/StatusDot.svelte';

  let selected = null;

  $: current = $clients.find(c => c.name === selected) ?? null;

  // On a phone the list and the detail cannot sit side by side, so picking
  // a client replaces the list and a back control returns to it.
  function back() {
    selected = null;
  }
</script>

<div class="flex items-center justify-between gap-3 mb-6">
  <h1 class="text-label font-bold text-ink">Clients</h1>
  <span class="text-meta text-ink-muted">{$clients.length} configured</span>
</div>

{#if $loading && $clients.length === 0}
  <div class="space-y-2" aria-hidden="true">
    {#each [1, 2, 3, 4] as i (i)}
      <div class="h-14 rounded-[var(--radius-sm)] bg-surface-50 animate-pulse"></div>
    {/each}
  </div>
{:else if $clients.length === 0}
  <Card>
    <p class="text-body text-ink-muted text-center py-6">
      No clients configured. Add them to your configuration file.
    </p>
  </Card>
{:else}
  <div class="md:flex md:gap-5 md:items-start">
    <!-- List -->
    <div
      class="md:w-72 md:shrink-0 border border-edge rounded-[var(--radius-md)]
        overflow-hidden {selected ? 'max-md:hidden' : ''}"
    >
      {#each $clients as client, i (client.name)}
        <button
          class="w-full text-left px-4 py-3 min-h-14 transition-colors border-l-2
            {i > 0 ? 'border-t border-t-edge-subtle' : ''}
            {selected === client.name
              ? 'bg-surface-100 border-l-canary'
              : 'border-l-transparent hover:bg-surface-50'}"
          aria-current={selected === client.name ? 'true' : undefined}
          onclick={() => (selected = client.name)}
        >
          <span class="flex items-center gap-2">
            <StatusDot tone={stateTone(client.state)} />
            <span class="text-body text-ink truncate">{client.name}</span>
          </span>
          <span class="block text-meta text-ink-muted mt-1 pl-3.5 truncate">
            {client.transport} · {client.address || 'no address'}
          </span>
        </button>
      {/each}
    </div>

    <!-- Detail -->
    <div class="flex-1 min-w-0 max-md:mt-0 {selected ? '' : 'max-md:hidden'}">
      {#if current}
        <Card padded={false}>
          <div class="p-4 border-b border-edge">
            <button
              class="md:hidden text-meta text-ink-muted hover:text-ink mb-3 min-h-10
                inline-flex items-center gap-1.5"
              onclick={back}
            >
              <span aria-hidden="true">←</span> All clients
            </button>

            <div class="flex items-center gap-3 flex-wrap">
              <StatusDot tone={stateTone(current.state)} />
              <h2 class="text-label font-bold text-ink">{current.name}</h2>
              <Chip tone={stateTone(current.state)}>{stateLabel(current.state)}</Chip>
            </div>

            {#if current.description}
              <p class="text-body text-ink-muted mt-2">{current.description}</p>
            {/if}
            {#if stateNote(current.state)}
              <p class="text-meta text-ink-muted mt-2">{stateNote(current.state)}</p>
            {/if}
          </div>

          <div class="p-4 space-y-5">
            <dl class="grid grid-cols-2 gap-4 text-body">
              <div>
                <dt class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1">
                  TRANSPORT
                </dt>
                <dd class="text-ink">{current.transport}</dd>
              </div>
              <div>
                <dt class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1">
                  ADDRESS
                </dt>
                <dd class="text-ink tabular-nums break-all">{current.address || '—'}</dd>
              </div>
              <div>
                <dt class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1">
                  FEED POLICY
                </dt>
                <dd class="text-ink">{current.feed_policy}</dd>
              </div>
              <div>
                <dt class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1">
                  WAKE POLICY
                </dt>
                <dd class="text-ink">{current.wake_policy}</dd>
              </div>
            </dl>

            {#each [['TAGS', current.tags, 'neutral'], ['FEEDS', current.feeds, 'live'], ['DEPENDS ON', current.depends_on, 'neutral']] as [label, items, tone] (label)}
              {#if items?.length}
                <div>
                  <div class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1.5">
                    {label}
                  </div>
                  <div class="flex gap-1.5 flex-wrap">
                    {#each items as item (item)}
                      <Chip {tone}>{item}</Chip>
                    {/each}
                  </div>
                </div>
              {/if}
            {/each}
          </div>
        </Card>
      {:else}
        <div class="hidden md:flex items-center justify-center h-64 border border-edge
          rounded-[var(--radius-md)] bg-surface-50 text-ink-muted text-body">
          Select a client to view details
        </div>
      {/if}
    </div>
  </div>
{/if}
