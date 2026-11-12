package server

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/index"
)

func TestEstimateRetention(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	const gib = 1 << 30
	// No data, and under an hour of it: unknown.
	if e := estimateRetention(index.RetentionInputs{}, now, 20*gib); e.ok {
		t.Errorf("no data: %+v", e)
	}
	if e := estimateRetention(index.RetentionInputs{Raw24h: 8 * gib, Earliest: now.Add(-59 * time.Minute)}, now, 20*gib); e.ok {
		t.Errorf("under 1 h: %+v", e)
	}
	var d api.Disk
	estimateRetention(index.RetentionInputs{Earliest: now.Add(-30 * time.Minute)}, now, 20*gib).apply(&d, 30)
	if d.BytesPerDay != nil || d.CompressionRatio != nil || d.EstimatedDays != nil || d.LimitedBy != nil {
		t.Errorf("unknown isn't null: %+v", d)
	}

	// Under 24 h: 6 h of data scale to a day; no gzipped file yet, ratio 8.
	e := estimateRetention(index.RetentionInputs{Raw24h: 2 * gib, Earliest: now.Add(-6 * time.Hour)}, now, 20*gib)
	if !e.ok || e.ratio != 8 || e.bytesPerDay != gib || e.days != 20 {
		t.Errorf("under 24 h: %+v", e)
	}
	d = api.Disk{}
	e.apply(&d, 30)
	if *d.BytesPerDay != gib || *d.CompressionRatio != 8 || *d.EstimatedDays != 20 || *d.LimitedBy != "cap" {
		t.Errorf("under 24 h disk: %+v", d)
	}

	// Steady: a full day (older data doesn't scale it), the measured ratio.
	in := index.RetentionInputs{Raw24h: 92 * gib, Earliest: now.Add(-10 * 24 * time.Hour), GzRaw: 92, GzSize: 10}
	e = estimateRetention(in, now, 20*gib)
	if !e.ok || e.ratio != 9.2 || e.bytesPerDay != 10*gib || e.days != 2 {
		t.Errorf("steady: %+v", e)
	}
	d = api.Disk{}
	e.apply(&d, 2)
	if *d.LimitedBy != "age" || *d.EstimatedDays != 2 {
		t.Errorf("days == retention_days is the age limit: %+v", d)
	}

	// A day with nothing received: a zero rate, the age limit decides.
	e = estimateRetention(index.RetentionInputs{Earliest: now.Add(-48 * time.Hour)}, now, 20*gib)
	d = api.Disk{}
	e.apply(&d, 30)
	if !e.ok || *d.BytesPerDay != 0 || d.EstimatedDays != nil || *d.LimitedBy != "age" {
		t.Errorf("zero rate: %+v %+v", e, d)
	}
}

func TestRetentionText(t *testing.T) {
	e := retentionEstimate{ok: true, bytesPerDay: 2361393152, ratio: 9.2, days: 9.1}
	got := retentionText(e, 20<<30, 30)
	if !strings.HasPrefix(got, "At the current rate the 20 GiB cap keeps about 9 days, less than the 30-day setting (about 2.2 GiB a day on disk).") {
		t.Errorf("%q", got)
	}
	e.days = 1.7
	if got := retentionText(e, 3<<29, 30); !strings.Contains(got, "the 1.5 GiB cap keeps about 1 day,") {
		t.Errorf("%q", got)
	}
	e.days = 0.4
	if got := retentionText(e, 1<<30, 7); !strings.Contains(got, "keeps less than a day, less than the 7-day setting") {
		t.Errorf("%q", got)
	}
}

// TestRetentionEstimateServer: /api/stats, the banner, the estimate endpoint and
// the once-a-day event on a running server.
func TestRetentionEstimateServer(t *testing.T) {
	dir := t.TempDir()
	const gib = 1 << 30
	r := start(t, writeConfig(t, dir, freePort(t), 0, func(c *config.Config) {
		c.Storage.RetentionBytes = 20 * gib
		c.Storage.RetentionDays = 30
	}))
	be := r.srv.be
	ctx := context.Background()
	reset := func() { be.retention.mu.Lock(); be.retention.at = time.Time{}; be.retention.mu.Unlock() }

	// No data yet: all null, no banner.
	reset()
	var stats api.Stats
	r.do(t, "GET", "/api/stats", nil, &stats)
	if stats.Disk.BytesPerDay != nil || stats.Disk.LimitedBy != nil {
		t.Errorf("no data: %+v", stats.Disk)
	}

	// A steady day at 2.2 GiB a day on disk (ratio 9.2): the 20 GiB cap keeps ~9.1 days.
	now := time.Now().UTC()
	bpd := int64(2361393152)
	files := []index.FileOp{
		{Kind: index.OpUpsert, File: index.File{DeviceID: "192.0.2.1", Path: "logs/192.0.2.1/old.log.gz", Day: "d1",
			FirstReceived: now.Add(-72 * time.Hour), LastReceived: now.Add(-48 * time.Hour), Size: 10 << 20, RawSize: 92 << 20, Compressed: true}},
		{Kind: index.OpUpsert, File: index.File{DeviceID: "192.0.2.1", Path: "logs/192.0.2.1/now.log", Day: "d2",
			FirstReceived: now.Add(-24 * time.Hour), LastReceived: now, Size: bpd * 92 / 10, RawSize: bpd * 92 / 10}},
	}
	if err := r.srv.idx.Apply(ctx, index.Batch{Files: files}); err != nil {
		t.Fatal(err)
	}
	reset()
	r.do(t, "GET", "/api/stats", nil, &stats)
	d := stats.Disk
	if d.BytesPerDay == nil || math.Abs(float64(*d.BytesPerDay-bpd)) > float64(bpd)/100 || *d.CompressionRatio != 9.2 ||
		math.Abs(*d.EstimatedDays-9.1) > 0.1 || *d.LimitedBy != "cap" {
		t.Fatalf("steady: %+v", d)
	}
	var st api.Status
	r.do(t, "GET", "/api/status", nil, &st)
	var ban *api.Banner
	for i := range st.Banners {
		if st.Banners[i].Kind == "retention_cap" {
			ban = &st.Banners[i]
		}
	}
	if ban == nil || ban.Level != api.LevelWarn || !strings.Contains(ban.Text, "the 20 GiB cap keeps about 9 days, less than the 30-day setting") {
		t.Fatalf("banner: %+v", st.Banners)
	}

	// The Settings preview: 40 GiB keeps ~18 days; under 1 GiB is refused.
	var est api.StorageEstimate
	if code := r.do(t, "GET", "/api/storage/estimate?retention_bytes=42949672960", nil, &est); code != 200 ||
		est.EstimatedDays == nil || math.Abs(*est.EstimatedDays-18.2) > 0.2 || *est.CompressionRatio != 9.2 {
		t.Fatalf("estimate: %d %+v", code, est)
	}
	var e struct{ Error string }
	if code := r.do(t, "GET", "/api/storage/estimate?retention_bytes=1000", nil, &e); code != 400 || !strings.Contains(e.Error, "at least 1 GiB") {
		t.Errorf("under 1 GiB: %d %q", code, e.Error)
	}

	// The event: once (the watcher's start-up check may have run before the data).
	for range 3 {
		if err := be.checkRetention(ctx); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := r.srv.idx.Events(ctx, now.Add(-time.Hour), time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind == index.KindRetentionCap {
			n++
			if ev.Level != index.LevelWarn || ev.Text != ban.Text {
				t.Errorf("event %+v", ev)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d retention_cap events, want 1", n)
	}

	// Raising the cap past the age limit clears the banner.
	var set api.Settings
	r.do(t, "GET", "/api/settings", nil, &set)
	set.Storage.RetentionBytes = 100 * gib
	if code := r.do(t, "PUT", "/api/settings", set, nil); code != 200 {
		t.Fatalf("PUT %d", code)
	}
	st = api.Status{}
	r.do(t, "GET", "/api/status", nil, &st)
	for _, b := range st.Banners {
		if b.Kind == "retention_cap" {
			t.Errorf("banner after raising the cap: %+v", b)
		}
	}
	r.do(t, "GET", "/api/stats", nil, &stats)
	if stats.Disk.LimitedBy == nil || *stats.Disk.LimitedBy != "age" {
		t.Errorf("after raising the cap: %+v", stats.Disk)
	}
}
