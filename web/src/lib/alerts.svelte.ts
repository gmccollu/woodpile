// Banners the user dismissed. A dismissal hides a banner until its content changes (the key
// is kind + text), and a banner that goes away and comes back shows again. Warnings and
// notices stay hidden for the browser session (sessionStorage); errors only until the page
// is reloaded, so a real problem can't stay hidden by accident.
import type { Banner } from './api';

const KEY = 'woodshed.dismissed';

export const bannerKey = (b: Banner) => `${b.kind}\u0000${b.text}`;

function load(): string[] {
  try {
    const v = JSON.parse(sessionStorage.getItem(KEY) ?? '[]');
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

export const alerts = $state<{ dismissed: string[]; errors: string[] }>({ dismissed: load(), errors: [] });

function save() {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(alerts.dismissed));
  } catch {
    /* storage off: keep them in memory */
  }
}

export function dismiss(b: Banner): void {
  const k = bannerKey(b);
  if (b.level === 'error') {
    if (!alerts.errors.includes(k)) alerts.errors = [...alerts.errors, k];
  } else if (!alerts.dismissed.includes(k)) {
    alerts.dismissed = [...alerts.dismissed, k];
    save();
  }
}

export const isDismissed = (b: Banner) => alerts.dismissed.includes(bannerKey(b)) || alerts.errors.includes(bannerKey(b));

/** Forget dismissals of banners that are gone, so a new occurrence shows again. */
export function prune(current: Banner[]): void {
  const keys = new Set(current.map(bannerKey));
  const d = alerts.dismissed.filter((k) => keys.has(k));
  if (d.length !== alerts.dismissed.length) {
    alerts.dismissed = d;
    save();
  }
  const e = alerts.errors.filter((k) => keys.has(k));
  if (e.length !== alerts.errors.length) alerts.errors = e;
}

export function restoreAll(): void {
  alerts.dismissed = [];
  alerts.errors = [];
  save();
}
