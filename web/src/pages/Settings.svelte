<script lang="ts">
  import {
    ApiError,
    getRejected,
    getSettings,
    getStorageHistory,
    putSettings,
    type RejectedSender,
    type Settings,
    type StorageHistoryEntry,
  } from '../lib/api';
  import { ago, bytes, dateTime, GiB, MiB, num } from '../lib/format';
  import { deviceName, live, refreshStatus } from '../lib/status.svelte';

  // The form edits friendlier units (GiB, MiB, minutes, one CIDR per line) and converts on save.
  interface Form {
    udpEnabled: boolean;
    udpAddr: string;
    udpPort: number;
    tcpEnabled: boolean;
    tcpAddr: string;
    tcpPort: number;
    allowlist: string;
    dataDir: string;
    retentionDays: number;
    retentionGiB: number;
    partMiB: number;
    lanMode: boolean;
    uiPort: number;
    quietMin: number;
    offlineMin: number;
  }

  let saved = $state<Settings | null>(null);
  let form = $state<Form | null>(null);
  let loadError = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let busy = $state(false);
  let savedAt = $state(0);
  let history = $state<StorageHistoryEntry[] | null>(null);
  let rejected = $state<RejectedSender[] | null>(null);
  /** The allowlist as last sent, so "listeners.allowlist[2]" errors can name the entry. */
  let sentAllowlist = $state<string[]>([]);

  const round = (n: number, d = 2) => Math.round(n * 10 ** d) / 10 ** d;

  function toForm(s: Settings): Form {
    return {
      udpEnabled: s.listeners.udp.enabled,
      udpAddr: s.listeners.udp.addr,
      udpPort: s.listeners.udp.port,
      tcpEnabled: s.listeners.tcp.enabled,
      tcpAddr: s.listeners.tcp.addr,
      tcpPort: s.listeners.tcp.port,
      allowlist: s.listeners.allowlist.join('\n'),
      dataDir: s.storage.data_dir,
      retentionDays: s.storage.retention_days,
      retentionGiB: round(s.storage.retention_bytes / GiB),
      partMiB: round(s.storage.part_bytes / MiB),
      lanMode: s.ui.lan_mode,
      uiPort: s.ui.port,
      quietMin: round(s.quiet.quiet_after_seconds / 60),
      offlineMin: round(s.quiet.offline_after_seconds / 60),
    };
  }

  // Empty number inputs bind to null; send NaN-free values and let the server reject them.
  const n = (v: number | null) => (typeof v === 'number' && Number.isFinite(v) ? v : 0);

  function fromForm(f: Form): Settings {
    return {
      listeners: {
        udp: { enabled: f.udpEnabled, addr: f.udpAddr.trim(), port: n(f.udpPort) },
        tcp: { enabled: f.tcpEnabled, addr: f.tcpAddr.trim(), port: n(f.tcpPort) },
        allowlist: parseAllowlist(f.allowlist),
      },
      storage: {
        data_dir: f.dataDir,
        retention_days: n(f.retentionDays),
        retention_bytes: Math.round(n(f.retentionGiB) * GiB),
        part_bytes: Math.round(n(f.partMiB) * MiB),
      },
      ui: { lan_mode: f.lanMode, port: n(f.uiPort) },
      quiet: {
        quiet_after_seconds: Math.round(n(f.quietMin) * 60),
        offline_after_seconds: Math.round(n(f.offlineMin) * 60),
      },
    };
  }

  function parseAllowlist(text: string): string[] {
    return text
      .split(/[\s,]+/)
      .map((x) => x.trim())
      .filter(Boolean);
  }

  async function loadRejected() {
    try {
      rejected = await getRejected();
    } catch {
      rejected = rejected ?? [];
    }
  }

  $effect(() => {
    loadRejected();
    const t = setInterval(loadRejected, 15000);
    return () => clearInterval(t);
  });

  const hostCidr = (ip: string) => `${ip}/${ip.includes(':') ? 128 : 32}`;
  const inDraft = (ip: string) => !!form && parseAllowlist(form.allowlist).some((c) => c === ip || c === hostCidr(ip));

  function allow(ip: string) {
    if (!form || inDraft(ip)) return;
    const cur = form.allowlist.replace(/\s+$/, '');
    form.allowlist = cur ? `${cur}\n${hostCidr(ip)}` : hostCidr(ip);
  }

  async function load() {
    try {
      saved = await getSettings();
      form = toForm(saved);
      loadError = '';
    } catch (e) {
      loadError = (e as Error).message;
    }
    try {
      history = await getStorageHistory();
    } catch {
      history = [];
    }
  }
  load();

  const dirty = $derived(!!form && !!saved && JSON.stringify(fromForm(form)) !== JSON.stringify(saved));

  // Field paths this page shows inline. Anything else the server reports goes in the summary.
  const FIELDS = [
    'listeners.udp.enabled',
    'listeners.udp.addr',
    'listeners.udp.port',
    'listeners.tcp.enabled',
    'listeners.tcp.addr',
    'listeners.tcp.port',
    'listeners.allowlist',
    'storage.data_dir',
    'storage.retention_days',
    'storage.retention_bytes',
    'storage.part_bytes',
    'ui.lan_mode',
    'ui.port',
    'quiet.quiet_after_seconds',
    'quiet.offline_after_seconds',
  ];

  /**
   * The message(s) for a field. Error keys are dotted paths with [i] for list items
   * (docs/api.md), so "listeners.allowlist[2]" belongs to the allowlist field and is
   * shown as "Entry 3 (value): message".
   */
  function err(path: string): string {
    const out: string[] = [];
    if (errors[path]) out.push(errors[path]);
    for (const k of Object.keys(errors)) {
      const m = k.startsWith(`${path}[`) ? /^\[(\d+)\]$/.exec(k.slice(path.length)) : null;
      if (m) {
        const i = Number(m[1]);
        const v = path === 'listeners.allowlist' ? sentAllowlist[i] : undefined;
        out.push(`Entry ${i + 1}${v ? ` (${v})` : ''}: ${errors[k]}`);
      } else if (k.startsWith(`${path}.`) || (k.startsWith(`${path}[`) && k !== path)) {
        out.push(errors[k]);
      }
    }
    return out.join('\n');
  }
  const owned = (k: string) => FIELDS.some((f) => k === f || k.startsWith(`${f}.`) || k.startsWith(`${f}[`));
  const otherErrors = $derived(Object.entries(errors).filter(([k]) => !owned(k)));

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!form) return;
    busy = true;
    errors = {};
    formError = '';
    const body = fromForm(form);
    sentAllowlist = body.listeners.allowlist;
    try {
      saved = await putSettings(body);
      form = toForm(saved);
      savedAt = Date.now();
      setTimeout(() => (savedAt = 0), 2500);
      refreshStatus();
      loadRejected();
    } catch (x) {
      if (x instanceof ApiError && Object.keys(x.fields).length) {
        errors = x.fields;
        formError = `${Object.keys(x.fields).length === 1 ? 'One setting needs' : 'Some settings need'} fixing before saving.`;
        await Promise.resolve();
        (document.querySelector('[aria-invalid="true"]') as HTMLElement | null)?.focus();
      } else {
        formError = (x as Error).message;
      }
    } finally {
      busy = false;
    }
  }

  function revert() {
    if (saved) form = toForm(saved);
    errors = {};
    formError = '';
  }

  const PROTOS = [
    { p: 'udp', label: 'UDP' },
    { p: 'tcp', label: 'TCP' },
  ] as const;
  const isMac = $derived(live.status?.os.startsWith('darwin') ?? false);
  const reasonText = { age: 'older than the retention period', cap: 'disk cap reached', low_disk: 'low disk space' };
</script>

{#snippet fieldError(path: string)}
  {#if err(path)}<p class="ferr" id="err-{path}">{err(path)}</p>{/if}
{/snippet}

{#if loadError}
  <p class="card text-bad-fg" role="alert">Couldn't load settings: {loadError}</p>
{:else if !form}
  <p class="card text-muted">Loading…</p>
{:else}
  <form class="space-y-4" onsubmit={save} novalidate>
    <div class="savebar">
      <h1 class="m-0 text-xl font-bold tracking-tight">Settings</h1>
      <div class="flex items-center gap-2">
        <span class="text-[14px]" aria-live="polite">
          {#if savedAt}<span class="text-ok-fg">Saved.</span>{:else if dirty}<span class="text-muted">Unsaved changes</span>{/if}
        </span>
        <button type="button" class="ghost-btn" onclick={revert} disabled={!dirty || busy}>Revert</button>
        <button type="submit" class="pill-btn" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
      </div>
    </div>

    {#if formError}
      <div class="rounded-2xl border border-[var(--error-border)] bg-[var(--error-bg)] px-4 py-3 text-[14px]" role="alert">
        {formError}
        {#each otherErrors as [k, m] (k)}
          <div class="mt-1"><code class="font-mono text-[13px]">{k}</code>: {m}</div>
        {/each}
      </div>
    {/if}

    <section class="card">
      <h2 class="sec">Listeners</h2>
      <p class="hint mt-0">
        Where devices send syslog. Changes apply when you save; a port that's in use shows a banner.
        {#if isMac}On macOS, ports below 1024 can only be bound on all addresses (0.0.0.0 or ::).{/if}
      </p>
      {#each PROTOS as l (l.p)}
        <fieldset class="lst">
          <legend class="sr-only">{l.label} listener</legend>
          <label class="flex items-center gap-2 font-semibold">
            {#if l.p === 'udp'}
              <input type="checkbox" bind:checked={form.udpEnabled} aria-invalid={!!err('listeners.udp.enabled')} aria-describedby="err-listeners.udp.enabled" />
            {:else}
              <input type="checkbox" bind:checked={form.tcpEnabled} aria-invalid={!!err('listeners.tcp.enabled')} aria-describedby="err-listeners.tcp.enabled" />
            {/if}
            {l.label}
          </label>
          <label class="lbl">
            <span>Bind address</span>
            {#if l.p === 'udp'}
              <input class="field font-mono" bind:value={form.udpAddr} aria-invalid={!!err('listeners.udp.addr')} aria-describedby="err-listeners.udp.addr" spellcheck="false" />
            {:else}
              <input class="field font-mono" bind:value={form.tcpAddr} aria-invalid={!!err('listeners.tcp.addr')} aria-describedby="err-listeners.tcp.addr" spellcheck="false" />
            {/if}
            {@render fieldError(`listeners.${l.p}.addr`)}
          </label>
          <label class="lbl w-40">
            <span>Port</span>
            {#if l.p === 'udp'}
              <input class="field" type="number" bind:value={form.udpPort} aria-invalid={!!err('listeners.udp.port')} aria-describedby="err-listeners.udp.port" />
            {:else}
              <input class="field" type="number" bind:value={form.tcpPort} aria-invalid={!!err('listeners.tcp.port')} aria-describedby="err-listeners.tcp.port" />
            {/if}
            {@render fieldError(`listeners.${l.p}.port`)}
          </label>
          <div class="basis-full">{@render fieldError(`listeners.${l.p}.enabled`)}</div>
        </fieldset>
      {/each}
      <label class="lbl mt-4">
        <span>Sender allowlist</span>
        <textarea
          class="field min-h-[84px] font-mono"
          bind:value={form.allowlist}
          placeholder="Empty: accept every sender. One CIDR per line, such as 10.1.0.0/16"
          aria-invalid={!!err('listeners.allowlist')}
          aria-describedby="err-listeners.allowlist"
          spellcheck="false"
        ></textarea>
        {@render fieldError('listeners.allowlist')}
        <span class="hint">Empty means every sender is accepted. Changes apply when you save.</span>
      </label>

      <h3 class="mt-4 mb-2 text-[14px] font-semibold">Refused senders, last 24 h</h3>
      {#if rejected === null}
        <p class="hint">Loading…</p>
      {:else if !rejected.length}
        <p class="hint">None. {saved?.listeners.allowlist.length ? 'Every sender is inside the allowlist.' : 'The allowlist is empty, so nothing is refused.'}</p>
      {:else}
        <div class="overflow-auto rounded-xl border border-[var(--divider)]">
          <table class="w-full border-collapse text-[13px]">
            <thead>
              <tr>
                <th scope="col">Sender</th>
                <th scope="col" class="text-right">Messages</th>
                <th scope="col">Last seen</th>
                <th scope="col"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {#each rejected as r (r.ip)}
                <tr>
                  <td class="font-mono">{r.ip}</td>
                  <td class="text-right tabular-nums">{num(r.count_24h)}</td>
                  <td title={dateTime(r.last_seen)}>{ago(r.last_seen, live.now)}</td>
                  <td class="text-right">
                    <button type="button" class="ghost-btn whitespace-nowrap" onclick={() => allow(r.ip)} disabled={inDraft(r.ip)} aria-label="Allow {r.ip}">
                      {inDraft(r.ip) ? 'Added, save to apply' : 'Allow'}
                    </button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <section class="card">
      <h2 class="sec">Storage</h2>
      <p class="hint mt-0">Logs are deleted oldest first when either limit is reached. If the disk runs low, Woodpile deletes more, and never fills it.</p>
      <label class="lbl">
        <span>Data folder</span>
        <input class="field font-mono" value={form.dataDir} readonly aria-invalid={!!err('storage.data_dir')} aria-describedby="err-storage.data_dir hint-datadir" />
        <span class="hint" id="hint-datadir">Set in the config file; moving it needs a restart.</span>
        {@render fieldError('storage.data_dir')}
      </label>
      <div class="mt-3 grid gap-4 sm:grid-cols-3">
        <label class="lbl">
          <span>Keep logs for (days)</span>
          <input class="field" type="number" min="1" bind:value={form.retentionDays} aria-invalid={!!err('storage.retention_days')} aria-describedby="err-storage.retention_days" />
          {@render fieldError('storage.retention_days')}
        </label>
        <label class="lbl">
          <span>Disk cap (GiB)</span>
          <input class="field" type="number" min="1" step="any" bind:value={form.retentionGiB} aria-invalid={!!err('storage.retention_bytes')} aria-describedby="err-storage.retention_bytes" />
          {@render fieldError('storage.retention_bytes')}
        </label>
        <label class="lbl">
          <span>Split files at (MiB)</span>
          <input class="field" type="number" min="1" step="any" bind:value={form.partMiB} aria-invalid={!!err('storage.part_bytes')} aria-describedby="err-storage.part_bytes" />
          {@render fieldError('storage.part_bytes')}
        </label>
      </div>

      <h3 class="mt-5 mb-2 text-[14px] font-semibold">Storage history</h3>
      {#if history === null}
        <p class="hint">Loading…</p>
      {:else if !history.length}
        <p class="hint">Nothing has been deleted yet.</p>
      {:else}
        <div class="max-h-64 overflow-auto rounded-xl border border-[var(--divider)]">
          <table class="w-full border-collapse text-[13px]">
            <thead>
              <tr><th scope="col">Deleted</th><th scope="col">File</th><th scope="col">Device</th><th scope="col" class="text-right">Size</th><th scope="col">Why</th></tr>
            </thead>
            <tbody>
              {#each history as h (h.time + h.file)}
                <tr>
                  <td class="whitespace-nowrap">{dateTime(h.time)}</td>
                  <td class="font-mono break-all">{h.file}</td>
                  <td>{deviceName(h.device_id)}</td>
                  <td class="text-right whitespace-nowrap tabular-nums">{bytes(h.bytes)}</td>
                  <td>{reasonText[h.reason] ?? h.reason}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <section class="card">
      <h2 class="sec">UI access</h2>
      <label class="flex items-start gap-3">
        <input type="checkbox" bind:checked={form.lanMode} disabled aria-describedby="hint-lan err-ui.lan_mode" class="mt-1" />
        <span>
          <span class="font-semibold">LAN mode</span>
          <span class="ml-2 rounded-full bg-chip px-2 py-0.5 text-[12px] font-semibold text-accent-fg">Not available yet</span>
          <span class="hint block" id="hint-lan">
            Lets other machines open this UI. It will need a password and switch the UI to HTTPS. Until then the UI is only reachable from
            this machine, on 127.0.0.1:{saved?.ui.port}.
          </span>
        </span>
      </label>
      {@render fieldError('ui.lan_mode')}
      <label class="lbl mt-3 w-40">
        <span>UI port</span>
        <input class="field" type="number" bind:value={form.uiPort} aria-invalid={!!err('ui.port')} aria-describedby="err-ui.port" />
        {@render fieldError('ui.port')}
      </label>
    </section>

    <section class="card">
      <h2 class="sec">Quiet devices</h2>
      <p class="hint mt-0">A device's dot turns amber when nothing has arrived for a while, and red after longer.</p>
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="lbl">
          <span><span class="dot" style:background="var(--warn)" aria-hidden="true"></span>Quiet after (minutes)</span>
          <input class="field" type="number" min="0.5" step="any" bind:value={form.quietMin} aria-invalid={!!err('quiet.quiet_after_seconds')} aria-describedby="err-quiet.quiet_after_seconds" />
          {@render fieldError('quiet.quiet_after_seconds')}
        </label>
        <label class="lbl">
          <span><span class="dot" style:background="var(--bad)" aria-hidden="true"></span>Offline after (minutes)</span>
          <input class="field" type="number" min="1" step="any" bind:value={form.offlineMin} aria-invalid={!!err('quiet.offline_after_seconds')} aria-describedby="err-quiet.offline_after_seconds" />
          {@render fieldError('quiet.offline_after_seconds')}
        </label>
      </div>
    </section>
  </form>
{/if}

<style>
  .savebar {
    position: sticky;
    top: 8px;
    z-index: 5;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 12px 10px 18px;
    border-radius: 999px;
    background: var(--glass-strong);
    border: 1px solid var(--border);
    -webkit-backdrop-filter: blur(16px);
    backdrop-filter: blur(16px);
  }
  .field[readonly] {
    background: transparent;
    border-style: dashed;
  }
  .sec {
    margin: 0 0 6px;
    font-size: 17px;
    font-weight: 700;
  }
  .hint {
    font-size: 13px;
    color: var(--muted);
    margin-bottom: 10px;
  }
  .lbl {
    display: flex;
    flex-direction: column;
    gap: 5px;
    font-size: 13.5px;
    font-weight: 500;
  }
  .lbl > .hint {
    margin: 0;
    font-weight: 400;
  }
  .lst {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 12px 18px;
    border: 0;
    border-top: 1px solid var(--divider);
    padding: 14px 0 4px;
    margin: 0;
  }
  .lst > label:first-of-type {
    width: 80px;
    padding-top: 30px;
  }
  .lst > .lbl:not(.w-40) {
    flex: 1;
    min-width: 200px;
  }
  .ferr {
    white-space: pre-line;
    margin: 2px 0 0;
    font-size: 13px;
    font-weight: 500;
    color: var(--bad-fg);
  }
  .dot {
    display: inline-block;
    width: 9px;
    height: 9px;
    border-radius: 50%;
    margin-right: 6px;
  }
  th {
    text-align: left;
    font-weight: 600;
    color: var(--muted);
    padding: 6px 10px;
    border-bottom: 1px solid var(--divider);
    position: sticky;
    top: 0;
    background: var(--glass-strong);
  }
  td {
    padding: 6px 10px;
    border-bottom: 1px solid var(--divider);
  }
</style>
