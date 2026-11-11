// Small display helpers. Numbers use the browser's locale; times are shown in local time.

const nf = new Intl.NumberFormat();
const nf1 = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });

export const num = (n: number) => nf.format(Math.round(n));
export const num1 = (n: number) => nf1.format(n);

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

/** Binary units, because the spec's limits (100 MiB parts, 20 GiB cap) are powers of two. */
export function bytes(n: number): string {
  let i = 0;
  let v = n;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : nf1.format(v)} ${UNITS[i]}`;
}

export const GiB = 1024 ** 3;
export const MiB = 1024 ** 2;

/**
 * "now", "12 s ago", "6 min ago", "2 h ago", "3 d ago". `nowWithin` (seconds) widens "now":
 * a value refreshed by polling is up to one poll interval old by the time it's shown.
 */
export function ago(iso: string, now = Date.now(), nowWithin = 2): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '—';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < nowWithin) return 'now';
  if (s < 60) return `${s} s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.floor(h / 24)} d ago`;
}

/** Message rate per device: msgs/s when at least 1/s, else msgs/min. */
export function rate(msgsPerMin: number): string {
  if (msgsPerMin >= 60) return `${num(msgsPerMin / 60)}/s`;
  return `${num(msgsPerMin)}/min`;
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0');

/** HH:MM:SS.mmm in local time, for log lines. */
export function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

export function dateTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : dtf.format(d);
}

export function duration(fromIso: string, now = Date.now()): string {
  const s = Math.max(0, Math.floor((now - Date.parse(fromIso)) / 1000));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d} d ${h} h`;
  if (h) return `${h} h ${m} min`;
  return `${m} min`;
}

const df = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' });

export function date(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : df.format(d);
}
