package service

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectLegacy(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	noEnv := env(nil)
	cases := []struct {
		name    string
		goos    string
		p       fakeProber
		getenv  func(string) string
		service string // "" means none
		remove  string // substring of the removal command
		data    []string
	}{
		{name: "linux nothing", goos: "linux", p: fakeProber{}},
		{name: "linux new install only", goos: "linux",
			p: fakeProber{files: files("/usr/lib/systemd/system/woodshed.service", "/var/lib/woodshed", "/etc/woodshed")}},
		{name: "linux old deb", goos: "linux",
			p:       fakeProber{files: files("/usr/lib/systemd/system/woodpile.service", "/var/lib/dpkg/info/woodpile.list", "/var/lib/woodpile", "/etc/woodpile")},
			service: "/usr/lib/systemd/system/woodpile.service", remove: "sudo apt remove woodpile",
			data: []string{"/var/lib/woodpile", "/etc/woodpile"}},
		{name: "linux old rpm", goos: "linux",
			p:       fakeProber{files: files("/usr/lib/systemd/system/woodpile.service", "/var/lib/woodpile")},
			service: "/usr/lib/systemd/system/woodpile.service", remove: "sudo dnf remove woodpile",
			data: []string{"/var/lib/woodpile"}},
		{name: "linux old package, /lib", goos: "linux",
			p:       fakeProber{files: files("/lib/systemd/system/woodpile.service", "/var/lib/dpkg/info/woodpile.list")},
			service: "/lib/systemd/system/woodpile.service", remove: "apt remove woodpile"},
		{name: "linux old `woodpile install`", goos: "linux",
			p:       fakeProber{files: files("/etc/systemd/system/woodpile.service", "/usr/local/bin/woodpile", "/var/lib/woodpile")},
			service: "/etc/systemd/system/woodpile.service", remove: "sudo /usr/local/bin/woodpile uninstall",
			data: []string{"/var/lib/woodpile"}},
		{name: "linux old manual unit, binary gone", goos: "linux",
			p:       fakeProber{files: files("/etc/systemd/system/woodpile.service")},
			service: "/etc/systemd/system/woodpile.service", remove: "systemctl disable --now woodpile.service"},
		{name: "linux data only (package removed)", goos: "linux",
			p:    fakeProber{files: files("/etc/woodpile")},
			data: []string{"/etc/woodpile"}},
		{name: "darwin nothing", goos: "darwin", p: fakeProber{}},
		{name: "darwin old pkg", goos: "darwin",
			p:       fakeProber{files: files(legacyDarwinPlist, "/usr/local/share/woodpile/uninstall.sh", "/usr/local/bin/woodpile", "/Library/Application Support/Woodpile")},
			service: legacyDarwinPlist, remove: "sudo /usr/local/share/woodpile/uninstall.sh",
			data: []string{"/Library/Application Support/Woodpile"}},
		{name: "darwin old `woodpile install`", goos: "darwin",
			p:       fakeProber{files: files(legacyDarwinPlist, "/usr/local/bin/woodpile")},
			service: legacyDarwinPlist, remove: "sudo /usr/local/bin/woodpile uninstall"},
		{name: "darwin old plist alone", goos: "darwin",
			p:       fakeProber{files: files(legacyDarwinPlist)},
			service: legacyDarwinPlist, remove: "launchctl bootout system/com.woodpile.daemon"},
		{name: "darwin data only (the pkg preinstall removed the rest)", goos: "darwin",
			p:    fakeProber{files: files("/Library/Application Support/Woodpile", "/Library/Application Support/Woodshed")},
			data: []string{"/Library/Application Support/Woodpile"}},
		{name: "windows nothing", goos: "windows", p: fakeProber{}, getenv: noEnv},
		{name: "windows new MSI only", goos: "windows", p: fakeProber{svc: true, msi: true}, getenv: noEnv},
		{name: "windows old MSI", goos: "windows",
			p:       fakeProber{oldSvc: true, oldMSI: true, files: files(`D:\PD\Woodpile`)},
			getenv:  env(map[string]string{"ProgramData": `D:\PD\`}),
			service: `service "Woodpile"`, remove: "Settings > Apps > Woodpile > Uninstall",
			data: []string{`D:\PD\Woodpile`}},
		{name: "windows old MSI key without its service", goos: "windows",
			p: fakeProber{oldMSI: true}, getenv: noEnv,
			service: `HKLM\Software\Woodpile`, remove: "Settings > Apps > Woodpile"},
		{name: "windows old `woodpile install`", goos: "windows",
			p:       fakeProber{oldSvc: true, files: files(`C:\Program Files\Woodpile\woodpile.exe`, `C:\ProgramData\Woodpile`)},
			getenv:  noEnv,
			service: `service "Woodpile"`, remove: `"C:\Program Files\Woodpile\woodpile.exe" uninstall`,
			data: []string{`C:\ProgramData\Woodpile`}},
		{name: "windows old manual service, binary gone", goos: "windows",
			p: fakeProber{oldSvc: true}, getenv: env(map[string]string{"ProgramFiles": `E:\Apps`}),
			service: `service "Woodpile"`, remove: "sc.exe delete Woodpile"},
		{name: "windows old manual service, ProgramFiles elsewhere", goos: "windows",
			p:       fakeProber{oldSvc: true, files: files(`E:\Apps\Woodpile\woodpile.exe`)},
			getenv:  env(map[string]string{"ProgramFiles": `E:\Apps`}),
			service: `service "Woodpile"`, remove: `"E:\Apps\Woodpile\woodpile.exe" uninstall`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getenv := c.getenv
			if getenv == nil {
				getenv = func(string) string { t.Fatal("getenv called off Windows"); return "" }
			}
			l, err := detectLegacy(c.goos, c.p, getenv)
			if err != nil {
				t.Fatal(err)
			}
			if l.Service != c.service {
				t.Errorf("Service = %q, want %q", l.Service, c.service)
			}
			if !strings.Contains(l.Remove, c.remove) || (c.service == "") != (l.Remove == "") {
				t.Errorf("Remove = %q, want it to contain %q", l.Remove, c.remove)
			}
			if strings.Join(l.Data, "|") != strings.Join(c.data, "|") {
				t.Errorf("Data = %q, want %q", l.Data, c.data)
			}
			err = checkLegacy(l)
			switch {
			case c.service == "" && err != nil:
				t.Errorf("install refused: %v", err)
			case c.service != "" && err == nil:
				t.Error("install allowed beside the old Woodpile service")
			case c.service != "" && !strings.Contains(err.Error(), l.Remove):
				t.Errorf("the refusal doesn't give the command: %v", err)
			}
			if (l.ServiceNote() == "") != (c.service == "") || (c.service != "" && !strings.Contains(l.ServiceNote(), l.Remove)) {
				t.Errorf("ServiceNote = %q", l.ServiceNote())
			}
			note := l.DataNote()
			if (note == "") != (len(c.data) == 0) {
				t.Errorf("DataNote = %q", note)
			}
			for _, d := range c.data {
				if !strings.Contains(note, d) || !strings.Contains(note, "delete") {
					t.Errorf("DataNote = %q, want %s and that it can be deleted", note, d)
				}
			}
		})
	}
}

func TestDetectLegacyWindowsError(t *testing.T) {
	if _, err := detectLegacy("windows", fakeProber{err: errors.New("boom")}, func(string) string { return "" }); err == nil {
		t.Fatal("no error")
	}
}
