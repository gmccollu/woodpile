package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/server"
	"github.com/gmccollu/woodpile/internal/store"
	"github.com/gmccollu/woodpile/internal/store/logfile"
)

func runCLI(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = realMain(args, &o, &e)
	return code, o.String(), e.String()
}

// udpTrap listens on a UDP and a TCP port and counts what arrives.
type udpTrap struct {
	port int
	got  chan string
}

func newTrap(t *testing.T) *udpTrap {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	tl, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		pc.Close()
		t.Skipf("TCP port %d not free: %v", port, err)
	}
	tr := &udpTrap{port: port, got: make(chan string, 100)}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			tr.got <- string(buf[:n])
		}
	}()
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			tr.got <- "tcp connection"
			c.Close()
		}
	}()
	t.Cleanup(func() { pc.Close(); tl.Close() })
	return tr
}

func (tr *udpTrap) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-tr.got:
		t.Fatalf("something was sent: %q", m)
	case <-time.After(d):
	}
}

func TestHelpNeverRunsTheCommand(t *testing.T) {
	tr := newTrap(t)
	port := fmt.Sprint(tr.port)
	for _, args := range [][]string{
		{"send", "--help"},
		{"send", "-h"},
		{"send", "--port", port, "--help"},
		{"send", "--port", port, "--count", "5", "--help"},
		{"send", "--port", port, "--proto", "tcp", "-h"},
		{"send", "--port", port, "--message", "--help"},
		{"send", "help"},
		{"help", "send"},
		{"bench", "--help"},
		{"bench", "--duration", "1h", "-h"},
		{"run", "--help"},
		{"install", "--help"},
		{"uninstall", "--help"},
		{"status", "--help"},
		{"reindex", "--help"},
		{"reset-password", "--help"},
		{"version", "--help"},
	} {
		code, out, errOut := runCLI(args...)
		name := args[0]
		if name == "help" {
			name = args[1]
		}
		if code != 0 || !strings.HasPrefix(out, "usage: woodpile "+name) || errOut != "" {
			t.Errorf("%v: code %d, stdout %.60q, stderr %q", args, code, out, errOut)
		}
	}
	tr.quiet(t, 300*time.Millisecond)
}

func TestTopLevel(t *testing.T) {
	if code, out, _ := runCLI("--help"); code != 0 || !strings.Contains(out, "reset-password") || !strings.Contains(out, "bench") {
		t.Errorf("--help: %d %q", code, out)
	}
	if code, _, errOut := runCLI("frobnicate"); code != 2 || !strings.Contains(errOut, `unknown command "frobnicate"`) {
		t.Errorf("unknown: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI(); code != 2 || !strings.Contains(errOut, "usage:") {
		t.Errorf("no args: %d %q", code, errOut)
	}
	if code, out, _ := runCLI("version"); code != 0 || !strings.HasPrefix(out, "woodpile "+version) {
		t.Errorf("version: %d %q", code, out)
	}
	if code, _, errOut := runCLI("version", "extra"); code != 2 || !strings.Contains(errOut, "unexpected argument") {
		t.Errorf("version extra: %d %q", code, errOut)
	}
}

func TestSendUsageErrors(t *testing.T) {
	tr := newTrap(t)
	port := fmt.Sprint(tr.port)
	for _, args := range [][]string{
		{"send", "--port", port, "--proto", "sctp"},
		{"send", "--port", port, "--proto", "tcp", "--framing", "crlf"},
		{"send", "--port", "70000"},
		{"send", "--port", port, "--duration", "5s"},
		{"send", "--port", port, "--duration", "5s", "--rate", "10", "--count", "3"},
		{"send", "--port", port, "--bogus"},
	} {
		if code, _, errOut := runCLI(args...); code != 2 || !strings.Contains(errOut, "woodpile send:") {
			t.Errorf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
	tr.quiet(t, 200*time.Millisecond)
}

func TestSendMessage(t *testing.T) {
	tr := newTrap(t)
	code, out, errOut := runCLI("send", "--port", fmt.Sprint(tr.port), "--message", "hello from the test")
	if code != 0 || !strings.Contains(out, "Sent 1 message to") {
		t.Fatalf("send: %d %q %q", code, out, errOut)
	}
	select {
	case m := <-tr.got:
		if !strings.HasPrefix(m, "<134>1 ") || !strings.HasSuffix(m, " hello from the test") || !strings.Contains(m, "seq=") {
			t.Errorf("message = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing arrived")
	}
}

func TestSendNothingListening(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	code, _, errOut := runCLI("send", "--proto", "tcp", "--port", fmt.Sprint(port))
	if code != 1 || !strings.Contains(errOut, "woodpile send:") || !strings.Contains(errOut, "refused") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

// testConfig writes a config whose data dir is dir/data.
func testConfig(t *testing.T, edit func(*config.Config)) (cfgPath, dataDir string) {
	t.Helper()
	dir := t.TempDir()
	c := config.Default()
	c.Storage.DataDir = filepath.Join(dir, "data")
	c.Listeners.UDP.Addr, c.Listeners.UDP.Port = "127.0.0.1", 15599
	c.Listeners.TCP.Addr, c.Listeners.TCP.Port = "127.0.0.1", 15599
	c.UI.Port = 18599
	if edit != nil {
		edit(&c)
	}
	cfgPath = filepath.Join(dir, "woodpile.toml")
	if err := config.Save(cfgPath, c); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Storage.DataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return cfgPath, c.Storage.DataDir
}

func TestResetPassword(t *testing.T) {
	cfgPath, dataDir := testConfig(t, func(c *config.Config) { c.UI.LANMode = true; c.Listeners.MaxMessageBytes = 8192 })
	hash := filepath.Join(dataDir, api.PasswordFile)
	if err := os.WriteFile(hash, []byte("$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("reset-password", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "Removed the UI password and turned LAN mode off") ||
		!strings.Contains(out, "http://localhost:18599/") {
		t.Fatalf("reset-password: %d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(hash); !os.IsNotExist(err) {
		t.Errorf("hash still there: %v", err)
	}
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.LANMode || c.Listeners.MaxMessageBytes != 8192 || c.UI.Port != 18599 {
		t.Errorf("config after reset: %+v", c)
	}
	// Again: nothing to do.
	if code, out, _ := runCLI("reset-password", "--config", cfgPath); code != 0 || !strings.Contains(out, "nothing to change") {
		t.Errorf("second run: %d %q", code, out)
	}
}

func TestResetPasswordWhileRunningInForeground(t *testing.T) {
	cfgPath, dataDir := testConfig(t, func(c *config.Config) { c.UI.LANMode = true })
	lock, err := server.LockDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	code, out, errOut := runCLI("reset-password", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "stop it (Ctrl-C) and start it again") {
		t.Errorf("%d %q %q", code, out, errOut)
	}
}

func TestResetPasswordNeedsWriteAccess(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root Unix user")
	}
	cfgPath, dataDir := testConfig(t, func(c *config.Config) { c.UI.LANMode = true })
	hash := filepath.Join(dataDir, api.PasswordFile)
	os.WriteFile(hash, []byte("x\n"), 0o600)
	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dataDir, 0o750)
	code, _, errOut := runCLI("reset-password", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "sudo") {
		t.Errorf("%d %q", code, errOut)
	}
	if _, err := os.Stat(hash); err != nil {
		t.Errorf("the hash was touched: %v", err)
	}
	if c, _ := config.Load(cfgPath); !c.UI.LANMode {
		t.Error("LAN mode was turned off although the reset failed")
	}
}

func TestResetPasswordNoConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "woodpile.toml")
	code, _, errOut := runCLI("reset-password", "--config", missing)
	if code != 1 || !strings.Contains(errOut, "no config file") {
		t.Errorf("%d %q", code, errOut)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("reset-password created a config")
	}
}

func TestReindex(t *testing.T) {
	cfgPath, dataDir := testConfig(t, nil)
	// One device's log file, written as the store writes it.
	dev := filepath.Join(dataDir, logfile.LogsDir, "192.0.2.1")
	os.MkdirAll(dev, 0o750)
	now := time.Now().UTC()
	var b []byte
	for i := range 10 {
		b = store.FormatLine(b, now.Add(time.Duration(i)*time.Millisecond), netip.MustParseAddr("192.0.2.1"), []byte(fmt.Sprintf("<134>hello %d", i)))
	}
	if err := os.WriteFile(filepath.Join(dev, logfile.DayOf(now)+".log"), b, 0o640); err != nil {
		t.Fatal(err)
	}

	lock, err := server.LockDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("reindex", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "Woodpile is running") || !strings.Contains(errOut, "stop it first") {
		t.Errorf("while locked: %d %q", code, errOut)
	}
	lock.Unlock()

	code, out, errOut := runCLI("reindex", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "files:   1") || !strings.Contains(out, "devices: 1") {
		t.Fatalf("reindex: %d %q %q", code, out, errOut)
	}
	idx, err := index.Open(filepath.Join(dataDir, index.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	d, err := idx.Device(context.Background(), "192.0.2.1")
	if err != nil || d.TotalMsgs != 10 {
		t.Errorf("device after reindex: %+v %v", d, err)
	}
	evs, _ := idx.Events(context.Background(), time.Time{}, time.Time{}, 0)
	if len(evs) == 0 || !strings.Contains(evs[len(evs)-1].Text, "woodpile reindex") {
		t.Errorf("events = %+v", evs)
	}
}

func TestStatusNotRunning(t *testing.T) {
	cfgPath, _ := testConfig(t, func(c *config.Config) {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		c.UI.Port = l.Addr().(*net.TCPAddr).Port
		l.Close()
	})
	code, out, _ := runCLI("status", "--config", cfgPath)
	if code != 1 || !strings.Contains(out, "not running") || !strings.Contains(out, "Service:") {
		t.Errorf("%d %q", code, out)
	}
}
