package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/server"
	"github.com/gmccollu/woodshed/internal/service"
	"github.com/gmccollu/woodshed/internal/updatecheck"
)

func TestUpdateCheckCommand(t *testing.T) {
	defer noService()
	cfgPath, _ := testConfig(t, nil)
	if loadCfg(t, cfgPath).Updates.Check != "ask" {
		t.Fatal("default isn't ask")
	}
	for _, v := range []string{"on", "off", "ask"} {
		code, out, errOut := runCLI("update-check", v, "--config", cfgPath)
		if code != 0 || errOut != "" || !strings.Contains(out, "isn't running") {
			t.Errorf("%s: %d %q %q", v, code, out, errOut)
		}
		if got := loadCfg(t, cfgPath).Updates.Check; got != v {
			t.Errorf("%s: saved %q", v, got)
		}
	}
	// Installers pass what the admin typed.
	if code, _, _ := runCLI("update-check", "--config", cfgPath, " OFF "); code != 0 || loadCfg(t, cfgPath).Updates.Check != "off" {
		t.Errorf("OFF: %d %q", code, loadCfg(t, cfgPath).Updates.Check)
	}
	if code, out, _ := runCLI("update-check", "off", "--config", cfgPath); code != 0 || !strings.Contains(out, "already off") {
		t.Errorf("again: %d %q", code, out)
	}
	// Invalid values change nothing and exit 2 (the installers log it and go on).
	for _, args := range [][]string{{"update-check"}, {"update-check", "yes"}, {"update-check", "maybe"}, {"update-check", "on", "extra"}, {"update-check", ""}} {
		code, _, errOut := runCLI(append(args, "--config", cfgPath)...)
		if code != 2 || errOut == "" {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
	if loadCfg(t, cfgPath).Updates.Check != "off" {
		t.Error("an invalid value changed the setting")
	}
	if code, out, _ := runCLI("update-check", "--help"); code != 0 || !strings.HasPrefix(out, "usage: woodshed update-check") ||
		!strings.Contains(out, updatecheck.URL) || !strings.Contains(out, "-no-restart") {
		t.Errorf("help: %d %q", code, out)
	}
	// This test binary is a development build.
	if _, out, _ := runCLI("update-check", "on", "--config", cfgPath); !strings.Contains(out, "isn't a release") {
		t.Errorf("no dev-build note: %q", out)
	}
}

// Before the service's first start there's no config yet: update-check creates
// it with the defaults a server would use there, owned like its folder.
func TestUpdateCheckCreatesTheConfig(t *testing.T) {
	defer noService()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "woodshed.toml")
	code, out, errOut := runCLI("update-check", "off", "--config", cfgPath, "--no-restart")
	if code != 0 || errOut != "" {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	c := loadCfg(t, cfgPath)
	if c.Updates.Check != "off" || c.Storage.DataDir != filepath.Join(dir, "woodshed-data") || c.UI.Port != config.DefaultUIPort {
		t.Errorf("created %+v", c)
	}
	if fi, err := os.Stat(cfgPath); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v %v", fi.Mode(), err)
	}
	// A missing folder is an error, not a new tree of folders.
	code, _, errOut = runCLI("update-check", "off", "--config", filepath.Join(dir, "nope", "woodshed.toml"))
	if code != 1 || !strings.Contains(errOut, "no folder") {
		t.Errorf("missing folder: %d %q", code, errOut)
	}
}

func TestUpdateCheckRestartsTheService(t *testing.T) {
	defer noService()
	cfgPath, dataDir := testConfig(t, nil)
	lock, err := server.LockDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	restarts := 0
	serviceStatus = func() (service.State, error) { return service.StateRunning, nil }
	serviceRestart = func() error { restarts++; return nil }
	code, out, errOut := runCLI("update-check", "on", "--config", cfgPath)
	if code != 0 || restarts != 1 || !strings.Contains(out, "Restarted the service") {
		t.Errorf("%d restarts=%d %q %q", code, restarts, out, errOut)
	}
	// --no-restart: saved, not restarted, and it says so.
	code, out, _ = runCLI("update-check", "off", "--config", cfgPath, "--no-restart")
	if code != 0 || restarts != 1 || !strings.Contains(out, "next starts") || loadCfg(t, cfgPath).Updates.Check != "off" {
		t.Errorf("--no-restart: %d restarts=%d %q", code, restarts, out)
	}
}

func TestStatusUpdateRow(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.DataDir = dir
	row := func(check, ver string) string {
		var b bytes.Buffer
		c := cfg
		c.Updates.Check = check
		updateRows(&tabw{w: &b}, c, ver)
		return b.String()
	}
	if s := row("on", "0.0.0-dev"); !strings.Contains(s, "no update check in this build") {
		t.Errorf("dev: %q", s)
	}
	if s := row("off", "1.1.0"); !strings.Contains(s, "check off") {
		t.Errorf("off: %q", s)
	}
	if s := row("ask", "1.1.0"); !strings.Contains(s, "the UI asks") {
		t.Errorf("ask: %q", s)
	}
	if s := row("on", "1.1.0"); !strings.Contains(s, "no check yet") {
		t.Errorf("on, never: %q", s)
	}
	last := time.Date(2026, 11, 13, 9, 12, 0, 0, time.UTC)
	write := func(v any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, updatecheck.StateFile), b, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"last_check": last, "last_result": "ok", "latest": map[string]any{"version": "1.2.0", "url": "x"}})
	if s := row("on", "1.1.0"); !strings.Contains(s, "OK, 1.2.0 is the latest") {
		t.Errorf("on, ok: %q", s)
	}
	write(map[string]any{"last_check": last, "last_result": "error", "last_error": "no answer within 10 s"})
	if s := row("on", "1.1.0"); !strings.Contains(s, "failed: no answer within 10 s") {
		t.Errorf("on, failed: %q", s)
	}
}
