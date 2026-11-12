package server

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/index"
)

const (
	// defaultCompressionRatio is used until this server has gzipped a file.
	defaultCompressionRatio = 8
	// retentionMinData is how much data the estimate needs before it says anything.
	retentionMinData = time.Hour
	// retentionCacheFor is how long the index inputs are reused (the dashboard
	// polls /api/stats every few seconds).
	retentionCacheFor = time.Minute
	// retentionCheckEvery is how often the server checks whether to record the
	// once-a-day retention_cap event.
	retentionCheckEvery = time.Hour
	// retentionEventEvery is the spacing of retention_cap events.
	retentionEventEvery = 24 * time.Hour
)

// retentionCache holds the index inputs of the estimate for retentionCacheFor.
type retentionCache struct {
	mu sync.Mutex
	at time.Time
	in index.RetentionInputs
}

// retentionEstimate is the estimate for one cap. ok is false under an hour of
// data: everything is unknown.
type retentionEstimate struct {
	ok          bool
	bytesPerDay int64
	ratio       float64
	days        float64 // capBytes / bytesPerDay; +Inf at a zero rate
}

// estimateRetention turns the index inputs into the estimate for capBytes at now.
func estimateRetention(in index.RetentionInputs, now time.Time, capBytes int64) retentionEstimate {
	if in.Earliest.IsZero() {
		return retentionEstimate{}
	}
	span := min(now.Sub(in.Earliest), 24*time.Hour)
	if span < retentionMinData {
		return retentionEstimate{}
	}
	ratio := float64(defaultCompressionRatio)
	if in.GzRaw > 0 && in.GzSize > 0 {
		ratio = float64(in.GzRaw) / float64(in.GzSize)
	}
	raw := in.Raw24h * float64(24*time.Hour) / float64(span)
	e := retentionEstimate{ok: true, ratio: ratio, bytesPerDay: int64(math.Round(raw / ratio))}
	e.days = math.Inf(1)
	if e.bytesPerDay > 0 {
		e.days = float64(capBytes) / float64(e.bytesPerDay)
	}
	return e
}

// limitedByCap reports whether the cap, not the age limit, decides how long logs
// are kept.
func (e retentionEstimate) limitedByCap(retentionDays int) bool {
	return e.ok && e.days < float64(retentionDays)
}

// apply fills the estimate fields of d (nil when unknown).
func (e retentionEstimate) apply(d *api.Disk, retentionDays int) {
	s := e.storage()
	d.BytesPerDay, d.CompressionRatio, d.EstimatedDays = s.BytesPerDay, s.CompressionRatio, s.EstimatedDays
	if !e.ok {
		return
	}
	lim := "age"
	if e.limitedByCap(retentionDays) {
		lim = "cap"
	}
	d.LimitedBy = &lim
}

func (e retentionEstimate) storage() api.StorageEstimate {
	var s api.StorageEstimate
	if !e.ok {
		return s
	}
	bpd, ratio := e.bytesPerDay, round(e.ratio, 1)
	s.BytesPerDay, s.CompressionRatio = &bpd, &ratio
	if !math.IsInf(e.days, 1) {
		d := round(e.days, 2)
		s.EstimatedDays = &d
	}
	return s
}

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}

// retentionInputs reads the index inputs, reusing them for retentionCacheFor.
func (b *backend) retentionInputs(ctx context.Context, now time.Time) (index.RetentionInputs, error) {
	c := &b.retention
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && now.Sub(c.at) < retentionCacheFor && !now.Before(c.at) {
		return c.in, nil
	}
	in, err := b.idx.RetentionInputs(ctx, now)
	if err != nil {
		return in, err
	}
	c.at, c.in = now, in
	return in, nil
}

// estimate is the retention estimate for capBytes now.
func (b *backend) estimate(ctx context.Context, capBytes int64) (retentionEstimate, error) {
	now := b.now()
	in, err := b.retentionInputs(ctx, now)
	if err != nil {
		return retentionEstimate{}, err
	}
	return estimateRetention(in, now, capBytes), nil
}

// StorageEstimate serves GET /api/storage/estimate. The cap is validated like the
// setting.
func (b *backend) StorageEstimate(ctx context.Context, retentionBytes int64) (api.StorageEstimate, error) {
	cur := b.config()
	next := cur.Clone()
	next.Storage.RetentionBytes = retentionBytes
	if msg, ok := next.Validate(&cur)["storage.retention_bytes"]; ok {
		return api.StorageEstimate{}, &api.RequestError{Msg: "retention_bytes " + msg}
	}
	e, err := b.estimate(ctx, retentionBytes)
	if err != nil {
		return api.StorageEstimate{}, err
	}
	return e.storage(), nil
}

// retentionText is the banner and event text.
func retentionText(e retentionEstimate, capBytes int64, retentionDays int) string {
	keeps := "less than a day"
	if d := int(math.Floor(e.days)); d == 1 {
		keeps = "about 1 day"
	} else if d > 1 {
		keeps = fmt.Sprintf("about %d days", d)
	}
	return fmt.Sprintf("At the current rate the %s cap keeps %s, less than the %d-day setting (about %s a day on disk). "+
		"Raise the size cap in Settings → Storage to keep logs for the full %d days.",
		shortBytes(capBytes), keeps, retentionDays, shortBytes(e.bytesPerDay), retentionDays)
}

// shortBytes is formatBytes without a trailing ".0" ("20 GiB", "1.5 GiB").
func shortBytes(n int64) string {
	s := formatBytes(n)
	if i := strings.Index(s, ".0 "); i >= 0 {
		s = s[:i] + s[i+2:]
	}
	return s
}

// retentionBanner is the retention_cap warn banner, or nil when the age limit
// decides (or nothing is known yet).
func (b *backend) retentionBanner(ctx context.Context, capBytes int64, retentionDays int) (*api.Banner, error) {
	e, err := b.estimate(ctx, capBytes)
	if err != nil || !e.limitedByCap(retentionDays) {
		return nil, err
	}
	return &api.Banner{Kind: "retention_cap", Level: api.LevelWarn, Text: retentionText(e, capBytes, retentionDays)}, nil
}

// checkRetention records a retention_cap warn event while the cap decides,
// at most once every retentionEventEvery (checked against the event log, so a
// restart doesn't repeat it).
func (b *backend) checkRetention(ctx context.Context) error {
	cfg := b.config()
	ban, err := b.retentionBanner(ctx, cfg.Storage.RetentionBytes, cfg.Storage.RetentionDays)
	if err != nil || ban == nil {
		return err
	}
	now := b.now()
	last, err := b.idx.LastEvent(ctx, index.KindRetentionCap, now.Add(-retentionEventEvery))
	if err != nil || !last.IsZero() {
		return err
	}
	return b.idx.AddEvent(ctx, index.Event{Time: now, Level: index.LevelWarn, Kind: index.KindRetentionCap, Text: ban.Text})
}

// watchRetention runs checkRetention at start and every retentionCheckEvery
// until stop closes.
func (b *backend) watchRetention(stop <-chan struct{}) {
	t := time.NewTicker(retentionCheckEvery)
	defer t.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := b.checkRetention(ctx); err != nil {
			b.log.Warn("retention check failed", "err", err)
		}
		cancel()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
