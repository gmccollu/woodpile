package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// The TLS syslog listener (SPEC §10.3): off by default, TCP 6514.
const DefaultTLSPort = 6514

// TLSListener is the syslog-over-TLS listener (RFC 5425). Everything after the
// handshake is the TCP listener: the same framing, limits and parsing.
type TLSListener struct {
	Enabled bool   `toml:"enabled" json:"enabled" comment:"Syslog over TLS (Settings → Listeners → \"Syslog over TLS\"). Off by default."`
	Addr    string `toml:"addr" json:"addr"`
	Port    int    `toml:"port" json:"port"`
	// AllowLegacyTLS also accepts TLS 1.0 and 1.1 (with ECDHE CBC suites).
	AllowLegacyTLS bool `toml:"allow_legacy_tls" json:"allow_legacy_tls" comment:"Also accept TLS 1.0 and 1.1, for older senders (a warning shows while it's on)."`
	// CertFile and KeyFile are the user's own certificate chain and key (PEM);
	// both empty means Woodshed's own CA and certificate in the data folder.
	CertFile string `toml:"cert_file" json:"cert_file" comment:"Your own certificate chain and key (PEM files, absolute paths). Leave both empty for Woodshed's own CA and certificate."`
	KeyFile  string `toml:"key_file" json:"key_file"`
}

// Listener returns the address part, for the shared address and port checks.
func (t TLSListener) Listener() Listener {
	return Listener{Enabled: t.Enabled, Addr: t.Addr, Port: t.Port}
}

// UsesOwnCert reports whether the user supplied the certificate.
func (t TLSListener) UsesOwnCert() bool {
	return strings.TrimSpace(t.CertFile) != "" || strings.TrimSpace(t.KeyFile) != ""
}

func checkTLSListener(e Errors, goos string, l Listeners) {
	t := l.TLS
	checkListener(e, goos, "listeners.tls", t.Listener())
	if _, bad := e["listeners.tls.port"]; !bad {
		for _, o := range []struct {
			name string
			ln   Listener
		}{{"TCP", l.TCP}, {"UDP", l.UDP}} {
			if o.ln.Port == t.Port && addrsOverlap(o.ln.Addr, t.Addr) {
				e["listeners.tls.port"] = fmt.Sprintf("can't be the same as the %s listener's port on the same address", o.name)
				break
			}
		}
	}
	checkFilePair(e, "listeners.tls", t.CertFile, t.KeyFile, "Woodshed's own certificate")
	if e["listeners.tls.cert_file"] != "" || e["listeners.tls.key_file"] != "" || !t.UsesOwnCert() {
		return
	}
	if certMsg, keyMsg := CheckTLSPair(t.CertFile, t.KeyFile); certMsg != "" || keyMsg != "" {
		if certMsg != "" {
			e["listeners.tls.cert_file"] = certMsg
		}
		if keyMsg != "" {
			e["listeners.tls.key_file"] = keyMsg
		}
	}
}

// addrsOverlap reports whether two listen addresses can take the same port: the
// same address, or either is a wildcard.
func addrsOverlap(a, b string) bool {
	x, err1 := netip.ParseAddr(strings.TrimSpace(a))
	y, err2 := netip.ParseAddr(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return x.IsUnspecified() || y.IsUnspecified() || x.Unmap() == y.Unmap()
}

// CheckTLSPair loads a user's TLS syslog certificate and key (SPEC §10.3.3) and
// says what's wrong with each, or "" for both when the pair is usable: the key
// matches the certificate, the certificate has EKU serverAuth (or no EKU), and
// the key is one the allowed suites can use (RSA of at least 2048 bits, or
// ECDSA P-256, P-384 or P-521). The messages never quote file content.
func CheckTLSPair(certFile, keyFile string) (certMsg, keyMsg string) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "does not match public key"):
			return "doesn't match key_file", "doesn't match cert_file"
		case strings.Contains(msg, "failed to find any PEM data in certificate") ||
			strings.Contains(msg, "failed to find certificate PEM data"):
			return "has no PEM certificate in it", ""
		case strings.Contains(msg, "failed to find any PEM data in key") ||
			strings.Contains(msg, "failed to find PEM block with type ending in \"PRIVATE KEY\""):
			return "", "has no PEM private key in it"
		case strings.Contains(msg, "failed to parse private key"):
			return "", "isn't a private key Woodshed can read (PEM PKCS #1, PKCS #8 or EC)"
		}
		return "isn't a certificate and key pair Woodshed can load", "isn't a certificate and key pair Woodshed can load"
	}
	leaf := pair.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return "has a certificate Woodshed can't parse", ""
		}
	}
	if len(leaf.ExtKeyUsage) > 0 && !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) &&
		!slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageAny) {
		certMsg = "must allow server authentication (EKU serverAuth), or have no extended key usage"
	}
	switch k := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			keyMsg = "is an RSA key under 2048 bits; use at least 2048"
		}
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256(), elliptic.P384(), elliptic.P521():
		default:
			keyMsg = "is on an elliptic curve TLS senders don't support; use P-256 or P-384"
		}
	default:
		keyMsg = "must be an RSA or ECDSA key (the allowed TLS 1.2 cipher suites can't use this type)"
	}
	return certMsg, keyMsg
}
