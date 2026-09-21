<script>
  /** live | ok | warn | danger | neutral */
  export let tone = 'neutral';
  export let label = '';

  const tones = {
    live: 'bg-amber',
    ok: 'bg-ok',
    warn: 'bg-warn',
    danger: 'bg-danger',
    neutral: 'bg-ink-faint',
  };
</script>

<!-- aria-hidden: the dot restates a state that is always also in text, so
     announcing it again would just be noise. -->
<span
  class="inline-block w-1.5 h-1.5 rounded-full shrink-0 {tones[tone]}"
  aria-hidden="true"
  title={label}
></span>
