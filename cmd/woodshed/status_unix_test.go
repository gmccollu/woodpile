//go:build !windows

package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/config"
)

// unreadableConfig is a config file in a folder this user can't enter, as the
// packages leave /etc/woodshed (0750, the service account's) for a normal user.
func unreadableConfig(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	dir := filepath.Join(t.TempDir(), "etc")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "woodshed.toml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o750) })
	return path
}

// assumeDefaults makes the defaults status falls back to point at port and
// dataDir instead of the real default UI port and data folder.
func assumeDefaults(t *testing.T, port int, dataDir string) {
	t.Helper()
	t.Cleanup(func() { defaultConfig = config.Default })
	defaultConfig = func() config.Config {
		c := config.Default()
		c.UI.Port = port
		c.Storage.DataDir = dataDir
		return c
	}
}

func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// A normal user can't read the config: status says so in one line with the
// command that can, and still shows what it can find without it.
func TestStatusConfigNeedsRoot(t *testing.T) {
	cfgPath := unreadableConfig(t)
	wantConfig := "Config:     " + cfgPath + " can't be read without root; assuming the defaults\n" +
		"            for the full status, run: sudo woodshed status --config " + cfgPath + "\n"
	check := func(name, out string, want ...string) {
		t.Helper()
		for _, w := range append([]string{"Service:", wantConfig}, want...) {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q in:\n%s", name, w, out)
			}
		}
		for _, bad := range []string{"needs root", "permission denied", "This needs"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: %q in:\n%s", name, bad, out)
			}
		}
	}

	// Nothing answers, and the data folder doesn't exist.
	assumeDefaults(t, closedPort(t), filepath.Join(t.TempDir(), "data"))
	code, out, _ := runCLI("status", "--config", cfgPath)
	if code != 1 {
		t.Errorf("not running: exit %d", code)
	}
	check("not running", out, "not running (nothing answers on http://localhost:")

	// The server answers on the default port: its own status, listeners included.
	st := api.Status{Version: "1.2.3", Build: "abc", StartedAt: time.Now(), DataDir: "/var/lib/woodshed",
		Listeners: []api.Listener{{Proto: "udp", Addr: "0.0.0.0", Port: 514, OK: true}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(st)
	}))
	defer srv.Close()
	assumeDefaults(t, serverPort(t, srv), filepath.Join(t.TempDir(), "data"))
	code, out, _ = runCLI("status", "--config", cfgPath)
	if code != 0 {
		t.Errorf("running: exit %d", code)
	}
	check("running", out, "running, version 1.2.3", "UDP 0.0.0.0:514: listening",
		"UI:         http://localhost:"+strings.TrimPrefix(srv.URL, "http://127.0.0.1:")+"/\n")

	// The data folder can't be entered either: that line gets the same plain hint.
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(data, 0o750) })
	assumeDefaults(t, closedPort(t), data)
	_, out, _ = runCLI("status", "--config", cfgPath)
	check("data folder", out, "the data folder can't be checked without root: run sudo woodshed status --config "+cfgPath)
}

// Without the config the scheme is a guess: a LAN-mode server (HTTPS) is found
// too, and reported as one that needs a login.
func TestStatusConfigNeedsRootLANMode(t *testing.T) {
	cfgPath := unreadableConfig(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"log in"}`, http.StatusUnauthorized)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the first, plain HTTP try
	srv.StartTLS()
	defer srv.Close()
	port := serverPort(t, srv)
	assumeDefaults(t, port, filepath.Join(t.TempDir(), "data"))
	code, out, _ := runCLI("status", "--config", cfgPath)
	for _, want := range []string{"the UI needs a login", "(LAN mode)",
		"log in to the UI to see them, or run sudo woodshed status --config " + cfgPath} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code != 0 {
		t.Errorf("exit %d", code)
	}
}

// A --config path the shell would split is quoted in the hint.
func TestStatusHintQuotesPath(t *testing.T) {
	if got := shellQuote("/tmp/a b/woodshed.toml"); got != "'/tmp/a b/woodshed.toml'" {
		t.Errorf("quote: %s", got)
	}
}
