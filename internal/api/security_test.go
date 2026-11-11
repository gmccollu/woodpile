package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func checkSecurityHeaders(t *testing.T, where string, h http.Header) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": ContentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", where, k, got, v)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, part := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, part) {
			t.Errorf("%s: CSP lacks %q", where, part)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("%s: CSP allows unsafe sources: %s", where, csp)
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	for _, rq := range []struct{ method, path, body string }{
		{"GET", "/", ""},
		{"GET", "/index.html", ""},
		{"GET", "/assets/index-BHAE32ir.js", ""},
		{"GET", "/assets/missing-AAAAAAAA.js", ""},
		{"GET", "/devices", ""},
		{"GET", "/api/status", ""},
		{"GET", "/api/nope", ""},
		{"DELETE", "/api/status", ""},
		{"GET", "/api/search", ""},
		{"GET", "/healthz", ""},
		{"PATCH", "/api/devices/nope", `{}`},
	} {
		r := do(t, c, rq.method, srv.url(rq.path), rq.body)
		checkSecurityHeaders(t, rq.method+" "+rq.path, r.header)
	}
	// Rejected requests too.
	r := do(t, c, "GET", srv.url("/api/status"), "", "Host: evil.example")
	checkSecurityHeaders(t, "bad host", r.header)
	r = do(t, c, "POST", srv.url("/api/listeners/retry"), "", "Sec-Fetch-Site: cross-site")
	checkSecurityHeaders(t, "cross-site", r.header)
	if cc := do(t, c, "GET", srv.url("/api/status"), "").header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("API Cache-Control %q", cc)
	}
}

func TestHostHeaderRejected(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	port := srv.boundPort()
	for _, h := range []string{
		"evil.example",
		fmt.Sprintf("evil.example:%d", port),
		fmt.Sprintf("localhost.evil.example:%d", port),
		fmt.Sprintf("10.1.0.50:%d", port),
		fmt.Sprintf("localhost:%d", port+1),
		"localhost",
		"127.0.0.1",
		fmt.Sprintf("127.0.0.2:%d", port),
		fmt.Sprintf("[::ffff:127.0.0.1]:%d", port),
	} {
		for _, path := range []string{"/api/status", "/", "/healthz"} {
			wantError(t, do(t, c, "GET", srv.url(path), "", "Host: "+h), 403, "")
		}
	}
	for _, h := range []string{
		fmt.Sprintf("localhost:%d", port),
		fmt.Sprintf("LocalHost:%d", port),
		fmt.Sprintf("127.0.0.1:%d", port),
		fmt.Sprintf("[::1]:%d", port),
	} {
		if r := do(t, c, "GET", srv.url("/api/status"), "", "Host: "+h); r.code != 200 {
			t.Errorf("Host %s: %d %s", h, r.code, r.body)
		}
	}
}

func TestCrossOriginStateChangeRejected(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	same := fmt.Sprintf("http://127.0.0.1:%d", srv.boundPort())
	u := srv.url("/api/devices/10.1.0.1")

	for _, hdrs := range [][]string{
		{"Sec-Fetch-Site: cross-site"},
		{"Sec-Fetch-Site: same-site"},
		{"Origin: http://evil.example"},
		{"Origin: http://localhost:5173"},
		{"Origin: null"},
		{"Sec-Fetch-Site: cross-site", "Origin: " + same},
	} {
		wantError(t, do(t, c, "PATCH", u, `{"name":"x"}`, hdrs...), 403, "Cross-origin")
		wantError(t, do(t, c, "POST", srv.url("/api/listeners/retry"), "", hdrs...), 403, "")
		wantError(t, do(t, c, "PUT", srv.url("/api/password"), `{"new":"longenough"}`, hdrs...), 403, "")
	}
	srv.b.mu.Lock()
	n, np := srv.b.retries, len(srv.b.patches)
	srv.b.mu.Unlock()
	if n != 0 || np != 0 {
		t.Fatalf("a cross-origin request reached the backend (retries %d, patches %d)", n, np)
	}
	if srv.auth.passwordSet() {
		t.Fatal("a cross-origin request set the password")
	}

	// Same-origin requests (and non-browser ones with neither header) pass.
	for _, hdrs := range [][]string{
		{"Sec-Fetch-Site: same-origin"},
		{"Origin: " + same},
		{},
	} {
		if r := do(t, c, "PATCH", u, `{"name":"x"}`, hdrs...); r.code != 200 {
			t.Errorf("%v: %d %s", hdrs, r.code, r.body)
		}
	}
	// Cross-origin GETs aren't state-changing; CORS keeps the response from the page.
	if r := do(t, c, "GET", srv.url("/api/status"), "", "Sec-Fetch-Site: cross-site"); r.code != 200 {
		t.Errorf("cross-site GET: %d", r.code)
	}
	if r := do(t, c, "GET", srv.url("/api/status"), ""); r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header set")
	}
}

func TestStaticUI(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()

	r := do(t, c, "GET", srv.url("/"), "")
	if r.code != 200 || !strings.Contains(string(r.body), `<div id="app">`) {
		t.Fatalf("/: %d %s", r.code, r.body)
	}
	if r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.header.Get("Cache-Control") != "no-cache" {
		t.Errorf("/: %q %q", r.header.Get("Content-Type"), r.header.Get("Cache-Control"))
	}
	etag := r.header.Get("ETag")
	if etag == "" {
		t.Error("no ETag on index.html")
	}
	if r := do(t, c, "GET", srv.url("/"), "", "If-None-Match: "+etag); r.code != 304 {
		t.Errorf("conditional GET: %d", r.code)
	}

	for path, want := range map[string][2]string{
		"/assets/index-BHAE32ir.js":  {"text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		"/assets/index-VoJOug0b.css": {"text/css; charset=utf-8", "public, max-age=31536000, immutable"},
		"/favicon.svg":               {"image/svg+xml", "no-cache"},
		"/index.html":                {"text/html; charset=utf-8", "no-cache"},
	} {
		r := do(t, c, "GET", srv.url(path), "")
		if r.code != 200 || r.header.Get("Content-Type") != want[0] || r.header.Get("Cache-Control") != want[1] {
			t.Errorf("%s: %d %q %q", path, r.code, r.header.Get("Content-Type"), r.header.Get("Cache-Control"))
		}
	}
	// SPA fallback.
	for _, p := range []string{"/devices", "/settings/storage", "/x.html"} {
		r := do(t, c, "GET", srv.url(p), "")
		if r.code != 200 || !strings.Contains(string(r.body), `<div id="app">`) || r.header.Get("Cache-Control") != "no-cache" {
			t.Errorf("fallback %s: %d %q", p, r.code, r.body)
		}
	}
	// No dotfiles, and missing hashed assets are a 404, not HTML.
	if r := do(t, c, "GET", srv.url("/.keep"), ""); !strings.Contains(string(r.body), `<div id="app">`) {
		t.Errorf("/.keep served: %q", r.body)
	}
	wantError(t, do(t, c, "GET", srv.url("/assets/index-XXXXXXXX.js"), ""), 404, "")
	if r := do(t, c, "HEAD", srv.url("/"), ""); r.code != 200 || len(r.body) != 0 {
		t.Errorf("HEAD: %d %d bytes", r.code, len(r.body))
	}
}

func TestStaticNotBuilt(t *testing.T) {
	srv := startServer(t, nil, UIConfig{}, func(o *Options) {
		o.Static = fstest.MapFS{".keep": {Data: nil}}
	})
	for _, p := range []string{"/", "/index.html", "/devices"} {
		r := do(t, httpClient(), "GET", srv.url(p), "")
		if r.code != 200 || !strings.Contains(string(r.body), "doesn't include the web UI") {
			t.Errorf("%s: %d %s", p, r.code, r.body)
		}
		if strings.Contains(string(r.body), "<script") || strings.Contains(string(r.body), "style=") {
			t.Error("the built-in page needs inline script or style")
		}
	}
	if r := do(t, httpClient(), "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Errorf("API without UI: %d", r.code)
	}
}

func TestEmbeddedDistLoads(t *testing.T) {
	// The real embed (whatever is in web/dist now) must load, built or not.
	s, err := New(Options{Backend: newFakeBackend(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if s.static.index == nil {
		t.Fatal("no index page")
	}
}

func TestContentTypesAreFixed(t *testing.T) {
	for _, ext := range []string{".js", ".css", ".html", ".svg", ".json", ".woff2"} {
		if contentTypes[ext] == "" {
			t.Errorf("no content type for %s", ext)
		}
	}
	for _, name := range []string{"index-BHAE32ir.js", "index-VoJOug0b.css", "vendor-a1_b2-C3.js"} {
		if !hashedAsset.MatchString(name) {
			t.Errorf("%s not seen as hashed", name)
		}
	}
	for _, name := range []string{"index.js", "favicon.svg", "logo-dark.svg"} {
		if hashedAsset.MatchString(name) {
			t.Errorf("%s seen as hashed", name)
		}
	}
}
