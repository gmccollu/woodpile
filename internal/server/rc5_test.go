package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gmccollu/woodshed/internal/api"
)

func TestUDPBufferBanner(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		goos               string
		granted, requested int
		want               bool
	}{
		{"linux", 4 * mib, 32 * mib, true},
		{"linux", 208 << 10, 32 * mib, true},
		{"linux", 8 * mib, 32 * mib, false},  // big enough
		{"linux", 4 * mib, 4 * mib, false},   // what was asked for
		{"linux", 2 * mib, 1 * mib, false},   // more than asked
		{"linux", 0, 32 * mib, false},        // UDP not bound
		{"darwin", 4 * mib, 32 * mib, false}, // Linux only
		{"windows", 4 * mib, 32 * mib, false},
	} {
		b := udpBufferBanner(tc.goos, tc.granted, tc.requested)
		if (b != nil) != tc.want {
			t.Errorf("%+v: %+v", tc, b)
		}
	}
	b := udpBufferBanner("linux", 4*mib, 32*mib)
	if b.Kind != "udp_buffer_small" || b.Level != api.LevelWarn || b.Action != "" {
		t.Errorf("%+v", b)
	}
	for _, s := range []string{"4 MiB", "32 MiB", "dropped before Woodshed sees them", "net.core.rmem_max on the host", "sysctl"} {
		if !strings.Contains(b.Text, s) {
			t.Errorf("text lacks %q: %s", s, b.Text)
		}
	}
}

// TestRunStateListenersAndBanners: the run-state file carries the listeners and
// banners as /api/status has them, and follows a change within a tick.
func TestRunStateListenersAndBanners(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	hold, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	r := start(t, writeConfig(t, dir, port, 0, nil))
	dataDir := filepath.Join(dir, "data")
	var rs RunState
	waitFor(t, "the run-state file to show the TCP listener down", func() bool {
		rs, err = ReadState(dataDir)
		return err == nil && len(rs.Listeners) == 2 && !rs.Listeners[1].OK
	})
	if rs.Listeners[0].Proto != "udp" || !rs.Listeners[0].OK || rs.Listeners[1].Proto != "tcp" || rs.Listeners[1].Error == "" ||
		rs.Listeners[1].Port != port {
		t.Errorf("listeners %+v", rs.Listeners)
	}
	if rs.UDPRcvbufRequestedBytes == 0 || rs.UDPRcvbufBytes == 0 {
		t.Errorf("rcvbuf %d of %d", rs.UDPRcvbufBytes, rs.UDPRcvbufRequestedBytes)
	}
	if !slices.ContainsFunc(rs.Banners, func(b api.Banner) bool { return b.Kind == "listener_bind" && b.Level == api.LevelError }) {
		t.Errorf("banners %+v", rs.Banners)
	}
	// The same as /api/status.
	var st api.Status
	r.do(t, "GET", "/api/status", nil, &st)
	if !jsonEqual(t, st.Listeners, rs.Listeners) || !jsonEqual(t, st.Banners, rs.Banners) {
		t.Errorf("status %+v %+v\nstate %+v %+v", st.Listeners, st.Banners, rs.Listeners, rs.Banners)
	}
	// The documented keys.
	raw, _ := os.ReadFile(filepath.Join(dataDir, StateFileName))
	var m map[string]any
	json.Unmarshal(raw, &m)
	for _, k := range []string{"pid", "version", "started_at", "config", "data_dir", "ui", "listeners", "udp_rcvbuf_bytes",
		"udp_rcvbuf_requested_bytes", "banners"} {
		if _, ok := m[k]; !ok {
			t.Errorf("no %q in %s", k, raw)
		}
	}

	hold.Close()
	if code := r.do(t, "POST", "/api/listeners/retry", nil, nil); code != 204 {
		t.Fatalf("retry %d", code)
	}
	waitFor(t, "the run-state file to show both listeners up", func() bool {
		rs, err = ReadState(dataDir)
		return err == nil && len(rs.Listeners) == 2 && rs.Listeners[1].OK &&
			!slices.ContainsFunc(rs.Banners, func(b api.Banner) bool { return b.Kind == "listener_bind" })
	})
	if rs.Banners == nil {
		t.Error("banners null, want []")
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// TestRunStateMatchesDoc: docs/api.md's run-state example has RunState's keys.
func TestRunStateMatchesDoc(t *testing.T) {
	raw, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")
	i := strings.Index(doc, "## Run-state file")
	b := strings.Index(doc[i:], "```json\n") + i + len("```json\n")
	e := strings.Index(doc[b:], "\n```") + b
	var want map[string]any
	if err := json.Unmarshal([]byte(doc[b:e]), &want); err != nil {
		t.Fatal(err)
	}
	full := RunState{UI: RunStateUI{Error: "x", CertFingerprint: "x"}, Listeners: []api.Listener{{}}, Banners: []api.Banner{{}}}
	out, _ := json.Marshal(full)
	var got map[string]any
	json.Unmarshal(out, &got)
	keysOf := func(m map[string]any) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		return ks
	}
	if !slices.Equal(keysOf(got), keysOf(want)) {
		t.Errorf("keys %v, doc %v", keysOf(got), keysOf(want))
	}
	wantUI := want["ui"].(map[string]any)
	wantUI["error"] = "" // omitted when empty in the example
	if !slices.Equal(keysOf(got["ui"].(map[string]any)), keysOf(wantUI)) {
		t.Errorf("ui keys %v, doc %v", keysOf(got["ui"].(map[string]any)), keysOf(wantUI))
	}
}
