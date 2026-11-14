// Events v2 helpers with no Svelte state, so `npm test` can run them under Node: the text
// mode switch and the 1.1 filters in the page URL, the field panel's include/exclude rules,
// histogram drag snapping, and the saved-search form <-> query mapping (docs/api.md, 1.1).

import type { FacKey, SavedQuery, SevKey } from './api';
import { PRESETS, type Range } from './range.ts';
import { splitList } from './fields.ts';

/** Text | Terms | Regex (SPEC §10.1.3). In the page URL: nothing, syn=terms or re=1. */
export type Mode = 'text' | 'terms' | 'regex';

export function modeToQuery(m: Mode, q: URLSearchParams): void {
  if (m === 'regex') q.set('re', '1');
  else if (m === 'terms') q.set('syn', 'terms');
}

export function modeFromQuery(q: URLSearchParams): Mode {
  if (q.get('re') === '1') return 'regex';
  return q.get('syn') === 'terms' ? 'terms' : 'text';
}

/**
 * The 1.1 filters that only the field panel sets: facilities ([] = all, ["none"] = nothing)
 * and the exclude forms of device, program/tag and host.
 */
export interface Extras {
  fac: string[];
  xdev: string[];
  xapp: string[];
  xhost: string[];
}

export const noExtras = (): Extras => ({ fac: [], xdev: [], xapp: [], xhost: [] });

export function extrasToQuery(e: Extras, q: URLSearchParams): void {
  if (e.fac.length) q.set('fac', e.fac.join(','));
  if (e.xdev.length) q.set('xdev', e.xdev.join(','));
  if (e.xapp.length) q.set('xapp', e.xapp.join(','));
  if (e.xhost.length) q.set('xhost', e.xhost.join(','));
}

export function extrasFromQuery(q: URLSearchParams): Extras {
  const l = (k: string) => splitList(q.get(k) ?? '');
  return { fac: l('fac'), xdev: l('xdev'), xapp: l('xapp'), xhost: l('xhost') };
}

export const hasExtras = (e: Extras) => !!(e.fac.length || e.xdev.length || e.xapp.length || e.xhost.length);

/** The facilities parameter's list form: numbers, "unknown" and "none". */
export const facKeys = (fac: string[]): FacKey[] => fac.map((v) => (/^\d+$/.test(v) ? Number(v) : (v as FacKey)));

/** Adds v to a list unless it's there (by key); returns a new list. */
export function addItem(list: string[], v: string, key: (x: string) => string = (x) => x): string[] {
  return list.some((x) => key(x) === key(v)) ? list : [...list, v];
}

export function removeItem(list: string[], v: string, key: (x: string) => string = (x) => x): string[] {
  return list.filter((x) => key(x) !== key(v));
}

/** How the program/tag and host filters compare values: ignoring case, and a leading "%" for tags. */
export const appKey = (x: string) => x.replace(/^%/, '').toLowerCase();
export const hostKey = (x: string) => x.toLowerCase();

/**
 * A closed field (severity, facility): `selected` is the parameter's list ([] = every value,
 * ["none"] = nothing). Including a value narrows "every value" to it, or adds it.
 */
export function domainInclude(selected: string[], v: string): string[] {
  if (!selected.length || selected.includes('none')) return [v];
  return selected.includes(v) ? selected : [...selected, v];
}

/** Excluding a value of a closed field sends every other value of the domain. */
export function domainExclude(selected: string[], domain: string[], v: string): string[] {
  const base = !selected.length ? domain : selected.filter((x) => x !== 'none');
  const out = domain.filter((x) => base.includes(x) && x !== v);
  if (!out.length) return ['none'];
  return out.length === domain.length ? [] : out;
}

/** Undoes one value's part of a closed-field filter (an active chip clicked away). */
export function domainRemove(selected: string[], domain: string[], v: string, wasIncluded: boolean): string[] {
  if (wasIncluded) {
    const out = selected.filter((x) => x !== v && x !== 'none');
    return out.length ? out : [];
  }
  const out = domain.filter((x) => selected.includes(x) || x === v);
  return out.length === domain.length ? [] : out;
}

/**
 * The active chips of a closed field: with most values selected, one "not" chip per left-out
 * value; otherwise one chip per selected value. [] when there's no filter.
 */
export function domainChips(selected: string[], domain: string[]): { value: string; included: boolean }[] {
  if (!selected.length) return [];
  if (selected.includes('none')) return domain.map((value) => ({ value, included: false }));
  const out = domain.filter((x) => !selected.includes(x));
  if (out.length < selected.length) return out.map((value) => ({ value, included: false }));
  return domain.filter((x) => selected.includes(x)).map((value) => ({ value, included: true }));
}

/**
 * A drag across histogram buckets i0..i1 (either order) as a range: snapped to the buckets'
 * edges and kept inside the range on show (the first and last buckets may stick out of it).
 */
export function snapSelection(
  starts: string[],
  bucketSeconds: number,
  i0: number,
  i1: number,
  rangeFrom: string,
  rangeTo: string,
): { from: string; to: string } {
  const a = Math.max(0, Math.min(i0, i1));
  const b = Math.min(starts.length - 1, Math.max(i0, i1));
  const from = Math.max(Date.parse(starts[a]), Date.parse(rangeFrom));
  const to = Math.min(Date.parse(starts[b]) + bucketSeconds * 1000, Date.parse(rangeTo));
  return { from: new Date(from).toISOString(), to: new Date(to).toISOString() };
}

const pad = (n: number) => String(n).padStart(2, '0');

/** "14:02" (or "11-12 14:02" when the span crosses days), in local time or UTC. */
function hm(t: number, withDay: boolean, utc: boolean): string {
  const d = new Date(t);
  const h = utc ? d.getUTCHours() : d.getHours();
  const m = utc ? d.getUTCMinutes() : d.getMinutes();
  const s = utc ? d.getUTCSeconds() : d.getSeconds();
  const day = utc ? `${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ` : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} `;
  return `${withDay ? day : ''}${pad(h)}:${pad(m)}${s ? `:${pad(s)}` : ''}`;
}

/** "14:02–15:00", or with the day when the two ends are on different days (or far apart). */
export function span(fromIso: string, toIso: string, utc = false, withDay?: boolean): string {
  const a = Date.parse(fromIso);
  const b = Date.parse(toIso);
  const day = (t: number) => (utc ? new Date(t).toISOString().slice(0, 10) : new Date(t).toDateString());
  const wd = withDay ?? (day(a) !== day(b - 1) || b - a > 86_400_000);
  return `${hm(a, wd, utc)}–${hm(b, wd, utc)}`;
}

/** "Counted 14:02–15:00 of 12:00–15:00": what the panel's counts cover (SPEC §10.1.1). */
export function countedLine(from: string, to: string, countedFrom: string, utc = false): string {
  const a = Date.parse(from);
  const b = Date.parse(to);
  const c = Date.parse(countedFrom);
  const wd = new Date(a).toDateString() !== new Date(b - 1).toDateString() || b - a > 86_400_000;
  if (c <= a) return `Counted all of ${span(from, to, utc, wd)}`;
  return `Counted ${span(countedFrom, to, utc, wd)} of ${span(from, to, utc, wd)}`;
}

/** An active filter shown in the field panel; clicking it removes that filter. */
export interface Chip {
  key: string;
  text: string;
  title: string;
  excluded: boolean;
  remove: () => void;
}

// ----- saved searches -----

/** The Events form, as a saved search sees it (display settings such as the raw view aren't saved). */
export interface EventsForm {
  q: string;
  mode: Mode;
  cs: boolean;
  scope: { mode: 'all' | 'group' | 'ungrouped' | 'devices'; group: string; devices: string[] };
  range: Range;
  sev: SevKey[];
  order: 'desc' | 'asc';
  /** Comma-separated, as typed. */
  app: string;
  host: string;
  exclude: string;
  extras: Extras;
}

const ALL_SEV: SevKey[] = [0, 1, 2, 3, 4, 5, 6, 7, 'unknown'];

/** A saved search's query for the form. Preset ranges save as relative ({"last": "1h"}). */
export function savedQueryOf(f: EventsForm): Required<SavedQuery> {
  const sev = ALL_SEV.filter((k) => f.sev.includes(k));
  return {
    q: f.q,
    syntax: f.mode === 'terms' ? 'terms' : 'text',
    regex: f.mode === 'regex',
    case: f.cs,
    exclude: f.exclude,
    devices: f.scope.mode === 'devices' ? [...f.scope.devices] : [],
    group: f.scope.mode === 'group' ? f.scope.group : '',
    ungrouped: f.scope.mode === 'ungrouped',
    exclude_devices: [...f.extras.xdev],
    severities: sev.length === ALL_SEV.length ? [] : sev.length ? sev : ['none'],
    facilities: facKeys(f.extras.fac),
    app: splitList(f.app),
    exclude_app: [...f.extras.xapp],
    host: splitList(f.host),
    exclude_host: [...f.extras.xhost],
    order: f.order,
    range: f.range.preset === 'custom' ? { from: f.range.from, to: f.range.to } : { last: f.range.preset },
  };
}

const LAST_MS: Record<string, number> = { m: 60_000, h: 3_600_000, d: 86_400_000 };

/**
 * The form for a saved query (missing keys take their defaults). A relative range that isn't
 * one of the presets opens as a custom range ending now.
 */
export function formOfSaved(q: SavedQuery, now = Date.now()): EventsForm {
  const devices = q.devices ?? [];
  const sevs = q.severities ?? [];
  let range: Range = { preset: '1h', from: '', to: '' };
  const r = q.range;
  if (r && 'last' in r) {
    const pre = PRESETS.find((x) => x.p === r.last);
    const m = /^(\d+)([mhd])$/.exec(r.last);
    if (pre) range = { preset: pre.p, from: '', to: '' };
    else if (m) range = { preset: 'custom', from: new Date(now - Number(m[1]) * LAST_MS[m[2]]).toISOString(), to: new Date(now).toISOString() };
  } else if (r && 'from' in r) range = { preset: 'custom', from: r.from, to: r.to };
  return {
    q: q.q ?? '',
    mode: q.regex ? 'regex' : q.syntax === 'terms' ? 'terms' : 'text',
    cs: !!q.case,
    scope: devices.length
      ? { mode: 'devices', group: '', devices: [...devices] }
      : q.group
        ? { mode: 'group', group: q.group, devices: [] }
        : q.ungrouped
          ? { mode: 'ungrouped', group: '', devices: [] }
          : { mode: 'all', group: '', devices: [] },
    range,
    sev: !sevs.length ? [...ALL_SEV] : sevs.includes('none') ? [] : ALL_SEV.filter((k) => sevs.includes(k)),
    order: q.order === 'asc' ? 'asc' : 'desc',
    app: (q.app ?? []).join(', '),
    host: (q.host ?? []).join(', '),
    exclude: q.exclude ?? '',
    extras: {
      fac: (q.facilities ?? []).map(String),
      xdev: [...(q.exclude_devices ?? [])],
      xapp: [...(q.exclude_app ?? [])],
      xhost: [...(q.exclude_host ?? [])],
    },
  };
}

/** Whether the form differs from a saved query (both read through the same defaults). */
export function differsFromSaved(f: EventsForm, saved: SavedQuery): boolean {
  const norm = (x: Required<SavedQuery>) => JSON.stringify({ ...x, devices: [...x.devices].sort() });
  return norm(savedQueryOf(f)) !== norm(savedQueryOf(formOfSaved(saved)));
}
