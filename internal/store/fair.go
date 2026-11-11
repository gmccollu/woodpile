package store

import (
	"context"
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

// firstSeenMap returns each device's first-seen time from the index. It must come
// from there, not from the oldest data still on disk: once retention deletes a
// device's older files, its data would look new.
func (s *Store) firstSeenMap() map[string]time.Time {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	devs, err := s.idx.ListDevices(ctx)
	if err != nil {
		s.log.Warn("store: reading first-seen times failed", "err", err)
		return nil
	}
	out := make(map[string]time.Time, len(devs))
	for _, d := range devs {
		out[d.ID] = d.FirstSeen
	}
	return out
}

// fairDevices collects every device with data, and its deletable closed files.
// firstSeen (from firstSeenMap) decides which devices are new; a device missing from
// it (not in the index yet) falls back to its oldest data on disk.
func (s *Store) fairDevices(now time.Time, uncompressed bool, firstSeen map[string]time.Time) map[string]*fairDev {
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
		f, ok := firstSeen[id]
		if !ok || f.IsZero() {
			f = first[id]
		}
		fd.isNew = !f.IsZero() && !f.Before(now.Add(-24*time.Hour))
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
// far), in fair-share order. Each device's fair share is RetentionBytes divided by
// the devices with data on disk.
//
//  1. Devices over their share, the most over first, oldest file first.
//  2. Devices first seen in the last 24 h, oldest file first.
//  3. Everyone, oldest file first.
//  4. Only for the low-disk guard (lowDisk, newestDays), when nothing else is left:
//     each device's newest day too, oldest first.
//
// Steps 1–3 never take a device's newest day of closed files (step 2's devices have
// no older data). Active and parked files are never taken. So neither one flooding
// device nor a flood spread over many new senders can wipe an established device's
// recent history.
func (s *Store) fairDelete(now time.Time, reason index.Reason, lowDisk, newestDays bool, firstSeen map[string]time.Time,
	done func(freed uint64) bool) {
	devs := s.fairDevices(now, lowDisk, firstSeen)
	withData := 0
	for _, fd := range devs {
		if fd.bytes > 0 {
			withData++
		}
	}
	if withData == 0 {
		return
	}
	share := s.retentionBytes.Load() / int64(withData)
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
		if pick == nil && lowDisk && newestDays { // 4: newest days too
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

// openFile is an active or parked file the low-disk guard could close as a part.
type openFile struct {
	dev    *device   // a live device's active file, or
	parked *catEntry // a parked file
	id     string
	key    string // its path, for tried
	size   int64
}

// closeOpen closes one active or parked file (not yet in tried) as a numbered part,
// so the low-disk guard can delete it, picked in the fair-share order:
//
//  1. devices over their share, the most over first;
//  2. devices first seen in the last 24 h;
//  3. (only with anyone) the largest file of any device.
//
// Within a device, its largest open file. So a flood spread over many new senders
// loses its own open files before an established device loses today's logs. It
// reports whether it closed (or tried) one.
func (s *Store) closeOpen(now time.Time, tried map[string]bool, anyone bool, firstSeen map[string]time.Time) bool {
	devs := s.fairDevices(now, true, firstSeen)
	withData := 0
	for _, fd := range devs {
		if fd.bytes > 0 {
			withData++
		}
	}
	if withData == 0 {
		return false
	}
	share := s.retentionBytes.Load() / int64(withData)
	var open []openFile
	s.catMu.Lock()
	for _, e := range s.cat {
		if e.parked && !tried[e.rel] {
			open = append(open, openFile{parked: e, id: e.deviceID, key: e.rel, size: e.size})
		}
	}
	s.catMu.Unlock()
	for _, d := range s.allDevices() {
		d.mu.Lock()
		if d.day != "" && d.w.size > 0 && !tried[d.rel] {
			open = append(open, openFile{dev: d, id: d.id, key: d.rel, size: d.w.size})
		}
		d.mu.Unlock()
	}
	rank := func(f openFile) (int, int64) { // lower tier first; within it, higher score first
		fd := devs[f.id]
		switch {
		case fd != nil && fd.bytes > share:
			return 1, fd.bytes - share
		case fd != nil && fd.isNew:
			return 2, f.size
		default:
			return 3, f.size
		}
	}
	var best *openFile
	bt, bs := 0, int64(0)
	for i := range open {
		t, sc := rank(open[i])
		if t == 3 && !anyone {
			continue
		}
		if best == nil || t < bt || (t == bt && (sc > bs || (sc == bs && open[i].size > best.size))) {
			best, bt, bs = &open[i], t, sc
		}
	}
	if best == nil {
		return false
	}
	tried[best.key] = true
	if d := best.dev; d != nil {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.day == "" || d.closed || d.rel != best.key {
			return true // changed meanwhile; let the caller look again
		}
		if e := d.rotateLocked(rotLowDisk); e != nil {
			s.catMu.Lock()
			e.syncing = false // about to be deleted: no need to wait for its fsync
			s.catMu.Unlock()
		}
		return true
	}
	s.catMu.Lock()
	defer s.catMu.Unlock()
	e := best.parked
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
