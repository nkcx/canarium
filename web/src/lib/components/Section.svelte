<script>
  /**
   * A titled region.
   *
   * The heading string `text-[10px] text-ink-muted tracking-wider` was
   * duplicated sixteen times across four routes, which is how four
   * different spellings of the same idea drifted in.
   */
  export let title;
  export let id = undefined;
</script>

<section class="mb-8" aria-labelledby={id}>
  <div class="flex items-baseline gap-3 mb-3 flex-wrap">
    <h2 {id} class="text-eyebrow text-ink-muted tracking-[0.12em] font-bold">
      {title}
    </h2>
    <slot name="aside" />
  </div>
  <slot />
</section>
