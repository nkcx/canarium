<script>
  /**
   * A small state label. Tone carries the meaning, so the palette stays
   * honest: see the colour contract in app.css.
   */

  /** live | ok | warn | danger | info | neutral */
  export let tone = 'neutral';
  export let title = undefined;

  const tones = {
    live: 'bg-amber/15 text-amber border-amber/30',
    ok: 'bg-ok/15 text-ok border-ok/30',
    warn: 'bg-warn/15 text-warn border-warn/30',
    danger: 'bg-danger/15 text-danger border-danger/30',
    info: 'bg-info/15 text-info border-info/30',
    neutral: 'bg-surface-200 text-ink-secondary border-edge',
  };
</script>

<span
  {title}
  class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-[var(--radius-sm)]
    border text-eyebrow font-bold tracking-wider whitespace-nowrap {tones[tone]}"
>
  <slot />
</span>
