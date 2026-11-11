// WCAG 2.x contrast check for site/style.css, light and dark (adapted from
// web/scripts/contrast.mjs). Translucent backgrounds are composited over every page
// gradient stop and the worst result is kept. Exits 1 if any pair fails.
//   node site/tools/contrast.mjs
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const css = readFileSync(fileURLToPath(new URL('../style.css', import.meta.url)), 'utf8');

function vars(body) {
  const v = {};
  for (const m of body.matchAll(/--([\w-]+):\s*([^;]+);/g)) v[m[1]] = m[2].trim();
  return v;
}
const lightBody = css.slice(css.indexOf(':root {'), css.indexOf('\n}', css.indexOf(':root {')));
const darkStart = css.indexOf('@media (prefers-color-scheme: dark)');
const darkBody = css.slice(darkStart, css.indexOf('\n}\n', darkStart));
const light = vars(lightBody);
const dark = { ...light, ...vars(darkBody) };

function parse(c) {
  let m = c.match(/^#([0-9a-f]{6})$/i);
  if (m) {
    const n = parseInt(m[1], 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255, 1];
  }
  m = c.match(/^rgba?\(([^)]+)\)$/);
  if (m) {
    const p = m[1].split(',').map(parseFloat);
    return [p[0], p[1], p[2], p[3] ?? 1];
  }
  throw new Error(`can't parse colour ${c}`);
}
const over = (top, under) => [0, 1, 2].map((i) => top[i] * top[3] + under[i] * (1 - top[3])).concat(1);
const lum = ([r, g, b]) => {
  const f = (v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
};
const ratio = (a, b) => {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
};

const stacks = {
  page: [],
  card: ['glass'],
  'card-strong': ['glass', 'glass-strong'],
  chip: ['chip'],
  btn: ['btn'],
  notice: ['glass', 'warn-bg'],
  code: ['code-bg'],
};
const AA = 4.5;
const pairs = [
  ['fg', ['page', 'card', 'card-strong', 'chip', 'notice']],
  ['muted', ['page', 'card', 'card-strong', 'chip', 'notice']],
  ['link', ['page', 'card', 'card-strong', 'chip', 'notice']],
  ['accent-fg', ['card-strong', 'chip']],
  ['ok-fg', ['card', 'card-strong']],
  ['btn-fg', ['btn']],
  ['code-fg', ['code']],
  ['code-dim', ['code']],
];

let failures = 0;
for (const [mode, v] of [['light', light], ['dark', dark]]) {
  const get = (n) => {
    if (!(n in v)) throw new Error(`missing --${n}`);
    return parse(v[n]);
  };
  const under = ['bg', 'bg-warm', 'bg-cool', 'bg-green'].map(get);
  console.log(mode);
  for (const [fg, on] of pairs) {
    for (const s of on) {
      let worst = Infinity;
      for (const base of under) {
        let bg = base;
        for (const layer of stacks[s]) bg = over(get(layer), bg);
        worst = Math.min(worst, ratio(over(get(fg), bg), bg));
      }
      const ok = worst >= AA;
      if (!ok) failures++;
      console.log(`  ${ok ? 'ok  ' : 'FAIL'} ${fg.padEnd(10)} on ${s.padEnd(11)} ${worst.toFixed(2)}`);
    }
  }
}
console.log(failures ? `${failures} pair(s) below WCAG AA` : 'all pairs meet WCAG AA');
process.exit(failures ? 1 : 0);
