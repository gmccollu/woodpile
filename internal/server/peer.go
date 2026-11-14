package server

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/peer"
)

// peerCacheFor is how long a peer lookup (a launchctl/systemctl/SCM query) is
// reused: /api/status is polled every few seconds.
const peerCacheFor = 10 * time.Second

// peerCache remembers the last peer lookup.
type peerCache struct {
	mu   sync.Mutex
	at   time.Time
	info peer.Info
	ok   bool
	find func(context.Context, peer.Options) (peer.Info, bool) // peer.Find; tests replace it
	// readConfig reads the service's config without creating it (config.Read;
	// tests replace it).
	readConfig func(path string) (config.Config, error)
}

// peer returns another running Woodshed (the installed service, or a copy
// whose run-state file in the default data folder is readable), cached.
func (b *backend) peer(ctx context.Context) (peer.Info, bool) {
	c := &b.peers
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < peerCacheFor {
		return c.info, c.ok
	}
	find := c.find
	if find == nil {
		find = peer.Find
	}
	var files []string
	if def := config.DefaultDataDir(); !samePath(filepath.Clean(def), filepath.Clean(b.config().Storage.DataDir)) {
		files = append(files, filepath.Join(def, StateFileName))
	}
	c.info, c.ok = find(ctx, peer.Options{StateFiles: files, SelfPID: os.Getpid()})
	c.at = time.Now()
	return c.info, c.ok
}

var holderPIDRE = regexp.MustCompile(`\(pid (\d+)\)`)

// annotateHolders names another Woodshed as the holder of listeners that
// couldn't bind: when the OS names its PID, or when the holder can't be seen
// at all (another user's socket) and a Woodshed is running.
func annotateHolders(ls []api.Listener, p peer.Info, ok bool) {
	if !ok {
		return
	}
	for i := range ls {
		l := &ls[i]
		if l.OK || !isAddrInUse(l.Error) {
			continue
		}
		pid := 0
		if m := holderPIDRE.FindStringSubmatch(l.Holder); m != nil {
			pid, _ = strconv.Atoi(m[1])
		}
		if (pid != 0 && pid == p.PID) || (l.Holder == "" && pid == 0) {
			l.HolderKind, l.HolderPID, l.HolderUIURL = string(p.Kind), p.PID, p.UIURL
		}
	}
}

// peerName is how banners name a peer.
func peerName(p peer.Info) string {
	name := "Another copy of Woodshed"
	if p.Kind == peer.KindService {
		name = "The Woodshed service"
	}
	if p.PID != 0 {
		name += fmt.Sprintf(" (PID %d)", p.PID)
	}
	return name
}

// peerBanners are the banners about another Woodshed: listeners it holds, and
// its UI shut out by this copy's UI port.
func peerBanners(ls []api.Listener, p peer.Info, ok bool, uiPort int, uiListening bool) []api.Banner {
	if !ok {
		return nil
	}
	var out []api.Banner
	held := 0
	for _, l := range ls {
		if l.HolderKind != "" {
			held++
		}
	}
	if held > 0 {
		text := peerName(p) + " is probably using this copy's syslog ports. Use it instead"
		if p.UIURL != "" {
			text += " (" + p.UIURL + ")"
		}
		text += ", or pick other ports in Settings and press Retry."
		out = append(out, api.Banner{Kind: "listener_bind", Level: api.LevelError, Text: text, Action: "retry_listeners"})
	}
	peerPort := api.DefaultPort
	if u, err := url.Parse(p.UIURL); err == nil && u.Port() != "" {
		peerPort, _ = strconv.Atoi(u.Port())
	}
	if uiListening && uiPort == peerPort && (!p.UIKnown || !p.UIListening) {
		out = append(out, api.Banner{Kind: "ui_port_busy", Level: api.LevelWarn, Text: fmt.Sprintf(
			"%s is running but can't open its UI on port %d, because this copy is using that port. "+
				"Stop this copy to use it, or move this copy's UI to another port in Settings.", peerName(p), uiPort)})
	}
	return out
}

// PeerWarning is the startup warning for `woodshed run` and portable mode when
// the installed service is running and this copy's ports overlap with its ports
// ("" otherwise). It's logged too. The service itself never warns about itself.
//
// The service's ports come from its run-state file and its config, when this
// user can read them (often not: they're the service's); otherwise they're the
// defaults, and the warning says "may".
func (s *Server) PeerWarning(ctx context.Context) string {
	p, ok := s.be.peer(ctx)
	if !ok || p.Kind != peer.KindService {
		return ""
	}
	read := s.be.peers.readConfig
	if read == nil {
		read = config.Read
	}
	theirs, known := servicePorts(p, read)
	mine := s.ownPorts()
	msg := peerWarningText(p, mine.overlap(theirs), known)
	if msg == "" {
		which := "its ports"
		if !known {
			which = "its default ports (its config isn't readable)"
		}
		s.log.Info("the Woodshed service is running; this copy's ports differ from "+which,
			"this_copy", mine.String(), "service", theirs.String())
		return ""
	}
	s.log.Warn(msg)
	return msg
}

// portSet is the syslog and UI ports a Woodshed uses (0: off, or a port the OS
// picks).
type portSet struct{ UDP, TCP, UI int }

// overlap lists the ports in both p and q ("UDP 514").
func (p portSet) overlap(q portSet) []string {
	var out []string
	for _, x := range []struct {
		name string
		a, b int
	}{{"UDP", p.UDP, q.UDP}, {"TCP", p.TCP, q.TCP}, {"UI", p.UI, q.UI}} {
		if x.a != 0 && x.a == x.b {
			out = append(out, fmt.Sprintf("%s %d", x.name, x.a))
		}
	}
	return out
}

func (p portSet) String() string {
	return fmt.Sprintf("UDP %d, TCP %d, UI %d", p.UDP, p.TCP, p.UI)
}

func syslogPorts(l config.Listeners) (udp, tcp int) {
	if l.UDP.Enabled {
		udp = l.UDP.Port
	}
	if l.TCP.Enabled {
		tcp = l.TCP.Port
	}
	return udp, tcp
}

// ownPorts is this copy's ports: the configured syslog ports, and the UI's
// bound port once it listens (portable mode may have moved it), else the
// configured one.
func (s *Server) ownPorts() portSet {
	cfg := s.be.config()
	var p portSet
	p.UDP, p.TCP = syslogPorts(cfg.Listeners)
	p.UI = cfg.UI.Port
	if st := s.api.State(); st.Listening {
		if _, port, err := net.SplitHostPort(st.Addr); err == nil {
			if n, err := strconv.Atoi(port); err == nil {
				p.UI = n
			}
		}
	}
	return p
}

// servicePorts returns the service peer p's ports, and whether they're known
// (from its run-state file or its config) rather than its defaults. Its config
// is the one its run-state file names, else the default path; read must not
// create it.
func servicePorts(p peer.Info, read func(string) (config.Config, error)) (portSet, bool) {
	def := config.Default()
	var ps portSet
	ps.UDP, ps.TCP = syslogPorts(def.Listeners)
	ps.UI = def.UI.Port
	known := false
	path := p.Config
	if path == "" {
		path = config.DefaultConfigPath()
	}
	if c, err := read(path); err == nil {
		ps.UDP, ps.TCP = syslogPorts(c.Listeners)
		ps.UI = c.UI.Port
		known = true
	}
	if len(p.Listeners) > 0 { // what it's actually running with
		ps.UDP, ps.TCP = 0, 0
		for _, l := range p.Listeners {
			switch l.Proto {
			case "udp":
				ps.UDP = l.Port
			case "tcp":
				ps.TCP = l.Port
			}
		}
		known = true
	}
	if u, err := url.Parse(p.UIURL); err == nil && u.Port() != "" {
		if n, err := strconv.Atoi(u.Port()); err == nil {
			ps.UI = n
		}
	}
	return ps, known
}

// peerWarningText is PeerWarning's text for the ports both use ("" if none):
// "will compete" when the service's ports are known, "may" when they're its
// defaults.
func peerWarningText(p peer.Info, overlap []string, known bool) string {
	if len(overlap) == 0 {
		return ""
	}
	msg := "The Woodshed service is installed and running"
	switch {
	case p.PID != 0 && p.UIURL != "":
		msg += fmt.Sprintf(" (PID %d, UI %s)", p.PID, p.UIURL)
	case p.PID != 0:
		msg += fmt.Sprintf(" (PID %d)", p.PID)
	}
	ports := joinAnd(overlap)
	if known {
		msg += ". This copy will compete with it for " + ports
	} else {
		msg += ". This copy may compete with it for " + ports + " (the service's default ports; its config isn't readable here)"
	}
	return msg + "; use the service, or pick other ports."
}

// joinAnd joins "a", "b", "c" as "a, b and c".
func joinAnd(s []string) string {
	if len(s) <= 1 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
