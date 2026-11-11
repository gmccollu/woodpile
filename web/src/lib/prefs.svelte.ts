// Display preferences remembered in localStorage.
import { ALL_SEVS, type SevKey } from './api';

const KEY = 'woodpile.prefs';

interface Prefs {
  /** Wrap long log lines (off: one line per message, scroll sideways). */
  wrap: boolean;
  /** Show times in UTC instead of local time. */
  utc: boolean;
  /** Live tail severity selection. */
  tailSev: SevKey[];
}

function load(): Prefs {
  const d: Prefs = { wrap: false, utc: false, tailSev: [...ALL_SEVS] };
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '{}');
    return {
      wrap: typeof v.wrap === 'boolean' ? v.wrap : d.wrap,
      utc: typeof v.utc === 'boolean' ? v.utc : d.utc,
      tailSev: Array.isArray(v.tailSev) ? (v.tailSev as SevKey[]).filter((k) => ALL_SEVS.includes(k)) : d.tailSev,
    };
  } catch {
    return d;
  }
}

export const prefs = $state<Prefs>(load());

$effect.root(() => {
  $effect(() => {
    const snapshot = JSON.stringify({ wrap: prefs.wrap, utc: prefs.utc, tailSev: prefs.tailSev });
    try {
      localStorage.setItem(KEY, snapshot);
    } catch {
      /* private mode: just don't remember */
    }
  });
});
