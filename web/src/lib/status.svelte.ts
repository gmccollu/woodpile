// Shared, polled server state: /api/status for the banners on every page, and
// the device list (names for log lines, first-run detection).
import { ApiError, getDevices, getStatus, type Device, type Status } from './api';
import { auth } from './session.svelte';

export const live = $state<{
  status: Status | null;
  statusError: string;
  devices: Device[] | null;
  now: number;
}>({ status: null, statusError: '', devices: null, now: Date.now() });

export async function refreshStatus(): Promise<void> {
  if (auth.needLogin) return;
  try {
    live.status = await getStatus();
    live.statusError = '';
  } catch (e) {
    // A 401 shows the login form instead of an "unreachable" banner.
    if (!(e instanceof ApiError && e.status === 401)) live.statusError = (e as Error).message;
  }
}

export async function refreshDevices(): Promise<void> {
  if (auth.needLogin) return;
  try {
    live.devices = await getDevices();
  } catch {
    /* keep the last list; pages show their own errors */
  }
}

/** Updates one device in the shared list after a PATCH. */
export function replaceDevice(d: Device): void {
  if (!live.devices) return;
  live.devices = live.devices.map((x) => (x.id === d.id ? d : x));
}

export function deviceName(id: string): string {
  const d = live.devices?.find((x) => x.id === id);
  return d?.name || id;
}

/** How often /api/devices is polled. */
export const DEVICE_POLL_MS = 5000;
/** last_heard is live when fetched, so anything within one poll (plus a margin) reads as "now". */
export const NOW_WITHIN_S = DEVICE_POLL_MS / 1000 + 2;

/** The device heard from first (earliest first_seen), for the Welcome page. */
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
  setInterval(refreshDevices, DEVICE_POLL_MS);
  setInterval(() => (live.now = Date.now()), 1000);
}
