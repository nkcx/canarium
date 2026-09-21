<script>
  import {
    status, clients, setMode, abortSequence, proceedStage, changePassword,
    minPasswordLength, passwordPinned, configReadonly,
  } from '../lib/stores/api.js';
  import { timeAgo } from '../lib/facts.js';
  import Section from '../lib/components/Section.svelte';
  import Card from '../lib/components/Card.svelte';
  import Chip from '../lib/components/Chip.svelte';
  import Button from '../lib/components/Button.svelte';
  import Field from '../lib/components/Field.svelte';
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte';

  let modeChanging = false;
  let aborting = false;
  let proceeding = false;
  let error = '';

  // Which destructive action is awaiting confirmation, if any.
  let pending = null;

  const modes = [
    {
      id: 'disarmed', label: 'Disarmed', tone: 'neutral',
      desc: 'Sources poll, conditions evaluate, nothing executes.',
    },
    {
      id: 'dry-run', label: 'Dry Run', tone: 'info',
      desc: 'Full sequences with timing, transports log instead of acting.',
    },
    {
      id: 'armed', label: 'Armed', tone: 'live',
      desc: 'Live execution. Plans will trigger and execute.',
    },
  ];

  $: sequence = $status?.sequence ?? null;
  $: abortable = sequence?.abortable ?? false;
  $: heldStage = sequence?.held_stage ?? '';

  // Naming the machines is the whole point of asking. A native confirm()
  // could only offer a sentence.
  $: runningClients = $clients.filter(
    c => c.state === 'up' || c.state === 'shutting_down',
  );

  function requestMode(mode) {
    if ($configReadonly) {
      error =
        'config_readonly is set; change canarium.mode in the configuration file and restart.';
      return;
    }
    error = '';

    // Only arming is consequential enough to interrupt for.
    if (mode === 'armed' && $status?.mode !== 'armed') {
      pending = 'arm';
      return;
    }
    applyMode(mode);
  }

  async function applyMode(mode) {
    modeChanging = true;
    error = '';
    const result = await setMode(mode);
    if (!result.ok) error = result.error;
    modeChanging = false;
    pending = null;
  }

  async function confirmAbort() {
    aborting = true;
    error = '';
    const result = await abortSequence();
    if (!result.ok) error = result.error;
    aborting = false;
    pending = null;
  }

  async function confirmProceed() {
    proceeding = true;
    error = '';
    const result = await proceedStage();
    if (!result.ok) error = result.error;
    proceeding = false;
    pending = null;
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
</script>

<h1 class="sr-only">Settings</h1>

{#if error}
  <div class="mb-6">
    <Card tone="danger">
      <p class="text-body text-danger">{error}</p>
    </Card>
  </div>
{/if}

<!-- ── Mode ───────────────────────────────────────────────────────── -->
<Section title="MODE" id="mode-heading">
  <span slot="aside" class="text-meta text-ink-muted">
    {#if $configReadonly}
      read-only — set in the configuration file
    {/if}
  </span>

  <!-- Constrained, like every other block. These cards used to stretch the
       full 1640px of a wide monitor for a two-line label. -->
  <div class="space-y-2 max-w-2xl">
    {#each modes as mode (mode.id)}
      {@const active = $status?.mode === mode.id}
      <button
        class="w-full text-left px-4 py-3 border rounded-[var(--radius-md)] transition-colors
          {active
            ? 'border-amber/50 bg-amber/5'
            : 'border-edge bg-surface-50 hover:border-edge-strong'}
          disabled:opacity-40 disabled:pointer-events-none"
        onclick={() => requestMode(mode.id)}
        disabled={modeChanging || $configReadonly}
        aria-pressed={active}
        title={$configReadonly
          ? 'config_readonly is set; the mode comes from the configuration file'
          : mode.desc}
      >
        <div class="flex items-center gap-2 flex-wrap">
          <span class="text-body font-bold {active ? 'text-ink' : 'text-ink-secondary'}">
            {mode.label}
          </span>
          {#if active}
            <Chip tone={mode.tone}>ACTIVE</Chip>
          {/if}
        </div>
        <div class="text-meta text-ink-muted mt-1">{mode.desc}</div>
      </button>
    {/each}
  </div>
</Section>

<!-- ── Sequence control ───────────────────────────────────────────── -->
{#if sequence}
  <Section title="SEQUENCE CONTROL" id="sequence-heading">
    <div class="max-w-2xl space-y-3">
      <Card tone={sequence.ponr_crossed ? 'danger' : 'live'}>
        <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-body">
          <dt class="text-ink-muted">Plan</dt>
          <dd class="text-ink text-right">{sequence.plan}</dd>

          <dt class="text-ink-muted">State</dt>
          <dd class="text-ink text-right">{sequence.state?.replace(/_/g, ' ')}</dd>

          <dt class="text-ink-muted">Stage</dt>
          <dd class="text-ink text-right">
            {sequence.stage_name || '—'}
            <span class="text-ink-muted">
              ({(sequence.current_stage ?? 0) + 1} of {sequence.total_stages})
            </span>
          </dd>

          <dt class="text-ink-muted">Started</dt>
          <dd class="text-ink-secondary text-right">{timeAgo(sequence.started_at)}</dd>

          <dt class="text-ink-muted">Point of no return</dt>
          <dd class="text-right {sequence.ponr_crossed ? 'text-danger' : 'text-ok'}">
            {sequence.ponr_crossed ? 'crossed' : 'not crossed'}
          </dd>
        </dl>
      </Card>

      {#if heldStage}
        <Card tone="warn">
          <div class="text-body font-bold text-warn">
            Stage "{heldStage}" is holding
          </div>
          <p class="text-meta text-ink-secondary mt-1.5">
            Its entry condition has not been met and its wait policy is
            <span class="text-ink">hold</span>. It will wait until the condition
            holds, you force it through, or the sequence is aborted.
          </p>
          <div class="mt-3">
            <Button variant="danger" on:click={() => (pending = 'proceed')} disabled={proceeding}>
              Force stage to proceed
            </Button>
          </div>
        </Card>
      {/if}

      <div>
        <Button
          variant={abortable ? 'danger' : 'default'}
          size="lg"
          on:click={() => (pending = 'abort')}
          disabled={aborting || !abortable}
          title={abortable
            ? 'Stop the sequence and hand over to the wake plan'
            : 'The point of no return has been crossed; this sequence can no longer be aborted'}
        >
          Abort sequence
        </Button>
        <p class="text-meta text-ink-muted mt-2">
          {#if abortable}
            In-flight shutdowns are allowed to finish, then the wake plan takes over.
          {:else}
            Past the point of no return — the sequence will complete and hand over
            to the wake plan.
          {/if}
        </p>
      </div>
    </div>
  </Section>
{/if}

<!-- ── Admin password ─────────────────────────────────────────────── -->
{#if !$passwordPinned}
  <Section title="ADMIN PASSWORD" id="password-heading">
    <div class="max-w-md">
      <Card>
        <p class="text-meta text-ink-muted mb-4">
          Changing this signs out every session, including this one.
        </p>

        <form onsubmit={e => { e.preventDefault(); handlePasswordChange(); }}>
          <Field
            id="current-password"
            label="CURRENT PASSWORD"
            type="password"
            bind:value={currentPassword}
            autocomplete="current-password"
          />
          <Field
            id="new-password"
            label="NEW PASSWORD"
            type="password"
            bind:value={newPassword}
            autocomplete="new-password"
            invalid={passwordTooShort}
            hint={`At least ${$minPasswordLength} characters.`}
          />
          <Field
            id="confirm-password"
            label="CONFIRM NEW PASSWORD"
            type="password"
            bind:value={confirmPassword}
            autocomplete="new-password"
            invalid={passwordMismatch}
          />

          <div aria-live="polite">
            {#if passwordTooShort}
              <p class="text-meta text-warn mb-3">
                Must be at least {$minPasswordLength} characters.
              </p>
            {:else if passwordMismatch}
              <p class="text-meta text-warn mb-3">Passwords do not match.</p>
            {/if}

            {#if passwordError}
              <p class="text-meta text-danger mb-3">{passwordError}</p>
            {/if}
          </div>

          <Button type="submit" variant="primary" size="lg" full disabled={!canChangePassword}>
            {changingPassword ? 'Changing…' : 'Change password'}
          </Button>
        </form>
      </Card>
    </div>
  </Section>
{/if}

<!-- ── System ─────────────────────────────────────────────────────── -->
<Section title="SYSTEM" id="system-heading">
  <div class="max-w-2xl">
    <Card>
      <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-body">
        <dt class="text-ink-muted">Config</dt>
        <dd class="text-ink-secondary text-right">File-canonical (YAML)</dd>

        <dt class="text-ink-muted">Auth</dt>
        <dd class="text-ink-secondary text-right">
          {$passwordPinned ? 'Local admin (set in config file)' : 'Local admin'}
        </dd>

        <dt class="text-ink-muted">Storage</dt>
        <dd class="text-ink-secondary text-right">SQLite WAL</dd>
      </dl>
    </Card>
  </div>
</Section>

<!-- ── Confirmations ──────────────────────────────────────────────── -->
<ConfirmDialog
  open={pending === 'arm'}
  title="Arm Canarium?"
  confirmLabel="Arm"
  confirmVariant="primary"
  busy={modeChanging}
  onconfirm={() => applyMode('armed')}
  oncancel={() => (pending = null)}
>
  <p>
    Plans will trigger on their conditions and shut down real machines,
    without asking again.
  </p>
  {#if runningClients.length > 0}
    <p class="text-ink-muted">
      {runningClients.length}
      {runningClients.length === 1 ? 'client is' : 'clients are'} currently
      running and in scope:
      <span class="text-ink">{runningClients.map(c => c.name).join(', ')}</span>.
    </p>
  {/if}
</ConfirmDialog>

<ConfirmDialog
  open={pending === 'abort'}
  title="Abort {sequence?.plan ?? 'the active sequence'}?"
  confirmLabel="Abort sequence"
  busy={aborting}
  onconfirm={confirmAbort}
  oncancel={() => (pending = null)}
>
  <p>
    Machines already told to shut down will finish doing so — that cannot be
    called back. Once they have settled, the wake plan takes over and brings
    everything back up.
  </p>
</ConfirmDialog>

<ConfirmDialog
  open={pending === 'proceed'}
  title="Force stage &quot;{heldStage}&quot; to proceed?"
  confirmLabel="Shut them down now"
  busy={proceeding}
  onconfirm={confirmProceed}
  oncancel={() => (pending = null)}
>
  <p>
    This stage is holding because its entry condition has not been met.
    Forcing it shuts its clients down now, regardless.
  </p>
</ConfirmDialog>
