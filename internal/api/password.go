package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

// PasswordFile is the UI password hash's file name in the data dir. It holds one
// PHC-format Argon2id string, is written with mode 0600, and never goes into
// the TOML config.
const PasswordFile = "ui-password.hash"

// Password length limits, in bytes.
const (
	minPasswordLen = 8
	maxPasswordLen = 1024
)

// argonParams are the Argon2id cost parameters for new hashes (RFC 9106's second
// recommended option). Verification reads the parameters stored in the hash, so
// changing these doesn't break existing passwords. Tests lower them.
type argonParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
	SaltLen int
}

var defaultArgon = argonParams{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16}

// Limits on stored parameters, so a tampered hash file can't make a login
// allocate gigabytes or spin for minutes.
const (
	maxArgonMemory = 1 << 20 // KiB (1 GiB)
	maxArgonTime   = 16
)

// hashPassword returns a PHC string: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>.
func hashPassword(password string, p argonParams) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("the password hash file is not a valid Argon2id hash")

// verifyPassword reports whether password matches the PHC string, in constant
// time with respect to the key.
func verifyPassword(password, phc string) (bool, error) {
	parts := strings.Split(strings.TrimSpace(phc), "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errBadHash
	}
	var m, t uint32
	var th uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return false, errBadHash
	}
	if m == 0 || m > maxArgonMemory || t == 0 || t > maxArgonTime || th == 0 {
		return false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errBadHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, m, th, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// checkNewPassword returns a message for an unacceptable new password, or "".
func checkNewPassword(pw string) string {
	switch {
	case len(pw) < minPasswordLen:
		return fmt.Sprintf("At least %d characters.", minPasswordLen)
	case len(pw) > maxPasswordLen:
		return fmt.Sprintf("At most %d characters.", maxPasswordLen)
	case strings.TrimSpace(pw) == "":
		return "Must not be only spaces."
	}
	return ""
}

// readHash returns the stored hash, or "" if none is set.
func readHash(dataDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, PasswordFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// writeHash atomically replaces the stored hash with mode 0600.
func writeHash(dataDir, phc string) error {
	return writeFileAtomic(filepath.Join(dataDir, PasswordFile), []byte(phc+"\n"), 0o600)
}

// writeFileAtomic writes data to a temp file in path's directory with perm, syncs
// it and renames it over path.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil && !isWindows {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
