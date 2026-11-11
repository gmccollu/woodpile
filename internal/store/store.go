// Package store writes received messages to Woodpile's plain log files (SPEC §3.3–3.4)
// and keeps them within the disk budget.
//
// Layout: <data>/logs/<model.DeviceID(sender)>/<YYYY-MM-DD>.log is a device's active
// file. It is closed at 00:00 UTC, or at PartBytes, when it is renamed to the next
// numbered part <YYYY-MM-DD>.NNN.log. A background worker gzips closed files to .log.gz
// (verifying the gzip before deleting the original), and retention deletes the oldest
// compressed files by age, by total size and when the disk runs low.
//
// Writes are buffered per device and flushed every FlushInterval or FlushBytes, and
// fsynced every SyncInterval, so a crash loses at most about SyncInterval of data.
// See package logfile for the line format.
package store

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/model"
	"github.com/gmccollu/woodpile/internal/store/logfile"
)

// Defaults for Options.
const (
	DefaultPartBytes      = 100 << 20
	DefaultRetentionDays  = 30
	DefaultRetentionBytes = 20 << 30
	DefaultMinFreeBytes   = 1 << 30
	DefaultMinFreePercent = 5.0
	DefaultFlushInterval  = time.Second
	DefaultFlushBytes     = 1 << 20
	DefaultSyncInterval   = 5 * time.Second
	DefaultMaxOpenFiles   = 512
	DefaultCloseWorkers   = 8
)

// Errors returned by Write.
var (
	ErrClosed        = errors.New("store: closed")
	ErrPaused        = errors.New("store: writes paused: the disk is nearly full")
	ErrInvalidSender = errors.New("store: message has no valid sender address")
)

// Options configures a Store. Zero fields take the defaults above.
type Options struct {
	DataDir string    // required; logs go under DataDir/logs
	Index   *index.DB // required; the store doesn't close it

	PartBytes      int64   // close the active file at this size (uncompressed)
	RetentionDays  int     // delete compressed files older than this
	RetentionBytes int64   // delete the oldest compressed files while all logs exceed this
	MinFreeBytes   uint64  // low-disk guard: keep at least max(MinFreeBytes,
	MinFreePercent float64 // MinFreePercent of the volume) free

	FlushInterval time.Duration // write buffers to the files at least this often
	FlushBytes    int           // and whenever a device's buffer reaches this
	SyncInterval  time.Duration // fsync written files this often
	MaxOpenFiles  int           // cap on open log file handles (LRU)
	// CloseWorkers bounds how many closed files (yesterday's at midnight, or a full
	// part) are fsynced at once, off the write path.
	CloseWorkers int

	// Clock returns the current time (tests inject a fake). It decides midnight
	// rotation of idle files, retention age and the time of a message with a zero
	// Received. Default time.Now.
	Clock func() time.Time
	// DiskUsage reports the free and total bytes of the volume holding path (tests
	// inject a fake). Default: the real DiskUsage for this OS.
	DiskUsage func(path string) (free, total uint64, err error)

	// Manual turns off every background goroutine. Call Maintain to rotate, compress,
	// apply retention and update the index, and Flush to write and fsync. Compression
	// then runs inside Maintain. Meant for tests.
	Manual bool

	Logger *slog.Logger // default slog.Default()
}

// Limits are the settings that can change while the store runs.
type Limits struct {
	RetentionDays  int
	RetentionBytes int64
	PartBytes      int64
}

// Store writes messages to log files. It is safe for concurrent use.
type Store struct {
	opts    Options
	dataDir string
	logsDir string
	idx     *index.DB
	clock   func() time.Time
	disk    func(string) (uint64, uint64, error)
	log     *slog.Logger

	retentionDays  atomic.Int64
	retentionBytes atomic.Int64
	partBytes      atomic.Int64

	devices sync.Map // netip.Addr -> *device
	devMu   sync.Mutex
	devList []*device // every device, for iteration (append only)

	closed      atomic.Bool
	paused      atomic.Bool
	pausedSince atomic.Int64 // unix ms
	dropped     atomic.Int64 // messages dropped since Open
	written     atomic.Int64 // messages written to files since Open
	activeBytes atomic.Int64 // bytes on disk in active files

	lruMu     sync.Mutex
	lru       list.List // of *device with an open handle, most recent first
	openFiles int
	maxOpen   int // high-water mark of openFiles, for tests

	qMu     sync.Mutex
	qOps    []index.FileOp
	qAct    []index.Activity
	qEvents []index.Event
	applyMu sync.Mutex

	catMu    sync.Mutex
	cat      map[string]*catEntry // closed files by rel path
	catBytes int64
	orphans  map[string]bool // compressed originals still to delete (abs paths)

	diskMu       sync.Mutex
	lastFree     uint64
	lastTotal    uint64
	diskErrShown bool

	maintMu   sync.Mutex
	lastPrune time.Time

	dropMu     sync.Mutex
	dropCounts map[dropKey]int64 // drops not yet reported in an event
	dropErrs   map[string]string // device -> last write error
	lastDropEv time.Time

	stop       chan struct{}
	wg         sync.WaitGroup
	compressCh chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc

	closeMu   sync.Mutex
	closeCond *sync.Cond
	closeQ    []closeJob
	closeStop bool
	closeWG   sync.WaitGroup
	syncPath  func(path string) error // fsync by path; tests slow it down

	lastRollup time.Time

	// testHookGzip, if set, is called with the temp gzip path before verification.
	testHookGzip func(path string)
}

// Open recovers the data dir (see recover.go) and starts the store.
func Open(opts Options) (*Store, error) {
	if opts.DataDir == "" {
		return nil, errors.New("store: DataDir is required")
	}
	if opts.Index == nil {
		return nil, errors.New("store: Index is required")
	}
	setDefault(&opts.PartBytes, DefaultPartBytes)
	setDefault(&opts.RetentionDays, DefaultRetentionDays)
	setDefault(&opts.RetentionBytes, DefaultRetentionBytes)
	setDefault(&opts.MinFreeBytes, DefaultMinFreeBytes)
	setDefault(&opts.MinFreePercent, DefaultMinFreePercent)
	setDefault(&opts.FlushInterval, DefaultFlushInterval)
	setDefault(&opts.FlushBytes, DefaultFlushBytes)
	setDefault(&opts.SyncInterval, DefaultSyncInterval)
	setDefault(&opts.MaxOpenFiles, DefaultMaxOpenFiles)
	setDefault(&opts.CloseWorkers, DefaultCloseWorkers)
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.DiskUsage == nil {
		opts.DiskUsage = DiskUsage
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Store{
		opts:       opts,
		dataDir:    opts.DataDir,
		logsDir:    filepath.Join(opts.DataDir, logfile.LogsDir),
		idx:        opts.Index,
		clock:      opts.Clock,
		disk:       opts.DiskUsage,
		log:        opts.Logger,
		cat:        map[string]*catEntry{},
		orphans:    map[string]bool{},
		dropCounts: map[dropKey]int64{},
		dropErrs:   map[string]string{},
		stop:       make(chan struct{}),
		compressCh: make(chan struct{}, 1),
		syncPath:   syncPath,
	}
	s.closeCond = sync.NewCond(&s.closeMu)
	s.retentionDays.Store(int64(opts.RetentionDays))
	s.retentionBytes.Store(opts.RetentionBytes)
	s.partBytes.Store(opts.PartBytes)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if err := os.MkdirAll(s.logsDir, 0o750); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := s.recover(s.ctx); err != nil {
		return nil, fmt.Errorf("store: recovery: %w", err)
	}
	if err := s.applyIndex(); err != nil {
		s.log.Warn("store: index update failed", "err", err)
	}
	if !opts.Manual {
		s.wg.Add(3)
		go s.flushLoop()
		go s.syncLoop()
		go s.compressLoop()
		s.startClosers()
		s.kickCompress()
	}
	return s, nil
}

func setDefault[T int | int64 | uint64 | float64 | time.Duration](v *T, def T) {
	if *v <= 0 {
		*v = def
	}
}

// SetLimits changes retention and the part size; zero fields are left unchanged.
// The new limits apply from the next maintenance pass.
func (s *Store) SetLimits(l Limits) {
	if l.RetentionDays > 0 {
		s.retentionDays.Store(int64(l.RetentionDays))
	}
	if l.RetentionBytes > 0 {
		s.retentionBytes.Store(l.RetentionBytes)
	}
	if l.PartBytes > 0 {
		s.partBytes.Store(l.PartBytes)
	}
}

// Limits returns the limits in effect.
func (s *Store) Limits() Limits {
	return Limits{
		RetentionDays:  int(s.retentionDays.Load()),
		RetentionBytes: s.retentionBytes.Load(),
		PartBytes:      s.partBytes.Load(),
	}
}

// Write stores one message. It never blocks on the disk except to write a full
// buffer (FlushBytes) to the OS. It returns ErrPaused, and counts the message as
// dropped, while the low-disk guard has paused writing.
func (s *Store) Write(m model.Message) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if !m.Sender.IsValid() {
		return ErrInvalidSender
	}
	rec := m.Received
	if rec.IsZero() {
		rec = s.clock()
	}
	rec = time.UnixMilli(rec.UnixMilli()).UTC() // the file keeps milliseconds; so does the index
	d := s.device(m.Sender.Unmap())
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	if s.paused.Load() {
		d.dropLocked(rec, 1)
		return ErrPaused
	}
	return d.appendLocked(rec, &m)
}

// device returns the state for a sender, creating it on first use.
func (s *Store) device(addr netip.Addr) *device {
	if v, ok := s.devices.Load(addr); ok {
		return v.(*device)
	}
	d := newDevice(s, addr)
	v, loaded := s.devices.LoadOrStore(addr, d)
	if !loaded {
		s.devMu.Lock()
		s.devList = append(s.devList, d)
		s.devMu.Unlock()
	}
	return v.(*device)
}

func (s *Store) allDevices() []*device {
	s.devMu.Lock()
	defer s.devMu.Unlock()
	return append([]*device(nil), s.devList...)
}

// Flush writes every buffer to its file, fsyncs the written files and updates the
// index.
func (s *Store) Flush() error {
	err := s.flushAll(false)
	s.drainClose()
	if serr := s.syncAll(); err == nil {
		err = serr
	}
	if ierr := s.applyIndex(); err == nil {
		err = ierr
	}
	return err
}

// Close flushes and fsyncs everything, updates the index and stops the background
// work. A compression in progress is abandoned and redone at the next Open.
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	close(s.stop)
	s.cancel()
	s.wg.Wait()
	s.stopClosers()
	var first error
	keep := func(err error) {
		if first == nil && err != nil {
			first = err
		}
	}
	for _, d := range s.allDevices() {
		d.mu.Lock()
		d.closed = true
		keep(d.flushLocked())
		d.collectLocked()
		keep(d.closeFileLocked(true))
		d.mu.Unlock()
	}
	s.drainClose()
	s.reportDrops(s.clock(), true)
	keep(s.applyIndex())
	return first
}

// Usage is the storage summary for /api/stats and the storage settings.
type Usage struct {
	BytesOnDisk        int64     // all log files, compressed and not
	CapBytes           int64     // RetentionBytes
	RetentionDays      int       // the age limit
	FreeBytes          uint64    // free space on the data volume (last check)
	VolumeBytes        uint64    // size of the data volume (last check)
	MinFreeBytes       uint64    // free space the low-disk guard keeps
	DaysKept           int       // calendar days from the oldest file's day to today
	Oldest             time.Time // earliest received time on disk (zero if none)
	Files              int       // log files on disk
	PendingCompression int       // closed files not yet gzipped
	WritesPaused       bool      // the low-disk guard stopped writing (red banner)
	PausedSince        time.Time // zero when not paused
	Dropped            int64     // messages dropped since Open (paused or write errors)
	Written            int64     // messages written since Open
}

// Usage returns the current storage summary.
func (s *Store) Usage() Usage {
	u := Usage{
		CapBytes:      s.retentionBytes.Load(),
		RetentionDays: int(s.retentionDays.Load()),
		WritesPaused:  s.paused.Load(),
		Dropped:       s.dropped.Load(),
		Written:       s.written.Load(),
	}
	if u.WritesPaused {
		u.PausedSince = time.UnixMilli(s.pausedSince.Load()).UTC()
	}
	s.diskMu.Lock()
	u.FreeBytes, u.VolumeBytes = s.lastFree, s.lastTotal
	s.diskMu.Unlock()
	u.MinFreeBytes = s.minFree(u.VolumeBytes)
	oldestDay := ""
	s.catMu.Lock()
	u.BytesOnDisk = s.catBytes
	u.Files = len(s.cat)
	for _, e := range s.cat {
		if !e.name.Compressed {
			u.PendingCompression++
		}
		if oldestDay == "" || e.name.Day < oldestDay {
			oldestDay = e.name.Day
		}
		if !e.first.IsZero() && (u.Oldest.IsZero() || e.first.Before(u.Oldest)) {
			u.Oldest = e.first
		}
	}
	s.catMu.Unlock()
	u.BytesOnDisk += s.activeBytes.Load()
	for _, d := range s.allDevices() {
		d.mu.Lock()
		if d.day != "" && d.w.size > 0 {
			u.Files++
			if oldestDay == "" || d.day < oldestDay {
				oldestDay = d.day
			}
			if !d.w.first.IsZero() && (u.Oldest.IsZero() || d.w.first.Before(u.Oldest)) {
				u.Oldest = d.w.first
			}
		}
		d.mu.Unlock()
	}
	if oldestDay != "" {
		if t, err := logfile.DayStart(oldestDay); err == nil {
			today, _ := logfile.DayStart(logfile.DayOf(s.clock()))
			u.DaysKept = int(today.Sub(t)/(24*time.Hour)) + 1
			if u.DaysKept < 1 {
				u.DaysKept = 1
			}
		}
	}
	return u
}

// Paused reports whether the low-disk guard has paused writing.
func (s *Store) Paused() bool { return s.paused.Load() }

// event queues a service event for the index.
func (s *Store) event(level index.Level, kind index.Kind, dev, text string) {
	s.qMu.Lock()
	s.qEvents = append(s.qEvents, index.Event{Time: s.clock(), Level: level, Kind: kind, DeviceID: dev, Text: text})
	s.qMu.Unlock()
	switch level {
	case index.LevelError:
		s.log.Error("store: "+text, "device", dev)
	case index.LevelWarn:
		s.log.Warn("store: "+text, "device", dev)
	default:
		s.log.Debug("store: "+text, "device", dev)
	}
}

func (s *Store) pushOps(ops ...index.FileOp) {
	s.qMu.Lock()
	s.qOps = append(s.qOps, ops...)
	s.qMu.Unlock()
}

// maxQueued bounds the index queue if the index keeps failing; the files stay
// correct, and Reconcile at the next start rebuilds what was lost.
const maxQueued = 1 << 20

// applyIndex writes the queued file ops, activity and events in one transaction.
func (s *Store) applyIndex() error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	s.qMu.Lock()
	b := index.Batch{Files: s.qOps, Activity: s.qAct, Events: s.qEvents}
	s.qOps, s.qAct, s.qEvents = nil, nil, nil
	s.qMu.Unlock()
	if b.Empty() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := s.idx.Apply(ctx, b)
	if err == nil {
		return nil
	}
	s.log.Error("store: index update failed; will retry", "err", err)
	s.qMu.Lock()
	s.qOps = append(b.Files, s.qOps...)
	s.qAct = append(b.Activity, s.qAct...)
	s.qEvents = append(b.Events, s.qEvents...)
	if len(s.qOps) > maxQueued {
		s.qOps = s.qOps[len(s.qOps)-maxQueued:]
	}
	if len(s.qAct) > maxQueued {
		s.qAct = s.qAct[len(s.qAct)-maxQueued:]
	}
	if len(s.qEvents) > maxQueued {
		s.qEvents = s.qEvents[len(s.qEvents)-maxQueued:]
	}
	s.qMu.Unlock()
	return err
}

func (s *Store) rel(abs string) string {
	r, err := filepath.Rel(s.dataDir, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}
