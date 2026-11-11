//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ksvc "github.com/kardianos/service"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/packaging"
)

// Firewall rules, named as the MSI names them (packaging/windows/woodpile.wxs).
var windowsFirewallRules = []struct{ name, proto string }{
	{"Woodpile syslog (UDP 514)", "UDP"},
	{"Woodpile syslog (TCP 514)", "TCP"},
}

func requireAdmin() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("installing or removing the service needs administrator rights: run it from an administrator prompt")
	}
	return nil
}

func windowsExe() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "Woodpile", "woodpile.exe")
}

// setDataACL gives dir the MSI's protected DACL (packaging.WindowsDataSDDL).
// Inheritable entries propagate to what's already inside.
func setDataACL(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(packaging.WindowsDataSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
}

func installOS(o InstallOptions) (Result, error) {
	h := realHost(o.Out)
	exe := windowsExe()
	dataDir, err := dataDirFor(o.ConfigPath)
	if err != nil {
		return Result{}, err
	}
	res := Result{Where: `service "` + WindowsName + `"`, Exe: exe, Config: o.ConfigPath, DataDir: dataDir, Account: WindowsAccount}

	if err := h.copyFile(o.Exe, exe, 0o755); err != nil {
		return res, fmt.Errorf("copy the binary to %s: %w", exe, err)
	}
	h.say("installed %s", exe)

	dirs := []string{dataDir}
	if cd := filepath.Dir(o.ConfigPath); !strings.EqualFold(filepath.Clean(cd), filepath.Clean(dataDir)) {
		dirs = append(dirs, cd)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return res, err
		}
		if err := setDataACL(d); err != nil {
			return res, fmt.Errorf("set the permissions on %s: %w", d, err)
		}
	}
	h.say("%s is writable only by %s, SYSTEM and Administrators", dataDir, WindowsAccount)

	// The event source the service logs to, registered as the MSI does.
	if err := eventlog.InstallAsEventCreate(WindowsName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return res, fmt.Errorf("register the event log source: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return res, adminHint(err)
	}
	defer m.Disconnect()
	s, err := m.CreateService(WindowsName, exe, mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		ServiceStartName: WindowsAccount,
		DisplayName:      DisplayName,
		Description:      Description,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "run", "--service", "--config", o.ConfigPath)
	if err != nil {
		return res, fmt.Errorf("create the service: %w", err)
	}
	defer s.Close()
	// Restart on failure, also when it exits with an error (the MSI's ServiceConfig).
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 5 * time.Second}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 86400); err != nil {
		return res, err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return res, err
	}
	h.say("created service %s, running as %s", WindowsName, WindowsAccount)

	for _, r := range windowsFirewallRules {
		_, _ = h.run("netsh", "advfirewall", "firewall", "delete", "rule", "name="+r.name)
		if _, err := h.run("netsh", "advfirewall", "firewall", "add", "rule", "name="+r.name, "dir=in", "action=allow",
			"program="+exe, "protocol="+r.proto, "localport=514", "profile=any",
			"description=Lets devices send syslog to Woodpile over "+r.proto+"."); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("couldn't add the firewall rule %q: %v", r.name, err))
		}
	}
	if err := s.Start(); err != nil {
		return res, fmt.Errorf("the service is installed but didn't start (see Event Viewer > Application): %w", err)
	}
	return res, nil
}

func uninstallOS(o UninstallOptions) (Result, error) {
	h := realHost(o.Out)
	res := Result{Where: `service "` + WindowsName + `"`, Account: WindowsAccount}
	m, err := mgr.Connect()
	if err != nil {
		return res, adminHint(err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(WindowsName)
	if err != nil {
		return res, err
	}
	cfg, err := s.Config()
	if err != nil {
		s.Close()
		return res, err
	}
	exe, cfgPath := parseWindowsCommand(cfg.BinaryPathName)
	res.Exe, res.Config = exe, cfgPath
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err == nil {
			for i := 0; i < 60; i++ {
				if st, err := s.Query(); err != nil || st.State == svc.Stopped {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
	if err := s.Delete(); err != nil {
		s.Close()
		return res, fmt.Errorf("delete the service: %w", err)
	}
	s.Close()
	h.say("removed service %s", WindowsName)
	for _, r := range windowsFirewallRules {
		_, _ = h.run("netsh", "advfirewall", "firewall", "delete", "rule", "name="+r.name)
	}
	_ = eventlog.Remove(WindowsName)

	if exe != "" && strings.EqualFold(filepath.Clean(exe), filepath.Clean(windowsExe())) {
		if err := removeExe(exe); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("couldn't delete %s now (%v); it's deleted at the next restart", exe, err))
		} else {
			_ = os.Remove(filepath.Dir(exe))
			h.say("removed %s", exe)
		}
	}
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	dataDir, err := dataDirFor(cfgPath)
	if err != nil {
		dataDir = filepath.Dir(cfgPath)
	}
	res.DataDir = dataDir
	return res, deleteData(h, o, &res, cfgPath, dataDir)
}

// removeExe deletes the installed binary, which may be the one running this
// command: then it's renamed out of the way and deleted at the next restart.
func removeExe(exe string) error {
	err := os.Remove(exe)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if rerr := os.Rename(exe, old); rerr != nil {
		return err
	}
	p, perr := windows.UTF16PtrFromString(old)
	if perr != nil {
		return err
	}
	if merr := windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); merr != nil {
		return merr
	}
	return errors.New("it's in use")
}

// parseWindowsCommand reads the binary and --config back from a service's
// command line.
func parseWindowsCommand(cmdline string) (exe, cfgPath string) {
	args, err := windows.DecomposeCommandLine(cmdline)
	if err != nil || len(args) == 0 {
		return "", ""
	}
	exe = args[0]
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--config" {
			cfgPath = args[i+1]
		}
	}
	return exe, cfgPath
}

func statusOS(runner) (State, error) {
	s, err := ksvc.New(nopProgram{}, runConfig(config.DefaultConfigPath()))
	if err != nil {
		return StateUnknown, err
	}
	st, err := s.Status()
	if errors.Is(err, ksvc.ErrNotInstalled) {
		return StateNotInstalled, nil
	}
	if err != nil {
		return StateUnknown, err
	}
	switch st {
	case ksvc.StatusRunning:
		return StateRunning, nil
	case ksvc.StatusStopped:
		return StateStopped, nil
	}
	return StateUnknown, nil
}

func restartOS(runner) error {
	s, err := ksvc.New(nopProgram{}, runConfig(config.DefaultConfigPath()))
	if err != nil {
		return err
	}
	return s.Restart()
}

type nopProgram struct{}

func (nopProgram) Start(ksvc.Service) error { return nil }
func (nopProgram) Stop(ksvc.Service) error  { return nil }
