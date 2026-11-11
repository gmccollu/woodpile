// Review screenshots of the site at desktop and phone widths, light and dark, into
// site/screenshots-review/ (gitignored). Drives headless Chrome over the DevTools protocol.
// The GitHub API call is answered from a fake release (or a 404), so nothing goes out.
//
//   cd site && python3 -m http.server 8765 &
//   node tools/screenshots.mjs            (BASE=http://127.0.0.1:8765, CHROME=… to override)
//
// Also fails on any page error or CSP violation.
import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const CHROME = process.env.CHROME ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const BASE = process.env.BASE ?? 'http://127.0.0.1:8765';
const OUT = fileURLToPath(new URL('../screenshots-review/', import.meta.url));
const PORT = 9800 + Math.floor(Math.random() * 100);

const NAMES = [
  'woodpile_1.0.0_windows_amd64.msi', 'woodpile_1.0.0_windows_arm64.msi', 'woodpile_1.0.0_macos_universal.pkg',
  'woodpile_1.0.0_amd64.deb', 'woodpile_1.0.0_arm64.deb', 'woodpile-1.0.0-1.x86_64.rpm', 'woodpile-1.0.0-1.aarch64.rpm',
  'woodpile_1.0.0_windows_amd64.zip', 'woodpile_1.0.0_windows_arm64.zip', 'woodpile_1.0.0_darwin_universal.tar.gz',
  'woodpile_1.0.0_linux_amd64.tar.gz', 'woodpile_1.0.0_linux_arm64.tar.gz', 'checksums.txt',
];
const hex = (i) => [...Array(64)].map((_, k) => '0123456789abcdef'[(i * 7 + k * 13) % 16]).join('');
const RELEASE = JSON.stringify({
  tag_name: 'v1.0.0',
  html_url: 'https://github.com/gmccollu/woodpile/releases/tag/v1.0.0',
  assets: NAMES.map((name, i) => ({
    name,
    size: name === 'checksums.txt' ? 1200 : 9_000_000 + i * 731_000,
    browser_download_url: `https://github.com/gmccollu/woodpile/releases/download/v1.0.0/${name}`,
    digest: name === 'checksums.txt' ? null : `sha256:${hex(i + 1)}`,
  })),
});

const WIN = { ua: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36', meta: { platform: 'Windows', platformVersion: '15.0.0', architecture: 'x86', bitness: '64', model: '', mobile: false } };
const MAC = { ua: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 15_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36', meta: { platform: 'macOS', platformVersion: '15.6.0', architecture: 'arm', bitness: '64', model: '', mobile: false } };
const LINUX_ARM = { ua: 'Mozilla/5.0 (X11; Linux aarch64; rv:140.0) Gecko/20100101 Firefox/140.0' };
const PHONE = { ua: 'Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1', mobile: true };

const both = (s) => [
  { ...s, name: `${s.name}-light`, mode: 'light' },
  { ...s, name: `${s.name}-dark`, mode: 'dark' },
];

const SHOTS = [
  ...both({ name: 'home-desktop', path: '/', w: 1400, h: 900, client: WIN, api: 'release',
    check: `document.querySelectorAll('#dl-primary .asset.best').length === 2 && document.querySelector('#dl-primary .asset .n').textContent === 'Windows installer (x64)' && document.querySelectorAll('#dl-others-body li').length === 10` }),
  ...both({ name: 'home-phone', path: '/', w: 390, h: 844, client: PHONE, api: 'release',
    check: `document.querySelectorAll('#dl-primary .asset').length === 0 && document.getElementById('dl-others').open && document.querySelectorAll('#dl-others-body li').length === 12 && document.documentElement.scrollWidth <= innerWidth
      && [...document.querySelectorAll('#dl-others-body .file')].every((f) => f.getBoundingClientRect().right <= f.closest('li').getBoundingClientRect().right)
      && [...document.querySelectorAll('#dl-others-body .dl')].every((b) => b.textContent === 'Download')` }),
  { name: 'home-desktop-all-downloads-light', mode: 'light', path: '/#download', w: 1400, h: 900, client: WIN, api: 'release',
    act: `(document.getElementById('dl-others').open = true, true)`,
    check: `[...document.querySelectorAll('#dl-others-body li')].every((li) => li.getBoundingClientRect().height < 80)` },
  { name: 'home-mac-light', mode: 'light', path: '/#download', w: 1400, h: 900, client: MAC, api: 'release',
    check: `[...document.querySelectorAll('#dl-primary .asset .n')].map((x) => x.textContent).join('|') === 'macOS installer (Intel and Apple silicon)|macOS portable (Intel and Apple silicon)'` },
  { name: 'home-linux-arm-dark', mode: 'dark', path: '/#download', w: 1400, h: 900, client: LINUX_ARM, api: 'release',
    check: `document.querySelectorAll('#dl-primary .asset.best').length === 3 && document.querySelector('#dl-status').textContent.includes('on ARM')` },
  { name: 'home-fallback-light', mode: 'light', path: '/#download', w: 1400, h: 900, client: WIN, api: '404',
    check: `document.querySelector('#dl-status').textContent.startsWith('No release has been published yet') && document.querySelector('#dl-primary a.btn').href.endsWith('/releases/latest')` },
  { name: 'home-ratelimit-dark', mode: 'dark', path: '/#download', w: 390, h: 844, client: PHONE, api: '403',
    check: `document.querySelector('#dl-status').textContent.includes('limit')` },
  ...both({ name: 'install-desktop', path: '/install.html', w: 1400, h: 900, client: MAC, api: 'none' }),
  ...both({ name: 'install-phone', path: '/install.html', w: 390, h: 844, client: PHONE, api: 'none',
    check: `[...document.querySelectorAll('main *')].every((e) => e.closest('pre, table') || e.getBoundingClientRect().right <= innerWidth + 1)` }),
];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const profile = mkdtempSync(join(tmpdir(), 'woodpile-site-'));
const chrome = spawn(CHROME, ['--headless=new', `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`, '--hide-scrollbars', '--no-first-run', 'about:blank'], { stdio: 'ignore' });

async function target() {
  for (let i = 0; i < 50; i++) {
    try {
      const page = (await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json()).find((t) => t.type === 'page');
      if (page) return page.webSocketDebuggerUrl;
    } catch {
      /* starting */
    }
    await sleep(200);
  }
  throw new Error('Chrome did not start');
}

let failed = 0;
try {
  const ws = new WebSocket(await target());
  await new Promise((r, j) => ((ws.onopen = r), (ws.onerror = j)));
  let id = 0;
  const pending = new Map();
  const events = [];
  let api = 'none';
  let apiCalls = 0;
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const n = ++id;
      pending.set(n, { res, rej });
      ws.send(JSON.stringify({ id: n, method, params }));
    });
  ws.onmessage = async (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && pending.has(msg.id)) {
      const { res, rej } = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? rej(new Error(msg.error.message)) : res(msg.result);
    } else if (msg.method === 'Fetch.requestPaused') {
      apiCalls++;
      const p = msg.params;
      const bodies = { release: [200, RELEASE], 404: [404, '{"message":"Not Found"}'], 403: [403, '{"message":"API rate limit exceeded"}'] };
      const [code, body] = bodies[api] ?? [500, '{}'];
      send('Fetch.fulfillRequest', {
        requestId: p.requestId,
        responseCode: code,
        responseHeaders: [{ name: 'Content-Type', value: 'application/json' }, { name: 'Access-Control-Allow-Origin', value: '*' }],
        body: Buffer.from(body).toString('base64'),
      });
    } else if (msg.method) events.push(msg);
  };
  const evaluate = async (expr) => {
    const r = await send('Runtime.evaluate', { expression: `(async () => (${expr}))()`, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
    return r.result.value;
  };

  await send('Page.enable');
  await send('Runtime.enable');
  await send('Log.enable');
  await send('Fetch.enable', { patterns: [{ urlPattern: 'https://api.github.com/*' }] });
  mkdirSync(OUT, { recursive: true });

  for (const s of SHOTS) {
    try {
      api = s.api;
      apiCalls = 0;
      await send('Storage.clearDataForOrigin', { origin: new URL(BASE).origin, storageTypes: 'all' });
      await send('Emulation.setDeviceMetricsOverride', { width: s.w, height: s.h, deviceScaleFactor: 1, mobile: !!s.client.mobile });
      await send('Emulation.setUserAgentOverride', { userAgent: s.client.ua, ...(s.client.meta ? { userAgentMetadata: { brands: [], fullVersionList: [], fullVersion: '140.0', ...s.client.meta } } : {}) });
      await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: s.mode }] });
      await send('Page.navigate', { url: 'about:blank' });
      await sleep(100);
      events.length = 0;
      await send('Page.navigate', { url: `${BASE}${s.path}` });
      await sleep(1500);
      const bad = events.filter(
        (e) => e.method === 'Runtime.exceptionThrown' || (e.method === 'Log.entryAdded' && /Content Security Policy|Refused to/i.test(e.params.entry.text)),
      );
      if (bad.length) {
        failed++;
        console.error(`  ! ${s.name}: ${JSON.stringify(bad[0].params).slice(0, 300)}`);
      }
      if (s.act) {
        await evaluate(s.act);
        await sleep(300);
      }
      if (s.check && !(await evaluate(s.check))) {
        failed++;
        console.error(`  ! ${s.name}: check failed: ${s.check}`);
      }
      if (s.api !== 'none' && apiCalls !== 1) {
        failed++;
        console.error(`  ! ${s.name}: expected 1 GitHub API call, saw ${apiCalls}`);
      }
      if (s.api === 'none' && apiCalls !== 0) {
        failed++;
        console.error(`  ! ${s.name}: the install page called the GitHub API`);
      }
      const height = Math.min(await evaluate('Math.ceil(document.documentElement.scrollHeight)'), 6000);
      await send('Emulation.setDeviceMetricsOverride', { width: s.w, height: Math.max(s.h, height), deviceScaleFactor: 1, mobile: !!s.client.mobile });
      await sleep(250);
      const shot = await send('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: s.w, height: Math.max(s.h, height), scale: 1 } });
      writeFileSync(join(OUT, `${s.name}.png`), Buffer.from(shot.data, 'base64'));
      console.log(`  ${s.name}.png (${s.w}x${Math.max(s.h, height)})`);
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
