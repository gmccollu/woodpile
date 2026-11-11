// Takes the review screenshots in web/screenshots/ from the mock-mode UI, in light
// and dark mode, by driving headless Chrome over the DevTools protocol (no extra
// npm packages). Start the mock server first:
//
//   npm run dev:mock            # serves http://127.0.0.1:5173
//   npm run screenshots         # all shots
//   npm run screenshots -- settings   # only shots whose name contains "settings"
//
// CHROME=/path/to/chrome and BASE=http://host:port override the defaults.
// Shots with a `check` also assert behaviour (a JS expression that must be true),
// and `noshot` entries are checks only, so this doubles as a UI smoke test.
import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const CHROME = process.env.CHROME ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const BASE = process.env.BASE ?? 'http://127.0.0.1:5173';
const OUT = fileURLToPath(new URL('../screenshots/', import.meta.url));
const PORT = 9300 + Math.floor(Math.random() * 500);
const W = 1400;
const H = 900;
const only = process.argv[2] ?? '';

// Helpers injected into the page for the interactive shots.
const H_JS = `
  window.__btn = (text) => [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === text);
  window.__set = (el, v) => {
    const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, v);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  };
  window.__tick = () => new Promise((r) => setTimeout(r, 120));
  window.__field = (label) => [...document.querySelectorAll('label')].find((l) => l.textContent.trim().startsWith(label))?.querySelector('input,textarea');
`;

const both = (s) => [
  { ...s, name: `${s.name}-light`, mode: 'light' },
  { ...s, name: `${s.name}-dark`, mode: 'dark' },
];

const SHOTS = [
  ...both({ name: 'welcome-waiting', url: '/?mock_panel=0&mock_firstrun=1&mock_firstmsg=0#/welcome', check: `document.body.innerText.includes('Waiting for the first message')` }),
  {
    name: 'welcome-received-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_firstrun=1&mock_firstmsg=1#/welcome',
    wait: 3500,
    check: `document.body.innerText.includes('First message received') && document.body.innerText.includes('From 127.0.0.1 at')
      && !!document.querySelector('a[href="#/dashboard"].pill-btn')`,
  },
  {
    name: 'welcome-names-first-not-latest',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_firstrun=1&mock_firstmsg=1#/welcome',
    wait: 12000,
    check: `document.body.innerText.includes('From 127.0.0.1 at')`,
  },
  {
    name: 'last-heard-reads-now',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/dashboard',
    wait: 4500,
    check: `[...document.querySelectorAll('.strip .chip')].find((t) => t.textContent.includes('edge-fw-01'))?.title.includes('last heard now')`,
  },
  { name: 'first-run-redirect', noshot: true, mode: 'light', url: '/?mock_panel=0&mock_firstrun=1&mock_firstmsg=0', check: `location.hash === '#/welcome'` },
  ...both({ name: 'dashboard', url: '/?mock_panel=0#/dashboard', wait: 3000, check: `document.querySelectorAll('[role=log] .logline').length > 50 && document.body.innerText.includes(' rejected')` }),
  {
    name: 'dashboard-sampled-portclash-dark',
    mode: 'dark',
    url: '/?mock_panel=0&mock_rate=1284&mock_clash=1#/dashboard',
    wait: 3500,
    check: `document.body.innerText.includes('msg/s, showing a sample') && !!__btn('Retry')`,
  },
  {
    name: 'retry-clears-banner',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_clash=1#/dashboard',
    act: `__btn('Retry').click();`,
    after: 1500,
    check: `!document.body.innerText.includes('TCP 514 is in use')`,
  },
  {
    name: 'tail-keeps-1000-lines',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_rate=5000#/dashboard',
    wait: 21000,
    check: `document.querySelectorAll('[role=log] .logline').length === 1000`,
  },
  {
    name: 'theme-remembered',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/about',
    act: `__btn('Dark').click();`,
    check: `localStorage.getItem('woodpile.theme') === 'dark' && document.documentElement.classList.contains('dark')`,
  },
  {
    name: 'settings-save-ok',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/settings',
    act: `__set(__field('Keep logs for'), '14'); __btn('Save').click();`,
    after: 600,
    check: `document.body.innerText.includes('Saved.') && __field('Keep logs for').value === '14'`,
  },
  {
    name: 'filter-reaches-tail',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_rate=200#/dashboard',
    act: `__set(document.querySelector('input[type=search]'), 'deny');`,
    after: 3000,
    check: `(() => { const l = [...document.querySelectorAll('[role=log] .logline')]; return l.length > 0 && l.every((x) => x.textContent.includes('deny')); })()`,
  },
  {
    name: 'allow-refused-sender',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/settings',
    act: `document.querySelector('[aria-label="Allow 198.51.100.7"]').click();`,
    after: 300,
    check: `document.querySelector('textarea').value.split('\\n').includes('198.51.100.7') && __btn('Added, save to apply')?.disabled`,
  },
  {
    name: 'ungrouped-tab-filters-tail',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_rate=200#/dashboard',
    act: `__btn('Ungrouped').click();`,
    after: 3000,
    check: `(() => { const h = [...document.querySelectorAll('[role=log] .logline .c.h')]; return h.length > 0 && h.every((x) => x.textContent === '192.0.2.44'); })()`,
  },
  {
    name: 'rename-saves',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/devices',
    act: `document.querySelector('[aria-label="Rename edge-fw-01"]').click(); await new Promise(r => setTimeout(r, 200));
      const i = document.getElementById('edit-name-10.1.0.1'); __set(i, 'edge-fw-01a');
      i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));`,
    after: 800,
    check: `document.body.innerText.includes('edge-fw-01a')`,
  },
  {
    name: 'dashboard-group-paused-light',
    mode: 'light',
    url: '/?mock_panel=0#/dashboard',
    wait: 2500,
    act: `__btn('HQ').click(); await new Promise(r => setTimeout(r, 1500)); __btn('Pause').click();`,
    after: 1500,
    check: `document.body.innerText.includes('Paused') && !!__btn('Resume')`,
  },
  ...both({ name: 'devices', url: '/?mock_panel=0#/devices' }),
  {
    name: 'devices-rename-dark',
    mode: 'dark',
    url: '/?mock_panel=0#/devices',
    act: `document.querySelector('[aria-label="Rename edge-fw-01"]').click();`,
  },
  {
    name: 'banners-writes-paused-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_clash=1&mock_paused=1#/devices',
  },
  ...both({ name: 'settings', url: '/?mock_panel=0#/settings' }),
  ...both({
    name: 'settings-errors',
    url: '/?mock_panel=0#/settings',
    act: `
      const ports = [...document.querySelectorAll('fieldset input[type=number]')];
      __set(ports[0], '70000');
      __set(document.querySelector('textarea'), '10.1.0.0/16\\n10.300.0.0/33');
      __set(__field('Offline after'), '2');
      __btn('Save').click();`,
    after: 800,
    check: `document.querySelectorAll('.ferr').length >= 3 && document.querySelectorAll('[aria-invalid=true]').length >= 3`,
  }),
  ...both({ name: 'about', url: '/?mock_panel=0#/about' }),
  { name: 'about-linux-light', mode: 'light', url: '/?mock_panel=0&mock_os=linux#/about' },
  // ---- Dashboard layout, wrap, severity chips ----
  { name: 'dashboard-2000-light', mode: 'light', w: 2000, h: 1100, url: '/?mock_panel=0#/dashboard', wait: 3000 },
  { name: 'dashboard-2000-dark', mode: 'dark', w: 2000, h: 1100, url: '/?mock_panel=0#/dashboard', wait: 3000 },
  {
    name: 'tail-nowrap-one-line-per-message',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/dashboard',
    wait: 2500,
    check: `(() => { const l = [...document.querySelectorAll('[role=log] .logline')]; return l.length > 50 && l.every((x) => x.offsetHeight <= 24)
      && getComputedStyle(l[0]).whiteSpace === 'pre'; })()`,
  },
  {
    name: 'dashboard-wrap-on-light',
    mode: 'light',
    url: '/?mock_panel=0#/dashboard',
    act: `__btn('Raw').click(); await __tick(); __btn('Wrap').click();`,
    after: 800,
    check: `[...document.querySelectorAll('[role=log] .logline')].some((x) => x.offsetHeight > 30)`,
  },
  {
    name: 'tail-debug-only-light',
    mode: 'light',
    url: '/?mock_panel=0#/dashboard',
    act: `window.__woodpileMock.livePaths.length = 0;
      document.querySelector('[aria-label="Live tail severities"] button[title^="Debug"]').dispatchEvent(new MouseEvent('click', { bubbles: true, altKey: true }));`,
    after: 2500,
    check: `(() => { const p = window.__woodpileMock.livePaths; const s = [...document.querySelectorAll('[role=log] .logline .c.s')];
      return p.length > 0 && /[?&]severities=7(&|$)/.test(p[p.length - 1]) && s.length > 0 && s.every((x) => x.textContent === 'debug'); })()`,
  },
  {
    name: 'chip-filters-tail',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/dashboard',
    act: `[...document.querySelectorAll('.strip .chip')].find((c) => c.textContent.includes('edge-fw-02')).click();
      await new Promise((r) => setTimeout(r, 2000));
      const one = [...document.querySelectorAll('[role=log] .logline .c.h')].map((x) => x.textContent);
      if (!one.length || one.some((h) => h !== 'edge-fw-02')) throw new Error('single chip did not filter: ' + [...new Set(one)]);
      [...document.querySelectorAll('.strip .chip')].find((c) => c.textContent.includes('core-sw-01')).dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }));`,
    after: 2500,
    check: `(() => { const h = new Set([...document.querySelectorAll('[role=log] .logline .c.h')].map((x) => x.textContent));
      return h.size === 2 && h.has('edge-fw-02') && h.has('core-sw-01'); })()`,
  },
  // ---- Embedded line breaks: one message is one row with Wrap off ----
  {
    name: 'tail-embedded-break-one-row',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_rate=400#/dashboard',
    act: `__set(document.querySelector('input[type=search]'), 'future version');`,
    after: 4000,
    check: `(() => { const l = [...document.querySelectorAll('[role=log] .logline')];
      return l.length > 0 && l.every((x) => x.offsetHeight <= 24 && x.querySelector('.m').textContent.includes('↵') && !x.querySelector('.m').textContent.includes('\\n')); })()`,
  },
  {
    name: 'tail-embedded-break-raw-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_rate=400#/dashboard',
    act: `__set(document.querySelector('input[type=search]'), 'job finished'); await __tick(); __btn('Raw').click();`,
    after: 4000,
    check: `(() => { const l = [...document.querySelectorAll('[role=log] .logline')];
      return l.length > 0 && l.every((x) => x.offsetHeight <= 24 && x.textContent.includes('\\\\r\\\\n')); })()`,
  },
  {
    name: 'search-embedded-break-light',
    mode: 'light',
    url: '/?mock_panel=0#/search?q=future%20version&r=24h',
    wait: 2500,
    check: `(() => { const l = [...document.querySelectorAll('.logline.clickable')];
      return l.length > 20 && l.every((x) => x.offsetHeight <= 24 && x.textContent.includes('↵')) && document.querySelectorAll('mark.hl').length >= l.length; })()`,
  },
  {
    name: 'context-embedded-break-one-row',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?q=future%20version&r=24h',
    wait: 2500,
    act: `document.querySelectorAll('.logline.clickable')[30].click();`,
    after: 1500,
    check: `(() => { const l = [...document.querySelectorAll('[role=dialog] .logline')]; const hit = document.querySelector('[role=dialog] .logline.hit');
      return l.length > 50 && hit && hit.textContent.includes('↵') && l.every((x) => x.offsetHeight <= 24); })()`,
  },
  {
    name: 'tail-fullscreen-dark',
    mode: 'dark',
    url: '/?mock_panel=0#/dashboard',
    act: `__btn('Full screen').click();`,
    check: `document.querySelector('.tail.full')?.getBoundingClientRect().width === innerWidth`,
  },

  // ---- Scale: 10,000 devices, paginated ----
  {
    name: 'devices-10k-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_devices=10000#/devices',
    wait: 3000,
    check: `/1–200 of [\\d,]{5,}/.test(document.body.innerText) && document.querySelectorAll('tbody tr').length === 200`,
  },
  {
    name: 'devices-10k-next-and-search',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_devices=10000#/devices',
    wait: 2500,
    act: `__btn('Next →').click(); await new Promise((r) => setTimeout(r, 1500));
      if (!/201–400 of/.test(document.body.innerText)) throw new Error('next page not shown');
      __set(document.querySelector('input[type=search]'), 'sw-0004');`,
    after: 2500,
    check: `(() => { const rows = [...document.querySelectorAll('tbody tr')]; return rows.length > 0 && rows.length <= 200
      && rows.every((r) => r.textContent.includes('sw-0004')) && /1–\\d+ of \\d+ matching/.test(document.body.innerText); })()`,
  },
  {
    name: 'dashboard-10k-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_devices=10000#/dashboard',
    wait: 3000,
    check: `document.querySelectorAll('.strip button.chip').length === 30 && /All [\\d,]{5,} devices/.test(document.querySelector('.strip .more')?.textContent ?? '')`,
  },
  {
    name: 'banner-device-limit-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_device_limit=1#/dashboard',
    wait: 2000,
    check: `document.body.innerText.includes('New senders are being refused') && !!document.querySelector('a[href="#/settings?section=limits"]')`,
  },
  {
    name: 'device-limit-link-opens-sender-limits',
    noshot: true,
    report: `JSON.stringify({ top: document.getElementById('sender-limits')?.getBoundingClientRect().top, y: scrollY, h: innerHeight, hash: location.hash })`,
    mode: 'light',
    url: '/?mock_panel=0&mock_device_limit=1#/dashboard',
    wait: 2000,
    act: `document.querySelector('a[href="#/settings?section=limits"]').click();`,
    after: 1500,
    check: `(() => { const el = document.getElementById('sender-limits'); const r = el?.getBoundingClientRect(); return !!r && r.top >= 0 && r.top < innerHeight; })()`,
  },
  {
    name: 'sender-limits-validation',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/settings',
    act: `__set(__field('Active devices'), '0'); __btn('Save').click();`,
    after: 800,
    check: `document.getElementById('err-listeners.max_devices')?.textContent.startsWith('Use a whole number')`,
  },
  {
    name: 'require-login-local',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_password=1#/settings',
    act: `const box = __field('Require login on this machine too'); if (box.disabled) throw new Error('disabled with a password set');
      box.click(); await __tick(); __btn('Save').click(); await new Promise((r) => setTimeout(r, 1500));
      if (!__field('Password')) throw new Error('no login form after turning on require_login_local');
      __set(__field('Password'), 'woodpile-demo'); await __tick(); __btn('Log in').click();`,
    after: 1500,
    check: `location.hash.startsWith('#/settings') && __field('Require login on this machine too').checked && !!__btn('Log out')`,
  },
  {
    name: 'require-login-local-needs-password',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/settings',
    check: `__field('Require login on this machine too').disabled && document.body.innerText.includes('shared or multi-user machines')`,
  },
  {
    name: 'password-change-needs-current-localhost',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_password=1#/settings',
    check: `!!__field('Current password') && document.body.innerText.includes('Change password')`,
  },
  {
    name: 'refused-sender-reasons',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/settings',
    check: `(() => { const rows = [...document.querySelectorAll('tr')]; const ps = rows.find((r) => r.textContent.includes('10.9.0.66'));
      const al = rows.find((r) => r.textContent.includes('198.51.100.7'));
      return ps && al && ps.textContent.includes('too many TCP connections') && !ps.querySelector('button')
        && al.textContent.includes('not on the allowlist') && !!al.querySelector('button'); })()`,
  },

  // ---- Hostile content: long lines, spoofing characters ----
  {
    name: 'search-long-line-light',
    mode: 'light',
    url: '/?mock_panel=0#/search?q=payload%20dump&r=7d',
    wait: 2500,
    check: `(() => { const l = [...document.querySelectorAll('.logline.clickable')]; return l.length > 0
      && l.every((x) => x.offsetHeight <= 24 && x.textContent.length < 9000 && x.textContent.includes('show full message')); })()`,
  },
  {
    name: 'long-line-expands-in-place',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?q=payload%20dump&r=7d',
    wait: 2500,
    act: `document.querySelector('.logline.clickable .more-btn').click();`,
    after: 600,
    check: `document.querySelector('.logline.clickable').textContent.length > 20000 && !document.querySelector('[role=dialog]')`,
  },
  {
    name: 'search-spoofing-chars-light',
    mode: 'light',
    url: '/?mock_panel=0#/search?q=file%20uploaded&r=24h',
    wait: 2500,
    check: `(() => { const l = [...document.querySelectorAll('.logline.clickable .m')]; return l.length > 0
      && l.every((x) => x.textContent.includes('⟨U+202E⟩') && x.textContent.includes('⟨U+200B⟩')
        && !x.textContent.includes('\u202e') && !x.textContent.includes('\u200b')); })()`,
  },
  {
    name: 'spoofing-chars-raw-escaped',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?q=file%20uploaded&r=24h',
    wait: 2500,
    act: `__btn('Raw').click();`,
    after: 500,
    check: `(() => { const l = [...document.querySelectorAll('.logline.clickable .m')]; return l.length > 0
      && l.every((x) => x.textContent.includes('\\\\u202e') && !x.textContent.includes('\u202e')); })()`,
  },

  // ---- Search ----
  ...both({
    name: 'search-results',
    url: '/?mock_panel=0#/search?q=deny&r=24h',
    wait: 3000,
    check: `document.querySelectorAll('.logline.clickable').length === 500 && document.querySelectorAll('mark.hl').length > 100`,
  }),
  {
    name: 'search-partial-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_slow_scan=1#/search?q=failover&r=7d',
    wait: 2600,
    check: `/Scanned [\\d.]+ \\w+ in [\\d,]+ files…/.test(document.body.innerText) && !!__btn('Cancel')`,
  },
  {
    name: 'search-context-light',
    mode: 'light',
    url: '/?mock_panel=0#/search?q=deny&r=24h',
    wait: 2500,
    act: `document.querySelectorAll('.logline.clickable')[60].click();`,
    after: 1500,
    check: `(() => { const n = document.querySelectorAll('[role=dialog] .logline').length;
      const atEnd = document.querySelector('[role=dialog]').innerText.includes('Newest stored line');
      return !!document.querySelector('[role=dialog] .logline.hit mark.hl') && n >= 51 && (n === 101 || atEnd); })()`,
  },
  {
    name: 'search-regex-error-light',
    mode: 'light',
    url: '/?mock_panel=0#/search?q=deny%20(tcp&re=1&r=1h',
    wait: 1500,
    check: `document.querySelector('#sq-err')?.textContent.startsWith('error parsing regexp') && document.querySelector('#sq').getAttribute('aria-invalid') === 'true'`,
  },
  {
    name: 'search-pagination-no-duplicates',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?r=24h',
    wait: 2500,
    act: `__btn('Load 500 more').click(); await new Promise((r) => setTimeout(r, 2500)); __btn('Load 500 more').click();`,
    after: 2500,
    check: `(() => { const refs = [...document.querySelectorAll('.logline.clickable')].map((x) => x.dataset.ref); return refs.length === 1500 && new Set(refs).size === 1500; })()`,
  },
  {
    name: 'search-partial-finishes-and-cancels',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_slow_scan=1#/search?q=failover&r=24h',
    wait: 1800,
    act: `__btn('Cancel').click();`,
    after: 400,
    check: `!__btn('Cancel') && (!!__btn('Continue the scan') || document.body.innerText.includes('Cancelled before anything matched'))`,
  },
  {
    name: 'search-empty-state',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?q=no-such-text-anywhere&r=15m',
    wait: 1500,
    check: `document.body.innerText.includes('No matches in this range')`,
  },
  {
    name: 'search-export-these-results',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?q=deny&r=1h&group=HQ',
    wait: 2000,
    act: `__btn('Export these results').click();`,
    after: 1500,
    check: `location.hash.startsWith('#/export?') && document.querySelector('#exq').value === 'deny'
      && document.body.innerText.includes('Filled in from your search') && document.body.innerText.includes('Group: HQ')`,
  },
  {
    name: 'search-export-keeps-regex-and-case',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?q=deny%20(tcp%7Cudp)&re=1&cs=1&r=1h',
    wait: 2000,
    act: `if (__btn('Export these results').disabled) throw new Error('export disabled for a regex search'); __btn('Export these results').click();`,
    after: 1500,
    check: `document.querySelector('#exq').value === 'deny (tcp|udp)' && __btn('.* Regex').getAttribute('aria-pressed') === 'true'
      && __btn('Aa Case').getAttribute('aria-pressed') === 'true' && /Up to [\\d,]+ lines/.test(document.body.innerText)`,
  },
  {
    name: 'search-none-selected',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/search?r=1h&sev=none',
    wait: 1500,
    check: `document.querySelectorAll('.logline.clickable').length === 0 && document.body.innerText.includes('No levels selected')`,
  },
  {
    name: 'tail-none-sends-none',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/dashboard',
    act: `window.__woodpileMock.livePaths.length = 0; __btn('None').click();`,
    after: 1500,
    check: `(() => { const p = window.__woodpileMock.livePaths; return p.length > 0 && /[?&]severities=none(&|$)/.test(p[p.length - 1])
      && document.querySelectorAll('[role=log] .logline').length === 0 && document.body.innerText.includes('No levels selected'); })()`,
  },

  // ---- Export ----
  ...both({
    name: 'export-estimate',
    url: '/?mock_panel=0#/export',
    wait: 1800,
    check: `/Up to [\\d,]+ lines from [\\d,]+ files?, about/.test(document.body.innerText)`,
  }),
  {
    name: 'export-building-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_export_secs=30#/export',
    wait: 1500,
    act: `__btn('Build the zip').click();`,
    after: 3500,
    check: `!!document.querySelector('li[data-state=building] [role=progressbar]')`,
  },
  {
    name: 'export-ready-list-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_export_secs=2#/export',
    wait: 1500,
    act: `__btn('Build the zip').click();`,
    after: 5000,
    check: `document.querySelectorAll('li[data-state=ready] a[download]').length >= 2 && document.body.innerText.includes('expires in 23 h')
      && document.body.innerText.includes('err, warn · “deny”') && document.body.innerText.includes('Group: DMZ')`,
  },
  {
    name: 'export-lifecycle',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_export_secs=2#/export',
    wait: 1500,
    act: `const before = new Set([...document.querySelectorAll('li[data-id]')].map((x) => x.dataset.id));
      __btn('Build the zip').click();
      await new Promise((r) => setTimeout(r, 1200));
      const mine = [...document.querySelectorAll('li[data-id]')].find((x) => !before.has(x.dataset.id));
      if (!mine || !['queued', 'building'].includes(mine.dataset.state)) throw new Error('not queued/building: ' + mine?.dataset.state);
      window.__exportId = mine.dataset.id;
      await new Promise((r) => setTimeout(r, 3500));
      const done = document.querySelector('li[data-id="' + window.__exportId + '"]');
      if (done?.dataset.state !== 'ready' || !done.querySelector('a[download]')) throw new Error('not ready: ' + done?.dataset.state);
      [...done.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Delete').click();`,
    after: 1200,
    check: `!document.querySelector('li[data-id="' + window.__exportId + '"]')`,
  },
  {
    name: 'export-all-devices-flag',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_devices=10000#/export',
    wait: 2500,
    check: `(() => { const b = JSON.parse(window.__woodpileMock.lastExportBody || '{}');
      return b.all_devices === true && !('devices' in b) && /Up to [\\d,]+ lines/.test(document.body.innerText)
        && /[\\d,]{5,} devices/.test(document.body.innerText); })()`,
  },
  {
    name: 'groups-from-api',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_devices=10000#/dashboard',
    wait: 2500,
    check: `['Stores', 'Warehouses', 'Lab', 'HQ', 'Ungrouped'].every((g) => [...document.querySelectorAll('[role=tab]')].some((t) => t.textContent.trim() === g))`,
  },
  {
    name: 'export-failure-shows-error',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_export_secs=2&mock_export_fail=1#/export',
    wait: 1500,
    act: `__btn('Build the zip').click();`,
    after: 2500,
    check: `[...document.querySelectorAll('li[data-state=failed]')].some((x) => x.textContent.includes('Not enough free disk space'))`,
  },
  {
    name: 'export-cancel-and-failure',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_export_secs=30&mock_export_fail=1#/export',
    wait: 1500,
    act: `__btn('Build the zip').click(); await new Promise((r) => setTimeout(r, 1500)); __btn('Build the zip').click();
      await new Promise((r) => setTimeout(r, 1500));
      const q = document.querySelector('li[data-state=queued]');
      if (!q) throw new Error('second export not queued');
      window.__q = q.dataset.id;
      [...q.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Cancel').click();`,
    after: 1500,
    check: `!document.querySelector('li[data-id="' + window.__q + '"]')`,
  },
  // ---- LAN mode and auth ----
  ...both({
    name: 'login',
    url: '/?mock_panel=0&mock_auth=1#/devices',
    check: `!!__field('Password') && !document.querySelector('nav') && !document.querySelector('input[type=search]')
      && document.body.innerText.includes('Forgot the password?')`,
  }),
  {
    name: 'login-forgot-password-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1#/devices',
    act: `document.querySelector('details summary').click();`,
    check: `document.body.innerText.includes('woodpile reset-password') && document.body.innerText.includes('http://localhost:5173/')`,
  },
  {
    name: 'login-wrong-password-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1#/devices',
    act: `__set(__field('Password'), 'not-it'); await __tick(); __btn('Log in').click();`,
    after: 800,
    check: `document.body.innerText.includes("That password isn't right.")`,
  },
  {
    name: 'login-locked-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1&mock_locked=1#/devices',
    act: `__set(__field('Password'), 'woodpile-demo'); await __tick(); __btn('Log in').click();`,
    after: 1200,
    check: `/locked for 1[45] min \\d+ s/.test(document.body.innerText) && __btn('Log in').disabled`,
  },
  {
    name: 'login-returns-and-sends-secret',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1#/devices',
    act: `__set(__field('Password'), 'woodpile-demo'); await __tick(); __btn('Log in').click();
      await new Promise(r => setTimeout(r, 1500));
      document.querySelector('[aria-label="Rename edge-fw-01"]').click(); await new Promise(r => setTimeout(r, 200));
      const i = document.getElementById('edit-name-10.1.0.1'); __set(i, 'edge-fw-01b');
      i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));`,
    after: 900,
    check: `(() => { const m = window.__woodpileMock; const key = 'woodpile.session.' + location.origin;
      return location.hash === '#/devices' && !!__btn('Log out') && document.body.innerText.includes('edge-fw-01b')
        && m.secret.length === 32 && localStorage.getItem(key) === m.secret
        && m.lastWriteHeader === m.secret && m.lastGetHeader === m.secret
        && m.sessionBodies.length > 0 && m.sessionBodies.every((b) => !b.includes(m.secret) && !b.includes('session_secret')); })()`,
  },
  {
    name: 'secret-sent-on-gets-and-live-stream',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1&mock_loggedin=1#/dashboard',
    wait: 3000,
    check: `(() => { const m = window.__woodpileMock; return !!m.secret && m.lastGetHeader === m.secret && m.lastLiveHeader === m.secret
      && document.querySelectorAll('[role=log] .logline').length > 0; })()`,
  },
  {
    name: 'cookie-without-header-gets-401-and-login',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1&mock_loggedin=1#/devices',
    wait: 1500,
    act: `window.__woodpileMock.cookieOnly = true;
      await new Promise((r) => setTimeout(r, 6000));
      if (!__field('Password') || document.querySelector('nav')) throw new Error('no login after a cookie-only 401');
      if (localStorage.getItem('woodpile.session.' + location.origin)) throw new Error('secret not cleared on 401');
      window.__woodpileMock.cookieOnly = false;
      __set(__field('Password'), 'woodpile-demo'); await __tick(); __btn('Log in').click();`,
    after: 1500,
    check: `location.hash === '#/devices' && document.querySelectorAll('tbody tr').length > 5`,
  },
  {
    name: 'logout-clears-secret',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1&mock_loggedin=1#/about',
    wait: 1500,
    act: `__btn('Log out').click();`,
    after: 1200,
    check: `!!__field('Password') && !localStorage.getItem('woodpile.session.' + location.origin)`,
  },
  {
    name: 'expired-session-401-shows-login',
    noshot: true,
    mode: 'light',
    url: '/?mock_auth=1&mock_loggedin=1#/settings',
    act: `__btn('Mock data ▸').click(); await new Promise(r => setTimeout(r, 200)); __btn('Expire session').click();
      await new Promise(r => setTimeout(r, 1200));
      if (!__field('Password') || document.querySelector('nav')) throw new Error('no login form after the 401');
      __set(__field('Password'), 'woodpile-demo'); await __tick(); __btn('Log in').click();`,
    after: 1500,
    check: `location.hash === '#/settings' && document.body.innerText.includes('UI access') && !!document.querySelector('nav')`,
  },
  {
    name: 'password-set-enables-lan',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0#/settings',
    act: `const lan = document.querySelector('[aria-describedby^="hint-lan"]'); if (!lan.disabled) throw new Error('LAN enabled without a password');
      __set(__field('New password'), 'short'); __set(__field('Repeat it'), 'short'); await __tick(); __btn('Set password').click();
      await new Promise(r => setTimeout(r, 600));
      if (!document.body.innerText.includes('Use 8 to 1024 characters.')) throw new Error('no length error');
      __set(__field('New password'), 'correct horse'); __set(__field('Repeat it'), 'correct horse'); await __tick(); __btn('Set password').click();`,
    after: 800,
    check: `document.body.innerText.includes('Password set.') && !document.querySelector('[aria-describedby^="hint-lan"]').disabled`,
  },
  {
    name: 'settings-lan-warning-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_password=1#/settings',
    act: `document.querySelector('[aria-describedby^="hint-lan"]').click();`,
    check: `document.body.innerText.includes('Before you turn this on')`,
  },
  {
    name: 'settings-lan-saved-windows-light',
    mode: 'light',
    url: '/?mock_panel=0&mock_password=1&mock_os=windows#/settings',
    act: `document.querySelector('[aria-describedby^="hint-lan"]').click(); __btn('Save').click();`,
    after: 900,
    check: `document.body.innerText.includes('netsh advfirewall firewall add rule name="Woodpile UI" dir=in action=allow protocol=TCP localport=8514')
      && [...document.querySelectorAll('a')].some((a) => a.textContent.trim() === 'Go there' && a.href.startsWith('https://'))`,
  },
  {
    name: 'settings-lan-on-dark',
    mode: 'dark',
    url: '/?mock_panel=0&mock_auth=1&mock_loggedin=1#/settings',
    check: `!!__field('Current password') && !!__btn('Log out') && document.body.innerText.includes('Certificate fingerprint (SHA-256)')`,
  },
  { name: 'mock-panel-light', mode: 'light', url: '/#/dashboard', act: `__btn('Mock data ▸').click();`, after: 500 },
].filter((s) => s.name.includes(only));

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const profile = mkdtempSync(join(tmpdir(), 'woodpile-shots-'));
const chrome = spawn(
  CHROME,
  [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${profile}`,
    `--window-size=${W},${H}`,
    '--hide-scrollbars',
    '--no-first-run',
    '--no-default-browser-check',
    'about:blank',
  ],
  { stdio: 'ignore' },
);

async function target() {
  for (let i = 0; i < 50; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
      const page = list.find((t) => t.type === 'page');
      if (page) return page.webSocketDebuggerUrl;
    } catch {
      /* not up yet */
    }
    await sleep(200);
  }
  throw new Error('Chrome did not start');
}

let failed = 0;
try {
  const ws = new WebSocket(await target());
  await new Promise((r, j) => {
    ws.onopen = r;
    ws.onerror = j;
  });
  let id = 0;
  const pending = new Map();
  const events = [];
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && pending.has(msg.id)) {
      const { res, rej } = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? rej(new Error(msg.error.message)) : res(msg.result);
    } else if (msg.method) events.push(msg);
  };
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const n = ++id;
      pending.set(n, { res, rej });
      ws.send(JSON.stringify({ id: n, method, params }));
    });
  const evaluate = async (expr) => {
    const r = await send('Runtime.evaluate', { expression: `(async () => { ${H_JS}; ${expr} })()`, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
    return r.result.value;
  };

  await send('Page.enable');
  await send('Runtime.enable');
  mkdirSync(OUT, { recursive: true });

  for (const s of SHOTS) {
    try {
      const sw = s.w ?? W;
      const sh = s.h ?? H;
      await send('Emulation.setDeviceMetricsOverride', { width: sw, height: sh, deviceScaleFactor: 1, mobile: false });
      // Fresh preferences per shot (wrap, severities, theme), so shots don't leak into each other.
      await send('Storage.clearDataForOrigin', { origin: new URL(BASE).origin, storageTypes: 'local_storage' });
      await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: s.mode }] });
      // A fresh document each time: blank first so the hash-only change reloads the app.
      await send('Page.navigate', { url: 'about:blank' });
      await sleep(150);
      await send('Page.navigate', { url: `${BASE}${s.url}` });
      await sleep(400);
      await evaluate(`localStorage.removeItem('woodpile.theme'); document.documentElement.classList.toggle('dark', ${s.mode === 'dark'});`);
      await sleep(s.wait ?? 2000);
      if (s.act) {
        await evaluate(s.act);
        await sleep(s.after ?? 600);
      }
      if (s.check && !(await evaluate(`return !!(${s.check})`))) {
        failed++;
        console.error(`  ! ${s.name}: check failed: ${s.check}`);
        if (s.report) console.error(`    ${await evaluate(`return ${s.report}`)}`);
      }
      const errs = events.filter((e) => e.method === 'Runtime.exceptionThrown');
      if (errs.length) {
        console.error(`  ! ${s.name}: page error: ${errs[0].params.exceptionDetails.exception?.description ?? errs[0].params.exceptionDetails.text}`);
        failed++;
      }
      events.length = 0;
      if (s.noshot) {
        console.log(`  ${s.name}: checked`);
        continue;
      }
      const height = await evaluate('return Math.ceil(document.documentElement.scrollHeight)');
      const h = Math.max(sh, Math.min(height, 3000));
      await send('Emulation.setDeviceMetricsOverride', { width: sw, height: h, deviceScaleFactor: 1, mobile: false });
      await sleep(250);
      const shot = await send('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: sw, height: h, scale: 1 } });
      writeFileSync(join(OUT, `${s.name}.png`), Buffer.from(shot.data, 'base64'));
      console.log(`  ${s.name}.png (${sw}x${h})`);
    } catch (e) {
      failed++;
      console.error(`  ! ${s.name}: ${e.message}`);
    }
  }
  ws.close();
} finally {
  chrome.kill();
  await sleep(300);
  rmSync(profile, { recursive: true, force: true });
}
process.exit(failed ? 1 : 0);
