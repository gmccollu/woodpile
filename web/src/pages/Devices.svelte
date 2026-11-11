<script lang="ts">
  import { tick } from 'svelte';
  import { patchDevice, type Device, type DevicePatch } from '../lib/api';
  import { ago, bytes, date, dateTime, num, rate } from '../lib/format';
  import { live, replaceDevice } from '../lib/status.svelte';
  import { ui } from '../lib/ui.svelte';
  import Icon from '../components/Icon.svelte';
  import StatusDot from '../components/StatusDot.svelte';

  type Col = 'name' | 'ip' | 'group' | 'status' | 'first_seen' | 'last_heard' | 'msgs_per_min' | 'total_msgs' | 'bytes_on_disk';

  let showHidden = $state(false);
  let sortBy = $state<Col>('name');
  let desc = $state(false);
  let editing = $state<{ id: string; field: 'name' | 'group' } | null>(null);
  let editValue = $state('');
  let saving = $state<string | null>(null);
  let rowError = $state<Record<string, string>>({});

  const all = $derived(live.devices ?? []);
  const hiddenCount = $derived(all.filter((d) => d.hidden).length);
  const groups = $derived([...new Set(all.map((d) => d.group).filter(Boolean))].sort());
  const filterText = $derived(ui.filter.trim().toLowerCase());
  const statusOrder = { online: 0, quiet: 1, offline: 2 };

  function key(d: Device, c: Col): string | number {
    if (c === 'name') return (d.name || d.ip).toLowerCase();
    if (c === 'status') return statusOrder[d.status];
    if (c === 'last_heard' || c === 'first_seen') return Date.parse(d[c]);
    return d[c] as string | number;
  }

  const rows = $derived(
    all
      .filter((d) => showHidden || !d.hidden)
      .filter((d) => !filterText || `${d.name} ${d.ip} ${d.group}`.toLowerCase().includes(filterText))
      .sort((a, b) => {
        const x = key(a, sortBy);
        const y = key(b, sortBy);
        const r = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y), undefined, { numeric: true });
        return desc ? -r : r;
      }),
  );

  function sort(c: Col) {
    if (sortBy === c) desc = !desc;
    else {
      sortBy = c;
      desc = ['first_seen', 'last_heard', 'msgs_per_min', 'total_msgs', 'bytes_on_disk'].includes(c);
    }
  }

  async function save(d: Device, patch: DevicePatch) {
    saving = d.id;
    rowError = { ...rowError, [d.id]: '' };
    try {
      replaceDevice(await patchDevice(d.id, patch));
    } catch (e) {
      rowError = { ...rowError, [d.id]: (e as Error).message };
    } finally {
      saving = null;
    }
  }

  async function startEdit(d: Device, field: 'name' | 'group') {
    editing = { id: d.id, field };
    editValue = d[field];
    await tick();
    const el = document.getElementById(`edit-${field}-${d.id}`) as HTMLInputElement | null;
    el?.focus();
    el?.select();
  }

  async function commit(d: Device) {
    if (!editing || editing.id !== d.id) return;
    const { field } = editing;
    const value = editValue;
    editing = null;
    if (value.trim() !== d[field]) await save(d, { [field]: value.trim() });
  }

  function onKey(e: KeyboardEvent, d: Device) {
    if (e.key === 'Enter') {
      e.preventDefault();
      commit(d);
    } else if (e.key === 'Escape') {
      editing = null;
    }
  }

  const cols: { c: Col; label: string; num?: boolean }[] = [
    { c: 'status', label: 'Status' },
    { c: 'name', label: 'Name' },
    { c: 'ip', label: 'IP address' },
    { c: 'group', label: 'Group' },
    { c: 'first_seen', label: 'First seen' },
    { c: 'last_heard', label: 'Last heard' },
    { c: 'msgs_per_min', label: 'Rate', num: true },
    { c: 'total_msgs', label: 'Messages', num: true },
    { c: 'bytes_on_disk', label: 'On disk', num: true },
  ];
</script>

<section class="card overflow-hidden p-0">
  <div class="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-3">
    <div>
      <h1 class="m-0 text-xl font-bold tracking-tight">Devices</h1>
      <p class="mt-1 mb-0 text-[13px] text-muted">
        Every sender, by source IP. Click a name or group to change it. Hiding a device keeps its logs until retention deletes them.
      </p>
    </div>
    {#if hiddenCount}
      <label class="flex items-center gap-2 text-[14px]">
        <input type="checkbox" bind:checked={showHidden} /> Show hidden ({hiddenCount})
      </label>
    {/if}
  </div>

  <datalist id="groups">
    {#each groups as g (g)}<option value={g}></option>{/each}
  </datalist>

  <div class="overflow-x-auto">
    <table class="w-full border-collapse text-[14px]">
      <thead>
        <tr>
          {#each cols as col (col.c)}
            <th scope="col" class:text-right={col.num} aria-sort={sortBy === col.c ? (desc ? 'descending' : 'ascending') : 'none'}>
              <button class="sort" onclick={() => sort(col.c)}>
                {col.label}{#if sortBy === col.c}<span aria-hidden="true">{desc ? ' ↓' : ' ↑'}</span>{/if}
              </button>
            </th>
          {/each}
          <th scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#if live.devices === null}
          <tr><td colspan="10" class="text-muted">Loading…</td></tr>
        {:else if !rows.length}
          <tr><td colspan="10" class="text-muted">{all.length ? 'No devices match.' : 'No devices have sent anything yet.'}</td></tr>
        {/if}
        {#each rows as d (d.id)}
          <tr class:opacity-60={d.hidden}>
            <td><StatusDot status={d.status} label /></td>
            <td class="min-w-[160px]">
              {#if editing?.id === d.id && editing.field === 'name'}
                <input
                  id="edit-name-{d.id}"
                  class="field py-1"
                  bind:value={editValue}
                  maxlength="64"
                  placeholder={d.ip}
                  aria-label="Name for {d.ip}"
                  onkeydown={(e) => onKey(e, d)}
                  onblur={() => commit(d)}
                />
              {:else}
                <button class="editable" onclick={() => startEdit(d, 'name')} aria-label="Rename {d.name || d.ip}">
                  {#if d.name}<b>{d.name}</b>{:else}<span class="text-muted italic">unnamed</span>{/if}
                  <Icon name="edit" size={14} />
                </button>
              {/if}
              {#if rowError[d.id]}<div class="mt-1 text-[12.5px] text-bad-fg" role="alert">{rowError[d.id]}</div>{/if}
            </td>
            <td class="font-mono text-[13px]">{d.ip}</td>
            <td class="min-w-[120px]">
              {#if editing?.id === d.id && editing.field === 'group'}
                <input
                  id="edit-group-{d.id}"
                  class="field py-1"
                  list="groups"
                  bind:value={editValue}
                  maxlength="32"
                  aria-label="Group for {d.name || d.ip}"
                  onkeydown={(e) => onKey(e, d)}
                  onblur={() => commit(d)}
                />
              {:else}
                <button class="editable" onclick={() => startEdit(d, 'group')} aria-label="Set group for {d.name || d.ip}">
                  {#if d.group}{d.group}{:else}<span class="text-muted">none</span>{/if}
                  <Icon name="edit" size={14} />
                </button>
              {/if}
            </td>
            <td class="whitespace-nowrap" title={dateTime(d.first_seen)}>{date(d.first_seen)}</td>
            <td class="whitespace-nowrap" title={dateTime(d.last_heard)}>{ago(d.last_heard, live.now)}</td>
            <td class="text-right whitespace-nowrap tabular-nums">{rate(d.msgs_per_min)}</td>
            <td class="text-right whitespace-nowrap tabular-nums">{num(d.total_msgs)}</td>
            <td class="text-right whitespace-nowrap tabular-nums">{bytes(d.bytes_on_disk)}</td>
            <td class="text-right">
              <button
                class="ghost-btn whitespace-nowrap"
                disabled={saving === d.id}
                onclick={() => save(d, { hidden: !d.hidden })}
                title={d.hidden ? 'Show this device again' : 'Forget this device: hide it everywhere. Its logs are kept until retention deletes them.'}
              >
                <Icon name={d.hidden ? 'eye' : 'eyeOff'} size={15} />{d.hidden ? 'Show' : 'Hide'}
              </button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>

<style>
  th {
    text-align: left;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--muted);
    padding: 8px 10px;
    border-bottom: 1px solid var(--divider);
    white-space: nowrap;
  }
  th.text-right {
    text-align: right;
  }
  td {
    padding: 9px 10px;
    border-bottom: 1px solid var(--divider);
    vertical-align: middle;
  }
  tr:last-child td {
    border-bottom: 0;
  }
  th:first-child,
  td:first-child {
    padding-left: 20px;
  }
  th:last-child,
  td:last-child {
    padding-right: 16px;
  }
  .sort {
    background: none;
    border: 0;
    padding: 0;
    font: inherit;
    color: inherit;
    cursor: pointer;
  }
  .editable {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: none;
    border: 1px solid transparent;
    border-radius: 10px;
    padding: 3px 8px;
    margin-left: -9px;
    font: inherit;
    color: var(--fg);
    cursor: text;
  }
  .editable :global(svg) {
    opacity: 0;
    color: var(--muted);
  }
  .editable:hover,
  .editable:focus-visible {
    border-color: var(--input-border);
    background: var(--input);
  }
  .editable:hover :global(svg),
  .editable:focus-visible :global(svg) {
    opacity: 1;
  }
</style>
