<script>
  import { plans, sequence, loading } from '../lib/stores/api.js';
  import Card from '../lib/components/Card.svelte';
  import Chip from '../lib/components/Chip.svelte';

  let expanded = null;

  function toggle(name) {
    expanded = expanded === name ? null : name;
  }
</script>

<div class="flex items-center justify-between gap-3 mb-6">
  <h1 class="text-label font-bold text-ink">Plans</h1>
  <span class="text-meta text-ink-muted">{$plans.length} configured</span>
</div>

{#if $loading && $plans.length === 0}
  <div class="space-y-2" aria-hidden="true">
    {#each [1, 2] as i (i)}
      <div class="h-14 rounded-[var(--radius-sm)] bg-surface-50 animate-pulse"></div>
    {/each}
  </div>
{:else if $plans.length === 0}
  <Card>
    <p class="text-body text-ink-muted text-center py-6">
      No plans configured. Add plans to your configuration file.
    </p>
  </Card>
{:else}
  <div class="space-y-2 max-w-3xl">
    {#each $plans as plan (plan.name)}
      {@const active = $sequence?.plan === plan.name}
      <div
        class="border rounded-[var(--radius-md)] overflow-hidden
          {active ? 'border-amber/30 bg-amber/5' : 'border-edge bg-surface-50'}"
      >
        <button
          class="w-full text-left px-4 py-3 min-h-14 flex items-center justify-between gap-3
            hover:bg-surface-100/50 transition-colors"
          aria-expanded={expanded === plan.name}
          onclick={() => toggle(plan.name)}
        >
          <span class="flex items-center gap-3 flex-wrap min-w-0">
            <span class="text-body font-bold text-ink truncate">{plan.name}</span>
            <span class="text-meta text-ink-muted">{plan.stages} stages</span>
            {#if active}
              <!-- Three breaths on arrival, then still. The old badge pulsed
                   for the whole sequence, which can be forty minutes. -->
              <span class="animate-breathe"><Chip tone="live">RUNNING</Chip></span>
            {/if}
          </span>
          <span class="text-ink-faint text-body shrink-0" aria-hidden="true">
            {expanded === plan.name ? '▾' : '▸'}
          </span>
        </button>

        {#if expanded === plan.name}
          <div class="px-4 pb-4 pt-3 border-t border-edge-subtle">
            {#if active}
              <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-body mb-3">
                <dt class="text-ink-muted">State</dt>
                <dd class="text-amber text-right">{$sequence.state?.replace(/_/g, ' ')}</dd>
                <dt class="text-ink-muted">Stage</dt>
                <dd class="text-ink text-right">
                  {($sequence.current_stage ?? 0) + 1} of {$sequence.total_stages ?? '?'}
                </dd>
                <dt class="text-ink-muted">Point of no return</dt>
                <dd class="text-right {$sequence.ponr_crossed ? 'text-danger' : 'text-ok'}">
                  {$sequence.ponr_crossed ? 'crossed' : 'not crossed'}
                </dd>
              </dl>
            {/if}
            <p class="text-meta text-ink-muted">
              Plan details are read from the configuration file. Edit the YAML to
              modify plans.
            </p>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}
