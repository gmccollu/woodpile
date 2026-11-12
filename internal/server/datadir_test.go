package server

import (
	"strings"
	"testing"
)

// Re-test N2: a data folder Users can open is fixed when this user owns it,
// refused under the service, and otherwise flagged with a banner.
func TestCheckSharedDataDir(t *testing.T) {
	origS, origO, origP := sharedAccess, ownedByCurrentUser, protectDir
	defer func() { sharedAccess, ownedByCurrentUser, protectDir = origS, origO, origP }()
	var shared []string
	owned, protected := false, 0
	sharedAccess = func(string) ([]string, error) { return shared, nil }
	ownedByCurrentUser = func(string) (bool, error) { return owned, nil }
	protectDir = func(string) error { protected++; shared = nil; return nil }

	reset := func(o bool) { shared, owned, protected = []string{"Users"}, o, 0 }

	reset(false)
	if b, err := checkSharedDataDir(`C:\ProgramData\Woodshed`, false, Options{Service: true}, quiet); err == nil || b != nil ||
		!strings.Contains(err.Error(), "--allow-shared-data-dir") {
		t.Errorf("service: %v %v", b, err)
	}
	if protected != 0 {
		t.Error("the service changed the ACL itself")
	}
	reset(false)
	if b, err := checkSharedDataDir(`C:\ProgramData\Woodshed`, false, Options{Service: true, AllowSharedDataDir: true}, quiet); err != nil ||
		b == nil || b.Kind != "shared_data_dir" || !strings.Contains(b.Text, "Users") {
		t.Errorf("service, allowed: %v %v", b, err)
	}
	reset(true)
	if b, err := checkSharedDataDir(`C:\x`, false, Options{}, quiet); err != nil || b != nil || protected != 1 {
		t.Errorf("run, owned: fixed=%d %v %v", protected, b, err)
	}
	reset(false)
	if b, err := checkSharedDataDir(`C:\x`, true, Options{}, quiet); err != nil || b != nil || protected != 1 {
		t.Errorf("run, created here: fixed=%d %v %v", protected, b, err)
	}
	reset(false)
	if b, err := checkSharedDataDir(`C:\x`, false, Options{}, quiet); err != nil || b == nil || protected != 0 {
		t.Errorf("run, someone else's folder: %v %v (fixed %d)", b, err, protected)
	}
	shared = nil
	if b, err := checkSharedDataDir(`C:\x`, false, Options{Service: true}, quiet); err != nil || b != nil {
		t.Errorf("a private folder: %v %v", b, err)
	}
}
