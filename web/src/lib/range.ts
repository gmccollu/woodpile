// Time ranges for search and export: a quick pick relative to now, or a custom from/to.

export type Preset = '15m' | '1h' | '24h' | '7d' | 'custom';
export const PRESETS: { p: Exclude<Preset, 'custom'>; label: string; ms: number }[] = [
  { p: '15m', label: '15 min', ms: 15 * 60_000 },
  { p: '1h', label: '1 h', ms: 3_600_000 },
  { p: '24h', label: '24 h', ms: 86_400_000 },
  { p: '7d', label: '7 d', ms: 7 * 86_400_000 },
];

export interface Range {
  preset: Preset;
  /** RFC 3339, used when preset is "custom". */
  from: string;
  to: string;
}

export const defaultRange = (): Range => ({ preset: '1h', from: '', to: '' });

/**
 * The concrete from/to. Quick picks are resolved against now, once per search, ending at now
 * rounded up to the whole minute: the histogram's buckets then line up with the per-minute
 * stats, so an unfiltered view's histogram comes from the index (SPEC §10.1.2).
 */
export function resolveRange(r: Range, now = Date.now()): { from: string; to: string } {
  const pre = PRESETS.find((x) => x.p === r.preset);
  if (pre) {
    const to = Math.ceil(now / 60_000) * 60_000;
    return { from: new Date(to - pre.ms).toISOString(), to: new Date(to).toISOString() };
  }
  return { from: r.from, to: r.to };
}

const pad = (n: number) => String(n).padStart(2, '0');

/** RFC 3339 → the value of an <input type=datetime-local>, in local time or UTC. */
export function toInput(iso: string, utc: boolean): string {
  const d = new Date(iso);
  if (!iso || Number.isNaN(d.getTime())) return '';
  return utc
    ? `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}T${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`
    : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function fromInput(v: string, utc: boolean): string {
  if (!v) return '';
  const t = utc ? Date.parse(`${v.length === 16 ? `${v}:00` : v}Z`) : new Date(v).getTime();
  return Number.isNaN(t) ? '' : new Date(t).toISOString();
}

export function rangeToQuery(r: Range, q: URLSearchParams): void {
  q.set('r', r.preset);
  if (r.preset === 'custom') {
    if (r.from) q.set('from', r.from);
    if (r.to) q.set('to', r.to);
  }
}

export function rangeFromQuery(q: URLSearchParams): Range {
  const p = q.get('r') as Preset | null;
  if (q.get('from') || q.get('to')) return { preset: 'custom', from: q.get('from') ?? '', to: q.get('to') ?? '' };
  return { preset: p && (p === 'custom' || PRESETS.some((x) => x.p === p)) ? p : '1h', from: '', to: '' };
}
