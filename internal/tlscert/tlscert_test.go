package tlscert

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testID() Identity {
	return Identity{
		Host: "logs-01", Primary: "logs-01.example.net",
		Names: []string{"localhost", "logs-01", "logs-01.example.net", "logs-01.local"},
		IPs:   []netip.Addr{netip.MustParseAddr("10.1.0.50"), netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()},
	}
}

func newTest(t *testing.T, at time.Time) (*Manager, *clock, *Identity, string) {
	t.Helper()
	dir := t.TempDir()
	c := &clock{t: at}
	id := testID()
	m := New(Options{DataDir: dir, Now: c.now, Identity: func() (Identity, error) { return id, nil }})
	return m, c, &id, dir
}

func readAll(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, f := range []string{CAFile, CAKeyFile, CertFile, KeyFile} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		out[f] = b
	}
	return out
}

func parseChain(t *testing.T, b []byte) []*x509.Certificate {
	t.Helper()
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return out
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
}

// Nothing exists until TLS is turned on (Load with create).
func TestNoFilesUntilCreate(t *testing.T) {
	m, _, _, dir := newTest(t, time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC))
	if _, err := m.Load(false); !errors.Is(err, ErrNone) {
		t.Fatalf("Load(false) = %v, want ErrNone", err)
	}
	st := m.Status()
	if st.CA != nil || st.Cert != nil || st.Err != "" || st.Source != SourceWoodshed {
		t.Errorf("status before: %+v", st)
	}
	if _, err := m.CADER(); !errors.Is(err, ErrNone) {
		t.Errorf("CADER before: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Errorf("files before TLS was turned on: %v", ents)
	}
}

// The CA and leaf fields exactly as in SPEC §10.3.3.
func TestCertificateProfile(t *testing.T) {
	now := time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)
	m, _, id, dir := newTest(t, now)
	created, err := m.Load(true)
	if err != nil || !created {
		t.Fatalf("Load(true) = %v, %v", created, err)
	}
	files := readAll(t, dir)
	if runtime.GOOS != "windows" {
		for f := range files {
			fi, _ := os.Stat(filepath.Join(dir, f))
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v", f, fi.Mode().Perm())
			}
		}
	}
	ca := parseChain(t, files[CAFile])[0]
	chain := parseChain(t, files[CertFile])
	if len(chain) != 2 || !bytes.Equal(chain[1].Raw, ca.Raw) {
		t.Fatalf("chain: %d certificates, CA second: %v", len(chain), len(chain) == 2 && bytes.Equal(chain[1].Raw, ca.Raw))
	}
	leaf := chain[0]

	// CA.
	if ca.Subject.CommonName != "Woodshed CA logs-01 2026-11-13" || !ca.IsCA || !ca.BasicConstraintsValid ||
		ca.MaxPathLen != 0 || !ca.MaxPathLenZero || ca.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign ||
		!ca.NotAfter.Equal(Cap) || ca.SignatureAlgorithm != x509.SHA256WithRSA {
		t.Errorf("CA: cn %q ca %v pathlen %d/%v ku %v not_after %v sig %v", ca.Subject.CommonName, ca.IsCA, ca.MaxPathLen,
			ca.MaxPathLenZero, ca.KeyUsage, ca.NotAfter, ca.SignatureAlgorithm)
	}
	if k, ok := ca.PublicKey.(*rsa.PublicKey); !ok || k.N.BitLen() != 2048 {
		t.Errorf("CA key %T", ca.PublicKey)
	}
	// Leaf.
	if leaf.IsCA || !leaf.BasicConstraintsValid || leaf.KeyUsage != x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment ||
		!slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) || leaf.SignatureAlgorithm != x509.SHA256WithRSA {
		t.Errorf("leaf: ca %v ku %v eku %v sig %v", leaf.IsCA, leaf.KeyUsage, leaf.ExtKeyUsage, leaf.SignatureAlgorithm)
	}
	if k, ok := leaf.PublicKey.(*rsa.PublicKey); !ok || k.N.BitLen() != 2048 {
		t.Errorf("leaf key %T", leaf.PublicKey)
	}
	if leaf.Subject.CommonName != "logs-01.example.net" || !slices.Equal(leaf.DNSNames, id.Names) || len(leaf.IPAddresses) != 3 {
		t.Errorf("leaf names: cn %q dns %v ips %v", leaf.Subject.CommonName, leaf.DNSNames, leaf.IPAddresses)
	}
	if want := now.AddDate(10, 0, 0); !leaf.NotAfter.Equal(want) {
		t.Errorf("leaf not_after %v, want %v (10 years)", leaf.NotAfter, want)
	}
	if !bytes.Equal(leaf.AuthorityKeyId, ca.SubjectKeyId) || leaf.CheckSignatureFrom(ca) != nil {
		t.Error("the leaf isn't signed by the CA")
	}
	// Every SAN name verifies with the CA as the only root; another name doesn't.
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, n := range append(slices.Clone(id.Names), "10.1.0.50") {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: n, Roots: roots, CurrentTime: now}); err != nil {
			t.Errorf("verify %s: %v", n, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "other.example.net", Roots: roots, CurrentTime: now}); err == nil {
		t.Error("a name the leaf doesn't have verified")
	}

	st := m.Status()
	if st.CA == nil || st.Cert == nil || st.CA.SHA1 == "" || len(st.CA.SHA256) != 95 || st.Cert.SHA1 != "" ||
		st.Cert.Issuer != st.CA.Subject || len(st.MissingNames) != 0 {
		t.Errorf("status: %+v ca %+v cert %+v", st, st.CA, st.Cert)
	}
	der, err := m.CADER()
	if err != nil || !bytes.Equal(der, ca.Raw) {
		t.Errorf("CADER: %v", err)
	}
	p, _ := m.CAPEM()
	if !bytes.Equal(p, files[CAFile]) {
		t.Error("CAPEM differs from tls-ca.pem")
	}
}

// With a clock in 2030, the leaf ends at the 2037 cap, not 10 years on.
func TestLeafCappedAt2037(t *testing.T) {
	m, _, _, _ := newTest(t, time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC))
	if _, err := m.Load(true); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if !st.Cert.NotAfter.Equal(Cap) || !st.CA.NotAfter.Equal(Cap) {
		t.Errorf("not_after: leaf %v CA %v, want %v", st.Cert.NotAfter, st.CA.NotAfter, Cap)
	}
	if Cap.Format(time.RFC3339) != "2037-12-31T23:59:59Z" {
		t.Errorf("cap %v", Cap)
	}
}

// Never silent: a clock past the 60-day mark and a renamed host raise the
// banners' conditions and leave every file byte-identical, through reloads (a
// restart) too.
func TestNeverChangedSilently(t *testing.T) {
	m, c, id, dir := newTest(t, time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC))
	if _, err := m.Load(true); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, dir)

	c.t = time.Date(2036, 10, 1, 0, 0, 0, 0, time.UTC) // 43 days before the leaf expires
	id.Names = append(id.Names, "edge-logs.example.net")
	id.Primary = "edge-logs.example.net"
	m2 := New(Options{DataDir: dir, Now: c.now, Identity: func() (Identity, error) { return *id, nil }})
	for i := 0; i < 2; i++ {
		if created, err := m2.Load(true); err != nil || created {
			t.Fatalf("reload %d: created %v, %v", i, created, err)
		}
	}
	st := m2.Status()
	if !slices.Equal(st.MissingNames, []string{"edge-logs.example.net"}) {
		t.Errorf("missing names %v", st.MissingNames)
	}
	if which, at := st.Expiring(c.t); which != "cert" || !at.Equal(st.Cert.NotAfter) {
		t.Errorf("expiring = %q %v", which, at)
	}
	if which, _ := st.Expiring(time.Date(2037, 11, 15, 0, 0, 0, 0, time.UTC)); which != "ca" {
		t.Errorf("expiring near the cap = %q, want ca", which)
	}
	if which, _ := st.Expiring(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)); which != "" {
		t.Errorf("expiring early = %q", which)
	}
	after := readAll(t, dir)
	for f := range before {
		if !bytes.Equal(before[f], after[f]) {
			t.Errorf("%s changed without an explicit action", f)
		}
	}
	// The original manager, asked again, doesn't change anything either.
	_ = m.Status()
	if _, err := m.Load(true); err != nil {
		t.Fatal(err)
	}
	for f, b := range readAll(t, dir) {
		if !bytes.Equal(before[f], b) {
			t.Errorf("%s changed on reload", f)
		}
	}
}

// Renew keeps tls-ca.pem byte-identical and makes a new leaf (new key, current
// names); Replace CA changes both.
func TestRenewAndReplaceCA(t *testing.T) {
	m, c, id, dir := newTest(t, time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC))
	if _, err := m.Load(true); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, dir)
	c.t = c.t.Add(24 * time.Hour)
	id.Names = append(id.Names, "edge-logs.example.net")
	r, err := m.Renew(false)
	if err != nil {
		t.Fatal(err)
	}
	after := readAll(t, dir)
	if !bytes.Equal(before[CAFile], after[CAFile]) || !bytes.Equal(before[CAKeyFile], after[CAKeyFile]) {
		t.Error("renew changed the CA")
	}
	if bytes.Equal(before[CertFile], after[CertFile]) || bytes.Equal(before[KeyFile], after[KeyFile]) {
		t.Error("renew didn't make a new leaf and key")
	}
	if r.NewCA || r.OldCA != r.NewCAHash || r.OldCert == r.NewCertHash || r.OldCert == "" {
		t.Errorf("renewal %+v", r)
	}
	cert, _ := m.Certificate()
	if cert.Leaf == nil || !slices.Contains(cert.Leaf.DNSNames, "edge-logs.example.net") || Fingerprint(cert.Certificate[0]) != r.NewCertHash {
		t.Error("the served certificate isn't the renewed one")
	}
	if st := m.Status(); len(st.MissingNames) != 0 {
		t.Errorf("still missing %v", st.MissingNames)
	}

	r2, err := m.Renew(true)
	if err != nil {
		t.Fatal(err)
	}
	final := readAll(t, dir)
	if bytes.Equal(after[CAFile], final[CAFile]) || r2.OldCA == r2.NewCAHash || !r2.NewCA {
		t.Errorf("replace CA kept the CA: %+v", r2)
	}
	chain := parseChain(t, final[CertFile])
	if !bytes.Equal(chain[1].Raw, parseChain(t, final[CAFile])[0].Raw) || chain[0].CheckSignatureFrom(chain[1]) != nil {
		t.Error("new leaf not signed by the new CA")
	}
}

// A user's own certificate is sent as given and re-read on every Load; no CA
// download, no renew.
func TestUserCertificate(t *testing.T) {
	src, _, _, srcDir := newTest(t, time.Now())
	if _, err := src.Load(true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(srcDir, CertFile), filepath.Join(srcDir, KeyFile)
	m := New(Options{DataDir: dir, CertFile: certPath, KeyFile: keyPath, Identity: func() (Identity, error) { return testID(), nil }})
	if created, err := m.Load(true); err != nil || created {
		t.Fatalf("Load: %v %v", created, err)
	}
	cert, err := m.Certificate()
	if err != nil || len(cert.Certificate) != 2 {
		t.Fatalf("certificate: %v, chain %d", err, len(cert.Certificate))
	}
	st := m.Status()
	if st.Source != SourceFile || st.CA != nil || st.Cert == nil || len(st.MissingNames) != 0 {
		t.Errorf("status %+v", st)
	}
	if _, err := m.CADER(); !errors.Is(err, ErrUserCert) {
		t.Errorf("CADER: %v", err)
	}
	if _, err := m.Renew(false); !errors.Is(err, ErrUserCert) {
		t.Errorf("Renew: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("files written for a user certificate: %v", ents)
	}
	// A replaced file takes effect on the next Load.
	old := Fingerprint(cert.Certificate[0])
	if _, err := src.Renew(false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(false); err != nil {
		t.Fatal(err)
	}
	cert, _ = m.Certificate()
	if Fingerprint(cert.Certificate[0]) == old {
		t.Error("the replaced file wasn't re-read")
	}
	// Switching back to Woodshed's own.
	m.SetFiles("", "")
	if _, err := m.Load(false); !errors.Is(err, ErrNone) {
		t.Errorf("after SetFiles: %v", err)
	}
}

// A missing CA is an error, never a reason to make a new one.
func TestMissingCAIsAnError(t *testing.T) {
	m, _, _, dir := newTest(t, time.Now())
	if _, err := m.Load(true); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, CAFile))
	m2 := New(Options{DataDir: dir, Identity: func() (Identity, error) { return testID(), nil }})
	if _, err := m2.Load(true); err == nil {
		t.Fatal("Load made a new CA")
	}
	if exists(filepath.Join(dir, CAFile)) {
		t.Error("tls-ca.pem was recreated")
	}
	if st := m2.Status(); st.Err == "" {
		t.Error("status has no error")
	}
	// Only the leaf missing: a leaf is made from the same CA.
	m3, _, _, dir3 := newTest(t, time.Now())
	m3.Load(true)
	ca := readAll(t, dir3)[CAFile]
	os.Remove(filepath.Join(dir3, CertFile))
	os.Remove(filepath.Join(dir3, KeyFile))
	m4 := New(Options{DataDir: dir3, Identity: func() (Identity, error) { return testID(), nil }})
	if created, err := m4.Load(true); err != nil || !created {
		t.Fatalf("leaf only: %v %v", created, err)
	}
	if !bytes.Equal(readAll(t, dir3)[CAFile], ca) {
		t.Error("the CA changed")
	}
}
