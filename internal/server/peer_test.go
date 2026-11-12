package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/gmccollu/woodshed/internal/api"
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

func TestPeerWarning(t *testing.T) {
	dir := t.TempDir()
	r := start(t, writeConfig(t, dir, freePort(t), 0, nil))
	r.srv.be.peers.mu.Lock()
	r.srv.be.peers.at = r.srv.be.peers.at.AddDate(-1, 0, 0)
	r.srv.be.peers.find = func(context.Context, peer.Options) (peer.Info, bool) {
		return peer.Info{Kind: peer.KindService, PID: 4210, UIURL: "http://localhost:8514/"}, true
	}
	r.srv.be.peers.mu.Unlock()
	want := "The Woodshed service is installed and running (PID 4210, UI http://localhost:8514/). This copy will compete with it for ports; use the service, or pick other ports."
	if got := r.srv.PeerWarning(context.Background()); got != want {
		t.Errorf("%q", got)
	}
}
