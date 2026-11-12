package server

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/export"
	"github.com/gmccollu/woodshed/internal/index"
)

// TestEventsMatchGapDetection runs the real service and listeners through a
// bind error, a retry and a stop, and checks that every start, stop and bind
// event they record classifies the way export's gap detection reads it. It
// fails if an event loses its Source or the listeners start emitting
// something gap detection doesn't understand.
func TestEventsMatchGapDetection(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	hold, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	r := start(t, writeConfig(t, dir, port, 0, nil))
	time.Sleep(50 * time.Millisecond)
	hold.Close()
	waitFor(t, "TCP up after Retry", func() bool {
		r.do(t, "POST", "/api/listeners/retry", nil, nil)
		evs, _ := r.srv.idx.Events(context.Background(), time.Time{}, time.Time{}, 0)
		for _, e := range evs {
			if c, p := export.ClassifyEvent(e); c == export.EventListenerStart && p == "tcp" {
				return true
			}
		}
		return false
	})
	r.stop(t)

	idx, err := index.Open(filepath.Join(dir, "data", index.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	evs, err := idx.Events(context.Background(), time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		cls, proto := export.ClassifyEvent(e)
		switch e.Kind {
		case index.KindStart, index.KindStop, index.KindBind:
			if cls == export.EventOther {
				t.Errorf("gap detection can't read %s event %q (source %q)", e.Kind, e.Text, e.Source)
			}
		}
		seen[fmt.Sprintf("%d/%s", cls, proto)] = true
	}
	for _, want := range []struct {
		cls   export.EventClass
		proto string
	}{
		{export.EventServiceStart, ""}, {export.EventServiceStop, ""},
		{export.EventBindError, "tcp"}, {export.EventListenerStart, "tcp"}, {export.EventListenerStart, "udp"},
		{export.EventListenerStop, "tcp"}, {export.EventListenerStop, "udp"},
	} {
		if !seen[fmt.Sprintf("%d/%s", want.cls, want.proto)] {
			t.Errorf("no event of class %d for %q among %d events", want.cls, want.proto, len(evs))
		}
	}
	// And the gaps they give end with "not running" after the stop (the TCP
	// outage, ~50 ms, is under the 1 s minimum).
	from := evs[0].Time.Add(-time.Second)
	last := evs[len(evs)-1].Time
	gaps := export.ComputeGaps(evs, from, last.Add(10*time.Second), last.Add(10*time.Second), func(time.Time) time.Time { return time.Time{} })
	var kinds []string
	for _, g := range gaps {
		kinds = append(kinds, g.Kind+"/"+g.Proto)
	}
	if len(gaps) == 0 || gaps[len(gaps)-1].Kind != export.GapNotRunning {
		t.Errorf("no not_running gap after the stop: %v", kinds)
	}
}
