// Package tlscert is the TLS syslog listener's certificate (SPEC §10.3.3):
// Woodshed's own small CA and a server ("leaf") certificate it signs, kept in
// the data folder, or a user's own certificate and key.
//
// Unlike the UI certificate, nothing here ever changes on its own: the CA and
// the leaf are made when TLS is first turned on, and replaced only by Renew.
// Expiry and a host rename show up in Status (for banners), never as a new
// certificate.
package tlscert

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gmccollu/woodshed/internal/atomicfile"
)

// Files in the data folder, mode 0600 (on Windows they inherit the data
// folder's protected ACL, like the UI certificate).
const (
	CAFile    = "tls-ca.pem"
	CAKeyFile = "tls-ca-key.pem"
	CertFile  = "tls-cert.pem" // the leaf, then the CA: the chain that's sent
	KeyFile   = "tls-key.pem"
)

// Sources of the certificate (GET /api/tls source).
const (
	SourceWoodshed = "woodshed"
	SourceFile     = "file"
)

// Cap is the latest expiry of both certificates: some 32-bit device stacks can't
// represent dates after 2038-01-19 (SPEC §10.3.3, §10.4 TLS answer 1).
var Cap = time.Date(2037, 12, 31, 23, 59, 59, 0, time.UTC)

const (
	// leafYears is the leaf's life, unless the cap is sooner.
	leafYears = 10
	// ExpiryWarning is how long before the leaf or the CA expires the
	// tls_cert_expiring banner shows.
	ExpiryWarning = 60 * 24 * time.Hour
	// keyBits: RSA-2048 with SHA-256 for both (§10.4 TLS 1).
	keyBits = 2048
	// identityRecheck is how often Status re-reads the host's names.
	identityRecheck = time.Minute
)

// ErrNone means there's no certificate yet (TLS was never turned on).
var ErrNone = errors.New("no TLS certificate yet")

// ErrUserCert is Renew's answer with a user's own certificate.
var ErrUserCert = errors.New("this server uses your own certificate; replace the files instead")

// Identity is what the leaf names: the UI certificate's identity (the
// hostname, its .local and search-domain names, localhost, ui.extra_hostnames;
// stable addresses), plus the primary DNS name for the CN.
type Identity struct {
	Host    string       // the lower-cased hostname, "" if unknown
	Primary string       // the CN: the first search-domain name, else the hostname
	Names   []string     // DNS SANs
	IPs     []netip.Addr // IP SANs
}

// Info describes one certificate (GET /api/tls ca and cert).
type Info struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer,omitempty"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	SHA256      string    `json:"sha256"`
	SHA1        string    `json:"sha1,omitempty"`
	DNSNames    []string  `json:"dns_names,omitempty"`
	IPAddresses []string  `json:"ip_addresses,omitempty"`
}

// Status is the certificate state.
type Status struct {
	Source string // SourceWoodshed or SourceFile
	CA     *Info  // nil with SourceFile, or before the CA exists
	Cert   *Info  // nil before the certificate exists
	// MissingNames are host DNS names the leaf doesn't cover (always empty with
	// SourceFile: those names are whoever issued it's to choose).
	MissingNames []string
	// Err is why the certificate can't be loaded ("" when it's fine, and when
	// there's simply none yet).
	Err string
}

// Options configures a Manager.
type Options struct {
	DataDir string
	// CertFile and KeyFile are a user's own pair (listeners.tls.cert_file and
	// key_file); both empty means Woodshed's own CA.
	CertFile, KeyFile string
	Now               func() time.Time         // default time.Now
	Identity          func() (Identity, error) // the host's names now (required to generate)
}

// Manager holds the certificate in use. Safe for concurrent use.
type Manager struct {
	dir      string
	now      func() time.Time
	identity func() (Identity, error)

	mu        sync.Mutex
	userCert  string
	userKey   string
	cert      *tls.Certificate
	leaf      *x509.Certificate
	ca        *x509.Certificate
	loadErr   error
	id        Identity
	idChecked time.Time
}

// New returns a Manager; it reads nothing until Load.
func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{dir: o.DataDir, now: o.Now, identity: o.Identity, userCert: o.CertFile, userKey: o.KeyFile}
}

// Recheck makes the next Status read the host's names again (after a Settings
// save changed ui.extra_hostnames, say).
func (m *Manager) Recheck() {
	m.mu.Lock()
	m.idChecked = time.Time{}
	m.mu.Unlock()
}

// SetFiles switches between a user's own pair and Woodshed's CA (a Settings
// save); call Load after.
func (m *Manager) SetFiles(certFile, keyFile string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if certFile != m.userCert || keyFile != m.userKey {
		m.userCert, m.userKey = certFile, keyFile
		m.cert, m.leaf, m.ca, m.loadErr = nil, nil, nil, nil
	}
}

func (m *Manager) source() string {
	if m.userCert != "" || m.userKey != "" {
		return SourceFile
	}
	return SourceWoodshed
}

// Load reads the certificate in use: the user's files (re-read every time, so a
// replaced file takes effect), or Woodshed's own from the data folder. With
// create, Woodshed's CA and leaf are made if neither exists yet (TLS being
// turned on), and a leaf alone is made if only the CA exists; created says
// whether anything was. An existing CA is never replaced here, and a missing or
// broken CA is an error, not a reason to make a new one: devices trust it.
func (m *Manager) Load(create bool) (created bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	created, err = m.loadLocked(create)
	m.loadErr = err
	return created, err
}

func (m *Manager) loadLocked(create bool) (bool, error) {
	if m.source() == SourceFile {
		if m.userCert == "" || m.userKey == "" {
			return false, errors.New("listeners.tls.cert_file and key_file must both be set")
		}
		pair, err := tls.LoadX509KeyPair(m.userCert, m.userKey)
		if err != nil {
			return false, fmt.Errorf("load the TLS certificate: %w", err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return false, fmt.Errorf("parse the TLS certificate: %w", err)
		}
		pair.Leaf = leaf
		m.cert, m.leaf, m.ca = &pair, leaf, nil
		return false, nil
	}

	ca, caErr := m.readCA()
	switch {
	case errors.Is(caErr, fs.ErrNotExist) && !exists(filepath.Join(m.dir, CertFile)) && !exists(filepath.Join(m.dir, KeyFile)):
		if !create {
			m.cert, m.leaf, m.ca = nil, nil, nil
			return false, ErrNone
		}
		if err := m.generate(true); err != nil {
			return false, err
		}
		return true, nil
	case caErr != nil:
		return false, fmt.Errorf("the TLS CA in %s can't be read (%v); fix the file, or replace the CA (devices must then import the new one)", m.dir, caErr)
	}
	pair, leaf, err := m.readLeaf()
	if errors.Is(err, fs.ErrNotExist) && create {
		// The CA is there but the leaf isn't: make the leaf. Devices that trust
		// the CA notice nothing.
		if err := m.generate(false); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("the TLS certificate in %s can't be read (%v); renew it", m.dir, err)
	}
	m.cert, m.leaf, m.ca = pair, leaf, ca
	return false, nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (m *Manager) readCA() (*x509.Certificate, error) {
	b, err := atomicfile.ReadFile(filepath.Join(m.dir, CAFile))
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New(CAFile + " holds no PEM certificate")
	}
	return x509.ParseCertificate(blk.Bytes)
}

func (m *Manager) readLeaf() (*tls.Certificate, *x509.Certificate, error) {
	certPEM, err := atomicfile.ReadFile(filepath.Join(m.dir, CertFile))
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := atomicfile.ReadFile(filepath.Join(m.dir, KeyFile))
	if err != nil {
		return nil, nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	pair.Leaf = leaf
	return &pair, leaf, nil
}

func (m *Manager) readCAKey() (*rsa.PrivateKey, error) {
	b, err := atomicfile.ReadFile(filepath.Join(m.dir, CAKeyFile))
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New(CAKeyFile + " holds no PEM key")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New(CAKeyFile + " isn't an RSA key")
	}
	return rk, nil
}

// Certificate is the certificate to serve (the listener asks on each
// handshake), or ErrNone / the load error.
func (m *Manager) Certificate() (*tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cert != nil {
		return m.cert, nil
	}
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return nil, ErrNone
}

// Renewal is what Renew replaced, for the event log.
type Renewal struct {
	NewCA                bool
	OldCA, NewCAHash     string // SHA-256 fingerprints ("" when there was none)
	OldCert, NewCertHash string
}

// Renew makes a new leaf with a new key and the current host names, signed by the
// same CA; with newCA, a new CA too (every device must import it). New
// connections get the new leaf at once; open ones keep theirs. ErrUserCert with
// a user's own certificate.
func (m *Manager) Renew(newCA bool) (Renewal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source() == SourceFile {
		return Renewal{}, ErrUserCert
	}
	r := Renewal{NewCA: newCA}
	if ca, err := m.readCA(); err == nil {
		r.OldCA = Fingerprint(ca.Raw)
	} else if !newCA {
		return r, fmt.Errorf("the TLS CA can't be read (%v): replace the CA instead", err)
	}
	if _, leaf, err := m.readLeaf(); err == nil {
		r.OldCert = Fingerprint(leaf.Raw)
	}
	m.idChecked = time.Time{} // the names are read afresh
	if err := m.generate(newCA); err != nil {
		m.loadErr = err
		return r, err
	}
	m.loadErr = nil
	r.NewCAHash, r.NewCertHash = Fingerprint(m.ca.Raw), Fingerprint(m.leaf.Raw)
	return r, nil
}

// generate makes a leaf (and with newCA a CA first), writes the files and makes
// them the ones in use (mu held).
func (m *Manager) generate(newCA bool) error {
	if m.identity == nil {
		return errors.New("tlscert: no identity source")
	}
	id, err := m.identity()
	if err != nil {
		return fmt.Errorf("read the host's names: %w", err)
	}
	now := m.now().UTC()
	if !now.Before(Cap) {
		return fmt.Errorf("the clock says %s, past the certificates' %s limit", now.Format(time.RFC3339), Cap.Format(time.RFC3339))
	}
	var ca *x509.Certificate
	var caKey *rsa.PrivateKey
	if newCA {
		caKey, err = rsa.GenerateKey(rand.Reader, keyBits)
		if err != nil {
			return err
		}
		host := id.Host
		if host == "" {
			host = "woodshed"
		}
		tmpl := &x509.Certificate{
			SerialNumber:          serial(),
			Subject:               pkix.Name{CommonName: fmt.Sprintf("Woodshed CA %s %s", host, now.Format("2006-01-02"))},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              Cap,
			IsCA:                  true,
			BasicConstraintsValid: true,
			MaxPathLen:            0,
			MaxPathLenZero:        true,
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			SignatureAlgorithm:    x509.SHA256WithRSA,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
		if err != nil {
			return fmt.Errorf("create the TLS CA: %w", err)
		}
		if ca, err = x509.ParseCertificate(der); err != nil {
			return err
		}
	} else {
		if ca, err = m.readCA(); err != nil {
			return fmt.Errorf("read the TLS CA: %w", err)
		}
		if caKey, err = m.readCAKey(); err != nil {
			return fmt.Errorf("read the TLS CA key: %w", err)
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return err
	}
	notAfter := now.AddDate(leafYears, 0, 0)
	if notAfter.After(Cap) {
		notAfter = Cap
	}
	if notAfter.After(ca.NotAfter) {
		notAfter = ca.NotAfter
	}
	cn := id.Primary
	if cn == "" {
		cn = "localhost"
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              slices.Clone(id.Names),
		SignatureAlgorithm:    x509.SHA256WithRSA,
	}
	for _, ip := range id.IPs {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(ip.AsSlice()))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create the TLS certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), caPEM...)
	keyPEM, err := encodeKey(key)
	if err != nil {
		return err
	}
	// The CA first (with its key), then the leaf's key, then the chain: an
	// interrupted renewal leaves files that don't load, never a silent mix.
	if newCA {
		caKeyPEM, err := encodeKey(caKey)
		if err != nil {
			return err
		}
		if err := write(filepath.Join(m.dir, CAKeyFile), caKeyPEM); err != nil {
			return err
		}
		if err := write(filepath.Join(m.dir, CAFile), caPEM); err != nil {
			return err
		}
	}
	if err := write(filepath.Join(m.dir, KeyFile), keyPEM); err != nil {
		return err
	}
	if err := write(filepath.Join(m.dir, CertFile), chain); err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(chain, keyPEM)
	if err != nil {
		return err
	}
	pair.Leaf = leaf
	m.cert, m.leaf, m.ca = &pair, leaf, ca
	return nil
}

func encodeKey(k *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func write(path string, data []byte) error {
	if err := atomicfile.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(path), err)
	}
	return nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand doesn't fail
	}
	return n.Add(n, big.NewInt(1))
}

// CAPEM and CADER are the CA certificate for the downloads: ErrNone before it
// exists, ErrUserCert with a user's own certificate.
func (m *Manager) CAPEM() ([]byte, error) {
	der, err := m.CADER()
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// CADER: see CAPEM.
func (m *Manager) CADER() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.source() == SourceFile {
		return nil, ErrUserCert
	}
	if m.ca == nil {
		ca, err := m.readCA()
		if err != nil {
			return nil, ErrNone
		}
		return bytes.Clone(ca.Raw), nil
	}
	return bytes.Clone(m.ca.Raw), nil
}

// Status describes the certificates in use and the host names the leaf
// misses. It reads the files only if none are loaded (so it shows a certificate
// even when the listener is off), and the host's names at most once a minute.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Source: m.source()}
	if m.cert == nil && m.loadErr == nil {
		if _, err := m.loadLocked(false); err != nil && !errors.Is(err, ErrNone) {
			m.loadErr = err
		}
	}
	if m.loadErr != nil && !errors.Is(m.loadErr, ErrNone) {
		st.Err = m.loadErr.Error()
	}
	if m.ca != nil {
		i := describe(m.ca)
		i.SHA1 = fingerprintSHA1(m.ca.Raw)
		i.Issuer = ""
		st.CA = &i
	}
	if m.leaf != nil {
		i := describe(m.leaf)
		st.Cert = &i
		if st.Source == SourceWoodshed {
			st.MissingNames = m.missingLocked()
		}
	}
	if st.MissingNames == nil {
		st.MissingNames = []string{}
	}
	return st
}

// missingLocked returns the host's DNS names the leaf doesn't name. Addresses
// don't count: the senders that check names check DNS names (§10.4 TLS 6).
func (m *Manager) missingLocked() []string {
	now := m.now()
	if m.identity != nil && (m.idChecked.IsZero() || now.Sub(m.idChecked) >= identityRecheck || now.Before(m.idChecked)) {
		if id, err := m.identity(); err == nil {
			m.id, m.idChecked = id, now
		}
	}
	var out []string
	for _, n := range m.id.Names {
		if !covers(m.leaf, n) {
			out = append(out, n)
		}
	}
	return out
}

// covers reports whether cert names host (exactly, or by a wildcard).
func covers(cert *x509.Certificate, host string) bool {
	return cert.VerifyHostname(host) == nil
}

func describe(c *x509.Certificate) Info {
	i := Info{
		Subject:   c.Subject.String(),
		Issuer:    c.Issuer.String(),
		NotBefore: c.NotBefore.UTC(),
		NotAfter:  c.NotAfter.UTC(),
		SHA256:    Fingerprint(c.Raw),
		DNSNames:  slices.Clone(c.DNSNames),
	}
	for _, ip := range c.IPAddresses {
		if a, ok := netip.AddrFromSlice(ip); ok {
			i.IPAddresses = append(i.IPAddresses, a.Unmap().String())
		}
	}
	if i.DNSNames == nil {
		i.DNSNames = []string{}
	}
	if i.IPAddresses == nil {
		i.IPAddresses = []string{}
	}
	return i
}

// Fingerprint is the SHA-256 of a DER certificate as colon-separated upper-case
// hex pairs, like ui_cert_fingerprint.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return colonHex(sum[:])
}

func fingerprintSHA1(der []byte) string {
	sum := sha1.Sum(der)
	return colonHex(sum[:])
}

func colonHex(b []byte) string {
	var s strings.Builder
	for i, x := range b {
		if i > 0 {
			s.WriteByte(':')
		}
		fmt.Fprintf(&s, "%02X", x)
	}
	return s.String()
}

// Expiring returns which certificate expires within ExpiryWarning of now:
// "ca" (devices need the new CA), "cert" (a renewal fixes it), or "". The CA
// wins when both do: renewing the leaf can't outlive it.
func (st Status) Expiring(now time.Time) (which string, at time.Time) {
	if st.CA != nil && now.Add(ExpiryWarning).After(st.CA.NotAfter) {
		return "ca", st.CA.NotAfter
	}
	if st.Cert != nil && now.Add(ExpiryWarning).After(st.Cert.NotAfter) {
		return "cert", st.Cert.NotAfter
	}
	return "", time.Time{}
}
