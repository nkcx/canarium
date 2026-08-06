<script>
  import { login, setup, needsSetup } from '../lib/stores/api.js';

  let password = '';
  let error = '';
  let loading = false;

  async function handleSubmit() {
    loading = true;
    error = '';
    const fn = $needsSetup ? setup : login;
    const ok = await fn(password);
    if (!ok) {
      error = $needsSetup ? 'Setup failed' : 'Invalid password';
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
        placeholder="Enter password"
        autocomplete="current-password"
      />

      {#if error}
        <div class="text-danger text-[10px] mt-1.5">{error}</div>
      {/if}

      <button
        type="submit"
        disabled={loading || !password}
        class="w-full mt-3 px-3 py-2 bg-amber/10 border border-amber/30 text-amber
          text-xs rounded-[var(--radius-sm)] hover:bg-amber/20 transition-colors
          disabled:opacity-40 disabled:cursor-not-allowed"
      >
        {loading ? '...' : $needsSetup ? 'Create Account' : 'Sign In'}
      </button>
    </form>
  </div>
</div>
