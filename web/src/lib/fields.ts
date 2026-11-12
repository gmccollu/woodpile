// The Events page's program/tag, host and exclude filters (docs/api.md → Search), and the
// link the live tail opens Events with. Plain functions with no imports, so `npm test` can
// run them under Node.

/** The filters as typed: app and host are comma-separated lists, exclude is text. */
export interface FieldFilters {
  app: string;
  host: string;
  exclude: string;
}

export const noFields = (): FieldFilters => ({ app: '', host: '', exclude: '' });

/** "sshd, %FW-6-*,," → ["sshd", "%FW-6-*"]: trimmed, blanks dropped (as the server does). */
export function splitList(s: string): string[] {
  return s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);
}

/**
 * Adds v to a comma-separated list unless it's already there (ignoring case, and a leading
 * "%", as the filter does). Returns the list in the "a, b" form the inputs show.
 */
export function addToList(list: string, v: string): string {
  v = v.trim();
  const items = splitList(list);
  if (!v) return items.join(', ');
  const key = (x: string) => x.replace(/^%/, '').toLowerCase();
  if (!items.some((x) => key(x) === key(v))) items.push(v);
  return items.join(', ');
}

/** URL form of the page (#/events?app=…&host=…&ex=…): only what's set. */
export function fieldsToQuery(f: FieldFilters, q: URLSearchParams): void {
  const app = splitList(f.app).join(',');
  const host = splitList(f.host).join(',');
  if (app) q.set('app', app);
  if (host) q.set('host', host);
  if (f.exclude) q.set('ex', f.exclude);
}

export function fieldsFromQuery(q: URLSearchParams): FieldFilters {
  return {
    app: splitList(q.get('app') ?? '').join(', '),
    host: splitList(q.get('host') ?? '').join(', '),
    exclude: q.get('ex') ?? '',
  };
}

/** What the live tail is showing: its devices, group or ungrouped, severities and text. */
export interface TailView {
  devices: string[];
  group: string;
  ungrouped: boolean;
  /** The severity keys selected; all nine (0-7 and "unknown") means every severity. */
  severities: (number | 'unknown')[];
  q: string;
}

const SEV_KEYS: (number | 'unknown')[] = [0, 1, 2, 3, 4, 5, 6, 7, 'unknown'];

/**
 * The Events query for "Open in Events": the tail's devices (or group, or ungrouped), its
 * severities and text, over the last hour. Same keys as the Events page's own URL.
 */
export function eventsQueryFor(v: TailView): string {
  const p = new URLSearchParams();
  const q = v.q.trim();
  if (q) p.set('q', q);
  if (v.devices.length) p.set('dev', v.devices.join(','));
  else if (v.group) p.set('group', v.group);
  else if (v.ungrouped) p.set('ug', '1');
  p.set('r', '1h');
  const sel = SEV_KEYS.filter((k) => v.severities.includes(k));
  if (sel.length !== SEV_KEYS.length) p.set('sev', sel.length ? sel.join(',') : 'none');
  return p.toString();
}
