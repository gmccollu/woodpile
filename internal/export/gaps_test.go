package export

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/index"
)

func TestGaps(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return t0.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	ev := func(tm time.Time, kind index.Kind, level index.Level, text string) index.Event {
		return index.Event{Time: tm, Kind: kind, Level: level, Text: text}
	}
	start := func(tm time.Time) []index.Event {
		return []index.Event{
			ev(tm, index.KindStart, index.LevelInfo, "Woodpile 1.0 (abc) started on linux/amd64; data folder /var/lib/woodpile."),
			ev(tm.Add(time.Millisecond), index.KindStart, index.LevelInfo, "UDP listener started on 0.0.0.0:514; receive buffer 8 MiB granted of 32 MiB requested"),
			ev(tm.Add(2*time.Millisecond), index.KindStart, index.LevelInfo, "TCP listener started on 0.0.0.0:514"),
		}
	}
	stop := func(tm time.Time) []index.Event {
		return []index.Event{
			ev(tm.Add(-2*time.Millisecond), index.KindStop, index.LevelInfo, "UDP listener on 0.0.0.0:514 stopped"),
			ev(tm.Add(-time.Millisecond), index.KindStop, index.LevelInfo, "TCP listener on 0.0.0.0:514 stopped"),
			ev(tm, index.KindStop, index.LevelInfo, "Woodpile stopped."),
		}
	}
	cat := func(parts ...[]index.Event) []index.Event {
		var out []index.Event
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	noMsgs := func(time.Time) time.Time { return time.Time{} }
	now := at(23, 0)
	cases := []struct {
		name     string
		events   []index.Event
		from, to time.Time
		last     func(time.Time) time.Time
		want     []string
	}{
		{"running throughout", start(at(0, 0)), at(1, 0), at(5, 0), noMsgs, nil},
		{"clean stop and start", cat(start(at(0, 0)), stop(at(2, 0)), start(at(3, 30))), at(1, 0), at(5, 0), noMsgs,
			[]string{"not_running 02:00-03:30"}},
		{"range starts while stopped", cat(start(at(0, 0)), stop(at(0, 30)), start(at(1, 15))), at(1, 0), at(5, 0), noMsgs,
			[]string{"not_running 01:00-01:15"}},
		{"stopped at the end", cat(start(at(0, 0)), stop(at(4, 0))), at(1, 0), at(5, 0), noMsgs,
			[]string{"not_running 04:00-05:00"}},
		{"before the first start", start(at(2, 0)), at(1, 0), at(5, 0), noMsgs, []string{"not_running 01:00-02:00"}},
		{"no events at all", nil, at(1, 0), at(5, 0), noMsgs, []string{"not_running 01:00-05:00"}},
		{"crash: start without a stop", cat(start(at(0, 0)), []index.Event{ev(at(1, 10), index.KindRotate, index.LevelInfo, "closed x")},
			start(at(3, 0))), at(1, 0), at(5, 0), func(t time.Time) time.Time { return at(1, 40) },
			[]string{"not_running 01:40-03:00"}},
		{"bind error until retry", cat(start(at(0, 0))[:2], []index.Event{
			ev(at(0, 0).Add(3*time.Millisecond), index.KindBind, index.LevelError, "TCP 0.0.0.0:514: bind: address already in use (held by syslogd (pid 312))"),
			ev(at(2, 5), index.KindStart, index.LevelInfo, "TCP listener started on 0.0.0.0:514"),
		}), at(1, 0), at(5, 0), noMsgs, []string{"listener_down tcp 01:00-02:05"}},
		{"bind error until the service stops", cat(start(at(0, 0))[:2], []index.Event{
			ev(at(1, 30), index.KindBind, index.LevelError, "TCP 0.0.0.0:514: bind: address already in use"),
		}, stop(at(3, 0)), start(at(3, 10))), at(1, 0), at(5, 0), noMsgs,
			[]string{"listener_down tcp 01:30-03:00", "not_running 03:00-03:10"}},
		{"listener turned off and on", cat(start(at(0, 0)), []index.Event{
			ev(at(1, 20), index.KindStop, index.LevelInfo, "UDP listener on 0.0.0.0:514 stopped"),
			ev(at(1, 50), index.KindStart, index.LevelInfo, "UDP listener started on 0.0.0.0:514; receive buffer 8 MiB"),
		}), at(1, 0), at(5, 0), noMsgs, []string{"listener_stopped udp 01:20-01:50"}},
		{"settings blip is not a gap", cat(start(at(0, 0)), []index.Event{
			ev(at(1, 20), index.KindStop, index.LevelInfo, "UDP listener on 0.0.0.0:514 stopped"),
			ev(at(1, 20).Add(20*time.Millisecond), index.KindStart, index.LevelInfo, "UDP listener started on 0.0.0.0:5514"),
		}), at(1, 0), at(5, 0), noMsgs, nil},
		{"the future isn't a gap", cat(start(at(0, 0)), stop(at(22, 0))), at(21, 0), at(23, 59), noMsgs,
			[]string{"not_running 22:00-23:00"}},
		{"other events are ignored", cat(start(at(0, 0)), []index.Event{
			ev(at(1, 5), index.KindUI, index.LevelError, "UI server can't listen on port 8514"),
			ev(at(1, 6), index.KindExport, index.LevelInfo, "Export exp_1 queued"),
		}), at(1, 0), at(5, 0), noMsgs, nil},
	}
	for _, c := range cases {
		var got []string
		for _, g := range computeGaps(c.events, c.from, c.to, now, c.last) {
			s := g.Kind
			if g.Proto != "" {
				s += " " + g.Proto
			}
			got = append(got, fmt.Sprintf("%s %s-%s", s, g.From.Format("15:04"), g.To.Format("15:04")))
			if g.Detail == "" {
				t.Errorf("%s: gap without detail", c.name)
			}
		}
		if strings.Join(got, ", ") != strings.Join(c.want, ", ") {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
	}
}
