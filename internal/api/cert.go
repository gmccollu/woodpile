package api

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gmccollu/woodshed/internal/atomicfile"
	"github.com/gmccollu/woodshed/internal/config"
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
	Host  string       // the lower-cased hostname ("" if unknown), for the UI URL
	Names []string     // DNS SANs: localhost, the hostname and its qualified names
	IPs   []netip.Addr // IP SANs: every stable local interface address
}

// withExtras adds ui.extra_hostnames entries (already validated) to id.
func (id hostIdentity) withExtras(extras []string) hostIdentity {
	if len(extras) == 0 {
		return id
	}
	names, ips := slices.Clone(id.Names), slices.Clone(id.IPs)
	for _, e := range extras {
		if n, ip, ok := config.ParseExtraHostname(e); ok && n != "" {
			names = append(names, n)
		} else if ok {
			ips = append(ips, ip)
		}
	}
	slices.Sort(names)
	id.Names = slices.Compact(names)
	slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })
	id.IPs = slices.Compact(ips)
	return id
}

// localIdentity reads the hostname and the machine's stable interface
// addresses. It makes no network calls (no DNS lookups). See identityFrom for
// which addresses count.
func localIdentity() (hostIdentity, error) {
	host, _ := os.Hostname()
	ifaces, err := net.Interfaces()
	if err != nil {
		return hostIdentity{}, fmt.Errorf("list local interfaces: %w", err)
	}
	var addrs []ifaceAddr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		as, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if p, ok := netip.AddrFromSlice(ip); ok {
				addrs = append(addrs, ifaceAddr{iface: ifc.Name, ip: p.Unmap().WithZone("")})
			}
		}
	}
	return identityFrom(host, searchDomains(), addrs, ipv6Flags()), nil
}

// ifaceAddr is one address of one interface.
type ifaceAddr struct {
	iface string
	ip    netip.Addr
}

// IPv6 address flags (Linux IFA_F_*), from /proc/net/if_inet6.
const (
	ifaTemporary  = 0x01
	ifaDeprecated = 0x20
	ifaTentative  = 0x40
)

// virtualIface reports interfaces whose addresses come and go with containers,
// VMs and VPNs; certifying them would churn the certificate.
func virtualIface(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "utun", "vethernet", "virbr", "vmnet", "vboxnet", "cni", "flannel",
		"podman", "awdl", "llw", "zt", "tailscale", "wg"} {
		if strings.HasPrefix(strings.ToLower(name), p) {
			return true
		}
	}
	return false
}

// identityFrom builds the certificate identity (security review L6: the
// certificate used to be regenerated whenever any address changed):
//   - the hostname, its ".local" mDNS name, the hostname qualified with each DNS
//     search domain (domains; at most maxSearchDomains), and localhost;
//   - loopback, and the IPv4 and IPv6 addresses of real interfaces that are
//     stable: not link-local, not on a virtual or tunnel interface, and for
//     IPv6 not temporary, deprecated or tentative. Where the OS's flags aren't
//     available (flags nil: macOS, Windows), only IPv6 addresses with an EUI-64
//     interface ID count, since privacy and temporary addresses look random.
func identityFrom(host string, domains []string, addrs []ifaceAddr, flags map[netip.Addr]uint32) hostIdentity {
	var id hostIdentity
	names := []string{"localhost"}
	if host != "" {
		h := strings.ToLower(strings.TrimSuffix(host, "."))
		id.Host = h
		names = append(names, h)
		// A ".local" mDNS name is how other machines often reach this one, and a
		// DNS record in the search domain how they reach it by name.
		if !strings.Contains(h, ".") {
			names = append(names, h+".local")
			for i, d := range domains {
				if i == maxSearchDomains {
					break
				}
				if n := h + "." + d; config.ValidDNSName(n) {
					names = append(names, n)
				}
			}
		}
	}
	ips := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}
	for _, a := range addrs {
		ip := a.ip
		switch {
		case ip.IsLoopback(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsMulticast(), ip.IsUnspecified():
			continue
		case virtualIface(a.iface):
			continue
		case ip.Is6():
			if flags != nil {
				f, known := flags[ip]
				if !known || f&(ifaTemporary|ifaDeprecated|ifaTentative) != 0 {
					continue
				}
			} else if b := ip.As16(); b[11] != 0xff || b[12] != 0xfe {
				continue // not EUI-64: possibly a temporary (privacy) address
			}
		}
		ips = append(ips, ip)
	}
	slices.Sort(names)
	id.Names = slices.Compact(names)
	slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })
	id.IPs = slices.Compact(ips)
	return id
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
// for certValidity from now, with a new key.
func generateCert(id hostIdentity, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return generateCertWithKey(id, now, key)
}

// generateCertWithKey is generateCert with a given key (kept across
// regenerations, so a client that pinned the key keeps working).
func generateCertWithKey(id hostIdentity, now time.Time, key *ecdsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	cn := "woodshed"
	for _, n := range id.Names {
		if n != "localhost" {
			cn = n
			break
		}
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"Woodshed (self-signed)"}},
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
	var certPEM, keyPEM []byte
	if key := readKey(c.dataDir); key != nil {
		certPEM, keyPEM, err = generateCertWithKey(id, now, key)
	} else {
		certPEM, keyPEM, err = generateCert(id, now)
	}
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

// recheck makes the next load re-read the identity.
func (c *certSource) recheck() {
	c.mu.Lock()
	c.checked = time.Time{}
	c.mu.Unlock()
}

// readKey returns the self-signed certificate's existing ECDSA key, or nil.
func readKey(dataDir string) *ecdsa.PrivateKey {
	b, err := atomicfile.ReadFile(filepath.Join(dataDir, KeyFile))
	if err != nil {
		return nil
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil
	}
	ek, _ := k.(*ecdsa.PrivateKey)
	return ek
}

// fingerprint is the SHA-256 of the certificate in use, as colon-separated
// upper-case hex (how browsers show it), or "".
func (c *certSource) fingerprint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert == nil || len(c.cert.Certificate) == 0 {
		return ""
	}
	return certFingerprint(c.cert.Certificate[0])
}

func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	var b strings.Builder
	for i, x := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", x)
	}
	return b.String()
}

func readCertPair(dataDir string) (*tls.Certificate, *x509.Certificate, error) {
	// Shared reads (FILE_SHARE_DELETE on Windows), so a regeneration's rename
	// isn't blocked by this read.
	certPEM, err := atomicfile.ReadFile(filepath.Join(dataDir, CertFile))
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := atomicfile.ReadFile(filepath.Join(dataDir, KeyFile))
	if err != nil {
		return nil, nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
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

// parseIfInet6 reads /proc/net/if_inet6 lines ("<addr hex> <ifindex>
// <prefixlen> <scope> <flags> <name>") into each address's flags.
func parseIfInet6(sc *bufio.Scanner) map[netip.Addr]uint32 {
	out := map[netip.Addr]uint32{}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 || len(fields[0]) != 32 {
			continue
		}
		b, err := hex.DecodeString(fields[0])
		if err != nil {
			continue
		}
		flags, err := strconv.ParseUint(fields[4], 16, 32)
		if err != nil {
			continue
		}
		out[netip.AddrFrom16([16]byte(b))] = uint32(flags)
	}
	return out
}
