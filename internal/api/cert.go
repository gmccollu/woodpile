package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Self-signed certificate files in the data dir (mode 0600).
const (
	CertFile = "ui-cert.pem"
	KeyFile  = "ui-key.pem"
)

const (
	certValidity = 2 * 365 * 24 * time.Hour
	// certRenewBefore regenerates a certificate this close to expiry.
	certRenewBefore = 30 * 24 * time.Hour
	// certRecheck is how often a running server re-reads the local IPs.
	certRecheck = time.Minute
)

// hostIdentity is what a self-signed certificate must cover.
type hostIdentity struct {
	Names []string     // DNS SANs: the hostname and "localhost"
	IPs   []netip.Addr // IP SANs: every local interface address
}

// localIdentity reads the hostname and every local interface address. It makes
// no network calls (no DNS lookups).
func localIdentity() (hostIdentity, error) {
	var id hostIdentity
	names := []string{"localhost"}
	if h, err := os.Hostname(); err == nil && h != "" {
		h = strings.ToLower(strings.TrimSuffix(h, "."))
		names = append(names, h)
		// A ".local" mDNS name is how other machines often reach this one.
		if !strings.Contains(h, ".") {
			names = append(names, h+".local")
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return id, fmt.Errorf("list local addresses: %w", err)
	}
	ips := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if p, ok := netip.AddrFromSlice(ip); ok {
			ips = append(ips, p.Unmap().WithZone(""))
		}
	}
	slices.Sort(names)
	id.Names = slices.Compact(names)
	slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })
	id.IPs = slices.Compact(ips)
	return id, nil
}

// covers reports whether cert names every hostname and IP in id and is valid for
// longer than certRenewBefore.
func (id hostIdentity) coveredBy(cert *x509.Certificate, now time.Time) bool {
	if now.Before(cert.NotBefore) || now.Add(certRenewBefore).After(cert.NotAfter) {
		return false
	}
	for _, n := range id.Names {
		if !slices.Contains(cert.DNSNames, n) {
			return false
		}
	}
	for _, ip := range id.IPs {
		found := false
		for _, c := range cert.IPAddresses {
			if p, ok := netip.AddrFromSlice(c); ok && p.Unmap() == ip {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// generateCert makes a self-signed ECDSA P-256 server certificate for id, valid
// for certValidity from now.
func generateCert(id hostIdentity, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	cn := "woodpile"
	for _, n := range id.Names {
		if n != "localhost" {
			cn = n
			break
		}
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"Woodpile (self-signed)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              id.Names,
	}
	for _, ip := range id.IPs {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(ip.AsSlice()))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
	return certPEM, keyPEM, nil
}

// certSource supplies the TLS certificate: a user-provided pair, or a
// self-signed one in the data dir that is regenerated when it no longer covers
// the host's names and IPs (checked at most once per certRecheck).
type certSource struct {
	dataDir           string
	userCert, userKey string
	now               func() time.Time
	identity          func() (hostIdentity, error)
	onGenerate        func(reason string)
	mu                sync.Mutex
	cert              *tls.Certificate
	leaf              *x509.Certificate
	checked           time.Time
}

// load returns the current certificate, generating or regenerating the
// self-signed one if needed.
func (c *certSource) load() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userCert != "" || c.userKey != "" {
		if c.cert != nil {
			return c.cert, nil
		}
		if c.userCert == "" || c.userKey == "" {
			return nil, errors.New("a user certificate needs both a certificate and a key file")
		}
		pair, err := tls.LoadX509KeyPair(c.userCert, c.userKey)
		if err != nil {
			return nil, fmt.Errorf("load the UI certificate: %w", err)
		}
		c.cert = &pair
		return c.cert, nil
	}
	now := c.now()
	if c.cert != nil && now.Sub(c.checked) < certRecheck {
		return c.cert, nil
	}
	id, err := c.identity()
	if err != nil {
		if c.cert != nil {
			return c.cert, nil // keep serving the old one
		}
		return nil, err
	}
	c.checked = now
	if c.cert == nil {
		if pair, leaf, err := readCertPair(c.dataDir); err == nil {
			c.cert, c.leaf = pair, leaf
		}
	}
	if c.cert != nil && id.coveredBy(c.leaf, now) {
		return c.cert, nil
	}
	reason := "first use"
	if c.cert != nil {
		reason = "the host's names or addresses changed, or it was near expiry"
	}
	certPEM, keyPEM, err := generateCert(id, now)
	if err != nil {
		return nil, fmt.Errorf("generate the UI certificate: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(c.dataDir, KeyFile), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("save the UI certificate key: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(c.dataDir, CertFile), certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("save the UI certificate: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	c.cert, c.leaf = &pair, pair.Leaf
	if c.onGenerate != nil {
		c.onGenerate(reason)
	}
	return c.cert, nil
}

func readCertPair(dataDir string) (*tls.Certificate, *x509.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(dataDir, CertFile), filepath.Join(dataDir, KeyFile))
	if err != nil {
		return nil, nil, err
	}
	if pair.Leaf == nil {
		return nil, nil, errors.New("no leaf certificate")
	}
	return &pair, pair.Leaf, nil
}

// tlsConfig returns a TLS config that asks the source for the certificate on
// each handshake (cheap: it's cached).
func (c *certSource) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return c.load()
		},
	}
}
