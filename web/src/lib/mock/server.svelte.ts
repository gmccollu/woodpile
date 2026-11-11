// In-browser fake of the Woodpile server for `npm run dev:mock`. It answers every
// route in docs/api.md with realistic data and simulates the live tail over a
// fake EventSource. Only main.ts imports it, and only when VITE_MOCK is set, so
// it isn't in the production bundle.
//
// URL parameters (before the #), e.g. http://127.0.0.1:5173/?mock_rate=1284#/dashboard
//   mock_rate=N       simulated msgs/s across all devices (default 40; above 50 the live tail is sampled)
//   mock_clash=1      TCP 514 is held by another process (red banner, Retry clears it)
//   mock_paused=1     writes paused by the low-disk guard (red banner)
//   mock_firstrun=1   no messages yet: opens on the welcome page; the test message arrives after mock_firstmsg s
//   mock_firstmsg=N   seconds until the first message in first-run mode (default 12; 0 = only via the panel)
//   mock_os=linux     server OS (darwin, linux, windows); changes kernel-drop visibility and the buffer size
//   mock_cycle=0      stop branch-ap-08 cycling online -> quiet -> offline -> online every 30 s
//   mock_latency=N    simulated response time in ms (default 120)
//   mock_panel=0      hide the mock control panel (for screenshots)

import type {
  Device,
  EventStream,
  ExportInfo,
  LiveFilters,
  Settings,
  Stats,
  Status,
  StorageHistoryEntry,
  Transport,
} from '../api';
import { isKnown } from '../severity';
import { FLEET, line, testLine, weighted, type GenLine, type MockDeviceDef } from './gen';

const P = new URLSearchParams(location.search);
const n = (k: string, d: number) => {
  const v = Number(P.get(k));
  return P.has(k) && Number.isFinite(v) ? v : d;
};
const osParam = P.get('mock_os') ?? 'darwin';
const OS = osParam.includes('/') ? osParam : `${osParam}/${osParam === 'windows' ? 'amd64' : 'arm64'}`;

export const mock = $state({
  rate: n('mock_rate', 40),
  clash: P.get('mock_clash') === '1',
  paused: P.get('mock_paused') === '1',
  firstRun: P.get('mock_firstrun') === '1',
  cycle: P.get('mock_cycle') !== '0',
  latency: n('mock_latency', 120),
  panel: P.get('mock_panel') !== '0',
});

const GiB = 1024 ** 3;
const MiB = 1024 ** 2;
const START = Date.now();
const MIN = 60_000;

// ---------- Fleet state ----------

interface Dev {
  def: MockDeviceDef;
  name: string;
  group: string;
  hidden: boolean;
  firstSeen: number;
  lastHeard: number;
  total: number;
  bytes: number;
  sending: boolean;
}

const devs = new Map<string, Dev>();

function addFleet(now: number) {
  for (const def of FLEET) {
    if (devs.has(def.ip)) continue;
    const silentFor = def.name === 'branch-fw-07' ? 6 * MIN + 12_000 : def.name === 'dmz-fw-01' ? 2 * 60 * MIN : 0;
    devs.set(def.ip, {
      def,
      name: def.name,
      group: def.group,
      hidden: !!def.hidden,
      firstSeen: now - (30 + Math.random() * 10) * 24 * 60 * MIN,
      lastHeard: now - silentFor,
      total: Math.round(def.weight * 3e5 + Math.random() * 5e4),
      bytes: Math.round(def.weight * 4.2e7 + Math.random() * 1e6),
      sending: def.weight > 0,
    });
  }
}

const thisHost: MockDeviceDef = { ip: '127.0.0.1', name: '', group: '', kind: 'srv', weight: 0.3 };

if (!mock.firstRun) addFleet(Date.now());

// ---------- Settings ----------

let settings: Settings = {
  listeners: {
    udp: { enabled: true, addr: '0.0.0.0', port: 514 },
    tcp: { enabled: true, addr: '0.0.0.0', port: 514 },
    allowlist: [],
  },
  storage: {
    data_dir: OS.startsWith('windows')
      ? 'C:\\ProgramData\\Woodpile'
      : OS.startsWith('linux')
        ? '/var/lib/woodpile'
        : '/Library/Application Support/Woodpile',
    retention_days: 30,
    retention_bytes: 20 * GiB,
    part_bytes: 100 * MiB,
  },
  ui: { lan_mode: false, port: 8514 },
  quiet: { quiet_after_seconds: 300, offline_after_seconds: 3600 },
};

const isIPv4 = (s: string) =>
  /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.test(s) && s.split('.').every((p) => Number(p) <= 255);
const isIPv6 = (s: string) => s.includes(':') && /^[0-9a-f:.]+$/i.test(s) && (s.match(/::/g)?.length ?? 0) <= 1;
const isIP = (s: string) => isIPv4(s) || isIPv6(s);
const WILDCARD = ['0.0.0.0', '::'];

function validate(s: Settings): Record<string, string> {
  const e: Record<string, string> = {};
  const port = (k: string, v: number, lo = 1) => {
    if (!Number.isInteger(v) || v < lo || v > 65535) e[k] = `Enter a port from ${lo} to 65535.`;
  };
  for (const proto of ['udp', 'tcp'] as const) {
    const l = s.listeners[proto];
    port(`listeners.${proto}.port`, l.port);
    if (!isIP(l.addr)) e[`listeners.${proto}.addr`] = 'Enter an IP address, or 0.0.0.0 / :: for all addresses.';
    else if (OS.startsWith('darwin') && !WILDCARD.includes(l.addr) && l.port < 1024)
      e[`listeners.${proto}.addr`] =
        'On macOS a port below 1024 can only be bound on all addresses (0.0.0.0 or ::). Use the sender allowlist to restrict senders.';
  }
  if (!s.listeners.udp.enabled && !s.listeners.tcp.enabled)
    e['listeners.udp.enabled'] = 'Keep at least one listener on, or Woodpile receives nothing.';
  const bad = s.listeners.allowlist.filter((c) => {
    const [ip, bits, extra] = c.split('/');
    if (extra !== undefined || !isIP(ip)) return true;
    if (bits === undefined) return false;
    const b = Number(bits);
    return !Number.isInteger(b) || b < 0 || b > (isIPv4(ip) ? 32 : 128);
  });
  if (bad.length) e['listeners.allowlist'] = `Not a valid CIDR: ${bad.join(', ')}`;
  const st = s.storage;
  if (!Number.isInteger(st.retention_days) || st.retention_days < 1 || st.retention_days > 3650)
    e['storage.retention_days'] = 'Keep logs for 1 to 3650 days.';
  if (!Number.isFinite(st.retention_bytes) || st.retention_bytes < GiB)
    e['storage.retention_bytes'] = 'The disk cap must be at least 1 GiB.';
  if (!Number.isFinite(st.part_bytes) || st.part_bytes < MiB || st.part_bytes > 4 * GiB)
    e['storage.part_bytes'] = 'Part size must be between 1 MiB and 4 GiB.';
  else if (st.part_bytes > st.retention_bytes) e['storage.part_bytes'] = 'Part size must be smaller than the disk cap.';
  if (st.data_dir !== settings.storage.data_dir) e['storage.data_dir'] = 'The data folder can only be changed in the config file.';
  port('ui.port', s.ui.port, 1024);
  if (s.ui.lan_mode) e['ui.lan_mode'] = 'LAN mode needs a password and HTTPS first; it is not available yet.';
  const q = s.quiet;
  if (!Number.isInteger(q.quiet_after_seconds) || q.quiet_after_seconds < 30)
    e['quiet.quiet_after_seconds'] = 'Use at least 30 seconds.';
  if (!Number.isInteger(q.offline_after_seconds) || q.offline_after_seconds <= q.quiet_after_seconds)
    e['quiet.offline_after_seconds'] = 'Offline must come after quiet.';
  return e;
}

// ---------- Traffic ----------

const RING_MAX = 5000;
const ring: GenLine[] = [];
let pendingFraction = 0;
let generatedThisSecond = 0;
let trueThisSecond = 0;
const spark: number[] = Array.from({ length: 60 }, (_, i) => Math.max(0, mock.rate * (0.8 + 0.25 * Math.sin(i / 6)) + (Math.random() - 0.5) * mock.rate * 0.15));
let sparkMinute = Math.floor(Date.now() / MIN);
let droppedByGuard = 0;
let firstMessageAt = 0;
const subs = new Set<MockEventSource>();

function status(d: Dev, now: number): Device['status'] {
  const age = (now - d.lastHeard) / 1000;
  if (age <= settings.quiet.quiet_after_seconds) return 'online';
  if (age <= settings.quiet.offline_after_seconds) return 'quiet';
  return 'offline';
}

function cycleDevice(now: number) {
  const ap = [...devs.values()].find((d) => d.def.name === 'branch-ap-08');
  if (!ap) return;
  if (!mock.cycle) {
    ap.sending = true;
    return;
  }
  // 30 s each: sending -> quiet -> offline -> sending. The mock warps its last-heard
  // time so the dot changes without waiting for the real thresholds.
  const phase = Math.floor((now - START) / 30_000) % 4;
  if (phase === 0 || phase === 3) {
    ap.sending = true;
  } else if (ap.sending || (phase === 2 && status(ap, now) === 'quiet')) {
    ap.sending = false;
    const q = settings.quiet;
    ap.lastHeard = now - (phase === 1 ? q.quiet_after_seconds + 60 : q.offline_after_seconds + 600) * 1000;
  }
}

function emit(l: GenLine) {
  ring.push(l);
  if (ring.length > RING_MAX) ring.splice(0, ring.length - RING_MAX);
  for (const s of subs) s.offer(l);
}

function tick() {
  const now = Date.now();
  cycleDevice(now);

  if (mock.firstRun) return;

  const senders = [...devs.values()].filter((d) => d.sending);
  if (!senders.length) return;
  const want = (mock.rate / 10) + pendingFraction;
  const whole = Math.floor(want);
  pendingFraction = want - whole;
  // Above 200 msg/s only a slice is generated as real lines; the counters use the full rate.
  const make = Math.min(whole, 20);
  trueThisSecond += whole;
  for (let i = 0; i < whole; i++) {
    const d = weighted(senders, (x) => x.def.weight);
    d.lastHeard = now;
    d.total++;
    d.bytes += 140;
    if (i < make) {
      generatedThisSecond++;
      const l = line(d.def, new Date(now - 100 + Math.floor((i / make) * 100)));
      if (!mock.paused) emit(l);
    }
  }
  if (mock.paused) droppedByGuard += whole;
}

function secondTick() {
  for (const s of subs) s.flushSecond(generatedThisSecond ? trueThisSecond / generatedThisSecond : 1);
  generatedThisSecond = 0;
  trueThisSecond = 0;
  const minute = Math.floor(Date.now() / MIN);
  if (minute !== sparkMinute) {
    sparkMinute = minute;
    spark.shift();
    spark.push(mock.firstRun ? 0 : mock.rate * (0.9 + Math.random() * 0.2));
  } else if (!mock.firstRun) {
    spark[59] = spark[59] * 0.8 + mock.rate * (0.9 + Math.random() * 0.2) * 0.2;
  }
}

let firstTimer = 0;
export function sendFirstMessage() {
  if (!mock.firstRun) return;
  clearTimeout(firstTimer);
  const now = Date.now();
  firstMessageAt = now;
  devs.set(thisHost.ip, {
    def: thisHost,
    name: '',
    group: '',
    hidden: false,
    firstSeen: now,
    lastHeard: now,
    total: 1,
    bytes: 90,
    sending: true,
  });
  mock.firstRun = false;
  emit(testLine(new Date(now)));
  // The rest of the fleet "joins" a few seconds later so the dashboard has something to show.
  setTimeout(() => addFleet(Date.now()), 5000);
}

export function restartFirstRun() {
  devs.clear();
  ring.length = 0;
  firstMessageAt = 0;
  mock.firstRun = true;
  for (const i in spark) spark[i] = 0;
  const delay = n('mock_firstmsg', 12);
  if (delay > 0) firstTimer = window.setTimeout(sendFirstMessage, delay * 1000);
}

if (mock.firstRun) {
  for (const i in spark) spark[i] = 0;
  const delay = n('mock_firstmsg', 12);
  if (delay > 0) firstTimer = window.setTimeout(sendFirstMessage, delay * 1000);
} else {
  // Seed the ring so the live tail's backlog isn't empty on first load.
  const senders = [...devs.values()].filter((d) => d.sending);
  const now = Date.now();
  for (let i = 300; i > 0; i--) {
    const d = weighted(senders, (x) => x.def.weight);
    ring.push(line(d.def, new Date(now - i * 180)));
  }
}

setInterval(tick, 100);
setInterval(secondTick, 1000);

// ---------- Live tail ----------

function matches(l: GenLine, f: LiveFilters): boolean {
  if (f.devices?.length && !f.devices.includes(l.device_id)) return false;
  if (f.group) {
    const d = devs.get(l.device_id);
    if ((d?.group ?? '') !== f.group) return false;
  }
  if (f.min_severity !== undefined && isKnown(l.severity) && l.severity > f.min_severity) return false;
  if (f.q && !l.raw.toLowerCase().includes(f.q.toLowerCase())) return false;
  return true;
}

const SAMPLE_LIMIT = 50;

class MockEventSource implements EventStream {
  onerror: ((e: Event) => void) | null = null;
  onopen: ((e: Event) => void) | null = null;
  private listeners: Record<string, ((e: MessageEvent<string>) => void)[]> = {};
  private filters: LiveFilters;
  private sentThisSecond = 0;
  private matchedThisSecond = 0;
  private closed = false;

  constructor(path: string) {
    const u = new URL(path, location.origin);
    const sev = u.searchParams.get('min_severity');
    this.filters = {
      devices: u.searchParams.get('devices')?.split(',').filter(Boolean),
      group: u.searchParams.get('group') ?? undefined,
      min_severity: sev === null ? undefined : Number(sev),
      q: u.searchParams.get('q') ?? undefined,
    };
    setTimeout(() => {
      if (this.closed) return;
      this.onopen?.(new Event('open'));
      for (const l of ring.filter((x) => matches(x, this.filters)).slice(-200)) this.send('message', l);
      subs.add(this);
    }, mock.latency);
  }

  addEventListener(type: 'message' | 'sampled', fn: (e: MessageEvent<string>) => void) {
    (this.listeners[type] ??= []).push(fn);
  }

  private send(type: string, data: unknown) {
    const ev = new MessageEvent<string>(type, { data: JSON.stringify(data) });
    for (const fn of this.listeners[type] ?? []) fn(ev);
  }

  offer(l: GenLine) {
    if (!matches(l, this.filters)) return;
    this.matchedThisSecond++;
    // Like the server: above ~50 lines/s send a sample.
    if (mock.rate > SAMPLE_LIMIT && this.sentThisSecond >= SAMPLE_LIMIT) return;
    if (mock.rate > SAMPLE_LIMIT && Math.random() > SAMPLE_LIMIT / Math.min(mock.rate, 200)) return;
    this.sentThisSecond++;
    this.send('message', l);
  }

  flushSecond(scale: number) {
    const rate = this.matchedThisSecond * scale;
    if (rate > SAMPLE_LIMIT) this.send('sampled', { rate: Math.round(rate * 10) / 10, shown: this.sentThisSecond });
    this.sentThisSecond = 0;
    this.matchedThisSecond = 0;
  }

  close() {
    this.closed = true;
    subs.delete(this);
  }
}

// ---------- Routes ----------

function deviceJSON(d: Dev, now: number): Device {
  const running = d.sending && !mock.firstRun;
  const share = d.def.weight / ([...devs.values()].filter((x) => x.sending).reduce((s, x) => s + x.def.weight, 0) || 1);
  return {
    id: d.def.ip,
    ip: d.def.ip,
    name: d.name,
    group: d.group,
    hidden: d.hidden,
    first_seen: new Date(d.firstSeen).toISOString(),
    last_heard: new Date(d.lastHeard).toISOString(),
    status: status(d, now),
    msgs_per_min: running ? Math.round(share * mock.rate * 60 * (0.9 + Math.random() * 0.2)) : 0,
    total_msgs: d.total,
    bytes_on_disk: d.bytes,
  };
}

function statusJSON(): Status {
  const l = settings.listeners;
  const listeners: Status['listeners'] = [];
  if (l.udp.enabled) listeners.push({ proto: 'udp', addr: l.udp.addr, port: l.udp.port, ok: true, error: '' });
  if (l.tcp.enabled) {
    const clash = mock.clash && l.tcp.port === 514;
    listeners.push({
      proto: 'tcp',
      addr: l.tcp.addr,
      port: l.tcp.port,
      ok: !clash,
      error: clash ? 'bind: address already in use' : '',
      ...(clash ? { holder: 'syslogd (pid 312)' } : {}),
    });
  }
  const banners: Status['banners'] = [];
  if (listeners.some((x) => !x.ok))
    banners.push({
      level: 'error',
      text: 'TCP 514 is in use by syslogd (pid 312). Woodpile is still receiving on UDP 514. Stop the other syslog service, or move the TCP listener to another port in Settings, then press Retry.',
    });
  if (mock.paused)
    banners.push({
      level: 'error',
      text: `Writes are paused: the data volume has 0.8 GiB free and there are no old logs left to delete. ${droppedByGuard.toLocaleString()} messages dropped so far. Free some disk space and writing resumes on its own.`,
    });
  return {
    version: '0.1.0',
    build: 'abc1234',
    os: OS,
    data_dir: settings.storage.data_dir,
    started_at: new Date(START - 3 * 3600_000 - 17 * MIN).toISOString(),
    udp_rcvbuf_bytes: OS.startsWith('darwin') ? 7_454_720 : 33_554_432,
    kernel_drops_visible: OS.startsWith('linux'),
    listeners,
    host_ips: ['10.1.0.50', '192.168.64.1', 'fd00::50'],
    banners,
  };
}

function statsJSON(now: number): Stats {
  const visible = [...devs.values()].filter((d) => !d.hidden);
  const st = visible.map((d) => status(d, now));
  const used = [...devs.values()].reduce((s, d) => s + d.bytes, 0);
  return {
    msgs_per_sec: mock.firstRun ? 0 : Math.round(mock.rate * (0.92 + Math.random() * 0.16) * 10) / 10,
    sparkline: spark.map((x) => Math.round(x * 10) / 10),
    devices: {
      total: visible.length,
      online: st.filter((s) => s === 'online').length,
      quiet: st.filter((s) => s === 'quiet').length,
      offline: st.filter((s) => s === 'offline').length,
    },
    disk: {
      used_bytes: used,
      cap_bytes: settings.storage.retention_bytes,
      free_bytes: mock.paused ? 0.8 * GiB : 400e9,
      days_kept: devs.size > 1 ? settings.storage.retention_days : firstMessageAt ? 1 : 0,
    },
    dropped_24h: droppedByGuard,
    kernel_dropped_24h: OS.startsWith('linux') ? 12 : null,
    truncated_24h: devs.size > 1 ? 3 : 0,
  };
}

const exportsList: ExportInfo[] = [
  {
    id: 'exp_7Hq2',
    created: new Date(Date.now() - 26 * 60 * MIN).toISOString(),
    devices: ['10.1.0.1', '10.1.0.2'],
    from: new Date(Date.now() - 50 * 60 * MIN).toISOString(),
    to: new Date(Date.now() - 26 * 60 * MIN).toISOString(),
    state: 'ready',
    size_bytes: 48 * MiB,
    error: '',
  },
];

function history(): StorageHistoryEntry[] {
  const out: StorageHistoryEntry[] = [];
  const day = 24 * 60 * MIN;
  const ips = ['10.1.0.1', '10.1.0.2', '10.1.0.10', '10.8.0.1'];
  for (let i = 0; i < 8; i++) {
    const t = new Date(Date.now() - i * day - 3 * 60 * MIN);
    const old = new Date(t.getTime() - 31 * day).toISOString().slice(0, 10);
    const ip = ips[i % ips.length];
    out.push({
      time: t.toISOString(),
      file: `logs/${ip}/${old}.log.gz`,
      device_id: ip,
      bytes: Math.round((8 + Math.random() * 30) * MiB),
      reason: i === 5 ? 'cap' : 'age',
    });
  }
  return out;
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const err = (status: number, error: string) => json({ error }, status);

async function route(method: string, url: URL, body: unknown): Promise<Response> {
  const p = url.pathname;
  const now = Date.now();
  if (method === 'GET' && p === '/api/status') return json(statusJSON());
  if (method === 'POST' && p === '/api/listeners/retry') {
    await sleep(500);
    mock.clash = false; // pretend the other process was stopped
    return new Response(null, { status: 204 });
  }
  if (method === 'GET' && p === '/api/stats') return json(statsJSON(now));
  if (method === 'GET' && p === '/api/devices') return json([...devs.values()].map((d) => deviceJSON(d, now)));
  const dm = p.match(/^\/api\/devices\/([^/]+)$/);
  if (dm && method === 'PATCH') {
    const d = devs.get(decodeURIComponent(dm[1]));
    if (!d) return err(404, 'No such device.');
    const b = (body ?? {}) as Partial<Device>;
    if (b.name !== undefined && b.name.length > 64) return err(400, 'Names can be at most 64 characters.');
    if (b.group !== undefined && b.group.length > 32) return err(400, 'Groups can be at most 32 characters.');
    if (b.name !== undefined) d.name = b.name.trim();
    if (b.group !== undefined) d.group = b.group.trim();
    if (b.hidden !== undefined) d.hidden = b.hidden;
    return json(deviceJSON(d, now));
  }
  if (method === 'GET' && p === '/api/settings') return json(settings);
  if (method === 'PUT' && p === '/api/settings') {
    const s = body as Settings;
    const errors = validate(s);
    if (Object.keys(errors).length) return json({ errors }, 400);
    settings = structuredClone(s);
    return json(settings);
  }
  if (method === 'GET' && p === '/api/storage/history') return json(history());
  if (method === 'GET' && p === '/api/search') {
    const q = url.searchParams;
    let re: RegExp | null = null;
    if (q.get('regex') === 'true') {
      try {
        re = new RegExp(q.get('q') ?? '', 'i');
      } catch {
        return err(400, 'That regular expression is not valid.');
      }
    }
    const f: LiveFilters = {
      devices: q.get('devices')?.split(',').filter(Boolean),
      group: q.get('group') ?? undefined,
      min_severity: q.has('min_severity') ? Number(q.get('min_severity')) : undefined,
      q: re ? undefined : (q.get('q') ?? undefined),
    };
    const hits = ring
      .map((l, i) => ({ ...l, ref: `r${i}` }))
      .filter((l) => matches(l, f) && (!re || re.test(l.raw)));
    const start = Number(q.get('cursor') || 0);
    const limit = Number(q.get('limit') || 500);
    const page = hits.slice(start, start + limit);
    return json({
      results: page,
      next_cursor: start + limit < hits.length ? String(start + limit) : '',
      scanned_bytes: ring.length * 140,
      truncated: false,
    });
  }
  if (method === 'GET' && p === '/api/context') {
    const i = Number((url.searchParams.get('ref') ?? '').slice(1));
    if (!Number.isInteger(i) || !ring[i]) return err(404, 'That result is no longer available.');
    const before = Number(url.searchParams.get('before') ?? 50);
    const after = Number(url.searchParams.get('after') ?? 50);
    const lo = Math.max(0, i - before);
    const lines = ring
      .slice(lo, i + after + 1)
      .map((l, k) => ({ ...l, ref: `r${lo + k}` }))
      .filter((l) => l.device_id === ring[i].device_id);
    return json({ lines, hit_index: lines.findIndex((l) => l.ref === `r${i}`) });
  }
  if (method === 'GET' && p === '/api/exports') return json(exportsList);
  if (method === 'POST' && p === '/api/exports') {
    const b = body as { devices: string[]; from: string; to: string };
    if (!b?.devices?.length) return err(400, 'Pick at least one device.');
    const e: ExportInfo = { id: `exp_${Math.random().toString(36).slice(2, 6)}`, created: new Date().toISOString(), ...b, state: 'building', size_bytes: 0, error: '' };
    exportsList.unshift(e);
    setTimeout(() => Object.assign(e, { state: 'ready', size_bytes: 12 * MiB }), 4000);
    return json({ id: e.id });
  }
  const ex = p.match(/^\/api\/exports\/([^/]+)\/download$/);
  if (ex && method === 'GET')
    return new Response('mock export', { status: 200, headers: { 'Content-Type': 'application/zip' } });
  return err(404, `No route for ${method} ${p}.`);
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export const mockTransport: Transport = {
  async fetch(path, init) {
    const url = new URL(path, location.origin);
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
    await sleep(mock.latency * (0.5 + Math.random()));
    return route(method, url, body);
  },
  eventSource: (path) => new MockEventSource(path),
};
