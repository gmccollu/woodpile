package config

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePair writes a self-signed certificate for key (with the given EKUs) and
// the key, in dir, and returns their paths.
func writePair(t *testing.T, dir, name string, key crypto.Signer, eku []x509.ExtKeyUsage) (certPath, keyPath string) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "logs.example.net"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: eku, DNSNames: []string{"logs.example.net"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, name+"-cert.pem"), filepath.Join(dir, name+"-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestTLSDefaultsOff(t *testing.T) {
	c := Default()
	if c.Listeners.TLS.Enabled || c.Listeners.TLS.Port != 6514 || c.Listeners.TLS.Addr != DefaultListenAddr ||
		c.Listeners.TLS.AllowLegacyTLS || c.Listeners.TLS.UsesOwnCert() {
		t.Errorf("TLS defaults: %+v", c.Listeners.TLS)
	}
	// A 1.0 file without [listeners.tls] gets the defaults.
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte("[listeners.udp]\nport = 5514\n"), 0o600)
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listeners.TLS != c.Listeners.TLS {
		t.Errorf("loaded %+v", got.Listeners.TLS)
	}
}

func TestTLSValidation(t *testing.T) {
	dir := t.TempDir()
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	smallKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	goodCert, goodKey := writePair(t, dir, "good", rsaKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	ecCert, ecKeyPath := writePair(t, dir, "ec", ecKey, nil)
	clientCert, clientKey := writePair(t, dir, "client", rsaKey, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	smallCert, smallKeyPath := writePair(t, dir, "small", smallKey, nil)
	edCert, edKeyPath := writePair(t, dir, "ed", edKey, nil)

	base := Default()
	base.Storage.DataDir = filepath.Join(dir, "data")
	cases := []struct {
		name string
		edit func(*TLSListener, *Config)
		want map[string]string // key -> substring; nil means valid
	}{
		{"default", func(*TLSListener, *Config) {}, nil},
		{"enabled", func(l *TLSListener, _ *Config) { l.Enabled = true }, nil},
		{"own RSA", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = goodCert, goodKey }, nil},
		{"own ECDSA, no EKU", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = ecCert, ecKeyPath }, nil},
		{"port range", func(l *TLSListener, _ *Config) { l.Port = 0 }, map[string]string{"listeners.tls.port": "between 1 and 65535"}},
		{"bad addr", func(l *TLSListener, _ *Config) { l.Addr = "nope" }, map[string]string{"listeners.tls.addr": "IP address"}},
		{"same as TCP", func(l *TLSListener, _ *Config) { l.Port = 514 }, map[string]string{"listeners.tls.port": "TCP"}},
		{"TCP elsewhere", func(l *TLSListener, c *Config) {
			l.Port, l.Addr = 1514, "127.0.0.1"
			c.Listeners.TCP = Listener{Enabled: true, Addr: "127.0.0.2", Port: 1514}
			c.Listeners.UDP = Listener{Enabled: true, Addr: "127.0.0.2", Port: 1514}
		}, nil},
		{"same as UI", func(_ *TLSListener, c *Config) { c.UI.Port = 6514 }, map[string]string{"ui.port": "syslog listener"}},
		{"only cert", func(l *TLSListener, _ *Config) { l.CertFile = goodCert }, map[string]string{"listeners.tls.key_file": "is required"}},
		{"relative", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = "cert.pem", goodKey },
			map[string]string{"listeners.tls.cert_file": "absolute"}},
		{"mismatch", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = goodCert, ecKeyPath },
			map[string]string{"listeners.tls.cert_file": "doesn't match", "listeners.tls.key_file": "doesn't match"}},
		{"client EKU", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = clientCert, clientKey },
			map[string]string{"listeners.tls.cert_file": "serverAuth"}},
		{"small RSA", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = smallCert, smallKeyPath },
			map[string]string{"listeners.tls.key_file": "2048"}},
		{"Ed25519", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = edCert, edKeyPath },
			map[string]string{"listeners.tls.key_file": "RSA or ECDSA"}},
		{"not PEM", func(l *TLSListener, _ *Config) { l.CertFile, l.KeyFile = goodKey, goodKey },
			map[string]string{"listeners.tls.cert_file": "certificate"}},
	}
	for _, tc := range cases {
		c := base.Clone()
		tc.edit(&c.Listeners.TLS, &c)
		errs := c.validate("linux", nil)
		if tc.want == nil {
			if len(errs) != 0 {
				t.Errorf("%s: unexpected errors %v", tc.name, errs)
			}
			continue
		}
		for k, sub := range tc.want {
			if !strings.Contains(errs[k], sub) {
				t.Errorf("%s: %s = %q, want it to mention %q (all: %v)", tc.name, k, errs[k], sub, errs)
			}
		}
		for k := range errs {
			if _, ok := tc.want[k]; !ok {
				t.Errorf("%s: unexpected error on %s: %s", tc.name, k, errs[k])
			}
		}
	}
}
