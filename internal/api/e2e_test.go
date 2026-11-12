package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// TestE2EBuiltUIUnderCSP serves the real embedded UI (build it first: `cd web &&
// npm run build`) with a fake backend, loads it in a headless Chromium-based browser, and checks
// that the dashboard renders and the console shows no CSP violation. It writes
// a screenshot and the DOM to $WOODSHED_E2E_OUT (default <repo>/tmp/api-e2e).
//
// Opt-in: WOODSHED_E2E=1 WOODSHED_BROWSER=/path/to/chromium go test -run E2E ./internal/api/
func TestE2EBuiltUIUnderCSP(t *testing.T) {
	if os.Getenv("WOODSHED_E2E") == "" {
		t.Skip("set WOODSHED_E2E=1 to run the browser check")
	}
	chrome := os.Getenv("WOODSHED_BROWSER")
	if chrome == "" {
		t.Skip("set WOODSHED_BROWSER to a Chromium-based browser binary")
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skipf("no browser at %s", chrome)
	}
	out := os.Getenv("WOODSHED_E2E_OUT")
	if out == "" {
		out, _ = filepath.Abs("../../tmp/api-e2e")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	b := e2eBackend()
	srv := startServer(t, b, UIConfig{}, func(o *Options) { o.Static = nil }) // nil = web.Dist
	if !srv.static.built {
		t.Fatal("web/dist has no index.html: run `npm run build` in web/ first")
	}
	base := fmt.Sprintf("http://localhost:%d/", srv.boundPort())
	if hold, _ := time.ParseDuration(os.Getenv("WOODSHED_E2E_HOLD")); hold > 0 {
		// Keep serving for manual checks (curl, a real browser) after the run.
		defer func() {
			t.Logf("holding %s at %s", hold, base)
			time.Sleep(hold)
		}()
		fmt.Fprintf(os.Stderr, "E2E_URL=%s\n", base)
	}

	profile := t.TempDir()
	run := func(args ...string) (string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		all := append([]string{
			"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
			"--disable-extensions", "--disable-background-networking", "--disable-component-update",
			"--disable-sync", "--metrics-recording-only", "--user-data-dir=" + profile,
			"--enable-logging=stderr", "--v=0", "--window-size=1440,1000",
			"--virtual-time-budget=3000",
		}, args...)
		cmd := exec.CommandContext(ctx, chrome, all...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Logf("chrome: %v\n%s", err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	page := base + "#/dashboard"
	dom, log1 := run("--dump-dom", page)
	_ = os.WriteFile(filepath.Join(out, "dashboard.dom.html"), []byte(dom), 0o644)
	shot := filepath.Join(out, "dashboard.png")
	_, log2 := run("--screenshot="+shot, page)
	_ = os.WriteFile(filepath.Join(out, "chrome.log"), []byte(log1+"\n----\n"+log2), 0o644)

	for _, want := range []string{"edge-fw-01", "core-sw-01"} {
		if !strings.Contains(dom, want) {
			t.Errorf("dashboard DOM lacks %q (see %s)", want, filepath.Join(out, "dashboard.dom.html"))
		}
	}
	for _, logs := range []string{log1, log2} {
		for _, line := range strings.Split(logs, "\n") {
			l := strings.ToLower(line)
			if strings.Contains(l, "content security policy") || strings.Contains(l, "refused to") {
				t.Errorf("CSP violation: %s", line)
			}
		}
	}
	// Canary: the same scan must catch a violation, or a clean log proves nothing.
	canary := startServer(t, nil, UIConfig{}, func(o *Options) {
		o.Static = fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><html><body><p style="color:red">x</p><script>document.title="ran"</script></body></html>`)}}
	})
	cdom, clog := run("--dump-dom", fmt.Sprintf("http://localhost:%d/", canary.boundPort()))
	if !strings.Contains(strings.ToLower(clog), "content security policy") {
		t.Errorf("canary: the browser log shows no CSP violation for an inline script; the scan above can't be trusted")
	}
	if strings.Contains(cdom, "<title>ran</title>") {
		t.Error("canary: an inline script ran under the CSP")
	}
	if fi, err := os.Stat(shot); err != nil || fi.Size() == 0 {
		t.Errorf("no screenshot: %v", err)
	}
	t.Logf("screenshot: %s", shot)
}

// e2eBackend is a fake backend with dashboard-like data and a live tail that
// keeps sending messages.
func e2eBackend() *fakeBackend {
	b := newFakeBackend()
	now := time.Now().UTC()
	first := now.Add(-2 * time.Hour)
	b.status.FirstMessageAt = &first
	b.status.StartedAt = now.Add(-3 * time.Hour)
	b.status.Banners = nil
	b.status.Listeners[1] = Listener{Proto: "tcp", Addr: "0.0.0.0", Port: 514, OK: true}
	b.stats.Sparkline = nil
	for i := range 60 {
		b.stats.Sparkline = append(b.stats.Sparkline, 1100+150*math.Sin(float64(i)/6))
	}
	b.stats.Devices = DeviceCounts{Total: 6, Online: 4, Quiet: 1, Offline: 1}
	b.devices = map[string]Device{}
	b.order = nil
	for i, d := range []Device{
		{ID: "10.1.0.1", Name: "edge-fw-01", Group: "HQ", Status: "online", MsgsPerMin: 36720},
		{ID: "10.1.0.2", Name: "core-sw-01", Group: "HQ", Status: "online", MsgsPerMin: 4210},
		{ID: "10.1.0.3", Name: "core-sw-02", Group: "HQ", Status: "online", MsgsPerMin: 3980},
		{ID: "10.2.0.1", Name: "branch-rtr-01", Group: "Branch", Status: "online", MsgsPerMin: 820},
		{ID: "10.2.0.9", Name: "branch-ap-07", Group: "Branch", Status: "quiet", MsgsPerMin: 0},
		{ID: "192.0.2.44", Name: "", Group: "", Status: "offline", MsgsPerMin: 0},
	} {
		d.IP = d.ID
		d.FirstSeen = now.Add(-72 * time.Hour)
		switch d.Status {
		case "online":
			d.LastHeard = now.Add(-time.Duration(i) * time.Second)
		case "quiet":
			d.LastHeard = now.Add(-20 * time.Minute)
		default:
			d.LastHeard = now.Add(-5 * time.Hour)
		}
		d.TotalMsgs = d.MsgsPerMin * 600
		d.BytesOnDisk = d.TotalMsgs * 180
		b.devices[d.ID] = d
		b.order = append(b.order, d.ID)
	}
	b.live = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		rc := http.NewResponseController(w)
		msgs := []struct {
			dev string
			sev int
			raw string
		}{
			{"10.1.0.1", 6, "<134>Nov 10 20:41:07 edge-fw-01 conn opened tcp 10.1.4.20:51544 -> 203.0.113.9:443"},
			{"10.1.0.2", 4, "<188>Nov 10 20:41:07 core-sw-01 %LINK-4-FLAP: interface Gi1/0/24 flapping"},
			{"10.1.0.1", 3, "<131>Nov 10 20:41:08 edge-fw-01 vpn: tunnel 7 down (peer timeout)"},
			{"10.2.0.1", 6, "<134>Nov 10 20:41:08 branch-rtr-01 dhcp lease 10.2.0.77"},
			{"10.1.0.3", 7, "<135>Nov 10 20:41:09 core-sw-02 debug: stp topology change"},
		}
		send := func(i int) error {
			m := msgs[i%len(msgs)]
			data, _ := json.Marshal(map[string]any{
				"received": time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), "device_id": m.dev, "severity": m.sev, "raw": m.raw,
			})
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", data); err != nil {
				return err
			}
			return rc.Flush()
		}
		// The headless browser's virtual time only advances while no request is
		// pending, so this stream ends after the backlog and asks EventSource to
		// wait a minute before reconnecting. A real backend keeps it open.
		fmt.Fprint(w, "retry: 60000\n\n")
		for i := range 30 {
			if send(i) != nil {
				return
			}
		}
	})
	return b
}
