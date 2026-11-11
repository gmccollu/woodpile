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
//   mock_releases=URL releases_url in /api/status (default empty: no public releases page yet)
//   mock_panel=0      hide the mock control panel (for screenshots)
//   mock_auth=1       LAN mode with auth: the password is "woodpile-demo" and the session starts logged out
//   mock_loggedin=1   with mock_auth: start logged in
//   mock_locked=1     with mock_auth: logins from "this address" are locked out for 15 minutes
//   mock_password=1   localhost mode, but a password is already set (so LAN mode can be turned on)
//   mock_slow_scan=1  search pages come back partial (budget used up) so the UI keeps fetching
//   mock_export_fail=1 exports fail at 40 %
//   mock_export_secs=N seconds an export takes to build (default 8)
//   mock_devices=N    add N generated devices (up to 10,000) to the fleet
//   mock_device_limit=1 new senders are being refused (device_limit banner)
//   mock_service_514=1 the installed Woodpile service holds UDP and TCP 514; this copy starts on 15514
//   mock_listen_514=1  with mock_service_514: start on 514 (so the clash shows straight away)
//   mock_ui_port_busy=1 the installed service can't open its UI because this copy holds the UI port

import type {
  Device,
  ExportInfo,
  RejectedSender,
  Settings,
  Stats,
  Status,
  StorageHistoryEntry,
  Transport,
} from '../api';
import { isKnown } from '../severity';
import { setSessionSecret } from '../api';
import { FLEET, line, testLine, weighted, type GenLine, type MockDeviceDef } from './gen';
import { STEP, deviceAt, lineAt, shareOf } from './history';

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
  /** LAN mode with auth (mirrors settings.ui.lan_mode). */
  lan: P.get('mock_auth') === '1',
  loggedIn: P.get('mock_loggedin') === '1',
  /** Search scans run out of budget quickly, so results arrive as partial pages. */
  slowScan: P.get('mock_slow_scan') === '1',
  /** The next export fails part-way. */
  exportFail: P.get('mock_export_fail') === '1',
  /** Seconds an export takes to build. */
  exportSecs: n('mock_export_secs', 8),
  /** New senders are being refused (the device_limit banner). Also on when the fleet is at max_devices. */
  deviceLimit: P.get('mock_device_limit') === '1',
  /** Generated devices added to the fleet. */
  fleetExtra: 0,
  /** Another Woodpile (the installed service) holds port 514 on UDP and TCP. */
  service514: P.get('mock_service_514') === '1',
});

export const MOCK_PASSWORD = 'woodpile-demo';

const authState = {
  password: P.get('mock_auth') === '1' || P.get('mock_password') === '1' ? MOCK_PASSWORD : '',
  /** The current session's secret; issued only by login. '' when there's no session. */
  secret: '',
  failures: [] as number[],
  lastAttempt: 0,
  lockedUntil: P.get('mock_locked') === '1' ? Date.now() + 15 * 60_000 : 0,
};

function newToken(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join('');
}

/** For the smoke checks: what the mock saw (header values and /api/session bodies). */
const probe = {
  livePaths: [] as string[],
  /** X-Woodpile-Session on the last request of each kind. */
  lastGetHeader: '' as string | null,
  lastWriteHeader: '' as string | null,
  lastLiveHeader: '' as string | null,
  sessionBodies: [] as string[],
  /** The body of the last POST /api/exports(/estimate). */
  lastExportBody: '',
  /** Downloads served, and the status of the last download attempt. */
  downloads: [] as { id: string; ticket: boolean; status: number }[],
  lastDownloadStatus: 0,
  lastTicketUrl: '',
  /** Appended to the device_limit banner text, so a smoke check can change a banner's content. */
  bannerNote: '',
  /** Fetches a URL like a browser navigation would (no session header); returns the status. */
  async navigateStatus(path: string): Promise<number> {
    return (await mockTransport.fetch(path, { headers: {} })).status;
  },
  get secret() {
    return authState.secret;
  },
  /** Simulates a request that has the cookie but not the header (e.g. another app on this host). */
  cookieOnly: false,
};
(window as unknown as { __woodpileMock: typeof probe }).__woodpileMock = probe;

// mock_loggedin=1 starts with a session, as if this browser had logged in earlier: the mock
// issues a secret and puts it where the UI keeps one.
if (P.get('mock_require_local') === '1') {
  // Localhost with require_login_local on (and so a password).
  if (!authState.password) authState.password = MOCK_PASSWORD;
}
if (mock.loggedIn && (mock.lan || P.get('mock_require_local') === '1')) {
  authState.secret = newToken();
  setSessionSecret(authState.secret);
}

/** Mock panel: switch LAN mode with auth on (logged out) or off. */
export function setLanAuth(on: boolean) {
  mock.lan = on;
  settings.ui.lan_mode = on;
  if (on && !authState.password) authState.password = MOCK_PASSWORD;
  mock.loggedIn = false;
  authState.secret = '';
}

/** Mock panel: end the session as if it expired; the next API call gets a 401. */
export function expireSession() {
  mock.loggedIn = false;
  authState.secret = '';
}

/** Login is required in LAN mode, and on localhost too when require_login_local is on. */
const authOn = () => mock.lan || (!!settings.ui.require_login_local && !!authState.password);

/** The mock's stand-in for "cookie and matching X-Woodpile-Session header". */
const hasSession = (header: string | null) => mock.loggedIn && !!authState.secret && header === authState.secret;

function sessionJSON(header: string | null = null) {
  const on = authOn();
  return {
    mode: mock.lan ? 'lan' : 'localhost',
    auth_required: on,
    authenticated: !on || hasSession(header),
    password_set: !!authState.password,
  };
}

const PUBLIC = ['/api/session', '/api/login', '/api/logout', '/healthz'];

function lockout(now: number): Response | null {
  if (authState.lockedUntil > now)
    return retryLater(Math.ceil((authState.lockedUntil - now) / 1000), 'Too many failed logins from this address.');
  return null;
}

function retryLater(secs: number, error: string): Response {
  return new Response(JSON.stringify({ error }), {
    status: 429,
    headers: { 'Content-Type': 'application/json', 'Retry-After': String(secs) },
  });
}

/** 10 failures in 15 min lock the address out for 15 min; after 5, one attempt a second. */
function recordFailure(now: number) {
  authState.failures = authState.failures.filter((t) => now - t < 15 * 60_000);
  authState.failures.push(now);
  if (authState.failures.length >= 10) authState.lockedUntil = now + 15 * 60_000;
}

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

/** joinedNow: the fleet starts sending now (after a first-run test message), so first_seen is now. */
function addFleet(now: number, joinedNow = false) {
  for (const def of FLEET) {
    if (devs.has(def.ip)) continue;
    const silentFor = def.name === 'branch-fw-07' ? 6 * MIN + 12_000 : def.name === 'dmz-fw-01' ? 2 * 60 * MIN : 0;
    devs.set(def.ip, {
      def,
      name: def.name,
      group: def.group,
      hidden: !!def.hidden,
      firstSeen: joinedNow ? now + Math.random() * 1000 : now - (30 + Math.random() * 10) * 24 * 60 * MIN,
      lastHeard: now - silentFor,
      total: Math.round(def.weight * 3e5 + Math.random() * 5e4),
      bytes: Math.round(def.weight * 4.2e7 + Math.random() * 1e6),
      sending: def.weight > 0,
    });
  }
}

const thisHost: MockDeviceDef = { ip: '127.0.0.1', name: '', group: '', kind: 'srv', weight: 0.3 };

if (!mock.firstRun) addFleet(Date.now());

const CERT_FP = 'SHA256:3F:9A:1C:77:0B:E4:52:D8:6A:91:C3:0E:F2:48:BB:17:65:2D:A0:C9:8E:11:7F:44:D6:5B:02:E9:A3:C8:70:1D';

// Generated devices for scale testing (mock_devices=N or the panel). Vendor-neutral names,
// private 10.100+ addresses, a few groups, a mix of busy, quiet and offline.
const BIG_GROUPS = ['Stores', 'Warehouses', 'Lab', ''];
const KINDS = ['sw', 'ap', 'srv', 'fw'] as const;
export function setFleetSize(extra: number) {
  extra = Math.max(0, Math.min(10_000, Math.round(extra)));
  for (const ip of [...devs.keys()]) if (ip.startsWith('10.1') && /^10\.1\d\d\./.test(ip) && Number(ip.split('.')[1]) >= 100) devs.delete(ip);
  const now = Date.now();
  for (let i = 0; i < extra; i++) {
    const ip = `10.${100 + Math.floor(i / 62500)}.${Math.floor(i / 250) % 250}.${(i % 250) + 1}`;
    const kind = KINDS[i % KINDS.length];
    const r = (Math.imul(i + 1, 2654435761) >>> 0) / 4294967296;
    const quietFor = r < 0.06 ? 20 * MIN : r < 0.1 ? 5 * 60 * MIN : 0;
    devs.set(ip, {
      def: { ip, name: i % 9 === 4 ? '' : `${kind === 'ap' ? 'ap' : kind === 'sw' ? 'sw' : kind === 'fw' ? 'fw' : 'srv'}-${String(i + 1).padStart(5, '0')}`, group: BIG_GROUPS[i % 4], kind, weight: quietFor ? 0 : 0.02 + r * 0.2 },
      name: i % 9 === 4 ? '' : `${kind}-${String(i + 1).padStart(5, '0')}`,
      group: BIG_GROUPS[i % 4],
      hidden: i % 97 === 0,
      firstSeen: now - (1 + r * 30) * 24 * 60 * MIN,
      lastHeard: now - quietFor,
      total: Math.round(r * 2e5),
      bytes: Math.round(r * 1.5e6),
      sending: !quietFor,
    });
  }
  pickerAt = 0;
  mock.fleetExtra = extra;
}

// Weighted random sender with a cumulative table and binary search (rebuilt each second).
let pickerAt = 0;
let pickTable: { cum: number[]; list: Dev[] } = { cum: [], list: [] };
function senderPicker(): (() => Dev) | null {
  const now = Date.now();
  if (now - pickerAt > 1000) {
    const list = [...devs.values()].filter((d) => d.sending);
    const cum: number[] = [];
    let t = 0;
    for (const d of list) cum.push((t += d.def.weight));
    pickTable = { cum, list };
    pickerAt = now;
  }
  const { cum, list } = pickTable;
  if (!list.length) return null;
  const total = cum[cum.length - 1];
  return () => {
    const x = Math.random() * total;
    let lo = 0;
    let hi = cum.length - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (cum[mid] < x) lo = mid + 1;
      else hi = mid;
    }
    return list[lo];
  };
}

/** Devices that sent in the last 10 minutes (what max_devices counts). */
function activeDevices(now: number): number {
  let n = 0;
  for (const d of devs.values()) if (now - d.lastHeard < 10 * MIN) n++;
  return n;
}

/** docs/api.md: limit/offset/sort/q/group/ungrouped/hidden → {devices, total}. */
function devicesRoute(u: URLSearchParams, now: number): Response {
  const limit = Math.min(1000, Math.max(1, Number(u.get('limit') || 200)));
  const offset = Math.max(0, Number(u.get('offset') || 0));
  const sort = u.get('sort') || 'rate';
  if (!['rate', 'name', 'last_heard', 'first_seen'].includes(sort)) return err(400, `invalid sort "${sort}"`);
  const hidden = u.get('hidden') || 'include';
  const q = (u.get('q') ?? '').toLowerCase();
  const group = u.get('group');
  const ungrouped = u.get('ungrouped') === 'true';
  let list = [...devs.values()].filter((d) => {
    if (hidden === 'exclude' && d.hidden) return false;
    if (hidden === 'only' && !d.hidden) return false;
    if (group && d.group !== group) return false;
    if (ungrouped && d.group) return false;
    if (q && !d.def.ip.toLowerCase().includes(q) && !d.name.toLowerCase().includes(q)) return false;
    return true;
  });
  const rateOf = (d: Dev) => (d.sending && !mock.firstRun ? d.def.weight : 0);
  const label = (d: Dev) => d.name || d.def.ip;
  // Directions: rate busiest first, name A–Z, last_heard and first_seen newest first.
  if (sort === 'rate') list.sort((a, b) => rateOf(b) - rateOf(a) || label(a).localeCompare(label(b), undefined, { numeric: true }));
  else if (sort === 'name') list.sort((a, b) => label(a).localeCompare(label(b), undefined, { numeric: true }));
  else if (sort === 'last_heard') list.sort((a, b) => b.lastHeard - a.lastHeard);
  else list.sort((a, b) => b.firstSeen - a.firstSeen);
  const total = list.length;
  list = list.slice(offset, offset + limit);
  return json({ devices: list.map((d) => deviceJSON(d, now)), total });
}

// ---------- Settings ----------

// A demo copy beside an installed service starts on 15514, as in the owner's test.
const SYSLOG_PORT = P.get('mock_service_514') === '1' && P.get('mock_listen_514') !== '1' ? 15514 : 514;

let settings: Settings = {
  listeners: {
    udp: { enabled: true, addr: '0.0.0.0', port: SYSLOG_PORT },
    tcp: { enabled: true, addr: '0.0.0.0', port: SYSLOG_PORT },
    // Non-empty so the rejected-senders list has something to show.
    allowlist: ['10.0.0.0/8', '192.0.2.44/32', 'fd00:1::/64'],
    max_devices: 10_000,
    new_device_burst: 5_000,
    new_devices_per_minute: 200,
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
  ui: { lan_mode: P.get('mock_auth') === '1', port: 8514, cert_file: '', key_file: '', require_login_local: P.get('mock_require_local') === '1' },
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
  s.listeners.allowlist.forEach((c, i) => {
    const [ip, bits, extra] = c.split('/');
    let bad = extra !== undefined || !isIP(ip);
    if (!bad && bits !== undefined) {
      const b = Number(bits);
      bad = bits === '' || !Number.isInteger(b) || b < 0 || b > (isIPv4(ip) ? 32 : 128);
    }
    if (bad) e[`listeners.allowlist[${i}]`] = 'Not an IP address or CIDR.';
  });
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
  if (s.ui.lan_mode && !authState.password) e['ui.lan_mode'] = 'Set a password before turning on LAN mode.';
  if (s.ui.require_login_local && !authState.password) e['ui.require_login_local'] = 'Set a password first.';
  const lim = (k: 'max_devices' | 'new_device_burst' | 'new_devices_per_minute', lo: number, hi: number) => {
    const v = s.listeners[k];
    if (!Number.isInteger(v) || v < lo || v > hi) e[`listeners.${k}`] = `Use a whole number from ${lo.toLocaleString()} to ${hi.toLocaleString()}.`;
  };
  lim('max_devices', 1, 1_000_000);
  lim('new_device_burst', 1, 1_000_000);
  lim('new_devices_per_minute', 1, 100_000);
  const cert = s.ui.cert_file ?? '';
  const key = s.ui.key_file ?? '';
  const abs = (f: string) => f.startsWith('/') || /^[A-Za-z]:\\/.test(f);
  if (cert && !abs(cert)) e['ui.cert_file'] = 'Use the full path to the certificate file.';
  if (key && !abs(key)) e['ui.key_file'] = 'Use the full path to the key file.';
  if (cert && !key && !e['ui.cert_file']) e['ui.key_file'] = 'Set the key file too, or clear both to use the generated certificate.';
  if (key && !cert && !e['ui.key_file']) e['ui.cert_file'] = 'Set the certificate file too, or clear both to use the generated certificate.';
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
// A fleet that's been running started when its oldest device was first seen.
let firstMessageAt = devs.size ? Math.min(...[...devs.values()].map((d) => d.firstSeen)) : 0;
const subs = new Set<LiveSub>();

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

  const pick = senderPicker();
  if (!pick) return;
  const want = (mock.rate / 10) + pendingFraction;
  const whole = Math.floor(want);
  pendingFraction = want - whole;
  // Above 200 msg/s only a slice is generated as real lines; the counters use the full rate.
  const make = Math.min(whole, 20);
  trueThisSecond += whole;
  for (let i = 0; i < whole; i++) {
    const d = pick();
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
  setTimeout(() => addFleet(Date.now(), true), 5000);
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

if (!mock.firstRun && n('mock_devices', 0) > 0) setFleetSize(n('mock_devices', 0));

setInterval(tick, 100);
setInterval(secondTick, 1000);

// ---------- Live tail ----------

/** Filters as the mock applies them: a resolved severity set (null = all). */
interface MockFilters {
  devices?: string[];
  group?: string;
  ungrouped?: boolean;
  sev: Set<string> | null;
  q?: string;
}

/**
 * docs/api.md → Severity filters: `severities=0,3,unknown` wins over `min_severity=N`
 * (0…N plus unknown). Empty or missing means all. Throws on an invalid value.
 */
function sevFilter(u: URLSearchParams): Set<string> | null {
  const list = u.get('severities');
  if (list) {
    const parts = list.split(',').map((x) => x.trim()).filter(Boolean);
    if (parts.includes('none')) {
      if (parts.length > 1) throw new Error('"none" can\'t be combined with other severities');
      return new Set(); // matches nothing
    }
    for (const x of parts) if (!/^[0-7]$/.test(x) && x !== 'unknown') throw new Error(`invalid severity "${x}"`);
    return parts.length ? new Set(parts) : null;
  }
  const min = u.get('min_severity');
  if (min !== null && min !== '') {
    if (!/^[0-7]$/.test(min)) throw new Error(`invalid min_severity "${min}"`);
    return new Set([...Array(Number(min) + 1).keys()].map(String).concat('unknown'));
  }
  return null;
}

const sevKeyOf = (sev: number | null) => (isKnown(sev) ? String(sev) : 'unknown');

function groupOf(ip: string): string {
  return devs.get(ip)?.group ?? FLEET.find((d) => d.ip === ip)?.group ?? '';
}

function hiddenNow(ip: string): boolean {
  return devs.get(ip)?.hidden ?? !!FLEET.find((d) => d.ip === ip)?.hidden;
}

/** Device-level part of a filter, checked before generating a line's text. */
function deviceMatches(ip: string, f: MockFilters, searchHidden = false): boolean {
  if (f.devices?.length) return f.devices.includes(ip);
  if (searchHidden && hiddenNow(ip)) return false; // hidden devices only when named
  if (f.group || f.ungrouped) {
    const g = groupOf(ip);
    if (f.ungrouped ? g !== '' : g !== f.group) return false;
  }
  return true;
}

function matches(l: GenLine, f: MockFilters): boolean {
  // docs/api.md: hidden devices are left out of the live tail unless named in devices.
  if (!deviceMatches(l.device_id, f, true)) return false;
  if (f.sev && !f.sev.has(sevKeyOf(l.severity))) return false;
  if (f.q && !l.raw.toLowerCase().includes(f.q.toLowerCase())) return false;
  return true;
}

const SAMPLE_LIMIT = 50;

/** One open /api/live stream: SSE text pushed into a ReadableStream. */
class LiveSub {
  private sentThisSecond = 0;
  private matchedThisSecond = 0;
  private readonly filters: MockFilters;
  private readonly out: ReadableStreamDefaultController<Uint8Array>;
  constructor(filters: MockFilters, out: ReadableStreamDefaultController<Uint8Array>) {
    this.filters = filters;
    this.out = out;
  }

  private enc = new TextEncoder();
  write(text: string) {
    try {
      this.out.enqueue(this.enc.encode(text));
    } catch {
      subs.delete(this); // the reader went away
    }
  }

  send(type: string, data: unknown) {
    // Split the event across lines to exercise the parser (data is one JSON line).
    this.write(`event: ${type}\ndata: ${JSON.stringify(data)}\n\n`);
  }

  replay() {
    for (const l of ring.filter((x) => matches(x, this.filters)).slice(-200)) this.send('message', l);
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
}

function liveResponse(u: URLSearchParams, sev: Set<string> | null, signal?: AbortSignal): Response {
  const filters: MockFilters = {
    devices: u.get('devices')?.split(',').filter(Boolean),
    group: u.get('group') ?? undefined,
    ungrouped: u.get('ungrouped') === 'true',
    sev,
    q: u.get('q') ?? undefined,
  };
  let sub: LiveSub | null = null;
  let beat = 0;
  const end = () => {
    if (sub) subs.delete(sub);
    clearInterval(beat);
  };
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      sub = new LiveSub(filters, controller);
      sub.write(': connected\n\n');
      sub.replay();
      subs.add(sub);
      // A heartbeat comment, as the server sends, so idle streams stay alive.
      beat = window.setInterval(() => sub?.write(': ping\n\n'), 15_000);
      signal?.addEventListener('abort', () => {
        end();
        try {
          controller.error(new DOMException('aborted', 'AbortError'));
        } catch {
          /* already closed */
        }
      });
    },
    cancel: end,
  });
  return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
}

// ---------- Routes ----------

let weightCache = { at: 0, total: 1 };
function sendingWeight(now: number): number {
  if (now - weightCache.at > 1000) {
    let t = 0;
    for (const x of devs.values()) if (x.sending) t += x.def.weight;
    weightCache = { at: now, total: t || 1 };
  }
  return weightCache.total;
}

function deviceJSON(d: Dev, now: number): Device {
  const running = d.sending && !mock.firstRun;
  const share = d.def.weight / sendingWeight(now);
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

/** Who holds a port this copy wants, if anyone (mock only). */
function holderOf(proto: 'udp' | 'tcp', port: number): Partial<Status['listeners'][number]> | null {
  if (mock.service514 && port === 514)
    return { holder: 'woodpile (pid 4242)', holder_kind: 'woodpile_service', holder_pid: 4242, holder_ui_url: 'http://localhost:8514/' };
  if (mock.clash && proto === 'tcp' && port === 514) return { holder: 'syslogd (pid 312)' };
  return null;
}

function statusJSON(): Status {
  const l = settings.listeners;
  const listeners: Status['listeners'] = [];
  for (const proto of ['udp', 'tcp'] as const) {
    const c = l[proto];
    if (!c.enabled) continue;
    const h = holderOf(proto, c.port);
    listeners.push({ proto, addr: c.addr, port: c.port, ok: !h, error: h ? 'bind: address already in use' : '', ...(h ?? {}) });
  }
  const banners: Status['banners'] = [];
  const down = listeners.filter((x) => !x.ok);
  if (down.length) {
    const ports = down.map((x) => `${x.proto.toUpperCase()} ${x.port}`).join(' and ');
    const woodpile = down.some((x) => x.holder_kind === 'woodpile_service');
    banners.push({
      kind: 'listener_bind',
      action: 'retry_listeners',
      level: 'error',
      text: woodpile
        ? `${ports} ${down.length > 1 ? 'are' : 'is'} in use by another Woodpile (the installed service).`
        : `${ports} is in use by syslogd (pid 312). Stop the other syslog service, or move the listener to another port in Settings, then press Retry.`,
    });
  }
  if (P.get('mock_ui_port_busy') === '1')
    banners.push({
      kind: 'ui_port_busy',
      action: '',
      level: 'warn',
      text: "The installed Woodpile service is running but can't open its web page, because this copy is using port 8514. Stop this copy to use the service.",
    });
  if (mock.deviceLimit || activeDevices(Date.now()) >= settings.listeners.max_devices)
    banners.push({
      kind: 'device_limit',
      action: '',
      level: 'warn',
      text: `New senders are being refused: ${activeDevices(Date.now()).toLocaleString()} devices sent in the last 10 minutes and the limit is ${settings.listeners.max_devices.toLocaleString()}. Their messages count as dropped.${probe.bannerNote}`,
    });
  if (mock.paused)
    banners.push({
      kind: 'writes_paused',
      action: '',
      level: 'error',
      text: `Writes are paused: the data volume has 0.8 GiB free and there are no old logs left to delete. ${droppedByGuard.toLocaleString()} messages dropped so far. Free some disk space and writing resumes on its own.`,
    });
  return {
    version: '0.1.0',
    build: 'abc1234',
    os: OS,
    data_dir: settings.storage.data_dir,
    started_at: new Date(START - 3 * 3600_000 - 17 * MIN).toISOString(),
    first_message_at: firstMessageAt ? new Date(firstMessageAt).toISOString() : null,
    udp_rcvbuf_bytes: OS.startsWith('darwin') ? 7_454_720 : 33_554_432,
    udp_rcvbuf_requested_bytes: 32 * MiB,
    releases_url: P.get('mock_releases') ?? '',
    kernel_drops_visible: OS.startsWith('linux'),
    ...(mock.lan ? { ui_cert_fingerprint: CERT_FP } : {}),
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
    rejected_24h: rejected(now).reduce((n, r) => n + r.count_24h, 0),
  };
}

// Senders outside the allowlist (IPv4 only in the mock). Counts grow while the mock runs.
const OUTSIDERS = [
  { ip: '198.51.100.7', base: 118, per_min: 6 },
  { ip: '203.0.113.50', base: 41, per_min: 2 },
  { ip: '192.0.2.200', base: 6, per_min: 0.2 },
];

function v4(ip: string): number | null {
  if (!isIPv4(ip)) return null;
  return ip.split('.').reduce((n, p) => n * 256 + Number(p), 0);
}

function allowed(ip: string): boolean {
  const n = v4(ip);
  return settings.listeners.allowlist.some((c) => {
    const [net, bits = '32'] = c.split('/');
    const base = v4(net);
    if (base === null || n === null) return false;
    const b = Number(bits);
    const mask = b === 0 ? 0 : (0xffffffff << (32 - b)) >>> 0;
    return ((n & mask) >>> 0) === ((base & mask) >>> 0);
  });
}

function rejected(now: number): RejectedSender[] {
  if (mock.firstRun) return [];
  const mins = (now - START) / MIN;
  const out: RejectedSender[] = settings.listeners.allowlist.length
    ? OUTSIDERS.filter((o) => !allowed(o.ip)).map((o, i) => ({
        ip: o.ip,
        count_24h: Math.round(o.base + o.per_min * mins),
        last_seen: new Date(now - (i * 47 + 3) * 1000).toISOString(),
        reason: 'allowlist',
      }))
    : [];
  // One host opening more TCP connections than the per-source limit allows.
  out.push({ ip: '10.9.0.66', count_24h: Math.round(2_310 + 40 * mins), last_seen: new Date(now - 9000).toISOString(), reason: 'per_source' });
  return out.sort((a, b) => b.count_24h - a.count_24h);
}

// ---------- Exports ----------

const H = 60 * MIN;
const EXPIRE_MS = 24 * H;

interface MockExport extends ExportInfo {
  estimate: number;
}

const noFilters = (): ExportInfo['filters'] => ({ group: '', ungrouped: false, severities: [], q: '', regex: false, case: false });
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

const exportsList: MockExport[] = [
  {
    id: 'exp_7Hq2', created: ago(23 * H + 10 * MIN), ready_at: ago(23 * H), expires_at: ago(23 * H - EXPIRE_MS),
    devices: ['10.1.0.1', '10.1.0.2'], from: ago(48 * H), to: ago(24 * H),
    state: 'ready', progress: 1, lines: 1_204_332, size_bytes: 48 * MiB, error: '', estimate: 0,
    filters: { ...noFilters(), severities: [3, 4], q: 'deny' },
  },
  {
    id: 'exp_3kPz', created: ago(5 * H), ready_at: null, expires_at: null,
    devices: [], from: ago(7 * 24 * H), to: ago(5 * H),
    state: 'failed', progress: 0.4, lines: 0, size_bytes: 0, error: 'Not enough free disk space to build the zip.', estimate: 0,
    filters: { ...noFilters(), group: 'DMZ' },
  },
  {
    id: 'exp_9aLm', created: ago(30 * H), ready_at: ago(29.9 * H), expires_at: ago(5.9 * H),
    devices: ['10.8.0.1'], from: ago(32 * H), to: ago(31 * H),
    state: 'expired', progress: 1, lines: 8_812, size_bytes: 0, error: '', estimate: 0,
    filters: { ...noFilters(), q: 'link (up|down)', regex: true },
  },
];

function estimate(b: { devices: string[]; from: string; to: string; severities?: unknown[] }) {
  const span = Math.max(0, Date.parse(b.to) - Date.parse(b.from));
  let lines = Math.round((span / STEP) * shareOf(b.devices));
  // An upper bound from the index: severity and text filters can only shrink it.
  if (b.severities?.length) lines = Math.round(lines * Math.min(1, b.severities.length / 3));
  const days = Math.max(1, Math.ceil(span / (24 * H)));
  return { lines, bytes: lines * 142, files: b.devices.length * days };
}

/** One build at a time; the rest wait in the queue. */
function exportTick() {
  const now = Date.now();
  for (const e of exportsList) if (e.state === 'ready' && e.expires_at && now > Date.parse(e.expires_at)) e.state = 'expired';
  let cur = exportsList.find((e) => e.state === 'building');
  if (!cur) {
    cur = [...exportsList].reverse().find((e) => e.state === 'queued');
    if (cur) cur.state = 'building';
  }
  if (!cur) return;
  cur.progress = Math.min(1, cur.progress + 0.25 / mock.exportSecs);
  cur.lines = Math.round(cur.estimate * 0.8 * cur.progress);
  cur.size_bytes = Math.round(cur.lines * 32);
  if (mock.exportFail && cur.progress >= 0.4) {
    cur.state = 'failed';
    cur.error = 'Not enough free disk space to build the zip.';
  } else if (cur.progress >= 1) {
    cur.state = 'ready';
    cur.ready_at = new Date(now).toISOString();
    cur.expires_at = new Date(now + EXPIRE_MS).toISOString();
  }
}
setInterval(exportTick, 250);

/** Download tickets: opaque → export, issuing session's secret, expiry. */
const tickets = new Map<string, { id: string; secret: string; until: number }>();

const exportJSON = (e: MockExport): ExportInfo => {
  const { estimate: _e, ...rest } = e;
  void _e;
  return rest;
};

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

// ---------- Search ----------

/** Lines scanned per request before the 10 s budget "runs out" (slow-scan mode: far fewer). */
const scanBudget = () => (mock.slowScan ? 2500 : 120_000);

function searchRoute(u: URLSearchParams, now: number): Response {
  let sev: Set<string> | null;
  try {
    sev = sevFilter(u);
  } catch (e) {
    return err(400, (e as Error).message);
  }
  const q = u.get('q') ?? '';
  const isRe = u.get('regex') === 'true';
  const cs = u.get('case') === 'true';
  let re: RegExp | null = null;
  if (isRe) {
    try {
      re = new RegExp(q, cs ? '' : 'i');
    } catch (e) {
      // RE2's message style; the JS engine's detail follows.
      return err(400, `error parsing regexp: ${(e as Error).message.replace(/^Invalid regular expression: /, '')}`);
    }
  }
  const to = u.has('to') ? Date.parse(u.get('to')!) : now;
  const from = u.has('from') ? Date.parse(u.get('from')!) : to - 24 * H;
  if (Number.isNaN(from) || Number.isNaN(to)) return err(400, '"from" and "to" must be RFC 3339 times.');
  if (!(from < to)) return err(400, '"from" must be before "to".');
  const order = u.get('order') === 'asc' ? 'asc' : 'desc';
  const limit = Math.min(5000, Math.max(1, Number(u.get('limit') || 500)));
  const f: MockFilters = {
    devices: u.get('devices')?.split(',').filter(Boolean),
    group: u.get('group') ?? undefined,
    ungrouped: u.get('ungrouped') === 'true',
    sev,
  };
  // The cursor is tied to the other parameters, as on the real server.
  const sigSrc = new URLSearchParams(u);
  sigSrc.delete('cursor');
  sigSrc.delete('limit');
  sigSrc.sort();
  const sig = [...sigSrc.toString()].reduce((h, c) => (Math.imul(h, 31) + c.charCodeAt(0)) >>> 0, 7).toString(36);
  const lo = Math.ceil(from / STEP);
  const hi = Math.floor(Math.min(to, now) / STEP);
  let i = order === 'desc' ? hi : lo;
  const cur = u.get('cursor');
  if (cur) {
    const [idx, s2] = cur.split('.');
    if (s2 !== sig) return err(400, 'The cursor belongs to a different search.');
    i = Number(idx);
  }
  const step = order === 'desc' ? -1 : 1;
  const inRange = (k: number) => k >= lo && k <= hi;
  const results: (GenLine & { ref: string })[] = [];
  const files = new Set<string>();
  let scanned = 0;
  const budget = scanBudget();
  const t0 = performance.now();
  for (; inRange(i) && results.length < limit && scanned < budget; i += step) {
    scanned++;
    const d = deviceAt(i);
    if (!deviceMatches(d.ip, f, true)) continue;
    files.add(`${d.ip}/${Math.floor((i * STEP) / (24 * H))}`);
    const l = lineAt(i);
    if (sev && !sev.has(sevKeyOf(l.severity))) continue;
    if (re ? !re.test(l.raw) : q && !(cs ? l.raw.includes(q) : l.raw.toLowerCase().includes(q.toLowerCase()))) continue;
    results.push({ ...l, ref: `h${i}` });
  }
  const exhausted = !inRange(i);
  return json({
    results,
    next_cursor: exhausted ? '' : `${i}.${sig}`,
    scanned_bytes: scanned * 142,
    scanned_files: files.size,
    elapsed_ms: Math.round(performance.now() - t0) + (mock.slowScan ? 9800 : 40),
    partial: !exhausted && results.length < limit,
  });
}

async function route(method: string, url: URL, body: unknown, header: string | null, signal?: AbortSignal): Promise<Response> {
  const p = url.pathname;
  const now = Date.now();
  const changing = !['GET', 'HEAD', 'OPTIONS'].includes(method);
  if (p !== '/api/live') {
    if (changing) probe.lastWriteHeader = header;
    else probe.lastGetHeader = header;
  }

  if (method === 'GET' && p === '/healthz') return json({ ok: true });
  if (method === 'GET' && p === '/api/session') {
    const body = JSON.stringify(sessionJSON(header));
    probe.sessionBodies.push(body);
    return new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } });
  }
  if (method === 'POST' && p === '/api/login') {
    if (!authOn()) return err(400, 'Login is off: LAN mode and require_login_local are both off.');
    const locked = lockout(now);
    if (locked) return locked;
    const recent = authState.failures.filter((t) => now - t < 15 * 60_000).length;
    if (recent >= 5 && now - authState.lastAttempt < 1000) return retryLater(1, 'Slow down.');
    authState.lastAttempt = now;
    if ((body as { password?: string })?.password !== authState.password) {
      recordFailure(now);
      return lockout(now) ?? err(401, 'Wrong password.');
    }
    authState.failures = [];
    mock.loggedIn = true;
    authState.secret = newToken();
    return json({ ...sessionJSON(authState.secret), session_secret: authState.secret });
  }
  if (method === 'POST' && p === '/api/logout') {
    mock.loggedIn = false;
    authState.secret = '';
    return new Response(null, { status: 204 });
  }
  const ticketed = method === 'GET' && /^\/api\/exports\/[^/]+\/download$/.test(p) && url.searchParams.has('ticket');
  if (authOn() && !PUBLIC.includes(p) && !ticketed) {
    // docs/api.md: the cookie and the X-Woodpile-Session header together; either alone is a 401.
    if (!hasSession(header)) return err(401, 'Log in first.');
  }
  if (method === 'PUT' && p === '/api/password') {
    const b = (body ?? {}) as { current?: string; new?: string };
    if (authState.password) {
      const locked = lockout(now);
      if (locked) return locked;
      if (b.current !== authState.password) {
        recordFailure(now);
        return err(403, 'The current password is wrong.');
      }
    }
    const len = new TextEncoder().encode(b.new ?? '').length;
    if (len < 8 || len > 1024)
      return json({ error: 'That password is not allowed.', errors: { new: 'Use 8 to 1024 characters.' } }, 400);
    // docs/api.md: the first password also turns on require_login_local.
    if (!authState.password) settings.ui.require_login_local = true;
    authState.password = b.new ?? '';
    return new Response(null, { status: 204 });
  }
  if (method === 'GET' && p === '/api/status') return json(statusJSON());
  if (method === 'POST' && p === '/api/listeners/retry') {
    await sleep(500);
    mock.clash = false; // pretend the other process was stopped
    return new Response(null, { status: 204 });
  }
  if (method === 'GET' && p === '/api/live') {
    probe.lastLiveHeader = header;
    probe.livePaths.push(url.pathname + url.search);
    let sev: Set<string> | null;
    try {
      sev = sevFilter(url.searchParams);
    } catch (e) {
      return err(400, (e as Error).message);
    }
    return liveResponse(url.searchParams, sev, signal);
  }
  if (method === 'GET' && p === '/api/stats') return json(statsJSON(now));
  if (method === 'GET' && p === '/api/devices') return devicesRoute(url.searchParams, now);
  if (method === 'GET' && p === '/api/groups') {
    const counts = new Map<string, number>();
    for (const d of devs.values()) if (d.group && !d.hidden) counts.set(d.group, (counts.get(d.group) ?? 0) + 1);
    return json([...counts].sort((a, b) => a[0].localeCompare(b[0])).map(([name, devices]) => ({ name, devices })));
  }
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
    const wasLan = settings.ui.lan_mode;
    settings = structuredClone(s);
    if (wasLan !== s.ui.lan_mode) {
      // The real server restarts at the new address; the mock just switches modes and keeps
      // this browser logged in, as if it had already logged in there.
      mock.lan = s.ui.lan_mode;
      // The real server restarts at a new origin, where this browser has no secret yet. The
      // mock keeps the origin, so it keeps the current session (if any) rather than invent one.
    }
    return json(settings);
  }
  if (method === 'GET' && p === '/api/storage/history') return json(history());
  if (method === 'GET' && p === '/api/rejected') return json(rejected(now));
  if (method === 'GET' && p === '/api/search') return searchRoute(url.searchParams, now);
  if (method === 'GET' && p === '/api/context') {
    const m = /^h(\d+)$/.exec(url.searchParams.get('ref') ?? '');
    if (!m) return err(404, 'That line is no longer stored.');
    const i = Number(m[1]);
    const before = Math.min(500, Number(url.searchParams.get('before') ?? 50));
    const after = Math.min(500, Number(url.searchParams.get('after') ?? 50));
    const ip = deviceAt(i).ip;
    const lastIdx = Math.floor(now / STEP);
    const lo: number[] = [];
    for (let j = i - 1; lo.length < before && j > i - 400_000; j--) if (deviceAt(j).ip === ip) lo.push(j);
    const hi: number[] = [];
    for (let j = i + 1; hi.length < after && j <= lastIdx; j++) if (deviceAt(j).ip === ip) hi.push(j);
    const idx = [...lo.reverse(), i, ...hi];
    // History starts 40 days back in the mock; the newest stored line is "now".
    const firstIdx = Math.ceil((now - 40 * 24 * H) / STEP);
    let older = false;
    for (let j = (idx[0] ?? i) - 1; j >= firstIdx && j > (idx[0] ?? i) - 400_000; j--) if (deviceAt(j).ip === ip) { older = true; break; }
    let newer = false;
    for (let j = idx[idx.length - 1] + 1; j <= lastIdx; j++) if (deviceAt(j).ip === ip) { newer = true; break; }
    return json({ lines: idx.map((j) => ({ ...lineAt(j), ref: `h${j}` })), hit_index: lo.length, at_start: !older, at_end: !newer });
  }
  if (method === 'POST' && (p === '/api/exports' || p === '/api/exports/estimate')) {
    const b = (body ?? {}) as {
      all_devices?: boolean; devices?: string[]; group?: string; ungrouped?: boolean; from?: string; to?: string;
      severities?: unknown[]; q?: string; regex?: boolean; case?: boolean;
    };
    // devices, group or ungrouped: resolved as the build would (hidden devices only when named).
    probe.lastExportBody = JSON.stringify(b);
    const f: MockFilters = { devices: b.devices, group: b.group || undefined, ungrouped: !!b.ungrouped, sev: null };
    const anySelector = b.all_devices || !!b.devices?.length || !!b.group || !!b.ungrouped;
    const ids = anySelector ? FLEET.map((d) => d.ip).filter((ip) => deviceMatches(ip, f, true)) : [];
    if (!ids.length) return err(400, 'Pick at least one device (by list, group or ungrouped).');
    if (!b.from || !b.to || !(Date.parse(b.from) < Date.parse(b.to))) return err(400, '"from" must be before "to".');
    for (const x of b.severities ?? []) if (!(x === 'unknown' || (Number.isInteger(x) && (x as number) >= 0 && (x as number) <= 7))) return err(400, `invalid severity ${JSON.stringify(x)}`);
    if (b.regex && b.q) {
      try {
        new RegExp(b.q);
      } catch (e) {
        return err(400, `error parsing regexp: ${(e as Error).message.replace(/^Invalid regular expression: /, '')}`);
      }
    }
    const est = estimate({ devices: ids, from: b.from, to: b.to, severities: b.severities });
    if (p === '/api/exports/estimate') return json(est);
    const e: MockExport = {
      id: `exp_${Math.random().toString(36).slice(2, 6)}`, created: new Date(now).toISOString(),
      devices: b.devices?.length ? b.devices : [], from: b.from, to: b.to,
      state: 'queued', progress: 0, lines: 0, size_bytes: 0, error: '', estimate: est.lines, ready_at: null, expires_at: null,
      filters: {
        group: b.group ?? '', ungrouped: !!b.ungrouped, severities: (b.severities ?? []) as ExportInfo['filters']['severities'],
        q: b.q ?? '', regex: !!b.regex, case: !!b.case,
      },
    };
    exportsList.unshift(e);
    return json({ id: e.id }, 202);
  }
  if (method === 'GET' && p === '/api/exports') return json(exportsList.map(exportJSON));
  const tk = p.match(/^\/api\/exports\/([^/]+)\/ticket$/);
  if (tk && method === 'POST') {
    const e = exportsList.find((x) => x.id === decodeURIComponent(tk[1]));
    if (!e || e.state === 'expired') return err(404, 'No such export.');
    if (e.state !== 'ready') return err(409, 'That export is not ready yet.');
    const t = newToken();
    tickets.set(t, { id: e.id, secret: authState.secret, until: now + 60_000 });
    probe.lastTicketUrl = `/api/exports/${encodeURIComponent(e.id)}/download?ticket=${t}`;
    return json({ url: probe.lastTicketUrl });
  }
  const ex = p.match(/^\/api\/exports\/([^/]+)(\/download)?$/);
  if (ex) {
    const k = exportsList.findIndex((e) => e.id === decodeURIComponent(ex[1]));
    const e = exportsList[k];
    if (!e) return err(404, 'No such export.');
    if (method === 'GET' && ex[2]) {
      // A ticket: single-use, 60 s, bound to the session (and its cookie) and to this export.
      const t = url.searchParams.get('ticket');
      if (t !== null) {
        const tk2 = tickets.get(t);
        tickets.delete(t);
        if (!tk2 || tk2.until < now || tk2.id !== e.id || (authOn() && (!mock.loggedIn || tk2.secret !== authState.secret)))
          return err(401, 'That download link has expired. Ask for a new one.');
      }
      probe.downloads.push({ id: e.id, ticket: t !== null, status: 200 });
      if (e.state === 'expired') return err(404, 'That export has expired.');
      if (e.state !== 'ready') return err(409, 'That export is not ready yet.');
      const name = `woodpile-${e.from.slice(0, 16).replace(/:/g, '')}-${e.to.slice(0, 16).replace(/:/g, '')}.zip`;
      return new Response('mock export', {
        status: 200,
        headers: { 'Content-Type': 'application/zip', 'Content-Disposition': `attachment; filename="${name}"` },
      });
    }
    if (method === 'GET') return json(exportJSON(e));
    if (method === 'DELETE') {
      exportsList.splice(k, 1); // cancels a queued or building one, deletes a finished one
      return new Response(null, { status: 204 });
    }
  }
  return err(404, `No route for ${method} ${p}.`);
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export const mockTransport: Transport = {
  async fetch(path, init) {
    const url = new URL(path, location.origin);
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
    await sleep(mock.latency * (0.5 + Math.random()));
    if (mock.slowScan && url.pathname === '/api/search') await sleep(700);
    const header = probe.cookieOnly ? null : new Headers(init?.headers).get('X-Woodpile-Session');
    return route(method, url, body, header, init?.signal ?? undefined);
  },
  async download(path) {
    // Like a browser download: the cookie goes along, the X-Woodpile-Session header can't.
    const res = await mockTransport.fetch(path, { headers: {} });
    probe.lastDownloadStatus = res.status;
    if (!res.ok) return;
    const a = document.createElement('a');
    a.href = URL.createObjectURL(await res.blob());
    a.download = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'woodpile-export.zip';
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  },
};
