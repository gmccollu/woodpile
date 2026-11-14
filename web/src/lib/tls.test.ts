// Unit tests for tls.ts: `npm test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { daysLeft, hostFirewallCommands, tlsHealth, tlsReason, tlsReasonShort, ymd } from './tls.ts';

test('every failure reason has plain words; unknown codes are shown as given', () => {
  for (const r of ['cert_rejected', 'version', 'cipher', 'not_tls', 'timeout', 'closed', 'busy', 'other']) {
    assert.notEqual(tlsReason(r), r);
    assert.notEqual(tlsReasonShort(r), r);
  }
  assert.equal(tlsReason('new_reason'), 'new_reason');
});

test('device TLS health', () => {
  const t = { connections_24h: 4, version: 'TLS 1.3', cipher: 'x', last_connected: null, failures_24h: 0, last_failure: null };
  assert.equal(tlsHealth(t), 'ok');
  assert.equal(tlsHealth({ ...t, failures_24h: 2 }), 'some');
  assert.equal(tlsHealth({ ...t, connections_24h: 0, failures_24h: 2 }), 'failing');
});

test('host firewall commands use the configured port', () => {
  const c = hostFirewallCommands(16514);
  assert.equal(c.ufw, 'sudo ufw allow 16514/tcp');
  assert.equal(c.firewalld, 'sudo firewall-cmd --permanent --add-port=16514/tcp && sudo firewall-cmd --reload');
});

test('dates', () => {
  assert.equal(ymd('2037-12-31T23:59:59Z'), '2037-12-31');
  const now = Date.parse('2026-11-13T00:00:00Z');
  assert.equal(daysLeft('2026-12-23T00:00:00Z', now), 40);
  assert.equal(daysLeft('2026-11-12T00:00:00Z', now), -1);
});
