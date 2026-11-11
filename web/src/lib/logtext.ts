// Turns a message into display tokens so one message is always one visual row, and
// nothing in it can disguise itself.
//
// Normal view:
//   - each run of C0 control characters (CR, LF, BEL…; tab excepted) and DEL becomes one
//     dim "↵" marker plus a space (no extra space when whitespace already follows);
//   - each bidi or invisible format character (U+202A–202E, U+2066–2069, U+200E/200F,
//     U+061C, U+200B–200D, U+FEFF) becomes a dim "⟨U+202E⟩" marker, so reordered or hidden
//     text can't pass for something else.
// Raw view: control characters are shown as \n, \r, \t, \xHH and format characters as
// \uXXXX, so the text is exact and still one row.
//
// Match highlighting is computed on the original text, so a match across a break still
// lights up. Everything here is linear in the text length: a hostile 64 KiB line full of
// matches must not freeze the page.

export type TokKind = 'text' | 'mark' | 'esc';

export interface Tok {
  t: string;
  kind: TokKind;
  hit: boolean;
}

/** Rendered characters per row before the "show full message" expander. */
export const ROW_CAP = 8192;

const isCtrl = (code: number) => (code <= 0x1f && code !== 0x09) || code === 0x7f;
const isFormat = (code: number) =>
  (code >= 0x202a && code <= 0x202e) ||
  (code >= 0x2066 && code <= 0x2069) ||
  code === 0x200e ||
  code === 0x200f ||
  code === 0x061c ||
  (code >= 0x200b && code <= 0x200d) ||
  code === 0xfeff;

const hex = (code: number, w: number) => code.toString(16).toUpperCase().padStart(w, '0');
const CTRL_ESC: Record<number, string> = { 0x0a: '\\n', 0x0d: '\\r', 0x09: '\\t' };
const ctrlEsc = (code: number) => CTRL_ESC[code] ?? `\\x${hex(code, 2).toLowerCase()}`;

/** A tooltip for a row: the exact raw text, cut at ROW_CAP characters. */
export const tip = (raw: string) => (raw.length > ROW_CAP ? `${raw.slice(0, ROW_CAP)}… (${raw.length - ROW_CAP} more characters)` : raw);

/** Non-overlapping [start, end) match ranges in order. */
export function matchRanges(text: string, re: RegExp | null): [number, number][] {
  if (!re) return [];
  const out: [number, number][] = [];
  re.lastIndex = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m[0] === '') {
      re.lastIndex++;
      if (re.lastIndex > text.length) break;
      continue;
    }
    out.push([m.index, m.index + m[0].length]);
  }
  return out;
}

export function tokens(text: string, re: RegExp | null, raw: boolean): Tok[] {
  const ranges = matchRanges(text, re);
  // Positions are asked for in increasing order, so one pointer walks the ranges once.
  let r = 0;
  const hitAt = (i: number) => {
    while (r < ranges.length && ranges[r][1] <= i) r++;
    return r < ranges.length && ranges[r][0] <= i;
  };

  const out: Tok[] = [];
  // Plain text is gathered in a buffer and flushed when the kind or hit state changes.
  let buf = '';
  let bufHit = false;
  const flush = () => {
    if (buf) out.push({ t: buf, kind: 'text', hit: bufHit });
    buf = '';
  };
  const plain = (s: string, hit: boolean) => {
    if (buf && hit !== bufHit) flush();
    bufHit = hit;
    buf += s;
  };
  const special = (t: string, kind: TokKind, hit: boolean) => {
    flush();
    const last = out[out.length - 1];
    if (kind === 'esc' && last && last.kind === 'esc' && last.hit === hit) last.t += t;
    else out.push({ t, kind, hit });
  };

  const n = text.length;
  let i = 0;
  while (i < n) {
    const code = text.charCodeAt(i);
    if (raw && (isCtrl(code) || code === 0x09)) {
      special(ctrlEsc(code), 'esc', hitAt(i));
      i++;
    } else if (raw && isFormat(code)) {
      special(`\\u${hex(code, 4).toLowerCase()}`, 'esc', hitAt(i));
      i++;
    } else if (isFormat(code)) {
      special(`⟨U+${hex(code, 4)}⟩`, 'mark', hitAt(i));
      i++;
    } else if (isCtrl(code)) {
      let j = i;
      let hit = false;
      while (j < n && isCtrl(text.charCodeAt(j))) {
        if (hitAt(j)) hit = true;
        j++;
      }
      special('↵', 'mark', hit);
      // One space after the marker, unless whitespace (or the end) already follows.
      if (j < n && !/\s/.test(text[j])) plain(' ', hit && hitAt(j));
      i = j;
    } else {
      // A run of ordinary characters, up to the next special one or match boundary.
      const hit = hitAt(i);
      const stop = r < ranges.length ? (hit ? ranges[r][1] : ranges[r][0]) : n;
      let j = i + 1;
      while (j < n && j < stop) {
        const c = text.charCodeAt(j);
        if (isCtrl(c) || isFormat(c) || (raw && c === 0x09)) break;
        j++;
      }
      plain(text.slice(i, j), hit);
      i = j;
    }
  }
  flush();
  return out;
}
