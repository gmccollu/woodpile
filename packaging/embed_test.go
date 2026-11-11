package packaging

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

// serviceSID derives a service's SID: S-1-5-80 followed by the SHA-1 of the
// upper-cased name in UTF-16LE, as five little-endian 32-bit numbers.
func serviceSID(name string) string {
	u := utf16.Encode([]rune(strings.ToUpper(name)))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	h := sha1.Sum(b)
	s := "S-1-5-80"
	for i := 0; i < 5; i++ {
		s += fmt.Sprintf("-%d", binary.LittleEndian.Uint32(h[4*i:]))
	}
	return s
}

func TestServiceSID(t *testing.T) {
	// A well-known one checks the derivation itself.
	if got := serviceSID("TrustedInstaller"); got != "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" {
		t.Fatalf("derivation is wrong: %s", got)
	}
	if got := serviceSID("Woodpile"); got != WindowsServiceSID {
		t.Errorf("WindowsServiceSID = %s, derived %s", WindowsServiceSID, got)
	}
}

func TestMSIUsesTheSameACL(t *testing.T) {
	wxs, err := os.ReadFile("windows/woodpile.wxs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wxs), `Sddl="`+WindowsDataSDDL+`"`) {
		t.Error("windows/woodpile.wxs's PermissionEx differs from WindowsDataSDDL; change both together")
	}
}

func TestEmbedded(t *testing.T) {
	for name, s := range map[string]string{"unit": SystemdUnit, "sysctl": SysctlDropIn, "plist": LaunchdPlist,
		"newsyslog": NewsyslogConf, "account script": MacAccountScript} {
		if len(s) < 50 {
			t.Errorf("%s is empty", name)
		}
	}
}
