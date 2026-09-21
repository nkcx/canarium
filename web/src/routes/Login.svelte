<script>
  import { login, setup, needsSetup, minPasswordLength } from '../lib/stores/api.js';
  import Button from '../lib/components/Button.svelte';
  import Field from '../lib/components/Field.svelte';

  let password = '';
  let confirmPassword = '';
  let error = '';
  let busy = false;

  $: tooShort = $needsSetup && password.length > 0 && password.length < $minPasswordLength;
  $: mismatch = $needsSetup && confirmPassword.length > 0 && password !== confirmPassword;
  $: canSubmit =
    !busy &&
    password.length > 0 &&
    (!$needsSetup || (password.length >= $minPasswordLength && password === confirmPassword));

  async function handleSubmit() {
    if (!canSubmit) return;

    busy = true;
    error = '';

    const result = $needsSetup ? await setup(password) : await login(password);
    if (!result.ok) {
      error = result.error;
      password = '';
      confirmPassword = '';
    }

    busy = false;
  }
</script>

<div class="min-h-screen flex items-center justify-center bg-surface-0 px-4 py-10">
  <div class="w-full max-w-xs">
    <div class="text-center mb-8">
      <div class="inline-flex items-center gap-2">
        <span class="w-2 h-2 rounded-full bg-amber" aria-hidden="true"></span>
        <span class="text-ink font-bold text-value tracking-[0.12em]">CANARIUM</span>
      </div>
      <div class="text-ink-muted text-meta mt-1.5">power orchestrator</div>
    </div>

    <form onsubmit={e => { e.preventDefault(); handleSubmit(); }}>
      <Field
        id="password"
        label={$needsSetup ? 'SET ADMIN PASSWORD' : 'PASSWORD'}
        type="password"
        bind:value={password}
        placeholder={$needsSetup ? `At least ${$minPasswordLength} characters` : ''}
        autocomplete={$needsSetup ? 'new-password' : 'current-password'}
        invalid={tooShort}
      />

      {#if $needsSetup}
        <Field
          id="confirm"
          label="CONFIRM PASSWORD"
          type="password"
          bind:value={confirmPassword}
          autocomplete="new-password"
          invalid={mismatch}
          hint="This password protects the API that can shut down your infrastructure. There is no recovery path — store it somewhere safe."
        />
      {/if}

      <div aria-live="polite">
        {#if tooShort}
          <p class="text-meta text-warn mb-3">
            Must be at least {$minPasswordLength} characters.
          </p>
        {:else if mismatch}
          <p class="text-meta text-warn mb-3">Passwords do not match.</p>
        {/if}

        {#if error}
          <p class="text-meta text-danger mb-3">{error}</p>
        {/if}
      </div>

      <Button type="submit" variant="primary" size="lg" full disabled={!canSubmit}>
        {busy ? 'Signing in…' : $needsSetup ? 'Set password' : 'Sign in'}
      </Button>
    </form>
  </div>
</div>
