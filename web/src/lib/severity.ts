// Syslog severities (RFC 5424 §6.2.1). The API sends 0–7, or null when the message
// had no parseable PRI. Anything else outside 0–7 is also treated as "unknown",
// defensively.

export const SEVERITIES = [
  { n: 0, short: 'emerg', name: 'Emergency' },
  { n: 1, short: 'alert', name: 'Alert' },
  { n: 2, short: 'crit', name: 'Critical' },
  { n: 3, short: 'err', name: 'Error' },
  { n: 4, short: 'warn', name: 'Warning' },
  { n: 5, short: 'notice', name: 'Notice' },
  { n: 6, short: 'info', name: 'Informational' },
  { n: 7, short: 'debug', name: 'Debug' },
] as const;

export function isKnown(sev: number | null | undefined): sev is number {
  return typeof sev === 'number' && Number.isInteger(sev) && sev >= 0 && sev <= 7;
}

export function sevLabel(sev: number | null | undefined): string {
  return isKnown(sev) ? SEVERITIES[sev].short : 'unknown';
}

export function sevClass(sev: number | null | undefined): string {
  return isKnown(sev) ? `sev-${sev}` : 'sev-u';
}
