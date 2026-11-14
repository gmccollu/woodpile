// The highlight pattern for search matches (see logtext.ts). Display only: the server
// uses RE2, so a pattern the browser's engine can't compile simply isn't highlighted.
import { parseTerms, positiveTerms } from './terms';

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/**
 * regex: q is a regular expression. terms: q is boolean terms (syntax=terms), and the words
 * and phrases that aren't under a NOT are marked.
 */
export function matcher(q: string, regex: boolean, caseSensitive: boolean, terms = false): RegExp | null {
  if (!q) return null;
  try {
    if (terms) {
      const ts = [...new Set(positiveTerms(parseTerms(q)))].sort((a, b) => b.length - a.length);
      return ts.length ? new RegExp(ts.map(esc).join('|'), caseSensitive ? 'g' : 'gi') : null;
    }
    return new RegExp(regex ? q : esc(q), caseSensitive ? 'g' : 'gi');
  } catch {
    return null;
  }
}
