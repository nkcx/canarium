<script>
  import ConditionTree from './ConditionTree.svelte';
  import StatusDot from './StatusDot.svelte';
  import {
    describeCondition, isGroup, resultTone, resultLabel, currentReading, dwellText,
  } from '../conditions.js';

  /** An explanation from /api/plans. */
  export let ex;
  export let depth = 0;

  $: tone = resultTone(ex?.result, ex);
  $: label = resultLabel(ex?.result, ex);
  $: reading = currentReading(ex);
  $: dwell = dwellText(ex);

  const toneText = { ok: 'text-ok', live: 'text-canary', neutral: 'text-ink-muted', warn: 'text-warn' };
</script>

<div class={depth > 0 ? 'pl-4 border-l border-edge-subtle' : ''}>
  <div class="flex items-start gap-2 py-1">
    <span class="mt-[7px]"><StatusDot {tone} {label} /></span>
    <div class="min-w-0">
      <span class="text-body {isGroup(ex) ? 'text-ink-secondary' : 'text-ink'} break-words">
        {describeCondition(ex)}
      </span>
      <span class="text-meta {toneText[tone]} whitespace-nowrap">
        — {label}
      </span>
      {#if reading || dwell}
        <div class="text-meta text-ink-muted">
          {[reading, dwell].filter(Boolean).join('; ')}
        </div>
      {/if}
    </div>
  </div>

  {#if ex?.conditions?.length}
    {#each ex.conditions as child, i (i)}
      <ConditionTree ex={child} depth={depth + 1} />
    {/each}
  {/if}
</div>
