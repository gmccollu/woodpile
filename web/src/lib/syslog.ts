// Display-only split of a raw syslog line into PRI, header and message, so the
// live tail can show the message without repeating the time and host. The raw
// text is never changed; anything that doesn't look like RFC 3164 or RFC 5424
// is shown whole.

export interface RawParts {
  pri: string;
  header: string;
  body: string;
}

const PRI = /^<\d{1,3}>/;
// RFC 5424: VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD|-]
const R5424 = /^(1 \S+ \S+ )(\S+) (\S+) \S+ (?:-|(?:\[(?:[^\]\\]|\\.)*\])+) ?/;
// RFC 3164: "Mmm dd hh:mm:ss HOST " (optionally with fractional seconds or a year)
const R3164 = /^([A-Z][a-z]{2} [ \d]\d (?:\d{4} )?\d\d:\d\d:\d\d(?:\.\d+)? \S+ )/;

export function splitRaw(raw: string): RawParts {
  const p = PRI.exec(raw);
  const pri = p ? p[0] : '';
  const rest = raw.slice(pri.length);
  const m5 = R5424.exec(rest);
  if (m5) {
    const app = m5[2] === '-' ? '' : m5[2] + (m5[3] !== '-' ? `[${m5[3]}]` : '') + ': ';
    return { pri, header: m5[0], body: app + rest.slice(m5[0].length) };
  }
  const m3 = R3164.exec(rest);
  if (m3) return { pri, header: m3[1], body: rest.slice(m3[1].length) };
  return { pri, header: '', body: rest };
}
