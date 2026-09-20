<script>
  import { status, facts, clients, sequence, events } from '../lib/stores/api.js';

  function qualityDot(q) {
    if (q === 'good') return 'bg-ok';
    if (q === 'stale') return 'bg-warn';
    return 'bg-ink-muted';
  }

  function stateColor(state) {
    if (state === 'up') return 'bg-ok';
    if (state === 'down') return 'bg-ink-muted';
    if (state === 'failed') return 'bg-danger';
    if (state === 'shutting_down' || state === 'waking' || state === 'down_unverified') {
      return 'bg-warn';
    }
    return 'bg-ink-faint';
  }

  /**
   * Renders a fact value for display.
   *
   * Integers are shown as integers: the previous version applied toFixed(1)
   * to every number, so a runtime of 3600 seconds read as "3600.0".
   */
  function formatValue(fact) {
    const val = fact?.value;
    if (val === null || val === undefined) return '—';
    if (Array.isArray(val)) return val.length ? val.join(' ') : '—';
    if (typeof val === 'boolean') return val ? 'yes' : 'no';

    if (typeof val === 'number') {
      if (fact.type === 'duration') return formatSeconds(val);
      if (fact.type === 'percent') return `${round(val)}%`;
      return round(val);
    }

    return String(val);
  }

  function round(n) {
    return Number.isInteger(n) ? String(n) : n.toFixed(1);
  }

  /** Renders a duration in seconds as something a human reads at a glance. */
  function formatSeconds(seconds) {
    if (seconds < 60) return `${Math.round(seconds)}s`;
    const mins = Math.floor(seconds / 60);
    if (mins < 60) return `${mins}m ${Math.round(seconds % 60)}s`;
    return `${Math.floor(mins / 60)}h ${mins % 60}m`;
  }

  /** A unit suffix, unless formatValue has already expressed it. */
  function unitSuffix(fact) {
    if (!fact?.unit) return '';
    if (fact.type === 'percent' || fact.type === 'duration') return '';
    return ` ${fact.unit}`;
  }

  // Sorted so the grid does not reshuffle on every refresh; Object.entries
  // follows insertion order, which a Go map randomises per response.
  $: sortedFacts = Object.entries($facts).sort(([a], [b]) => a.localeCompare(b));

  $: staleFacts = sortedFacts.filter(([, f]) => f.quality !== 'good');

  function timeAgo(ts) {
    if (!ts) return '';
    const d = new Date(ts);
    const s = Math.floor((Date.now() - d.getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    return `${Math.floor(s / 3600)}h ago`;
  }
</script>

<div class="p-6">
  <!-- Header -->
  <div class="flex items-center justify-between mb-6">
    <h1 class="text-sm font-bold text-ink tracking-wider">DASHBOARD</h1>
    {#if $status?.sequence}
      <div class="flex items-center gap-2">
        <span class="px-2 py-1 rounded text-[10px] font-bold bg-amber/20 text-amber animate-pulse">
          {$status.sequence.state?.toUpperCase()}
        </span>
        {#if $status.sequence.stage_name}
          <span class="text-[10px] text-ink-muted">
            {$status.sequence.stage_name}
            ({$status.sequence.current_stage + 1}/{$status.sequence.total_stages})
          </span>
        {/if}
        {#if $status.sequence.ponr_crossed}
          <span class="px-2 py-1 rounded text-[10px] font-bold bg-danger/20 text-danger">
            PONR CROSSED
          </span>
        {/if}
      </div>
    {/if}
  </div>

  <!-- Facts Grid -->
  <section class="mb-6">
    <div class="flex items-center gap-2 mb-3">
      <h2 class="text-[10px] text-ink-muted tracking-wider">FACTS</h2>
      {#if staleFacts.length > 0}
        <span class="text-[10px] text-warn">
          {staleFacts.length} not reporting — conditions reading them cannot be satisfied
        </span>
      {/if}
    </div>
    <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-2">
      {#each sortedFacts as [key, fact] (key)}
        <div
          class="border rounded-[var(--radius-sm)] p-3 bg-surface-50 transition-colors
            {fact.quality === 'good' ? 'border-edge' : 'border-warn/40'}"
          title={fact.description || key}
        >
          <div class="text-[10px] text-ink-muted truncate mb-1">{key}</div>
          <div class="text-lg font-bold tabular-nums
            {fact.quality === 'good' ? 'text-ink' : 'text-ink-muted'}">
            {formatValue(fact)}<span class="text-xs font-normal text-ink-muted">{unitSuffix(fact)}</span>
          </div>
          <div class="flex items-center gap-1.5 mt-1">
            <span class="w-1 h-1 rounded-full {qualityDot(fact.quality)}"></span>
            <span class="text-[9px] text-ink-muted">{fact.quality}</span>
            {#if fact.updated_at}
              <span class="text-[9px] text-ink-faint ml-auto">{timeAgo(fact.updated_at)}</span>
            {/if}
          </div>
        </div>
      {/each}
      {#if Object.keys($facts).length === 0}
        <div class="col-span-full text-center py-8 text-ink-muted text-xs">
          No facts received. Check source configuration.
        </div>
      {/if}
    </div>
  </section>

  <!-- Clients -->
  <section class="mb-6">
    <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">CLIENTS</h2>
    <div class="border border-edge rounded-[var(--radius-sm)] overflow-hidden">
      <table class="w-full text-xs">
        <thead>
          <tr class="bg-surface-100 text-ink-muted text-[10px] tracking-wider">
            <th class="text-left px-3 py-2">STATE</th>
            <th class="text-left px-3 py-2">NAME</th>
            <th class="text-left px-3 py-2">TRANSPORT</th>
            <th class="text-left px-3 py-2">ADDRESS</th>
            <th class="text-left px-3 py-2">TAGS</th>
          </tr>
        </thead>
        <tbody>
          {#each $clients as client}
            <tr class="border-t border-edge-subtle hover:bg-surface-50 transition-colors">
              <td class="px-3 py-2">
                <span class="inline-flex items-center gap-1.5">
                  <span class="w-1.5 h-1.5 rounded-full {stateColor(client.state)}"></span>
                  <span class="text-ink-secondary">{client.state}</span>
                </span>
              </td>
              <td class="px-3 py-2 text-ink font-medium">{client.name}</td>
              <td class="px-3 py-2 text-ink-secondary">{client.transport}</td>
              <td class="px-3 py-2 text-ink-muted tabular-nums">{client.address || '—'}</td>
              <td class="px-3 py-2">
                {#each (client.tags || []) as tag}
                  <span class="inline-block px-1.5 py-0.5 bg-surface-200 text-ink-muted text-[9px] rounded mr-1">
                    {tag}
                  </span>
                {/each}
              </td>
            </tr>
          {/each}
          {#if $clients.length === 0}
            <tr>
              <td colspan="5" class="px-3 py-8 text-center text-ink-muted">
                No clients configured.
              </td>
            </tr>
          {/if}
        </tbody>
      </table>
    </div>
  </section>

  <!-- Active Sequence -->
  {#if $status?.sequence}
    <section class="mb-6">
      <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">ACTIVE SEQUENCE</h2>
      <div class="border border-amber/30 rounded-[var(--radius-sm)] p-4 bg-amber/5">
        <div class="flex items-center gap-3 mb-2">
          <span class="text-amber font-bold text-sm">{$status.sequence.plan}</span>
          <span class="text-[10px] px-1.5 py-0.5 rounded bg-amber/20 text-amber">
            {$status.sequence.state}
          </span>
          {#if $status.sequence.ponr_crossed}
            <span class="text-[10px] px-1.5 py-0.5 rounded bg-danger/20 text-danger">
              PONR
            </span>
          {/if}
        </div>
        <div class="text-[10px] text-ink-muted">
          Stage {$status.sequence.current_stage} · Started {timeAgo($status.sequence.started_at)}
        </div>
      </div>
    </section>
  {/if}

  <!-- Event Log -->
  <section>
    <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">EVENT LOG</h2>
    <div class="border border-edge rounded-[var(--radius-sm)] max-h-64 overflow-y-auto">
      {#each $events as evt}
        <div class="flex items-start gap-3 px-3 py-1.5 border-b border-edge-subtle text-[11px]">
          <span class="text-ink-faint tabular-nums flex-shrink-0 w-16">
            {new Date(evt.timestamp).toLocaleTimeString()}
          </span>
          <span class="text-amber flex-shrink-0 w-32">{evt.type}</span>
          <span class="text-ink-secondary truncate">
            {typeof evt.data === 'object' ? JSON.stringify(evt.data) : evt.data}
          </span>
        </div>
      {/each}
      {#if $events.length === 0}
        <div class="px-3 py-6 text-center text-ink-muted text-xs">No events yet.</div>
      {/if}
    </div>
  </section>
</div>
