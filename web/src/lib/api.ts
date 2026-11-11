// Typed wrappers for the Woodpile HTTP API (docs/api.md). The UI talks to the
// server only through this module. In mock mode (VITE_MOCK=1) main.ts swaps the
// transport for the in-browser fake in ./mock, and nothing else changes.

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
}

export type BannerLevel = 'error' | 'warn' | 'info';

export type BannerKind = 'listener_bind' | 'writes_paused' | 'low_disk';
export type BannerAction = '' | 'retry_listeners';

export interface Banner {
  /** Unknown future kinds are shown like any other banner. */
  kind: BannerKind | (string & {});
  level: BannerLevel;
  text: string;
  action: BannerAction | (string & {});
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
  disk: { used_bytes: number; cap_bytes: number; free_bytes: number; days_kept: number };
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
}

export type DeviceStatus = 'online' | 'quiet' | 'offline';

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
}

export type DevicePatch = Partial<Pick<Device, 'name' | 'group' | 'hidden'>>;

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
 * The `severities=` value (docs/api.md → Severity filters). All selected → undefined, which
 * the server reads as "all". The caller must not send an empty set: the API reads that as
 * "all" too, so an empty selection has to be handled in the UI (show nothing).
 */
export function sevParam(sel: SevKey[]): string | undefined {
  if (!sel.length || ALL_SEVS.every((k) => sel.includes(k))) return undefined;
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
}

export interface SearchResult extends LogLine {
  ref: string;
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
}

export interface ExportRequest {
  devices: string[];
  from: string;
  to: string;
  severities?: SevKey[];
  q?: string;
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
  /** Not in the contract yet (see the report); used for the expiry countdown when present. */
  ready_at?: string;
}

export interface ListenerSettings {
  enabled: boolean;
  addr: string;
  port: number;
}

export interface Settings {
  listeners: { udp: ListenerSettings; tcp: ListenerSettings; allowlist: string[] };
  storage: { data_dir: string; retention_days: number; retention_bytes: number; part_bytes: number };
  /** cert_file/key_file: paths to a user certificate for LAN mode; empty = self-signed. */
  ui: { lan_mode: boolean; port: number; cert_file: string; key_file: string };
  quiet: { quiet_after_seconds: number; offline_after_seconds: number };
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
  /** "" when not logged in, and always "" in localhost mode. */
  csrf_token: string;
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

// ---------- Session: CSRF token and 401 handling ----------

let csrfToken = '';
let onUnauthorized: (() => void) | null = null;

/** The token sent as X-CSRF-Token on every POST, PUT, PATCH and DELETE. */
export function setCsrfToken(t: string): void {
  csrfToken = t;
}

/** Called on any 401 from a route that needs a session, so the app can show the login form. */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

const SESSION_ROUTES = ['/api/session', '/api/login', '/api/logout'];

// ---------- Transport ----------

/** The minimum of EventSource that the live tail uses. */
export interface EventStream {
  addEventListener(type: 'message' | 'sampled', fn: (e: MessageEvent<string>) => void): void;
  onerror: ((e: Event) => void) | null;
  /** 2 (CLOSED) when the browser gave up, e.g. after a 401: it won't reconnect by itself. */
  readonly readyState: number;
  onopen: ((e: Event) => void) | null;
  close(): void;
}

export interface Transport {
  fetch(path: string, init?: RequestInit): Promise<Response>;
  eventSource(path: string): EventStream;
  /** Mock mode only: there's no server behind a plain download link. */
  download?(path: string): Promise<void>;
}

const browserTransport: Transport = {
  fetch: (path, init) => fetch(path, { credentials: 'same-origin', ...init }),
  eventSource: (path) => new EventSource(path) as unknown as EventStream,
};

let transport: Transport = browserTransport;

/** True when a download link must go through the transport (mock mode). */
export const downloadsViaTransport = () => !!transport.download;
export const transportDownload = (path: string) => transport.download?.(path);

export function setTransport(t: Transport): void {
  transport = t;
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const init: RequestInit = { method, headers, signal };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    headers['Content-Type'] = 'application/json';
  }
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  let res: Response;
  try {
    res = await transport.fetch(path, init);
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e;
    throw new ApiError(0, `no connection: ${(e as Error).message}`);
  }
  if (res.status === 204) return undefined as T;
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
    if (res.status === 401 && !SESSION_ROUTES.includes(path.split('?')[0])) onUnauthorized?.();
    throw new ApiError(res.status, msg, fields, retryAfterSeconds(res.headers.get('Retry-After')));
  }
  return data as T;
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
export const login = (password: string) => request<Session>('POST', '/api/login', { password });
export const logout = () => request<void>('POST', '/api/logout');
export const putPassword = (current: string, next: string) =>
  request<void>('PUT', '/api/password', { current, new: next });

export const getStatus = () => request<Status>('GET', '/api/status');
export const retryListeners = () => request<void>('POST', '/api/listeners/retry');
export const getStats = () => request<Stats>('GET', '/api/stats');
export const getRejected = () => request<RejectedSender[]>('GET', '/api/rejected');

export const getDevices = () => request<Device[]>('GET', '/api/devices');
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
    order: p.order,
    limit: p.limit,
    cursor: p.cursor,
  })}`;
export const search = (p: SearchParams, signal?: AbortSignal) =>
  request<SearchResponse>('GET', searchPath(p), undefined, signal);
export const getContext = (ref: string, before = 50, after = 50) =>
  request<ContextResponse>('GET', `/api/context${query({ ref, before, after })}`);

/** Severities are sent only when they narrow the export (all = no filter). */
function exportBody(r: ExportRequest): ExportRequest {
  const b: ExportRequest = { devices: r.devices, from: r.from, to: r.to };
  if (r.severities && sevParam(r.severities)) b.severities = ALL_SEVS.filter((k) => r.severities!.includes(k));
  if (r.q) b.q = r.q;
  return b;
}
export const createExport = (r: ExportRequest) => request<{ id: string }>('POST', '/api/exports', exportBody(r));
export const estimateExport = (r: ExportRequest, signal?: AbortSignal) =>
  request<ExportEstimate>('POST', '/api/exports/estimate', exportBody(r), signal);
export const listExports = () => request<ExportInfo[]>('GET', '/api/exports');
export const deleteExport = (id: string) => request<void>('DELETE', `/api/exports/${encodeURIComponent(id)}`);
export const exportDownloadUrl = (id: string) => `/api/exports/${encodeURIComponent(id)}/download`;

export const getSettings = () => request<Settings>('GET', '/api/settings');
export const putSettings = (s: Settings) => request<Settings>('PUT', '/api/settings', s);
export const getStorageHistory = () => request<StorageHistoryEntry[]>('GET', '/api/storage/history');

export interface LiveHandlers {
  onMessage(line: LogLine): void;
  onSampled?(n: SampledNotice): void;
  onState?(state: 'open' | 'error' | 'closed'): void;
}

/** Opens the live tail. EventSource reconnects by itself after errors. Returns a close function. */
export function openLive(filters: LiveFilters, h: LiveHandlers): () => void {
  const es = transport.eventSource(
    `/api/live${query({
      devices: filters.devices,
      group: filters.group,
      ungrouped: filters.ungrouped ? true : undefined,
      severities: filters.severities ? sevParam(filters.severities) : undefined,
      q: filters.q,
    })}`,
  );
  es.addEventListener('message', (e) => {
    try {
      h.onMessage(JSON.parse(e.data) as LogLine);
    } catch {
      /* skip a malformed event rather than stop the tail */
    }
  });
  es.addEventListener('sampled', (e) => {
    try {
      h.onSampled?.(JSON.parse(e.data) as SampledNotice);
    } catch {
      /* ignore */
    }
  });
  es.onopen = () => h.onState?.('open');
  es.onerror = () => h.onState?.(es.readyState === 2 ? 'closed' : 'error');
  return () => es.close();
}
