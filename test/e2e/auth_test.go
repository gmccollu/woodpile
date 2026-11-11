package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// TestRequireLoginLocal runs the real binary with require_login_local: the
// session cookie alone (what another server on this host could catch) gets
// nothing; cookie and session secret together work; `woodpile status` still
// reports the server.
func TestRequireLoginLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	dir := t.TempDir()
	syslogPort, uiPort := pickPorts(t)
	cfgPath, dataDir := writeConfig(t, dir, syslogPort, uiPort)
	p := startWoodpile(t, cfgPath, dataDir, uiPort)

	req := func(method, path, body string, headers ...string) (int, []byte, http.Header) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r, _ := http.NewRequest(method, p.base+path, rd)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		for _, h := range headers {
			k, v, _ := strings.Cut(h, ": ")
			r.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header
	}
	if code, b, _ := req("PUT", "/api/password", `{"new": "e2e local password"}`); code != 204 {
		t.Fatalf("set password: %d %s", code, b)
	}
	code, b, _ := req("GET", "/api/settings", "")
	var set map[string]any
	json.Unmarshal(b, &set)
	set["ui"].(map[string]any)["require_login_local"] = true
	sb, _ := json.Marshal(set)
	if code, b, _ = req("PUT", "/api/settings", string(sb)); code != 200 {
		t.Fatalf("enable: %d %s", code, b)
	}
	if code, _, _ = req("GET", "/api/status", ""); code != 401 {
		t.Fatalf("status without a session: %d", code)
	}
	code, b, h := req("POST", "/api/login", `{"password": "e2e local password"}`)
	if code != 200 {
		t.Fatalf("login %d %s", code, b)
	}
	var info struct {
		Authenticated bool   `json:"authenticated"`
		SessionSecret string `json:"session_secret"`
	}
	json.Unmarshal(b, &info)
	cookie := strings.SplitN(h.Get("Set-Cookie"), ";", 2)[0]
	if !info.Authenticated || info.SessionSecret == "" || !strings.HasPrefix(cookie, "woodpile_session=") {
		t.Fatalf("login: %s, cookie %q", b, cookie)
	}
	ck, secret := "Cookie: "+cookie, "X-Woodpile-Session: "+info.SessionSecret
	for _, tc := range []struct {
		headers []string
		want    int
	}{{[]string{ck}, 401}, {[]string{secret}, 401}, {[]string{ck, secret}, 200}} {
		if code, _, _ := req("GET", "/api/status", "", tc.headers...); code != tc.want {
			t.Errorf("status with %d header(s): %d, want %d", len(tc.headers), code, tc.want)
		}
	}
	if _, b, _ := req("GET", "/api/session", "", ck, secret); bytes.Contains(b, []byte(info.SessionSecret)) {
		t.Error("/api/session returned the secret")
	}
	out, err := exec.Command(binary, "status", "--config", cfgPath).CombinedOutput()
	if !strings.Contains(string(out), "the UI needs a login") {
		t.Errorf("woodpile status (%v):\n%s", err, out)
	}
	if code, _, _ := req("POST", "/api/logout", "", ck, secret); code != 204 {
		t.Errorf("logout %d", code)
	}
	if code, _, _ := req("GET", "/api/status", "", ck, secret); code != 401 {
		t.Errorf("after logout: %d", code)
	}
	p.stop()
}
