<script lang="ts">
  import type { Device } from '../lib/api';
  import { scopeLabel, type Scope } from '../lib/scope';
  import StatusDot from './StatusDot.svelte';

  let {
    value = $bindable(),
    devices,
    allowGroups = true,
  }: { value: Scope; devices: Device[]; allowGroups?: boolean } = $props();

  let open = $state(false);
  let filter = $state('');
  let root: HTMLDivElement | undefined = $state();

  const groups = $derived([...new Set(devices.filter((d) => !d.hidden).map((d) => d.group).filter(Boolean))].sort());
  const hasUngrouped = $derived(devices.some((d) => !d.hidden && !d.group));
  const listed = $derived(
    devices
      .filter((d) => !filter || `${d.name} ${d.ip} ${d.group}`.toLowerCase().includes(filter.toLowerCase()))
      .sort((a, b) => (a.name || a.ip).localeCompare(b.name || b.ip, undefined, { numeric: true })),
  );

  $effect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (root && !root.contains(e.target as Node)) open = false;
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') open = false;
    };
    addEventListener('mousedown', close);
    addEventListener('keydown', esc);
    return () => {
      removeEventListener('mousedown', close);
      removeEventListener('keydown', esc);
    };
  });

  function toggle(id: string) {
    const cur = value.mode === 'devices' ? value.devices : [];
    value = { mode: 'devices', group: '', devices: cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id] };
  }
</script>

<div class="dp" bind:this={root}>
  <button type="button" class="ghost-btn trigger" aria-expanded={open} aria-haspopup="dialog" onclick={() => (open = !open)}>
    <span class="truncate">{scopeLabel(value, devices)}</span> <span aria-hidden="true">▾</span>
  </button>
  {#if open}
    <div class="pop glass" role="dialog" aria-label="Choose devices">
      {#if allowGroups}
        <div class="opts" role="radiogroup" aria-label="Scope">
          <label><input type="radio" checked={value.mode === 'all'} onchange={() => (value = { mode: 'all', group: '', devices: [] })} /> All devices</label>
          {#each groups as g (g)}
            <label><input type="radio" checked={value.mode === 'group' && value.group === g} onchange={() => (value = { mode: 'group', group: g, devices: [] })} /> Group: {g}</label>
          {/each}
          {#if hasUngrouped}
            <label><input type="radio" checked={value.mode === 'ungrouped'} onchange={() => (value = { mode: 'ungrouped', group: '', devices: [] })} /> Ungrouped</label>
          {/if}
        </div>
      {/if}
      <input class="field my-2" placeholder="Find a device" bind:value={filter} aria-label="Find a device" />
      <div class="list">
        {#each listed as d (d.id)}
          <label class="dev">
            <input type="checkbox" checked={value.mode === 'devices' && value.devices.includes(d.id)} onchange={() => toggle(d.id)} />
            <StatusDot status={d.status} />
            <span class="truncate font-medium">{d.name || d.ip}</span>
            <span class="text-muted ml-auto font-mono text-[12px]">{d.name ? d.ip : ''}{d.hidden ? ' · hidden' : ''}</span>
          </label>
        {/each}
      </div>
      <div class="mt-2 flex gap-2">
        <button
          type="button"
          class="ghost-btn"
          onclick={() => (value = { mode: 'devices', group: '', devices: listed.filter((d) => !d.hidden).map((d) => d.id) })}>Select shown</button
        >
        <button type="button" class="ghost-btn" onclick={() => (value = { mode: 'devices', group: '', devices: [] })}>Clear</button>
        <button type="button" class="pill-btn ml-auto" onclick={() => (open = false)}>Done</button>
      </div>
    </div>
  {/if}
</div>

<style>
  .dp {
    position: relative;
  }
  .trigger {
    max-width: 320px;
    padding: 7px 14px;
  }
  .pop {
    position: absolute;
    z-index: 30;
    top: calc(100% + 6px);
    left: 0;
    width: 360px;
    padding: 12px;
    border-radius: 18px;
    background: var(--glass-strong);
    box-shadow: var(--shadow);
  }
  .opts {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 14px;
    padding-bottom: 6px;
    border-bottom: 1px solid var(--divider);
  }
  .list {
    max-height: 260px;
    overflow: auto;
    display: flex;
    flex-direction: column;
  }
  .dev {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 4px;
    font-size: 14px;
    border-radius: 8px;
    min-width: 0;
  }
  .dev:hover {
    background: var(--glass);
  }
</style>
