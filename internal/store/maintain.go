package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/store/logfile"
)

// catEntry is a closed log file (awaiting compression, or compressed).
type catEntry struct {
	deviceID    string
	name        logfile.Name
	abs, rel    string
	first, last time.Time
	size        int64 // on disk
	rawSize     int64
	lines       int64

	busy     bool // being compressed
	syncing  bool // just closed; the closer pool hasn't fsynced it yet
	failures int
	nextTry  time.Time
}

func (e *catEntry) row() index.File {
	return index.File{DeviceID: e.deviceID, Path: e.rel, Day: e.name.Day, Part: e.name.Part, FirstReceived: e.first,
		LastReceived: e.last, Size: e.size, RawSize: e.rawSize, Lines: e.lines, Compressed: e.name.Compressed}
}

func (s *Store) addClosed(e *catEntry) {
	s.catMu.Lock()
	if old := s.cat[e.rel]; old != nil {
		s.catBytes -= old.size
	}
	s.cat[e.rel] = e
	s.catBytes += e.size
	s.catMu.Unlock()
}

func (s *Store) flushLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.opts.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.maintain()
		}
	}
}

func (s *Store) syncLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.opts.SyncInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.syncAll()
		}
	}
}

// Maintain runs one maintenance pass now: flush buffers, close files whose day has
// ended, compress (in Manual mode; otherwise the background worker is woken),
// apply retention and the low-disk guard, and update the index. The background loop
// runs it every FlushInterval.
func (s *Store) Maintain() error {
	if s.closed.Load() {
		return ErrClosed
	}
	return s.maintain()
}

func (s *Store) maintain() error {
	s.maintMu.Lock()
	defer s.maintMu.Unlock()
	err := s.flushAll(true)
	if s.opts.Manual {
		s.drainClose()
		s.compressPending(s.ctx)
	}
	now := s.clock()
	s.cleanOrphans()
	s.enforceRetention(now)
	s.guardDisk(now)
	s.reportDrops(now, false)
	if b := now.Add(-index.MinuteRetention).UTC().Truncate(time.Hour); !b.Equal(s.lastRollup) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if _, rerr := s.idx.RollupStats(ctx, b); rerr != nil {
			s.log.Warn("store: rolling up stats failed", "err", rerr)
		} else {
			s.lastRollup = b
		}
		cancel()
	}
	if now.Sub(s.lastPrune) >= time.Hour {
		s.lastPrune = now
		hist := time.Duration(max(2*s.retentionDays.Load(), 90)) * 24 * time.Hour
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if perr := s.idx.Prune(ctx, now.Add(-index.StatsRetention), now.Add(-hist)); perr != nil {
			s.log.Warn("store: pruning the index failed", "err", perr)
		}
		cancel()
	}
	if ierr := s.applyIndex(); err == nil {
		err = ierr
	}
	if !s.opts.Manual && s.hasCompressWork(now) {
		s.kickCompress()
	}
	return err
}

// flushAll writes every device's buffer and, with midnight set, closes files whose
// day has ended.
func (s *Store) flushAll(midnight bool) error {
	today := logfile.DayOf(s.clock())
	var first error
	for _, d := range s.allDevices() {
		d.mu.Lock()
		if !d.closed {
			if midnight && d.day != "" && d.day < today {
				d.rotateLocked(rotMidnight)
			} else if err := d.flushLocked(); err != nil && first == nil {
				first = err
			}
		}
		d.collectLocked()
		d.mu.Unlock()
	}
	return first
}

// syncAll fsyncs every file written since the last pass, a few at a time.
func (s *Store) syncAll() error {
	var dirty []*device
	for _, d := range s.allDevices() {
		d.fileMu.Lock()
		if d.dirty {
			dirty = append(dirty, d)
		}
		d.fileMu.Unlock()
	}
	var mu sync.Mutex
	var first error
	one := func(d *device) {
		d.fileMu.Lock()
		var err error
		if d.dirty {
			if d.f != nil {
				err = syncFile(d.f)
			} else if d.dirtyPath != "" {
				err = syncPath(d.dirtyPath)
				if errors.Is(err, fs.ErrNotExist) {
					err = nil // renamed or deleted since; rotation synced it
				}
			}
			d.dirty = false
		}
		d.fileMu.Unlock()
		if err != nil {
			mu.Lock()
			if first == nil {
				first = err
			}
			mu.Unlock()
		}
	}
	const workers = 4
	if len(dirty) <= workers {
		for _, d := range dirty {
			one(d)
		}
	} else {
		ch := make(chan *device)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for d := range ch {
					one(d)
				}
			}()
		}
		for _, d := range dirty {
			ch <- d
		}
		close(ch)
		wg.Wait()
	}
	if first != nil {
		s.log.Warn("store: fsync failed", "err", first)
	}
	return first
}

// sortedClosed returns closed entries matching keep, oldest first (by the newest
// line in the file, then path).
func (s *Store) sortedClosed(keep func(*catEntry) bool) []*catEntry {
	s.catMu.Lock()
	var out []*catEntry
	for _, e := range s.cat {
		if !e.busy && !e.syncing && keep(e) {
			out = append(out, e)
		}
	}
	s.catMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].last.Equal(out[j].last) {
			return out[i].last.Before(out[j].last)
		}
		return out[i].rel < out[j].rel
	})
	return out
}

// enforceRetention deletes compressed files older than RetentionDays, then the oldest
// compressed files while all logs together exceed RetentionBytes. It runs every pass,
// so the common case (nothing to delete) is one scan without sorting.
func (s *Store) enforceRetention(now time.Time) {
	cutoff := now.Add(-time.Duration(s.retentionDays.Load()) * 24 * time.Hour)
	for _, e := range s.sortedClosed(func(e *catEntry) bool { return e.name.Compressed && e.last.Before(cutoff) }) {
		s.deleteEntry(e, index.ReasonAge, now)
	}
	limit := s.retentionBytes.Load()
	over := func() bool {
		s.catMu.Lock()
		total := s.catBytes
		s.catMu.Unlock()
		return total+s.activeBytes.Load() > limit
	}
	if !over() {
		return
	}
	for _, e := range s.sortedClosed(func(e *catEntry) bool { return e.name.Compressed }) {
		if !over() {
			break
		}
		s.deleteEntry(e, index.ReasonCap, now)
	}
}

// deleteEntry removes a closed file and records the deletion.
func (s *Store) deleteEntry(e *catEntry, reason index.Reason, now time.Time) bool {
	s.catMu.Lock()
	if e.busy || e.syncing || s.cat[e.rel] != e {
		s.catMu.Unlock()
		return false
	}
	if err := os.Remove(e.abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.catMu.Unlock()
		s.log.Warn("store: couldn't delete a log file; will retry", "file", e.rel, "err", err)
		return false
	}
	delete(s.cat, e.rel)
	s.catBytes -= e.size
	s.catMu.Unlock()
	s.pushOps(index.FileOp{Kind: index.OpDelete, File: index.File{DeviceID: e.deviceID, Path: e.rel},
		Deletion: &index.Deletion{Time: now, File: e.rel, DeviceID: e.deviceID, Bytes: e.size, Reason: reason}})
	return true
}

func (s *Store) minFree(total uint64) uint64 {
	pct := uint64(float64(total) * s.opts.MinFreePercent / 100)
	return max(s.opts.MinFreeBytes, pct)
}

// checkDisk reads the data volume's free space.
func (s *Store) checkDisk() (free, total uint64, ok bool) {
	free, total, err := s.disk(s.dataDir)
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if err != nil {
		if !s.diskErrShown {
			s.diskErrShown = true
			s.event(index.LevelWarn, index.KindError, "", fmt.Sprintf("can't read free disk space (%v); the low-disk guard is off", err))
		}
		return 0, 0, false
	}
	s.diskErrShown = false
	s.lastFree, s.lastTotal = free, total
	return free, total, true
}

// guardDisk keeps free space above max(MinFreeBytes, MinFreePercent): it asks
// Reclaim (exports) first, then deletes the oldest closed files (compressed first, then ones still awaiting compression; never
// an active file), and pauses writing when nothing is left to delete.
func (s *Store) guardDisk(now time.Time) {
	free, total, ok := s.checkDisk()
	if !ok {
		return
	}
	thr := s.minFree(total)
	if free < thr && s.opts.Reclaim != nil {
		if freed := s.opts.Reclaim(thr - free); freed > 0 {
			if free, total, ok = s.checkDisk(); !ok {
				return
			}
			thr = s.minFree(total)
		}
	}
	if free < thr {
		cands := s.sortedClosed(func(e *catEntry) bool { return e.name.Compressed })
		cands = append(cands, s.sortedClosed(func(e *catEntry) bool { return !e.name.Compressed })...)
		for _, e := range cands {
			if free >= thr {
				break
			}
			if !s.deleteEntry(e, index.ReasonLowDisk, now) {
				continue
			}
			if free, total, ok = s.checkDisk(); !ok {
				return
			}
			thr = s.minFree(total)
		}
	}
	if free < thr {
		if !s.paused.Swap(true) {
			s.pausedSince.Store(now.UnixMilli())
			s.event(index.LevelError, index.KindPause, "", fmt.Sprintf(
				"writes paused: %d bytes free on the data volume, %d needed, and no closed logs left to delete", free, thr))
		}
		return
	}
	// Resume only with some headroom, so the banner doesn't flap.
	if s.paused.Load() && free >= thr+thr/20 {
		s.paused.Store(false)
		s.event(index.LevelInfo, index.KindResume, "", fmt.Sprintf("writes resumed: %d bytes free", free))
	}
}

type dropReason int

const (
	dropPaused dropReason = iota
	dropWriteError
)

type dropKey struct {
	dev    string
	reason dropReason
}

func (s *Store) noteDrop(dev string, r dropReason, n int64) {
	if n <= 0 {
		return
	}
	s.dropMu.Lock()
	s.dropCounts[dropKey{dev, r}] += n
	s.dropMu.Unlock()
}

func (s *Store) noteWriteError(dev, rel string, err error) {
	s.dropMu.Lock()
	s.dropErrs[dev] = fmt.Sprintf("%s: %v", rel, err)
	s.dropMu.Unlock()
}

// reportDrops turns counted drops into events, at most once a minute per device and
// reason (or now, with force).
func (s *Store) reportDrops(now time.Time, force bool) {
	s.dropMu.Lock()
	if len(s.dropCounts) == 0 || (!force && now.Sub(s.lastDropEv) < time.Minute) {
		s.dropMu.Unlock()
		return
	}
	counts, errs := s.dropCounts, s.dropErrs
	s.dropCounts, s.dropErrs = map[dropKey]int64{}, map[string]string{}
	s.lastDropEv = now
	s.dropMu.Unlock()
	keys := make([]dropKey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].dev < keys[j].dev || (keys[i].dev == keys[j].dev && keys[i].reason < keys[j].reason)
	})
	for _, k := range keys {
		var text string
		if k.reason == dropPaused {
			text = fmt.Sprintf("dropped %d messages: writes paused (low disk space)", counts[k])
		} else {
			text = fmt.Sprintf("dropped %d messages: write error (%s)", counts[k], errs[k.dev])
		}
		s.event(index.LevelError, index.KindDrop, k.dev, text)
	}
}
