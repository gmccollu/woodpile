// Plain words for the TLS syslog listener's handshake failures (SPEC §10.3.4), shared by
// Settings → Syslog over TLS and the Devices page.
import type { DeviceTls } from './api';

const REASONS: Record<string, string> = {
  cert_rejected: "Certificate refused: the device doesn't trust the CA, or its name check failed",
  version: 'No TLS version in common: often an older sender that needs TLS 1.0 or 1.1',
  cipher: 'No cipher suite in common',
  not_tls: 'Not TLS: usually plain syslog sent to the TLS port',
  timeout: 'The handshake didn’t finish within 10 s',
  closed: 'The sender closed the connection during the handshake',
  busy: 'Too many handshakes at once: closed straight away',
  other: 'Another error',
};

/** A failure reason code in plain words; an unknown code is shown as given. */
export const tlsReason = (code: string): string => REASONS[code] ?? code;

/** The short form, for tight spaces (the Devices badge). */
const SHORT: Record<string, string> = {
  cert_rejected: 'certificate refused',
  version: 'no TLS version in common',
  cipher: 'no cipher in common',
  not_tls: 'not TLS',
  timeout: 'timed out',
  closed: 'closed by the sender',
  busy: 'too many at once',
  other: 'other error',
};
export const tlsReasonShort = (code: string): string => SHORT[code] ?? code;

/** How a device's TLS is doing: "ok" (no failures), "some" failures, or "failing" (never connected). */
export function tlsHealth(t: DeviceTls): 'ok' | 'some' | 'failing' {
  if (t.failures_24h <= 0) return 'ok';
  return t.connections_24h > 0 ? 'some' : 'failing';
}

/** Linux: the host-firewall commands that open the TLS port (SPEC §10.3.6), for ufw and for firewalld. */
export function hostFirewallCommands(port: number): { ufw: string; firewalld: string } {
  return {
    ufw: `sudo ufw allow ${port}/tcp`,
    firewalld: `sudo firewall-cmd --permanent --add-port=${port}/tcp && sudo firewall-cmd --reload`,
  };
}

/** The date part of an RFC 3339 time, in UTC ("2037-12-31"), as the banners and the CLI show it. */
export const ymd = (iso: string): string => iso.slice(0, 10);

/** Whole days from now until `iso` (negative once it's past). */
export const daysLeft = (iso: string, now = Date.now()): number => Math.floor((Date.parse(iso) - now) / 86_400_000);
