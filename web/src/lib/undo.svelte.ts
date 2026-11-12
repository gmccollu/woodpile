// "Undo last save": when a settings save leads to a new listener error (a port that's
// taken, for example), keep the settings from before it so one click puts them back. Kept
// for the browser session (sessionStorage), cleared once used or once every listener is up.
import { getSettings, isConflict, putSettings, type Settings } from './api';
import { live, refreshStatus } from './status.svelte';

const KEY = 'woodshed.undo';

interface Saved {
  prev: Settings;
  /** What broke, for the notice ("UDP 514, TCP 514"). */
  broke: string;
  /** The ETag the save left: undo is refused (409) if the settings changed again since. */
  etag?: string;
}

function load(): Saved | null {
  try {
    const v = JSON.parse(sessionStorage.getItem(KEY) ?? 'null');
    return v && v.prev ? v : null;
  } catch {
    return null;
  }
}

export const undo = $state<{ saved: Saved | null; busy: boolean; error: string; version: number }>({
  saved: load(),
  busy: false,
  error: '',
  version: 0,
});

function persist() {
  try {
    if (undo.saved) sessionStorage.setItem(KEY, JSON.stringify(undo.saved));
    else sessionStorage.removeItem(KEY);
  } catch {
    /* storage off: memory only */
  }
}

const failing = () => new Set((live.status?.listeners ?? []).filter((l) => !l.ok).map((l) => `${l.proto.toUpperCase()} ${l.port}`));

/** Call before a save: what was failing already. */
export const failingNow = () => failing();

/**
 * Call after a successful save with the settings from before it. Listeners rebind just
 * after the save, so status is read again (now and a moment later) to see what broke.
 */
export async function noteSave(before: Settings, failingBefore: Set<string>, etag: string): Promise<void> {
  for (const wait of [0, 1200]) {
    if (wait) await new Promise((r) => setTimeout(r, wait));
    await refreshStatus();
    const fresh = [...failing()].filter((k) => !failingBefore.has(k));
    if (fresh.length) {
      undo.saved = { prev: before, broke: fresh.join(', '), etag };
      undo.error = '';
      persist();
      return;
    }
  }
}

export function clearUndo(): void {
  undo.saved = null;
  undo.error = '';
  persist();
}

/** Puts back the settings from before the last save. */
export async function undoLastSave(): Promise<void> {
  if (!undo.saved || undo.busy) return;
  undo.busy = true;
  undo.error = '';
  try {
    // No ETag from the save (an older server): read the current one.
    const etag = undo.saved.etag || (await getSettings()).etag;
    await putSettings(undo.saved.prev, etag);
    clearUndo();
    undo.version++; // the Settings page reloads its form
    await refreshStatus();
  } catch (e) {
    undo.error = isConflict(e)
      ? "Couldn't undo: the settings changed again since that save. Check them in Settings."
      : `Couldn't undo: ${(e as Error).message}`;
  } finally {
    undo.busy = false;
  }
}

// Every listener up again (fixed by hand, or undone): nothing to undo.
$effect.root(() => {
  $effect(() => {
    const ls = live.status?.listeners;
    if (undo.saved && ls && ls.length && ls.every((l) => l.ok) && !undo.busy) clearUndo();
  });
});
