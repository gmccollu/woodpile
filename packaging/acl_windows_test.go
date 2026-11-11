//go:build windows

package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func sddlOf(t *testing.T, path string, info windows.SECURITY_INFORMATION) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return sd.String()
}

// TestResetDataACL needs an elevated administrator (the GitHub Windows runners are).
func TestResetDataACL(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated administrator")
	}
	base := t.TempDir()
	data := filepath.Join(base, "Woodpile")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(data, "logs", "10.0.0.1"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	logFile := filepath.Join(data, "logs", "10.0.0.1", "2026-11-11.log")
	os.WriteFile(logFile, []byte("x\n"), 0o644)
	secret := filepath.Join(outside, "secret.txt")
	os.WriteFile(secret, []byte("s\n"), 0o644)
	// An explicit grant that must not survive, a junction and a hard link that must go.
	if out, err := exec.Command("icacls", logFile, "/grant", "*S-1-1-0:F").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v %s", err, out)
	}
	junction := filepath.Join(data, "logs", "evil")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	hard := filepath.Join(data, "woodpile.toml")
	if err := os.Link(secret, hard); err != nil {
		t.Fatal(err)
	}
	outsideBefore := sddlOf(t, outside, windows.DACL_SECURITY_INFORMATION)
	secretBefore := sddlOf(t, secret, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)

	var removed []string
	if err := ResetDataACL(data, func(f string, a ...any) { removed = append(removed, f) }); err != nil {
		t.Fatal(err)
	}

	var owner, dacl windows.SECURITY_INFORMATION = windows.OWNER_SECURITY_INFORMATION, windows.DACL_SECURITY_INFORMATION
	if got := sddlOf(t, data, owner|dacl); got != strings.Replace(WindowsDataSDDL, "G:SY", "", 1) {
		t.Errorf("data folder: %s, want %s", got, WindowsDataSDDL)
	}
	fileDACL, _ := InheritedDACL(false)
	dirDACL, _ := InheritedDACL(true)
	if got := sddlOf(t, logFile, owner|dacl); got != "O:BA"+fileDACL {
		t.Errorf("log file: %s, want O:BA%s", got, fileDACL)
	}
	if got := sddlOf(t, filepath.Join(data, "logs"), owner|dacl); got != "O:BA"+dirDACL {
		t.Errorf("logs folder: %s, want O:BA%s", got, dirDACL)
	}
	if _, err := os.Lstat(junction); !os.IsNotExist(err) {
		t.Errorf("junction still there: %v", err)
	}
	if _, err := os.Lstat(hard); !os.IsNotExist(err) {
		t.Errorf("hard link still there: %v", err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("the link target was harmed: %v", err)
	}
	if got := sddlOf(t, outside, dacl); got != outsideBefore {
		t.Errorf("junction target's ACL changed: %s -> %s", outsideBefore, got)
	}
	if got := sddlOf(t, secret, dacl|owner); got != secretBefore {
		t.Errorf("hard link target's ACL changed: %s -> %s", secretBefore, got)
	}
	if len(removed) != 2 {
		t.Errorf("removed %d entries, want 2: %v", len(removed), removed)
	}

	// The data folder itself being a link is refused.
	link := filepath.Join(base, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	if err := ResetDataACL(link, nil); err == nil {
		t.Error("a linked data folder was accepted")
	}
}
