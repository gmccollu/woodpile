<script lang="ts">
  import { tick } from 'svelte';
  import { getDevices, patchDevice, type Device, type DevicePatch, type DeviceSort } from '../lib/api';
  import { ago, bytes, date, dateTime, num, rate } from '../lib/format';
  import { DEVICE_POLL_MS, groupsSeen, live, NOW_WITHIN_S, refreshGroups, remember, replaceDevice } from '../lib/status.svelte';
  import { ui } from '../lib/ui.svelte';
  import Icon from '../components/Icon.svelte';
  import StatusDot from '../components/StatusDot.svelte';
  import { askBlock, blockUi, unblock } from '../lib/block.svelte';

  type Col = 'name' | 'ip' | 'group' | 'status' | 'first_seen' | 'last_heard' | 'msgs_per_min' | 'total_msgs' | 'bytes_on_disk';

  // The server pages, filters and sorts (docs/api.md), so this stays fast with 10,000 devices.
  const PAGE = 200;
  /** Columns the API can sort by, and its sort key. Direction is the server's (see the report). */
  const SORTS: Partial<Record<Col, DeviceSort>> = { name: 'name', msgs_per_min: 'rate', last_heard: 'last_heard', first_seen: 'first_seen' };
  const SORT_HINT: Record<DeviceSort, string> = { name: 'A to Z', rate: 'busiest first', last_heard: 'most recent first', first_seen: 'newest first' };

  let showHidden = $state(false);
  /** All devices, or only the blocked ones. */
  let view = $state<'all' | 'blocked'>('all');
  let sortBy = $state<DeviceSort>('name');
  let offset = $state(0);
  let rows = $state<Device[] | null>(null);
  let total = $state(0);
  let hiddenCount = $state(0);
  let loadError = $state('');
  let editing = $state<{ id: string; field: 'name' | 'group' } | null>(null);
  let editValue = $state('');
  let saving = $state<string | null>(null);
  let rowError = $state<Record<string, string>>({});

  const groups = $derived(rows ? groupsSeen() : []);
  let q = $state('');
  $effect(() => {
    const v = ui.filter.trim();
    const t = setTimeout(() => {
      if (v !== q) {
        q = v;
        offset = 0;
      }
    }, 250);
    return () => clearTimeout(t);
  });

  $effect(() => {
    void blockUi.version; // reload after a block or unblock
    const params = {
      limit: PAGE,
      offset,
      sort: sortBy,
      q,
      hidden: showHidden ? ('include' as const) : ('exclude' as const),
      blocked: view === 'blocked' ? ('only' as const) : ('include' as const),
    };
    let stop = false;
    const ctrl = new AbortController();
    const load = async () => {
      // Don't overwrite a row while it's being edited.
      if (editing) return;
      const seq = changes;
      try {
        const [page, hid] = await Promise.all([
          getDevices(params, ctrl.signal),
          getDevices({ limit: 1, hidden: 'only' }, ctrl.signal),
        ]);
        // A rename, group or hide saved while this was in flight wins over these older rows.
        if (stop || seq !== changes) return;
        remember(page.devices);
        rows = view === 'blocked' ? page.devices.filter((d) => d.blocked) : page.devices;
        total = page.total;
        hiddenCount = hid.total;
        loadError = '';
      } catch (e) {
        if (!stop && (e as Error).name !== 'AbortError') loadError = (e as Error).message;
      }
    };
    load();
    const t = setInterval(load, DEVICE_POLL_MS);
    return () => {
      stop = true;
      ctrl.abort();
      clearInterval(t);
    };
  });

  function sort(c: Col) {
    const k = SORTS[c];
    if (!k || k === sortBy) return;
    sortBy = k;
    offset = 0;
  }

  /** Bumped on every saved change, so a poll that started before it can't undo it on screen. */
  let changes = 0;

  function replaceRow(d: Device) {
    changes++;
    replaceDevice(d);
    if (rows) rows = rows.map((x) => (x.id === d.id ? d : x));
  }

  async function save(d: Device, patch: DevicePatch) {
    saving = d.id;
    rowError = { ...rowError, [d.id]: '' };
    try {
      replaceRow(await patchDevice(d.id, patch));
      if (patch.group !== undefined || patch.hidden !== undefined) refreshGroups();
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
    <div class="seg" role="radiogroup" aria-label="Which devices">
      <button role="radio" aria-checked={view === 'all'} onclick={() => ((view = 'all'), (offset = 0))}>All</button>
      <button role="radio" aria-checked={view === 'blocked'} onclick={() => ((view = 'blocked'), (offset = 0))}>Blocked</button>
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
            {@const k = SORTS[col.c]}
            <th scope="col" class:text-right={col.num} aria-sort={k && sortBy === k ? (k === 'name' ? 'ascending' : 'descending') : undefined}>
              {#if k}
                <button class="sort" class:on={sortBy === k} onclick={() => sort(col.c)} title="Sort: {SORT_HINT[k]}">
                  {col.label}{#if sortBy === k}<span aria-hidden="true">{k === 'name' ? ' ↑' : ' ↓'}</span>{/if}
                </button>
              {:else}
                {col.label}
              {/if}
            </th>
          {/each}
          <th scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#if loadError}
          <tr><td colspan="10" class="text-bad-fg">Couldn't load devices: {loadError}</td></tr>
        {:else if rows === null}
          <tr><td colspan="10" class="text-muted">Loading…</td></tr>
        {:else if !rows.length}
          <tr><td colspan="10" class="text-muted">{q || live.deviceTotal ? 'No devices match.' : 'No devices have sent anything yet.'}</td></tr>
        {/if}
        {#each rows ?? [] as d (d.id)}
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
            <td class="whitespace-nowrap" title={dateTime(d.last_heard)}>{ago(d.last_heard, live.now, NOW_WITHIN_S)}</td>
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
              {#if d.blocked}
                <button class="ghost-btn whitespace-nowrap" onclick={() => unblock(d.id)} title="Accept this sender's messages again">Unblock</button>
              {:else}
                <button class="ghost-btn whitespace-nowrap" onclick={() => askBlock(d.id)} title="Refuse and count this sender's messages; its logs stay searchable">Block</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if rows && total > 0}
    <div class="pager">
      <span class="text-[13px] text-muted" aria-live="polite">
        {num(offset + 1)}–{num(Math.min(offset + rows.length, total))} of {num(total)}{q ? ` matching “${q}”` : ''}
      </span>
      <span class="grow"></span>
      <button class="ghost-btn" disabled={offset === 0} onclick={() => (offset = Math.max(0, offset - PAGE))}>← Previous</button>
      <button class="ghost-btn" disabled={offset + PAGE >= total} onclick={() => (offset += PAGE)}>Next →</button>
    </div>
  {/if}
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
  .seg {
    display: inline-flex;
    background: var(--glass);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 3px;
  }
  .seg button {
    border: 0;
    background: transparent;
    color: var(--fg);
    padding: 5px 12px;
    border-radius: 999px;
    font-size: 13px;
    cursor: pointer;
  }
  .seg button[aria-checked='true'] {
    background: var(--chip);
    font-weight: 600;
    box-shadow: var(--shadow-chip);
  }
  .pager {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 16px 14px 20px;
    border-top: 1px solid var(--divider);
  }
  .sort.on {
    color: var(--fg);
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
