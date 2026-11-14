package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUpdatesRoutes(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()

	r := do(t, c, "GET", srv.url("/api/updates"), "")
	if r.code != 200 || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET: %d %s", r.code, r.body)
	}
	m := r.json(t)
	if m["check"] != "ask" || m["last_check"] != "2026-11-13T09:12:44Z" || m["latest"].(map[string]any)["version"] != "1.2.0" {
		t.Errorf("GET body %s", r.body)
	}

	// PUT: on and off only.
	etag := do(t, c, "GET", srv.url("/api/settings"), "").header.Get("ETag")
	r = do(t, c, "PUT", srv.url("/api/updates"), `{"check":"on"}`)
	if r.code != 200 || r.json(t)["check"] != "on" {
		t.Fatalf("PUT on: %d %s", r.code, r.body)
	}
	set := do(t, c, "GET", srv.url("/api/settings"), "")
	if !strings.Contains(string(set.body), `"updates":{"check":"on"}`) || set.header.Get("ETag") == etag {
		t.Errorf("settings after PUT: %s (etag %s → %s)", set.body, etag, set.header.Get("ETag"))
	}
	for _, body := range []string{`{"check":"ask"}`, `{"check":"yes"}`, `{"check":""}`, `{}`, `{"check":"on","x":1}`} {
		r := do(t, c, "PUT", srv.url("/api/updates"), body)
		wantError(t, r, 400, "")
	}
	if srv.b.settings.Updates.Check != "on" {
		t.Errorf("a bad PUT changed it: %q", srv.b.settings.Updates.Check)
	}
	r = do(t, c, "PUT", srv.url("/api/updates"), `{"check":"off"}`)
	if r.code != 200 || r.json(t)["check"] != "off" {
		t.Fatalf("PUT off: %d %s", r.code, r.body)
	}

	// Check now: 202 with no body, 409 while off, 429 with Retry-After.
	r = do(t, c, "POST", srv.url("/api/updates/check"), "")
	if r.code != 202 || len(r.body) != 0 {
		t.Errorf("check: %d %q", r.code, r.body)
	}
	srv.b.mu.Lock()
	srv.b.checkNowErr = ErrUpdateCheckOff
	srv.b.mu.Unlock()
	wantError(t, do(t, c, "POST", srv.url("/api/updates/check"), ""), 409, "off")
	srv.b.mu.Lock()
	srv.b.checkNowErr = &RetryAfterError{After: 41500 * time.Millisecond, Msg: "A check ran less than a minute ago."}
	srv.b.mu.Unlock()
	r = do(t, c, "POST", srv.url("/api/updates/check"), "")
	wantError(t, r, 429, "minute")
	if r.header.Get("Retry-After") != "42" {
		t.Errorf("Retry-After %q", r.header.Get("Retry-After"))
	}
	wantError(t, do(t, c, "GET", srv.url("/api/updates/check"), ""), 405, "")

	// Dismiss.
	r = do(t, c, "POST", srv.url("/api/updates/dismiss"), `{"version":"1.2.0"}`)
	if r.code != 204 || len(r.body) != 0 {
		t.Errorf("dismiss: %d %s", r.code, r.body)
	}
	wantError(t, do(t, c, "POST", srv.url("/api/updates/dismiss"), `{"version":"<b>"}`), 400, "version")
	wantError(t, do(t, c, "POST", srv.url("/api/updates/dismiss"), `{"ver":"1.2.0"}`), 400, "unknown field")
	if len(srv.b.dismissed) != 1 || srv.b.dismissed[0] != "1.2.0" {
		t.Errorf("dismissed %v", srv.b.dismissed)
	}
}

// PUT /api/settings: updates.check is validated, and a body without it (a 1.0
// client) keeps the setting.
func TestSettingsUpdatesCheck(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	var s map[string]any
	if err := json.Unmarshal(do(t, c, "GET", srv.url("/api/settings"), "").body, &s); err != nil {
		t.Fatal(err)
	}
	put := func(v any) resp {
		b, _ := json.Marshal(v)
		return do(t, c, "PUT", srv.url("/api/settings"), string(b))
	}
	s["updates"] = map[string]any{"check": "maybe"}
	r := put(s)
	if r.code != 400 || !strings.Contains(string(r.body), `"updates.check"`) {
		t.Errorf("bad check: %d %s", r.code, r.body)
	}
	s["updates"] = map[string]any{"check": "off"}
	if r := put(s); r.code != 200 || srv.b.settings.Updates.Check != "off" {
		t.Errorf("off: %d %s", r.code, r.body)
	}
	delete(s, "updates")
	if r := put(s); r.code != 200 || srv.b.settings.Updates.Check != "off" {
		t.Errorf("without updates: %d %s, check %q", r.code, r.body, srv.b.settings.Updates.Check)
	}
}

// The url field is on a banner only when it has one.
func TestBannerURLOmitted(t *testing.T) {
	b := newFakeBackend()
	b.status.Banners = append(b.status.Banners, Banner{Kind: "update_available", Level: "info", Text: "Woodshed 1.2.0 is available (this is 1.1.0).",
		Action: "update_dismiss", URL: "https://example.invalid/releases/tag/v1.2.0"})
	srv := startServer(t, b, UIConfig{})
	r := do(t, httpClient(), "GET", srv.url("/api/status"), "")
	var st struct {
		Banners []map[string]any `json:"banners"`
	}
	if err := json.Unmarshal(r.body, &st); err != nil || len(st.Banners) != 2 {
		t.Fatalf("%s", r.body)
	}
	if _, ok := st.Banners[0]["url"]; ok {
		t.Errorf("url on a banner without one: %v", st.Banners[0])
	}
	if st.Banners[1]["url"] != "https://example.invalid/releases/tag/v1.2.0" || st.Banners[1]["kind"] != "update_available" {
		t.Errorf("update banner %v", st.Banners[1])
	}
}
