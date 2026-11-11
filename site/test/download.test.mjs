// Unit tests for site/download.js: cd site && npm test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classify, detectPlatform, loadRelease, pick, latestApiUrl, releasesUrl, REPO } from '../download.js';

// Every file the release workflow produces for 1.0.0 (.goreleaser.yaml, build-msi.ps1,
// build-pkg.sh), plus checksums.txt.
const NAMES = [
  'woodpile_1.0.0_windows_amd64.msi',
  'woodpile_1.0.0_windows_arm64.msi',
  'woodpile_1.0.0_macos_universal.pkg',
  'woodpile_1.0.0_amd64.deb',
  'woodpile_1.0.0_arm64.deb',
  'woodpile-1.0.0-1.x86_64.rpm',
  'woodpile-1.0.0-1.aarch64.rpm',
  'woodpile_1.0.0_windows_amd64.zip',
  'woodpile_1.0.0_windows_arm64.zip',
  'woodpile_1.0.0_darwin_universal.tar.gz',
  'woodpile_1.0.0_linux_amd64.tar.gz',
  'woodpile_1.0.0_linux_arm64.tar.gz',
  'checksums.txt',
];
const RELEASE = {
  tag_name: 'v1.0.0',
  html_url: 'https://github.com/gmccollu/woodpile/releases/tag/v1.0.0',
  assets: NAMES.map((name, i) => ({
    name,
    size: 1_000_000 + i,
    browser_download_url: `https://github.com/gmccollu/woodpile/releases/download/v1.0.0/${name}`,
    digest: `sha256:${String(i).padStart(64, '0')}`,
  })),
};

const names = (list) => list.map((a) => a.name);

test('every release file is classified; checksums.txt is kept apart', () => {
  for (const n of NAMES.slice(0, -1)) assert.ok(classify(n), n);
  assert.equal(classify('checksums.txt'), null);
  assert.equal(classify('woodpile_1.0.0_linux_amd64.tar.gz.intoto.jsonl'), null);
  assert.deepEqual(classify('woodpile_1.0.0_windows_arm64.msi'), { os: 'windows', kind: 'installer', arch: 'arm64', label: 'Windows installer (ARM64)', format: 'msi' });
  assert.equal(classify('woodpile-1.0.0-1.aarch64.rpm').arch, 'arm64');
  assert.equal(classify('woodpile_1.0.0_darwin_universal.tar.gz').kind, 'portable');
});

test('the best files per platform, installer first', () => {
  const p = (os, arch) => names(pick(RELEASE.assets, { os, arch }).best);
  assert.deepEqual(p('windows', 'x64'), ['woodpile_1.0.0_windows_amd64.msi', 'woodpile_1.0.0_windows_amd64.zip']);
  assert.deepEqual(p('windows', 'arm64'), ['woodpile_1.0.0_windows_arm64.msi', 'woodpile_1.0.0_windows_arm64.zip']);
  assert.deepEqual(p('windows', 'unknown'), ['woodpile_1.0.0_windows_amd64.msi', 'woodpile_1.0.0_windows_amd64.zip']);
  assert.deepEqual(p('macos', 'arm64'), ['woodpile_1.0.0_macos_universal.pkg', 'woodpile_1.0.0_darwin_universal.tar.gz']);
  assert.deepEqual(p('linux', 'x64'), ['woodpile_1.0.0_amd64.deb', 'woodpile-1.0.0-1.x86_64.rpm', 'woodpile_1.0.0_linux_amd64.tar.gz']);
  assert.deepEqual(p('linux', 'arm64'), ['woodpile_1.0.0_arm64.deb', 'woodpile-1.0.0-1.aarch64.rpm', 'woodpile_1.0.0_linux_arm64.tar.gz']);
  assert.deepEqual(p('unknown', 'unknown'), []);
});

test('"other downloads" lists everything else, and nothing is lost', () => {
  for (const platform of [{ os: 'windows', arch: 'x64' }, { os: 'linux', arch: 'arm64' }, { os: 'unknown', arch: 'unknown' }]) {
    const r = pick(RELEASE.assets, platform);
    assert.equal(r.best.length + r.others.length, NAMES.length - 1);
    assert.equal(r.checksums.name, 'checksums.txt');
  }
});

const nav = (userAgent, userAgentData) => ({ userAgent, userAgentData });

test('platform from user agents', async () => {
  const cases = [
    ['Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140 Safari/537.36', { os: 'windows', arch: 'x64' }],
    ['Mozilla/5.0 (Windows NT 10.0; ARM64) AppleWebKit/537.36 Edg/140', { os: 'windows', arch: 'arm64' }],
    ['Mozilla/5.0 (Macintosh; Intel Mac OS X 15_6) AppleWebKit/605.1.15 Version/19 Safari/605.1.15', { os: 'macos', arch: 'unknown' }],
    ['Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0', { os: 'linux', arch: 'x64' }],
    ['Mozilla/5.0 (X11; Linux aarch64; rv:140.0) Gecko/20100101 Firefox/140.0', { os: 'linux', arch: 'arm64' }],
    ['Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 Chrome/140 Mobile Safari/537.36', { os: 'unknown', arch: 'unknown' }],
    ['Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148', { os: 'unknown', arch: 'unknown' }],
    ['Mozilla/5.0 (X11; CrOS x86_64 16000.0.0) AppleWebKit/537.36 Chrome/140', { os: 'unknown', arch: 'unknown' }],
  ];
  for (const [ua, want] of cases) assert.deepEqual(await detectPlatform(nav(ua)), want, ua);
});

test('userAgentData wins where the browser has it (Windows on ARM reports x64 in its UA)', async () => {
  const uad = (platform, architecture, mobile = false) => ({ platform, mobile, getHighEntropyValues: async () => ({ architecture, bitness: '64' }) });
  const ua = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/140';
  assert.deepEqual(await detectPlatform(nav(ua, uad('Windows', 'arm'))), { os: 'windows', arch: 'arm64' });
  assert.deepEqual(await detectPlatform(nav(ua, uad('macOS', 'arm'))), { os: 'macos', arch: 'arm64' });
  assert.deepEqual(await detectPlatform(nav(ua, uad('Android', 'arm', true))), { os: 'unknown', arch: 'unknown' });
  const denied = { platform: 'Linux', mobile: false, getHighEntropyValues: async () => { throw new Error('no'); } };
  assert.deepEqual(await detectPlatform(nav('Mozilla/5.0 (X11; Linux x86_64) Chrome/140', denied)), { os: 'linux', arch: 'x64' });
});

const res = (status, body) => ({ status, ok: status >= 200 && status < 300, json: async () => body });

test('the release loads, and is cached for 10 minutes', async () => {
  const store = new Map();
  const storage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
  let calls = 0;
  const f = async (url) => {
    calls++;
    assert.equal(url, latestApiUrl());
    return res(200, RELEASE);
  };
  const a = await loadRelease(f, storage, 1_000);
  assert.equal(a.ok, true);
  assert.equal(a.release.tag_name, 'v1.0.0');
  await loadRelease(f, storage, 1_000 + 9 * 60_000);
  assert.equal(calls, 1);
  await loadRelease(f, storage, 1_000 + 11 * 60_000);
  assert.equal(calls, 2);
});

test('fallbacks: no release yet, rate limit, network error, server error, no files', async () => {
  assert.deepEqual(await loadRelease(async () => res(404, { message: 'Not Found' })), { ok: false, reason: 'none' });
  assert.deepEqual(await loadRelease(async () => res(403, { message: 'API rate limit exceeded' })), { ok: false, reason: 'rate' });
  assert.deepEqual(await loadRelease(async () => res(429, {})), { ok: false, reason: 'rate' });
  assert.deepEqual(await loadRelease(async () => { throw new TypeError('Failed to fetch'); }), { ok: false, reason: 'network' });
  assert.deepEqual(await loadRelease(async () => res(502, {})), { ok: false, reason: 'error' });
  assert.deepEqual(await loadRelease(async () => ({ status: 200, ok: true, json: async () => { throw new SyntaxError('bad'); } })), { ok: false, reason: 'error' });
  assert.deepEqual(await loadRelease(async () => res(200, { tag_name: 'v1.0.0', assets: [{ name: 'checksums.txt' }] })), { ok: false, reason: 'empty' });
});

test('one constant names the repository', () => {
  assert.equal(latestApiUrl(), `https://api.github.com/repos/${REPO.owner}/${REPO.name}/releases/latest`);
  assert.equal(releasesUrl(), `https://github.com/${REPO.owner}/${REPO.name}/releases/latest`);
});
