<script>
  /** default | live | danger | warn */
  export let tone = 'default';
  export let padded = true;

  const tones = {
    default: 'border-edge bg-surface-50',
    live: 'border-canary/30 bg-canary/5',
    danger: 'border-danger/40 bg-danger/5',
    warn: 'border-warn/40 bg-warn/5',
  };
</script>

<div
  class="border rounded-[var(--radius-md)] {tones[tone]} {padded ? 'p-4' : ''}"
>
  <slot />
</div>
