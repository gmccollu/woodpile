// Splits text into matching and non-matching parts for <mark>. Display only: the server
// uses RE2, so a pattern the browser's engine can't compile simply isn't highlighted.

export interface Part {
  t: string;
  hit: boolean;
}

export function matcher(q: string, regex: boolean, caseSensitive: boolean): RegExp | null {
  if (!q) return null;
  try {
    return new RegExp(regex ? q : q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), caseSensitive ? 'g' : 'gi');
  } catch {
    return null;
  }
}

export function parts(text: string, re: RegExp | null): Part[] {
  if (!re) return [{ t: text, hit: false }];
  const out: Part[] = [];
  let last = 0;
  re.lastIndex = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m[0] === '') {
      re.lastIndex++; // zero-length match: step past it
      if (re.lastIndex > text.length) break;
      continue;
    }
    if (m.index > last) out.push({ t: text.slice(last, m.index), hit: false });
    out.push({ t: m[0], hit: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ t: text.slice(last), hit: false });
  return out.length ? out : [{ t: text, hit: false }];
}
