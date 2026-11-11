// Package model holds the types shared between Woodpile's packages.
package model

import (
	"net/netip"
	"time"
)

// Unknown marks a Facility or Severity that could not be parsed (no valid PRI).
const Unknown = -1

// Message is one received syslog message. Raw is always the bytes exactly as received
// (after framing, before any parsing); everything else is best-effort metadata.
type Message struct {
	Received  time.Time  // when Woodpile received it, UTC
	Sender    netip.Addr // source IP of the packet or connection
	Raw       []byte     // exact bytes as received (no trailing newline)
	Truncated bool       // Raw was cut at the max message size

	// Parsed metadata (zero values when absent).
	Facility  int       // 0-23, or Unknown if no PRI
	Severity  int       // 0-7, or Unknown if no PRI
	Timestamp time.Time // the device's own timestamp as a UTC instant, if parseable. Zone-less device times are
	// read as UTC wall-clock time (the real zone can't be known), so the UI labels this "device time".
	Hostname string
	App      string
}

// DeviceID returns the directory-safe device ID for a sender: the IP as text,
// with ':' replaced by '_' so IPv6 addresses are valid Windows file names.
func DeviceID(a netip.Addr) string {
	s := a.Unmap().String()
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == '%' {
			out[i] = '_'
		} else {
			out[i] = s[i]
		}
	}
	return string(out)
}
