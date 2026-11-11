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

// Fair-share retention. See fairDelete for the order; these tune it.
const (
	// newDeviceAge: a device first seen less than this ago is "new" and its data goes
	// first.
	newDeviceAge = 24 * time.Hour
	// shareHistory is how far back a device's average daily volume (its weight) is taken.
	shareHistory = 7 * 24 * time.Hour
	// minShareWeight is the weight floor, in bytes a day: a device that sends little
	// still gets a small share.
	minShareWeight = 1 << 10
	// guaranteedDays is how many calendar days (today included) an established device
	// always keeps against the size cap (also capped by RetentionDays).
	guaranteedDays = 2
)

// fairInfo is what a deletion pass needs from the index, read once per pass.
type fairInfo struct {
	firstSeen map[string]time.Time
	perDay    map[string]float64 // average raw bytes a day over shareHistory
}

// fairInfoAt reads first-seen times and recent volumes from the index. First seen
// must come from there, not from the oldest data still on disk: once retention
// deletes a device's older files, its data would look new.
func (s *Store) fairInfoAt(now time.Time) *fairInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fi := &fairInfo{firstSeen: map[string]time.Time{}, perDay: map[string]float64{}}
	devs, err := s.idx.ListDevices(ctx)
	if err != nil {
		s.log.Warn("store: reading first-seen times failed", "err", err)
	}
	for _, d := range devs {
		fi.firstSeen[d.ID] = d.FirstSeen
	}
	counts, err := s.idx.DeviceCounts(ctx, now.Add(-shareHistory), now)
	if err != nil {
		s.log.Warn("store: reading recent volumes failed", "err", err)
	}
	for id, c := range counts {
		// Over the whole window, whatever the device's age: a sender that appeared two
		// days ago weighs two-sevenths of one that sent as much every day of the week.
		// Stats outlive files, so a device keeps its weight after retention trims it.
		fi.perDay[id] = float64(c.Bytes) / (shareHistory.Hours() / 24)
	}
	return fi
}

// fairDev is one device's state for a fair-share deletion pass.
type fairDev struct {
	id     string
	bytes  int64 // on disk: closed, parked and active files
	isNew  bool  // first seen less than newDeviceAge ago
	weight float64
	share  int64
	files  []*catEntry     // deletable closed files, oldest first
	keep   map[string]bool // guaranteed days (established devices only)
}

// fairDevices collects every device with data: bytes, weight and share, its
// deletable closed files and its guaranteed days.
func (s *Store) fairDevices(now time.Time, uncompressed bool, fi *fairInfo) map[string]*fairDev {
	devs := map[string]*fairDev{}
	first := map[string]time.Time{}
	days := map[string]map[string]bool{}
	get := func(id string) *fairDev {
		fd := devs[id]
		if fd == nil {
			fd = &fairDev{id: id}
			devs[id] = fd
			days[id] = map[string]bool{}
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
		days[e.deviceID][e.name.Day] = true
	}
	s.catMu.Unlock()
	for _, d := range s.allDevices() {
		d.mu.Lock()
		if d.day != "" && d.w.size > 0 {
			get(d.id).bytes += d.w.size
			seen(d.id, d.w.first)
			days[d.id][d.day] = true
		}
		d.mu.Unlock()
	}
	for _, e := range s.sortedClosed(func(e *catEntry) bool { return uncompressed || e.name.Compressed }) {
		fd := get(e.deviceID)
		fd.files = append(fd.files, e)
	}
	// The guaranteed days count back from today (today and yesterday), not a device's
	// own newest days, so a sender can't make old files undeletable by staying quiet.
	keepN := int(min(int64(guaranteedDays), s.retentionDays.Load()))
	keepFrom := logfile.DayOf(now.Add(-time.Duration(keepN-1) * 24 * time.Hour))
	var total float64
	for id, fd := range devs {
		f, ok := fi.firstSeen[id]
		if !ok || f.IsZero() {
			f = first[id]
		}
		fd.isNew = f.IsZero() || !f.Before(now.Add(-newDeviceAge))
		if !fd.isNew && keepN > 0 {
			fd.keep = map[string]bool{}
			for day := range days[id] {
				if day >= keepFrom {
					fd.keep[day] = true
				}
			}
		}
		fd.weight = max(minShareWeight, fi.perDay[id])
		if fd.bytes > 0 {
			total += fd.weight
		}
	}
	// Each share is the weighted split of RetentionBytes, but never below a tenth of
	// an equal split: a device that only ever sends a little isn't "over" for a few KB.
	budget := float64(s.retentionBytes.Load())
	withData := 0
	for _, fd := range devs {
		if fd.bytes > 0 {
			withData++
		}
	}
	for _, fd := range devs {
		if total > 0 {
			fd.share = int64(max(budget*fd.weight/total, budget/float64(10*withData)))
		}
	}
	return devs
}

// oldest returns the index of the device's oldest deletable file, or -1. Without
// guaranteed, files in the device's guaranteed days are skipped.
func (fd *fairDev) oldest(guaranteed bool) int {
	for i, e := range fd.files {
		if guaranteed || !fd.keep[e.name.Day] {
			return i
		}
	}
	return -1
}

// fairDelete deletes closed files until done reports true (given the bytes freed so
// far), in fair-share order:
//
//  1. Devices first seen in the last 24 h, oldest file first. A flood of new
//     senders, however it's spread, pays first.
//  2. Established devices over their share, the most over first, oldest file first.
//     Shares split RetentionBytes by weight: each device's average bytes a day over
//     the last 7 days (1 KiB floor), and no share is below a tenth of an equal split.
//     A long-established busy device gets a large share; junk senders get little.
//  3. Everyone, oldest file first.
//  4. Only for the low-disk guard (lowDisk and guaranteed), when nothing else is left:
//     the guaranteed days.
//
// Steps 2 and 3 never take an established device's files from its guaranteed days:
// the last min(RetentionDays, 2) calendar days, today included. The size cap never
// does, so it can be exceeded by those. Active and parked files are never taken here.
func (s *Store) fairDelete(now time.Time, reason index.Reason, lowDisk, guaranteed bool, fi *fairInfo,
	done func(freed uint64) bool) {
	devs := s.fairDevices(now, lowDisk, fi)
	var freed uint64
	for !done(freed) {
		var pick *fairDev
		pi := -1
		olderFirst := func(fd *fairDev, i int) bool {
			if pick == nil {
				return true
			}
			a, b := fd.files[i], pick.files[pi]
			return a.last.Before(b.last) || (a.last.Equal(b.last) && a.rel < b.rel)
		}
		take := func(fd *fairDev, i int, better bool) {
			if i >= 0 && better {
				pick, pi = fd, i
			}
		}
		for _, fd := range devs { // 1: new devices, oldest first
			if fd.isNew {
				if i := fd.oldest(true); i >= 0 {
					take(fd, i, olderFirst(fd, i))
				}
			}
		}
		if pick == nil { // 2: over share, most over first
			for _, fd := range devs {
				if !fd.isNew && fd.bytes > fd.share {
					i := fd.oldest(false)
					take(fd, i, i >= 0 && (pick == nil || fd.bytes-fd.share > pick.bytes-pick.share ||
						(fd.bytes-fd.share == pick.bytes-pick.share && fd.id < pick.id)))
				}
			}
		}
		if pick == nil { // 3: everyone, oldest first
			for _, fd := range devs {
				if i := fd.oldest(false); i >= 0 {
					take(fd, i, olderFirst(fd, i))
				}
			}
		}
		if pick == nil && lowDisk && guaranteed { // 4: the guaranteed days
			for _, fd := range devs {
				if i := fd.oldest(true); i >= 0 {
					take(fd, i, olderFirst(fd, i))
				}
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
// so the low-disk guard can delete it, in the fair-share order: new devices' files
// first (largest first); then, only with anyone, established devices over their
// share (the most over first), then the largest file of anyone. It reports whether
// it closed (or tried) one.
func (s *Store) closeOpen(now time.Time, tried map[string]bool, anyone bool, fi *fairInfo) bool {
	devs := s.fairDevices(now, true, fi)
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
		case fd == nil || fd.isNew:
			return 1, f.size
		case fd.bytes > fd.share:
			return 2, fd.bytes - fd.share
		default:
			return 3, f.size
		}
	}
	var best *openFile
	bt, bs := 0, int64(0)
	for i := range open {
		t, sc := rank(open[i])
		if t > 1 && !anyone {
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
