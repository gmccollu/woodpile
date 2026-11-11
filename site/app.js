// The site's only script: repo links from one constant, and the download section.
// Remote data is only ever inserted with textContent, never as HTML.
import { FALLBACK_TEXT, detectPlatform, loadRelease, pick, releasesUrl, repoUrl, sha256Of } from './download.js';

// Links to the repository: the HTML has working hrefs for visitors without JavaScript;
// this keeps them in step with REPO in download.js.
for (const a of document.querySelectorAll('a[data-gh]')) a.href = repoUrl(a.dataset.gh);
for (const a of document.querySelectorAll('a[data-releases]')) a.href = releasesUrl();

const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
};

const MB = (n) => `${(n / 1024 / 1024).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MB`;
const OS_NAME = { windows: 'Windows', macos: 'macOS', linux: 'Linux' };

function assetCard(a, best) {
  const card = el('a', best ? 'asset best' : 'asset');
  card.href = a.browser_download_url;
  card.append(
    el('span', 'k', a.info.kind === 'installer' ? 'Installer · runs as a service' : 'Portable · nothing installed'),
    el('span', 'n', a.info.label),
    el('span', 'f', `${a.name} · ${MB(a.size || 0)}`),
  );
  const sum = sha256Of(a);
  if (sum) {
    const s = el('span', 'f', `SHA-256 ${sum.slice(0, 16)}…`);
    s.title = sum;
    card.append(s);
  }
  return card;
}

async function downloads() {
  const status = document.getElementById('dl-status');
  const primary = document.getElementById('dl-primary');
  const others = document.getElementById('dl-others');
  const body = document.getElementById('dl-others-body');
  const sums = document.getElementById('dl-sums');
  if (!status || !primary) return;

  const [platform, result] = await Promise.all([detectPlatform(navigator), loadRelease(fetch.bind(window), window.sessionStorage)]);
  if (!result.ok) {
    status.textContent = FALLBACK_TEXT[result.reason] || FALLBACK_TEXT.error;
    status.classList.add('notice');
    const link = el('a', 'btn', 'Open the releases page');
    link.href = releasesUrl();
    primary.replaceChildren(link);
    return;
  }

  const rel = result.release;
  const { best, others: rest, checksums } = pick(rel.assets, platform);
  const version = String(rel.tag_name || rel.name || '').replace(/^v/, '');
  status.textContent = best.length
    ? `Woodpile ${version} for ${OS_NAME[platform.os]}${platform.os !== 'macos' && platform.arch === 'arm64' ? ' on ARM' : ''}:`
    : `Woodpile ${version}. It runs on a Windows, macOS or Linux computer; pick the file for the one you'll use:`;
  primary.replaceChildren(...best.map((a) => assetCard(a, true)));

  if (others && body) {
    body.replaceChildren(
      ...rest.map((a) => {
        const tr = el('tr');
        const link = el('a', '', a.name);
        link.href = a.browser_download_url;
        const name = el('td');
        name.append(link);
        const sum = sha256Of(a);
        const sumTd = el('td', 'sum hide-sm', sum || '');
        if (sum) sumTd.title = sum;
        tr.append(el('td', '', a.info.label), name, el('td', 'hide-sm', MB(a.size || 0)), sumTd);
        return tr;
      }),
    );
    others.hidden = false;
    // Nothing to highlight (a phone, or an unknown system): show the full list straight away.
    if (!best.length) {
      others.open = true;
      const summary = others.querySelector('summary');
      if (summary) summary.textContent = 'All downloads';
    }
  }
  if (sums) {
    sums.replaceChildren();
    if (checksums) {
      sums.append('Check a download against ');
      const link = el('a', '', 'checksums.txt');
      link.href = checksums.browser_download_url;
      sums.append(link, ' (SHA-256), for example ');
      sums.append(el('code', '', 'sha256sum -c checksums.txt --ignore-missing'), '.');
    }
    const rl = el('a', '', `Release notes for ${version}`);
    rl.href = rel.html_url || releasesUrl();
    sums.append(' ', rl, '.');
  }
}

downloads();
