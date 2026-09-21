<script>
  /**
   * The one button in the app.
   *
   * Settings alone previously had three unrelated treatments assembled from
   * one-off class strings, none of which had a focus state, and the nav
   * buttons came out ~32px tall on an interface whose stated primary
   * context is a phone.
   */

  /** primary | default | danger | ghost */
  export let variant = 'default';
  /** md (40px, the tap-target floor) | lg */
  export let size = 'md';
  export let type = 'button';
  export let disabled = false;
  export let title = undefined;
  export let full = false;

  const variants = {
    primary:
      'bg-amber/10 border-amber/40 text-amber hover:bg-amber/20 hover:border-amber/60',
    default:
      'bg-surface-100 border-edge text-ink-secondary hover:border-edge-strong hover:text-ink',
    danger:
      'bg-transparent border-danger/50 text-danger hover:bg-danger/10 hover:border-danger',
    ghost:
      'bg-transparent border-transparent text-ink-muted hover:text-ink hover:bg-surface-50',
  };

  // min-h rather than a fixed height so a wrapped label still grows.
  const sizes = {
    md: 'min-h-10 px-3.5 py-2 text-meta',
    lg: 'min-h-11 px-4 py-2.5 text-body',
  };
</script>

<button
  {type}
  {disabled}
  {title}
  class="inline-flex items-center justify-center gap-2 border rounded-[var(--radius-sm)]
    font-medium transition-colors duration-150 ease-out
    active:scale-[0.98] motion-reduce:active:scale-100
    disabled:opacity-35 disabled:pointer-events-none
    {variants[variant]} {sizes[size]} {full ? 'w-full' : ''}"
  on:click
>
  <slot />
</button>
