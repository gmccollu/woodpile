// Shared, polled server state: /api/status for the banners on every page, and a
// snapshot of the busiest devices for names, groups and pickers.
//
// /api/devices is paginated (up to 10,000+ devices), so nothing here holds the whole
// fleet. Pages that list devices (dashboard strip, Devices page, pickers) ask the server
// for exactly what they show. Names for log lines come from a cache filled by every
// device response, and unknown ids are looked up a few at a time.
import { SvelteMap } from 'svelte/reactivity';
import { ApiError, getDevices, getStatus, type Device, type Status } from './api';
import { auth } from './session.svelte';

/** Devices kept in the snapshot (the busiest, hidden ones left out). */
const SNAPSHOT = 1000;

export const live = $state<{
  status: Status | null;
  statusError: string;
  /** The busiest visible devices (at most SNAPSHOT), or null before the first fetch. */
  devices: Device[] | null;
  /** All visible (not hidden) devices on the server. */
  deviceTotal: number;
  now: number;
}>({ status: null, statusError: '', devices: null, deviceTotal: 0, now: Date.now() });

/** id → device, from every device response seen. Reactive, so names update in place. */
const known = new SvelteMap<string, Device>();

export function remember(devs: Device[]): void {
  for (const d of devs) {
    const old = known.get(d.id);
    if (!old || old.name !== d.name || old.group !== d.group || old.hidden !== d.hidden) known.set(d.id, d);
  }
}

export async function refreshStatus(): Promise<void> {
  if (auth.needLogin || auth.movedTo) return;
  try {
    live.status = await getStatus();
    live.statusError = '';
  } catch (e) {
    // A 401 shows the login form instead of an "unreachable" banner.
    if (!(e instanceof ApiError && e.status === 401)) live.statusError = (e as Error).message;
  }
}

export async function refreshDevices(): Promise<void> {
  if (auth.needLogin || auth.movedTo) return;
  try {
    const page = await getDevices({ sort: 'rate', limit: SNAPSHOT, hidden: 'exclude' });
    remember(page.devices);
    live.devices = page.devices;
    live.deviceTotal = page.total;
  } catch {
    /* keep the last snapshot; pages show their own errors */
  }
}

/** Updates one device everywhere after a PATCH. */
export function replaceDevice(d: Device): void {
  known.set(d.id, d);
  if (live.devices) live.devices = live.devices.map((x) => (x.id === d.id ? d : x));
}

// Unknown ids (a quiet device in the tail, a hidden one in search) are looked up by id,
// a few at a time and each only once. The queue is capped, so a tail full of new senders
// (10,000 devices) can't turn into thousands of requests; skipped ids are tried again later.
const QUEUE_MAX = 50;
const asked = new Set<string>();
const queue: string[] = [];
let draining = false;

async function drain() {
  if (draining) return;
  draining = true;
  try {
    while (queue.length) {
      const batch = queue.splice(0, 4);
      await Promise.all(
        batch.map(async (id) => {
          try {
            remember((await getDevices({ q: id, limit: 5, hidden: 'include' })).devices);
          } catch {
            /* keep showing the id */
          }
        }),
      );
      await new Promise((r) => setTimeout(r, 500));
    }
  } finally {
    draining = false;
  }
}

export function deviceName(id: string): string {
  const d = known.get(id);
  if (d) return d.name || id;
  if (!asked.has(id) && live.devices !== null && !auth.needLogin && queue.length < QUEUE_MAX) {
    asked.add(id);
    queue.push(id);
    queueMicrotask(drain);
  }
  return id;
}

/** Group names seen so far (the API has no group list; see the report). */
export function groupsSeen(): string[] {
  const g = new Set<string>();
  for (const d of known.values()) if (d.group && !d.hidden) g.add(d.group);
  return [...g].sort();
}

export function anyUngrouped(): boolean {
  for (const d of known.values()) if (!d.group && !d.hidden) return true;
  return false;
}

/** Every visible device id, paging through the server (exports of "all devices"). */
export async function allVisibleIds(): Promise<string[]> {
  const ids: string[] = [];
  for (let offset = 0; ; offset += 1000) {
    const page = await getDevices({ sort: 'name', limit: 1000, offset, hidden: 'exclude' });
    remember(page.devices);
    ids.push(...page.devices.map((d) => d.id));
    if (!page.devices.length || ids.length >= page.total) return ids;
  }
}

/** How often the device views poll. */
export const DEVICE_POLL_MS = 5000;
/** last_heard is live when fetched, so anything within one poll (plus a margin) reads as "now". */
export const NOW_WITHIN_S = DEVICE_POLL_MS / 1000 + 2;

/** The device heard from first (earliest first_seen) in the snapshot, for the Welcome page. */
export function firstDevice(): Device | undefined {
  return [...(live.devices ?? [])].sort((a, b) => Date.parse(a.first_seen) - Date.parse(b.first_seen))[0];
}

let started = false;
export function startPolling(): void {
  if (started) return;
  started = true;
  refreshStatus();
  refreshDevices();
  setInterval(refreshStatus, 5000);
  setInterval(refreshDevices, 15_000);
  setInterval(() => (live.now = Date.now()), 1000);
}
