package syslog

import (
	"bytes"
	"testing"
	"time"

	"woodpile/internal/model"
)

const unk = model.SeverityUnknown

// recv is the default received time for the table: 2026-11-10 20:41:08 UTC.
var recv = time.Date(2026, 11, 10, 20, 41, 8, 0, time.UTC)

func utc(y int, mo time.Month, d, h, mi, s, ns int) time.Time {
	return time.Date(y, mo, d, h, mi, s, ns, time.UTC)
}

type parseCase struct {
	name     string
	raw      string
	received time.Time // recv when zero, unless noRecv
	noRecv   bool
	fac, sev int
	ts       time.Time
	host     string
	app      string
}

var parseCases = []parseCase{
	// RFC 5424, including the RFC's own examples (example.com hosts).
	{name: "5424 rfc example 1",
		raw: "<34>1 2003-10-11T22:14:15.003Z mymachine.example.com su - ID47 - \xEF\xBB\xBF'su root' failed for lonvick on /dev/pts/8",
		fac: 4, sev: 2, ts: utc(2003, 10, 11, 22, 14, 15, 3e6), host: "mymachine.example.com", app: "su"},
	{name: "5424 rfc example 2 offset and microseconds",
		raw: "<165>1 2003-08-24T05:14:15.000003-07:00 192.0.2.1 myproc 8710 - - %% It's time to make the do-nuts.",
		fac: 20, sev: 5, ts: utc(2003, 8, 24, 12, 14, 15, 3000), host: "192.0.2.1", app: "myproc"},
	{name: "5424 rfc example 3 structured data",
		raw: `<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3" eventSource="Application" eventID="1011"] An application event log entry...`,
		fac: 20, sev: 5, ts: utc(2003, 10, 11, 22, 14, 15, 3e6), host: "mymachine.example.com", app: "evntslog"},
	{name: "5424 rfc example 4 SD only",
		raw: `<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3"][examplePriority@32473 class="high"]`,
		fac: 20, sev: 5, ts: utc(2003, 10, 11, 22, 14, 15, 3e6), host: "mymachine.example.com", app: "evntslog"},
	{name: "5424 all nil",
		raw: "<13>1 - - - - - -", fac: 1, sev: 5},
	{name: "5424 nil timestamp with host",
		raw: "<14>1 - edge-fw-01 - - - - link up", fac: 1, sev: 6, host: "edge-fw-01"},
	{name: "5424 nil host and app",
		raw: "<14>1 2026-11-10T20:41:07Z - - - - - x", fac: 1, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0)},
	{name: "5424 positive offset with nanoseconds",
		raw: "<190>1 2026-11-10T21:41:07.123456789+01:00 edge-fw-01 dhcpd 4411 LEASE - lease granted",
		fac: 23, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 123456789), host: "edge-fw-01", app: "dhcpd"},
	{name: "5424 too many fraction digits are cut",
		raw: "<14>1 2026-11-10T20:41:07.1234567891234Z h a - - -",
		fac: 1, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 123456789), host: "h", app: "a"},
	{name: "5424 lower-case t and z",
		raw: "<14>1 2026-11-10t20:41:07z edge-fw-01 app - - -", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "5424 IPv6 host",
		raw: "<14>1 2026-11-10T20:41:07Z 2001:db8::1 ntpd - - - clock synced", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "2001:db8::1", app: "ntpd"},
	{name: "5424 header ends after timestamp",
		raw: "<14>1 2026-11-10T20:41:07Z", fac: 1, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0)},
	{name: "5424 header ends after host",
		raw: "<14>1 2026-11-10T20:41:07Z edge-fw-01", fac: 1, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01"},
	{name: "5424 without PRI",
		raw: "1 2026-11-10T20:41:07Z edge-fw-01 app 22 - - hello", fac: unk, sev: unk,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "5424 space instead of T",
		raw: "<14>1 2026-11-10 20:41:07Z edge-fw-01 app - - -", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "5424 impossible date leaves timestamp zero",
		raw: "<14>1 2026-02-30T20:41:07Z edge-fw-01 app - - -", fac: 1, sev: 6, host: "edge-fw-01", app: "app"},
	{name: "5424 app over 64 bytes is dropped",
		raw: "<14>1 2026-11-10T20:41:07Z h " + string(bytes.Repeat([]byte("a"), 65)) + " - - -",
		fac: 1, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "h"},
	{name: "5424 non-ASCII host is dropped",
		raw: "<14>1 2026-11-10T20:41:07Z h\xc3\xa9 app - - -", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), app: "app"},
	{name: "5424 BOM after PRI",
		raw: "<14>\xEF\xBB\xBF1 2026-11-10T20:41:07Z edge-fw-01 app - - -", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},

	// RFC 3164 and its relatives.
	{name: "3164 rfc example",
		raw:      "<34>Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8",
		received: utc(2026, 10, 12, 0, 0, 0, 0),
		fac:      4, sev: 2, ts: utc(2026, 10, 11, 22, 14, 15, 0), host: "mymachine", app: "su"},
	{name: "3164 rfc example without tag",
		raw:      "<13>Feb  5 17:32:18 192.0.2.99 Use the BFG!",
		received: utc(2026, 2, 5, 17, 32, 19, 0),
		fac:      1, sev: 5, ts: utc(2026, 2, 5, 17, 32, 18, 0), host: "192.0.2.99"},
	{name: "3164 rfc example relayed",
		raw:      "<0>Oct 22 10:52:12 scapegoat 1990 Oct 22 10:52:01 TZ-6 scapegoat.dmz.example.org 192.0.2.3 sched[0]: That's All Folks!",
		received: utc(2026, 10, 22, 10, 52, 13, 0),
		fac:      0, sev: 0, ts: utc(2026, 10, 22, 10, 52, 12, 0), host: "scapegoat"},
	{name: "3164 sshd with pid",
		raw: "<86>Nov 10 20:41:07 edge-fw-01 sshd[4721]: Accepted publickey for admin from 198.51.100.7 port 52114 ssh2",
		fac: 10, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "sshd"},
	{name: "3164 space-padded day",
		raw: "<30>Nov  5 09:01:02 web-01 systemd[1]: Started Daily apt download activities.",
		fac: 3, sev: 6, ts: utc(2026, 11, 5, 9, 1, 2, 0), host: "web-01", app: "systemd"},
	{name: "3164 single-digit day",
		raw: "<30>Nov 5 09:01:02 web-01 systemd[1]: x", fac: 3, sev: 6,
		ts: utc(2026, 11, 5, 9, 1, 2, 0), host: "web-01", app: "systemd"},
	{name: "3164 cron upper-case tag",
		raw: "<78>Nov 10 20:41:00 web-01 CRON[12345]: (root) CMD (command -v debian-sa1 > /dev/null)",
		fac: 9, sev: 6, ts: utc(2026, 11, 10, 20, 41, 0, 0), host: "web-01", app: "CRON"},
	{name: "3164 slash in tag",
		raw: "<22>Nov 10 20:41:07 mail-01 postfix/smtpd[2211]: connect from unknown[203.0.113.9]",
		fac: 2, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "mail-01", app: "postfix/smtpd"},
	{name: "3164 kernel colon tag",
		raw: "<4>Nov 10 20:41:07 web-01 kernel: [ 1234.567890] eth0: link up",
		fac: 0, sev: 4, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "web-01", app: "kernel"},
	{name: "3164 tag without colon after pid",
		raw: "<14>Nov 10 20:41:07 web-01 app[77] started", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "web-01", app: "app"},
	{name: "3164 FQDN host",
		raw: "<14>Nov 10 20:41:07 core-sw-02.site.example.net lldpd[900]: neighbor added", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "core-sw-02.site.example.net", app: "lldpd"},
	{name: "3164 IPv6 host",
		raw: "<14>Nov 10 20:41:07 2001:db8::5 app: hi", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "2001:db8::5", app: "app"},
	{name: "3164 missing hostname, pid tag",
		raw: "<13>Nov 10 20:41:07 sshd[1]: Server listening on 0.0.0.0 port 22.", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), app: "sshd"},
	{name: "3164 missing hostname, colon tag",
		raw: "<13>Nov 10 20:41:07 kernel: Out of memory", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), app: "kernel"},
	{name: "3164 nil hostname",
		raw: "<13>Nov 10 20:41:07 - app: x", fac: 1, sev: 5, ts: utc(2026, 11, 10, 20, 41, 7, 0), app: "app"},
	{name: "3164 host with no tag",
		raw: "<13>Nov 10 20:41:07 edge-fw-01 interface eth1 down", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01"},
	{name: "3164 timestamp only",
		raw: "<13>Nov 10 20:41:07", fac: 1, sev: 5, ts: utc(2026, 11, 10, 20, 41, 7, 0)},
	{name: "3164 lower-case month",
		raw: "<13>nov 10 20:41:07 edge-fw-01 app: x", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "3164 tabs between fields",
		raw: "<13>Nov 10 20:41:07\tedge-fw-01\tapp: x", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "3164 milliseconds",
		raw: "<13>Nov 10 20:41:07.123 edge-fw-01 app: x", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 123e6), host: "edge-fw-01", app: "app"},
	{name: "3164 comma fraction",
		raw: "<13>Nov 10 20:41:07,5 edge-fw-01 app: x", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 5e8), host: "edge-fw-01", app: "app"},
	{name: "3164 impossible day keeps host and app",
		raw: "<13>Nov 31 20:41:07 edge-fw-01 app: x", fac: 1, sev: 5, host: "edge-fw-01", app: "app"},
	{name: "3164 hour out of range",
		raw: "<13>Nov 10 25:41:07 edge-fw-01 app: x", fac: 1, sev: 5, host: "edge-fw-01", app: "app"},
	{name: "3164 host token with invalid chars",
		raw: "<13>Nov 10 20:41:07 user=admin logged in", fac: 1, sev: 5, ts: utc(2026, 11, 10, 20, 41, 7, 0)},
	{name: "3164 embedded newline after tag",
		raw: "<13>Nov 10 20:41:07 edge-fw-01 app: line one\nline two", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},

	// Leading years, trailing years and zones.
	{name: "leading year with ms and %-tag",
		raw: "<166>2026 Nov 10 20:41:07.123 edge-fw-01 %FW-6-302013: Built outbound TCP connection 12 for outside:198.51.100.7/443",
		fac: 20, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 123e6), host: "edge-fw-01", app: "%FW-6-302013"},
	{name: "year after day, colon, no host",
		raw: "<166>Nov 10 2026 20:41:07: %FW-6-302013: Built inbound UDP connection",
		fac: 20, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0), app: "%FW-6-302013"},
	{name: "year after day, host, lone colon",
		raw: "<166>Nov 10 2026 20:41:07 edge-fw-01 : %FW-6-302013: Built inbound UDP connection",
		fac: 20, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "%FW-6-302013"},
	{name: "year after day, host, %-tag",
		raw: "<166>Nov 10 2026 20:41:07 edge-fw-01 %FW-6-302013: Built inbound UDP connection",
		fac: 20, sev: 6, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "%FW-6-302013"},
	{name: "year after time",
		raw: "<14>Nov 10 20:41:07 2026 edge-fw-01 app: x", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "explicit year in the past is kept",
		raw: "<14>2019 Mar  2 01:02:03 edge-fw-01 app: x", fac: 1, sev: 6,
		ts: utc(2019, 3, 2, 1, 2, 3, 0), host: "edge-fw-01", app: "app"},
	{name: "sequence number, host colon, unsynced star, ms",
		raw: "<189>123: edge-sw-01: *Nov 10 20:41:07.123: %SYS-5-CONFIG_I: Configured from console by vty0 (198.51.100.20)",
		fac: 23, sev: 5, ts: utc(2026, 11, 10, 20, 41, 7, 123e6), host: "edge-sw-01", app: "%SYS-5-CONFIG_I"},
	{name: "sequence number, UTC zone, colon",
		raw: "<187>45: *Nov 10 20:41:07.123 UTC: %LINK-3-UPDOWN: Interface port1, changed state to down",
		fac: 23, sev: 3, ts: utc(2026, 11, 10, 20, 41, 7, 123e6), app: "%LINK-3-UPDOWN"},
	{name: "synced dot marker and EST",
		raw: "<189>46: .Nov 10 15:41:07.123 EST: %LINK-5-CHANGED: Interface port1, changed state to up",
		fac: 23, sev: 5, ts: utc(2026, 11, 10, 20, 41, 7, 123e6), app: "%LINK-5-CHANGED"},
	{name: "ambiguous zone leaves timestamp zero",
		raw: "<189>Nov 10 14:41:07 CST: %LINK-5-CHANGED: x", fac: 23, sev: 5, app: "%LINK-5-CHANGED"},
	{name: "zone then host",
		raw: "<14>Nov 10 21:41:07 CET edge-fw-01 app: x", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "upper-case host that is not a zone",
		raw: "<14>Nov 10 20:41:07 CORE01 app: x", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "CORE01", app: "app"},
	{name: "separate numeric offset",
		raw: "<14>Nov 10 21:41:07 +0100 edge-fw-01 app: x", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "attached numeric offset",
		raw: "<14>Nov 10 15:41:07-05:00 edge-fw-01 app: x", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "%-tag with sub-facility",
		raw: "<187>Nov 10 20:41:07 edge-rtr-01 %ETH-PORT-3-LINKDOWN: port 7", fac: 23, sev: 3,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-rtr-01", app: "%ETH-PORT-3-LINKDOWN"},
	{name: "%-tag shape without colon",
		raw: "<187>Nov 10 20:41:07 edge-rtr-01 %ETH-3-LINKDOWN port 7", fac: 23, sev: 3,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-rtr-01", app: "%ETH-3-LINKDOWN"},
	{name: "percent word is not a tag",
		raw: "<14>Nov 10 20:41:07 edge-fw-01 %cpu 99", fac: 1, sev: 6,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01"},

	// ISO 8601 without an RFC 5424 header.
	{name: "ISO with offset and pid tag",
		raw: "<13>2026-11-10T21:41:07.123+01:00 edge-fw-01 app[9]: msg", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 123e6), host: "edge-fw-01", app: "app"},
	{name: "ISO space-separated without zone",
		raw: "<13>2026-11-10 20:41:07 edge-fw-01 app: msg", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "ISO then zone name",
		raw: "<13>2026-11-10 20:41:07 UTC edge-fw-01 app: msg", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "ISO without PRI",
		raw: "2026-11-10T20:41:07Z edge-fw-01 app: msg", fac: unk, sev: unk,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "ISO with hhmm offset",
		raw: "<13>2026-11-11T02:11:07+0530 edge-fw-01 app: msg", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},

	// Missing or broken PRI, BOMs and leading whitespace.
	{name: "no PRI 3164",
		raw: "Nov 10 20:41:07 edge-fw-01 app: msg", fac: unk, sev: unk,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "leading BOM",
		raw: "\xEF\xBB\xBF<13>Nov 10 20:41:07 edge-fw-01 app: msg", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "leading whitespace and line breaks",
		raw: " \t\r\n\x00<13>Nov 10 20:41:07 edge-fw-01 app: msg", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "space after PRI",
		raw: "<13> Nov 10 20:41:07 edge-fw-01 app: msg", fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "edge-fw-01", app: "app"},
	{name: "PRI zero",
		raw: "<0>Nov 10 20:41:07 h a: x", fac: 0, sev: 0, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "h", app: "a"},
	{name: "PRI 191",
		raw: "<191>Nov 10 20:41:07 h a: x", fac: 23, sev: 7, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "h", app: "a"},
	{name: "PRI leading zeros",
		raw: "<013>Nov 10 20:41:07 h a: x", fac: 1, sev: 5, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "h", app: "a"},
	{name: "PRI out of range is consumed",
		raw: "<192>Nov 10 20:41:07 h a: x", fac: unk, sev: unk, ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "h", app: "a"},
	{name: "PRI four digits is not a PRI", raw: "<1234>Nov 10 20:41:07 h a: x", fac: unk, sev: unk},
	{name: "empty PRI", raw: "<>Nov 10 20:41:07 h a: x", fac: unk, sev: unk},
	{name: "unterminated PRI", raw: "<13", fac: unk, sev: unk},
	{name: "PRI only", raw: "<13>", fac: 1, sev: 5},
	{name: "empty", raw: "", fac: unk, sev: unk},
	{name: "only BOM", raw: "\xEF\xBB\xBF", fac: unk, sev: unk},
	{name: "plain text", raw: "hello world", fac: unk, sev: unk},

	// No timestamp: only strong tags are believed.
	{name: "no timestamp, pid tag", raw: "<13>sshd[1]: hi", fac: 1, sev: 5, app: "sshd"},
	{name: "no timestamp, %-tag", raw: "<13>%FW-6-1: hi", fac: 1, sev: 5, app: "%FW-6-1"},
	{name: "no timestamp, host and pid tag", raw: "<13>edge-fw-01 app[123]: msg", fac: 1, sev: 5, host: "edge-fw-01", app: "app"},
	{name: "no timestamp, host colon and %-tag", raw: "<13>edge-fw-01: %SYS-5-RESTART: up", fac: 1, sev: 5, host: "edge-fw-01", app: "%SYS-5-RESTART"},
	{name: "no timestamp, prose with colon", raw: "<13>Disk full: /var", fac: 1, sev: 5},
	{name: "no timestamp, word colon", raw: "<13>Error: something failed", fac: 1, sev: 5},
	{name: "bad month", raw: "<13>Foo 10 20:41:07 edge-fw-01 app: x", fac: 1, sev: 5},
	{name: "numeric version-like start", raw: "<13>12 apples in a box", fac: 1, sev: 5},

	// Year inference around New Year and leap days.
	{name: "Dec message received on Jan 1",
		raw: "<13>Dec 31 23:59:58 h a: x", received: utc(2027, 1, 1, 0, 0, 5, 0),
		fac: 1, sev: 5, ts: utc(2026, 12, 31, 23, 59, 58, 0), host: "h", app: "a"},
	{name: "Jan message received on Dec 31",
		raw: "<13>Jan  1 00:00:03 h a: x", received: utc(2026, 12, 31, 23, 59, 59, 0),
		fac: 1, sev: 5, ts: utc(2027, 1, 1, 0, 0, 3, 0), host: "h", app: "a"},
	{name: "date weeks ahead is last year",
		raw: "<13>Dec 25 10:00:00 h a: x", fac: 1, sev: 5, ts: utc(2025, 12, 25, 10, 0, 0, 0), host: "h", app: "a"},
	{name: "date days ahead is this year",
		raw: "<13>Nov 12 10:00:00 h a: x", fac: 1, sev: 5, ts: utc(2026, 11, 12, 10, 0, 0, 0), host: "h", app: "a"},
	{name: "date months behind is this year",
		raw: "<13>Mar  3 10:00:00 h a: x", fac: 1, sev: 5, ts: utc(2026, 3, 3, 10, 0, 0, 0), host: "h", app: "a"},
	{name: "Feb 29 in a leap year",
		raw: "<13>Feb 29 12:00:00 h a: x", received: utc(2028, 3, 1, 0, 0, 0, 0),
		fac: 1, sev: 5, ts: utc(2028, 2, 29, 12, 0, 0, 0), host: "h", app: "a"},
	{name: "Feb 29 with no leap year near",
		raw: "<13>Feb 29 12:00:00 h a: x", received: utc(2027, 3, 1, 0, 0, 0, 0),
		fac: 1, sev: 5, host: "h", app: "a"},
	{name: "no received time leaves year-less timestamp zero",
		raw: "<13>Nov 10 20:41:07 h a: x", noRecv: true, fac: 1, sev: 5, host: "h", app: "a"},
	{name: "no received time keeps a full timestamp",
		raw: "<13>2026 Nov 10 20:41:07 h a: x", noRecv: true, fac: 1, sev: 5,
		ts: utc(2026, 11, 10, 20, 41, 7, 0), host: "h", app: "a"},
}

func TestParse(t *testing.T) {
	for _, tc := range parseCases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)
			orig := bytes.Clone(raw)
			m := model.Message{Received: tc.received}
			if m.Received.IsZero() && !tc.noRecv {
				m.Received = recv
			}
			Parse(raw, &m)
			if !bytes.Equal(raw, orig) {
				t.Fatalf("raw modified: %q", raw)
			}
			if m.Facility != tc.fac || m.Severity != tc.sev {
				t.Errorf("facility/severity = %d/%d, want %d/%d", m.Facility, m.Severity, tc.fac, tc.sev)
			}
			if !m.Timestamp.Equal(tc.ts) || m.Timestamp.IsZero() != tc.ts.IsZero() {
				t.Errorf("timestamp = %v, want %v", m.Timestamp, tc.ts)
			}
			if !m.Timestamp.IsZero() && m.Timestamp.Location() != time.UTC {
				t.Errorf("timestamp location = %v, want UTC", m.Timestamp.Location())
			}
			if m.Hostname != tc.host {
				t.Errorf("hostname = %q, want %q", m.Hostname, tc.host)
			}
			if m.App != tc.app {
				t.Errorf("app = %q, want %q", m.App, tc.app)
			}
		})
	}
}

// Parse must overwrite every parsed field, so a reused Message doesn't keep
// the previous message's metadata.
func TestParseResetsFields(t *testing.T) {
	m := model.Message{Received: recv}
	Parse([]byte("<86>Nov 10 20:41:07 edge-fw-01 sshd[1]: x"), &m)
	if m.Hostname == "" || m.App == "" || m.Timestamp.IsZero() {
		t.Fatalf("first parse incomplete: %+v", m)
	}
	Parse([]byte("garbage"), &m)
	if m.Facility != unk || m.Severity != unk || !m.Timestamp.IsZero() || m.Hostname != "" || m.App != "" {
		t.Fatalf("fields not reset: %+v", m)
	}
}

// Hostname and App must not alias raw: the listener reuses its read buffers.
func TestParseCopiesStrings(t *testing.T) {
	raw := []byte("<86>Nov 10 20:41:07 edge-fw-01 sshd[1]: x")
	m := model.Message{Received: recv}
	Parse(raw, &m)
	for i := range raw {
		raw[i] = 'X'
	}
	if m.Hostname != "edge-fw-01" || m.App != "sshd" {
		t.Fatalf("strings alias raw: host %q app %q", m.Hostname, m.App)
	}
}

func TestParseAllocs(t *testing.T) {
	for _, s := range benchMessages {
		raw := []byte(s.raw)
		m := model.Message{Received: recv}
		n := testing.AllocsPerRun(200, func() { Parse(raw, &m) })
		if n > 1 {
			t.Errorf("%s: %.0f allocs per Parse, want <= 1", s.name, n)
		}
	}
	m := model.Message{Received: recv}
	raw := []byte("<13>1 - - - - - -")
	if n := testing.AllocsPerRun(200, func() { Parse(raw, &m) }); n != 0 {
		t.Errorf("nil 5424: %.0f allocs, want 0", n)
	}
}
