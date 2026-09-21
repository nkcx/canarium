<script>
  import { tick } from 'svelte';
  import Button from './Button.svelte';

  /**
   * The confirmation for an action that cannot be taken back.
   *
   * This replaces native confirm(), which guarded arming the executor,
   * aborting a sequence and forcing a stage past its entry condition --
   * the three most consequential things this product does. confirm() is
   * unstyleable, can be permanently suppressed by the browser with a
   * "don't show me these again" checkbox, and on a phone renders as a
   * system sheet that looks nothing like the app. For "these machines
   * will be shut down now", the operator deserves to see *which* machines.
   */

  export let open = false;
  export let title;
  export let confirmLabel = 'Confirm';
  export let confirmVariant = 'danger';
  export let onconfirm = () => {};
  export let oncancel = () => {};
  export let busy = false;

  let dialogEl;
  let confirmEl;
  let previouslyFocused = null;

  // Focus moves into the dialog on open and returns to whatever opened it
  // on close, so a keyboard user is never dropped at the top of the page.
  $: if (open) {
    previouslyFocused = document.activeElement;
    tick().then(() => confirmEl?.focus?.());
  } else if (previouslyFocused) {
    previouslyFocused.focus?.();
    previouslyFocused = null;
  }

  function cancel() {
    if (busy) return;
    oncancel();
  }

  function onKeydown(e) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      cancel();
      return;
    }
    if (e.key !== 'Tab') return;

    // Keep Tab inside the dialog: an action this serious should not let
    // focus wander back to the page behind it.
    const focusable = dialogEl?.querySelectorAll(
      'button:not([disabled]), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
    );
    if (!focusable?.length) return;

    const first = focusable[0];
    const last = focusable[focusable.length - 1];

    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
</script>

<svelte:window on:keydown={open ? onKeydown : undefined} />

{#if open}
  <div
    class="fixed inset-0 z-50 flex items-end sm:items-center justify-center
      bg-surface-0/80 p-0 sm:p-6"
  >
    <!-- Dismiss-by-backdrop is a pointer convenience. It is a real button
         so it is not a div pretending to be interactive, but it stays out
         of the tab order: Escape and Cancel are the keyboard paths, and a
         full-screen tab stop in front of the dialog would be noise. -->
    <button
      type="button"
      tabindex="-1"
      aria-hidden="true"
      class="absolute inset-0 w-full h-full cursor-default"
      onclick={cancel}
    ></button>

    <div
      bind:this={dialogEl}
      role="alertdialog"
      tabindex="-1"
      aria-modal="true"
      aria-labelledby="confirm-title"
      aria-describedby="confirm-body"
      class="relative w-full sm:max-w-md bg-surface-50 border border-edge-strong
        rounded-t-[var(--radius-lg)] sm:rounded-[var(--radius-lg)]
        shadow-2xl shadow-black/50"
    >
      <div class="p-5 border-b border-edge">
        <h2 id="confirm-title" class="text-label font-bold text-ink">{title}</h2>
      </div>

      <div id="confirm-body" class="p-5 text-body text-ink-secondary space-y-3">
        <slot />
      </div>

      <div class="p-5 pt-0 flex flex-col-reverse sm:flex-row sm:justify-end gap-2">
        <Button variant="default" size="lg" on:click={cancel} disabled={busy}>
          Cancel
        </Button>
        <button
          bind:this={confirmEl}
          type="button"
          disabled={busy}
          onclick={onconfirm}
          class="inline-flex items-center justify-center gap-2 border rounded-[var(--radius-sm)]
            font-medium transition-colors duration-150 ease-out min-h-11 px-4 py-2.5 text-body
            active:scale-[0.98] motion-reduce:active:scale-100
            disabled:opacity-35 disabled:pointer-events-none
            {confirmVariant === 'danger'
              ? 'bg-danger/15 border-danger/60 text-danger hover:bg-danger/25'
              : 'bg-amber/15 border-amber/60 text-amber hover:bg-amber/25'}"
        >
          {busy ? 'Working…' : confirmLabel}
        </button>
      </div>
    </div>
  </div>
{/if}
