// Package service runs Woodpile under the OS service manager (Windows SCM,
// systemd, launchd) and installs or removes the service, with the names in
// docs/service-contract.md.
//
// `woodpile install` and `uninstall` are for machines without a package. They
// refuse when the package installed the service (the systemd unit in
// /usr/lib/systemd/system, the macOS pkg receipt, the MSI's service account), so
// a machine never ends up with two services or a package whose service vanished.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	ksvc "github.com/kardianos/service"

	"github.com/gmccollu/woodpile/internal/config"
)

// Service names (docs/service-contract.md §2). Don't change them: the packages
// and the Windows data-folder ACL (the service SID) depend on them.
const (
	WindowsName = "Woodpile"            // SCM service name
	LinuxName   = "woodpile"            // woodpile.service
	DarwinName  = "com.woodpile.daemon" // launchd label
)

// DisplayName and Description are shown by the service managers.
const (
	DisplayName = "Woodpile syslog server"
	Description = "Receives syslog and stores it as plain files. UI: http://127.0.0.1:8514/"
)

// Name returns the service name on goos.
func Name(goos string) string {
	switch goos {
	case "windows":
		return WindowsName
	case "darwin":
		return DarwinName
	}
	return LinuxName
}

// Config returns the service definition: run `exe run --service --config
// cfgPath` at boot, restarted on failure.
func Config(goos, cfgPath, exe string) *ksvc.Config {
	c := &ksvc.Config{
		Name:        Name(goos),
		DisplayName: DisplayName,
		Description: Description,
		Arguments:   []string{"run", "--service", "--config", cfgPath},
		Executable:  exe,
		Option:      ksvc.KeyValue{},
	}
	switch goos {
	case "windows":
		c.Option["StartType"] = "automatic"
		c.Option["OnFailure"] = "restart"
		c.Option["OnFailureDelayDuration"] = "5s"
		c.Option["OnFailureResetPeriod"] = 86400
	case "darwin":
		c.Option["KeepAlive"] = true
		c.Option["RunAtLoad"] = true
		c.Option["LogDirectory"] = DarwinLogDir
	default:
		c.Option["Restart"] = "on-failure"
		c.Option["LimitNOFILE"] = 65536
		c.Dependencies = []string{"After=network.target"}
	}
	return c
}

// DarwinLogDir is where a launchd daemon installed by `woodpile install` writes
// its stdout and stderr (the package's daemon uses woodpile.log in the same
// folder).
const DarwinLogDir = "/Library/Logs/Woodpile"

// ---- running under the service manager ----

// RunFunc runs the server until ctx is done. It gets the logger to use.
type RunFunc func(ctx context.Context, log *slog.Logger) error

// Run runs fn as the service. Logs go to the Windows Application event log
// (source "Woodpile", registered by the MSI) on Windows and to stdout elsewhere
// (journald and the launchd log file pick them up). It returns when the service
// manager stops the service; if fn fails on its own, the process exits with
// status 1 so the manager can restart it.
func Run(cfgPath string, stopTimeout time.Duration, fn RunFunc) error {
	prg := &program{run: fn, stopTimeout: stopTimeout}
	s, err := ksvc.New(prg, Config(runtime.GOOS, cfgPath, ""))
	if err != nil {
		return err
	}
	prg.log = slog.New(slog.NewTextHandler(os.Stdout, nil))
	if runtime.GOOS == "windows" {
		l, err := s.Logger(nil)
		if err != nil {
			// The event source is registered by the MSI; without it, fall back to
			// stderr so the error is at least visible somewhere.
			fmt.Fprintf(os.Stderr, "woodpile: the event log source %q isn't available (%v); logging to stderr\n", WindowsName, err)
			prg.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
		} else {
			prg.log = slog.New(newLoggerHandler(l, slog.LevelInfo))
		}
	}
	return s.Run()
}

type program struct {
	run         RunFunc
	log         *slog.Logger
	stopTimeout time.Duration

	cancel context.CancelFunc
	done   chan error
	stop   chan struct{}
}

func (p *program) Start(s ksvc.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan error, 1)
	p.stop = make(chan struct{})
	go func() {
		err := p.run(ctx, p.log)
		select {
		case <-p.stop:
			p.done <- err
		default:
			// Stopped without being asked: a startup error or a failure.
			if err == nil {
				err = errors.New("the server stopped unexpectedly")
			}
			p.log.Error("woodpile stopped", "err", err)
			os.Exit(1)
		}
	}()
	return nil
}

func (p *program) Stop(s ksvc.Service) error {
	close(p.stop)
	p.cancel()
	timeout := p.stopTimeout + 2*time.Second
	select {
	case err := <-p.done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("the server didn't stop within %v", timeout)
	}
}

// ---- install, uninstall, status ----

// Installed describes the Woodpile service found on this machine.
type Installed struct {
	Exists  bool   // a Woodpile service is defined
	Package bool   // and the package installed it
	Where   string // the unit, plist or SCM entry found
	// Remove is how to remove a package-installed service.
	Remove string
}

// prober is how Detect looks at the machine (faked in tests).
type prober interface {
	exists(path string) bool
	// windowsService reads the SCM entry: whether it exists and the account it
	// runs as.
	windowsService(name string) (exists bool, account string, err error)
}

// Package paths (docs/install.md, packaging/).
var (
	linuxPackageUnits = []string{"/usr/lib/systemd/system/woodpile.service", "/lib/systemd/system/woodpile.service"}
	linuxManualUnits  = []string{"/etc/systemd/system/woodpile.service"}
	darwinPlist       = "/Library/LaunchDaemons/" + DarwinName + ".plist"
	darwinPkgMarkers  = []string{"/var/db/receipts/com.woodpile.pkg.plist", "/var/db/receipts/com.woodpile.pkg.bom", "/usr/local/share/woodpile/uninstall.sh"}
	// The MSI runs the service as its virtual account; `woodpile install` doesn't.
	windowsPackageAccount = `NT SERVICE\` + WindowsName
)

func detect(goos string, p prober) (Installed, error) {
	switch goos {
	case "windows":
		ok, account, err := p.windowsService(WindowsName)
		if err != nil {
			return Installed{}, fmt.Errorf("read the service list: %w", err)
		}
		if !ok {
			return Installed{}, nil
		}
		in := Installed{Exists: true, Where: `service "` + WindowsName + `"`}
		if strings.EqualFold(strings.TrimSpace(account), windowsPackageAccount) {
			in.Package = true
			in.Remove = "Settings > Apps > Woodpile > Uninstall (or msiexec /x with the MSI)"
		}
		return in, nil
	case "darwin":
		in := Installed{}
		if p.exists(darwinPlist) {
			in.Exists, in.Where = true, darwinPlist
		}
		for _, m := range darwinPkgMarkers {
			if p.exists(m) {
				in.Package = true
				in.Remove = "sudo /usr/local/share/woodpile/uninstall.sh"
				if !in.Exists {
					// The package is installed even if its plist is gone: still one service.
					in.Exists, in.Where = true, m
				}
				break
			}
		}
		return in, nil
	default:
		in := Installed{}
		for _, u := range linuxPackageUnits {
			if p.exists(u) {
				return Installed{Exists: true, Package: true, Where: u,
					Remove: "your package manager (apt remove woodpile, dnf remove woodpile)"}, nil
			}
		}
		for _, u := range linuxManualUnits {
			if p.exists(u) {
				in.Exists, in.Where = true, u
			}
		}
		return in, nil
	}
}

// checkInstall refuses an install when any Woodpile service already exists.
func checkInstall(in Installed) error {
	if !in.Exists {
		return nil
	}
	if in.Package {
		return fmt.Errorf("Woodpile's package already installed the service (%s); it's managed by the package, so there's nothing to install. "+
			"To remove it use %s", in.Where, in.Remove)
	}
	return fmt.Errorf("a Woodpile service is already installed (%s); run `woodpile uninstall` first to replace it", in.Where)
}

// checkUninstall refuses to remove a package's service, or a missing one.
func checkUninstall(in Installed) error {
	if !in.Exists {
		return errors.New("no Woodpile service is installed")
	}
	if in.Package {
		return fmt.Errorf("the Woodpile service was installed by the package (%s); remove it with %s, so the package and the service stay in step",
			in.Where, in.Remove)
	}
	return nil
}

// Detect looks for an installed Woodpile service on this machine.
func Detect() (Installed, error) { return detect(runtime.GOOS, osProber{}) }

type osProber struct{}

func (osProber) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (osProber) windowsService(name string) (bool, string, error) { return windowsServiceAccount(name) }

// Install installs the service for cfgPath (the OS default when empty) and
// starts it. It needs administrator rights.
func Install(cfgPath string) (Installed, error) {
	in, err := Detect()
	if err != nil {
		return in, err
	}
	if err := checkInstall(in); err != nil {
		return in, err
	}
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfgPath, err = filepath.Abs(cfgPath)
	if err != nil {
		return in, err
	}
	exe, err := os.Executable()
	if err != nil {
		return in, fmt.Errorf("find this program's path: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if runtime.GOOS == "darwin" {
		if err := os.MkdirAll(DarwinLogDir, 0o755); err != nil {
			return in, adminHint(fmt.Errorf("create %s: %w", DarwinLogDir, err))
		}
	}
	s, err := ksvc.New(nopProgram{}, Config(runtime.GOOS, cfgPath, exe))
	if err != nil {
		return in, err
	}
	if err := s.Install(); err != nil {
		return in, adminHint(fmt.Errorf("install the service: %w", err))
	}
	if err := s.Start(); err != nil {
		return in, adminHint(fmt.Errorf("the service is installed but didn't start: %w", err))
	}
	return Detect()
}

// Uninstall stops and removes a service installed by Install. It keeps the
// config and the logs.
func Uninstall() error {
	in, err := Detect()
	if err != nil {
		return err
	}
	if err := checkUninstall(in); err != nil {
		return err
	}
	s, err := ksvc.New(nopProgram{}, Config(runtime.GOOS, config.DefaultConfigPath(), ""))
	if err != nil {
		return err
	}
	if st, err := s.Status(); err == nil && st == ksvc.StatusRunning {
		if err := s.Stop(); err != nil {
			return adminHint(fmt.Errorf("stop the service: %w", err))
		}
	}
	if err := s.Uninstall(); err != nil {
		return adminHint(fmt.Errorf("remove the service: %w", err))
	}
	return nil
}

// State is what the service manager says about the service.
type State string

// Service states.
const (
	StateRunning      State = "running"
	StateStopped      State = "stopped"
	StateNotInstalled State = "not installed"
	StateUnknown      State = "unknown"
)

// Status asks the service manager about the service. It never needs
// administrator rights; when the manager can't say, it returns StateUnknown and
// the reason.
func Status() (State, error) {
	s, err := ksvc.New(nopProgram{}, Config(runtime.GOOS, config.DefaultConfigPath(), ""))
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

// Restart asks the service manager to restart the service (administrator
// rights needed).
func Restart() error {
	s, err := ksvc.New(nopProgram{}, Config(runtime.GOOS, config.DefaultConfigPath(), ""))
	if err != nil {
		return err
	}
	return adminHint(s.Restart())
}

// Platform names the service manager in use, for messages.
func Platform() string { return ksvc.Platform() }

type nopProgram struct{}

func (nopProgram) Start(ksvc.Service) error { return nil }
func (nopProgram) Stop(ksvc.Service) error  { return nil }

// adminHint adds how to get the rights to a permission error.
func adminHint(err error) error {
	if err == nil {
		return nil
	}
	if !isPermission(err) {
		return err
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("%w (run it from an administrator prompt)", err)
	}
	return fmt.Errorf("%w (run it with sudo)", err)
}

func isPermission(err error) bool {
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "permission denied") || strings.Contains(s, "access is denied") ||
		strings.Contains(s, "operation not permitted") || strings.Contains(s, "must be run as root")
}
