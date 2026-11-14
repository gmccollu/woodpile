// Boolean terms for Events (`syntax=terms`, SPEC §10.1.3): words and "quoted phrases" joined
// by AND (or just a space), OR and NOT, upper case only, with ( ) for grouping. NOT binds
// tightest, then AND, then OR. Every word and phrase is a substring of the raw message.
//
// The server parses q itself; this copy gives the mock the same answers and error messages
// (with the same 1-based character positions), and gives the highlighter the terms that
// aren't under a NOT. No imports, so `npm test` runs it under Node.

export type TermNode =
  | { k: 'term'; text: string; pos: number }
  | { k: 'not'; x: TermNode }
  | { k: 'and'; xs: TermNode[] }
  | { k: 'or'; xs: TermNode[] };

export const MAX_TERMS = 32;
export const MAX_DEPTH = 8;
export const MAX_BYTES = 4096;

export const TERMS_AND_REGEX = "Terms and regex can't be combined: pick one.";

export class TermsError extends Error {}

type Tok = { t: 'word' | 'phrase' | '(' | ')' | 'AND' | 'OR' | 'NOT'; text: string; pos: number };

const isSpace = (c: string) => /\s/u.test(c);

function tokenize(q: string): Tok[] {
  const cs = Array.from(q); // positions count characters (code points), not UTF-16 units
  const out: Tok[] = [];
  let i = 0;
  while (i < cs.length) {
    const c = cs[i];
    if (isSpace(c)) {
      i++;
      continue;
    }
    const pos = i + 1;
    if (c === '(' || c === ')') {
      out.push({ t: c, text: c, pos });
      i++;
      continue;
    }
    if (c === '"') {
      let s = '';
      let j = i + 1;
      let closed = false;
      while (j < cs.length) {
        const d = cs[j];
        if (d === '\\' && j + 1 < cs.length && (cs[j + 1] === '"' || cs[j + 1] === '\\')) {
          s += cs[j + 1];
          j += 2;
          continue;
        }
        if (d === '"') {
          closed = true;
          break;
        }
        s += d; // a backslash before anything else is an ordinary character
        j++;
      }
      if (!closed) throw new TermsError(`Unclosed quote starting at character ${pos}.`);
      if (s === '') throw new TermsError(`Empty phrase at character ${pos}.`);
      out.push({ t: 'phrase', text: s, pos });
      i = j + 1;
      continue;
    }
    let s = '';
    while (i < cs.length && !isSpace(cs[i]) && cs[i] !== '(' && cs[i] !== ')') s += cs[i++];
    out.push({ t: s === 'AND' || s === 'OR' || s === 'NOT' ? s : 'word', text: s, pos });
  }
  return out;
}

/**
 * Parses q. Returns null for an empty query (it matches every line). Throws a TermsError
 * with the server's message for anything it refuses.
 */
export function parseTerms(q: string): TermNode | null {
  if (new TextEncoder().encode(q).length > MAX_BYTES) throw new TermsError('The query is longer than 4 KiB.');
  const toks = tokenize(q);
  const n = toks.filter((t) => t.t === 'word' || t.t === 'phrase').length;
  if (n > MAX_TERMS) throw new TermsError(`Too many terms (${n}); the limit is ${MAX_TERMS}.`);
  if (!toks.length) return null;
  let i = 0;
  let depth = 0;
  const peek = (): Tok | undefined => toks[i];
  const both = (op: Tok): never => {
    throw new TermsError(`"${op.t}" at character ${op.pos} needs a term on both sides.`);
  };
  const startsAtom = (t: Tok | undefined) => !!t && (t.t === 'word' || t.t === 'phrase' || t.t === '(' || t.t === 'NOT');

  // after: the operator a missing operand belongs to (undefined at the start of an expression).
  function unary(after?: Tok): TermNode {
    const t = peek();
    if (!t || t.t === 'AND' || t.t === 'OR' || t.t === ')') {
      if (after?.t === 'NOT') throw new TermsError(`"NOT" at character ${after.pos} needs a term after it.`);
      if (after) both(after);
      if (t && (t.t === 'AND' || t.t === 'OR')) both(t);
      if (t && t.t === ')') throw new TermsError(`Unmatched ")" at character ${t.pos}.`);
      throw new TermsError('The query is empty.'); // not reachable: empty input returns null
    }
    i++;
    if (t.t === 'NOT') return { k: 'not', x: unary(t) };
    if (t.t === '(') {
      if (peek()?.t === ')') throw new TermsError(`Empty parentheses at character ${t.pos}.`);
      if (++depth > MAX_DEPTH) throw new TermsError(`Parentheses nested deeper than ${MAX_DEPTH}.`);
      if (!peek()) throw new TermsError(`Missing ")" for the "(" at character ${t.pos}.`);
      const x = or();
      const c = peek();
      if (!c || c.t !== ')') throw new TermsError(`Missing ")" for the "(" at character ${t.pos}.`);
      i++;
      depth--;
      return x;
    }
    return { k: 'term', text: t.text, pos: t.pos };
  }

  function and(after?: Tok): TermNode {
    const xs = [unary(after)];
    for (;;) {
      const t = peek();
      if (t?.t === 'AND') {
        i++;
        xs.push(unary(t));
      } else if (startsAtom(t)) xs.push(unary());
      else break;
    }
    return xs.length === 1 ? xs[0] : { k: 'and', xs };
  }

  function or(): TermNode {
    const xs = [and()];
    while (peek()?.t === 'OR') {
      const op = toks[i++];
      xs.push(and(op));
    }
    return xs.length === 1 ? xs[0] : { k: 'or', xs };
  }

  const root = or();
  const rest = peek();
  if (rest) {
    if (rest.t === ')') throw new TermsError(`Unmatched ")" at character ${rest.pos}.`);
    both(rest); // not reachable: or() stops only at ")" or the end
  }
  return root;
}

/** Whether raw matches: every term as a substring, ignoring case unless caseSensitive. */
export function matchTerms(n: TermNode | null, raw: string, caseSensitive = false): boolean {
  if (!n) return true;
  const hay = caseSensitive ? raw : raw.toLowerCase();
  const ev = (x: TermNode): boolean => {
    switch (x.k) {
      case 'term':
        return hay.includes(caseSensitive ? x.text : x.text.toLowerCase());
      case 'not':
        return !ev(x.x);
      case 'and':
        return x.xs.every(ev);
      case 'or':
        return x.xs.some(ev);
    }
  };
  return ev(n);
}

/** The words and phrases that aren't under a NOT (what the highlighter marks), in query order. */
export function positiveTerms(n: TermNode | null): string[] {
  const out: string[] = [];
  const walk = (x: TermNode, neg: boolean) => {
    if (x.k === 'term') {
      if (!neg) out.push(x.text);
    } else if (x.k === 'not') walk(x.x, !neg);
    else for (const y of x.xs) walk(y, neg);
  };
  if (n) walk(n, false);
  return out;
}

/**
 * The longest term every match must contain (a positive term on the top-level AND chain),
 * or "" when there is none: the server's prefilter.
 */
export function requiredTerm(n: TermNode | null): string {
  if (!n) return '';
  const chain = n.k === 'and' ? n.xs : [n];
  let best = '';
  for (const x of chain) if (x.k === 'term' && new TextEncoder().encode(x.text).length > new TextEncoder().encode(best).length) best = x.text;
  return best;
}

/** The parse error's message for q, or "" when it parses. */
export function termsError(q: string): string {
  try {
    parseTerms(q);
    return '';
  } catch (e) {
    return (e as Error).message;
  }
}
