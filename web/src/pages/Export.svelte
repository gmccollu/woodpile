<script lang="ts">
  import {
    ALL_SEVS,
    createExport,
    deleteExport,
    estimateExport,
    ApiError,
    downloadsViaTransport,
    exportDownloadUrl,
    exportTicket,
    listExports,
    transportDownload,
    type ExportEstimate,
    type ExportInfo,
    type ExportRequest,
    type SevKey,
  } from '../lib/api';
  import { ago, bytes, num, remaining, stamp } from '../lib/format';
  import { prefs } from '../lib/prefs.svelte';
  import { defaultRange, rangeFromQuery, resolveRange, type Range } from '../lib/range';
  import { router } from '../lib/router.svelte';
  import { scopeFromQuery, sevFromQuery, type Scope } from '../lib/scope';
  import { deviceName, groupSize, live, ungroupedCount } from '../lib/status.svelte';
  import { SEVERITIES } from '../lib/severity';
  import DevicePicker from '../components/DevicePicker.svelte';
  import Icon from '../components/Icon.svelte';
  import SeverityChips from '../components/SeverityChips.svelte';
  import TimeRange from '../components/TimeRange.svelte';

  const DAY = 86_400_000;

  // ----- the form; "Export these results" from Search fills it through the URL -----
  let scope = $state<Scope>({ mode: 'all', group: '', devices: [] });
  let range = $state<Range>(defaultRange());
  let sev = $state<SevKey[]>([...ALL_SEVS]);
  let q = $state('');
  let regex = $state(false);
  let cs = $state(false);
  let fromSearch = $state(false);

  $effect(() => {
    const s = router.query;
    if (!s) return;
    const p = new URLSearchParams(s);
    scope = scopeFromQuery(p);
    range = rangeFromQuery(p);
    sev = sevFromQuery(p);
    q = p.get('q') ?? '';
    regex = p.get('re') === '1';
    cs = p.get('cs') === '1';
    fromSearch = true;
  });

  // How many devices the scope covers (from /api/devices and /api/groups totals).
  const count = $derived(
    scope.mode === 'devices'
      ? scope.devices.length
      : scope.mode === 'all'
        ? live.deviceTotal
        : scope.mode === 'group'
          ? groupSize(scope.group)
          : ungroupedCount(),
  );
  const formError = $derived(
    count === 0 ? 'Pick at least one device.' : !sev.length ? 'Pick at least one severity, or All.' : '',
  );


  // Groups stay groups (the server resolves them when it builds); "all devices" is sent as the list.
  async function request(): Promise<ExportRequest> {
    const { from, to } = resolveRange(range);
    const who =
      scope.mode === 'group'
        ? { group: scope.group }
        : scope.mode === 'ungrouped'
          ? { ungrouped: true }
          : scope.mode === 'all'
            ? { all_devices: true }
            : { devices: scope.devices };
    return { ...who, from, to, severities: sev, q: q.trim(), regex, case: cs };
  }

  // ----- estimate (an upper bound from the index, before building) -----
  let est = $state<ExportEstimate | null>(null);
  let estError = $state('');
  let estBusy = $state(false);
  $effect(() => {
    const key = JSON.stringify({ scope, range, sev, q, regex, cs, e: formError });
    void key;
    if (formError) {
      est = null;
      return;
    }
    const ctrl = new AbortController();
    const t = setTimeout(async () => {
      estBusy = true;
      try {
        est = await estimateExport(await request(), ctrl.signal);
        estError = '';
      } catch (x) {
        if ((x as Error).name !== 'AbortError') {
          est = null;
          estError = (x as Error).message;
        }
      } finally {
        estBusy = false;
      }
    }, 350);
    return () => {
      clearTimeout(t);
      ctrl.abort();
    };
  });

  // ----- the list -----
  let list = $state<ExportInfo[] | null>(null);
  let listError = $state('');
  let building = $state(false);
  let buildError = $state('');
  let now = $state(Date.now());

  async function refresh() {
    try {
      list = await listExports();
      listError = '';
    } catch (x) {
      listError = (x as Error).message;
    }
  }

  // Poll fast while something is queued or building. kick() polls now and re-evaluates the
  // pace, so a new build shows its progress straight away instead of after a slow tick.
  let pollTimer = 0;
  let polling = true;
  async function poll() {
    clearTimeout(pollTimer);
    await refresh();
    now = Date.now();
    if (!polling) return;
    clearTimeout(pollTimer);
    const busy = list?.some((e) => e.state === 'queued' || e.state === 'building');
    pollTimer = window.setTimeout(poll, busy ? 1000 : 5000);
  }
  const kick = () => poll();

  $effect(() => {
    polling = true;
    poll();
    const clock = setInterval(() => (now = Date.now()), 30_000);
    return () => {
      polling = false;
      clearTimeout(pollTimer);
      clearInterval(clock);
    };
  });

  async function build() {
    if (formError || building) return;
    building = true;
    buildError = '';
    try {
      await createExport(await request());
      await kick();
    } catch (x) {
      buildError = (x as Error).message;
    } finally {
      building = false;
    }
  }

  async function remove(e: ExportInfo) {
    try {
      await deleteExport(e.id);
    } catch (x) {
      listError = (x as Error).message;
    }
    await kick();
  }

  // A plain link streams the zip; only mock mode (no server) needs to go through the transport.
  // Downloads always go through a fresh ticket (docs/api.md): a plain link can't carry the
  // session header, and the ticket URL keeps the browser's own download handling for big
  // files. A 401 shows the login (api.ts); a gone or unready export refreshes the list.
  let downloading = $state('');
  let downloadError = $state<Record<string, string>>({});
  async function download(ev: MouseEvent, e: ExportInfo) {
    ev.preventDefault();
    if (downloading) return;
    downloading = e.id;
    downloadError = { ...downloadError, [e.id]: '' };
    try {
      const { url } = await exportTicket(e.id);
      if (downloadsViaTransport()) await transportDownload(url);
      else {
        const a = document.createElement('a');
        a.href = url;
        a.download = '';
        document.body.appendChild(a);
        a.click();
        a.remove();
      }
    } catch (x) {
      const err = x as ApiError;
      if (err.status !== 401) {
        downloadError = {
          ...downloadError,
          [e.id]: err.status === 404 ? 'That export is gone. It may have expired.' : err.status === 409 ? "It isn't ready yet." : err.message,
        };
        kick();
      }
    } finally {
      downloading = '';
    }
  }

  // A group export may list no devices (the server resolves the group when it builds).
  const names = (e: ExportInfo) =>
    e.devices.length
      ? e.devices.slice(0, 3).map(deviceName).join(', ')
      : e.filters?.group
        ? `Group: ${e.filters.group}`
        : e.filters?.ungrouped
          ? 'Ungrouped devices'
          : 'No devices';
  const more = (devs: string[]) => (devs.length > 3 ? `+${devs.length - 3}` : '');
  const expiresIn = (e: ExportInfo) => (e.expires_at ? Date.parse(e.expires_at) - now : DAY);

  /** "Group: HQ · warn, err · “deny” (regex, case)": what the export was asked to include. */
  function filterText(e: ExportInfo): string {
    const f = e.filters;
    if (!f) return '';
    const bits: string[] = [];
    if (e.devices.length && f.group) bits.push(`Group: ${f.group}`);
    if (e.devices.length && f.ungrouped) bits.push('Ungrouped devices');
    if (f.severities?.length) bits.push(f.severities.map((k) => (k === 'unknown' ? 'unknown' : SEVERITIES[k].short)).join(', '));
    if (f.q) {
      const how = [f.regex ? 'regex' : '', f.case ? 'case' : ''].filter(Boolean).join(', ');
      bits.push(`“${f.q}”${how ? ` (${how})` : ''}`);
    }
    return bits.join(' · ');
  }
  const STATE: Record<string, string> = { queued: 'Queued', building: 'Building', ready: 'Ready', failed: 'Failed', expired: 'Expired' };
</script>

<div class="grid gap-4 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
  <div class="space-y-4">
    <section class="card space-y-4" aria-labelledby="ex-title">
      <div>
        <h1 id="ex-title" class="m-0 text-xl font-bold tracking-tight">Export logs</h1>
        <p class="mt-1 mb-0 text-[13.5px] text-muted">
          {fromSearch ? 'Filled in from your search. Check it, then build.' : 'Build a zip of the raw logs to send to someone else.'}
        </p>
      </div>

      <div class="row">
        <span class="lbl">Devices</span>
        <DevicePicker bind:value={scope} />
        {#if count !== null}<span class="text-[13px] text-muted">{num(count)} device{count === 1 ? '' : 's'}</span>{/if}
      </div>
      <div class="row top">
        <span class="lbl">Time range</span>
        <div class="min-w-0 grow"><TimeRange bind:value={range} /></div>
      </div>
      <div class="row">
        <span class="lbl">Severities</span>
        <div class="sevbar"><SeverityChips bind:value={sev} label="Export severities" /></div>
      </div>
      <div class="row">
        <label class="lbl" for="exq">Only lines matching</label>
        <input
          id="exq"
          class="field w-auto min-w-0 grow basis-40 font-mono"
          bind:value={q}
          placeholder={regex ? 'Regular expression (RE2)' : 'Optional text'}
          spellcheck="false"
        />
        <button type="button" class="toggle" aria-pressed={regex} onclick={() => (regex = !regex)} title="Regular expression (RE2 syntax)">.* Regex</button>
        <button type="button" class="toggle" aria-pressed={cs} onclick={() => (cs = !cs)} title="Match upper and lower case exactly">Aa Case</button>
      </div>

      <div class="estimate" aria-live="polite">
        {#if formError}
          <span class="text-bad-fg font-medium">{formError}</span>
        {:else if estError}
          <span class="text-bad-fg font-medium">{estError}</span>
        {:else if est}
          <span>Up to <b>{num(est.lines)}</b> lines from <b>{num(est.files)}</b> file{est.files === 1 ? '' : 's'}, about <b>{bytes(est.bytes)}</b> before compression.</span>
          {#if estBusy}<span class="text-muted"> Updating…</span>{/if}
        {:else}
          <span class="text-muted">Estimating…</span>
        {/if}
      </div>

      <div class="flex flex-wrap items-center gap-3">
        <button type="button" class="pill-btn px-5 py-2.5" onclick={build} disabled={!!formError || building}>
          <Icon name="export" size={15} />{building ? 'Starting…' : 'Build the zip'}
        </button>
        {#if buildError}<span class="text-[13.5px] font-medium text-bad-fg" role="alert">{buildError}</span>{/if}
        <span class="text-[13px] text-muted">One export builds at a time; others wait in the queue.</span>
      </div>
    </section>

    <section class="card">
      <h2 class="m-0 mb-2 text-[16px] font-bold">What's in the zip</h2>
      <ul class="m-0 list-disc space-y-1.5 pl-5 text-[14px] leading-relaxed">
        <li><b>The logs</b>, one plain-text file per device per day, exactly as received, so anyone can open them.</li>
        <li><b>A manifest</b> with the devices, range and filters, and a line count and SHA-256 checksum for every file, so the recipient can tell nothing was changed or lost.</li>
        <li><b>A gaps report</b>: times when Woodpile wasn't running or a listener was down. "No logs at 14:05" then means something definite.</li>
        <li><b>Woodpile's own events</b> for the range (restarts, rotations, deletions, drops), so "the device sent nothing" can be told apart from "Woodpile wasn't listening".</li>
      </ul>
    </section>
  </div>

  <section class="card self-start" aria-labelledby="ex-list">
    <h2 id="ex-list" class="m-0 mb-1 text-[16px] font-bold">Recent exports</h2>
    <p class="mt-0 mb-3 text-[13px] text-muted">Each zip is kept for 24 hours after it's ready.</p>
    {#if listError}<p class="text-[13.5px] text-bad-fg" role="alert">{listError}</p>{/if}
    {#if list === null}
      <p class="text-muted">Loading…</p>
    {:else if !list.length}
      <p class="text-muted">No exports yet.</p>
    {:else}
      <ul class="m-0 list-none space-y-2.5 p-0">
        {#each list as e (e.id)}
          <li class="ex" data-id={e.id} data-state={e.state}>
            <div class="flex items-start gap-3">
              <div class="min-w-0 grow">
                <div class="flex min-w-0 items-center gap-2">
                  <span class="shrink-0 badge {e.state}">{STATE[e.state] ?? e.state}</span>
                  <b class="min-w-0 truncate text-[14px]" title={e.devices.map(deviceName).join(', ')}>{names(e)}</b>
                  {#if more(e.devices)}<span class="shrink-0 text-[13px] text-muted">{more(e.devices)}</span>{/if}
                </div>
                {#if filterText(e)}<div class="mt-1 truncate font-mono text-[12px]" title={filterText(e)}>{filterText(e)}</div>{/if}
                <div class="mt-1 text-[12.5px] text-muted">
                  {stamp(e.from, prefs.utc).slice(0, 11)} → {stamp(e.to, prefs.utc).slice(0, 11)}{prefs.utc ? ' UTC' : ''} · created {ago(e.created, now)}
                </div>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                {#if e.state === 'ready'}
                  <a class="pill-btn no-underline" href={exportDownloadUrl(e.id)} download onclick={(ev) => download(ev, e)} aria-busy={downloading === e.id}>
                    <Icon name="download" size={14} />{downloading === e.id ? 'Starting…' : 'Download'}
                  </a>
                {/if}
                <button type="button" class="ghost-btn" onclick={() => remove(e)}>
                  {e.state === 'queued' || e.state === 'building' ? 'Cancel' : 'Delete'}
                </button>
              </div>
            </div>
            {#if e.state === 'building'}
              <div class="bar mt-2" role="progressbar" aria-label="Export progress" aria-valuenow={Math.round(e.progress * 100)} aria-valuemin={0} aria-valuemax={100}>
                <i style:width="{Math.max(2, e.progress * 100)}%"></i>
              </div>
              <div class="mt-1 text-[12.5px] text-muted">{Math.round(e.progress * 100)} % · {num(e.lines)} lines so far</div>
            {:else if e.state === 'queued'}
              <div class="mt-1 text-[12.5px] text-muted">Waiting for the export ahead of it.</div>
            {:else if e.state === 'ready'}
              <div class="mt-1 text-[12.5px] text-muted">
                {num(e.lines)} lines · {bytes(e.size_bytes)} · expires in {remaining(expiresIn(e))}
              </div>
            {/if}
            {#if downloadError[e.id]}
              <div class="mt-1 text-[12.5px] font-medium text-bad-fg" role="alert">{downloadError[e.id]}</div>
            {/if}
            {#if e.state === 'failed'}
              <div class="mt-1 text-[12.5px] font-medium text-bad-fg">{e.error || 'The build failed.'}</div>
            {:else if e.state === 'expired'}
              <div class="mt-1 text-[12.5px] text-muted">Deleted after 24 hours. Build it again if you still need it.</div>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 14px;
  }
  .row.top {
    align-items: flex-start;
  }
  .row.top > .lbl {
    padding-top: 8px;
  }
  .lbl {
    width: 150px;
    font-size: 13.5px;
    font-weight: 600;
  }
  .sevbar {
    display: inline-flex;
    padding: 6px 10px;
    border-radius: 14px;
    background: var(--log-bar);
    border: 1px solid var(--log-border);
  }
  .toggle {
    padding: 7px 11px;
    border-radius: 12px;
    border: 1px solid var(--input-border);
    background: var(--input);
    color: var(--fg);
    font-size: 13px;
    font-family: var(--font-mono);
    cursor: pointer;
    white-space: nowrap;
  }
  .toggle[aria-pressed='true'] {
    background: var(--btn);
    color: var(--btn-fg);
    border-color: var(--btn);
  }
  .estimate {
    padding: 10px 14px;
    border-radius: 14px;
    background: var(--glass-strong);
    border: 1px solid var(--border);
    font-size: 14px;
  }
  .ex {
    padding: 12px 14px;
    border-radius: 16px;
    background: var(--glass-strong);
    border: 1px solid var(--border);
  }
  .badge {
    font-size: 11.5px;
    font-weight: 700;
    padding: 2px 8px;
    border-radius: 999px;
    border: 1px solid var(--divider);
    background: var(--chip);
  }
  .badge.ready {
    color: var(--ok-fg);
  }
  .badge.building,
  .badge.queued {
    color: var(--link);
  }
  .badge.failed {
    color: var(--bad-fg);
  }
  .badge.expired {
    color: var(--muted);
  }
  .bar {
    height: 8px;
    border-radius: 99px;
    background: var(--divider);
    overflow: hidden;
  }
  .bar i {
    display: block;
    height: 100%;
    border-radius: 99px;
    background: var(--spark);
    transition: width 0.25s;
  }
</style>
