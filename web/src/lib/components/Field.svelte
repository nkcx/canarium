<script>
  /**
   * A labelled input.
   *
   * There were previously zero <label> elements in the app: every field was
   * identified by placeholder alone, so the moment you typed, the field
   * stopped saying what it was -- and screen readers never heard it at all.
   */
  export let label;
  export let value = '';
  export let type = 'text';
  export let id;
  export let placeholder = '';
  export let autocomplete = undefined;
  export let hint = '';
  export let invalid = false;
  export let describedBy = undefined;

  $: hintId = hint ? `${id}-hint` : undefined;
  $: described = [describedBy, hintId].filter(Boolean).join(' ') || undefined;

  // bind:value cannot take a dynamic `type`, so the handler does it.
  function onInput(e) {
    value = e.currentTarget.value;
  }
</script>

<div class="mb-3">
  <label for={id} class="block text-eyebrow text-ink-muted tracking-[0.12em] font-bold mb-1.5">
    {label}
  </label>
  <input
    {id}
    {type}
    {placeholder}
    {autocomplete}
    value={value}
    on:input={onInput}
    aria-invalid={invalid || undefined}
    aria-describedby={described}
    class="w-full min-h-10 px-3 py-2 bg-surface-100 border rounded-[var(--radius-sm)]
      text-ink text-body placeholder:text-ink-faint transition-colors
      {invalid ? 'border-danger/60' : 'border-edge hover:border-edge-strong'}"
  />
  {#if hint}
    <p id={hintId} class="text-meta text-ink-muted mt-1.5">{hint}</p>
  {/if}
</div>
