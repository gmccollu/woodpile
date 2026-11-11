<script lang="ts">
  import { router, href, go, type Route } from './lib/router.svelte';
  import { theme, setTheme } from './lib/theme.svelte';
  import { live, startPolling } from './lib/status.svelte';
  import { ui } from './lib/ui.svelte';
  import Icon, { type IconName } from './components/Icon.svelte';
  import Logo from './components/Logo.svelte';
  import Banners from './components/Banners.svelte';
  import Welcome from './pages/Welcome.svelte';
  import Dashboard from './pages/Dashboard.svelte';
  import Devices from './pages/Devices.svelte';
  import Search from './pages/Search.svelte';
  import Export from './pages/Export.svelte';
  import Settings from './pages/Settings.svelte';
  import About from './pages/About.svelte';

  startPolling();

  const NAV: { r: Route; label: string; icon: IconName; soon?: boolean }[] = [
    { r: 'dashboard', label: 'Dashboard', icon: 'home' },
    { r: 'devices', label: 'Devices', icon: 'devices' },
    { r: 'search', label: 'Search', icon: 'search', soon: true },
    { r: 'export', label: 'Export', icon: 'export', soon: true },
    { r: 'settings', label: 'Settings', icon: 'settings' },
    { r: 'about', label: 'About', icon: 'about' },
    { r: 'welcome', label: 'Setup', icon: 'welcome' },
  ];

  const TITLES: Record<Route, string> = {
    welcome: 'Welcome',
    dashboard: 'Dashboard',
    devices: 'Devices',
    search: 'Search',
    export: 'Export',
    settings: 'Settings',
    about: 'About',
  };

  // First run: with no page in the URL and no message received yet, open on the welcome page.
  let decided = false;
  $effect(() => {
    if (decided || !live.status) return;
    decided = true;
    if (!router.explicit && live.status.first_message_at === null) location.replace('#/welcome');
  });

  $effect(() => {
    document.title = `${TITLES[router.route]} · Woodpile`;
  });

  const filterable = $derived(router.route === 'dashboard' || router.route === 'devices');

  function onFilter(e: Event) {
    ui.filter = (e.currentTarget as HTMLInputElement).value;
    if (!filterable) go('dashboard');
  }
</script>

<a href="#main" class="skip">Skip to content</a>

<div class="mx-auto max-w-[1320px] p-3 sm:p-6 lg:p-10">
  <div class="shell glass">
    <header class="mb-5 flex flex-wrap items-center gap-3 sm:gap-5">
      <a class="brand glass" href={href('dashboard')} aria-label="Woodpile dashboard">
        <span class="grid size-11 place-items-center rounded-full bg-chip shadow-[var(--shadow-chip)]"><Logo /></span>
        <span class="text-[22px] leading-tight font-bold tracking-tight">
          Woodpile<small class="block text-[11px] font-medium tracking-normal text-muted">syslog server</small>
        </span>
      </a>

      <label class="search glass order-3 w-full sm:order-none sm:w-auto sm:flex-1">
        <span class="text-muted"><Icon name="search" /></span>
        <span class="sr-only">Filter devices and live messages</span>
        <input
          type="search"
          value={ui.filter}
          oninput={onFilter}
          placeholder={router.route === 'devices' ? 'Filter devices' : 'Filter devices and messages'}
          class="w-full border-0 bg-transparent text-[14px] text-fg outline-none placeholder:text-muted"
        />
      </label>

      <div class="mode glass ml-auto sm:ml-0" role="group" aria-label="Colour mode">
        <button aria-pressed={theme.mode === 'light'} onclick={() => setTheme('light')}><Icon name="sun" size={15} />Light</button>
        <button aria-pressed={theme.mode === 'dark'} onclick={() => setTheme('dark')}><Icon name="moon" size={15} />Dark</button>
      </div>
    </header>

    <div class="grid grid-cols-1 gap-5 md:grid-cols-[84px_minmax(0,1fr)]">
      <nav class="nav" aria-label="Pages">
        {#each NAV as n (n.r)}
          <a href={href(n.r)} class="navi" aria-current={router.route === n.r ? 'page' : undefined}>
            <span class="ico"><Icon name={n.icon} size={19} /></span>
            <span class="lab">{n.label}</span>
            {#if n.soon}<span class="soon" title="Coming in M3">M3</span>{/if}
          </a>
        {/each}
      </nav>

      <main id="main" class="panel" tabindex="-1">
        <Banners />
        {#if router.route === 'welcome'}
          <Welcome />
        {:else if router.route === 'devices'}
          <Devices />
        {:else if router.route === 'search'}
          <Search />
        {:else if router.route === 'export'}
          <Export />
        {:else if router.route === 'settings'}
          <Settings />
        {:else if router.route === 'about'}
          <About />
        {:else}
          <Dashboard />
        {/if}
      </main>
    </div>
  </div>
</div>

<style>
  .skip {
    position: absolute;
    left: -9999px;
  }
  .skip:focus {
    left: 12px;
    top: 12px;
    z-index: 100;
    background: var(--chip);
    color: var(--fg);
    padding: 8px 12px;
    border-radius: 10px;
  }
  .shell {
    background: var(--shell);
    border-radius: 36px;
    padding: 24px;
    box-shadow: var(--shadow);
  }
  @media (max-width: 640px) {
    .shell {
      padding: 14px;
      border-radius: 24px;
    }
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 18px 8px 8px;
    border-radius: 999px;
    color: var(--fg);
    text-decoration: none;
    backdrop-filter: none;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 13px 20px;
    border-radius: 999px;
    backdrop-filter: none;
  }
  .search:focus-within {
    outline: 2px solid var(--link);
    outline-offset: 2px;
  }
  .search input:focus-visible {
    outline: none;
  }
  .mode {
    display: flex;
    border-radius: 999px;
    padding: 4px;
    font-size: 13px;
    backdrop-filter: none;
  }
  .mode button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 8px 14px;
    border-radius: 999px;
    border: 0;
    background: transparent;
    color: var(--fg);
    cursor: pointer;
    font: inherit;
  }
  .mode button[aria-pressed='true'] {
    background: var(--chip);
    font-weight: 600;
    box-shadow: var(--shadow-chip);
  }
  .nav {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 12px 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    height: fit-content;
    position: sticky;
    top: 16px;
  }
  .navi {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 3px;
    width: 70px;
    padding: 5px 0;
    color: var(--fg);
    text-decoration: none;
    border-radius: 18px;
  }
  .ico {
    display: grid;
    place-items: center;
    width: 42px;
    height: 42px;
    border-radius: 50%;
  }
  .navi:hover .ico {
    background: var(--glass);
  }
  .navi[aria-current='page'] .ico {
    background: var(--chip);
    box-shadow: var(--shadow-chip);
  }
  .lab {
    font-size: 11.5px;
    font-weight: 500;
    color: var(--muted);
  }
  .navi[aria-current='page'] .lab {
    color: var(--fg);
    font-weight: 650;
  }
  .soon {
    position: absolute;
    top: 0;
    right: 4px;
    font-size: 10px;
    font-weight: 700;
    line-height: 1;
    padding: 3px 5px;
    border-radius: 999px;
    background: var(--chip);
    color: var(--accent-fg);
    box-shadow: var(--shadow-chip);
  }
  .panel {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 28px;
    padding: 20px;
    min-width: 0;
    outline: none;
  }
  @media (max-width: 767px) {
    .nav {
      flex-direction: row;
      flex-wrap: wrap;
      justify-content: center;
      border-radius: 28px;
      padding: 6px;
      position: static;
    }
    .panel {
      padding: 12px;
      border-radius: 22px;
    }
  }
</style>
