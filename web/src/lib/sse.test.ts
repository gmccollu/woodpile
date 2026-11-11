// Unit tests for the SSE parser: `npm test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { SseParser, type SseEvent } from './sse.ts';

function parse(chunks: string[]): { events: SseEvent[]; retry: number | null } {
  const events: SseEvent[] = [];
  const p = new SseParser((e) => events.push(e));
  for (const c of chunks) p.push(c);
  return { events, retry: p.retry };
}

test('message and named events, heartbeats ignored', () => {
  const { events } = parse([': connected\n\nevent: message\ndata: {"a":1}\n\n: ping\n\nevent: sampled\ndata: {"rate":5}\n\n']);
  assert.deepEqual(
    events.map((e) => [e.event, e.data]),
    [
      ['message', '{"a":1}'],
      ['sampled', '{"rate":5}'],
    ],
  );
});

test('a default event type, multi-line data, and no space after the colon', () => {
  const { events } = parse(['data:one\ndata: two\n\n']);
  assert.deepEqual(events, [{ event: 'message', data: 'one\ntwo', id: '' }]);
});

test('CRLF and CR line endings, including a CRLF split across chunks', () => {
  const { events } = parse(['event: message\r', '\ndata: x\r\r', 'data: y\r\n\r\n']);
  assert.deepEqual(
    events.map((e) => e.data),
    ['x', 'y'],
  );
});

test('any chunk boundary, even inside a field name or a multi-byte JSON value', () => {
  const text = 'event: message\ndata: {"raw":"héllo ↵"}\n\n';
  for (let cut = 1; cut < text.length; cut++) {
    const { events } = parse([text.slice(0, cut), text.slice(cut)]);
    assert.equal(events.length, 1, `cut at ${cut}`);
    assert.equal(events[0].data, '{"raw":"héllo ↵"}');
  }
});

test('an unfinished event is not dispatched; retry and id are read', () => {
  const { events, retry } = parse(['retry: 5000\nid: 7\ndata: done\n\ndata: half']);
  assert.equal(events.length, 1);
  assert.equal(events[0].id, '7');
  assert.equal(retry, 5000);
});
