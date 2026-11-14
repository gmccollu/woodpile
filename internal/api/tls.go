package api

import (
	"cmp"
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"slices"
	"time"
)

// The TLS syslog listener's routes (SPEC §10.3, docs/api.md "TLS syslog
// listener"): its state, renewals, the CA downloads and the handshake failures.

// TLSBackend is what the TLS routes need from the service.
type TLSBackend interface {
	// TLS serves GET /api/tls.
	TLS(ctx context.Context) (TLSState, error)
	// RenewTLS serves POST /api/tls/renew: a new leaf signed by the same CA,
	// or with newCA a new CA too. ErrTLSUserCert with a user's own
	// certificate.
	RenewTLS(ctx context.Context, newCA bool) (TLSState, error)
	// TLSCA serves GET /api/tls/ca.pem and ca.der: the CA certificate, DER.
	// ErrNoTLSCA before TLS was ever turned on, ErrTLSUserCert with a user's
	// own certificate.
	TLSCA(ctx context.Context) ([]byte, error)
	// TLSFailures serves GET /api/tls/failures.
	TLSFailures(ctx context.Context) ([]TLSFailure, error)
}

// Errors of the TLS routes.
var (
	ErrNoTLSCA     = errors.New("TLS has never been turned on, so there's no CA certificate yet")
	ErrTLSUserCert = errors.New("this server uses your own certificate")
)

// TLSState is the body of GET /api/tls.
type TLSState struct {
	Enabled        bool        `json:"enabled"`
	Port           int         `json:"port"`
	Source         string      `json:"source"` // "woodshed" (its own CA) or "file" (cert_file/key_file)
	AllowLegacyTLS bool        `json:"allow_legacy_tls"`
	CA             *TLSCA      `json:"ca"`   // nil with source "file", or before TLS was turned on
	Cert           *TLSCert    `json:"cert"` // nil before TLS was turned on
	Firewall       TLSFirewall `json:"firewall"`
}

// TLSCA is the CA certificate's description.
type TLSCA struct {
	Subject   string    `json:"subject"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	SHA256    string    `json:"sha256"`
	SHA1      string    `json:"sha1"`
}

// TLSCert is the server certificate's description.
type TLSCert struct {
	Subject      string    `json:"subject"`
	Issuer       string    `json:"issuer"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	SHA256       string    `json:"sha256"`
	DNSNames     []string  `json:"dns_names"`
	IPAddresses  []string  `json:"ip_addresses"`
	MissingNames []string  `json:"missing_names"` // host DNS names it doesn't cover (tls_cert_names)
}

// TLSFirewall is what Woodshed can see of the OS firewall for the TLS port.
type TLSFirewall struct {
	State   string `json:"state"`   // ok, missing, disabled, wrong_port, unknown
	Command string `json:"command"` // what to run as an administrator ("" except on Windows)
}

// TLSFailure is one sender with failed TLS handshakes (GET /api/tls/failures).
type TLSFailure struct {
	IP       string    `json:"ip"`
	Count24h int64     `json:"count_24h"`
	LastSeen time.Time `json:"last_seen"`
	Reason   string    `json:"reason"`
	Detail   string    `json:"detail"`
	DeviceID string    `json:"device_id"` // "" when it isn't a known device
}

// DeviceTLS is a device's TLS counts (Device.TLS; null until it tried TLS).
type DeviceTLS struct {
	Connections24h int64             `json:"connections_24h"`
	Version        string            `json:"version"`
	Cipher         string            `json:"cipher"`
	LastConnected  *time.Time        `json:"last_connected"`
	Failures24h    int64             `json:"failures_24h"`
	LastFailure    *DeviceTLSFailure `json:"last_failure"`
}

// DeviceTLSFailure is a device's latest failed handshake.
type DeviceTLSFailure struct {
	Time   time.Time `json:"time"`
	Reason string    `json:"reason"`
	Detail string    `json:"detail"`
}

func (st *TLSState) normalize() {
	if st.CA != nil {
		st.CA.NotBefore, st.CA.NotAfter = st.CA.NotBefore.UTC(), st.CA.NotAfter.UTC()
	}
	if c := st.Cert; c != nil {
		c.NotBefore, c.NotAfter = c.NotBefore.UTC(), c.NotAfter.UTC()
		c.DNSNames, c.IPAddresses, c.MissingNames = nonNil(c.DNSNames), nonNil(c.IPAddresses), nonNil(c.MissingNames)
	}
	if st.Firewall.State == "" {
		st.Firewall.State = "unknown"
	}
}

// MaxTLSFailures caps GET /api/tls/failures.
const MaxTLSFailures = 200

func normalizeTLSFailures(fs []TLSFailure) []TLSFailure {
	fs = slices.Clone(nonNil(fs))
	slices.SortStableFunc(fs, func(a, b TLSFailure) int {
		if a.Count24h != b.Count24h {
			return cmp.Compare(b.Count24h, a.Count24h)
		}
		return b.LastSeen.Compare(a.LastSeen)
	})
	if len(fs) > MaxTLSFailures {
		fs = fs[:MaxTLSFailures]
	}
	for i := range fs {
		fs[i].LastSeen = fs[i].LastSeen.UTC()
	}
	return fs
}

// tlsRoutes adds the TLS routes to mux.
func (s *Server) tlsRoutes(mux *http.ServeMux) {
	b := s.backend
	mux.Handle("/api/tls", methods{http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
		st, err := b.TLS(r.Context())
		if err != nil {
			s.backendError(w, r, err)
			return
		}
		st.normalize()
		writeJSON(w, http.StatusOK, st)
	}})
	mux.Handle("/api/tls/renew", methods{http.MethodPost: func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NewCA bool `json:"new_ca"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		st, err := b.RenewTLS(r.Context(), body.NewCA)
		if errors.Is(err, ErrTLSUserCert) {
			writeError(w, http.StatusConflict, "This server uses your own certificate; replace the files instead.")
			return
		}
		if errors.Is(err, ErrNoTLSCA) {
			writeError(w, http.StatusConflict, "TLS has never been turned on, so there's no certificate to renew. Turn it on first.")
			return
		}
		if err != nil {
			s.backendError(w, r, err)
			return
		}
		st.normalize()
		writeJSON(w, http.StatusOK, st)
	}})
	ca := func(pemForm bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			der, err := b.TLSCA(r.Context())
			switch {
			case errors.Is(err, ErrTLSUserCert):
				writeError(w, http.StatusNotFound, "This server uses your own certificate; get its CA from whoever issued it.")
				return
			case errors.Is(err, ErrNoTLSCA):
				writeError(w, http.StatusNotFound, "TLS has never been turned on, so there's no CA certificate yet.")
				return
			case err != nil:
				s.backendError(w, r, err)
				return
			}
			h := w.Header()
			h.Set("Cache-Control", "no-store")
			if pemForm {
				h.Set("Content-Type", "application/x-pem-file")
				h.Set("Content-Disposition", `attachment; filename="woodshed-ca.pem"`)
				_, _ = w.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
				return
			}
			h.Set("Content-Type", "application/pkix-cert")
			h.Set("Content-Disposition", `attachment; filename="woodshed-ca.cer"`)
			_, _ = w.Write(der)
		}
	}
	mux.Handle("/api/tls/ca.pem", methods{http.MethodGet: ca(true)})
	mux.Handle("/api/tls/ca.der", methods{http.MethodGet: ca(false)})
	mux.Handle("/api/tls/failures", methods{http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
		fs, err := b.TLSFailures(r.Context())
		if err != nil {
			s.backendError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, normalizeTLSFailures(fs))
	}})
}
