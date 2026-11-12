package index

import (
	"math"
	"testing"
	"time"
)

func TestRetentionInputs(t *testing.T) {
	d := openIn(t, t.TempDir())
	now := t0.Add(72 * time.Hour)
	if in, err := d.RetentionInputs(ctx, now); err != nil || in.Raw24h != 0 || !in.Earliest.IsZero() || in.GzRaw != 0 {
		t.Fatalf("empty: %+v %v", in, err)
	}
	file := func(path string, first, last time.Time, size, raw int64, gz bool) FileOp {
		return FileOp{Kind: OpUpsert, File: File{DeviceID: "192.0.2.1", Path: path, Day: first.Format("2006-01-02"),
			FirstReceived: first, LastReceived: last, Size: size, RawSize: raw, Compressed: gz}}
	}
	d.Apply(ctx, Batch{Files: []FileOp{
		// Two days ago, gzipped, wholly outside the 24 h window: only the ratio.
		file("logs/192.0.2.1/a.log.gz", now.Add(-60*time.Hour), now.Add(-48*time.Hour), 100, 1000, true),
		// Straddles the window start: half of its span is inside.
		file("logs/192.0.2.1/b.log.gz", now.Add(-30*time.Hour), now.Add(-18*time.Hour), 200, 2400, true),
		// Inside: the active file.
		file("logs/192.0.2.1/c.log", now.Add(-18*time.Hour), now, 5000, 5000, false),
	}})
	in, err := d.RetentionInputs(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(in.Raw24h-(1200+5000)) > 1 {
		t.Errorf("raw24h %v", in.Raw24h)
	}
	// a.log.gz ended before the window: some time before it.
	if !in.Earliest.Before(now.Add(-24*time.Hour)) || in.GzRaw != 3400 || in.GzSize != 300 {
		t.Errorf("%+v", in)
	}
}

func TestRetentionInputsUnderADay(t *testing.T) {
	d := openIn(t, t.TempDir())
	now := t0.Add(72 * time.Hour)
	d.Apply(ctx, Batch{Files: []FileOp{{Kind: OpUpsert, File: File{DeviceID: "192.0.2.1", Path: "logs/192.0.2.1/c.log",
		Day: "x", FirstReceived: now.Add(-3 * time.Hour), LastReceived: now, Size: 10, RawSize: 10}}}})
	in, err := d.RetentionInputs(ctx, now)
	if err != nil || !in.Earliest.Equal(now.Add(-3*time.Hour)) || in.Raw24h != 10 || in.GzSize != 0 {
		t.Fatalf("%+v %v", in, err)
	}
}

func TestLastEvent(t *testing.T) {
	d := openIn(t, t.TempDir())
	if at, err := d.LastEvent(ctx, KindClock, t0); err != nil || !at.IsZero() {
		t.Fatal(at, err)
	}
	for i, k := range []Kind{KindClock, KindClock, KindExport} {
		if err := d.AddEvent(ctx, Event{Time: t0.Add(time.Duration(i) * time.Hour), Level: LevelWarn, Kind: k, Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if at, err := d.LastEvent(ctx, KindClock, t0); err != nil || !at.Equal(t0.Add(time.Hour)) {
		t.Fatal(at, err)
	}
	if at, _ := d.LastEvent(ctx, KindClock, t0.Add(90*time.Minute)); !at.IsZero() {
		t.Fatal(at)
	}
}
