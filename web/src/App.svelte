<script>
  import { onMount } from 'svelte';
  import { checkAuth, refreshAll, connectWS, authenticated, status, connected } from './lib/stores/api.js';
  import Dashboard from './routes/Dashboard.svelte';
  import Clients from './routes/Clients.svelte';
  import Plans from './routes/Plans.svelte';
  import Settings from './routes/Settings.svelte';
  import Login from './routes/Login.svelte';

  let currentView = 'dashboard';
  let pollTimer;

  const views = [
    { id: 'dashboard', label: 'Dashboard' },
    { id: 'clients', label: 'Clients' },
    { id: 'plans', label: 'Plans' },
    { id: 'settings', label: 'Settings' },
  ];

  onMount(async () => {
    await checkAuth();
    if ($authenticated) {
      await refreshAll();
      connectWS();
      pollTimer = setInterval(refreshAll, 15000);
    }
    return () => {
      if (pollTimer) clearInterval(pollTimer);
    };
  });

  $: if ($authenticated && !pollTimer) {
    refreshAll();
    connectWS();
    pollTimer = setInterval(refreshAll, 15000);
  }
</script>

{#if !$authenticated}
  <Login />
{:else}
  <div class="flex h-screen overflow-hidden">
    <!-- Sidebar -->
    <nav class="w-52 flex-shrink-0 border-r border-edge flex flex-col bg-surface-0">
      <div class="p-4 border-b border-edge">
        <div class="text-amber font-bold text-sm tracking-wider">CANARIUM</div>
        <div class="text-ink-muted text-[10px] mt-0.5">power orchestrator</div>
      </div>

      <div class="flex-1 py-2">
        {#each views as view}
          <button
            class="w-full text-left px-4 py-2 text-xs transition-colors
              {currentView === view.id
                ? 'text-ink bg-surface-100 border-r-2 border-amber'
                : 'text-ink-secondary hover:text-ink hover:bg-surface-50'}"
            onclick={() => currentView = view.id}
          >
            {view.label}
          </button>
        {/each}
      </div>

      <div class="p-3 border-t border-edge">
        <div class="flex items-center gap-2 text-[10px]">
          <span class="w-1.5 h-1.5 rounded-full {$connected ? 'bg-ok' : 'bg-danger'}"></span>
          <span class="text-ink-muted">{$connected ? 'Connected' : 'Disconnected'}</span>
        </div>
        {#if $status}
          <div class="mt-1 flex items-center gap-2 text-[10px]">
            <span class="px-1.5 py-0.5 rounded text-[9px] font-bold tracking-wider
              {$status.mode === 'armed' ? 'bg-danger/20 text-danger' :
               $status.mode === 'dry-run' ? 'bg-warn/20 text-warn' :
               'bg-surface-200 text-ink-muted'}">
              {$status.mode?.toUpperCase()}
            </span>
          </div>
        {/if}
      </div>
    </nav>

    <!-- Main content -->
    <main class="flex-1 overflow-y-auto bg-surface-0">
      {#if currentView === 'dashboard'}
        <Dashboard />
      {:else if currentView === 'clients'}
        <Clients />
      {:else if currentView === 'plans'}
        <Plans />
      {:else if currentView === 'settings'}
        <Settings />
      {/if}
    </main>
  </div>
{/if}
