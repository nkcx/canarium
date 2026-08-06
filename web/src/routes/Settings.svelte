<script>
  import { status, setMode, abortSequence } from '../lib/stores/api.js';

  let modeChanging = false;
  let aborting = false;

  const modes = [
    { id: 'disarmed', label: 'Disarmed', desc: 'Sources poll, conditions evaluate, nothing executes.' },
    { id: 'dry-run', label: 'Dry Run', desc: 'Full sequences with timing, transports log instead of acting.' },
    { id: 'armed', label: 'Armed', desc: 'Live execution. Plans will trigger and execute.' },
  ];

  async function changeMode(mode) {
    modeChanging = true;
    await setMode(mode);
    modeChanging = false;
  }

  async function handleAbort() {
    if (!confirm('Abort the active sequence?')) return;
    aborting = true;
    await abortSequence();
    aborting = false;
  }
</script>

<div class="p-6">
  <h1 class="text-sm font-bold text-ink tracking-wider mb-6">SETTINGS</h1>

  <!-- Mode -->
  <section class="mb-8">
    <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">MODE</h2>
    <div class="space-y-2">
      {#each modes as mode}
        <button
          class="w-full text-left px-4 py-3 border rounded-[var(--radius-sm)] transition-colors
            {$status?.mode === mode.id
              ? 'border-amber bg-amber/5 text-ink'
              : 'border-edge bg-surface-50 text-ink-secondary hover:border-edge-strong'}"
          onclick={() => changeMode(mode.id)}
          disabled={modeChanging}
        >
          <div class="flex items-center gap-2">
            <span class="text-xs font-bold">{mode.label}</span>
            {#if $status?.mode === mode.id}
              <span class="text-[9px] px-1.5 py-0.5 rounded bg-amber/20 text-amber">ACTIVE</span>
            {/if}
          </div>
          <div class="text-[10px] text-ink-muted mt-0.5">{mode.desc}</div>
        </button>
      {/each}
    </div>
  </section>

  <!-- Sequence Control -->
  {#if $status?.sequence}
    <section class="mb-8">
      <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">SEQUENCE CONTROL</h2>
      <button
        class="px-4 py-2 border border-danger/50 text-danger text-xs rounded-[var(--radius-sm)]
          hover:bg-danger/10 transition-colors disabled:opacity-40"
        onclick={handleAbort}
        disabled={aborting}
      >
        {aborting ? 'Aborting...' : 'Abort Sequence'}
      </button>
    </section>
  {/if}

  <!-- Info -->
  <section>
    <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">SYSTEM</h2>
    <div class="border border-edge rounded-[var(--radius-sm)] bg-surface-50 p-4 space-y-2 text-xs">
      <div class="flex justify-between">
        <span class="text-ink-muted">Config</span>
        <span class="text-ink-secondary">File-canonical (YAML)</span>
      </div>
      <div class="flex justify-between">
        <span class="text-ink-muted">Auth</span>
        <span class="text-ink-secondary">Local admin</span>
      </div>
      <div class="flex justify-between">
        <span class="text-ink-muted">Storage</span>
        <span class="text-ink-secondary">SQLite WAL</span>
      </div>
    </div>
  </section>
</div>
