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

export interface Banner {
  level: BannerLevel;
  text: string;
}

export interface Status {
  version: string;
  build: string;
  os: string;
  data_dir: string;
  started_at: string;
  udp_rcvbuf_bytes: number;
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
  /** 0 emerg … 7 debug. The contract doesn't say how "unknown" is sent; see severity.ts. */
  severity: number | null;
  raw: string;
}

export interface SampledNotice {
  rate: number;
  shown: number;
}

export interface LiveFilters {
  devices?: string[];
  group?: string;
  /** Numeric severity ceiling: 4 means warning and worse. Unknown severities always pass. */
  min_severity?: number;
  q?: string;
}

export interface SearchParams {
  q: string;
  regex?: boolean;
  devices?: string[];
  group?: string;
  from?: string;
  to?: string;
  min_severity?: number;
  limit?: number;
  cursor?: string;
}

export interface SearchResult extends LogLine {
  ref: string;
}

export interface SearchResponse {
  results: SearchResult[];
  next_cursor: string;
  scanned_bytes: number;
  truncated: boolean;
}

export interface ContextResponse {
  lines: SearchResult[];
  hit_index: number;
}

export interface ExportRequest {
  devices: string[];
  from: string;
  to: string;
}

export type ExportState = 'building' | 'ready' | 'failed';

export interface ExportInfo {
  id: string;
  created: string;
  devices: string[];
  from: string;
  to: string;
  state: ExportState;
  size_bytes: number;
  error: string;
}

export interface ListenerSettings {
  enabled: boolean;
  addr: string;
  port: number;
}

export interface Settings {
  listeners: { udp: ListenerSettings; tcp: ListenerSettings; allowlist: string[] };
  storage: { data_dir: string; retention_days: number; retention_bytes: number; part_bytes: number };
  ui: { lan_mode: boolean; port: number };
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

export class ApiError extends Error {
  readonly status: number;
  /** Per-field messages from a 400 on PUT /api/settings, keyed by field path. */
  readonly fields: Record<string, string>;

  constructor(status: number, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.fields = fields;
  }
}

// ---------- Transport ----------

/** The minimum of EventSource that the live tail uses. */
export interface EventStream {
  addEventListener(type: 'message' | 'sampled', fn: (e: MessageEvent<string>) => void): void;
  onerror: ((e: Event) => void) | null;
  onopen: ((e: Event) => void) | null;
  close(): void;
}

export interface Transport {
  fetch(path: string, init?: RequestInit): Promise<Response>;
  eventSource(path: string): EventStream;
}

const browserTransport: Transport = {
  fetch: (path, init) => fetch(path, { credentials: 'same-origin', ...init }),
  eventSource: (path) => new EventSource(path) as unknown as EventStream,
};

let transport: Transport = browserTransport;

export function setTransport(t: Transport): void {
  transport = t;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, headers: { Accept: 'application/json' } };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    (init.headers as Record<string, string>)['Content-Type'] = 'application/json';
  }
  let res: Response;
  try {
    res = await transport.fetch(path, init);
  } catch (e) {
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
    throw new ApiError(res.status, msg, fields);
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

export const getStatus = () => request<Status>('GET', '/api/status');
export const retryListeners = () => request<void>('POST', '/api/listeners/retry');
export const getStats = () => request<Stats>('GET', '/api/stats');

export const getDevices = () => request<Device[]>('GET', '/api/devices');
export const patchDevice = (id: string, patch: DevicePatch) =>
  request<Device>('PATCH', `/api/devices/${encodeURIComponent(id)}`, patch);

export const search = (p: SearchParams) =>
  request<SearchResponse>(
    'GET',
    `/api/search${query({ ...p, regex: p.regex ? true : undefined })}`,
  );
export const getContext = (ref: string, before = 50, after = 50) =>
  request<ContextResponse>('GET', `/api/context${query({ ref, before, after })}`);

export const createExport = (r: ExportRequest) => request<{ id: string }>('POST', '/api/exports', r);
export const listExports = () => request<ExportInfo[]>('GET', '/api/exports');
export const exportDownloadUrl = (id: string) => `/api/exports/${encodeURIComponent(id)}/download`;

export const getSettings = () => request<Settings>('GET', '/api/settings');
export const putSettings = (s: Settings) => request<Settings>('PUT', '/api/settings', s);
export const getStorageHistory = () => request<StorageHistoryEntry[]>('GET', '/api/storage/history');

export interface LiveHandlers {
  onMessage(line: LogLine): void;
  onSampled?(n: SampledNotice): void;
  onState?(state: 'open' | 'error'): void;
}

/** Opens the live tail. EventSource reconnects by itself after errors. Returns a close function. */
export function openLive(filters: LiveFilters, h: LiveHandlers): () => void {
  const es = transport.eventSource(
    `/api/live${query({
      devices: filters.devices,
      group: filters.group,
      min_severity: filters.min_severity,
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
  es.onerror = () => h.onState?.('error');
  return () => es.close();
}
