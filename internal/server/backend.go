package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/export"
	"github.com/gmccollu/woodpile/internal/hub"
	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/listen"
	"github.com/gmccollu/woodpile/internal/model"
	"github.com/gmccollu/woodpile/internal/search"
	"github.com/gmccollu/woodpile/internal/store"
)

// backend implements api.Backend on top of the real packages.
type backend struct {
	// startupBanners are shown for the whole run (a data folder others can read).
	startupBanners []api.Banner

	// isService is set when this process is the installed service (it never
	// reports itself as a peer); peers caches the lookup of another Woodpile;
	// uiListening reports whether this process's UI is up.
	isService   bool
	peers       peerCache
	uiListening func() bool

	cfgPath string
	idx     *index.DB
	store   *store.Store
	hub     *hub.Hub
	search  *search.Engine
	exports *export.Manager
	log     *slog.Logger
	version string
	commit  string
	release string
	now     func() time.Time

	startedAt time.Time

	// ln is set once the listeners start (nil before Run).
	ln atomic.Pointer[listen.Server]

	cfgMu sync.RWMutex
	cfg   config.Config

	// saveMu serializes settings saves (validate, save, apply) end to end.
	saveMu sync.Mutex

	// groups maps device ID to group for the hub's GroupOf (a lock-free read on
	// the ingest path). Refreshed at start and after every PATCH.
	groups atomic.Pointer[map[string]string]
	// hidden holds the hidden devices, for the hub's HiddenOf; refreshed with
	// groups.
	hidden atomic.Pointer[map[string]bool]
	// cacheMu serializes updates of groups and hidden (setCaches, refreshGroups).
	cacheMu sync.Mutex

	// firstMsg is the receive time of the first message since start (unix ms; 0
	// until then), for first_message_at before the store's first index update.
	firstMsg atomic.Int64
	// firstSeen caches the earliest device first_seen once the index has one.
	firstSeen atomic.Int64

	// live has each sender's last-heard time and rate since start, without the
	// index's lag.
	live *liveDevices

	// hostIPs is cached briefly: interface enumeration isn't free.
	ipMu     sync.Mutex
	ipCache  []string
	ipCached time.Time
}

type backendDeps struct {
	cfgPath                  string
	cfg                      config.Config
	idx                      *index.DB
	store                    *store.Store
	log                      *slog.Logger
	version, commit, release string
	search                   *search.Engine
	exports                  *export.Manager
}

func newBackend(d backendDeps) *backend {
	b := &backend{
		cfgPath: d.cfgPath,
		cfg:     d.cfg.Clone(),
		idx:     d.idx,
		store:   d.store,
		log:     d.log,
		version: d.version,
		commit:  d.commit,
		release: d.release,
		search:  d.search,
		exports: d.exports,
		now:     time.Now,
		live:    newLiveDevices(),
	}
	empty := map[string]string{}
	b.groups.Store(&empty)
	none := map[string]bool{}
	b.hidden.Store(&none)
	return b
}

var _ api.Backend = (*backend)(nil)

func (b *backend) config() config.Config {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	return b.cfg.Clone()
}

func (b *backend) setListeners(ln *listen.Server) { b.ln.Store(ln) }

// sink receives every accepted message from the listeners: store it, then
// offer it to the live tail. The listener allocates a fresh Raw per message,
// so both can keep it.
func (b *backend) sink(m model.Message) {
	if b.firstMsg.Load() == 0 {
		b.firstMsg.CompareAndSwap(0, m.Received.UnixMilli())
	}
	b.live.add(m.Sender, m.Received)
	// The store counts what it drops (writes paused, write errors) itself.
	_ = b.store.Write(m)
	b.hub.Publish(m)
}

// listenEvent records a listener start, stop or bind event in the event log.
func (b *backend) listenEvent(e listen.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.idx.AddEvent(ctx, index.Event{
		Time: e.Time, Level: index.Level(e.Level), Kind: index.Kind(e.Kind), Text: e.Text, Source: index.ListenerSource(e.Proto),
	}); err != nil {
		b.log.Warn("couldn't record a listener event", "err", err, "text", e.Text)
	}
}

// serviceEvent records the service's start or stop (index.SourceService).
func (b *backend) serviceEvent(level index.Level, kind index.Kind, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.idx.AddEvent(ctx, index.Event{Level: level, Kind: kind, Text: text, Source: index.SourceService}); err != nil {
		b.log.Warn("couldn't record an event", "err", err, "text", text)
	}
}

// groupOf is the hub's GroupOf: a map lookup, no locks.
func (b *backend) groupOf(id string) string { return (*b.groups.Load())[id] }

func (b *backend) refreshGroups(ctx context.Context) error {
	ds, err := b.listDevices(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]string, len(ds))
	hid := map[string]bool{}
	for _, d := range ds {
		if d.Group != "" {
			m[d.ID] = d.Group
		}
		if d.Hidden {
			hid[d.ID] = true
		}
	}
	b.cacheMu.Lock()
	b.groups.Store(&m)
	b.hidden.Store(&hid)
	b.cacheMu.Unlock()
	return nil
}

// setCaches updates one device in the live tail's group and hidden caches
// (copy on write, so the hub's lock-free reads never see a half-built map),
// instead of re-reading every device on each PATCH.
func (b *backend) setCaches(d index.Device) {
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	groups := maps.Clone(*b.groups.Load())
	if d.Group != "" {
		groups[d.ID] = d.Group
	} else {
		delete(groups, d.ID)
	}
	hidden := maps.Clone(*b.hidden.Load())
	if d.Hidden {
		hidden[d.ID] = true
	} else {
		delete(hidden, d.ID)
	}
	b.groups.Store(&groups)
	b.hidden.Store(&hidden)
}

// hiddenOf is the hub's HiddenOf: a map lookup, no locks.
func (b *backend) hiddenOf(id string) bool { return (*b.hidden.Load())[id] }

// ---- api.Backend ----

func (b *backend) Status(ctx context.Context) (api.Status, error) {
	cfg := b.config()
	st := api.Status{
		Version:     b.version,
		Build:       b.commit,
		OS:          runtime.GOOS + "/" + runtime.GOARCH,
		DataDir:     cfg.Storage.DataDir,
		StartedAt:   b.startedAt,
		ReleasesURL: b.release,
		HostIPs:     b.hostIPs(),
	}
	first, err := b.firstMessageAt(ctx)
	if err != nil {
		return st, err
	}
	st.FirstMessageAt = first

	var ls listen.Status
	if ln := b.ln.Load(); ln != nil {
		ls = ln.Status()
	}
	st.UDPRcvbufBytes = int64(ls.UDPRcvbufBytes)
	st.UDPRcvbufRequestedBytes = int64(ls.UDPRcvbufRequestedBytes)
	if st.UDPRcvbufRequestedBytes == 0 && cfg.Listeners.UDP.Enabled {
		st.UDPRcvbufRequestedBytes = int64(cfg.Listeners.UDPRcvbufBytes)
	}
	st.KernelDropsVisible = ls.KernelDropsVisible
	for _, l := range ls.Listeners {
		st.Listeners = append(st.Listeners, api.Listener{
			Proto: l.Proto, Addr: l.Addr, Port: l.Port, OK: l.OK, Error: l.Error, Holder: l.Holder,
		})
	}
	st.Banners = append(buildBanners(ls.Listeners, b.store.Usage(), cfg.Storage.DataDir), b.startupBanners...)
	// Another Woodpile on this machine (usually the installed service): name it
	// as the holder and say what to do.
	if !b.isService {
		p, ok := b.peer(ctx)
		annotateHolders(st.Listeners, p, ok)
		if pb := peerBanners(st.Listeners, p, ok, cfg.UI.Port, b.uiListening()); len(pb) > 0 {
			if pb[0].Kind == "listener_bind" {
				st.Banners = slices.DeleteFunc(st.Banners, func(x api.Banner) bool { return x.Kind == "listener_bind" })
			}
			st.Banners = append(st.Banners, pb...)
		}
	}
	return st, nil
}

// firstMessageAt is the earliest device first_seen in the index, or the first
// message since start when the index has none yet, or nil.
func (b *backend) firstMessageAt(ctx context.Context) (*time.Time, error) {
	if ms := b.firstSeen.Load(); ms != 0 {
		t := time.UnixMilli(ms).UTC()
		return &t, nil
	}
	ds, err := b.listDevices(ctx)
	if err != nil {
		return nil, err
	}
	var first time.Time
	for _, d := range ds {
		if !d.FirstSeen.IsZero() && (first.IsZero() || d.FirstSeen.Before(first)) {
			first = d.FirstSeen
		}
	}
	if !first.IsZero() {
		b.firstSeen.Store(first.UnixMilli())
		t := first.UTC()
		return &t, nil
	}
	if ms := b.firstMsg.Load(); ms != 0 {
		t := time.UnixMilli(ms).UTC()
		return &t, nil
	}
	return nil, nil
}

// buildBanners turns the listener and storage state into the UI's banners. The
// api package sorts them (errors first).
func buildBanners(ls []listen.ListenerStatus, u store.Usage, dataDir string) []api.Banner {
	var out []api.Banner
	var down []string
	inUse := false
	for _, l := range ls {
		if l.OK {
			continue
		}
		down = append(down, fmt.Sprintf("%s %d", strings.ToUpper(l.Proto), l.Port))
		if isAddrInUse(l.Error) {
			inUse = true
		}
	}
	if len(down) > 0 {
		var text string
		what := strings.Join(down, " and ")
		verb := "isn't"
		if len(down) > 1 {
			verb = "aren't"
		}
		if inUse {
			text = fmt.Sprintf("%s %s listening: another program is using the port. Stop it, or pick another port in Settings, then press Retry.", what, verb)
		} else {
			text = fmt.Sprintf("%s %s listening. Fix the cause below, or change the listener in Settings, then press Retry.", what, verb)
		}
		out = append(out, api.Banner{Kind: "listener_bind", Level: api.LevelError, Text: text, Action: "retry_listeners"})
	}
	if u.WritesPaused {
		text := fmt.Sprintf("Writing is paused: the disk holding %s is nearly full (%s free) and there are no old logs left to delete. "+
			"New messages are being dropped (%d so far). Free up space on that disk.", dataDir, formatBytes(int64(u.FreeBytes)), u.Dropped)
		out = append(out, api.Banner{Kind: "writes_paused", Level: api.LevelError, Text: text})
	} else if u.VolumeBytes > 0 && u.FreeBytes < u.ReserveBytes {
		text := fmt.Sprintf("Disk space is low: %s free on the disk holding %s. Woodpile is deleting the oldest logs to keep %s free.",
			formatBytes(int64(u.FreeBytes)), dataDir, formatBytes(int64(u.ReserveBytes)))
		out = append(out, api.Banner{Kind: "low_disk", Level: api.LevelWarn, Text: text})
	}
	if u.DeviceLimited {
		why := fmt.Sprintf("the limit of %d active devices was reached", u.MaxDevices)
		switch u.DeviceLimitReason {
		case "rate":
			why = "too many new devices appeared at once (the new-device rate limit)"
		case "prefix":
			why = "too many new devices appeared from one network (the per-network new-device limit)"
		}
		text := fmt.Sprintf("New senders are being refused: %s. Their messages are counted as dropped (%d so far). "+
			"Raise the limits in Settings → Listeners if these devices are expected.", why, u.RefusedMsgs)
		out = append(out, api.Banner{Kind: "device_limit", Level: api.LevelWarn, Text: text})
	}
	return out
}

func isAddrInUse(e string) bool {
	e = strings.ToLower(e)
	return strings.Contains(e, "address already in use") || strings.Contains(e, "only one usage of each socket address") ||
		strings.Contains(e, "in use")
}

// formatBytes is a short binary-unit size ("1.5 GiB").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (b *backend) Stats(ctx context.Context) (api.Stats, error) {
	now := b.now().UTC()
	var st api.Stats
	st.MsgsPerSec = b.hub.Rate()

	// Sparkline: 60 points, msgs/s. The last 59 whole minutes from the index,
	// then the current rate, so the line ends where the msg/s card is (the
	// current minute's bucket is still filling).
	end := now.Truncate(time.Minute)
	series, err := b.idx.MinuteSeries(ctx, nil, end.Add(-59*time.Minute), end)
	if err != nil {
		return st, err
	}
	st.Sparkline = make([]float64, 0, len(series)+1)
	for _, m := range series {
		st.Sparkline = append(st.Sparkline, float64(m.Msgs)/60)
	}
	st.Sparkline = append(st.Sparkline, st.MsgsPerSec)

	// Device statuses, counted in SQL from the index's last-heard times (which
	// trail the live ones by the store's index interval, a few seconds).
	q := b.config().Quiet
	on, quiet, off, err := b.idx.StatusCounts(ctx, now.Add(-time.Duration(q.QuietAfterSeconds)*time.Second),
		now.Add(-time.Duration(q.OfflineAfterSeconds)*time.Second))
	if err != nil {
		return st, err
	}
	// Blocked devices count in none of them.
	onlineSince := now.Add(-time.Duration(q.QuietAfterSeconds) * time.Second).UnixMilli()
	quietSince := now.Add(-time.Duration(q.OfflineAfterSeconds) * time.Second).UnixMilli()
	bOn, bQuiet, bOff, err := b.blockedCounts(ctx, b.blocker(), onlineSince, quietSince)
	if err != nil {
		return st, err
	}
	on, quiet, off = on-bOn, quiet-bQuiet, off-bOff
	st.Devices = api.DeviceCounts{Total: on + quiet + off, Online: on, Quiet: quiet, Offline: off}

	u := b.store.Usage()
	st.Disk = api.Disk{UsedBytes: u.BytesOnDisk, CapBytes: u.CapBytes, FreeBytes: int64(u.FreeBytes), DaysKept: u.DaysKept}
	if u.Files == 0 {
		st.Disk.DaysKept = 0
	}

	// Only buckets with drops or truncations are read (a day of per-minute
	// buckets for 10,000 devices is 14 million rows).
	if st.Truncated24h, st.Dropped24h, err = b.idx.DropCounts(ctx, now.Add(-24*time.Hour)); err != nil {
		return st, err
	}
	if ln := b.ln.Load(); ln != nil {
		st.Rejected24h = ln.Rejected24h()
		if n, ok := ln.KernelDropped24h(); ok {
			st.KernelDropped24h = &n
		}
	}
	return st, nil
}

func (b *backend) Rejected(ctx context.Context) ([]api.RejectedSender, error) {
	ln := b.ln.Load()
	if ln == nil {
		return []api.RejectedSender{}, nil
	}
	rs := ln.Rejected()
	out := make([]api.RejectedSender, len(rs))
	for i, r := range rs {
		out[i] = api.RejectedSender{IP: r.IP, Count24h: r.Count24h, LastSeen: r.LastSeen, Reason: r.Reason}
	}
	return out, nil
}

// Device statuses (docs/api.md).
const (
	statusOnline  = "online"
	statusQuiet   = "quiet"
	statusOffline = "offline"
	statusBlocked = "blocked" // on the blocklist (whatever its last-heard time)
)

// deviceStatus is online when heard within quiet_after_seconds, quiet up to
// offline_after_seconds, offline after that (or never heard).
func deviceStatus(lastHeard, now time.Time, q config.Quiet) string {
	if lastHeard.IsZero() {
		return statusOffline
	}
	age := now.Sub(lastHeard)
	switch {
	case age <= time.Duration(q.QuietAfterSeconds)*time.Second:
		return statusOnline
	case age <= time.Duration(q.OfflineAfterSeconds)*time.Second:
		return statusQuiet
	}
	return statusOffline
}

// listDevices is the index's device list without index.RefusedDeviceID (the
// stats bucket of refused new senders, not a device).
func (b *backend) listDevices(ctx context.Context) ([]index.Device, error) {
	ds, err := b.idx.ListDevices(ctx)
	return slices.DeleteFunc(ds, func(d index.Device) bool { return d.ID == index.RefusedDeviceID }), err
}

func (b *backend) Devices(ctx context.Context) ([]api.Device, error) {
	ds, err := b.listDevices(ctx)
	if err != nil {
		return nil, err
	}
	now := b.now()
	rates, err := b.deviceRates(ctx, now)
	if err != nil {
		return nil, err
	}
	q := b.config().Quiet
	live := b.live.snapshot(now)
	out := make([]api.Device, len(ds))
	for i, d := range ds {
		out[i] = withLive(d, live, rates, now, q)
	}
	b.blocker().mark(out)
	return out, nil
}

// withLive makes the API device from the index's row, with the live last-heard
// time and rate when the device has sent since start (the index's otherwise).
func withLive(d index.Device, live map[string]liveStat, rs rates, now time.Time, q config.Quiet) api.Device {
	rate := rs.perMinute(d, now)
	if l, ok := live[d.ID]; ok {
		if l.LastHeard.After(d.LastHeard) {
			d.LastHeard = l.LastHeard
		}
		rate = l.MsgsPerMin
	}
	return toAPIDevice(d, rate, now, q)
}

func toAPIDevice(d index.Device, rate int64, now time.Time, q config.Quiet) api.Device {
	return api.Device{
		ID: d.ID, IP: d.IP, Name: d.Name, Group: d.Group, Hidden: d.Hidden,
		FirstSeen: d.FirstSeen, LastHeard: d.LastHeard,
		Status:      deviceStatus(d.LastHeard, now, q),
		MsgsPerMin:  rate,
		TotalMsgs:   d.TotalMsgs,
		BytesOnDisk: d.BytesOnDisk,
	}
}

// rates holds each device's message count since from (the start of the
// previous whole minute).
type rates struct {
	from   time.Time
	counts map[string]index.Counts
}

// deviceRates reads the counts for msgs_per_min: the previous whole minute plus
// the current partial one. Minute buckets are exact; the current one fills as
// the store flushes (every second).
func (b *backend) deviceRates(ctx context.Context, now time.Time) (rates, error) {
	from := now.UTC().Truncate(time.Minute).Add(-time.Minute)
	counts, err := b.idx.DeviceCounts(ctx, from, time.Time{})
	delete(counts, index.RefusedDeviceID)
	return rates{from: from, counts: counts}, err
}

// minRateWindow keeps a device that just appeared from showing a huge rate off
// its first message or two.
const minRateWindow = 10 * time.Second

// perMinute scales a device's count to a minute over the window it was actually
// sending in: since from, or since its first message if that's later.
func (r rates) perMinute(d index.Device, now time.Time) int64 {
	c, ok := r.counts[d.ID]
	if !ok || c.Msgs == 0 {
		return 0
	}
	start := r.from
	if d.FirstSeen.After(start) {
		start = d.FirstSeen
	}
	window := max(now.Sub(start), minRateWindow)
	return int64(float64(c.Msgs)*float64(time.Minute)/float64(window) + 0.5)
}

func (b *backend) UpdateDevice(ctx context.Context, id string, p api.DevicePatch) (api.Device, error) {
	d, err := b.idx.UpdateDevice(ctx, id, index.DeviceUpdate{Name: p.Name, Group: p.Group, Hidden: p.Hidden})
	if errors.Is(err, index.ErrNotFound) {
		return api.Device{}, fmt.Errorf("device %q: %w", id, api.ErrNotFound)
	}
	if err != nil {
		return api.Device{}, err
	}
	b.setCaches(d)
	var blockedBy string
	if p.Blocked != nil {
		if blockedBy, err = b.setBlocked(ctx, d, *p.Blocked); err != nil {
			return api.Device{}, err
		}
	}
	now := b.now()
	from := now.UTC().Truncate(time.Minute).Add(-time.Minute)
	c, err := b.idx.Counts(ctx, []string{d.ID}, from, time.Time{})
	if err != nil {
		return api.Device{}, err
	}
	rs := rates{from: from, counts: map[string]index.Counts{d.ID: c}}
	out := []api.Device{withLive(d, b.live.subset([]string{d.ID}, now), rs, now, b.config().Quiet)}
	b.blocker().mark(out)
	out[0].BlockedBy = blockedBy
	return out[0], nil
}

func (b *backend) Settings(ctx context.Context) (api.Settings, error) {
	return toAPISettings(b.config())
}

// toAPISettings converts through JSON: config.Config's JSON form is exactly the
// settings object (file-only fields are json:"-").
func toAPISettings(c config.Config) (api.Settings, error) {
	var s api.Settings
	raw, err := json.Marshal(c)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	return s, err
}

// SaveSettings unmarshals the client's object onto a copy of the config in
// effect (so the file-only fields survive), normalizes and validates it, saves
// it, and applies it live: listeners through Reconfigure, retention through
// SetLimits, quiet thresholds on the next read. The api package applies UI
// changes itself.
func (b *backend) SaveSettings(ctx context.Context, in api.Settings) (api.Settings, error) {
	b.saveMu.Lock()
	defer b.saveMu.Unlock()
	cur := b.config()
	next := cur.Clone()
	raw, err := json.Marshal(in)
	if err != nil {
		return api.Settings{}, err
	}
	next.Listeners.Allowlist = nil // replaced, not merged
	if err := json.Unmarshal(raw, &next); err != nil {
		return api.Settings{}, err
	}
	next.Normalize()
	if errs := next.Validate(&cur); len(errs) > 0 {
		return api.Settings{}, &api.ValidationError{Fields: errs}
	}
	if err := config.Save(b.cfgPath, next); err != nil {
		return api.Settings{}, fmt.Errorf("save the config: %w", err)
	}
	b.cfgMu.Lock()
	b.cfg = next.Clone()
	b.cfgMu.Unlock()

	if ln := b.ln.Load(); ln != nil && !listenersEqual(cur.Listeners, next.Listeners) {
		if err := ln.Reconfigure(next.Listeners); err != nil {
			// Validate already checked the allowlist, so this shouldn't happen.
			return api.Settings{}, fmt.Errorf("apply the listener settings: %w", err)
		}
	}
	if cur.Storage != next.Storage || cur.Listeners.MaxDevices != next.Listeners.MaxDevices ||
		cur.Listeners.NewDeviceBurst != next.Listeners.NewDeviceBurst || cur.Listeners.NewDevicesPerMinute != next.Listeners.NewDevicesPerMinute {
		b.store.SetLimits(store.Limits{
			RetentionDays:       next.Storage.RetentionDays,
			RetentionBytes:      next.Storage.RetentionBytes,
			PartBytes:           next.Storage.PartBytes,
			MaxDevices:          next.Listeners.MaxDevices,
			NewDeviceBurst:      next.Listeners.NewDeviceBurst,
			NewDevicesPerMinute: float64(next.Listeners.NewDevicesPerMinute),
		})
	}
	b.log.Info("settings saved", "path", b.cfgPath)
	return toAPISettings(next)
}

func listenersEqual(a, b config.Listeners) bool {
	if a.UDP != b.UDP || a.TCP != b.TCP || a.UDPRcvbufBytes != b.UDPRcvbufBytes || a.MaxMessageBytes != b.MaxMessageBytes {
		return false
	}
	return slices.Equal(a.Allowlist, b.Allowlist) && slices.Equal(a.Blocklist, b.Blocklist)
}

func (b *backend) RetryListeners(ctx context.Context) error {
	if ln := b.ln.Load(); ln != nil {
		ln.Retry()
	}
	return nil
}

// maxHistory caps GET /api/storage/history.
const maxHistory = 1000

func (b *backend) StorageHistory(ctx context.Context) ([]api.StorageEntry, error) {
	ds, err := b.idx.Deletions(ctx, time.Time{}, time.Time{}, maxHistory)
	if err != nil {
		return nil, err
	}
	out := make([]api.StorageEntry, len(ds))
	for i, d := range ds {
		out[i] = api.StorageEntry{Time: d.Time, File: d.File, DeviceID: d.DeviceID, Bytes: d.Bytes, Reason: string(d.Reason)}
	}
	return out, nil
}

func (b *backend) LiveHandler() http.Handler { return http.HandlerFunc(b.hub.ServeSSE) }

func (b *backend) RecordEvent(ctx context.Context, e api.Event) error {
	kind := index.Kind(e.Kind)
	switch e.Kind {
	case api.EventKindUI:
		kind = index.KindUI
	case api.EventKindAuth:
		kind = index.KindAuth
	}
	level := index.Level(e.Level)
	switch level {
	case index.LevelInfo, index.LevelWarn, index.LevelError:
	default:
		level = index.LevelInfo
	}
	// The api package logs its own events; this only stores them.
	return b.idx.AddEvent(ctx, index.Event{Level: level, Kind: kind, Text: e.Text})
}
