// Records the demo GIF and video from mock mode (vendor-neutral demo data):
// the dashboard filling up, a device filter, the debug chip, Events with the context
// panel, and an Export from estimate to Ready. Drives headless Chrome over the DevTools
// protocol with real mouse and keyboard events (and a drawn pointer, since headless has
// none), then assembles the frames with ffmpeg.
//
//   npm run dev:mock                 # in web/, serves http://127.0.0.1:5173
//   node scripts/demo.mjs            # writes docs/demo.gif (README), and site/img/demo.{webm,mp4,gif} + demo-poster.jpg
//                                    # (the site's hero: the video, its poster, and the GIF for browsers without video)
//
// FFMPEG=/path/to/ffmpeg, CHROME=…, BASE=… override the defaults.
import { spawn, spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const CHROME = process.env.CHROME ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const FFMPEG = process.env.FFMPEG ?? '/opt/homebrew/bin/ffmpeg';
const BASE = process.env.BASE ?? 'http://127.0.0.1:5173';
const REPO = fileURLToPath(new URL('../../', import.meta.url));
// Laid out at the app's wide layout (four stat cards in a row, Export's list beside the
// form); Chrome renders each screenshot at OUT_W pixels, so the text stays crisp (no resampling).
const W = 1300;
const H = 800;
const OUT_W = 1200;
const DPR = OUT_W / W;
const PORT = 9700 + Math.floor(Math.random() * 100);

const work = mkdtempSync(join(tmpdir(), 'woodshed-demo-'));
const framesDir = join(work, 'frames');
mkdirSync(framesDir);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const profile = join(work, 'profile');
const chrome = spawn(CHROME, ['--headless=new', `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`, '--hide-scrollbars', '--no-first-run', '--disable-extensions', 'about:blank'], { stdio: 'ignore' });

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

const ws = new WebSocket(await target());
await new Promise((r, j) => ((ws.onopen = r), (ws.onerror = j)));
let id = 0;
const pending = new Map();
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data);
  if (msg.id && pending.has(msg.id)) {
    const { res, rej } = pending.get(msg.id);
    pending.delete(msg.id);
    msg.error ? rej(new Error(msg.error.message)) : res(msg.result);
  }
};
const send = (method, params = {}) =>
  new Promise((res, rej) => {
    const n = ++id;
    pending.set(n, { res, rej });
    ws.send(JSON.stringify({ id: n, method, params }));
  });
const evaluate = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: `(async () => (${expr}))()`, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
  return r.result.value;
};

// ---- frames: a capture loop running beside the actions; speed marks retime segments ----
const frames = [];
let capturing = true;
let speed = 1;
async function captureLoop() {
  let n = 0;
  while (capturing) {
    const t = Date.now();
    const shot = await send('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: W, height: H, scale: DPR } });
    const file = `f${String(++n).padStart(5, '0')}.png`;
    writeFileSync(join(framesDir, file), Buffer.from(shot.data, 'base64'));
    frames.push({ file, t, speed });
    const wait = 70 - (Date.now() - t);
    if (wait > 0) await sleep(wait);
  }
}

// ---- a drawn pointer that glides to its target ----
const POINTER = `(() => {
  if (document.getElementById('__ptr')) return true;
  const p = document.createElement('div');
  p.id = '__ptr';
  p.innerHTML = '<svg width="22" height="22" viewBox="0 0 22 22"><path d="M3 2 L3 18 L7.5 14 L10.5 20.5 L13 19.4 L10 13 L16 13 Z" fill="#111" stroke="#fff" stroke-width="1.5" stroke-linejoin="round"/></svg>';
  Object.assign(p.style, { position: 'fixed', left: '640px', top: '420px', zIndex: 99999, pointerEvents: 'none', transition: 'left .3s ease, top .3s ease', filter: 'drop-shadow(0 1px 2px rgba(0,0,0,.35))' });
  document.body.appendChild(p);
  return true;
})()`;
let px = 640;
let py = 420;
async function pointTo(x, y) {
  await evaluate(POINTER);
  await evaluate(`(() => { const p = document.getElementById('__ptr'); p.style.left = '${x}px'; p.style.top = '${y}px'; return true; })()`);
  px = x;
  py = y;
  await sleep(330);
}
async function centerOf(selectorExpr) {
  return evaluate(`(() => { const e = ${selectorExpr}; if (!e) throw new Error('not found: ' + ${JSON.stringify(selectorExpr)}); e.scrollIntoView({ block: 'nearest' }); const r = e.getBoundingClientRect(); return [Math.round(r.left + r.width / 2), Math.round(r.top + r.height / 2)]; })()`);
}
async function click(selectorExpr, modifiers = 0) {
  const [x, y] = await centerOf(selectorExpr);
  await pointTo(x, y);
  for (const type of ['mousePressed', 'mouseReleased'])
    await send('Input.dispatchMouseEvent', { type, x, y, button: 'left', clickCount: 1, modifiers });
  await sleep(100);
}
async function type(text, perChar = 60) {
  for (const ch of text) {
    await send('Input.insertText', { text: ch });
    await sleep(perChar);
  }
}
const byText = (sel, text) => `[...document.querySelectorAll(${JSON.stringify(sel)})].find((e) => e.textContent.trim() === ${JSON.stringify(text)})`;

try {
  await send('Page.enable');
  await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: 1, mobile: false });
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'light' }] });
  await send('Storage.clearDataForOrigin', { origin: new URL(BASE).origin, storageTypes: 'all' });
  // A fresh start: the first message arrives after 1 s and the rest of the fleet 5 s later.
  await send('Page.navigate', { url: `${BASE}/?mock_panel=0&mock_firstrun=1&mock_firstmsg=1&mock_export_secs=2#/dashboard` });
  await sleep(700);
  await evaluate(POINTER);
  const loop = captureLoop();

  // 1. Devices appear and the tail fills (time-lapse ×8 while the fleet joins; the strip
  //    refreshes every 5 s, so wait for the chips rather than a fixed time).
  speed = 8;
  for (let i = 0; i < 60 && !(await evaluate(`!!document.querySelector('[aria-label="More for edge-fw-01"]')`)); i++) await sleep(250);
  await sleep(1500);
  speed = 1;
  await sleep(700);

  // 2. Click a device: the tail follows only that one; click again to see all.
  await click(`document.querySelector('[aria-label="More for edge-fw-01"]')?.previousElementSibling`);
  await sleep(1200);
  await click(`document.querySelector('[aria-label="More for edge-fw-01"]')?.previousElementSibling`);
  await sleep(300);

  // 3. The debug chip: Alt-click shows only debug lines.
  await click(`document.querySelector('[aria-label="Live tail severities"] button[title^="Debug"]')`, 1);
  await sleep(1200);

  // 4. Events: type a query, results stream in, open the lines around one.
  await click(`document.querySelector('nav a[href="#/events"]')`);
  await sleep(250);
  await click(`document.getElementById('sq')`);
  await type('deny tcp');
  await click(byText('button', 'Search'));
  for (let i = 0; i < 40 && (await evaluate(`document.querySelectorAll('.logline.clickable').length`)) < 6; i++) await sleep(150);
  await sleep(500);
  await click(`document.querySelectorAll('.logline.clickable')[2]`);
  await sleep(1100);
  await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
  await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
  await sleep(300);

  // 5. Export: pick a group, the estimate follows; build, and Ready.
  await click(`document.querySelector('nav a[href="#/export"]')`);
  await sleep(500);
  await click(`document.querySelector('.dp .trigger')`);
  await sleep(250);
  await click(byText('[aria-label="Scope"] label', 'Group: Branches'));
  await sleep(200);
  await click(byText('[aria-label="Choose devices"] button', 'Done'));
  await sleep(500);
  const readyBefore = await evaluate(`document.querySelectorAll('li.ex[data-state=ready]').length`);
  await click(byText('button', 'Build the zip'));
  for (let i = 0; i < 40 && (await evaluate(`document.querySelectorAll('li.ex[data-state=ready]').length`)) <= readyBefore; i++) await sleep(150);
  await sleep(800);

  capturing = false;
  await loop;
} catch (e) {
  capturing = false;
  const shot = await send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(join(tmpdir(), 'woodshed-demo-fail.png'), Buffer.from(shot.data, 'base64'));
  console.error(`failed; the page is in ${join(tmpdir(), 'woodshed-demo-fail.png')}`);
  throw e;
} finally {
  ws.close();
  chrome.kill();
  await sleep(300);
}

// ---- assemble: frame durations from real time, divided by each segment's speed ----
const list = [];
for (let i = 0; i < frames.length; i++) {
  const next = frames[i + 1]?.t ?? frames[i].t + 100;
  const dur = Math.max(0.02, (next - frames[i].t) / 1000 / frames[i].speed);
  list.push(`file '${join(framesDir, frames[i].file)}'`, `duration ${dur.toFixed(3)}`);
}
list.push(`file '${join(framesDir, frames[frames.length - 1].file)}'`);
writeFileSync(join(work, 'frames.txt'), list.join('\n'));

const ff = (args) => {
  const r = spawnSync(FFMPEG, ['-hide_banner', '-loglevel', 'error', '-y', ...args], { stdio: 'inherit' });
  if (r.status !== 0) throw new Error(`ffmpeg failed: ${args.join(' ')}`);
};
const input = ['-f', 'concat', '-safe', '0', '-i', join(work, 'frames.txt')];
const gif = join(REPO, 'docs', 'demo.gif');
ff([...input, '-vf', `fps=9,split[a][b];[a]palettegen=max_colors=112:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle`, '-loop', '0', gif]);
const img = join(REPO, 'site', 'img');
copyFileSync(gif, join(img, 'demo.gif'));
ff([...input, '-vf', 'fps=24,format=yuv420p', '-c:v', 'libvpx-vp9', '-b:v', '0', '-crf', '40', '-row-mt', '1', '-deadline', 'good', '-an', join(img, 'demo.webm')]);
ff([...input, '-vf', 'fps=24,format=yuv420p', '-c:v', 'libx264', '-crf', '28', '-preset', 'slow', '-movflags', '+faststart', '-an', join(img, 'demo.mp4')]);
// Poster: the dashboard once the fleet is in (the first frame at normal speed).
const poster = frames.find((f) => f.speed === 1) ?? frames[0];
ff(['-i', join(framesDir, poster.file), '-q:v', '4', join(img, 'demo-poster.jpg')]);

// Frame samples for review (gitignored folder).
const samples = join(REPO, 'site', 'screenshots-review', 'demo-frames');
mkdirSync(samples, { recursive: true });
for (const k of [0.05, 0.2, 0.35, 0.5, 0.65, 0.8, 0.95]) {
  const f = frames[Math.floor(k * (frames.length - 1))];
  ff(['-i', join(framesDir, f.file), '-q:v', '3', join(samples, `at-${Math.round(k * 100)}pct.jpg`)]);
}
const probe = spawnSync(FFMPEG.replace(/ffmpeg$/, 'ffprobe'), ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', gif], { encoding: 'utf8' });
console.log(`${frames.length} frames captured; the loop runs ${Number(probe.stdout).toFixed(1)} s`);
for (const f of [gif, join(img, 'demo.gif'), join(img, 'demo.webm'), join(img, 'demo.mp4'), join(img, 'demo-poster.jpg')]) console.log(`${f.replace(REPO, '')}: ${(statSync(f).size / 1024).toFixed(0)} KiB`);
if (process.env.KEEP) console.log(`frames kept in ${work}`);
else rmSync(work, { recursive: true, force: true });
