// Builds site/releases.json for the download section, at deploy time (pages.yml), so the
// page never calls the GitHub API from the visitor's browser (shared corporate egress
// addresses use up the anonymous limit of 60 requests an hour).
//
//   gh api --paginate --slurp "repos/OWNER/REPO/releases?per_page=100" | node site/tools/releases.mjs > releases.json
//
// Input: the releases API's JSON (an array, or an array of pages from --slurp).
// Output: { generated_at, stable, prerelease }, each a release or null, with only what the
// page needs. Drafts are never included.

import { readFileSync } from 'node:fs';

const keepAsset = (a) => ({
  name: a.name,
  size: a.size,
  browser_download_url: a.browser_download_url,
  digest: typeof a.digest === 'string' ? a.digest : null,
});

const keepRelease = (r) =>
  r && {
    tag_name: r.tag_name,
    name: r.name || r.tag_name,
    published_at: r.published_at,
    html_url: r.html_url,
    assets: (r.assets || []).map(keepAsset),
  };

/** The latest stable release and the latest prerelease (by published_at), drafts left out. */
export function summarize(input, now = new Date()) {
  const all = [input].flat(Infinity).filter((r) => r && typeof r === 'object' && !r.draft && r.published_at);
  all.sort((a, b) => Date.parse(b.published_at) - Date.parse(a.published_at));
  return {
    generated_at: now.toISOString(),
    stable: keepRelease(all.find((r) => !r.prerelease)) || null,
    prerelease: keepRelease(all.find((r) => r.prerelease)) || null,
  };
}

// Run as a script: read stdin, write stdout.
if (import.meta.url === `file://${process.argv[1]}`) {
  const raw = readFileSync(0, 'utf8');
  process.stdout.write(`${JSON.stringify(summarize(raw.trim() ? JSON.parse(raw) : []), null, 1)}\n`);
}
