// releases.json is a contract (SPEC §10.2): Woodshed 1.1's update check reads it from
// https://gmccollu.github.io/woodshed/releases.json. These keys stay: { generated_at,
// stable, prerelease }, each a release or null, and in a release tag_name and html_url (the
// only two the checker reads). Removing or renaming any of them breaks every installed copy.
import test from 'node:test';
import assert from 'node:assert/strict';
import { summarize } from '../tools/releases.mjs';

const api = (tag, prerelease, published) => ({
  tag_name: tag,
  name: tag,
  draft: false,
  prerelease,
  published_at: published,
  html_url: `https://github.com/gmccollu/woodshed/releases/tag/${tag}`,
  assets: [{ name: 'woodshed.msi', size: 1, browser_download_url: 'https://example.invalid/x', digest: null }],
  body: 'notes', author: { login: 'someone' },
});

test('releases.json keeps the keys the update check reads', () => {
  const out = summarize([api('v1.1.0', false, '2026-12-01T00:00:00Z'), api('v1.2.0-rc1', true, '2026-12-05T00:00:00Z')]);
  assert.deepEqual(Object.keys(out).sort(), ['generated_at', 'prerelease', 'stable']);
  for (const [which, tag] of [['stable', 'v1.1.0'], ['prerelease', 'v1.2.0-rc1']]) {
    assert.equal(out[which].tag_name, tag, `${which}.tag_name`);
    assert.equal(out[which].html_url, `https://github.com/gmccollu/woodshed/releases/tag/${tag}`, `${which}.html_url`);
  }
  // Round-trips as JSON with the same shape (the file is JSON.stringify of this).
  assert.deepEqual(JSON.parse(JSON.stringify(out)), out);
});

test('releases.json: stable and prerelease are null, not missing, when there is none', () => {
  const out = summarize([]);
  assert.ok('stable' in out && 'prerelease' in out);
  assert.equal(out.stable, null);
  assert.equal(out.prerelease, null);
});
