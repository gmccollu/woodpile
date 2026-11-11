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

let started = false;
export function startPolling(): void {
  if (started) return;
  started = true;
  refreshStatus();
  refreshDevices();
  setInterval(refreshStatus, 5000);
  setInterval(refreshDevices, 5000);
  setInterval(() => (live.now = Date.now()), 1000);
}
