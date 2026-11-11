package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/loadgen"
)

// syncBuf is a goroutine-safe buffer for a child's stdout.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestPortable runs `woodpile portable` (on high loopback ports, since tests never
// bind 514), with its UI port already taken so it falls back, checks the console
// banner and the UI, sends from two protocols, watches the new-device line, then
// stops it with SIGTERM (Ctrl-Break on Windows) and checks everything was flushed.
func TestPortable(t *testing.T) {
	if testingShort() {
		t.Skip("-short")
	}
	dir := t.TempDir()
	syslogPort, uiPort := pickPorts(t)
	// Something else (an installed Woodpile, say) has the UI port.
	hold, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", uiPort))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()

	var out syncBuf
	cmd := exec.Command(binary, "portable", "--dir", dir, "--no-browser",
		"--port", fmt.Sprint(syslogPort), "--listen", "127.0.0.1", "--ui-port", fmt.Sprint(uiPort))
	cmd.Stdout, cmd.Stderr = &out, &out
	prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		select {
		case <-exited:
		default:
			_ = cmd.Process.Kill() // our own child, by PID
			<-exited
		}
		if t.Failed() {
			t.Logf("console:\n%s", out.String())
		}
	}()

	waitFor(t, "the banner", 20*time.Second, func() bool { return strings.Contains(out.String(), "Press Ctrl-C") })
	banner := out.String()
	for _, want := range []string{
		"portable mode",
		"Data folder:  " + dir,
		fmt.Sprintf("Listening on UDP %d and TCP %d on 127.0.0.1", syslogPort, syslogPort),
		"Send logs to: 127.0.0.1",
		fmt.Sprintf("port %d is in use", uiPort),
		"Your logs stay in " + dir,
	} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner lacks %q", want)
		}
	}
	var ui int
	for p := uiPort + 1; p <= uiPort+16; p++ {
		if strings.Contains(banner, fmt.Sprintf("http://localhost:%d/", p)) {
			ui = p
		}
	}
	if ui == 0 {
		t.Fatalf("no fallback UI URL in the banner:\n%s", banner)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", ui))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("UI on the fallback port: %v %v", resp, err)
	}
	resp.Body.Close()

	run := loadgen.NewRunID()
	for i, proto := range []string{"udp", "tcp"} {
		if _, err := loadgen.Send(context.Background(), loadgen.SendOptions{
			Addr: fmt.Sprintf("127.0.0.1:%d", syslogPort), Proto: proto, Count: 2000, Rate: 4000, RunID: run, FirstSeq: i * 2000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the new-device line", 20*time.Second, func() bool { return strings.Contains(out.String(), "New device 127.0.0.1 (first message") })
	if n := strings.Count(out.String(), "New device"); n != 1 {
		t.Errorf("%d new-device lines for one device", n)
	}

	// A second copy on the same folder is refused, kindly.
	second, _ := exec.Command(binary, "portable", "--dir", dir, "--no-browser", "--port", fmt.Sprint(syslogPort),
		"--listen", "127.0.0.1", "--ui-port", fmt.Sprint(ui)).CombinedOutput()
	if !strings.Contains(string(second), "Woodpile is already running on "+dir) {
		t.Errorf("second copy: %s", second)
	}

	time.Sleep(300 * time.Millisecond) // let the TCP data be read; the flush is what's tested
	t0 := time.Now()
	if err := terminate(cmd); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		exited <- err
		if err != nil {
			t.Fatalf("portable exited with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("portable didn't stop within 10 s")
	}
	took := time.Since(t0)
	if !strings.Contains(out.String(), "Woodpile stopped; everything is saved in "+dir) {
		t.Errorf("no clean-stop line")
	}
	if took > 5*time.Second {
		t.Errorf("stopping took %v; Windows allows about 5 s after a console close", took)
	}
	v, err := loadgen.Verify(dir, run, 4000)
	if err != nil || v.Missing != 0 || v.Duplicates != 0 {
		t.Fatalf("on disk after stopping: %+v %v", v, err)
	}
	t.Logf("portable: UI fell back %d -> %d, 4000/4000 on disk, stopped in %v", uiPort, ui, took.Round(time.Millisecond))
}
