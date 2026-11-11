package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/store/logfile"
)

// fairDev is one device's state for a fair-share deletion pass.
type fairDev struct {
	id        string
	bytes     int64       // on disk: closed, parked and active files
	isNew     bool        // first data less than 24 h old
	files     []*catEntry // deletable closed files, oldest first
	protectAt time.Time   // files whose newest line is at or after this are its newest day
}

// fairDevices collects every device with data, and its deletable closed files.
func (s *Store) fairDevices(now time.Time, uncompressed bool) map[string]*fairDev {
	devs := map[string]*fairDev{}
	first := map[string]time.Time{}
	get := func(id string) *fairDev {
		fd := devs[id]
		if fd == nil {
			fd = &fairDev{id: id}
			devs[id] = fd
		}
		return fd
	}
	seen := func(id string, t time.Time) {
		if !t.IsZero() && (first[id].IsZero() || t.Before(first[id])) {
			first[id] = t
		}
	}
	s.catMu.Lock()
	for _, e := range s.cat {
		get(e.deviceID).bytes += e.size
		seen(e.deviceID, e.first)
	}
	s.catMu.Unlock()
	for _, d := range s.allDevices() {
		d.mu.Lock()
		if d.day != "" && d.w.size > 0 {
			get(d.id).bytes += d.w.size
			seen(d.id, d.w.first)
		}
		d.mu.Unlock()
	}
	for _, e := range s.sortedClosed(func(e *catEntry) bool { return uncompressed || e.name.Compressed }) {
		fd := get(e.deviceID)
		fd.files = append(fd.files, e)
		if e.last.After(fd.protectAt) {
			fd.protectAt = e.last
		}
	}
	for id, fd := range devs {
		fd.protectAt = fd.protectAt.Add(-24 * time.Hour)
		fd.isNew = !first[id].IsZero() && !first[id].Before(now.Add(-24*time.Hour))
	}
	return devs
}

// oldest returns the index of the device's oldest file it may delete, or -1. A
// device's newest day of closed files is protected unless allowProtected, except for
// devices first seen in the last 24 h (all their data is their "newest day", and a
// burst of new senders mustn't shield itself that way).
func (fd *fairDev) oldest(allowProtected bool) int {
	for i, e := range fd.files {
		if allowProtected || fd.isNew || !e.last.After(fd.protectAt) {
			return i
		}
	}
	return -1
}

// fairDelete deletes closed files until done reports true (given the bytes freed so
// far), in fair-share order. Each device's fair share is the budget divided by the
// devices with data on disk; the budget is RetentionBytes, or what's on disk if
// that's less (under the low-disk guard the cap may be far above what fits).
//
//  1. Devices over their share, the most over first, oldest file first.
//  2. Devices first seen in the last 24 h, oldest file first.
//  3. Everyone, oldest file first.
//  4. Only for the low-disk guard (uncompressed set), when nothing else is left:
//     each device's newest day too, oldest first.
//
// Steps 1–3 never take a device's newest day of closed files (step 2's devices have
// no older data). Active and parked files are never taken. So neither one flooding
// device nor a flood spread over many new senders can wipe an established device's
// recent history.
func (s *Store) fairDelete(now time.Time, reason index.Reason, lowDisk bool, done func(freed uint64) bool) {
	devs := s.fairDevices(now, lowDisk)
	withData, total := 0, int64(0)
	for _, fd := range devs {
		if fd.bytes > 0 {
			withData++
			total += fd.bytes
		}
	}
	if withData == 0 {
		return
	}
	share := min(s.retentionBytes.Load(), total) / int64(withData)
	var freed uint64
	for !done(freed) {
		var pick *fairDev
		pi := -1
		consider := func(fd *fairDev, i int, better bool) {
			if i >= 0 && (pick == nil || better) {
				pick, pi = fd, i
			}
		}
		for _, fd := range devs { // 1: over share, most over first
			if fd.bytes > share {
				i := fd.oldest(false)
				consider(fd, i, pick != nil && (fd.bytes-share > pick.bytes-share || (fd.bytes == pick.bytes && fd.id < pick.id)))
			}
		}
		olderFirst := func(fd *fairDev, i int) bool {
			if pick == nil || i < 0 {
				return false
			}
			a, b := fd.files[i], pick.files[pi]
			return a.last.Before(b.last) || (a.last.Equal(b.last) && a.rel < b.rel)
		}
		if pick == nil { // 2: new devices
			for _, fd := range devs {
				if fd.isNew {
					i := fd.oldest(false)
					consider(fd, i, olderFirst(fd, i))
				}
			}
		}
		if pick == nil { // 3: everyone, oldest first
			for _, fd := range devs {
				i := fd.oldest(false)
				consider(fd, i, olderFirst(fd, i))
			}
		}
		if pick == nil && lowDisk { // 4: newest days too
			for _, fd := range devs {
				i := fd.oldest(true)
				consider(fd, i, olderFirst(fd, i))
			}
		}
		if pick == nil {
			return
		}
		e := pick.files[pi]
		pick.files = append(pick.files[:pi:pi], pick.files[pi+1:]...)
		size := e.size
		if s.deleteEntry(e, reason, now) {
			pick.bytes -= size
			freed += uint64(size)
		}
	}
}

// closeLargestOpen closes the largest active or parked file (not yet in tried) as a
// numbered part, so the low-disk guard can delete it. It reports whether it closed one.
func (s *Store) closeLargestOpen(tried map[string]bool) bool {
	var bestDev *device
	var bestParked *catEntry
	var bestSize int64
	s.catMu.Lock()
	for _, e := range s.cat {
		if e.parked && !tried[e.rel] && e.size > bestSize {
			bestParked, bestSize = e, e.size
		}
	}
	s.catMu.Unlock()
	for _, d := range s.allDevices() {
		d.mu.Lock()
		if d.day != "" && !tried[d.rel] && d.w.size > bestSize {
			bestDev, bestParked, bestSize = d, nil, d.w.size
		}
		d.mu.Unlock()
	}
	switch {
	case bestDev != nil:
		d := bestDev
		d.mu.Lock()
		defer d.mu.Unlock()
		tried[d.rel] = true
		if d.day == "" || d.closed {
			return true // changed meanwhile; let the caller look again
		}
		if e := d.rotateLocked(rotLowDisk); e != nil {
			s.catMu.Lock()
			e.syncing = false // about to be deleted: no need to wait for its fsync
			s.catMu.Unlock()
		}
		return true
	case bestParked != nil:
		s.catMu.Lock()
		defer s.catMu.Unlock()
		e := bestParked
		tried[e.rel] = true
		if !e.parked || s.cat[e.rel] != e {
			return true
		}
		dir := filepath.Dir(e.abs)
		nn := logfile.Name{Day: e.name.Day, Part: nextPart(dir, e.name.Day)}
		nabs := filepath.Join(dir, nn.String())
		if err := os.Rename(e.abs, nabs); err != nil {
			s.log.Warn("store: couldn't close a parked file for the low-disk guard", "file", e.rel, "err", err)
			return true
		}
		oldRel := e.rel
		delete(s.cat, oldRel)
		e.name, e.abs, e.rel, e.parked, e.syncing = nn, nabs, s.rel(nabs), false, false
		s.cat[e.rel] = e
		s.pushOps(index.FileOp{Kind: index.OpRename, OldPath: oldRel, File: e.row()})
		s.event(index.LevelInfo, index.KindRotate, e.deviceID, fmt.Sprintf("closed %s (closed early: low disk space, %d lines, %d bytes)",
			e.rel, e.lines, e.size))
		return true
	}
	return false
}

// deletableBytes is everything the low-disk guard could free by deleting closed files
// and closing active and parked files as parts.
func (s *Store) deletableBytes() int64 {
	var n int64
	s.catMu.Lock()
	for _, e := range s.cat {
		if !e.busy {
			n += e.size
		}
	}
	s.catMu.Unlock()
	return n + s.activeBytes.Load()
}
