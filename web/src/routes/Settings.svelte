<script>
  import {
    status,
    setMode,
    abortSequence,
    proceedStage,
    changePassword,
    minPasswordLength,
    passwordPinned,
    configReadonly,
  } from '../lib/stores/api.js';

  let modeChanging = false;
  let aborting = false;
  let proceeding = false;
  let error = '';

  const modes = [
    { id: 'disarmed', label: 'Disarmed', desc: 'Sources poll, conditions evaluate, nothing executes.' },
    { id: 'dry-run', label: 'Dry Run', desc: 'Full sequences with timing, transports log instead of acting.' },
    { id: 'armed', label: 'Armed', desc: 'Live execution. Plans will trigger and execute.' },
  ];

  $: sequence = $status?.sequence ?? null;
  $: abortable = sequence?.abortable ?? false;
  $: heldStage = sequence?.held_stage ?? '';

  async function changeMode(mode) {
    if ($configReadonly) {
      error =
        'config_readonly is set; change canarium.mode in the configuration file and restart.';
      return;
    }

    if (mode === 'armed' && $status?.mode !== 'armed') {
      const ok = confirm(
        'Arm Canarium?\n\n' +
        'Plans will trigger on their conditions and shut down real machines.',
      );
      if (!ok) return;
    }

    modeChanging = true;
    error = '';
    const result = await setMode(mode);
    if (!result.ok) error = result.error;
    modeChanging = false;
  }

  async function handleAbort() {
    const plan = sequence?.plan ?? 'the active sequence';
    if (!confirm(`Abort ${plan}?\n\nIn-flight shutdowns will be allowed to finish.`)) return;

    aborting = true;
    error = '';
    const result = await abortSequence();
    if (!result.ok) error = result.error;
    aborting = false;
  }

  let currentPassword = '';
  let newPassword = '';
  let confirmPassword = '';
  let changingPassword = false;
  let passwordError = '';

  $: passwordTooShort =
    newPassword.length > 0 && newPassword.length < $minPasswordLength;
  $: passwordMismatch =
    confirmPassword.length > 0 && newPassword !== confirmPassword;
  $: canChangePassword =
    !changingPassword &&
    currentPassword.length > 0 &&
    newPassword.length >= $minPasswordLength &&
    newPassword === confirmPassword;

  async function handlePasswordChange() {
    if (!canChangePassword) return;

    changingPassword = true;
    passwordError = '';

    const result = await changePassword(currentPassword, newPassword);
    if (!result.ok) {
      passwordError = result.error;
      changingPassword = false;
      return;
    }
    // On success every session is invalidated, including this one, so the
    // app returns to the login screen on its own.
  }

  async function handleProceed() {
    const ok = confirm(
      `Force stage "${heldStage}" to proceed?\n\n` +
      'Its entry condition has not been met. These clients will be shut down now.',
    );
    if (!ok) return;

    proceeding = true;
    error = '';
    const result = await proceedStage();
    if (!result.ok) error = result.error;
    proceeding = false;
  }
</script>

<div class="p-6">
  <h1 class="text-sm font-bold text-ink tracking-wider mb-6">SETTINGS</h1>

  <!-- Mode -->
  <section class="mb-8">
    <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">MODE</h2>
    {#if $configReadonly}
      <div class="text-[10px] text-ink-muted mb-2">
        Read-only: the mode comes from the configuration file.
      </div>
    {/if}
    <div class="space-y-2">
      {#each modes as mode (mode.id)}
        <button
          class="w-full text-left px-4 py-3 border rounded-[var(--radius-sm)] transition-colors
            {$status?.mode === mode.id
              ? 'border-amber bg-amber/5 text-ink'
              : 'border-edge bg-surface-50 text-ink-secondary hover:border-edge-strong'}"
          onclick={() => changeMode(mode.id)}
          disabled={modeChanging || $configReadonly}
          title={$configReadonly
            ? 'config_readonly is set; the mode comes from the configuration file'
            : mode.desc}
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
  {#if sequence}
    <section class="mb-8">
      <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">SEQUENCE CONTROL</h2>

      <div class="border border-edge rounded-[var(--radius-sm)] bg-surface-50 p-4 mb-3 space-y-1.5 text-xs">
        <div class="flex justify-between">
          <span class="text-ink-muted">Plan</span>
          <span class="text-ink">{sequence.plan}</span>
        </div>
        <div class="flex justify-between">
          <span class="text-ink-muted">State</span>
          <span class="text-ink">{sequence.state}</span>
        </div>
        <div class="flex justify-between">
          <span class="text-ink-muted">Stage</span>
          <span class="text-ink">
            {sequence.stage_name || '—'}
            <span class="text-ink-faint">
              ({sequence.current_stage + 1} of {sequence.total_stages})
            </span>
          </span>
        </div>
        <div class="flex justify-between">
          <span class="text-ink-muted">Point of no return</span>
          <span class={sequence.ponr_crossed ? 'text-danger' : 'text-ink-secondary'}>
            {sequence.ponr_crossed ? 'crossed' : 'not crossed'}
          </span>
        </div>
      </div>

      {#if heldStage}
        <div class="border border-warn/40 bg-warn/5 rounded-[var(--radius-sm)] p-3 mb-3">
          <div class="text-warn text-xs font-bold">Stage "{heldStage}" is holding</div>
          <div class="text-ink-muted text-[10px] mt-1">
            Its entry condition has not been met and its wait policy is
            <span class="font-mono">hold</span>. It will wait until the condition
            holds, you force it through, or the sequence is aborted.
          </div>
          <button
            class="mt-2 px-3 py-1.5 border border-warn/50 text-warn text-[11px]
              rounded-[var(--radius-sm)] hover:bg-warn/10 transition-colors disabled:opacity-40"
            onclick={handleProceed}
            disabled={proceeding}
          >
            {proceeding ? 'Forcing...' : 'Force stage to proceed'}
          </button>
        </div>
      {/if}

      <button
        class="px-4 py-2 border border-danger/50 text-danger text-xs rounded-[var(--radius-sm)]
          hover:bg-danger/10 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
        onclick={handleAbort}
        disabled={aborting || !abortable}
        title={abortable
          ? 'Stop the sequence and hand over to the wake plan'
          : 'The point of no return has been crossed; this sequence can no longer be aborted'}
      >
        {aborting ? 'Aborting...' : 'Abort Sequence'}
      </button>

      {#if !abortable}
        <div class="text-ink-muted text-[10px] mt-1.5">
          Past the point of no return — the sequence will complete and hand over
          to the wake plan.
        </div>
      {/if}
    </section>
  {/if}

  {#if error}
    <div class="mb-6 border border-danger/40 bg-danger/5 rounded-[var(--radius-sm)] p-3
      text-danger text-xs">
      {error}
    </div>
  {/if}

  <!-- Admin password -->
  {#if !$passwordPinned}
    <section class="mb-8">
      <h2 class="text-[10px] text-ink-muted tracking-wider mb-3">ADMIN PASSWORD</h2>
      <div class="border border-edge rounded-[var(--radius-sm)] bg-surface-50 p-4 max-w-sm">
        <div class="text-[10px] text-ink-muted mb-3">
          Changing this signs out every session, including this one.
        </div>

        <form onsubmit={e => { e.preventDefault(); handlePasswordChange(); }}>
          <input
            type="password"
            bind:value={currentPassword}
            class="w-full px-3 py-2 bg-surface-100 border border-edge rounded-[var(--radius-sm)]
              text-ink text-xs focus:outline-none focus:border-amber placeholder:text-ink-faint"
            placeholder="Current password"
            autocomplete="current-password"
          />
          <input
            type="password"
            bind:value={newPassword}
            class="w-full mt-2 px-3 py-2 bg-surface-100 border border-edge rounded-[var(--radius-sm)]
              text-ink text-xs focus:outline-none focus:border-amber placeholder:text-ink-faint"
            placeholder={`New password (at least ${$minPasswordLength} characters)`}
            autocomplete="new-password"
          />
          <input
            type="password"
            bind:value={confirmPassword}
            class="w-full mt-2 px-3 py-2 bg-surface-100 border border-edge rounded-[var(--radius-sm)]
              text-ink text-xs focus:outline-none focus:border-amber placeholder:text-ink-faint"
            placeholder="Confirm new password"
            autocomplete="new-password"
          />

          {#if passwordTooShort}
            <div class="text-warn text-[10px] mt-1.5">
              Must be at least {$minPasswordLength} characters.
            </div>
          {:else if passwordMismatch}
            <div class="text-warn text-[10px] mt-1.5">Passwords do not match.</div>
          {/if}

          {#if passwordError}
            <div class="text-danger text-[10px] mt-1.5">{passwordError}</div>
          {/if}

          <button
            type="submit"
            disabled={!canChangePassword}
            class="w-full mt-3 px-3 py-2 bg-surface-100 border border-edge text-ink-secondary
              text-xs rounded-[var(--radius-sm)] hover:border-edge-strong hover:text-ink
              transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {changingPassword ? 'Changing...' : 'Change password'}
          </button>
        </form>
      </div>
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
        <span class="text-ink-secondary">
          {$passwordPinned ? 'Local admin (set in config file)' : 'Local admin'}
        </span>
      </div>
      <div class="flex justify-between">
        <span class="text-ink-muted">Storage</span>
        <span class="text-ink-secondary">SQLite WAL</span>
      </div>
    </div>
  </section>
</div>
