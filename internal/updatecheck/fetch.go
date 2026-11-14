package updatecheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

// Limits on the request (SPEC §10.2).
const (
	requestTimeout = 10 * time.Second
	maxBody        = 1 << 20
)

// Error kinds, as GET /api/updates reports them in last_error_kind.
const (
	KindDNS        = "dns"
	KindConnect    = "connect"
	KindProxy      = "proxy"
	KindTLS        = "tls"
	KindTimeout    = "timeout"
	KindHTTPStatus = "http_status"
	KindRedirect   = "redirect"
	KindTooLarge   = "too_large"
	KindBadJSON    = "bad_json"
	KindNoRelease  = "no_release"
)

// CheckError is a failed check: its kind and a short message for Settings.
// Messages are written here, never copied from the response, so a hostile
// server or proxy can't put text in the UI.
type CheckError struct {
	Kind string
	Msg  string
	err  error // the underlying error, for the service log
}

func (e *CheckError) Error() string { return e.Msg }

// releaseEntry is what the checker reads of a release in releases.json.
type releaseEntry struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// releasesFile is the shape of releases.json (site/tools/releases.mjs): the
// checker reads only stable and prerelease.
type releasesFile struct {
	Stable     *releaseEntry `json:"stable"`
	Prerelease *releaseEntry `json:"prerelease"`
}

// newTransport is the checker's own transport and dialer: the system's roots,
// TLS 1.2 or later, HTTPS_PROXY and NO_PROXY from the environment, HTTP/1.1.
// fetch closes the connection after each request (one request a day). hooks
// replaces the dialer and roots in tests.
func newTransport(hooks *TestHooks) *http.Transport {
	d := &net.Dialer{Timeout: requestTimeout, KeepAlive: -1}
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   requestTimeout,
		ResponseHeaderTimeout: requestTimeout,
		// Not DisableKeepAlives: that adds a "Connection: close" header. fetch
		// closes the idle connection itself.
		MaxIdleConns:           1,
		IdleConnTimeout:        time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		// The CONNECT to a proxy names us the same way as the request.
		ProxyConnectHeader: http.Header{"User-Agent": {userAgent}},
		// HTTP/1.1 only: one small file a day doesn't need HTTP/2.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	if hooks != nil {
		if hooks.DialContext != nil {
			t.DialContext = hooks.DialContext
		}
		if hooks.RootCAs != nil {
			t.TLSClientConfig.RootCAs = hooks.RootCAs
		}
	}
	return t
}

// userAgent names the caller without a version (SPEC §10.2).
const userAgent = "Woodshed"

// fetch makes the one GET and parses the file. The only headers are Host,
// User-Agent, Accept and Accept-Encoding (added by the transport, which also
// decompresses): no query, cookies, credentials or conditional headers.
func fetch(ctx context.Context, t *http.Transport) (*releasesFile, *CheckError) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL, nil)
	if err != nil {
		return nil, &CheckError{Kind: KindConnect, Msg: "couldn't build the request", err: err}
	}
	req.Header = http.Header{"User-Agent": {userAgent}, "Accept": {"application/json"}}
	proxyURL, _ := t.Proxy(req)

	client := &http.Client{
		Transport: t,
		Timeout:   requestTimeout,
		// A redirect is an error: the URL is a contract and must not move.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer t.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, classify(err, proxyURL != nil)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, &CheckError{Kind: KindRedirect, Msg: fmt.Sprintf("the server answered with a redirect (%d); redirects aren't followed", resp.StatusCode)}
	case resp.StatusCode != http.StatusOK:
		return nil, &CheckError{Kind: KindHTTPStatus, Msg: fmt.Sprintf("the server answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))}
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return nil, &CheckError{Kind: KindBadJSON, Msg: "the server didn't send JSON"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, classify(err, proxyURL != nil)
	}
	if len(body) > maxBody {
		return nil, &CheckError{Kind: KindTooLarge, Msg: "the releases file is larger than 1 MiB"}
	}
	var f releasesFile
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, &CheckError{Kind: KindBadJSON, Msg: "the releases file isn't valid JSON", err: err}
	}
	return &f, nil
}

// classify turns a transport error into a kind and a message.
func classify(err error, viaProxy bool) *CheckError {
	ce := func(kind, msg string) *CheckError { return &CheckError{Kind: kind, Msg: msg, err: err} }
	var (
		dnsErr   *net.DNSError
		opErr    *net.OpError
		unknown  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		invalid  x509.CertificateInvalidError
		recErr   tls.RecordHeaderError
		alertErr tls.AlertError
		verify   *tls.CertificateVerificationError
		netErr   net.Error
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return ce(KindTimeout, "no answer within 10 s")
	case errors.As(err, &unknown):
		return ce(KindTLS, "TLS certificate not trusted (a proxy that inspects HTTPS?)")
	case errors.As(err, &hostErr):
		return ce(KindTLS, "TLS certificate is for another name (a proxy that inspects HTTPS?)")
	case errors.As(err, &invalid):
		return ce(KindTLS, "TLS certificate isn't valid (expired, or the clock is wrong?)")
	case errors.As(err, &verify):
		return ce(KindTLS, "TLS certificate couldn't be verified")
	case errors.As(err, &recErr):
		return ce(KindTLS, "the server didn't answer with TLS")
	case errors.As(err, &alertErr):
		return ce(KindTLS, "the TLS handshake failed")
	case viaProxy:
		return ce(KindProxy, "couldn't connect through the proxy in HTTPS_PROXY")
	case errors.As(err, &dnsErr):
		return ce(KindDNS, "couldn't look up "+Host)
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return ce(KindConnect, "couldn't connect to "+Host)
	case strings.Contains(err.Error(), "tls:"):
		return ce(KindTLS, "the TLS handshake failed")
	}
	return ce(KindConnect, "couldn't connect to "+Host)
}
