// Package fwrule opens and closes the host firewall for the TLS syslog listener
// (SPEC §10.3.6), and reads the Windows rule's state for the tls_firewall
// banner. The service account can't change the firewall, so only `woodshed tls
// on|off`, `woodshed install` and the MSI (as an administrator or SYSTEM) call
// the changing functions; the running service only reads.
//
//   - Windows: one rule, RuleName, inbound TCP for woodshed.exe on every profile.
//     The MSI and `woodshed install` create it disabled; `tls on` enables it on
//     listeners.tls.port and `tls off` disables it. Its state survives upgrades.
//   - Linux: `tls on` opens the port in ufw when it's active, else in firewalld
//     when it's running (permanent, default zone, then a reload), and records
//     what it added in <data>/RecordFile; `tls off` removes only that.
//   - macOS: nothing (the application firewall works per program, not per port).
//
// Every command goes through a Runner, so the logic is tested with fakes.
package fwrule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gmccollu/woodshed/internal/atomicfile"
)

// RuleName is the Windows rule (the MSI's and `woodshed install`'s).
const RuleName = "Woodshed syslog (TLS 6514)"

// RecordFile is where `tls on` records what it added to a Linux firewall.
const RecordFile = "tls-firewall.json"

// Windows rule states (GET /api/tls firewall.state).
const (
	StateOK        = "ok"
	StateMissing   = "missing"
	StateDisabled  = "disabled"
	StateWrongPort = "wrong_port"
	StateUnknown   = "unknown"
)

// Runner runs a program and returns its combined output; a non-zero exit is an
// error (with the output still returned).
type Runner func(name string, args ...string) (string, error)

// Exec is the real Runner.
func Exec(name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		return s, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return s, nil
}

// Netsh is netsh's path (System32, never a netsh.exe found on PATH).
func Netsh() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "netsh.exe")
}

// ---- Windows ----

// rule is the part of `netsh advfirewall firewall show rule ... verbose` we read.
type rule struct {
	enabled bool
	port    string // LocalPort
	program string
}

// parseRules reads netsh's verbose rule listing (English field names). It
// returns nil when no block has a Rule Name field (no rule, or another
// language).
func parseRules(out string) []rule {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	var rules []rule
	for _, block := range strings.Split(out, "\n\n") {
		f := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok {
				f[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if _, ok := f["Rule Name"]; !ok {
			continue
		}
		rules = append(rules, rule{enabled: strings.EqualFold(f["Enabled"], "Yes"), port: f["LocalPort"], program: f["Program"]})
	}
	return rules
}

// noMatch is netsh's answer when no rule has the name (English).
func noMatch(out string) bool { return strings.Contains(out, "No rules match") }

// WindowsState reads the rule's state for port. It needs no administrator
// rights. Output it can't read (another language, an error) is StateUnknown.
func WindowsState(run Runner, port int) string {
	out, err := run(Netsh(), "advfirewall", "firewall", "show", "rule", "name="+RuleName, "verbose")
	if noMatch(out) {
		return StateMissing
	}
	rules := parseRules(out)
	if err != nil || len(rules) == 0 {
		return StateUnknown
	}
	return stateOf(rules, port)
}

func stateOf(rules []rule, port int) string {
	p := strconv.Itoa(port)
	state := StateDisabled
	for _, r := range rules {
		if !r.enabled {
			continue
		}
		if r.port == p {
			return StateOK
		}
		state = StateWrongPort
	}
	return state
}

func addArgs(exe string, port int, enable bool) []string {
	en := "no"
	if enable {
		en = "yes"
	}
	return []string{"advfirewall", "firewall", "add", "rule", "name=" + RuleName, "dir=in", "action=allow",
		"program=" + exe, "protocol=TCP", "localport=" + strconv.Itoa(port), "profile=any", "enable=" + en,
		"description=Lets devices send syslog to Woodshed over TLS. Turned on by `woodshed tls on`."}
}

// exists reports whether the rule is there, and an error when netsh's answer
// can't be read.
func exists(run Runner) (bool, error) {
	out, err := run(Netsh(), "advfirewall", "firewall", "show", "rule", "name="+RuleName)
	if noMatch(out) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// WindowsEnsure creates the rule, disabled on 6514, unless it exists (then it's
// left exactly as it is, so a reinstall keeps its state). For `woodshed
// install` and the MSI.
func WindowsEnsure(run Runner, exe string) (created bool, err error) {
	ok, err := exists(run)
	if err != nil || ok {
		return false, err
	}
	if _, err := run(Netsh(), addArgs(exe, 6514, false)...); err != nil {
		return false, err
	}
	return true, nil
}

// WindowsEnable enables the rule on port for exe, creating it if it's missing
// (portable copies and older installs).
func WindowsEnable(run Runner, exe string, port int) error {
	ok, err := exists(run)
	if err != nil {
		return err
	}
	if !ok {
		_, err := run(Netsh(), addArgs(exe, port, true)...)
		return err
	}
	_, err = run(Netsh(), "advfirewall", "firewall", "set", "rule", "name="+RuleName, "new",
		"enable=yes", "localport="+strconv.Itoa(port), "program="+exe)
	return err
}

// WindowsDisable disables the rule (it stays, for the next `tls on`).
func WindowsDisable(run Runner) error {
	ok, err := exists(run)
	if err != nil || !ok {
		return err
	}
	_, err = run(Netsh(), "advfirewall", "firewall", "set", "rule", "name="+RuleName, "new", "enable=no")
	return err
}

// WindowsRemove deletes the rule (uninstall). A missing rule isn't an error.
func WindowsRemove(run Runner) error {
	ok, err := exists(run)
	if err != nil || !ok {
		return err
	}
	_, err = run(Netsh(), "advfirewall", "firewall", "delete", "rule", "name="+RuleName)
	return err
}

// ---- Linux ----

// Record is what `tls on` added, by firewall ("6514/tcp" entries).
type Record struct {
	Ufw       []string `json:"ufw,omitempty"`
	Firewalld []string `json:"firewalld,omitempty"`
}

func readRecord(dataDir string) (Record, error) {
	var r Record
	b, err := atomicfile.ReadFile(filepath.Join(dataDir, RecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", RecordFile, err)
	}
	return r, nil
}

func writeRecord(dataDir string, r Record) error {
	p := filepath.Join(dataDir, RecordFile)
	if len(r.Ufw) == 0 && len(r.Firewalld) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	return atomicfile.WriteFile(p, append(b, '\n'), 0o640)
}

func ufwActive(run Runner) bool {
	out, err := run("ufw", "status")
	return err == nil && strings.Contains(out, "Status: active")
}

func firewalldRunning(run Runner) bool {
	out, err := run("firewall-cmd", "--state")
	return err == nil && strings.TrimSpace(out) == "running"
}

// LinuxCommands are the commands for the common firewalls, for when neither
// is active (and for Settings).
func LinuxCommands(port int) string {
	p := strconv.Itoa(port) + "/tcp"
	return "  ufw:       sudo ufw allow " + p + "\n" +
		"  firewalld: sudo firewall-cmd --permanent --add-port=" + p + " && sudo firewall-cmd --reload\n" +
		"  Other rules (nftables, iptables): allow inbound TCP port " + strconv.Itoa(port) + ".\n"
}

// LinuxOpen opens port in ufw (when active) or firewalld (when running), and
// records it unless it was already open (then `tls off` leaves it alone). It
// returns what it did, for the user.
func LinuxOpen(run Runner, dataDir string, port int) (string, error) {
	p := strconv.Itoa(port) + "/tcp"
	rec, err := readRecord(dataDir)
	if err != nil {
		return "", err
	}
	switch {
	case ufwActive(run):
		out, err := run("ufw", "allow", p)
		if err != nil {
			return "", err
		}
		if strings.Contains(out, "Skipping") {
			return "ufw already allows " + p + "; left as it is.", nil
		}
		if !slices.Contains(rec.Ufw, p) {
			rec.Ufw = append(rec.Ufw, p)
		}
		return "Opened " + p + " in ufw.", writeRecord(dataDir, rec)
	case firewalldRunning(run):
		if _, err := run("firewall-cmd", "--permanent", "--query-port="+p); err == nil {
			return "firewalld already allows " + p + "; left as it is.", nil
		}
		if _, err := run("firewall-cmd", "--permanent", "--add-port="+p); err != nil {
			return "", err
		}
		if _, err := run("firewall-cmd", "--reload"); err != nil {
			return "", err
		}
		if !slices.Contains(rec.Firewalld, p) {
			rec.Firewalld = append(rec.Firewalld, p)
		}
		return "Opened " + p + " in firewalld (default zone).", writeRecord(dataDir, rec)
	}
	return "No active ufw or firewalld found, so the firewall wasn't changed. If devices can't connect, open the port:\n" +
		LinuxCommands(port), nil
}

// LinuxClose removes what LinuxOpen recorded, and nothing else.
func LinuxClose(run Runner, dataDir string) (string, error) {
	rec, err := readRecord(dataDir)
	if err != nil {
		return "", err
	}
	if len(rec.Ufw) == 0 && len(rec.Firewalld) == 0 {
		return "Woodshed didn't open a firewall port, so none was closed.", nil
	}
	var did []string
	var errs []error
	var keep Record
	for _, p := range rec.Ufw {
		if _, err := run("ufw", "delete", "allow", p); err != nil {
			errs = append(errs, err)
			keep.Ufw = append(keep.Ufw, p)
			continue
		}
		did = append(did, "closed "+p+" in ufw")
	}
	reload := false
	for _, p := range rec.Firewalld {
		if _, err := run("firewall-cmd", "--permanent", "--remove-port="+p); err != nil {
			errs = append(errs, err)
			keep.Firewalld = append(keep.Firewalld, p)
			continue
		}
		reload = true
		did = append(did, "closed "+p+" in firewalld")
	}
	if reload {
		if _, err := run("firewall-cmd", "--reload"); err != nil {
			errs = append(errs, err)
		}
	}
	if err := writeRecord(dataDir, keep); err != nil {
		errs = append(errs, err)
	}
	msg := ""
	if len(did) > 0 {
		msg = strings.ToUpper(did[0][:1]) + strings.Join(did, ", ")[1:] + "."
	}
	return msg, errors.Join(errs...)
}
