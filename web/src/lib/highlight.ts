// The highlight pattern for search matches (see logtext.ts). Display only: the server
// uses RE2, so a pattern the browser's engine can't compile simply isn't highlighted.

export function matcher(q: string, regex: boolean, caseSensitive: boolean): RegExp | null {
  if (!q) return null;
  try {
    return new RegExp(regex ? q : q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), caseSensitive ? 'g' : 'gi');
  } catch {
    return null;
  }
}
