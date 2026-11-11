package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/loadgen"
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
	// Some messages, for the export below.
	run := loadgen.NewRunID()
	if _, err := loadgen.Send(context.Background(), loadgen.SendOptions{Addr: fmt.Sprintf("127.0.0.1:%d", syslogPort),
		Proto: "tcp", Count: 50, RunID: run, Message: "auth e2e"}); err != nil {
		t.Fatal(err)
	}
	// The first password turns require_login_local on by itself.
	if code, b, _ := req("PUT", "/api/password", `{"new": "e2e local password"}`); code != 204 {
		t.Fatalf("set password: %d %s", code, b)
	}
	if code, _, _ := req("GET", "/api/status", ""); code != 401 {
		t.Fatalf("status without a session after the first password: %d", code)
	}
	code, b, h := req("POST", "/api/login", `{"password": "e2e local password"}`)
	var cookieLine string
	for _, v := range h.Values("Set-Cookie") {
		if strings.HasPrefix(v, "woodpile_session=") {
			cookieLine = v
		}
	}
	if code != 200 {
		t.Fatalf("login %d %s", code, b)
	}
	var info struct {
		Authenticated bool   `json:"authenticated"`
		SessionSecret string `json:"session_secret"`
	}
	json.Unmarshal(b, &info)
	cookie := strings.SplitN(cookieLine, ";", 2)[0]
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
	// An authenticated download: ask for a ticket, then fetch with the cookie
	// only, as <a href download> does.
	now := time.Now().UTC()
	eb, _ := json.Marshal(map[string]any{"all_devices": true, "from": now.Add(-time.Hour).Format(time.RFC3339),
		"to": now.Add(time.Minute).Format(time.RFC3339)})
	var created struct{ ID string }
	waitFor(t, "the messages to be exportable", 20*time.Second, func() bool {
		code, b, _ := req("POST", "/api/exports", string(eb), ck, secret)
		json.Unmarshal(b, &created)
		return code == 202
	})
	waitFor(t, "the export", 30*time.Second, func() bool {
		_, b, _ := req("GET", "/api/exports/"+created.ID, "", ck, secret)
		return bytes.Contains(b, []byte(`"state":"ready"`))
	})
	if code, _, _ := req("GET", "/api/exports/"+created.ID+"/download", "", ck); code != 401 {
		t.Errorf("download with the cookie only: %d", code)
	}
	code, b, _ = req("POST", "/api/exports/"+created.ID+"/ticket", "", ck, secret)
	var tk struct{ URL string }
	json.Unmarshal(b, &tk)
	if code != 200 || tk.URL == "" {
		t.Fatalf("ticket %d %s", code, b)
	}
	code, zb, zh := req("GET", tk.URL, "", ck)
	if code != 200 || !bytes.HasPrefix(zb, []byte("PK\x03\x04")) || !strings.HasPrefix(zh.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download with a ticket: %d (%d bytes)", code, len(zb))
	}
	if code, _, _ := req("GET", tk.URL, "", ck); code != 401 {
		t.Errorf("ticket reused: %d", code)
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
