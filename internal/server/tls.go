package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/atomicfile"
	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/fwrule"
	"github.com/gmccollu/woodshed/internal/index"
	"github.com/gmccollu/woodshed/internal/model"
	"github.com/gmccollu/woodshed/internal/tlscert"
)

// The TLS syslog listener's service side (SPEC §10.3): its certificate, the
// banners, the firewall hint and the API's TLS routes.

// fwRecheck is how often the Windows firewall rule's state is read again.
const fwRecheck = time.Minute

// tlsSide holds the certificate and the cached firewall state.
type tlsSide struct {
	certs *tlscert.Manager

	// fwCheck reads the Windows rule's state for a port (nil elsewhere).
	fwCheck func(port int) string
	fwMu    sync.Mutex
	fwState string
	fwPort  int
	fwAt    time.Time
}

func newTLSSide(dataDir string, l config.TLSListener, now func() time.Time, extras func() []string) *tlsSide {
	t := &tlsSide{certs: tlscert.New(tlscert.Options{
		DataDir: dataDir, CertFile: l.CertFile, KeyFile: l.KeyFile, Now: now,
		Identity: func() (tlscert.Identity, error) {
			host, primary, names, ips, err := api.HostIdentity(extras())
			return tlscert.Identity{Host: host, Primary: primary, Names: names, IPs: ips}, err
		},
	})}
	if runtime.GOOS == "windows" {
		t.fwCheck = func(port int) string { return fwrule.WindowsState(fwrule.Exec, port) }
	}
	return t
}

// firewall returns the Windows rule's state for port (cached for fwRecheck), or
// "unknown" where it isn't read.
func (t *tlsSide) firewall(port int, now time.Time) string {
	if t.fwCheck == nil {
		return fwrule.StateUnknown
	}
	t.fwMu.Lock()
	defer t.fwMu.Unlock()
	if t.fwState == "" || t.fwPort != port || now.Sub(t.fwAt) >= fwRecheck || now.Before(t.fwAt) {
		t.fwState, t.fwPort, t.fwAt = t.fwCheck(port), port, now
	}
	return t.fwState
}

// prepareTLS points the certificate at l's source and loads it: with the listener
// on, Woodshed's own CA and certificate are created the first time (an event
// records it). A user's files are re-read every time. It returns the load error
// (none for "no certificate yet" while the listener is off).
func (b *backend) prepareTLS(l config.TLSListener) error {
	m := b.tlsc.certs
	m.Recheck() // ui.extra_hostnames may have changed
	m.SetFiles(strings.TrimSpace(l.CertFile), strings.TrimSpace(l.KeyFile))
	if !l.Enabled && !l.UsesOwnCert() {
		_, err := m.Load(false)
		if errors.Is(err, tlscert.ErrNone) {
			return nil
		}
		return err
	}
	created, err := m.Load(l.Enabled)
	if errors.Is(err, tlscert.ErrNone) && !l.Enabled {
		return nil
	}
	if err != nil {
		b.log.Error("TLS certificate", "err", err)
		return err
	}
	if created {
		st := m.Status()
		text := "Created the TLS syslog certificate."
		if st.CA != nil && st.Cert != nil {
			text = fmt.Sprintf("Created the TLS syslog CA and certificate (TLS turned on). CA SHA-256 %s; certificate SHA-256 %s, valid until %s.",
				st.CA.SHA256, st.Cert.SHA256, st.Cert.NotAfter.Format("2006-01-02"))
		}
		b.tlsEvent(index.LevelInfo, text)
	}
	return nil
}

func (b *backend) tlsEvent(level index.Level, text string) {
	b.log.Info(text)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.idx.AddEvent(ctx, index.Event{Level: level, Kind: index.KindTLS, Text: text}); err != nil {
		b.log.Warn("couldn't record a TLS event", "err", err, "text", text)
	}
}

// ---- api.TLSBackend ----

func (b *backend) TLS(ctx context.Context) (api.TLSState, error) {
	return b.tlsState(b.config()), nil
}

func (b *backend) tlsState(cfg config.Config) api.TLSState {
	l := cfg.Listeners.TLS
	st := b.tlsc.certs.Status()
	out := api.TLSState{Enabled: l.Enabled, Port: l.Port, Source: st.Source, AllowLegacyTLS: l.AllowLegacyTLS}
	if c := st.CA; c != nil {
		out.CA = &api.TLSCA{Subject: c.Subject, NotBefore: c.NotBefore, NotAfter: c.NotAfter, SHA256: c.SHA256, SHA1: c.SHA1}
	}
	if c := st.Cert; c != nil {
		out.Cert = &api.TLSCert{Subject: c.Subject, Issuer: c.Issuer, NotBefore: c.NotBefore, NotAfter: c.NotAfter, SHA256: c.SHA256,
			DNSNames: c.DNSNames, IPAddresses: c.IPAddresses, MissingNames: st.MissingNames}
	}
	// Only the installed service has the rule (the MSI's or `woodshed install`'s):
	// for a portable copy or `woodshed run`, Windows' own first-run prompt
	// decides, so its state isn't this copy's to report (TLS decision 4).
	out.Firewall = api.TLSFirewall{State: fwrule.StateUnknown}
	if b.isService {
		out.Firewall.State = b.tlsc.firewall(l.Port, b.now())
	}
	switch out.Firewall.State {
	case fwrule.StateMissing, fwrule.StateDisabled, fwrule.StateWrongPort:
		out.Firewall.Command = "woodshed tls on"
	}
	return out
}

func (b *backend) RenewTLS(ctx context.Context, newCA bool) (api.TLSState, error) {
	b.saveMu.Lock()
	defer b.saveMu.Unlock()
	m := b.tlsc.certs
	before := m.Status()
	if before.Source == tlscert.SourceWoodshed && before.CA == nil && !newCA {
		return api.TLSState{}, api.ErrNoTLSCA
	}
	r, err := m.Renew(newCA)
	if errors.Is(err, tlscert.ErrUserCert) {
		return api.TLSState{}, api.ErrTLSUserCert
	}
	if err != nil {
		return api.TLSState{}, err
	}
	if newCA {
		b.tlsEvent(index.LevelWarn, fmt.Sprintf("Replaced the TLS syslog CA: CA SHA-256 %s → %s; certificate SHA-256 %s → %s. "+
			"Every device that sends over TLS must import the new CA certificate.", orNone(r.OldCA), r.NewCAHash, orNone(r.OldCert), r.NewCertHash))
	} else {
		b.tlsEvent(index.LevelInfo, fmt.Sprintf("Renewed the TLS syslog certificate with the same CA (devices need no change): "+
			"certificate SHA-256 %s → %s; CA SHA-256 %s.", orNone(r.OldCert), r.NewCertHash, r.NewCAHash))
	}
	return b.tlsState(b.config()), nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func (b *backend) TLSCA(ctx context.Context) ([]byte, error) {
	der, err := b.tlsc.certs.CADER()
	switch {
	case errors.Is(err, tlscert.ErrUserCert):
		return nil, api.ErrTLSUserCert
	case errors.Is(err, tlscert.ErrNone):
		return nil, api.ErrNoTLSCA
	}
	return der, err
}

func (b *backend) TLSFailures(ctx context.Context) ([]api.TLSFailure, error) {
	ln := b.ln.Load()
	if ln == nil {
		return []api.TLSFailure{}, nil
	}
	fs := ln.TLSFailures()
	if len(fs) == 0 {
		return []api.TLSFailure{}, nil
	}
	ds, err := b.listDevices(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(ds))
	for _, d := range ds {
		known[d.ID] = true
	}
	out := make([]api.TLSFailure, 0, len(fs))
	for _, f := range fs {
		e := api.TLSFailure{IP: f.IP, Count24h: f.Failures24h, LastSeen: f.LastFailure.Time}
		e.Reason, e.Detail = f.LastFailure.Reason, f.LastFailure.Detail
		if ip, err := netip.ParseAddr(f.IP); err == nil && known[model.DeviceID(ip)] {
			e.DeviceID = model.DeviceID(ip)
		}
		out = append(out, e)
	}
	return out, nil
}

// markTLS fills each device's tls object from the listener's per-sender stats.
func (b *backend) markTLS(ds []api.Device) {
	ln := b.ln.Load()
	if ln == nil {
		return
	}
	for i := range ds {
		ip, err := netip.ParseAddr(ds[i].IP)
		if err != nil {
			continue
		}
		s, ok := ln.TLSSenderStats(ip)
		if !ok {
			continue
		}
		t := &api.DeviceTLS{Connections24h: s.Connections24h, Version: s.Version, Cipher: s.Cipher, Failures24h: s.Failures24h}
		if !s.LastConnected.IsZero() {
			lc := s.LastConnected
			t.LastConnected = &lc
		}
		if f := s.LastFailure; f != nil {
			t.LastFailure = &api.DeviceTLSFailure{Time: f.Time, Reason: f.Reason, Detail: f.Detail}
		}
		ds[i].TLS = t
	}
}

// ---- banners ----

// tlsBanners are the TLS listener's warn banners (docs/api.md), only while it's
// on. None of them ever renews anything.
func (b *backend) tlsBanners(cfg config.Config, now time.Time) []api.Banner {
	l := cfg.Listeners.TLS
	if !l.Enabled {
		return nil
	}
	var out []api.Banner
	if l.AllowLegacyTLS {
		out = append(out, api.Banner{Kind: "tls_legacy", Level: api.LevelWarn,
			Text: "TLS 1.0 and 1.1 are allowed for older senders. Turn this off once they're updated."})
	}
	st := b.tlsc.certs.Status()
	if which, at := st.Expiring(now); which != "" {
		date := at.UTC().Format("2006-01-02")
		var text string
		switch {
		case st.Source == tlscert.SourceFile:
			text = fmt.Sprintf("Your TLS syslog certificate (listeners.tls.cert_file) expires on %s. Replace the certificate and key files, then save Settings or press Retry.", date)
		case which == "ca":
			text = fmt.Sprintf("The TLS syslog CA expires on %s. Replace the CA in Settings → TLS, then import the new CA certificate on every device that sends over TLS.", date)
		default:
			text = fmt.Sprintf("The TLS syslog certificate expires on %s. Renew it, then nothing changes on your devices.", date)
		}
		out = append(out, api.Banner{Kind: "tls_cert_expiring", Level: api.LevelWarn, Text: text, Action: "tls_renew"})
	}
	if n := st.MissingNames; len(n) > 0 && st.Source == tlscert.SourceWoodshed {
		text := fmt.Sprintf("The TLS certificate doesn't name %s. Devices that check the server name will refuse it. Renew it to add the name.", n[0])
		if len(n) > 1 {
			text = fmt.Sprintf("The TLS certificate doesn't name %s. Devices that check the server name will refuse it. Renew it to add the names.",
				strings.Join(n, ", "))
		}
		out = append(out, api.Banner{Kind: "tls_cert_names", Level: api.LevelWarn, Text: text, Action: "tls_renew"})
	}
	// The firewall rule exists only for the installed service (portable mode and
	// `woodshed run` have none: Windows' own prompt covers them).
	if b.isService {
		if text := firewallText(b.tlsc.firewall(l.Port, now), l.Port); text != "" {
			out = append(out, api.Banner{Kind: "tls_firewall", Level: api.LevelWarn, Text: text})
		}
	}
	return out
}

func firewallText(state string, port int) string {
	const fix = " In an administrator prompt, run: woodshed tls on"
	switch state {
	case fwrule.StateDisabled:
		return fmt.Sprintf("The Windows firewall rule %q is turned off, so devices can't reach syslog over TLS on port %d.", fwrule.RuleName, port) + fix
	case fwrule.StateMissing:
		return fmt.Sprintf("There's no Windows firewall rule for syslog over TLS, so devices can't reach port %d.", port) + fix
	case fwrule.StateWrongPort:
		return fmt.Sprintf("The Windows firewall rule %q allows another port, not %d, so devices can't reach syslog over TLS.", fwrule.RuleName, port) + fix
	}
	return ""
}

// ---- run-state file ----

// RunStateTLS is the TLS listener's part of the run-state file, for `woodshed
// status`.
type RunStateTLS struct {
	Enabled      bool                 `json:"enabled"`
	Port         int                  `json:"port"`
	Source       string               `json:"source"`
	CASHA256     string               `json:"ca_sha256,omitempty"`
	CASHA1       string               `json:"ca_sha1,omitempty"`
	CANotAfter   *time.Time           `json:"ca_not_after,omitempty"`
	CertSHA256   string               `json:"cert_sha256,omitempty"`
	CertNotAfter *time.Time           `json:"cert_not_after,omitempty"`
	Error        string               `json:"error,omitempty"`
	Failures     []RunStateTLSFailure `json:"failures,omitempty"` // the 5 senders failing most in the last 24 h
}

// RunStateTLSFailure is one failing sender.
type RunStateTLSFailure struct {
	IP       string    `json:"ip"`
	Count24h int64     `json:"count_24h"`
	Reason   string    `json:"reason"`
	LastSeen time.Time `json:"last_seen"`
}

func (b *backend) runStateTLS(cfg config.Config) *RunStateTLS {
	l := cfg.Listeners.TLS
	st := b.tlsc.certs.Status()
	if !l.Enabled && st.Cert == nil {
		return nil
	}
	rs := &RunStateTLS{Enabled: l.Enabled, Port: l.Port, Source: st.Source, Error: st.Err}
	if st.CA != nil {
		at := st.CA.NotAfter
		rs.CASHA256, rs.CASHA1, rs.CANotAfter = st.CA.SHA256, st.CA.SHA1, &at
	}
	if st.Cert != nil {
		at := st.Cert.NotAfter
		rs.CertSHA256, rs.CertNotAfter = st.Cert.SHA256, &at
	}
	if ln := b.ln.Load(); ln != nil {
		for i, f := range ln.TLSFailures() {
			if i == 5 {
				break
			}
			rs.Failures = append(rs.Failures, RunStateTLSFailure{IP: f.IP, Count24h: f.Failures24h, Reason: f.LastFailure.Reason,
				LastSeen: f.LastFailure.Time.Truncate(time.Minute)})
		}
	}
	return rs
}

// ---- renewals asked for by `woodshed tls renew` ----

// TLSRenewRequestFile is where `woodshed tls renew` (run as an administrator)
// leaves its request; the service carries it out when it starts, as its own
// account, so the new files get the right owner, and records the event.
const TLSRenewRequestFile = "tls-renew-request.json"

type renewRequest struct {
	NewCA bool      `json:"new_ca"`
	Asked time.Time `json:"asked"`
}

// WriteTLSRenewRequest leaves a renewal request in dataDir (mode 0644: it holds
// nothing secret, and the service's account must read it).
func WriteTLSRenewRequest(dataDir string, newCA bool) error {
	b, _ := json.Marshal(renewRequest{NewCA: newCA, Asked: time.Now().UTC()})
	return atomicfile.WriteFile(filepath.Join(dataDir, TLSRenewRequestFile), append(b, '\n'), 0o644)
}

// applyTLSRenewRequest carries out a pending `woodshed tls renew`, then
// removes the request.
func (b *backend) applyTLSRenewRequest(dataDir string) {
	p := filepath.Join(dataDir, TLSRenewRequestFile)
	raw, err := atomicfile.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	defer os.Remove(p)
	var req renewRequest
	if err == nil {
		err = json.Unmarshal(raw, &req)
	}
	if err != nil {
		b.tlsEvent(index.LevelError, "Couldn't read the TLS renewal request from `woodshed tls renew`: "+err.Error())
		return
	}
	if _, err := b.RenewTLS(context.Background(), req.NewCA); err != nil {
		text := "The TLS renewal asked for by `woodshed tls renew` failed: " + err.Error()
		if errors.Is(err, api.ErrTLSUserCert) {
			text = "Ignored `woodshed tls renew`: this server uses your own certificate (listeners.tls.cert_file)."
		}
		b.tlsEvent(index.LevelError, text)
	}
}
