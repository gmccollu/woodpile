package server

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
// another Woodshed is running ("" if none). It's logged too. The service itself
// never warns about itself.
func (s *Server) PeerWarning(ctx context.Context) string {
	p, ok := s.be.peer(ctx)
	if !ok || p.Kind != peer.KindService {
		return ""
	}
	msg := "The Woodshed service is installed and running"
	switch {
	case p.PID != 0 && p.UIURL != "":
		msg += fmt.Sprintf(" (PID %d, UI %s)", p.PID, p.UIURL)
	case p.PID != 0:
		msg += fmt.Sprintf(" (PID %d)", p.PID)
	}
	msg += ". This copy will compete with it for ports; use the service, or pick other ports."
	s.log.Warn(msg)
	return msg
}
