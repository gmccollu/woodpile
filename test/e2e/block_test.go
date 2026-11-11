package e2e

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/loadgen"
)

// TestBlockDevice: a device blocked through PATCH stops landing, live, while
// other senders carry on; its refusals show in /api/rejected; unblocking
// lets it back in.
func TestBlockDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	dir := t.TempDir()
	syslogPort, uiPort := pickPorts(t)
	cfgPath, dataDir := writeConfig(t, dir, syslogPort, uiPort)
	p := startWoodpile(t, cfgPath, dataDir, uiPort)

	ips := senderIPs(t)
	blocked, other := "127.0.0.1", ips[1] // 127.0.0.2 where loopback aliases exist
	twoSenders := other != blocked
	phase := []string{loadgen.NewRunID(), loadgen.NewRunID(), loadgen.NewRunID()}
	send := func(src, proto string, ph, first int) {
		t.Helper()
		if _, err := loadgen.Send(context.Background(), loadgen.SendOptions{Addr: fmt.Sprintf("127.0.0.1:%d", syslogPort),
			Proto: proto, Source: src, Count: 20, RunID: phase[ph], FirstSeq: first, Message: "block e2e"}); err != nil {
			t.Fatal(err)
		}
	}
	results := func(ph int) []searchResult {
		t.Helper()
		var pg searchPage
		p.getJSON("/api/search?limit=5000&q="+url.QueryEscape(loadgen.SeqTag(phase[ph])), &pg)
		return pg.Results
	}
	// Before: both land.
	send(blocked, "udp", 0, 0)
	if twoSenders {
		send(other, "udp", 0, 100)
	}
	waitFor(t, "phase 0", 20*time.Second, func() bool { return len(results(0)) == map[bool]int{true: 40, false: 20}[twoSenders] })

	if code, b := p.do("PATCH", "/api/devices/127.0.0.1", map[string]any{"blocked": true}); code != 200 ||
		!strings.Contains(string(b), `"status":"blocked"`) {
		t.Fatalf("block: %d %s", code, b)
	}
	// Blocked: UDP and TCP from the blocked sender are refused, the other lands.
	send(blocked, "udp", 1, 0)
	send(blocked, "tcp", 1, 20)
	if twoSenders {
		send(other, "udp", 1, 100)
		waitFor(t, "the other sender's phase 1", 20*time.Second, func() bool { return len(results(1)) >= 20 })
	} else {
		t.Log("NOTE: no loopback aliases; only the blocked sender is checked")
		time.Sleep(3 * time.Second) // a flush interval or two
	}
	for _, r := range results(1) {
		if r.DeviceID == "127.0.0.1" {
			t.Fatalf("a blocked message landed: %s", r.Raw)
		}
	}
	var rejected []map[string]any
	waitFor(t, "the refusals", 10*time.Second, func() bool {
		rejected = nil
		p.getJSON("/api/rejected", &rejected)
		return len(rejected) == 1 && rejected[0]["reason"] == "blocked" && num(rejected[0]["count_24h"]) >= 20
	})

	// Unblocked: it lands again.
	if code, b := p.do("PATCH", "/api/devices/127.0.0.1", map[string]any{"blocked": false}); code != 200 ||
		strings.Contains(string(b), `"blocked":true`) {
		t.Fatalf("unblock: %d %s", code, b)
	}
	send(blocked, "udp", 2, 0)
	waitFor(t, "phase 2 after unblocking", 20*time.Second, func() bool { return len(results(2)) == 20 })
	p.stop()
}
