// Syslog severities (RFC 5424 §6.2.1). The API sends a number 0–7; for messages
// that couldn't be parsed it may send null, a negative number or omit it (the
// contract doesn't say), so anything outside 0–7 is treated as "unknown".

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
