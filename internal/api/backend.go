// Package api is Woodpile's HTTP layer: the REST API in docs/api.md, the embedded
// web UI, and the UI's security (localhost mode, LAN mode with a password and
// HTTPS).
//
// The handlers talk to the rest of the service only through the Backend
// interface, so this package doesn't depend on the listeners, the live hub or
// the config. All JSON shapes below follow docs/api.md field for field.
package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"time"
)

// Backend is everything the handlers need from the running service. Each method
// names the route it serves. Methods may be called concurrently.
type Backend interface {
	// Status serves GET /api/status.
	Status(ctx context.Context) (Status, error)

	// Stats serves GET /api/stats.
	Stats(ctx context.Context) (Stats, error)

	// Rejected serves GET /api/rejected: senders refused by the allowlist.
	Rejected(ctx context.Context) ([]RejectedSender, error)

	// Devices serves GET /api/devices: every device, hidden ones included; this
	// package filters, sorts and pages them (DeviceQuery).
	Devices(ctx context.Context) ([]Device, error)

	// UpdateDevice serves PATCH /api/devices/{id}. Nil fields in p are left
	// alone. It returns the updated device, or an error wrapping ErrNotFound for
	// an unknown id.
	UpdateDevice(ctx context.Context, id string, p DevicePatch) (Device, error)

	// Settings serves GET /api/settings.
	Settings(ctx context.Context) (Settings, error)

	// SaveSettings serves PUT /api/settings. It validates the whole object and,
	// if it's valid, saves and applies it and returns the saved object. If it
	// isn't, it returns a *ValidationError (and saves nothing); its Fields go
	// to the client unchanged as {"errors": {...}}. A change to UI is applied by
	// this package, not the backend: it restarts the HTTP server after the
	// response is sent.
	SaveSettings(ctx context.Context, s Settings) (Settings, error)

	// RetryListeners serves POST /api/listeners/retry: rebind any syslog
	// listener that failed to bind. The client re-reads /api/status afterwards,
	// so a bind that fails again is not an error here.
	RetryListeners(ctx context.Context) error

	// StorageHistory serves GET /api/storage/history, newest first.
	StorageHistory(ctx context.Context) ([]StorageEntry, error)

	// LiveHandler serves GET /api/live (text/event-stream). This package
	// authenticates the request and adds the security headers, then hands it
	// over unchanged, query string included. The handler must return when
	// r.Context() is done: that is how a UI restart ends open streams.
	LiveHandler() http.Handler

	// Search serves GET /api/search: one page. The request is already parsed
	// and checked; what only the backend can check (the regular expression,
	// the cursor, the resolved range) comes back as a *RequestError.
	Search(ctx context.Context, r SearchRequest) (SearchPage, error)

	// Context serves GET /api/context: before and after are 0-500. An invalid
	// ref is a *RequestError, a ref whose file is gone ErrNotFound.
	Context(ctx context.Context, ref string, before, after int) (ContextPage, error)

	// CreateExport serves POST /api/exports: it queues an export and returns
	// its entry. A selection with no devices, or a bad regular expression, is a
	// *RequestError.
	CreateExport(ctx context.Context, r ExportRequest) (ExportInfo, error)

	// EstimateExport serves POST /api/exports/estimate: an upper bound from the
	// index alone. Errors as for CreateExport.
	EstimateExport(ctx context.Context, r ExportRequest) (ExportEstimate, error)

	// Exports serves GET /api/exports, newest first.
	Exports(ctx context.Context) ([]ExportInfo, error)

	// Export serves GET /api/exports/{id}: ErrNotFound for an unknown id.
	Export(ctx context.Context, id string) (ExportInfo, error)

	// OpenExport opens a ready export's zip for GET /api/exports/{id}/download:
	// ErrNotFound for an unknown or expired one, ErrNotReady for one that isn't
	// ready. The caller closes the file.
	OpenExport(ctx context.Context, id string) (ExportDownload, error)

	// DeleteExport serves DELETE /api/exports/{id}: it cancels a queued or
	// building export, or deletes a ready one. ErrNotFound for an unknown id.
	DeleteExport(ctx context.Context, id string) error

	// RecordEvent adds one of the HTTP layer's own events to the service event
	// log (the one exported as woodpile-events.log): UI start and bind errors,
	// mode changes, password changes, certificate generation and login
	// lockouts. Errors are logged and otherwise ignored.
	RecordEvent(ctx context.Context, e Event) error
}

// ErrNotFound is returned (possibly wrapped) by UpdateDevice, Context and the
// export methods for an unknown id.
var ErrNotFound = errors.New("not found")

// ValidationError is returned by SaveSettings for an invalid object. Fields maps
// a dotted JSON path ("listeners.udp.port", "listeners.allowlist[2]") to a
// human-readable message.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "invalid settings" }

// Event is one of the HTTP layer's own events (see Backend.RecordEvent).
type Event struct {
	Level string // "info", "warn" or "error"
	Kind  string // EventKindUI or EventKindAuth
	Text  string
}

// Event kinds and levels used by this package.
const (
	EventKindUI   = "ui"   // the UI server started, failed to bind, changed mode, generated a certificate
	EventKindAuth = "auth" // password set or changed, login lockout

	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// ---- GET /api/status ----

// Status is the body of GET /api/status.
type Status struct {
	Version                 string     `json:"version"`
	Build                   string     `json:"build"`
	OS                      string     `json:"os"`
	DataDir                 string     `json:"data_dir"`
	StartedAt               time.Time  `json:"started_at"`
	FirstMessageAt          *time.Time `json:"first_message_at"` // nil until the first message is received
	UDPRcvbufBytes          int64      `json:"udp_rcvbuf_bytes"`
	UDPRcvbufRequestedBytes int64      `json:"udp_rcvbuf_requested_bytes"`
	ReleasesURL             string     `json:"releases_url"`
	KernelDropsVisible      bool       `json:"kernel_drops_visible"`
	Listeners               []Listener `json:"listeners"`
	HostIPs                 []string   `json:"host_ips"`
	Banners                 []Banner   `json:"banners"`
	// UICertFingerprint is the SHA-256 of the UI's certificate in LAN mode
	// ("AB:CD:…"), "" in localhost mode. Filled by this package.
	UICertFingerprint string `json:"ui_cert_fingerprint"`
}

// Listener is one syslog listener's state.
type Listener struct {
	Proto  string `json:"proto"` // "udp" or "tcp"
	Addr   string `json:"addr"`
	Port   int    `json:"port"`
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
	Holder string `json:"holder,omitempty"` // the process holding the port, when the OS tells us
}

// Banner is a message the UI shows at the top of every page.
type Banner struct {
	Kind   string `json:"kind"`   // listener_bind, writes_paused, low_disk
	Level  string `json:"level"`  // error, warn, info
	Text   string `json:"text"`   //
	Action string `json:"action"` // "" or "retry_listeners"
}

// ---- GET /api/stats ----

// Stats is the body of GET /api/stats.
type Stats struct {
	MsgsPerSec       float64      `json:"msgs_per_sec"`
	Sparkline        []float64    `json:"sparkline"` // msgs/s averaged per minute, oldest first, last 60 minutes
	Devices          DeviceCounts `json:"devices"`
	Disk             Disk         `json:"disk"`
	Dropped24h       int64        `json:"dropped_24h"`
	KernelDropped24h *int64       `json:"kernel_dropped_24h"` // nil where the OS doesn't expose it
	Truncated24h     int64        `json:"truncated_24h"`
	Rejected24h      int64        `json:"rejected_24h"`
}

// DeviceCounts counts non-hidden devices by status.
type DeviceCounts struct {
	Total   int `json:"total"`
	Online  int `json:"online"`
	Quiet   int `json:"quiet"`
	Offline int `json:"offline"`
}

// Disk is the storage summary on the dashboard.
type Disk struct {
	UsedBytes int64 `json:"used_bytes"`
	CapBytes  int64 `json:"cap_bytes"`
	FreeBytes int64 `json:"free_bytes"`
	DaysKept  int   `json:"days_kept"`
}

// ---- GET /api/rejected ----

// RejectedSender is a refused sender.
type RejectedSender struct {
	IP       string    `json:"ip"`
	Count24h int64     `json:"count_24h"`
	LastSeen time.Time `json:"last_seen"`
	Reason   string    `json:"reason"` // the latest refusal's reason: "allowlist" or "per_source"
}

// ---- /api/devices ----

// Device is one sender.
type Device struct {
	ID          string    `json:"id"`
	IP          string    `json:"ip"`
	Name        string    `json:"name"`
	Group       string    `json:"group"`
	Hidden      bool      `json:"hidden"`
	FirstSeen   time.Time `json:"first_seen"`
	LastHeard   time.Time `json:"last_heard"`
	Status      string    `json:"status"` // online, quiet, offline
	MsgsPerMin  int64     `json:"msgs_per_min"`
	TotalMsgs   int64     `json:"total_msgs"`
	BytesOnDisk int64     `json:"bytes_on_disk"`
}

// DevicePatch is the body of PATCH /api/devices/{id}; nil fields are left alone.
type DevicePatch struct {
	Name   *string `json:"name,omitempty"`
	Group  *string `json:"group,omitempty"`
	Hidden *bool   `json:"hidden,omitempty"`
}

// ---- /api/settings ----

// Settings is the body of GET and PUT /api/settings.
type Settings struct {
	Listeners ListenersSettings `json:"listeners"`
	Storage   StorageSettings   `json:"storage"`
	UI        UISettings        `json:"ui"`
	Quiet     QuietSettings     `json:"quiet"`
}

// ListenersSettings holds the syslog listener settings.
type ListenersSettings struct {
	UDP       ListenerSettings `json:"udp"`
	TCP       ListenerSettings `json:"tcp"`
	Allowlist []string         `json:"allowlist"`
	// New-sender limits (the device_limit banner).
	MaxDevices          int `json:"max_devices"`
	NewDeviceBurst      int `json:"new_device_burst"`
	NewDevicesPerMinute int `json:"new_devices_per_minute"`
}

// ListenerSettings is one syslog listener's settings.
type ListenerSettings struct {
	Enabled bool   `json:"enabled"`
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
}

// StorageSettings holds the storage settings. DataDir is read-only in v1.
type StorageSettings struct {
	DataDir        string `json:"data_dir"`
	RetentionDays  int    `json:"retention_days"`
	RetentionBytes int64  `json:"retention_bytes"`
	PartBytes      int64  `json:"part_bytes"`
}

// UISettings holds the web UI settings.
type UISettings struct {
	LANMode           bool   `json:"lan_mode"`
	Port              int    `json:"port"`
	CertFile          string `json:"cert_file"` // empty: use the self-signed certificate
	KeyFile           string `json:"key_file"`
	RequireLoginLocal bool   `json:"require_login_local"` // localhost mode asks for the password too
}

// QuietSettings holds the device status thresholds.
type QuietSettings struct {
	QuietAfterSeconds   int `json:"quiet_after_seconds"`
	OfflineAfterSeconds int `json:"offline_after_seconds"`
}

// ---- GET /api/storage/history ----

// StorageEntry is one deleted file.
type StorageEntry struct {
	Time     time.Time `json:"time"`
	File     string    `json:"file"`
	DeviceID string    `json:"device_id"`
	Bytes    int64     `json:"bytes"`
	Reason   string    `json:"reason"` // age, cap, low_disk
}

// ---- normalization: UTC times, [] rather than null ----

func (s *Status) normalize() {
	s.StartedAt = s.StartedAt.UTC()
	if s.FirstMessageAt != nil {
		t := s.FirstMessageAt.UTC()
		s.FirstMessageAt = &t
	}
	s.Listeners = nonNil(s.Listeners)
	s.HostIPs = nonNil(s.HostIPs)
	s.Banners = nonNil(s.Banners)
	// Priority order: errors, then warnings, then anything else; stable within a level.
	slices.SortStableFunc(s.Banners, func(a, b Banner) int { return bannerRank(a.Level) - bannerRank(b.Level) })
}

func bannerRank(level string) int {
	switch level {
	case LevelError:
		return 0
	case LevelWarn:
		return 1
	}
	return 2
}

// MaxRejected caps GET /api/rejected.
const MaxRejected = 200

// normalizeRejected sorts by count_24h descending (most recent first on a tie),
// keeps at most MaxRejected and makes times UTC.
func normalizeRejected(rs []RejectedSender) []RejectedSender {
	rs = slices.Clone(nonNil(rs))
	slices.SortStableFunc(rs, func(a, b RejectedSender) int {
		if a.Count24h != b.Count24h {
			return cmp.Compare(b.Count24h, a.Count24h)
		}
		return b.LastSeen.Compare(a.LastSeen)
	})
	if len(rs) > MaxRejected {
		rs = rs[:MaxRejected]
	}
	for i := range rs {
		rs[i].LastSeen = rs[i].LastSeen.UTC()
	}
	return rs
}

func (s *Stats) normalize() { s.Sparkline = nonNil(s.Sparkline) }

func (d *Device) normalize() {
	d.FirstSeen = d.FirstSeen.UTC()
	d.LastHeard = d.LastHeard.UTC()
}

func (s *Settings) normalize() { s.Listeners.Allowlist = nonNil(s.Listeners.Allowlist) }

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
