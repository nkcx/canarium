<script>
  import {
    status, facts, clients, events, loading, lastError, lastUpdated,
  } from '../lib/stores/api.js';
  import {
    formatValue, unitSuffix, timeAgo, qualityTone, sortFacts, primaryPower,
    splitFactKey, sourceHealth, factStatus,
  } from '../lib/facts.js';
  import { describeEvent, stateLabel, stateTone, stateNote } from '../lib/events.js';
  import Section from '../lib/components/Section.svelte';
  import Card from '../lib/components/Card.svelte';
  import Chip from '../lib/components/Chip.svelte';
  import StatusDot from '../lib/components/StatusDot.svelte';

  $: seq = $status?.sequence ?? null;
  $: power = primaryPower($facts);
  $: health = sourceHealth($facts);
  $: sortedFacts = sortFacts($facts);

  // Only readings that stopped, or whose whole source is silent. A reading
  // the device simply does not provide is not a fault and never changes,
  // and counting it here put a permanent amber warning on a healthy UPS.
  $: problemFacts = sortedFacts.filter(([k, f]) => factStatus(k, f, health) === 'problem');
  $: shownFacts = sortedFacts.filter(([k, f]) => factStatus(k, f, health) !== 'unsupported');
  $: unsupportedFacts = sortedFacts.filter(([k, f]) => factStatus(k, f, health) === 'unsupported');

  $: warnings = $status?.config_warnings ?? [];

  /*
   * The headline answers the only two questions worth asking at 2am: how
   * long do I have, and what is happening. Both used to be buried in an
   * alphabetically sorted grid of eight identically-weighted cards.
   */
  $: headline = seq
    ? { tone: 'live', text: `${seq.plan} · ${stageText(seq)}` }
    : power?.onBattery
      ? { tone: 'warn', text: 'On battery — no plan has triggered' }
      : { tone: 'ok', text: 'On mains power' };

  function stageText(s) {
    const n = (s.current_stage ?? 0) + 1;
    const of = s.total_stages ?? '?';
    return s.stage_name
      ? `stage ${n} of ${of}: ${s.stage_name}`
      : `stage ${n} of ${of}`;
  }

  const eventToneClass = {
    live: 'text-amber',
    ok: 'text-ok',
    warn: 'text-warn',
    danger: 'text-danger',
    neutral: 'text-ink-secondary',
  };

  $: shownClients = [...$clients].sort(
    (a, b) => clientRank(a.state) - clientRank(b.state) || a.name.localeCompare(b.name),
  );

  // Whatever still needs attention floats up: failures, then machines
  // mid-transition, then the ones already settled.
  function clientRank(state) {
    if (state === 'failed') return 0;
    if (state === 'shutting_down' || state === 'waking') return 1;
    if (state === 'up') return 2;
    return 3;
  }
</script>

<h1 class="sr-only">Dashboard</h1>

<!--
  Failure and loading are distinct from "nothing configured". The empty
  state used to render during the normal gap before the first fetch
  returned, telling the operator to check a source configuration that was
  fine.
-->
{#if $lastError}
  <div class="mb-6">
    <Card tone="danger">
      <div class="flex items-start gap-3">
        <StatusDot tone="danger" />
        <div>
          <div class="text-body text-danger font-bold">Lost contact with the daemon</div>
          <p class="text-meta text-ink-secondary mt-1">
            {$lastError}. The numbers below are the last ones received{#if $lastUpdated}, {timeAgo($lastUpdated)}{/if}
            — they may no longer describe reality.
          </p>
        </div>
      </div>
    </Card>
  </div>
{/if}

<!--
  Validation warnings from startup. They were only ever logged, so on a
  deployment whose every stage matched no client -- an outage would shut
  nothing down -- the UI gave no hint.
-->
{#if warnings.length > 0}
  <div class="mb-6">
    <Card tone="warn">
      <details>
        <summary class="cursor-pointer text-body text-warn font-bold list-none flex items-center gap-2">
          <StatusDot tone="warn" />
          {warnings.length} configuration {warnings.length === 1 ? 'warning' : 'warnings'}
          <span class="text-meta text-ink-muted font-normal">— show</span>
        </summary>
        <ul class="mt-3 space-y-1.5 text-meta text-ink-secondary list-disc pl-5">
          {#each warnings as w, i (i)}
            <li class="break-words">{w}</li>
          {/each}
        </ul>
        <p class="text-meta text-ink-muted mt-3">
          From <code>canarium validate</code> at startup. Fix them in the
          configuration file and restart.
        </p>
      </details>
    </Card>
  </div>
{/if}

<!-- ── The headline ───────────────────────────────────────────────── -->
<section
  class="mb-8 border rounded-[var(--radius-lg)] overflow-hidden
    {seq ? 'border-amber/30 bg-amber/5' : 'border-edge bg-surface-50'}"
  aria-label="Current status"
>
  <div class="p-5 sm:p-6">
    <div class="flex items-center gap-2 flex-wrap mb-4">
      <Chip tone={headline.tone}>{seq ? seq.state?.replace(/_/g, ' ') : 'idle'}</Chip>
      {#if seq?.ponr_crossed}
        <Chip tone="danger" title="This sequence can no longer be aborted">
          PAST POINT OF NO RETURN
        </Chip>
      {/if}
      <span class="text-body text-ink-secondary">{headline.text}</span>
    </div>

    {#if $loading && !power}
      <div class="h-16 w-56 rounded bg-surface-200/60 animate-pulse" aria-hidden="true"></div>
      <span class="sr-only">Loading current status…</span>
    {:else if power}
      <div class="flex flex-wrap items-end gap-x-10 gap-y-4">
        <!-- The focal element. Nothing else on the page is this size. -->
        <div>
          <div class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1">
            {power.onBattery ? 'RUNTIME REMAINING' : 'RUNTIME ON BATTERY'}
          </div>
          <div
            class="text-hero leading-none font-bold tabular-nums
              {power.runtime?.quality === 'good'
                ? power.onBattery ? 'text-amber' : 'text-ink'
                : 'text-ink-muted'}"
          >
            {power.runtime ? formatValue(power.runtime) : '—'}
          </div>
        </div>

        <div>
          <div class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1">
            BATTERY
          </div>
          <div class="text-value leading-none font-bold tabular-nums text-ink">
            {power.charge ? formatValue(power.charge) : '—'}
          </div>
        </div>

        <div class="min-w-0">
          <div class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1 truncate">
            {power.source}
          </div>
          <div class="text-value leading-none font-bold text-ink-secondary">
            {power.status ? formatValue(power.status) : '—'}
          </div>
        </div>
      </div>

      {#if power.degraded}
        <p class="text-meta text-warn mt-4">
          These readings are not current. Conditions that depend on them cannot
          be satisfied, so the plan will not act on them.
        </p>
      {/if}
    {:else}
      <p class="text-body text-ink-muted">
        No power source is reporting. Check your source configuration.
      </p>
    {/if}
  </div>

  {#if seq}
    <div class="px-5 sm:px-6 py-3 border-t border-amber/20 bg-surface-0/30
      flex flex-wrap items-center gap-x-6 gap-y-1 text-meta">
      <span class="text-ink-muted">
        Started <span class="text-ink-secondary">{timeAgo(seq.started_at)}</span>
      </span>
      <span class="text-ink-muted">
        Abort
        <span class={seq.abortable ? 'text-ok' : 'text-danger'}>
          {seq.abortable ? 'still available' : 'no longer possible'}
        </span>
      </span>
      {#if seq.held_stage}
        <span class="text-warn">Stage "{seq.held_stage}" is holding</span>
      {/if}
    </div>
  {/if}
</section>

<!-- ── Who is affected ────────────────────────────────────────────── -->
<Section title="CLIENTS" id="clients-heading">
  <span slot="aside" class="text-meta text-ink-muted">
    {$clients.length} configured
  </span>

  {#if $loading && $clients.length === 0}
    <div class="space-y-2" aria-hidden="true">
      {#each [1, 2, 3] as i (i)}
        <div class="h-12 rounded-[var(--radius-sm)] bg-surface-50 animate-pulse"></div>
      {/each}
    </div>
  {:else if $clients.length === 0}
    <Card>
      <p class="text-body text-ink-muted text-center py-4">
        No clients configured. Add them to your configuration file.
      </p>
    </Card>
  {:else}
    <!-- Cards on a phone, table on a desktop. The table used to render at
         every width, so on 390px only the STATE column was reachable. -->
    <div class="sm:hidden space-y-2">
      {#each shownClients as client (client.name)}
        <Card>
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="text-body text-ink font-bold truncate">{client.name}</div>
              <div class="text-meta text-ink-muted mt-0.5 truncate">
                {client.transport} · {client.address || 'no address'}
              </div>
            </div>
            <span class="flex items-center gap-1.5 shrink-0">
              <StatusDot tone={stateTone(client.state)} />
              <span class="text-meta text-ink-secondary">{stateLabel(client.state)}</span>
            </span>
          </div>
          {#if stateNote(client.state)}
            <p class="text-meta text-ink-muted mt-2">{stateNote(client.state)}</p>
          {/if}
        </Card>
      {/each}
    </div>

    <div class="hidden sm:block border border-edge rounded-[var(--radius-md)] overflow-hidden">
      <table class="w-full text-body">
        <caption class="sr-only">Configured clients and their current state</caption>
        <thead>
          <tr class="bg-surface-100 text-ink-muted text-eyebrow tracking-[0.12em]">
            <th scope="col" class="text-left px-4 py-2.5 font-bold">STATE</th>
            <th scope="col" class="text-left px-4 py-2.5 font-bold">NAME</th>
            <th scope="col" class="text-left px-4 py-2.5 font-bold">TRANSPORT</th>
            <th scope="col" class="text-left px-4 py-2.5 font-bold">ADDRESS</th>
            <th scope="col" class="text-left px-4 py-2.5 font-bold">TAGS</th>
          </tr>
        </thead>
        <tbody>
          {#each shownClients as client (client.name)}
            <tr class="border-t border-edge-subtle hover:bg-surface-50 transition-colors">
              <td class="px-4 py-2.5">
                <span class="inline-flex items-center gap-2">
                  <StatusDot tone={stateTone(client.state)} />
                  <span class="text-ink-secondary">{stateLabel(client.state)}</span>
                </span>
                {#if stateNote(client.state)}
                  <span class="block text-meta text-ink-muted mt-0.5 max-w-xs">
                    {stateNote(client.state)}
                  </span>
                {/if}
              </td>
              <td class="px-4 py-2.5 text-ink font-bold">{client.name}</td>
              <td class="px-4 py-2.5 text-ink-secondary">{client.transport}</td>
              <td class="px-4 py-2.5 text-ink-muted tabular-nums">{client.address || '—'}</td>
              <td class="px-4 py-2.5">
                <span class="flex flex-wrap gap-1">
                  {#each (client.tags || []) as tag (tag)}
                    <span class="px-1.5 py-0.5 bg-surface-200 text-ink-muted text-eyebrow rounded-[var(--radius-sm)]">
                      {tag}
                    </span>
                  {/each}
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Section>

<!-- ── Why ────────────────────────────────────────────────────────── -->
<Section title="FACTS" id="facts-heading">
  <span slot="aside" class="text-meta">
    {#if problemFacts.length > 0}
      <span class="text-warn">
        {problemFacts.length} not reporting — conditions reading them cannot be satisfied
      </span>
    {:else}
      <span class="text-ink-muted">all reporting</span>
    {/if}
  </span>

  {#if $loading && sortedFacts.length === 0}
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-2" aria-hidden="true">
      {#each [1, 2, 3, 4] as i (i)}
        <div class="h-20 rounded-[var(--radius-sm)] bg-surface-50 animate-pulse"></div>
      {/each}
    </div>
  {:else if sortedFacts.length === 0}
    <Card>
      <p class="text-body text-ink-muted text-center py-4">
        No facts received. Check your source configuration.
      </p>
    </Card>
  {:else}
    <!-- Reference density, deliberately quieter than the headline: these
         are the readings behind the number above, not a second focal point. -->
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-2">
      {#each shownFacts as [key, fact] (key)}
        {@const parts = splitFactKey(key)}
        <div
          class="border rounded-[var(--radius-sm)] px-3 py-2.5 bg-surface-50
            {fact.quality === 'good' ? 'border-edge' : 'border-warn/40'}"
          title={fact.description || key}
        >
          <div class="text-meta leading-tight" title={key}>
            {#if parts.source}
              <span class="text-ink-faint">{parts.source}</span>
            {/if}
            <span class="text-ink-muted block break-words">{parts.reading}</span>
          </div>
          <div
            class="text-value font-bold tabular-nums mt-1 break-words
              {fact.quality === 'good' ? 'text-ink' : 'text-ink-muted'}"
          >
            {formatValue(fact)}<span class="text-meta font-normal text-ink-muted"
              >{unitSuffix(fact)}</span
            >
          </div>
          <div class="flex items-center gap-1.5 mt-1.5">
            <StatusDot tone={qualityTone(fact.quality)} />
            <span class="text-meta text-ink-muted">{fact.quality}</span>
            {#if fact.updated_at}
              <span class="text-meta text-ink-faint ml-auto">{timeAgo(fact.updated_at)}</span>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if unsupportedFacts.length > 0}
    <p class="text-meta text-ink-muted mt-3">
      Not provided by this hardware:
      {unsupportedFacts.map(([k]) => k).join(', ')}.
      <span class="text-ink-faint">
        The source is reporting, but has never sent these readings.
      </span>
    </p>
  {/if}
</Section>

<!-- ── History ────────────────────────────────────────────────────── -->
<Section title="EVENT LOG" id="events-heading">
  <div class="border border-edge rounded-[var(--radius-md)] max-h-80 overflow-y-auto">
    {#each $events as evt, i (evt.timestamp + '-' + i)}
      {@const described = describeEvent(evt)}
      <div
        class="flex items-baseline gap-3 px-4 py-2 text-body
          {i > 0 ? 'border-t border-edge-subtle' : ''}"
      >
        <time
          class="text-meta text-ink-faint tabular-nums shrink-0"
          datetime={evt.timestamp}
        >
          {new Date(evt.timestamp).toLocaleTimeString([], {
            hour: '2-digit', minute: '2-digit', second: '2-digit',
          })}
        </time>
        <span class={eventToneClass[described.tone]}>{described.text}</span>
      </div>
    {/each}
    {#if $events.length === 0}
      <p class="px-4 py-6 text-center text-ink-muted text-body">
        {$loading ? 'Connecting to the event stream…' : 'Nothing has happened yet.'}
      </p>
    {/if}
  </div>
</Section>
