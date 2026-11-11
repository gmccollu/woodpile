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
  import { auth, loadSession } from '../lib/session.svelte';
  import { router } from '../lib/router.svelte';
  import { failingNow, noteSave, undo, undoLastSave } from '../lib/undo.svelte';
  import PasswordForm from '../components/PasswordForm.svelte';
  import CopyButton from '../components/CopyButton.svelte';

  // The form edits friendlier units (GiB, MiB, minutes, one CIDR per line) and converts on save.
  interface Form {
    udpEnabled: boolean;
    udpAddr: string;
    udpPort: number;
    tcpEnabled: boolean;
    tcpAddr: string;
    tcpPort: number;
    allowlist: string;
    maxDevices: number;
    burst: number;
    perMinute: number;
    dataDir: string;
    retentionDays: number;
    retentionGiB: number;
    partMiB: number;
    lanMode: boolean;
    requireLocal: boolean;
    uiPort: number;
    certFile: string;
    keyFile: string;
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
  /** Set after a save that changed lan_mode or the UI port: the server restarts at a new address. */
  let moved = $state<{ url: string; lan: boolean; port: number } | null>(null);
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
      maxDevices: s.listeners.max_devices,
      burst: s.listeners.new_device_burst,
      perMinute: s.listeners.new_devices_per_minute,
      dataDir: s.storage.data_dir,
      retentionDays: s.storage.retention_days,
      retentionGiB: round(s.storage.retention_bytes / GiB),
      partMiB: round(s.storage.part_bytes / MiB),
      lanMode: s.ui.lan_mode,
      requireLocal: !!s.ui.require_login_local,
      uiPort: s.ui.port,
      certFile: s.ui.cert_file ?? '',
      keyFile: s.ui.key_file ?? '',
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
        max_devices: n(f.maxDevices),
        new_device_burst: n(f.burst),
        new_devices_per_minute: n(f.perMinute),
      },
      storage: {
        data_dir: f.dataDir,
        retention_days: n(f.retentionDays),
        retention_bytes: Math.round(n(f.retentionGiB) * GiB),
        part_bytes: Math.round(n(f.partMiB) * MiB),
      },
      ui: {
        lan_mode: f.lanMode,
        port: n(f.uiPort),
        cert_file: f.certFile.trim(),
        key_file: f.keyFile.trim(),
        require_login_local: f.requireLocal,
      },
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
    // docs/api.md: allowlist entries may be a bare IP or a CIDR.
    form.allowlist = cur ? `${cur}\n${ip}` : ip;
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

  // "Undo last save" (here or on the banner) put the old settings back: show them.
  let undoSeen = undo.version;
  $effect(() => {
    if (undo.version !== undoSeen) {
      undoSeen = undo.version;
      load();
    }
  });

  // The device_limit banner links to #/settings?section=limits.
  let scrolled = false;
  $effect(() => {
    if (scrolled || !form || !router.query.includes('section=limits')) return;
    scrolled = true;
    requestAnimationFrame(() => document.getElementById('sender-limits')?.scrollIntoView({ block: 'start', behavior: 'instant' }));
  });

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
    'listeners.max_devices',
    'listeners.new_device_burst',
    'listeners.new_devices_per_minute',
    'storage.data_dir',
    'storage.retention_days',
    'storage.retention_bytes',
    'storage.part_bytes',
    'ui.lan_mode',
    'ui.require_login_local',
    'ui.port',
    'ui.cert_file',
    'ui.key_file',
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
    const before = saved?.ui;
    const prevSettings = saved ? $state.snapshot(saved) : null;
    const failingBefore = failingNow();
    try {
      saved = await putSettings(body);
      form = toForm(saved);
      if (before && (before.lan_mode !== saved.ui.lan_mode || before.port !== saved.ui.port)) {
        const lan = saved.ui.lan_mode;
        // location.hostname keeps IPv6 brackets. Turning LAN mode off means localhost only.
        const url = lan ? `https://${location.hostname}:${saved.ui.port}/` : `http://localhost:${saved.ui.port}/`;
        moved = { url, lan, port: saved.ui.port };
        auth.movedTo = url;
      }
      savedAt = Date.now();
      setTimeout(() => (savedAt = 0), 2500);
      if (!auth.movedTo) {
        loadRejected();
        loadSession();
        // A save that breaks a listener (a port that's taken) offers "Undo last save".
        if (prevSettings) noteSave(prevSettings as Settings, failingBefore);
        else refreshStatus();
      }
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
  const isWindows = $derived(live.status?.os.startsWith('windows') ?? false);
  const firewallCmd = (port: number) =>
    `netsh advfirewall firewall add rule name="Woodpile UI" dir=in action=allow protocol=TCP localport=${port}`;
  const passwordSet = $derived(!!auth.session?.password_set);
  const isMac = $derived(live.status?.os.startsWith('darwin') ?? false);
  const REASON: Record<string, string> = { allowlist: 'not on the allowlist', per_source: 'too many TCP connections' };
  const reasonText = { age: 'older than the retention period', cap: 'disk cap reached', low_disk: 'low disk space' };
</script>

{#snippet firewall(port: number)}
  <div class="mt-3">
    <p class="m-0 text-[13.5px]">
      <b>Windows firewall:</b> allow the UI port so other machines can connect. Run this in a Command Prompt or PowerShell opened with
      <b>Run as administrator</b>:
    </p>
    <div class="log-panel mt-2 flex items-start gap-3 rounded-2xl p-3">
      <code class="grow text-[13px] leading-relaxed break-all whitespace-pre-wrap">{firewallCmd(port)}</code>
      <CopyButton text={firewallCmd(port)} />
    </div>
  </div>
{/snippet}

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
        <button type="button" class="ghost-btn" onclick={revert} disabled={!dirty || busy} title="Throw away the edits you haven't saved">Discard changes</button>
        <button type="submit" class="pill-btn" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
      </div>
    </div>

    {#if undo.saved}
      <div class="undo" role="alert">
        <span>
          Your last save stopped a listener ({undo.saved.broke}): Woodpile can't receive logs there.
          {#if undo.error}<b>{undo.error}</b>{/if}
        </span>
        <button type="button" class="pill-btn" onclick={undoLastSave} disabled={undo.busy}>{undo.busy ? 'Undoing…' : 'Undo last save'}</button>
      </div>
    {/if}

    {#if formError}
      <div class="rounded-2xl border border-[var(--error-border)] bg-[var(--error-bg)] px-4 py-3 text-[14px]" role="alert">
        {formError}
        {#each otherErrors as [k, m] (k)}
          <div class="mt-1"><code class="font-mono text-[13px]">{k}</code>: {m}</div>
        {/each}
      </div>
    {/if}

    {#if moved}
      <div class="moved" role="status">
        <p class="m-0 font-semibold">Woodpile is restarting its web UI at a new address.</p>
        <p class="m-0 mt-1 text-[14px]">
          {moved.lan ? 'LAN mode is on: the UI is now served over HTTPS and needs the password.' : 'The UI now answers only on this machine.'}
          This page stops updating; open the new address to carry on.
        </p>
        <div class="mt-3 flex flex-wrap items-center gap-3">
          <code class="font-mono text-[14px] font-semibold break-all">{moved.url}</code>
          <a class="pill-btn no-underline" href={moved.url}>Go there</a>
          <button type="button" class="ghost-btn" onclick={() => (moved = null)}>Dismiss</button>
        </div>
        {#if moved.lan}
          <p class="m-0 mt-3 text-[13.5px]">
            The browser will warn that the certificate isn't trusted, because Woodpile made it itself. That's expected the first time: check the address is
            this machine, then continue.
          </p>
        {/if}
        {#if moved.lan && isWindows}
          {@render firewall(moved.port)}
        {/if}
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

      <fieldset class="limits" id="sender-limits">
        <legend class="text-[14px] font-semibold">Sender limits</legend>
        <p class="hint mt-1">
          These protect against floods of fake source addresses, which would otherwise each become a device. Senders that already have logs
          on disk are never rate-limited; refused messages count as dropped.
        </p>
        <div class="grid gap-4 sm:grid-cols-3">
          <label class="lbl">
            <span>Active devices</span>
            <input class="field" type="number" min="1" bind:value={form.maxDevices} aria-invalid={!!err('listeners.max_devices')} aria-describedby="err-listeners.max_devices h-max" />
            <span class="hint" id="h-max">Devices that sent in the last 10 minutes. Default 10,000.</span>
            {@render fieldError('listeners.max_devices')}
          </label>
          <label class="lbl">
            <span>New senders at once</span>
            <input class="field" type="number" min="1" bind:value={form.burst} aria-invalid={!!err('listeners.new_device_burst')} aria-describedby="err-listeners.new_device_burst h-burst" />
            <span class="hint" id="h-burst">A burst of new addresses accepted before the rate applies. Default 5,000.</span>
            {@render fieldError('listeners.new_device_burst')}
          </label>
          <label class="lbl">
            <span>New senders per minute after that</span>
            <input class="field" type="number" min="1" bind:value={form.perMinute} aria-invalid={!!err('listeners.new_devices_per_minute')} aria-describedby="err-listeners.new_devices_per_minute h-rate" />
            <span class="hint" id="h-rate">The steady rate for brand-new addresses. Default 200.</span>
            {@render fieldError('listeners.new_devices_per_minute')}
          </label>
        </div>
      </fieldset>

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
                <th scope="col">Why</th>
                <th scope="col"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {#each rejected as r (r.ip)}
                <tr>
                  <td class="font-mono">{r.ip}</td>
                  <td class="text-right tabular-nums">{num(r.count_24h)}</td>
                  <td title={dateTime(r.last_seen)}>{ago(r.last_seen, live.now)}</td>
                  <td>{REASON[r.reason] ?? r.reason}</td>
                  <td class="text-right">
                    <!-- Allowing only helps allowlist refusals; a per-source refusal is a connection flood. -->
                    {#if r.reason === 'allowlist'}
                      <button type="button" class="ghost-btn whitespace-nowrap" onclick={() => allow(r.ip)} disabled={inDraft(r.ip)} aria-label="Allow {r.ip}">
                        {inDraft(r.ip) ? 'Added, save to apply' : 'Allow'}
                      </button>
                    {/if}
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
        <input
          type="checkbox"
          bind:checked={form.lanMode}
          disabled={!passwordSet && !form.lanMode}
          aria-invalid={!!err('ui.lan_mode')}
          aria-describedby="hint-lan err-ui.lan_mode"
          class="mt-1"
        />
        <span>
          <span class="font-semibold">LAN mode</span>
          <span class="hint block" id="hint-lan">
            {#if saved?.ui.lan_mode}
              On: other machines can open this UI over HTTPS, with the password.
            {:else}
              Off: the UI is only reachable from this machine, on localhost:{saved?.ui.port}.
              {#if !passwordSet}Set a password below to turn it on.{/if}
            {/if}
          </span>
        </span>
      </label>
      {@render fieldError('ui.lan_mode')}
      {#if form.lanMode && !saved?.ui.lan_mode}
        <div class="lanwarn" role="note">
          <b>Before you turn this on:</b> anyone on your network who can reach this machine will see the Woodpile login page, and with the password,
          all of your logs. Use a strong password. Browsers will warn about the certificate the first time, because Woodpile creates its own
          (self-signed) certificate; that's expected, not an attack, as long as the address is this machine.
          {#if isWindows}Windows also needs a firewall rule for the UI port; the command is shown after you save.{/if}
        </div>
      {/if}
      {#if saved?.ui.lan_mode && isWindows}
        {@render firewall(saved.ui.port)}
      {/if}
      {#if saved?.ui.lan_mode && live.status?.ui_cert_fingerprint}
        <div class="fp mt-3">
          <p class="m-0 text-[13.5px]">
            <b>Certificate fingerprint (SHA-256).</b> When the browser warns about the certificate, open its details and check the SHA-256
            fingerprint matches this one. If it doesn't, you aren't talking to this Woodpile.
          </p>
          <div class="mt-2 flex flex-wrap items-center gap-2">
            <code class="font-mono text-[12.5px] break-all">{live.status.ui_cert_fingerprint}</code>
            <CopyButton text={live.status.ui_cert_fingerprint} />
          </div>
        </div>
      {/if}

      <label class="mt-4 flex items-start gap-3">
        <input
          type="checkbox"
          bind:checked={form.requireLocal}
          disabled={!passwordSet && !form.requireLocal}
          aria-invalid={!!err('ui.require_login_local')}
          aria-describedby="hint-local err-ui.require_login_local"
          class="mt-1"
        />
        <span>
          <span class="font-semibold">Require login on this machine too</span>
          <span class="hint block" id="hint-local">
            For shared or multi-user machines: anyone logged in to this computer can otherwise open the UI on localhost without a password.
            {#if !passwordSet}Set a password below first.{/if}
          </span>
        </span>
      </label>
      {@render fieldError('ui.require_login_local')}

      <label class="lbl mt-3 w-40">
        <span>UI port</span>
        <input class="field" type="number" bind:value={form.uiPort} aria-invalid={!!err('ui.port')} aria-describedby="err-ui.port" />
        {@render fieldError('ui.port')}
      </label>

      <details class="adv mt-4" open={!!(form.certFile || form.keyFile || err('ui.cert_file') || err('ui.key_file'))}>
        <summary class="cursor-pointer text-[14px] font-semibold">Advanced: use your own certificate</summary>
        <p class="hint mt-2">
          For LAN mode. Leave both empty to use the certificate Woodpile generates. Paths are on the machine running Woodpile (PEM files).
        </p>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="lbl">
            <span>Certificate file</span>
            <input class="field font-mono" bind:value={form.certFile} placeholder="/path/to/cert.pem" spellcheck="false" aria-invalid={!!err('ui.cert_file')} aria-describedby="err-ui.cert_file" />
            {@render fieldError('ui.cert_file')}
          </label>
          <label class="lbl">
            <span>Private key file</span>
            <input class="field font-mono" bind:value={form.keyFile} placeholder="/path/to/key.pem" spellcheck="false" aria-invalid={!!err('ui.key_file')} aria-describedby="err-ui.key_file" />
            {@render fieldError('ui.key_file')}
          </label>
        </div>
      </details>

      <PasswordForm onchange={load} />
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
  .undo {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 12px 16px;
    border-radius: 18px;
    border: 1px solid var(--error-border);
    background: var(--error-bg);
    font-size: 14px;
  }
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
  .moved {
    padding: 16px 18px;
    border-radius: 22px;
    border: 1px solid var(--info-border);
    background: var(--info-bg);
  }
  .limits {
    /* Land below the sticky save bar when the device_limit banner links here. */
    scroll-margin-top: 90px;
    border: 0;
    border-top: 1px solid var(--divider);
    margin: 18px 0 0;
    padding: 14px 0 0;
  }
  .fp {
    padding: 10px 14px;
    border-radius: 14px;
    border: 1px solid var(--info-border);
    background: var(--info-bg);
  }
  .lanwarn {
    margin-top: 10px;
    padding: 10px 14px;
    border-radius: 14px;
    border: 1px solid var(--warn-border);
    background: var(--warn-bg);
    font-size: 13.5px;
    line-height: 1.5;
  }
  .adv summary::marker {
    color: var(--muted);
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
