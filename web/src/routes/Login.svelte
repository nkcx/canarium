<script>
  import { login, setup, needsSetup, minPasswordLength } from '../lib/stores/api.js';

  let password = '';
  let confirmPassword = '';
  let error = '';
  let loading = false;

  $: tooShort = $needsSetup && password.length > 0 && password.length < $minPasswordLength;
  $: mismatch = $needsSetup && confirmPassword.length > 0 && password !== confirmPassword;
  $: canSubmit =
    !loading &&
    password.length > 0 &&
    (!$needsSetup || (password.length >= $minPasswordLength && password === confirmPassword));

  async function handleSubmit() {
    if (!canSubmit) return;

    loading = true;
    error = '';

    const result = $needsSetup ? await setup(password) : await login(password);
    if (!result.ok) {
      error = result.error;
      password = '';
      confirmPassword = '';
    }

    loading = false;
  }
</script>

<div class="flex items-center justify-center h-screen bg-surface-0">
  <div class="w-72">
    <div class="text-center mb-8">
      <div class="text-amber font-bold text-lg tracking-wider">CANARIUM</div>
      <div class="text-ink-muted text-[10px] mt-1">power orchestrator</div>
    </div>

    <form onsubmit={e => { e.preventDefault(); handleSubmit(); }}>
      <div class="text-ink-muted text-[10px] tracking-wider mb-1.5">
        {$needsSetup ? 'SET ADMIN PASSWORD' : 'PASSWORD'}
      </div>
      <input
        type="password"
        bind:value={password}
        class="w-full px-3 py-2 bg-surface-100 border border-edge rounded-[var(--radius-sm)]
          text-ink text-xs focus:outline-none focus:border-amber
          placeholder:text-ink-faint"
        placeholder={$needsSetup ? `At least ${$minPasswordLength} characters` : 'Enter password'}
        autocomplete={$needsSetup ? 'new-password' : 'current-password'}
      />

      {#if $needsSetup}
        <input
          type="password"
          bind:value={confirmPassword}
          class="w-full mt-2 px-3 py-2 bg-surface-100 border border-edge rounded-[var(--radius-sm)]
            text-ink text-xs focus:outline-none focus:border-amber
            placeholder:text-ink-faint"
          placeholder="Confirm password"
          autocomplete="new-password"
        />
        <div class="text-ink-faint text-[10px] mt-1.5">
          This password protects the API that can shut down your infrastructure.
          There is no recovery path — store it somewhere safe.
        </div>
      {/if}

      {#if tooShort}
        <div class="text-warn text-[10px] mt-1.5">
          Must be at least {$minPasswordLength} characters.
        </div>
      {:else if mismatch}
        <div class="text-warn text-[10px] mt-1.5">Passwords do not match.</div>
      {/if}

      {#if error}
        <div class="text-danger text-[10px] mt-1.5">{error}</div>
      {/if}

      <button
        type="submit"
        disabled={!canSubmit}
        class="w-full mt-3 px-3 py-2 bg-amber/10 border border-amber/30 text-amber
          text-xs rounded-[var(--radius-sm)] hover:bg-amber/20 transition-colors
          disabled:opacity-40 disabled:cursor-not-allowed"
      >
        {loading ? '...' : $needsSetup ? 'Set Password' : 'Sign In'}
      </button>
    </form>
  </div>
</div>
