// Download picking for the site: which release files exist, and which one this visitor
// wants. Pure functions (no DOM), so site/test/download.test.mjs can test them in Node.
//
// File names come from the release workflow:
//   .goreleaser.yaml      woodpile_<v>_{linux_amd64,linux_arm64,darwin_universal}.tar.gz,
//                         woodpile_<v>_windows_{amd64,arm64}.zip, woodpile_<v>_{amd64,arm64}.deb,
//                         woodpile-<v>-1.{x86_64,aarch64}.rpm, checksums.txt
//   build-msi.ps1         woodpile_<v>_windows_{amd64,arm64}.msi
//   build-pkg.sh          woodpile_<v>_macos_universal.pkg

/** The one place the project's home is named. */
export const REPO = { owner: 'gmccollu', name: 'woodpile' };

export const repoUrl = (path = '') => `https://github.com/${REPO.owner}/${REPO.name}${path}`;
export const releasesUrl = () => repoUrl('/releases/latest');
export const latestApiUrl = () => `https://api.github.com/repos/${REPO.owner}/${REPO.name}/releases/latest`;

const ARCH = { amd64: 'x64', x86_64: 'x64', arm64: 'arm64', aarch64: 'arm64', universal: 'universal' };
const ARCH_LABEL = { x64: 'x64', arm64: 'ARM64', universal: 'Intel and Apple silicon' };

const RULES = [
  { re: /_windows_(amd64|arm64)\.msi$/, os: 'windows', kind: 'installer', label: 'Windows installer' },
  { re: /_macos_(universal)\.pkg$/, os: 'macos', kind: 'installer', label: 'macOS installer' },
  { re: /_(amd64|arm64)\.deb$/, os: 'linux', kind: 'installer', label: 'Debian / Ubuntu package' },
  { re: /-1\.(x86_64|aarch64)\.rpm$/, os: 'linux', kind: 'installer', label: 'Fedora / RHEL package' },
  { re: /_windows_(amd64|arm64)\.zip$/, os: 'windows', kind: 'portable', label: 'Windows portable' },
  { re: /_darwin_(universal)\.tar\.gz$/, os: 'macos', kind: 'portable', label: 'macOS portable' },
  { re: /_linux_(amd64|arm64)\.tar\.gz$/, os: 'linux', kind: 'portable', label: 'Linux portable' },
];

/** What a release file is, or null for files the site doesn't offer (checksums.txt is separate). */
export function classify(name) {
  for (const r of RULES) {
    const m = r.re.exec(name);
    if (m) {
      const arch = ARCH[m[1]];
      return { os: r.os, kind: r.kind, arch, label: `${r.label} (${ARCH_LABEL[arch]})`, format: name.split('.').slice(-1)[0] };
    }
  }
  return null;
}

/**
 * The visitor's OS and CPU: { os: windows|macos|linux|unknown, arch: x64|arm64|unknown }.
 * Uses navigator.userAgentData where the browser has it (Chromium), else the user agent.
 * Phones, tablets and ChromeOS are "unknown": nothing is highlighted, everything is listed.
 */
export async function detectPlatform(nav) {
  let os = 'unknown';
  let arch = 'unknown';
  const ua = nav.userAgent || '';
  const uad = nav.userAgentData;
  if (uad && !uad.mobile) {
    const p = String(uad.platform || '').toLowerCase();
    if (p.startsWith('win')) os = 'windows';
    else if (p.startsWith('mac')) os = 'macos';
    else if (p === 'linux') os = 'linux';
    try {
      const h = await uad.getHighEntropyValues(['architecture', 'bitness']);
      if (h.architecture === 'arm') arch = 'arm64';
      else if (h.architecture === 'x86') arch = 'x64';
    } catch {
      /* not allowed: fall back below */
    }
  }
  if (uad?.mobile || /Android|iPhone|iPad|iPod|Mobile|CrOS/.test(ua)) return { os: 'unknown', arch: 'unknown' };
  if (os === 'unknown') {
    if (/Windows NT/.test(ua)) os = 'windows';
    else if (/Macintosh|Mac OS X/.test(ua)) os = 'macos';
    else if (/Linux|X11/.test(ua)) os = 'linux';
  }
  if (arch === 'unknown') {
    if (/ARM64|aarch64|arm64|armv8/i.test(ua)) arch = 'arm64';
    else if (/Win64|WOW64|x64|x86_64|amd64/i.test(ua)) arch = 'x64';
  }
  return { os, arch };
}

/**
 * Splits a release's assets into the ones to highlight for this platform and the rest.
 * Windows and Linux default to x64 when the CPU can't be told (most PCs; the other build
 * is one line below). macOS builds are universal.
 */
export function pick(assets, platform) {
  const files = assets
    .map((a) => ({ ...a, info: classify(a.name) }))
    .filter((a) => a.info);
  const checksums = assets.find((a) => a.name === 'checksums.txt') || null;
  const arch = platform.arch === 'arm64' ? 'arm64' : 'x64';
  const order = { installer: 0, portable: 1 };
  const fmt = { msi: 0, pkg: 0, deb: 0, rpm: 1, zip: 2, gz: 2 };
  const archOrder = { universal: 0, x64: 0, arm64: 1 };
  const cmp = (a, b) =>
    order[a.info.kind] - order[b.info.kind] || fmt[a.info.format] - fmt[b.info.format] || archOrder[a.info.arch] - archOrder[b.info.arch];
  const best = files.filter((a) => a.info.os === platform.os && (a.info.arch === 'universal' || a.info.arch === arch)).sort(cmp);
  const osOrder = { windows: 0, macos: 1, linux: 2 };
  const others = files
    .filter((a) => !best.includes(a))
    .sort((a, b) => osOrder[a.info.os] - osOrder[b.info.os] || cmp(a, b));
  return { best, others, checksums };
}

/** "sha256:abc…" from the API's asset digest, or ''. */
export function sha256Of(asset) {
  const d = asset && asset.digest;
  return typeof d === 'string' && d.startsWith('sha256:') ? d.slice(7) : '';
}

const CACHE_KEY = 'woodpile.site.latest';
const CACHE_MS = 10 * 60_000;

/**
 * Fetches the latest release from the GitHub API. Never throws:
 *   { ok: true, release } or { ok: false, reason: 'none' | 'rate' | 'network' | 'error' | 'empty' }.
 * "none" is the normal state before 1.0: /releases/latest ignores prereleases.
 * A successful answer is cached for 10 minutes (sessionStorage), to stay well inside the
 * API's 60 requests an hour per address.
 */
export async function loadRelease(fetchFn, storage = null, now = Date.now()) {
  try {
    const hit = storage && JSON.parse(storage.getItem(CACHE_KEY) || 'null');
    if (hit && now - hit.at < CACHE_MS && hit.release) return { ok: true, release: hit.release };
  } catch {
    /* ignore a bad cache entry */
  }
  let res;
  try {
    res = await fetchFn(latestApiUrl(), { headers: { Accept: 'application/vnd.github+json' }, credentials: 'omit', referrerPolicy: 'no-referrer' });
  } catch {
    return { ok: false, reason: 'network' };
  }
  if (res.status === 404) return { ok: false, reason: 'none' };
  if (res.status === 403 || res.status === 429) return { ok: false, reason: 'rate' };
  if (!res.ok) return { ok: false, reason: 'error' };
  let release;
  try {
    release = await res.json();
  } catch {
    return { ok: false, reason: 'error' };
  }
  if (!release || !Array.isArray(release.assets) || !release.assets.some((a) => classify(a.name))) return { ok: false, reason: 'empty' };
  try {
    storage?.setItem(CACHE_KEY, JSON.stringify({ at: now, release }));
  } catch {
    /* storage full or disabled */
  }
  return { ok: true, release };
}

export const FALLBACK_TEXT = {
  none: 'No release has been published yet. When it is, the downloads appear here and on the releases page.',
  rate: "GitHub's API limit for your network was reached, so the list can't be shown here. The releases page has every file.",
  network: "GitHub couldn't be reached from this page. The releases page has every file.",
  error: "The release list couldn't be read. The releases page has every file.",
  empty: "The latest release has no downloadable files yet. The releases page has the details.",
};
