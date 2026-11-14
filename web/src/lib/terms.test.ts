// Unit tests for terms.ts (SPEC §10.1.3): `npm test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { matchTerms, parseTerms, positiveTerms, requiredTerm, termsError, type TermNode } from './terms.ts';

/** A compact form of the tree: words as-is, phrases quoted, and (op …) for the rest. */
function show(n: TermNode | null): string {
  if (!n) return '∅';
  switch (n.k) {
    case 'term':
      return /\s|^$/.test(n.text) ? JSON.stringify(n.text) : n.text;
    case 'not':
      return `(NOT ${show(n.x)})`;
    default:
      return `(${n.k.toUpperCase()} ${n.xs.map(show).join(' ')})`;
  }
}
const p = (q: string) => show(parseTerms(q));

test('precedence: NOT, then AND, then OR, left to right', () => {
  assert.equal(p('a OR b c'), '(OR a (AND b c))');
  assert.equal(p('a b OR c'), '(OR (AND a b) c)');
  assert.equal(p('a AND b OR c AND d'), '(OR (AND a b) (AND c d))');
  assert.equal(p('NOT a b'), '(AND (NOT a) b)');
  assert.equal(p('a NOT b'), '(AND a (NOT b))');
  assert.equal(p('NOT NOT a'), '(NOT (NOT a))');
  assert.equal(p('(a OR b) c'), '(AND (OR a b) c)');
  assert.equal(p('NOT (a OR b)'), '(NOT (OR a b))');
  assert.equal(p('a OR b OR c'), '(OR a b c)');
});

test('only upper-case operators; quoting makes them terms', () => {
  assert.equal(p('a and b'), '(AND a and b)');
  assert.equal(p('not found'), '(AND not found)');
  assert.equal(p('"AND"'), 'AND');
  assert.equal(p('"OR" x'), '(AND OR x)');
});

test('phrases, escapes, and quotes inside words', () => {
  assert.equal(p('"deny tcp"'), '"deny tcp"');
  assert.equal(p('user="bob"'), 'user="bob"');
  assert.equal(p('"say \\"hi\\""'), '"say \\"hi\\""');
  assert.equal((parseTerms('"a\\\\b"') as { text: string }).text, 'a\\b');
  assert.equal((parseTerms('"a\\nb"') as { text: string }).text, 'a\\nb'); // other escapes stay as typed
  assert.equal(p('"(pid 312)"'), '"(pid 312)"');
  assert.equal(p('"a"b'), '(AND a b)');
  assert.equal(p('(a)b'), '(AND a b)');
  assert.equal(p('   '), '∅');
  assert.equal(p(''), '∅');
});

test('every error message, with 1-based character positions', () => {
  const cases: [string, string][] = [
    ['deny "tcp port', 'Unclosed quote starting at character 6.'],
    ['a "x\\"', 'Unclosed quote starting at character 3.'],
    ['a b )', 'Unmatched ")" at character 5.'],
    [') a', 'Unmatched ")" at character 1.'],
    ['(a))', 'Unmatched ")" at character 4.'],
    ['x (a OR b', 'Missing ")" for the "(" at character 3.'],
    ['(', 'Missing ")" for the "(" at character 1.'],
    ['a OR', '"OR" at character 3 needs a term on both sides.'],
    ['OR a', '"OR" at character 1 needs a term on both sides.'],
    ['a AND', '"AND" at character 3 needs a term on both sides.'],
    ['a AND OR b', '"AND" at character 3 needs a term on both sides.'],
    ['(a OR )', '"OR" at character 4 needs a term on both sides.'],
    ['a NOT', '"NOT" at character 3 needs a term after it.'],
    ['NOT )', '"NOT" at character 1 needs a term after it.'],
    ['NOT OR a', '"NOT" at character 1 needs a term after it.'],
    ['a "" b', 'Empty phrase at character 3.'],
    ['a ( ) b', 'Empty parentheses at character 3.'],
    [Array.from({ length: 40 }, (_, i) => `t${i}`).join(' '), 'Too many terms (40); the limit is 32.'],
    ['((((((((( a )))))))))', 'Parentheses nested deeper than 8.'],
    ['x'.repeat(4097), 'The query is longer than 4 KiB.'],
    // Positions count characters, not bytes or UTF-16 units.
    ['é😀 "x', 'Unclosed quote starting at character 4.'],
  ];
  for (const [q, want] of cases) assert.equal(termsError(q), want, q.slice(0, 40));
  assert.equal(termsError('((((((((a))))))))'), ''); // 8 deep is fine
  assert.equal(termsError(Array.from({ length: 32 }, (_, i) => `t${i}`).join(' OR ')), '');
  assert.equal(termsError('é'.repeat(2048)), ''); // 4 KiB exactly
  assert.equal(termsError('é'.repeat(2049)), 'The query is longer than 4 KiB.');
});

test('matching: substrings, case, NOT alone, empty', () => {
  const m = (q: string, raw: string, cs = false) => matchTerms(parseTerms(q), raw, cs);
  assert.ok(m('deny tcp', 'Deny UDP and TCP'));
  assert.ok(!m('"deny tcp"', 'deny udp tcp'));
  assert.ok(m('"deny tcp"', 'x DENY TCP y'));
  assert.ok(!m('"deny tcp"', 'x DENY TCP y', true));
  assert.ok(m('NOT 10.0.0.1', 'from 10.0.0.2'));
  assert.ok(!m('NOT 10.0.0.1', 'from 10.0.0.1'));
  assert.ok(m('a OR b c', 'only a'));
  assert.ok(!m('a OR b c', 'only b'));
  assert.ok(m('', 'anything'));
  assert.ok(m('ÉCHEC', 'un échec'));
});

test('positive terms skip NOT; the prefilter term is on the top-level AND chain', () => {
  assert.deepEqual(positiveTerms(parseTerms('deny NOT 10.0.0.1 "port 22"')), ['deny', 'port 22']);
  assert.deepEqual(positiveTerms(parseTerms('NOT (a OR NOT b)')), ['b']);
  assert.equal(requiredTerm(parseTerms('deny NOT 10.0.0.1 "port 22"')), 'port 22');
  assert.equal(requiredTerm(parseTerms('a OR bbbb')), '');
  assert.equal(requiredTerm(parseTerms('xx (a OR bbbb)')), 'xx');
  assert.equal(requiredTerm(parseTerms('NOT long-word')), '');
  assert.equal(requiredTerm(parseTerms('single')), 'single');
});
