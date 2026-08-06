<script>
  import { clients } from '../lib/stores/api.js';

  let selected = null;

  function stateColor(state) {
    if (state === 'up') return 'bg-ok';
    if (state === 'down' || state === 'failed') return 'bg-danger';
    if (state === 'shutting_down' || state === 'waking') return 'bg-warn';
    return 'bg-ink-muted';
  }
</script>

<div class="p-6">
  <div class="flex items-center justify-between mb-6">
    <h1 class="text-sm font-bold text-ink tracking-wider">CLIENTS</h1>
    <span class="text-[10px] text-ink-muted">{$clients.length} configured</span>
  </div>

  <div class="flex gap-4">
    <!-- Client List -->
    <div class="w-64 flex-shrink-0">
      {#each $clients as client}
        <button
          class="w-full text-left px-3 py-2.5 border-b border-edge-subtle transition-colors
            {selected === client.name
              ? 'bg-surface-100 border-l-2 border-l-amber'
              : 'hover:bg-surface-50 border-l-2 border-l-transparent'}"
          onclick={() => selected = client.name}
        >
          <div class="flex items-center gap-2">
            <span class="w-1.5 h-1.5 rounded-full {stateColor(client.state)}"></span>
            <span class="text-xs text-ink">{client.name}</span>
          </div>
          <div class="text-[10px] text-ink-muted mt-0.5 pl-4">
            {client.transport} · {client.address || 'no address'}
          </div>
        </button>
      {/each}
    </div>

    <!-- Client Detail -->
    <div class="flex-1 border border-edge rounded-[var(--radius-sm)] bg-surface-50">
      {#if selected}
        {@const client = $clients.find(c => c.name === selected)}
        {#if client}
          <div class="p-4 border-b border-edge">
            <div class="flex items-center gap-3">
              <span class="w-2 h-2 rounded-full {stateColor(client.state)}"></span>
              <h2 class="text-sm font-bold text-ink">{client.name}</h2>
              <span class="text-[10px] px-1.5 py-0.5 rounded bg-surface-200 text-ink-secondary">
                {client.state}
              </span>
            </div>
            {#if client.description}
              <div class="text-xs text-ink-muted mt-1">{client.description}</div>
            {/if}
          </div>

          <div class="p-4 space-y-4">
            <div class="grid grid-cols-2 gap-4 text-xs">
              <div>
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">TRANSPORT</div>
                <div class="text-ink">{client.transport}</div>
              </div>
              <div>
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">ADDRESS</div>
                <div class="text-ink tabular-nums">{client.address || '—'}</div>
              </div>
              <div>
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">FEED POLICY</div>
                <div class="text-ink">{client.feed_policy}</div>
              </div>
              <div>
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">WAKE POLICY</div>
                <div class="text-ink">{client.wake_policy}</div>
              </div>
            </div>

            {#if client.tags?.length > 0}
              <div>
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">TAGS</div>
                <div class="flex gap-1.5 flex-wrap">
                  {#each client.tags as tag}
                    <span class="px-2 py-0.5 bg-surface-200 text-ink-secondary text-[10px] rounded">
                      {tag}
                    </span>
                  {/each}
                </div>
              </div>
            {/if}

            {#if client.feeds?.length > 0}
              <div>
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">FEEDS</div>
                <div class="flex gap-1.5 flex-wrap">
                  {#each client.feeds as feed}
                    <span class="px-2 py-0.5 bg-amber/10 text-amber text-[10px] rounded">
                      {feed}
                    </span>
                  {/each}
                </div>
              </div>
            {/if}

            {#if client.depends_on?.length > 0}
              <div>
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">DEPENDS ON</div>
                <div class="flex gap-1.5 flex-wrap">
                  {#each client.depends_on as dep}
                    <span class="px-2 py-0.5 bg-surface-200 text-ink-secondary text-[10px] rounded">
                      {dep}
                    </span>
                  {/each}
                </div>
              </div>
            {/if}
          </div>
        {/if}
      {:else}
        <div class="flex items-center justify-center h-64 text-ink-muted text-xs">
          Select a client to view details
        </div>
      {/if}
    </div>
  </div>
</div>
