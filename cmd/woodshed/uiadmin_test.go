package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/server"
	"github.com/gmccollu/woodshed/internal/service"
)

func readHashFile(t *testing.T, dataDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataDir, api.PasswordFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loadCfg(t *testing.T, path string) config.Config {
	t.Helper()
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSetPasswordStdin(t *testing.T) {
	defer noService()
	cfgPath, dataDir := testConfig(t, nil)
	stdin = strings.NewReader("correct horse battery\n")
	code, out, errOut := runCLI("set-password", "--stdin", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "Set the UI password.") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	// The server's own format, and its mode.
	h := readHashFile(t, dataDir)
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") || !strings.HasSuffix(h, "\n") {
		t.Errorf("hash %q", h)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dataDir, api.PasswordFile)); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode())
		}
	}
	// The first password turns on require_login_local, and says so and how to undo it.
	if !loadCfg(t, cfgPath).UI.RequireLoginLocal {
		t.Error("require_login_local is still off")
	}
	if !strings.Contains(out, "Turned on ui.require_login_local") || !strings.Contains(out, "require_login_local = false") {
		t.Errorf("no require_login_local note: %q", out)
	}
	if !strings.Contains(out, "isn't running") || !strings.Contains(out, "lan-mode on") {
		t.Errorf("next steps: %q", out)
	}

	// Changing it: a new hash, and require_login_local isn't touched again.
	c := loadCfg(t, cfgPath)
	c.UI.RequireLoginLocal = false
	config.Save(cfgPath, c)
	stdin = strings.NewReader("another long one")
	code, out, errOut = runCLI("set-password", "--stdin", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "Changed the UI password.") || strings.Contains(out, "require_login_local") {
		t.Fatalf("change: %d %q %q", code, out, errOut)
	}
	if readHashFile(t, dataDir) == h {
		t.Error("the hash didn't change")
	}
	if loadCfg(t, cfgPath).UI.RequireLoginLocal {
		t.Error("a change turned require_login_local back on")
	}
}

func TestSetPasswordPrompts(t *testing.T) {
	defer noService()
	cfgPath, dataDir := testConfig(t, nil)
	answers := []string{"first password", "other password"}
	var prompts []string
	readSecret = func(p string, _ io.Writer) (string, error) {
		prompts = append(prompts, p)
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	code, _, errOut := runCLI("set-password", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "don't match") || len(prompts) != 2 {
		t.Fatalf("mismatch: %d %q %q", code, errOut, prompts)
	}
	if _, err := os.Stat(filepath.Join(dataDir, api.PasswordFile)); !os.IsNotExist(err) {
		t.Error("a mismatch wrote a hash")
	}

	answers = []string{"same password", "same password"}
	if code, out, errOut := runCLI("set-password", "--config", cfgPath); code != 0 || !strings.Contains(out, "Set the UI password.") {
		t.Fatalf("match: %d %q %q", code, out, errOut)
	}

	// Too short: refused at the first prompt, as the UI refuses it.
	answers = []string{"short"}
	if code, _, errOut := runCLI("set-password", "--config", cfgPath); code != 1 || !strings.Contains(errOut, "At least 8") {
		t.Errorf("short: %d %q", code, errOut)
	}

	// No terminal and no --stdin.
	readSecret = func(string, io.Writer) (string, error) { return "", errors.New("stdin isn't a terminal; pass --stdin") }
	if code, _, errOut := runCLI("set-password", "--config", cfgPath); code != 1 || !strings.Contains(errOut, "--stdin") {
		t.Errorf("no terminal: %d %q", code, errOut)
	}
	stdin = strings.NewReader("")
	if code, _, errOut := runCLI("set-password", "--stdin", "--config", cfgPath); code != 1 || !strings.Contains(errOut, "no password on stdin") {
		t.Errorf("empty stdin: %d %q", code, errOut)
	}
}

func TestSetPasswordRestartsTheService(t *testing.T) {
	defer noService()
	restarts := 0
	serviceStatus = func() (service.State, error) { return service.StateRunning, nil }
	serviceRestart = func() error { restarts++; return nil }
	cfgPath, dataDir := testConfig(t, nil)
	lock, err := server.LockDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	stdin = strings.NewReader("correct horse battery\n")
	code, out, errOut := runCLI("set-password", "--stdin", "--config", cfgPath)
	if code != 0 || restarts != 1 || !strings.Contains(out, "Restarted the service") {
		t.Errorf("%d restarts=%d %q %q", code, restarts, out, errOut)
	}

	// A foreground copy on the folder: told to restart it, nothing restarted.
	noService()
	stdin = strings.NewReader("correct horse battery\n")
	code, out, _ = runCLI("set-password", "--stdin", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "stop it (Ctrl-C) and start it again") {
		t.Errorf("foreground: %d %q", code, out)
	}
}

func TestSetPasswordNeedsWriteAccess(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root Unix user")
	}
	defer noService()
	cfgPath, dataDir := testConfig(t, nil)
	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dataDir, 0o750)
	stdin = strings.NewReader("correct horse battery\n")
	code, _, errOut := runCLI("set-password", "--stdin", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "sudo") {
		t.Errorf("%d %q", code, errOut)
	}
	if loadCfg(t, cfgPath).UI.RequireLoginLocal {
		t.Error("the config changed although the password wasn't set")
	}
}

// writeTestCert writes a self-signed certificate as the UI's and returns its
// SHA-256 as status shows it.
func writeTestCert(t *testing.T, dataDir string) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dataDir, api.CertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	sum := sha256.Sum256(der)
	var parts []string
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(parts, ":")
}

func TestLANMode(t *testing.T) {
	defer noService()
	cfgPath, dataDir := testConfig(t, nil)

	// No password yet: refused, pointing at set-password.
	code, _, errOut := runCLI("lan-mode", "on", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "set-password") {
		t.Fatalf("no password: %d %q", code, errOut)
	}
	if loadCfg(t, cfgPath).UI.LANMode {
		t.Fatal("LAN mode turned on without a password")
	}

	stdin = strings.NewReader("correct horse battery\n")
	if code, _, errOut := runCLI("set-password", "--stdin", "--config", cfgPath); code != 0 {
		t.Fatal(errOut)
	}
	// A port clash with the syslog listeners (15599 in testConfig).
	code, _, errOut = runCLI("lan-mode", "on", "--port", "15599", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "syslog listener port") {
		t.Errorf("clash: %d %q", code, errOut)
	}
	if loadCfg(t, cfgPath).UI.LANMode {
		t.Fatal("LAN mode turned on with a clashing port")
	}

	fp := writeTestCert(t, dataDir)
	code, out, errOut := runCLI("lan-mode", "--config", cfgPath, "--port", "18600", "on")
	if code != 0 || !strings.Contains(out, "Turned LAN mode on (port 18600)") || !strings.Contains(out, "isn't running") {
		t.Fatalf("on: %d %q %q", code, out, errOut)
	}
	if c := loadCfg(t, cfgPath); !c.UI.LANMode || c.UI.Port != 18600 {
		t.Errorf("config: %+v", c.UI)
	}
	if addrs, _ := api.StableAddrs(); len(addrs) > 0 && !strings.Contains(out, "https://") {
		t.Errorf("no URL: %q", out)
	}
	if !strings.Contains(out, ":18600/") || !strings.Contains(out, "Certificate SHA-256: "+fp) {
		t.Errorf("URL or fingerprint missing: %q", out)
	}

	// Again: nothing to change, the addresses still shown.
	if code, out, _ := runCLI("lan-mode", "on", "--config", cfgPath); code != 0 || !strings.Contains(out, "nothing to change") || !strings.Contains(out, fp) {
		t.Errorf("again: %d %q", code, out)
	}

	if code, out, _ := runCLI("lan-mode", "off", "--config", cfgPath); code != 0 || !strings.Contains(out, "http://localhost:18600/") {
		t.Errorf("off: %d %q", code, out)
	}
	if loadCfg(t, cfgPath).UI.LANMode {
		t.Error("still on")
	}
	for _, args := range [][]string{{"lan-mode"}, {"lan-mode", "maybe"}, {"lan-mode", "on", "extra"}} {
		if code, _, _ := runCLI(append(args, "--config", cfgPath)...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestLANModeRestartsTheService(t *testing.T) {
	defer noService()
	cfgPath, dataDir := testConfig(t, nil)
	stdin = strings.NewReader("correct horse battery\n")
	if code, _, errOut := runCLI("set-password", "--stdin", "--config", cfgPath); code != 0 {
		t.Fatal(errOut)
	}
	lock, err := server.LockDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	restarts := 0
	serviceStatus = func() (service.State, error) { return service.StateRunning, nil }
	serviceRestart = func() error { restarts++; return nil }
	waitForRestart = func(string, server.RunState, time.Duration) (server.RunState, bool) {
		return server.RunState{PID: 2, UI: server.RunStateUI{Listening: true, LANMode: true, CertFingerprint: "AB:CD"}}, true
	}
	code, out, errOut := runCLI("lan-mode", "on", "--config", cfgPath)
	if code != 0 || restarts != 1 || !strings.Contains(out, "Restarted the service") || !strings.Contains(out, "Certificate SHA-256: AB:CD") {
		t.Errorf("%d restarts=%d %q %q", code, restarts, out, errOut)
	}

	// The service manager refuses: reported, exit 1 (the config is saved).
	serviceRestart = func() error { return errNoServiceManager }
	code, _, errOut = runCLI("lan-mode", "off", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "couldn't restart the service") {
		t.Errorf("refused: %d %q", code, errOut)
	}
}

// With login required, status shows what the run-state file says: listeners,
// banners and the certificate; without access to it, how to get them.
func TestStatusNeedsLoginReadsRunState(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"log in"}`, http.StatusUnauthorized)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	cfgPath, dataDir := testConfig(t, func(c *config.Config) { c.UI.Port = ln.Addr().(*net.TCPAddr).Port })

	code, out, _ := runCLI("status", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "the UI needs a login") || !strings.Contains(out, asAdmin("status")) {
		t.Errorf("no run-state: %d %q", code, out)
	}

	rs := server.RunState{PID: 4242, Version: "1.2.3", StartedAt: time.Now(), DataDir: dataDir,
		UI: server.RunStateUI{Listening: true, CertFingerprint: "AB:CD"},
		Listeners: []api.Listener{{Proto: "udp", Addr: "0.0.0.0", Port: 514, OK: true},
			{Proto: "tcp", Addr: "0.0.0.0", Port: 514, Error: "address already in use"}},
		Banners: []api.Banner{{Kind: "udp_buffer_small", Level: api.LevelWarn, Text: "UDP receive buffer is 4 MiB"}}}
	b, _ := json.Marshal(rs)
	os.WriteFile(filepath.Join(dataDir, server.StateFileName), b, 0o640)
	code, out, _ = runCLI("status", "--config", cfgPath)
	for _, want := range []string{"running (pid 4242), version 1.2.3", "UDP 0.0.0.0:514: listening", "TCP 0.0.0.0:514: NOT listening",
		"[warn] UDP receive buffer is 4 MiB", "SHA-256 AB:CD"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if code != 0 || strings.Contains(out, "log in to the UI") {
		t.Errorf("%d %q", code, out)
	}
}
