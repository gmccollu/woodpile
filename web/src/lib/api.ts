// Typed wrappers for the Woodshed HTTP API (docs/api.md). The UI talks to the
// server only through this module. In mock mode (VITE_MOCK=1) main.ts swaps the
// transport for the in-browser fake in ./mock, and nothing else changes.

import { SseParser, type SseEvent } from './sse';

// ---------- Types (docs/api.md) ----------

export type Proto = 'udp' | 'tcp';

export interface Listener {
  proto: Proto;
  addr: string;
  port: number;
  ok: boolean;
  error: string;
  /** Process holding the port, when the OS lets the server find out. */
  holder?: string;
  /**
   * docs/api.md (0d039ea): the holder is another Woodshed, the installed service or another
   * running copy; omitted (or "") otherwise. When the holder can't be seen (another user's
   * process), a running service is reported as the likely holder.
   */
  holder_kind?: 'woodshed_service' | 'woodshed' | '' | (string & {});
  holder_pid?: number;
  /** That Woodshed's UI address, when known. */
  holder_ui_url?: string;
}

export type BannerLevel = 'error' | 'warn' | 'info';

export type BannerKind =
  | 'listener_bind'
  | 'writes_paused'
  | 'low_disk'
  | 'ui_port_busy'
  | 'udp_buffer_small'
  | 'update_prompt'
  | 'update_available';
/** update_choice: Turn on / No thanks (PUT /api/updates); update_dismiss: hide this version (POST /api/updates/dismiss). */
export type BannerAction = '' | 'retry_listeners' | 'update_choice' | 'update_dismiss';

export interface Banner {
  /** Unknown future kinds are shown like any other banner. */
  kind: BannerKind | (string & {});
  level: BannerLevel;
  text: string;
  action: BannerAction | (string & {});
  /** A link for the banner (update_available: the release page); omitted when empty. */
  url?: string;
}

export interface Status {
  version: string;
  build: string;
  os: string;
  data_dir: string;
  started_at: string;
  /** null until the first message is received; drives the Welcome page. */
  first_message_at: string | null;
  /** What the OS actually granted. */
  udp_rcvbuf_bytes: number;
  udp_rcvbuf_requested_bytes: number;
  /** Empty until the project has a public releases page. */
  releases_url: string;
  /** SHA-256 of the UI's HTTPS certificate (LAN mode), so users can check the browser's warning. Being added. */
  ui_cert_fingerprint?: string;
  kernel_drops_visible: boolean;
  listeners: Listener[];
  host_ips: string[];
  banners: Banner[];
}

export interface Stats {
  msgs_per_sec: number;
  /** msgs/sec averaged per minute, oldest first, last 60 minutes. */
  sparkline: number[];
  devices: { total: number; online: number; quiet: number; offline: number };
  disk: {
    used_bytes: number;
    cap_bytes: number;
    free_bytes: number;
    days_kept: number;
    /** On-disk bytes written over the last 24 h (raw / compression_ratio); null until there's 1 h of data. */
    bytes_per_day: number | null;
    /** Measured on this server's closed files (8 until it has closed one). */
    compression_ratio: number | null;
    /** cap_bytes / bytes_per_day, or null. */
    estimated_days: number | null;
    /** "cap" when the size cap, not the age limit, decides how long logs are kept. */
    limited_by: 'cap' | 'age' | null;
  };
  dropped_24h: number;
  /** Integer on Linux; null where the OS doesn't expose kernel UDP drops. */
  kernel_dropped_24h: number | null;
  truncated_24h: number;
  /** Messages from senders outside the allowlist. */
  rejected_24h: number;
}

export interface RejectedSender {
  ip: string;
  count_24h: number;
  last_seen: string;
  /** The latest refusal's reason: not on the allowlist, or over the TCP connections-per-source limit. */
  reason: 'allowlist' | 'blocked' | 'per_source' | (string & {});
}

export type DeviceStatus = 'online' | 'quiet' | 'offline' | 'blocked';

export interface Device {
  id: string;
  ip: string;
  name: string;
  group: string;
  hidden: boolean;
  first_seen: string;
  last_heard: string;
  status: DeviceStatus;
  msgs_per_min: number;
  total_msgs: number;
  bytes_on_disk: number;
  /** On the blocklist: its messages are refused and counted; its existing logs stay searchable. */
  blocked: boolean;
  /** On a PATCH that unblocked the device's own address: the broader blocklist entry that still covers it. */
  blocked_by?: string;
}

export type DevicePatch = Partial<Pick<Device, 'name' | 'group' | 'hidden' | 'blocked'>>;

export interface LogLine {
  received: string;
  device_id: string;
  /** 0 emerg … 7 debug, or null when the message had no parseable PRI. */
  severity: number | null;
  raw: string;
}

export interface SampledNotice {
  rate: number;
  shown: number;
}

/** A syslog severity 0 (emerg) … 7 (debug), or "unknown" (no parseable PRI). */
export type SevKey = 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 'unknown';
export const ALL_SEVS: SevKey[] = [0, 1, 2, 3, 4, 5, 6, 7, 'unknown'];

/**
 * The `severities=` value (docs/api.md → Severity filters): all selected → undefined (the
 * server's default is all), nothing selected → "none".
 */
export function sevParam(sel: SevKey[]): string | undefined {
  if (!sel.length) return 'none';
  if (ALL_SEVS.every((k) => sel.includes(k))) return undefined;
  return ALL_SEVS.filter((k) => sel.includes(k)).join(',');
}

export interface LiveFilters {
  devices?: string[];
  group?: string;
  /** Only devices with no group. An alternative to `group`. */
  ungrouped?: boolean;
  /** Severity set; undefined or all = every severity. */
  severities?: SevKey[];
  q?: string;
}

export interface SearchParams {
  q: string;
  regex?: boolean;
  /** Case-sensitive match (default insensitive). */
  case?: boolean;
  devices?: string[];
  group?: string;
  ungrouped?: boolean;
  from?: string;
  to?: string;
  severities?: SevKey[];
  order?: 'desc' | 'asc';
  limit?: number;
  cursor?: string;
  /** Programs/tags from the header, ignoring case; a trailing "*" is a prefix, a leading "%" optional. */
  app?: string[];
  /** Hostnames from the header (not the sender IP), ignoring case. */
  host?: string[];
  /** Leave out lines matching this; regex and case apply as for q. */
  exclude?: string;
}

export interface SearchResult extends LogLine {
  ref: string;
  /** The parsed program/tag ("" when the header has none). */
  app: string;
  /** The parsed header hostname ("" when the header has none). */
  host: string;
}

export interface SearchResponse {
  results: SearchResult[];
  /** Empty when the range is exhausted. */
  next_cursor: string;
  scanned_bytes: number;
  scanned_files: number;
  elapsed_ms: number;
  /** The 10 s budget ran out: keep asking with next_cursor. */
  partial: boolean;
}

export interface ContextResponse {
  lines: SearchResult[];
  hit_index: number;
  /** No older / newer stored lines for that device. */
  at_start: boolean;
  at_end: boolean;
}

/** Devices by list, group, ungrouped or all (at least one must select a device); filters as for search. */
export interface ExportRequest {
  /** Every non-hidden device. */
  all_devices?: boolean;
  devices?: string[];
  group?: string;
  ungrouped?: boolean;
  from: string;
  to: string;
  severities?: SevKey[];
  q?: string;
  regex?: boolean;
  case?: boolean;
  app?: string[];
  host?: string[];
  exclude?: string;
}

export interface ExportFilters {
  group: string;
  ungrouped: boolean;
  severities: SevKey[];
  q: string;
  regex: boolean;
  case: boolean;
  app: string[];
  host: string[];
  exclude: string;
}

export interface ExportEstimate {
  lines: number;
  bytes: number;
  files: number;
}

export type ExportState = 'queued' | 'building' | 'ready' | 'failed' | 'expired';

export interface ExportInfo {
  id: string;
  created: string;
  devices: string[];
  from: string;
  to: string;
  state: ExportState;
  /** 0…1 while building. */
  progress: number;
  lines: number;
  size_bytes: number;
  error: string;
  /** Set once the export is ready. */
  ready_at: string | null;
  expires_at: string | null;
  /** Echoes the request's filters. */
  filters: ExportFilters;
}

export interface ListenerSettings {
  enabled: boolean;
  addr: string;
  port: number;
}

export interface Settings {
  listeners: {
    udp: ListenerSettings;
    tcp: ListenerSettings;
    allowlist: string[];
    /** Senders refused at the listener (IPs or CIDRs); checked before the allowlist and wins. */
    blocklist: string[];
    /** Sender limits against floods of fake source addresses (docs/api.md). */
    max_devices: number;
    new_device_burst: number;
    new_devices_per_minute: number;
  };
  storage: { data_dir: string; retention_days: number; retention_bytes: number; part_bytes: number };
  /**
   * cert_file/key_file: paths to a user certificate for LAN mode; empty = self-signed.
   * extra_hostnames: more names for the self-signed certificate; trusted_hosts: more Host
   * names to accept (host or host:port, for a reverse proxy). Older servers omit both.
   */
  ui: {
    lan_mode: boolean;
    port: number;
    cert_file: string;
    key_file: string;
    require_login_local: boolean;
    extra_hostnames: string[];
    trusted_hosts: string[];
  };
  quiet: { quiet_after_seconds: number; offline_after_seconds: number };
  /** The update check (1.1). Older servers omit it; a PUT without it keeps it. */
  updates?: { check: UpdateCheck };
}

export type UpdateCheck = 'ask' | 'on' | 'off';

/** GET /api/updates (docs/api.md → Update check). */
export interface UpdateInfo {
  check: UpdateCheck;
  /** false in a build that can't check (not a release version, or no releases page). */
  available: boolean;
  /** The one file the check fetches. */
  url: string;
  current: string;
  last_check: string | null;
  last_result: '' | 'ok' | 'error';
  /** A short message Woodshed writes itself. */
  last_error: string;
  last_error_kind: string;
  /** null unless check is "on". */
  next_check: string | null;
  /** null until a check succeeds, and while check isn't "on". */
  latest: { version: string; prerelease: boolean; url: string } | null;
  update_available: boolean;
  dismissed_version: string;
}

export type DeletionReason = 'age' | 'cap' | 'low_disk';

export interface StorageHistoryEntry {
  time: string;
  file: string;
  device_id: string;
  bytes: number;
  reason: DeletionReason;
}

// ---------- Errors ----------

export interface Session {
  mode: 'localhost' | 'lan';
  auth_required: boolean;
  authenticated: boolean;
  password_set: boolean;
}

/** POST /api/login also returns the session secret, once (docs/api.md → Session secret). */
export interface LoginResponse extends Session {
  session_secret: string;
}

export class ApiError extends Error {
  readonly status: number;
  /** Per-field messages from a 400 (e.g. PUT /api/settings), keyed by dotted path ("listeners.allowlist[2]"). */
  readonly fields: Record<string, string>;
  /** Seconds from a Retry-After header (429 on login), else 0. */
  readonly retryAfter: number;

  constructor(status: number, message: string, fields: Record<string, string> = {}, retryAfter = 0) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.fields = fields;
    this.retryAfter = retryAfter;
  }
}

/** Retry-After is either delay-seconds or an HTTP date (RFC 9110 §10.2.3). */
function retryAfterSeconds(h: string | null): number {
  if (!h) return 0;
  const n = Number(h);
  if (Number.isFinite(n)) return Math.max(0, Math.ceil(n));
  const t = Date.parse(h);
  return Number.isNaN(t) ? 0 : Math.max(0, Math.ceil((t - Date.now()) / 1000));
}

// ---------- Session secret and 401 handling ----------
//
// docs/api.md: when login is required, every /api/ request needs the session cookie AND an
// X-Woodshed-Session header with the secret from POST /api/login. The cookie isn't port-
// scoped; localStorage is scoped to the exact origin (scheme, host and port), so the secret
// lives there, under a key per origin, and is read on every request (a logout in another
// tab takes effect here too).

const SECRET_KEY = `woodshed.session.${location.origin}`;
export const SESSION_HEADER = 'X-Woodshed-Session';

export function sessionSecret(): string {
  try {
    return localStorage.getItem(SECRET_KEY) ?? '';
  } catch {
    return '';
  }
}

export function setSessionSecret(secret: string): void {
  try {
    if (secret) localStorage.setItem(SECRET_KEY, secret);
    else localStorage.removeItem(SECRET_KEY);
  } catch {
    /* storage unavailable (private mode): requests will get 401 and show the login */
  }
}

/** Headers for an /api/ request: the session secret when there is one. */
function authHeaders(): Record<string, string> {
  const s = sessionSecret();
  return s ? { [SESSION_HEADER]: s } : {};
}

let onUnauthorized: (() => void) | null = null;

/** Called on any 401 from a route that needs a session, so the app can show the login form. */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

function unauthorized(): void {
  setSessionSecret('');
  onUnauthorized?.();
}

const SESSION_ROUTES = ['/api/session', '/api/login', '/api/logout'];

// ---------- Transport ----------

export interface Transport {
  fetch(path: string, init?: RequestInit): Promise<Response>;
  /** Mock mode only: there's no server behind a plain download link. */
  download?(path: string): Promise<void>;
}

const browserTransport: Transport = {
  fetch: (path, init) => fetch(path, { credentials: 'same-origin', ...init }),
};

let transport: Transport = browserTransport;

/** True when a download link must go through the transport (mock mode). */
export const downloadsViaTransport = () => !!transport.download;
export const transportDownload = (path: string) => transport.download?.(path);

export function setTransport(t: Transport): void {
  transport = t;
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return (await requestFull<T>(method, path, body, undefined, signal)).data;
}

/** request(), with extra request headers and the response's headers. */
async function requestFull<T>(
  method: string,
  path: string,
  body?: unknown,
  extra?: Record<string, string>,
  signal?: AbortSignal,
): Promise<{ data: T; headers: Headers }> {
  const headers: Record<string, string> = { Accept: 'application/json', ...authHeaders(), ...extra };
  const init: RequestInit = { method, headers, signal };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    headers['Content-Type'] = 'application/json';
  }
  let res: Response;
  try {
    res = await transport.fetch(path, init);
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e;
    throw new ApiError(0, `no connection: ${(e as Error).message}`);
  }
  if (res.status === 204) return { data: undefined as T, headers: res.headers };
  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      if (res.ok) throw new ApiError(res.status, `Unexpected reply from ${path}.`);
    }
  }
  if (!res.ok) {
    const d = (data ?? {}) as { error?: string; errors?: Record<string, string> };
    const fields = d.errors && typeof d.errors === 'object' ? d.errors : {};
    const msg = d.error || (Object.keys(fields).length ? 'Some settings need fixing.' : `${res.status} ${res.statusText}`);
    if (res.status === 401 && !SESSION_ROUTES.includes(path.split('?')[0])) unauthorized();
    throw new ApiError(res.status, msg, fields, retryAfterSeconds(res.headers.get('Retry-After')));
  }
  return { data: data as T, headers: res.headers };
}

function query(params: Record<string, string | number | boolean | string[] | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '') continue;
    if (Array.isArray(v)) {
      if (v.length) q.set(k, v.join(','));
    } else q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

// ---------- Routes ----------

export const getSession = () => request<Session>('GET', '/api/session');
export const login = (password: string) => request<LoginResponse>('POST', '/api/login', { password });
export const logout = () => request<void>('POST', '/api/logout');
export const putPassword = (current: string, next: string) =>
  request<void>('PUT', '/api/password', { current, new: next });

export const getStatus = () => request<Status>('GET', '/api/status');
export const retryListeners = () => request<void>('POST', '/api/listeners/retry');
export const getStats = () => request<Stats>('GET', '/api/stats');
export const getRejected = () => request<RejectedSender[]>('GET', '/api/rejected');

export type DeviceSort = 'rate' | 'name' | 'last_heard' | 'first_seen';

export interface DeviceQuery {
  limit?: number;
  offset?: number;
  /** Server default is rate (busiest first). */
  sort?: DeviceSort;
  /** Substring of id, IP or name (case-insensitive). */
  q?: string;
  group?: string;
  ungrouped?: boolean;
  /** Server default is include. */
  hidden?: 'include' | 'only' | 'exclude';
  /** The server takes this like hidden (default include); not in docs/api.md yet, see the report. */
  blocked?: 'include' | 'only' | 'exclude';
}

export interface DevicePage {
  devices: Device[];
  total: number;
}

/** A group and its number of non-hidden devices (ungrouped devices aren't listed). */
export interface GroupInfo {
  name: string;
  devices: number;
}

export const getGroups = () => request<GroupInfo[]>('GET', '/api/groups');

export const getDevices = (p: DeviceQuery = {}, signal?: AbortSignal) =>
  request<DevicePage>(
    'GET',
    `/api/devices${query({ ...p, q: p.q || undefined, group: p.group || undefined, ungrouped: p.ungrouped ? true : undefined })}`,
    undefined,
    signal,
  );
export const patchDevice = (id: string, patch: DevicePatch) =>
  request<Device>('PATCH', `/api/devices/${encodeURIComponent(id)}`, patch);

export const searchPath = (p: SearchParams) =>
  `/api/search${query({
    q: p.q,
    regex: p.regex ? true : undefined,
    case: p.case ? true : undefined,
    devices: p.devices,
    group: p.group,
    ungrouped: p.ungrouped ? true : undefined,
    from: p.from,
    to: p.to,
    severities: p.severities ? sevParam(p.severities) : undefined,
    app: p.app,
    host: p.host,
    exclude: p.exclude,
    order: p.order,
    limit: p.limit,
    cursor: p.cursor,
  })}`;
export const search = (p: SearchParams, signal?: AbortSignal) =>
  request<SearchResponse>('GET', searchPath(p), undefined, signal);
export const getContext = (ref: string, before = 50, after = 50) =>
  request<ContextResponse>('GET', `/api/context${query({ ref, before, after })}`);

/** Optional filters are sent only when they narrow the export (all severities = no filter). */
function exportBody(r: ExportRequest): ExportRequest {
  const b: ExportRequest = { from: r.from, to: r.to };
  if (r.all_devices) b.all_devices = true;
  if (r.devices?.length) b.devices = r.devices;
  if (r.group) b.group = r.group;
  if (r.ungrouped) b.ungrouped = true;
  if (r.severities && r.severities.length && sevParam(r.severities)) b.severities = ALL_SEVS.filter((k) => r.severities!.includes(k));
  if (r.app?.length) b.app = r.app;
  if (r.host?.length) b.host = r.host;
  if (r.exclude) b.exclude = r.exclude;
  if (r.q || r.exclude) {
    if (r.q) b.q = r.q;
    if (r.regex) b.regex = true;
    if (r.case) b.case = true;
  }
  return b;
}
export const createExport = (r: ExportRequest) => request<{ id: string }>('POST', '/api/exports', exportBody(r));
export const estimateExport = (r: ExportRequest, signal?: AbortSignal) =>
  request<ExportEstimate>('POST', '/api/exports/estimate', exportBody(r), signal);
export const listExports = () => request<ExportInfo[]>('GET', '/api/exports');
export const deleteExport = (id: string) => request<void>('DELETE', `/api/exports/${encodeURIComponent(id)}`);
export const exportDownloadUrl = (id: string) => `/api/exports/${encodeURIComponent(id)}/download`;
/**
 * A one-use, 60 s download URL (docs/api.md): a browser download can't send the
 * X-Woodshed-Session header, so the ticket stands in for it. Ask for a new one each time.
 */
export const exportTicket = (id: string) =>
  request<{ url: string }>('POST', `/api/exports/${encodeURIComponent(id)}/ticket`);

/** Settings with the ETag they were read or saved at ("" if the server sent none). */
export interface VersionedSettings {
  settings: Settings;
  etag: string;
}

export async function getSettings(): Promise<VersionedSettings> {
  const { data, headers } = await requestFull<Settings>('GET', '/api/settings');
  return { settings: data, etag: headers.get('ETag') ?? '' };
}

/**
 * Saves the whole object with If-Match: the ETag it was read at. A stale ETag gets a 409
 * (isConflict). `restarting`: the UI server restarts at a new address just after this reply.
 */
export async function putSettings(s: Settings, etag: string): Promise<VersionedSettings & { restarting: boolean }> {
  const { data, headers } = await requestFull<Settings & { restarting?: boolean }>('PUT', '/api/settings', s, etag ? { 'If-Match': etag } : undefined);
  const { restarting, ...settings } = data;
  return { settings, etag: headers.get('ETag') ?? '', restarting: restarting === true };
}

/** A save refused because the settings changed since they were read (409). */
export const isConflict = (e: unknown) => e instanceof ApiError && e.status === 409;
export const getUpdates = () => request<UpdateInfo>('GET', '/api/updates');
/** The banner's Turn on / No thanks, and the Settings switch. */
export const putUpdates = (check: 'on' | 'off') => request<UpdateInfo>('PUT', '/api/updates', { check });
/** Check now: 409 unless the check is on, 429 (retryAfter) within a minute of the last one. */
export const checkUpdatesNow = () => request<void>('POST', '/api/updates/check');
/** Hide the update_available banner for this version, for everyone. */
export const dismissUpdate = (version: string) => request<void>('POST', '/api/updates/dismiss', { version });
export const getStorageHistory = () => request<StorageHistoryEntry[]>('GET', '/api/storage/history');

export interface StorageEstimate {
  estimated_days: number | null;
  bytes_per_day: number | null;
  compression_ratio: number | null;
}
/** The retention estimate for a cap that hasn't been saved yet (docs/api.md). */
export const getStorageEstimate = (retentionBytes: number, signal?: AbortSignal) =>
  request<StorageEstimate>('GET', `/api/storage/estimate${query({ retention_bytes: Math.round(retentionBytes) })}`, undefined, signal);

export interface LiveHandlers {
  onMessage(line: LogLine): void;
  onSampled?(n: SampledNotice): void;
  onState?(state: 'open' | 'error' | 'closed'): void;
}

/** First reconnect delay; doubles after each failure up to LIVE_BACKOFF_MAX (with jitter). */
const LIVE_BACKOFF_MIN = 1000;
const LIVE_BACKOFF_MAX = 30_000;
/** docs/api.md: a heartbeat every 15 s, so 45 s with no bytes at all means the stream is dead. */
const LIVE_IDLE_MS = 45_000;

/**
 * Opens the live tail over fetch() (EventSource can't send the session header), parses the
 * SSE stream, and reconnects with backoff after errors or a dropped stream. A 401 ends it
 * and shows the login. Returns a close function.
 */
export function openLive(filters: LiveFilters, h: LiveHandlers): () => void {
  const path = `/api/live${query({
    devices: filters.devices,
    group: filters.group,
    ungrouped: filters.ungrouped ? true : undefined,
    severities: filters.severities ? sevParam(filters.severities) : undefined,
    q: filters.q,
  })}`;
  let closed = false;
  let ctrl: AbortController | null = null;
  let backoff = LIVE_BACKOFF_MIN;
  let timer = 0;

  const dispatch = (e: SseEvent) => {
    try {
      if (e.event === 'message') h.onMessage(JSON.parse(e.data) as LogLine);
      else if (e.event === 'sampled') h.onSampled?.(JSON.parse(e.data) as SampledNotice);
    } catch {
      /* skip a malformed event rather than stop the tail */
    }
  };

  const retryLater = () => {
    if (closed) return;
    h.onState?.('error');
    const wait = Math.min(LIVE_BACKOFF_MAX, backoff) * (0.8 + Math.random() * 0.4);
    backoff = Math.min(LIVE_BACKOFF_MAX, backoff * 2);
    timer = window.setTimeout(connect, wait);
  };

  async function connect() {
    if (closed) return;
    ctrl = new AbortController();
    const my = ctrl;
    let idle = 0;
    const bump = () => {
      clearTimeout(idle);
      idle = window.setTimeout(() => my.abort(), LIVE_IDLE_MS);
    };
    try {
      bump();
      const res = await transport.fetch(path, {
        headers: { Accept: 'text/event-stream', ...authHeaders() },
        cache: 'no-store',
        signal: my.signal,
      });
      if (res.status === 401) {
        clearTimeout(idle);
        h.onState?.('closed');
        unauthorized();
        return;
      }
      if (!res.ok || !res.body) throw new Error(`live tail: ${res.status}`);
      h.onState?.('open');
      backoff = LIVE_BACKOFF_MIN;
      const parser = new SseParser(dispatch);
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        bump();
        parser.push(dec.decode(value, { stream: true }));
      }
      parser.push(dec.decode());
      if (parser.retry !== null) backoff = Math.max(LIVE_BACKOFF_MIN, parser.retry);
      clearTimeout(idle);
      retryLater(); // the server ended the stream (restart, timeout): reconnect
    } catch (e) {
      clearTimeout(idle);
      if (closed) return;
      retryLater();
      void e;
    }
  }

  connect();
  return () => {
    closed = true;
    clearTimeout(timer);
    ctrl?.abort();
  };
}
