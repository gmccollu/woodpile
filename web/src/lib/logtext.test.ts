// Unit tests for logtext.ts: `npm test` (Node's own test runner, no extra packages).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { tokens, type Tok } from './logtext.ts';

const show = (t: Tok[]) =>
  t.map((k) => (k.kind === 'mark' ? `[${k.t}]` : k.kind === 'esc' ? `{${k.t}}` : k.hit ? `<${k.t}>` : k.t)).join('');

test('a line break after the tag is one marker and one space', () => {
  const body = ': %XYZ-7-711001: \nBGP(0): Future scanner version: 969';
  assert.equal(show(tokens(body, null, false)), ': %XYZ-7-711001: [↵] BGP(0): Future scanner version: 969');
  assert.equal(show(tokens(body, null, true)), ': %XYZ-7-711001: {\\n}BGP(0): Future scanner version: 969');
});

test('CR/LF runs, BEL and tabs', () => {
  assert.equal(show(tokens('a\r\nb\r\n', null, false)), 'a[↵] b[↵]');
  assert.equal(show(tokens('a\tb\x07c', null, false)), 'a\tb[↵] c');
  assert.equal(show(tokens('a\tb\x07c', null, true)), 'a{\\t}b{\\x07}c');
});

test('bidi and invisible characters are shown, never applied', () => {
  const s = 'user \u202eexe.txt\u202c zw\u200bsp \ufeffbom';
  assert.equal(show(tokens(s, null, false)), 'user [⟨U+202E⟩]exe.txt[⟨U+202C⟩] zw[⟨U+200B⟩]sp [⟨U+FEFF⟩]bom');
  assert.equal(show(tokens(s, null, true)), 'user {\\u202e}exe.txt{\\u202c} zw{\\u200b}sp {\\ufeff}bom');
  for (const c of ['\u202a', '\u2066', '\u2069', '\u200e', '\u200f', '\u061c', '\u200d']) {
    assert.ok(tokens(`a${c}b`, null, false).some((k) => k.kind === 'mark'), `U+${c.charCodeAt(0).toString(16)}`);
  }
});

test('highlighting works across a marker and around it', () => {
  assert.equal(show(tokens('x 711001: \nBGP y', /711001: \nBGP/g, false)), 'x <711001: >[↵]< BGP> y');
  assert.equal(show(tokens('deny deny', /deny/g, false)), '<deny> <deny>');
  assert.equal(show(tokens('no match here', /deny/g, false)), 'no match here');
});

test('a 64 KiB line with 10,000 matches renders in under 50 ms', () => {
  const unit = 'abcdea';
  const text = unit.repeat(Math.ceil(65536 / unit.length)).slice(0, 65536);
  const count = text.split('a').length - 1;
  assert.ok(count >= 10000, `${count} matches`);
  tokens(text, /a/g, false); // warm up
  const t0 = performance.now();
  const toks = tokens(text, /a/g, false);
  const ms = performance.now() - t0;
  // Adjacent matches merge into one highlighted token, so count highlighted characters.
  assert.equal(toks.filter((k) => k.hit).reduce((n, k) => n + k.t.length, 0), count);
  assert.ok(ms < 50, `took ${ms.toFixed(1)} ms`);
});
