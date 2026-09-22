<script>
  import { onMount } from 'svelte';
  import {
    checkAuth,
    refreshAll,
    connectWS,
    disconnectWS,
    logout,
    authenticated,
    status,
    connected,
  } from './lib/stores/api.js';
  import Dashboard from './routes/Dashboard.svelte';
  import Clients from './routes/Clients.svelte';
  import Plans from './routes/Plans.svelte';
  import Settings from './routes/Settings.svelte';
  import Login from './routes/Login.svelte';
  import Chip from './lib/components/Chip.svelte';
  import StatusDot from './lib/components/StatusDot.svelte';

  let currentView = 'dashboard';
  let navOpen = false;
  let pollTimer = null;

  async function handleLogout() {
    stopSession();
    await logout();
  }

  const views = [
    { id: 'dashboard', label: 'Dashboard' },
    { id: 'clients', label: 'Clients' },
    { id: 'plans', label: 'Plans' },
    { id: 'settings', label: 'Settings' },
  ];

  const POLL_INTERVAL_MS = 15000;

  function startSession() {
    if (pollTimer) return;
    refreshAll();
    connectWS();
    pollTimer = setInterval(refreshAll, POLL_INTERVAL_MS);
  }

  function stopSession() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
    disconnectWS();
  }

  // onMount's cleanup must be returned synchronously. The previous version
  // was an async function, so Svelte received a Promise rather than a
  // cleanup function and the interval was never cleared.
  onMount(() => {
    checkAuth();
    return stopSession;
  });

  // Start polling when authentication is established, and stop when it is
  // lost — including when a request 401s because the session expired.
  $: if ($authenticated) {
    startSession();
  } else {
    stopSession();
  }

  function go(view) {
    currentView = view;
    navOpen = false;
  }

  /*
   * Armed is the correct, intended operating state, so it is not painted
   * red. It used to be, which taught the operator to discount the colour
   * that also marks the point of no return -- the one thing on this screen
   * that can never be taken back.
   */
  const modeTone = { armed: 'live', 'dry-run': 'info' };
  $: mode = $status?.mode;
  $: modeLabel = mode ? mode.toUpperCase() : '';
</script>

{#if !$authenticated}
  <Login />
{:else}
  <div class="md:flex md:h-screen md:overflow-hidden">
    <!--
      Mobile bar. The shell was previously a fixed 208px sidebar next to
      the content at every width, so on a 390px phone -- the stated primary
      context -- the nav took more than half the viewport and the client
      table was cropped to its first column.
    -->
    <header
      class="md:hidden sticky top-0 z-30 flex items-center gap-3 px-4 py-2.5
        border-b border-edge bg-surface-0/95 backdrop-blur"
    >
      <button
        class="inline-flex items-center justify-center w-10 h-10 -ml-2 rounded-[var(--radius-sm)]
          text-ink-secondary hover:text-ink hover:bg-surface-50 transition-colors"
        aria-expanded={navOpen}
        aria-controls="main-nav"
        aria-label={navOpen ? 'Close menu' : 'Open menu'}
        onclick={() => (navOpen = !navOpen)}
      >
        {#if navOpen}
          <svg width="18" height="18" viewBox="0 0 18 18" fill="none" aria-hidden="true">
            <path d="M4 4l10 10M14 4L4 14" stroke="currentColor" stroke-width="1.5" />
          </svg>
        {:else}
          <svg width="18" height="18" viewBox="0 0 18 18" fill="none" aria-hidden="true">
            <path d="M2 5h14M2 9h14M2 13h14" stroke="currentColor" stroke-width="1.5" />
          </svg>
        {/if}
      </button>

      <span class="flex items-center gap-2">
        <img src="/mark.webp" alt="" width="24" height="24" class="w-6 h-6 rounded-full" />
        <span class="font-bold text-body tracking-[0.12em] text-ink">CANARIUM</span>
      </span>

      <div class="ml-auto flex items-center gap-2">
        {#if mode}
          <Chip tone={modeTone[mode] ?? 'neutral'}>{modeLabel}</Chip>
        {/if}
        <StatusDot
          tone={$connected ? 'ok' : 'danger'}
          label={$connected ? 'Connected' : 'Disconnected'}
        />
      </div>
    </header>

    <!-- Scrim, mobile only. A real button rather than a div with a click
         handler; out of the tab order because the menu toggle already
         closes the drawer from the keyboard. -->
    {#if navOpen}
      <button
        type="button"
        tabindex="-1"
        aria-hidden="true"
        class="md:hidden fixed inset-0 z-30 bg-surface-0/70 cursor-default"
        onclick={() => (navOpen = false)}
      ></button>
    {/if}

    <nav
      id="main-nav"
      aria-label="Main"
      class="bg-surface-0 border-edge flex flex-col
        max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-40 max-md:w-64
        max-md:border-r max-md:transition-transform max-md:duration-200 max-md:ease-out
        {navOpen ? 'max-md:translate-x-0' : 'max-md:-translate-x-full'}
        md:static md:w-52 md:shrink-0 md:translate-x-0 md:border-r"
    >
      <div class="p-4 border-b border-edge">
        <!-- The mascot carries the identity, so the wordmark can stay ink
             rather than spending the colour reserved for "look here". -->
        <div class="flex items-center gap-2.5">
          <img src="/mark.webp" alt="" width="36" height="36"
            class="w-9 h-9 rounded-full shrink-0" />
          <span class="text-ink font-bold text-body tracking-[0.12em]">CANARIUM</span>
        </div>
        <div class="text-ink-muted text-meta mt-1.5">power orchestrator</div>
      </div>

      <div class="flex-1 py-2 overflow-y-auto">
        {#each views as view (view.id)}
          <button
            class="w-full text-left px-4 min-h-11 py-2.5 text-body transition-colors
              border-l-2
              {currentView === view.id
                ? 'text-ink bg-surface-100 border-l-canary'
                : 'text-ink-secondary border-l-transparent hover:text-ink hover:bg-surface-50'}"
            aria-current={currentView === view.id ? 'page' : undefined}
            onclick={() => go(view.id)}
          >
            {view.label}
          </button>
        {/each}
      </div>

      <div class="p-3 border-t border-edge space-y-2">
        <div class="flex items-center gap-2 text-meta">
          <StatusDot tone={$connected ? 'ok' : 'danger'} />
          <span class={$connected ? 'text-ink-muted' : 'text-danger'}>
            {$connected ? 'Connected' : 'Disconnected'}
          </span>
        </div>
        {#if mode}
          <div>
            <Chip tone={modeTone[mode] ?? 'neutral'}>{modeLabel}</Chip>
          </div>
        {/if}
        <button
          class="w-full text-left min-h-10 px-2 -mx-2 rounded-[var(--radius-sm)]
            text-meta text-ink-muted hover:text-ink hover:bg-surface-50 transition-colors"
          onclick={handleLogout}
        >
          Sign out
        </button>
      </div>
    </nav>

    <main class="flex-1 md:overflow-y-auto bg-surface-0">
      <div class="max-w-[var(--width-content)] mx-auto px-4 py-5 sm:px-6 sm:py-6">
        {#if currentView === 'dashboard'}
          <Dashboard />
        {:else if currentView === 'clients'}
          <Clients />
        {:else if currentView === 'plans'}
          <Plans />
        {:else if currentView === 'settings'}
          <Settings />
        {/if}
      </div>
    </main>
  </div>
{/if}
