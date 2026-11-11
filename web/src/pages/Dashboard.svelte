<script lang="ts">
  import { getStats, type Device, type Stats } from '../lib/api';
  import { ago, bytes, num, rate } from '../lib/format';
  import { live } from '../lib/status.svelte';
  import { ui } from '../lib/ui.svelte';
  import LiveTail from '../components/LiveTail.svelte';
  import Sparkline from '../components/Sparkline.svelte';
  import StatusDot from '../components/StatusDot.svelte';
  import Icon from '../components/Icon.svelte';

  const ALL = '\u0000all';
  const UNGROUPED = '\u0000none';

  let stats = $state<Stats | null>(null);
  let statsError = $state('');
  let tab = $state(ALL);
  let picked = $state<string | null>(null);

  async function load() {
    try {
      stats = await getStats();
      statsError = '';
    } catch (e) {
      statsError = (e as Error).message;
    }
  }

  $effect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  const visible = $derived((live.devices ?? []).filter((d) => !d.hidden));
  const groups = $derived([...new Set(visible.map((d) => d.group).filter(Boolean))].sort());
  const hasUngrouped = $derived(visible.some((d) => !d.group));
  const filterText = $derived(ui.filter.trim().toLowerCase());

  const inTab = (d: Device) => tab === ALL || (tab === UNGROUPED ? !d.group : d.group === tab);
  // Stable order (by name), so tiles don't jump around as rates change.
  const label = (d: Device) => d.name || d.ip;
  const tiles = $derived(
    visible
      .filter(inTab)
      .filter((d) => !filterText || `${d.name} ${d.ip}`.toLowerCase().includes(filterText))
      .sort((a, b) => label(a).localeCompare(label(b), undefined, { numeric: true })),
  );

  // Live tail scope: one picked device, else the tab. "Ungrouped" has no API filter, so send its device ids.
  const tailDevices = $derived(
    picked ? [picked] : tab === UNGROUPED ? visible.filter((d) => !d.group).map((d) => d.id) : [],
  );
  const tailGroup = $derived(picked || tab === ALL || tab === UNGROUPED ? '' : tab);
  const pickedDevice = $derived(visible.find((d) => d.id === picked));
  const scope = $derived(
    pickedDevice ? pickedDevice.name || pickedDevice.ip : tab === ALL ? 'all devices' : tab === UNGROUPED ? 'ungrouped' : tab,
  );

  function setTab(t: string) {
    tab = t;
    picked = null;
  }

  const diskPct = $derived(stats ? Math.min(100, (stats.disk.used_bytes / Math.max(1, stats.disk.cap_bytes)) * 100) : 0);
  const osName = $derived(
    live.status?.os.startsWith('darwin') ? 'macOS' : live.status?.os.startsWith('windows') ? 'Windows' : 'this OS',
  );
</script>

{#if statsError && !live.statusError}
  <p class="mb-3 text-sm text-bad-fg" role="alert">Couldn't load stats: {statsError}</p>
{/if}

<div class="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
  <div class="card">
    <h2 class="card-title">Messages / sec</h2>
    <div class="big">{stats ? num(stats.msgs_per_sec) : '—'}</div>
    <Sparkline values={stats?.sparkline ?? []} />
    <p class="sub">Last hour, per minute</p>
  </div>
  <div class="card">
    <h2 class="card-title">Devices</h2>
    <div class="big">{stats ? num(stats.devices.total) : '—'}</div>
    {#if stats}
      <p class="sub flex flex-wrap gap-x-3">
        <span class="inline-flex items-center gap-1.5"><StatusDot status="online" />{stats.devices.online} online</span>
        <span class="inline-flex items-center gap-1.5"><StatusDot status="quiet" />{stats.devices.quiet} quiet</span>
        <span class="inline-flex items-center gap-1.5"><StatusDot status="offline" />{stats.devices.offline} offline</span>
      </p>
    {/if}
  </div>
  <div class="card">
    <h2 class="card-title">Disk used</h2>
    <div class="big">
      {stats ? bytes(stats.disk.used_bytes).split(' ')[0] : '—'}
      {#if stats}<small>{bytes(stats.disk.used_bytes).split(' ')[1]}</small>{/if}
    </div>
    <div
      class="mt-2 h-2 overflow-hidden rounded-full bg-black/10 dark:bg-white/10"
      role="progressbar"
      aria-label="Disk used against the cap"
      aria-valuenow={Math.round(diskPct)}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      <i class="block h-full rounded-full bg-accent" style:width="{Math.max(diskPct, stats ? 1.5 : 0)}%"></i>
    </div>
    {#if stats}
      <p class="sub" title="{bytes(stats.disk.free_bytes)} free on the data volume">of {bytes(stats.disk.cap_bytes)} · {stats.disk.days_kept} {stats.disk.days_kept === 1 ? 'day' : 'days'} kept</p>
    {/if}
  </div>
  <div class="card">
    <h2 class="card-title">Dropped</h2>
    <div class="big" class:text-bad-fg={!!stats?.dropped_24h}>
      {stats ? num(stats.dropped_24h) : '—'} <small>last 24 h</small>
    </div>
    {#if stats}
      <p class="sub">
        {#if stats.kernel_dropped_24h === null}
          Kernel drops aren't visible on {osName}. <a class="text-link underline" href="#/about">Why</a>
        {:else}
          Kernel: {num(stats.kernel_dropped_24h)} dropped
        {/if}
        {#if stats.truncated_24h}· {num(stats.truncated_24h)} truncated{/if}
      </p>
    {/if}
  </div>
</div>

<div class="mb-4 flex flex-wrap justify-center gap-2" role="tablist" aria-label="Device groups">
  <button role="tab" class="tab" aria-selected={tab === ALL} onclick={() => setTab(ALL)}>All devices</button>
  {#each groups as g (g)}
    <button role="tab" class="tab" aria-selected={tab === g} onclick={() => setTab(g)}>{g}</button>
  {/each}
  {#if hasUngrouped && groups.length}
    <button role="tab" class="tab" aria-selected={tab === UNGROUPED} onclick={() => setTab(UNGROUPED)}>Ungrouped</button>
  {/if}
  <a class="tab" href="#/devices" title="Set groups on the Devices page" aria-label="Add a group on the Devices page"><Icon name="plus" size={16} /></a>
</div>

<div class="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]">
  <div class="grid content-start gap-3 [grid-template-columns:repeat(auto-fill,minmax(136px,1fr))]">
    {#if live.devices && !tiles.length}
      <p class="col-span-full m-0 rounded-[18px] p-4 text-sm text-muted glass">
        {visible.length ? 'No devices match.' : 'No devices yet. Point a device at this machine on port 514.'}
        {#if !visible.length}<a class="text-link underline" href="#/welcome">Setup help</a>{/if}
      </p>
    {/if}
    {#each tiles as d (d.id)}
      <button
        class="dev"
        aria-pressed={picked === d.id}
        onclick={() => (picked = picked === d.id ? null : d.id)}
        title="Show only {d.name || d.ip} in the live tail"
      >
        <b class="block truncate text-[15px]">{d.name || d.ip}</b>
        <span class="block truncate text-xs text-muted">{d.name ? d.ip : d.group || 'unnamed'}</span>
        <span class="mt-2.5 flex items-center justify-between gap-2 text-xs">
          <span class="flex min-w-0 items-center gap-1.5">
            <StatusDot status={d.status} />
            <span class="truncate">{ago(d.last_heard, live.now)}</span>
          </span>
          <span class="text-muted">{rate(d.msgs_per_min)}</span>
        </span>
      </button>
    {/each}
  </div>

  <LiveTail
    group={tailGroup}
    devices={tailDevices}
    q={ui.filter}
    {scope}
    onClearScope={picked || tab !== ALL ? () => setTab(ALL) : undefined}
  />
</div>

<style>
  .big {
    font-size: 34px;
    font-weight: 700;
    letter-spacing: -1px;
    line-height: 1.2;
  }
  .sub {
    margin: 8px 0 0;
    font-size: 12.5px;
    line-height: 1.4;
    color: var(--muted);
  }
  .big small {
    font-size: 14px;
    color: var(--muted);
    font-weight: 500;
    letter-spacing: 0;
  }
  .dev {
    text-align: left;
    background: var(--glass);
    border: 1px solid var(--border);
    border-radius: 18px;
    padding: 12px 13px;
    color: var(--fg);
    cursor: pointer;
    min-width: 0;
    font: inherit;
  }
  .dev:hover {
    background: var(--glass-strong);
  }
  .dev[aria-pressed='true'] {
    background: var(--chip);
    box-shadow: var(--shadow-chip), 0 0 0 2px var(--spark);
  }
  .tab {
    display: inline-flex;
    align-items: center;
    padding: 9px 18px;
    border-radius: 999px;
    color: var(--fg);
    font-size: 15px;
    background: transparent;
    border: 0;
    cursor: pointer;
    text-decoration: none;
  }
  .tab:hover {
    background: var(--glass);
  }
  .tab[aria-selected='true'] {
    background: var(--chip);
    font-weight: 600;
    box-shadow: var(--shadow-chip);
  }
</style>
