// Unit tests for events.ts and range.ts's preset rounding: `npm test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  countedLine,
  differsFromSaved,
  domainChips,
  domainExclude,
  domainInclude,
  domainRemove,
  extrasFromQuery,
  extrasToQuery,
  formOfSaved,
  modeFromQuery,
  modeToQuery,
  noExtras,
  savedQueryOf,
  snapSelection,
  span,
  type EventsForm,
} from './events.ts';
import { resolveRange } from './range.ts';

const SEV = ['0', '1', '2', '3', '4', '5', '6', '7', 'unknown'];

test('the mode switch in the URL: text has no parameter', () => {
  for (const m of ['text', 'terms', 'regex'] as const) {
    const p = new URLSearchParams();
    modeToQuery(m, p);
    assert.equal(modeFromQuery(p), m);
  }
  const p = new URLSearchParams();
  modeToQuery('text', p);
  assert.equal(p.toString(), '');
  assert.equal(modeFromQuery(new URLSearchParams('re=1&syn=terms')), 'regex');
});

test('extras round-trip through the URL', () => {
  const e = { fac: ['4', 'unknown'], xdev: ['10.1.0.9'], xapp: ['kernel', '%FW-6-*'], xhost: ['lab-01'] };
  const p = new URLSearchParams();
  extrasToQuery(e, p);
  assert.equal(p.toString(), 'fac=4%2Cunknown&xdev=10.1.0.9&xapp=kernel%2C%25FW-6-*&xhost=lab-01');
  assert.deepEqual(extrasFromQuery(p), e);
  const q = new URLSearchParams();
  extrasToQuery(noExtras(), q);
  assert.equal(q.toString(), '');
});

test('closed fields: include narrows or adds, exclude sends the complement', () => {
  assert.deepEqual(domainInclude([], '3'), ['3']);
  assert.deepEqual(domainInclude(['3'], '4'), ['3', '4']);
  assert.deepEqual(domainInclude(['3'], '3'), ['3']); // no cycling
  assert.deepEqual(domainInclude(['none'], '3'), ['3']);
  assert.deepEqual(domainExclude([], SEV, '7'), ['0', '1', '2', '3', '4', '5', '6', 'unknown']);
  assert.deepEqual(domainExclude(['3', '4'], SEV, '4'), ['3']);
  assert.deepEqual(domainExclude(['3'], SEV, '3'), ['none']);
  assert.deepEqual(domainRemove(['3', '4'], SEV, '4', true), ['3']);
  assert.deepEqual(domainRemove(['3'], SEV, '3', true), []);
  assert.deepEqual(domainRemove(domainExclude([], SEV, '7'), SEV, '7', false), []);
  assert.deepEqual(domainChips([], SEV), []);
  assert.deepEqual(domainChips(['3'], SEV), [{ value: '3', included: true }]);
  assert.deepEqual(domainChips(domainExclude([], SEV, '7'), SEV), [{ value: '7', included: false }]);
});

test('a histogram drag snaps to bucket edges and stays in the range', () => {
  const starts = ['2026-11-13T14:00:00.000Z', '2026-11-13T14:01:00.000Z', '2026-11-13T14:02:00.000Z', '2026-11-13T14:03:00.000Z'];
  const r = snapSelection(starts, 60, 2, 1, '2026-11-13T14:00:30.000Z', '2026-11-13T14:03:30.000Z');
  assert.deepEqual(r, { from: '2026-11-13T14:01:00.000Z', to: '2026-11-13T14:03:00.000Z' });
  const edge = snapSelection(starts, 60, 0, 3, '2026-11-13T14:00:30.000Z', '2026-11-13T14:03:30.000Z');
  assert.deepEqual(edge, { from: '2026-11-13T14:00:30.000Z', to: '2026-11-13T14:03:30.000Z' });
});

test('the counted span, in UTC', () => {
  assert.equal(span('2026-11-13T14:02:00Z', '2026-11-13T15:00:00Z', true), '14:02–15:00');
  assert.equal(countedLine('2026-11-13T12:00:00Z', '2026-11-13T15:00:00Z', '2026-11-13T14:02:00Z', true), 'Counted 14:02–15:00 of 12:00–15:00');
  assert.equal(countedLine('2026-11-13T12:00:00Z', '2026-11-13T15:00:00Z', '2026-11-13T12:00:00Z', true), 'Counted all of 12:00–15:00');
  assert.equal(span('2026-11-12T14:00:00Z', '2026-11-13T15:00:00Z', true), '11-12 14:00–11-13 15:00');
  assert.equal(span('2026-11-13T14:02:07Z', '2026-11-13T14:03:00Z', true), '14:02:07–14:03');
});

const form = (o: Partial<EventsForm> = {}): EventsForm => ({
  q: 'deny NOT 10.0.0.1',
  mode: 'terms',
  cs: false,
  scope: { mode: 'devices', group: '', devices: ['10.1.0.1'] },
  range: { preset: '1h', from: '', to: '' },
  sev: [0, 1, 2, 3, 4, 5, 6, 7, 'unknown'],
  order: 'desc',
  app: '%FW-6-*',
  host: '',
  exclude: '',
  extras: noExtras(),
  ...o,
});

test('a saved query holds every Events parameter; presets save as relative', () => {
  const q = savedQueryOf(form());
  assert.deepEqual(q, {
    q: 'deny NOT 10.0.0.1', syntax: 'terms', regex: false, case: false, exclude: '',
    devices: ['10.1.0.1'], group: '', ungrouped: false, exclude_devices: [],
    severities: [], facilities: [], app: ['%FW-6-*'], exclude_app: [], host: [], exclude_host: [],
    order: 'desc', range: { last: '1h' },
  });
  assert.deepEqual(savedQueryOf(form({ sev: [] })).severities, ['none']);
  assert.deepEqual(savedQueryOf(form({ extras: { ...noExtras(), fac: ['4', 'unknown'] } })).facilities, [4, 'unknown']);
  const custom = savedQueryOf(form({ range: { preset: 'custom', from: '2026-11-13T12:00:00Z', to: '2026-11-13T15:00:00Z' } }));
  assert.deepEqual(custom.range, { from: '2026-11-13T12:00:00Z', to: '2026-11-13T15:00:00Z' });
});

test('opening a saved query fills the form; "changed" once it differs', () => {
  const f = form({ extras: { fac: ['16'], xdev: ['10.1.0.9'], xapp: ['kernel'], xhost: [] }, sev: [3, 4] });
  const saved = savedQueryOf(f);
  assert.deepEqual(formOfSaved(saved), f);
  assert.ok(!differsFromSaved(f, saved));
  assert.ok(!differsFromSaved(form(), { q: 'deny NOT 10.0.0.1', syntax: 'terms', devices: ['10.1.0.1'], app: ['%FW-6-*'], range: { last: '1h' } }));
  assert.ok(differsFromSaved({ ...f, q: 'deny' }, saved));
  assert.ok(differsFromSaved({ ...f, range: { preset: '24h', from: '', to: '' } }, saved));
  // Missing keys take their defaults: all devices, all severities, the last hour.
  const d = formOfSaved({});
  assert.equal(d.scope.mode, 'all');
  assert.equal(d.sev.length, 9);
  assert.equal(d.range.preset, '1h');
  assert.equal(formOfSaved({ regex: true, syntax: 'text' }).mode, 'regex');
  // A relative range that isn't a preset opens as a custom range ending now.
  const now = Date.parse('2026-11-13T15:00:00Z');
  assert.deepEqual(formOfSaved({ range: { last: '30m' } }, now).range, { preset: 'custom', from: '2026-11-13T14:30:00.000Z', to: '2026-11-13T15:00:00.000Z' });
});

test('preset ranges end at now rounded up to the whole minute', () => {
  const now = Date.parse('2026-11-13T14:59:12.345Z');
  assert.deepEqual(resolveRange({ preset: '1h', from: '', to: '' }, now), { from: '2026-11-13T14:00:00.000Z', to: '2026-11-13T15:00:00.000Z' });
  const exact = Date.parse('2026-11-13T15:00:00.000Z');
  assert.equal(resolveRange({ preset: '15m', from: '', to: '' }, exact).to, '2026-11-13T15:00:00.000Z');
  assert.deepEqual(resolveRange({ preset: 'custom', from: 'a', to: 'b' }, now), { from: 'a', to: 'b' });
});
