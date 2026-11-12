// Unit tests for fields.ts: `npm test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { addToList, eventsQueryFor, fieldsFromQuery, fieldsToQuery, noFields, splitList } from './fields.ts';

const ALL: (number | 'unknown')[] = [0, 1, 2, 3, 4, 5, 6, 7, 'unknown'];

test('lists are trimmed and blanks dropped, like the server', () => {
  assert.deepEqual(splitList(' sshd, %FW-6-*,, ,kernel '), ['sshd', '%FW-6-*', 'kernel']);
  assert.deepEqual(splitList(''), []);
});

test('clicking a value adds it once, ignoring case and a leading %', () => {
  assert.equal(addToList('', 'sshd'), 'sshd');
  assert.equal(addToList('sshd', 'kernel'), 'sshd, kernel');
  assert.equal(addToList('sshd, kernel', 'SSHD'), 'sshd, kernel');
  assert.equal(addToList('FW-6-302013', '%FW-6-302013'), 'FW-6-302013');
  assert.equal(addToList('a,,b ', ' '), 'a, b');
});

test('the filters round-trip through the URL, and only what is set is written', () => {
  const p = new URLSearchParams();
  fieldsToQuery(noFields(), p);
  assert.equal(p.toString(), '');
  fieldsToQuery({ app: 'sshd, %FW-6-*', host: ' web-01 ', exclude: 'keepalive' }, p);
  assert.equal(p.toString(), 'app=sshd%2C%25FW-6-*&host=web-01&ex=keepalive');
  assert.deepEqual(fieldsFromQuery(new URLSearchParams(p.toString())), { app: 'sshd, %FW-6-*', host: 'web-01', exclude: 'keepalive' });
  assert.deepEqual(fieldsFromQuery(new URLSearchParams('q=x')), noFields());
});

test('Open in Events carries the live tail view over the last hour', () => {
  assert.equal(eventsQueryFor({ devices: [], group: '', ungrouped: false, severities: ALL, q: '' }), 'r=1h');
  assert.equal(
    eventsQueryFor({ devices: ['10.1.0.1', '10.1.0.2'], group: 'HQ', ungrouped: false, severities: [3, 4, 'unknown'], q: ' deny ' }),
    'q=deny&dev=10.1.0.1%2C10.1.0.2&r=1h&sev=3%2C4%2Cunknown',
  );
  assert.equal(eventsQueryFor({ devices: [], group: 'HQ', ungrouped: false, severities: ALL, q: '' }), 'group=HQ&r=1h');
  assert.equal(eventsQueryFor({ devices: [], group: '', ungrouped: true, severities: [], q: '' }), 'ug=1&r=1h&sev=none');
});
