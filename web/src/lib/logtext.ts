// Turns a message into display tokens so one message is always one visual row.
//
// Normal view: each run of C0 control characters (CR, LF, BEL…; tab excepted) becomes
// one dim "↵" marker plus a space (no extra space when whitespace already follows).
// Raw view: each control character is shown as its escape (\n, \r, \t, \x07), so the
// text is exact and still one row. Match highlighting is computed on the original
// text, so a match that spans a line break still lights up across the marker.

export type TokKind = 'text' | 'mark' | 'esc';

export interface Tok {
  t: string;
  kind: TokKind;
  hit: boolean;
}

// C0 controls and DEL. Tab is kept as text in the normal view.
const CTRL = /[\x00-\x08\x0a-\x1f\x7f]/;
const isCtrl = (c: string, raw: boolean) => CTRL.test(c) || (raw && c === '\t');

const ESC: Record<string, string> = { '\n': '\\n', '\r': '\\r', '\t': '\\t' };
const esc = (c: string) => ESC[c] ?? `\\x${c.charCodeAt(0).toString(16).padStart(2, '0')}`;

function matchRanges(text: string, re: RegExp | null): [number, number][] {
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
  const hitAt = (i: number) => ranges.some(([s, e]) => i >= s && i < e);
  const out: Tok[] = [];
  const push = (t: string, kind: TokKind, hit: boolean) => {
    const last = out[out.length - 1];
    if (last && last.kind === kind && last.hit === hit && kind !== 'mark') last.t += t;
    else out.push({ t, kind, hit });
  };
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (isCtrl(c, raw)) {
      if (raw) {
        push(esc(c), 'esc', hitAt(i));
        i++;
        continue;
      }
      let j = i;
      let hit = false;
      while (j < text.length && isCtrl(text[j], false)) hit ||= hitAt(j++);
      push('↵', 'mark', hit);
      // One space after the marker, unless whitespace (or the end) already follows.
      if (j < text.length && !/\s/.test(text[j])) push(' ', 'text', hit && hitAt(j));
      i = j;
      continue;
    }
    push(c, 'text', hitAt(i));
    i++;
  }
  return out;
}
