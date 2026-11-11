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
    check: `document.body.innerText.includes('First message received') && !!document.querySelector('a[href="#/dashboard"].pill-btn')`,
  },
  { name: 'first-run-redirect', noshot: true, mode: 'light', url: '/?mock_panel=0&mock_firstrun=1&mock_firstmsg=0', check: `location.hash === '#/welcome'` },
  ...both({ name: 'dashboard', url: '/?mock_panel=0#/dashboard', wait: 3000, check: `document.querySelectorAll('[role=log] .line').length > 50 && document.body.innerText.includes(' rejected')` }),
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
    check: `document.querySelectorAll('[role=log] .line').length === 1000`,
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
    check: `(() => { const l = [...document.querySelectorAll('[role=log] .line')]; return l.length > 0 && l.every((x) => x.textContent.includes('deny')); })()`,
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
    check: `(() => { const h = [...document.querySelectorAll('[role=log] .line .c.h')]; return h.length > 0 && h.every((x) => x.textContent === '192.0.2.44'); })()`,
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
  ...both({ name: 'search', url: '/?mock_panel=0#/search' }),
  ...both({ name: 'export', url: '/?mock_panel=0#/export' }),
  // ---- LAN mode and auth ----
  ...both({
    name: 'login',
    url: '/?mock_panel=0&mock_auth=1#/devices',
    check: `!!__field('Password') && !document.querySelector('nav') && !document.querySelector('input[type=search]')`,
  }),
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
    name: 'login-returns-and-sends-csrf',
    noshot: true,
    mode: 'light',
    url: '/?mock_panel=0&mock_auth=1#/devices',
    act: `__set(__field('Password'), 'woodpile-demo'); await __tick(); __btn('Log in').click();
      await new Promise(r => setTimeout(r, 1500));
      document.querySelector('[aria-label="Rename edge-fw-01"]').click(); await new Promise(r => setTimeout(r, 200));
      const i = document.getElementById('edit-name-10.1.0.1'); __set(i, 'edge-fw-01b');
      i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));`,
    after: 900,
    check: `location.hash === '#/devices' && !!__btn('Log out') && document.body.innerText.includes('edge-fw-01b')
      && window.__woodpileMock.lastCsrf === window.__woodpileMock.csrf && window.__woodpileMock.csrf.length === 32`,
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
    check: `!!__field('Current password') && !!__btn('Log out')`,
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
      await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: 1, mobile: false });
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
      const h = Math.max(H, Math.min(height, 3000));
      await send('Emulation.setDeviceMetricsOverride', { width: W, height: h, deviceScaleFactor: 1, mobile: false });
      await sleep(250);
      const shot = await send('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: W, height: h, scale: 1 } });
      writeFileSync(join(OUT, `${s.name}.png`), Buffer.from(shot.data, 'base64'));
      console.log(`  ${s.name}.png (${W}x${h})`);
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
