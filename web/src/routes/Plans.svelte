<script>
  import { plans, sequence, loading, refreshAll } from '../lib/stores/api.js';
  import { onMount } from 'svelte';
  import Card from '../lib/components/Card.svelte';
  import Chip from '../lib/components/Chip.svelte';
  import Section from '../lib/components/Section.svelte';
  import ConditionTree from '../lib/components/ConditionTree.svelte';
  import { resultLabel } from '../lib/conditions.js';

  // Conditions change with the facts, and this page exists to show them
  // live. The app-wide poll is 15s; while this page is open, tighten it so
  // a dwell timer visibly counts.
  onMount(() => {
    const t = setInterval(refreshAll, 5000);
    return () => clearInterval(t);
  });

  function stageClientsLabel(stage) {
    if (stage.clients.length === 0) return 'nothing — matches no clients';
    return stage.clients.join(', ');
  }

  function meta(stage) {
    const parts = [];
    if (stage.budget) parts.push(`budget ${stage.budget}`);
    if (stage.wait_timeout) parts.push(`waits up to ${stage.wait_timeout}`);
    if (stage.wait_policy) parts.push(`then ${stage.wait_policy}s`);
    return parts.join(' · ');
  }
</script>

<div class="flex items-center justify-between gap-3 mb-6">
  <h1 class="text-label font-bold text-ink">Plans</h1>
  <span class="text-meta text-ink-muted">{$plans.length} configured</span>
</div>

{#if $loading && $plans.length === 0}
  <div class="space-y-2" aria-hidden="true">
    {#each [1, 2] as i (i)}
      <div class="h-32 rounded-[var(--radius-sm)] bg-surface-50 animate-pulse"></div>
    {/each}
  </div>
{:else if $plans.length === 0}
  <Card>
    <p class="text-body text-ink-muted text-center py-6">
      No plans configured. Add plans to your configuration file.
    </p>
  </Card>
{:else}
  <div class="max-w-3xl">
    {#each $plans as plan (plan.name)}
      {@const active = $sequence?.plan === plan.name}
      {@const emptyStages = (plan.shutdown ?? []).filter(s => s.clients.length === 0)}

      <section
        class="mb-10 border rounded-[var(--radius-lg)] overflow-hidden
          {active ? 'border-canary/30 bg-canary/5' : 'border-edge bg-surface-50'}"
        aria-labelledby="plan-{plan.name}"
      >
        <header class="px-5 py-4 border-b border-edge flex items-center gap-3 flex-wrap">
          <h2 id="plan-{plan.name}" class="text-label font-bold text-ink">{plan.name}</h2>
          {#if active}
            <span class="animate-breathe"><Chip tone="live">RUNNING</Chip></span>
          {:else if plan.trigger?.result === 'true'}
            <Chip tone="live">TRIGGER HOLDS</Chip>
          {/if}
          <span class="text-meta text-ink-muted ml-auto">
            {plan.stages} {plan.stages === 1 ? 'stage' : 'stages'}
          </span>
        </header>

        <div class="p-5">
          {#if emptyStages.length === (plan.shutdown ?? []).length && emptyStages.length > 0}
            <!-- The lembas case: every stage's tags match nothing. The plan
                 would trigger on an outage and shut down nothing at all. -->
            <div class="mb-5">
              <Card tone="warn">
                <p class="text-body text-warn font-bold">This plan would shut nothing down</p>
                <p class="text-meta text-ink-secondary mt-1">
                  None of its stages match a configured client. If it triggered
                  now, the sequence would run through every stage and act on
                  nothing.
                </p>
              </Card>
            </div>
          {/if}

          <Section title="TRIGGERS WHEN" id="{plan.name}-trigger">
            <ConditionTree ex={plan.trigger} />
          </Section>

          {#if plan.abort}
            <Section title="CALLED OFF WHEN" id="{plan.name}-abort">
              <ConditionTree ex={plan.abort} />
              <p class="text-meta text-ink-muted mt-1">
                Only before the point of no return.
              </p>
            </Section>
          {/if}

          <Section title="SHUTS DOWN, IN ORDER" id="{plan.name}-stages">
            <ol class="space-y-3">
              {#each plan.shutdown ?? [] as stage, i (stage.name)}
                <li class="border border-edge-subtle rounded-[var(--radius-md)] p-3 bg-surface-0/30">
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="text-meta text-ink-faint tabular-nums">{i + 1}</span>
                    <span class="text-body font-bold text-ink">{stage.name}</span>
                    {#if stage.point_of_no_return}
                      <Chip tone="danger" title="Once this stage begins, the sequence can no longer be aborted">
                        POINT OF NO RETURN
                      </Chip>
                    {/if}
                  </div>

                  <div class="mt-2 text-body">
                    <span class="text-ink-muted">Shuts down</span>
                    <span class={stage.clients.length ? 'text-ink' : 'text-warn'}>
                      {stageClientsLabel(stage)}
                    </span>
                  </div>
                  {#if stage.unmatched?.length}
                    <p class="text-meta text-warn mt-0.5">
                      {stage.unmatched.join(', ')}
                      {stage.unmatched.length === 1 ? 'matches' : 'match'} no configured client.
                    </p>
                  {/if}

                  <div class="mt-2">
                    <div class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold">WHEN</div>
                    <ConditionTree ex={stage.when} />
                  </div>

                  {#if meta(stage)}
                    <p class="text-meta text-ink-muted mt-1">{meta(stage)}</p>
                  {/if}
                </li>
              {/each}
            </ol>
          </Section>

          {#if plan.post_shutdown}
            <Section title="AFTER THE LAST STAGE" id="{plan.name}-post">
              <p class="text-body text-ink">
                Sends <span class="font-bold">{plan.post_shutdown.command}</span>
                to {plan.post_shutdown.ups}, after {plan.post_shutdown.delay}s.
              </p>
            </Section>
          {/if}

          <Section title="WAKES WHEN" id="{plan.name}-wake">
            {#if plan.wake?.gate_missing}
              <Card tone="warn">
                <p class="text-body text-warn font-bold">No wake gate</p>
                <p class="text-meta text-ink-secondary mt-1">
                  Clients would be woken as soon as the shutdown finishes, while
                  whatever triggered it is most likely still true — so the plan
                  would trigger again and repeat.
                </p>
              </Card>
            {:else}
              <ConditionTree ex={plan.wake?.gate} />
            {/if}
            <p class="text-meta text-ink-muted mt-2">
              {[
                plan.wake?.order && `${plan.wake.order} order`,
                plan.wake?.stagger && `${plan.wake.stagger} apart`,
                plan.wake?.boot_deadline && `${plan.wake.boot_deadline} to boot`,
                plan.wake?.retries && `${plan.wake.retries} retries`,
              ].filter(Boolean).join(' · ')}
            </p>
          </Section>

          <p class="text-meta text-ink-faint">
            Read from the configuration file. Trigger currently
            {resultLabel(plan.trigger?.result)}.
          </p>
        </div>
      </section>
    {/each}
  </div>
{/if}
