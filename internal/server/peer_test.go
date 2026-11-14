package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/peer"
)

func TestAnnotateHolders(t *testing.T) {
	svc := peer.Info{Kind: peer.KindService, PID: 4210, UIURL: "http://localhost:8514/"}
	ls := []api.Listener{
		{Proto: "udp", Port: 514, Error: "bind: address already in use"},                                // holder invisible
		{Proto: "tcp", Port: 514, Error: "bind: address already in use", Holder: "woodshed (pid 4210)"}, // visible, the service
		{Proto: "tcp", Port: 601, Error: "bind: address already in use", Holder: "syslogd (pid 312)"},   // someone else
		{Proto: "udp", Port: 5514, Error: "bind: permission denied"},                                    // not a clash
		{Proto: "tcp", Port: 6514, OK: true},
	}
	annotateHolders(ls, svc, true)
	for i, want := range []string{"woodshed_service", "woodshed_service", "", "", ""} {
		if ls[i].HolderKind != want {
			t.Errorf("listener %d: kind %q, want %q", i, ls[i].HolderKind, want)
		}
	}
	if ls[0].HolderPID != 4210 || ls[0].HolderUIURL != "http://localhost:8514/" {
		t.Errorf("%+v", ls[0])
	}
	b, _ := json.Marshal(ls[0])
	if !strings.Contains(string(b), `"holder_kind":"woodshed_service","holder_pid":4210,"holder_ui_url":"http://localhost:8514/"`) {
		t.Errorf("JSON %s", b)
	}
	if b, _ := json.Marshal(ls[2]); strings.Contains(string(b), "holder_kind") {
		t.Errorf("empty kind not omitted: %s", b)
	}
	// No peer: nothing changes.
	ls2 := []api.Listener{{Proto: "udp", Port: 514, Error: "bind: address already in use"}}
	annotateHolders(ls2, peer.Info{}, false)
	if ls2[0].HolderKind != "" {
		t.Error("annotated without a peer")
	}
}

func TestPeerBanners(t *testing.T) {
	held := []api.Listener{{Proto: "udp", Port: 514, HolderKind: "woodshed_service"}}
	svc := peer.Info{Kind: peer.KindService, PID: 4210} // its state file isn't readable (another user)
	bs := peerBanners(held, svc, true, 8514, true)
	if len(bs) != 2 || bs[0].Kind != "listener_bind" || !strings.Contains(bs[0].Text, "The Woodshed service (PID 4210)") ||
		bs[1].Kind != "ui_port_busy" || !strings.Contains(bs[1].Text, "can't open its UI on port 8514") {
		t.Fatalf("%+v", bs)
	}
	// The service's UI is up elsewhere: no ui_port_busy.
	up := peer.Info{Kind: peer.KindService, PID: 4210, UIURL: "http://localhost:9514/", UIKnown: true, UIListening: true}
	if bs := peerBanners(nil, up, true, 8514, true); len(bs) != 0 {
		t.Errorf("%+v", bs)
	}
	// Its UI is down on our port: ui_port_busy, for a copy too.
	cp := peer.Info{Kind: peer.KindCopy, PID: 77, UIURL: "http://localhost:8514/", UIKnown: true}
	if bs := peerBanners(nil, cp, true, 8514, true); len(bs) != 1 || !strings.Contains(bs[0].Text, "Another copy of Woodshed (PID 77)") {
		t.Errorf("%+v", bs)
	}
	// Our UI isn't up, or no peer: nothing.
	if bs := peerBanners(nil, svc, true, 8514, false); len(bs) != 0 {
		t.Errorf("%+v", bs)
	}
	if bs := peerBanners(held, peer.Info{}, false, 8514, true); len(bs) != 0 {
		t.Errorf("%+v", bs)
	}
}

// TestStatusNamesTheServiceAsHolder: /api/status names the Woodshed service
// as the holder of a port it can't bind. The "service" here is this test
// process (it holds the port), so whether the OS shows the holder (same PID)
// or not (an invisible holder), the listener is annotated.
func TestStatusNamesTheServiceAsHolder(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	hold, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	r := start(t, writeConfig(t, dir, port, 0, nil))
	me := os.Getpid()
	r.srv.be.peers.mu.Lock()
	r.srv.be.peers.at = r.srv.be.peers.at.AddDate(-1, 0, 0) // expire the cached "none"
	r.srv.be.peers.find = func(context.Context, peer.Options) (peer.Info, bool) {
		return peer.Info{Kind: peer.KindService, PID: me, UIURL: "http://localhost:8514/"}, true
	}
	r.srv.be.peers.mu.Unlock()
	var st map[string]any
	r.do(t, "GET", "/api/status", nil, &st)
	l := st["listeners"].([]any)[0].(map[string]any)
	if l["ok"] != false || l["holder_kind"] != "woodshed_service" || l["holder_pid"] != float64(me) || l["holder_ui_url"] != "http://localhost:8514/" {
		t.Fatalf("listener %v", l)
	}
	found := false
	for _, b := range st["banners"].([]any) {
		if strings.Contains(b.(map[string]any)["text"].(string), fmt.Sprintf("The Woodshed service (PID %d)", me)) {
			found = true
		}
	}
	if !found {
		t.Errorf("banners %v", st["banners"])
	}
}

// setPeer makes the server see svc as the installed service, with read as the
// way to read its config.
func setPeer(r *running, svc peer.Info, read func(string) (config.Config, error)) {
	r.srv.be.peers.mu.Lock()
	defer r.srv.be.peers.mu.Unlock()
	r.srv.be.peers.at = r.srv.be.peers.at.AddDate(-1, 0, 0)
	r.srv.be.peers.find = func(context.Context, peer.Options) (peer.Info, bool) { return svc, true }
	r.srv.be.peers.readConfig = read
}

func unreadable(string) (config.Config, error) { return config.Config{}, fs.ErrPermission }

func TestPeerWarning(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	r := start(t, writeConfig(t, dir, port, 0, nil))
	svc := peer.Info{Kind: peer.KindService, PID: 4210, UIURL: "http://localhost:8514/"}

	// The reported case: a copy on other ports (UDP/TCP 15514, UI 18514 there),
	// the service's config not readable (it's the service's): its default ports
	// don't overlap, so no warning.
	setPeer(r, svc, unreadable)
	if got := r.srv.PeerWarning(context.Background()); got != "" {
		t.Errorf("other ports, config unreadable: %q", got)
	}

	// The service's config is readable and uses this copy's syslog port.
	var gotPath string
	setPeer(r, peer.Info{Kind: svc.Kind, PID: svc.PID, UIURL: svc.UIURL, Config: "/etc/woodshed/svc.toml"}, func(path string) (config.Config, error) {
		gotPath = path
		c := config.Default()
		c.Listeners.UDP.Port, c.Listeners.TCP.Port = port, port
		return c, nil
	})
	want := fmt.Sprintf("The Woodshed service is installed and running (PID 4210, UI http://localhost:8514/). "+
		"This copy will compete with it for UDP %d and TCP %d; use the service, or pick other ports.", port, port)
	if got := r.srv.PeerWarning(context.Background()); got != want {
		t.Errorf("readable config:\n got %q\nwant %q", got, want)
	}
	if gotPath != "/etc/woodshed/svc.toml" {
		t.Errorf("read %q, want the config its run-state file names", gotPath)
	}

	// Its run-state file's listeners win over its config: only TCP clashes.
	setPeer(r, peer.Info{Kind: svc.Kind, PID: svc.PID, Listeners: []peer.Listener{{Proto: "udp", Port: 514}, {Proto: "tcp", Port: port}}}, unreadable)
	want = fmt.Sprintf("The Woodshed service is installed and running (PID 4210). This copy will compete with it for TCP %d; "+
		"use the service, or pick other ports.", port)
	if got := r.srv.PeerWarning(context.Background()); got != want {
		t.Errorf("state listeners:\n got %q\nwant %q", got, want)
	}

	// Readable and on other ports: no warning.
	setPeer(r, svc, func(string) (config.Config, error) { return config.Default(), nil })
	if got := r.srv.PeerWarning(context.Background()); got != "" {
		t.Errorf("readable, other ports: %q", got)
	}
}

func TestServicePorts(t *testing.T) {
	missing := func(string) (config.Config, error) { return config.Config{}, fs.ErrNotExist }
	// Nothing readable: the defaults, not known.
	ps, known := servicePorts(peer.Info{Kind: peer.KindService}, missing)
	if ps != (portSet{514, 514, 8514}) || known {
		t.Errorf("defaults: %+v %v", ps, known)
	}
	// Its config (UDP off), with the UI address from its run-state file.
	ps, known = servicePorts(peer.Info{Kind: peer.KindService, UIURL: "https://host.example:9443/"}, func(string) (config.Config, error) {
		c := config.Default()
		c.Listeners.UDP.Enabled = false
		c.Listeners.TCP.Port = 6514
		c.UI.Port = 9000
		return c, nil
	})
	if ps != (portSet{0, 6514, 9443}) || !known {
		t.Errorf("config: %+v %v", ps, known)
	}
	// The run-state file's listeners alone are enough to know.
	ps, known = servicePorts(peer.Info{Kind: peer.KindService, Listeners: []peer.Listener{{Proto: "tcp", Port: 1514}}}, missing)
	if ps != (portSet{0, 1514, 8514}) || !known {
		t.Errorf("state: %+v %v", ps, known)
	}
}

func TestPeerWarningText(t *testing.T) {
	svc := peer.Info{Kind: peer.KindService, PID: 4210}
	mine := portSet{514, 514, 8515} // the UI fell back to the next port
	theirs := portSet{514, 514, 8514}
	want := "The Woodshed service is installed and running (PID 4210). This copy may compete with it for UDP 514 and " +
		"TCP 514 (the service's default ports; its config isn't readable here); use the service, or pick other ports."
	if got := peerWarningText(svc, mine.overlap(theirs), false); got != want {
		t.Errorf("unknown:\n got %q\nwant %q", got, want)
	}
	if got := peerWarningText(svc, (portSet{15514, 15514, 18514}).overlap(theirs), false); got != "" {
		t.Errorf("no overlap: %q", got)
	}
	if got := (portSet{514, 0, 8514}).overlap(portSet{514, 0, 8514}); strings.Join(got, ",") != "UDP 514,UI 8514" {
		t.Errorf("overlap %q (a port that's off never overlaps)", got)
	}
	if got := joinAnd([]string{"UDP 514", "TCP 514", "UI 8514"}); got != "UDP 514, TCP 514 and UI 8514" {
		t.Errorf("joinAnd %q", got)
	}
}
