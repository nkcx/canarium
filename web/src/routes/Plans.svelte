<script>
  import { plans, sequence } from '../lib/stores/api.js';

  let selected = null;
</script>

<div class="p-6">
  <div class="flex items-center justify-between mb-6">
    <h1 class="text-sm font-bold text-ink tracking-wider">PLANS</h1>
    <span class="text-[10px] text-ink-muted">{$plans.length} configured</span>
  </div>

  <div class="space-y-3">
    {#each $plans as plan}
      <div class="border border-edge rounded-[var(--radius-sm)] bg-surface-50">
        <button
          class="w-full text-left px-4 py-3 flex items-center justify-between"
          onclick={() => selected = selected === plan.name ? null : plan.name}
        >
          <div class="flex items-center gap-3">
            <span class="text-xs font-bold text-ink">{plan.name}</span>
            <span class="text-[10px] text-ink-muted">{plan.stages} stages</span>
            {#if $sequence?.plan === plan.name}
              <span class="px-1.5 py-0.5 text-[9px] font-bold rounded bg-amber/20 text-amber animate-pulse">
                ACTIVE
              </span>
            {/if}
          </div>
          <span class="text-ink-faint text-xs">{selected === plan.name ? '▾' : '▸'}</span>
        </button>

        {#if selected === plan.name}
          <div class="px-4 pb-4 pt-2 border-t border-edge-subtle">
            <div class="text-[10px] text-ink-muted mb-3">
              Plan details are read from the configuration file. Edit the YAML to modify plans.
            </div>

            {#if $sequence?.plan === plan.name}
              <div class="border border-amber/30 rounded-[var(--radius-sm)] p-3 bg-amber/5">
                <div class="text-[10px] text-ink-muted tracking-wider mb-1">ACTIVE SEQUENCE</div>
                <div class="text-xs text-ink">
                  State: <span class="text-amber">{$sequence.state}</span>
                  · Stage: {$sequence.current_stage}
                  {#if $sequence.ponr_crossed}
                    · <span class="text-danger">PONR crossed</span>
                  {/if}
                </div>
              </div>
            {/if}
          </div>
        {/if}
      </div>
    {/each}
    {#if $plans.length === 0}
      <div class="text-center py-12 text-ink-muted text-xs">
        No plans configured. Add plans to your configuration file.
      </div>
    {/if}
  </div>
</div>
