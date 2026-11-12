package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type fakeProber struct {
	files map[string]bool
	svc   bool
	msi   bool
	err   error
}

func (f fakeProber) exists(p string) bool { return f.files[p] }
func (f fakeProber) windowsService(string) (bool, bool, error) {
	return f.svc, f.msi, f.err
}

func files(paths ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func TestDetectAndRefusals(t *testing.T) {
	cases := []struct {
		name              string
		goos              string
		p                 fakeProber
		exists, pkg       bool
		installErr, unErr string // substrings; "" means allowed
	}{
		{name: "linux nothing", goos: "linux", p: fakeProber{},
			unErr: "no Woodshed service"},
		{name: "linux deb/rpm unit", goos: "linux", p: fakeProber{files: files("/usr/lib/systemd/system/woodshed.service")},
			exists: true, pkg: true, installErr: "package already installed", unErr: "installed by the package"},
		{name: "linux old-style package unit", goos: "linux", p: fakeProber{files: files("/lib/systemd/system/woodshed.service")},
			exists: true, pkg: true, installErr: "package already installed", unErr: "package manager"},
		{name: "linux both (package wins)", goos: "linux",
			p:      fakeProber{files: files("/usr/lib/systemd/system/woodshed.service", "/etc/systemd/system/woodshed.service")},
			exists: true, pkg: true, installErr: "package", unErr: "package"},
		{name: "linux manual unit", goos: "linux", p: fakeProber{files: files("/etc/systemd/system/woodshed.service")},
			exists: true, installErr: "uninstall` first"},
		{name: "darwin nothing", goos: "darwin", p: fakeProber{}, unErr: "no Woodshed service"},
		{name: "darwin pkg", goos: "darwin",
			p:      fakeProber{files: files(darwinPlist, "/var/db/receipts/com.woodshed.pkg.plist", "/usr/local/share/woodshed/uninstall.sh")},
			exists: true, pkg: true, installErr: "package already installed", unErr: "uninstall.sh"},
		{name: "darwin pkg receipt without plist", goos: "darwin",
			p:      fakeProber{files: files("/var/db/receipts/com.woodshed.pkg.bom")},
			exists: true, pkg: true, installErr: "package", unErr: "package"},
		{name: "darwin manual plist", goos: "darwin", p: fakeProber{files: files(darwinPlist)},
			exists: true, installErr: "already installed"},
		{name: "windows nothing", goos: "windows", p: fakeProber{}, unErr: "no Woodshed service"},
		{name: "windows MSI", goos: "windows", p: fakeProber{svc: true, msi: true},
			exists: true, pkg: true, installErr: "package already installed", unErr: "Apps"},
		{name: "windows MSI key without its service", goos: "windows", p: fakeProber{msi: true},
			exists: true, pkg: true, installErr: "package", unErr: "package"},
		{name: "windows manual (same account as the MSI, no MSI key)", goos: "windows", p: fakeProber{svc: true},
			exists: true, installErr: "already installed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, err := detect(c.goos, c.p)
			if err != nil {
				t.Fatal(err)
			}
			if in.Exists != c.exists || in.Package != c.pkg {
				t.Fatalf("detect = %+v, want exists=%v package=%v", in, c.exists, c.pkg)
			}
			check := func(what string, err error, want string) {
				t.Helper()
				switch {
				case want == "" && err != nil:
					t.Errorf("%s refused: %v", what, err)
				case want != "" && err == nil:
					t.Errorf("%s allowed, want a refusal mentioning %q", what, want)
				case want != "" && !strings.Contains(err.Error(), want):
					t.Errorf("%s: %v, want it to mention %q", what, err, want)
				}
			}
			check("install", checkInstall(in), c.installErr)
			check("uninstall", checkUninstall(in), c.unErr)
		})
	}
}

func TestDetectWindowsError(t *testing.T) {
	if _, err := detect("windows", fakeProber{err: errors.New("boom")}); err == nil {
		t.Fatal("no error")
	}
}

func TestNames(t *testing.T) {
	for goos, want := range map[string]string{"windows": "Woodshed", "linux": "woodshed", "darwin": "com.woodshed.daemon"} {
		if got := Name(goos); got != want {
			t.Errorf("Name(%s) = %q, want %q", goos, got, want)
		}
	}
}

type recLogger struct{ lines []string }

func (r *recLogger) Error(v ...any) error { r.lines = append(r.lines, "E "+v[0].(string)); return nil }
func (r *recLogger) Warning(v ...any) error {
	r.lines = append(r.lines, "W "+v[0].(string))
	return nil
}
func (r *recLogger) Info(v ...any) error               { r.lines = append(r.lines, "I "+v[0].(string)); return nil }
func (r *recLogger) Errorf(f string, v ...any) error   { return nil }
func (r *recLogger) Warningf(f string, v ...any) error { return nil }
func (r *recLogger) Infof(f string, v ...any) error    { return nil }

func TestLoggerHandler(t *testing.T) {
	rec := &recLogger{}
	log := slog.New(newLoggerHandler(rec, slog.LevelInfo)).With("component", "test")
	log.Debug("hidden")
	log.Info("hello", "n", 1)
	log.Warn("careful")
	log.ErrorContext(context.Background(), "broke", "err", "x")
	want := []string{`I msg=hello component=test n=1`, `W msg=careful component=test`, `E msg=broke component=test err=x`}
	if strings.Join(rec.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(rec.lines, "\n"), strings.Join(want, "\n"))
	}
}
