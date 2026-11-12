package search

import (
	"strings"

	"github.com/gmccollu/woodshed/internal/model"
	"github.com/gmccollu/woodshed/internal/syslog"
)

// fieldMatcher matches the program/tag and hostname that syslog.Parse reads
// from a message's header (model.Message.App and Hostname). It holds no state,
// so the runs of one request can share it; each run brings its own Message.
type fieldMatcher struct {
	apps  []appPattern
	hosts []string // lower-cased
}

// appPattern is one app value: lower-cased, without a leading '%', and with a
// trailing '*' turned into prefix.
type appPattern struct {
	s      string
	prefix bool
}

// newFieldMatcher returns nil when apps and hosts are both empty.
func newFieldMatcher(apps, hosts []string) *fieldMatcher {
	var fm fieldMatcher
	for _, a := range apps {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		p := appPattern{}
		if strings.HasSuffix(a, "*") {
			p.prefix = true
			a = strings.TrimRight(a, "*")
		}
		p.s = strings.ToLower(trimPct(a))
		fm.apps = append(fm.apps, p)
	}
	for _, h := range hosts {
		if h = strings.TrimSpace(h); h != "" {
			fm.hosts = append(fm.hosts, strings.ToLower(h))
		}
	}
	if len(fm.apps) == 0 && len(fm.hosts) == 0 {
		return nil
	}
	return &fm
}

// trimPct drops one leading '%': syslog.Parse keeps it in App for a
// "%FAC-N-MNEMONIC" tag, and people type the tag either way.
func trimPct(s string) string { return strings.TrimPrefix(s, "%") }

// match parses raw into msg (the caller's scratch Message) and reports whether
// its app and host pass.
func (fm *fieldMatcher) match(raw []byte, msg *model.Message) bool {
	syslog.Parse(raw, msg)
	if len(fm.hosts) > 0 {
		ok := false
		for _, h := range fm.hosts {
			if strings.EqualFold(msg.Hostname, h) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(fm.apps) > 0 {
		app := trimPct(msg.App)
		if app == "" {
			return false
		}
		for _, p := range fm.apps {
			if p.prefix {
				if len(app) >= len(p.s) && strings.EqualFold(app[:len(p.s)], p.s) {
					return true
				}
			} else if strings.EqualFold(app, p.s) {
				return true
			}
		}
		return false
	}
	return true
}

// Fields returns the program/tag and hostname of a raw message, as the app and
// host filters see them (App keeps a "%" tag's '%').
func Fields(raw []byte) (app, host string) {
	var m model.Message
	syslog.Parse(raw, &m)
	return m.App, m.Hostname
}
