package store

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"woodpile/internal/index"
	"woodpile/internal/model"
	"woodpile/internal/store/logfile"
)

// recover brings the data dir to a clean state at startup:
//
//  1. Temp files (a gzip in progress) are deleted.
//  2. A .log with a .log.gz of the same name is an interrupted compression: if the
//     gzip holds the same bytes the .log is deleted, otherwise the .log is moved to a
//     new part number (nothing is overwritten).
//  3. A torn last line (a partial write) in an uncompressed file is completed with a
//     newline if its timestamp and sender survived, and cut off otherwise.
//  4. The index is reconciled with the files (index.Reconcile).
//  5. Each device's latest "<day>.log" for today (or later) becomes its active file
//     again, appended to; every other uncompressed file is queued for compression.
func (s *Store) recover(ctx context.Context) error {
	entries, temps, err := logfile.List(s.dataDir)
	if err != nil {
		return err
	}
	var notes []string
	removed := 0
	for _, t := range temps {
		if os.Remove(t) == nil {
			removed++
		}
	}
	if removed > 0 {
		notes = append(notes, fmt.Sprintf("removed %d unfinished gzip files", removed))
	}

	byRel := make(map[string]logfile.Entry, len(entries))
	for _, e := range entries {
		byRel[e.Rel] = e
	}
	pairs, changed := 0, false
	for _, e := range entries {
		if e.Name.Compressed {
			continue
		}
		gz := e.Name
		gz.Compressed = true
		g, ok := byRel[logfile.RelPath(e.DeviceID, gz)]
		if !ok {
			continue
		}
		changed = true
		pairs++
		if same, err := sameContent(e.Path, g.Path); err == nil && same {
			if err := os.Remove(e.Path); err != nil {
				return err
			}
			continue
		}
		dir := filepath.Dir(e.Path)
		nn := logfile.Name{Day: e.Name.Day, Part: nextPart(dir, e.Name.Day)}
		if err := os.Rename(e.Path, filepath.Join(dir, nn.String())); err != nil {
			return err
		}
	}
	if pairs > 0 {
		notes = append(notes, fmt.Sprintf("finished %d interrupted compressions", pairs))
	}
	if changed {
		if entries, _, err = logfile.List(s.dataDir); err != nil {
			return err
		}
	}

	for _, e := range entries {
		if e.Name.Compressed {
			continue
		}
		kept, cut, err := repairTail(e.Path)
		if err != nil {
			return err
		}
		if kept > 0 {
			s.event(index.LevelWarn, index.KindRecover, e.DeviceID,
				fmt.Sprintf("%s ended in a partial line (%d bytes, likely an unclean stop); kept it as its own line", e.Rel, kept))
		}
		if cut > 0 {
			s.event(index.LevelWarn, index.KindRecover, e.DeviceID,
				fmt.Sprintf("%s ended in %d bytes of an unreadable partial line (likely an unclean stop); removed them", e.Rel, cut))
		}
	}

	res, err := s.idx.Reconcile(ctx, s.dataDir)
	if err != nil {
		return err
	}
	if res.Scanned > 0 || res.Removed > 0 {
		notes = append(notes, fmt.Sprintf("index: re-read %d files, dropped %d rows for missing files", res.Scanned, res.Removed))
	}
	rows := make(map[string]index.File, len(res.Files))
	for _, f := range res.Files {
		rows[f.Path] = f
	}
	if entries, _, err = logfile.List(s.dataDir); err != nil {
		return err
	}
	today := logfile.DayOf(s.clock())
	var devOrder []string
	byDev := map[string][]logfile.Entry{}
	for _, e := range entries {
		if _, ok := byDev[e.DeviceID]; !ok {
			devOrder = append(devOrder, e.DeviceID)
		}
		byDev[e.DeviceID] = append(byDev[e.DeviceID], e)
	}
	queued := 0
	for _, dev := range devOrder {
		list := byDev[dev]
		var active *logfile.Entry
		for i := range list {
			n := list[i].Name
			if !n.Compressed && n.Part == 0 && n.Day >= today && (active == nil || n.Day > active.Name.Day) {
				active = &list[i]
			}
		}
		var d *device
		if addr, ok := s.deviceAddr(ctx, dev, list); ok {
			d = s.device(addr)
		}
		if d == nil {
			active = nil // without the sender's address we can't append; close it instead
		}
		minDay := ""
		for _, e := range list {
			if active != nil && e.Rel == active.Rel {
				continue
			}
			ce := &catEntry{deviceID: dev, name: e.Name, abs: e.Path, rel: e.Rel, size: e.Size}
			if !e.Name.Compressed {
				ce.rawSize = e.Size
				queued++
			}
			if row, ok := rows[e.Rel]; ok {
				ce.first, ce.last, ce.lines, ce.rawSize = row.FirstReceived, row.LastReceived, row.Lines, row.RawSize
			}
			if ce.last.IsZero() { // no readable lines: age it by its file name
				if t, err := logfile.DayStart(e.Name.Day); err == nil {
					ce.last = t.Add(24*time.Hour - time.Millisecond)
				}
			}
			s.addClosed(ce)
			if e.Name.Part == 0 && nextDay(e.Name.Day) > minDay {
				minDay = nextDay(e.Name.Day)
			}
		}
		if d != nil {
			d.mu.Lock()
			if minDay > d.minDay {
				d.minDay = minDay
			}
			if active != nil {
				row, ok := rows[active.Rel]
				if err := d.loadActiveLocked(ctx, *active, row, ok); err != nil {
					d.mu.Unlock()
					return err
				}
			}
			d.mu.Unlock()
		}
	}
	if len(notes) > 0 {
		s.event(index.LevelWarn, index.KindRecover, "", "startup recovery: "+strings.Join(notes, "; "))
	}
	if queued > 0 {
		s.log.Info("store: closed files waiting for compression", "files", queued)
	}
	return nil
}

// loadActiveLocked makes an existing file the device's active file again.
func (d *device) loadActiveLocked(ctx context.Context, e logfile.Entry, row index.File, hasRow bool) error {
	d.startDayLocked(e.Name.Day)
	d.needResync = true
	if hasRow && row.Size == e.Size {
		d.w = fileState{size: row.Size, lines: row.Lines, first: row.FirstReceived, last: row.LastReceived}
		last, ok, err := d.s.idx.LastCheckpoint(ctx, row.ID)
		if err != nil {
			return err
		}
		if ok {
			d.wcp = logfile.ResumeCheckpointer(last, row.LastReceived)
			d.needResync = false
		}
	}
	if d.needResync {
		d.w = fileState{}
		if err := d.resyncLocked(); err != nil {
			return err
		}
		if d.w.lines > 0 {
			d.s.pushOps(index.FileOp{Kind: index.OpUpsert, File: d.fileRow(), Checkpoints: d.newCPs, ResetCheckpoints: true})
		}
		d.newCPs, d.resetCPs = nil, false
	} else {
		d.s.activeBytes.Add(d.w.size)
	}
	d.p, d.cp = d.w, d.wcp
	return nil
}

// deviceAddr finds the sender address of a device directory: from the index, from
// the directory name, or from the first line of one of its files.
func (s *Store) deviceAddr(ctx context.Context, dev string, list []logfile.Entry) (netip.Addr, bool) {
	try := func(ip string) (netip.Addr, bool) {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			return netip.Addr{}, false
		}
		a = a.Unmap()
		return a, model.DeviceID(a) == dev
	}
	if dv, err := s.idx.Device(ctx, dev); err == nil {
		if a, ok := try(dv.IP); ok {
			return a, true
		}
	}
	if a, ok := try(dev); ok {
		return a, true
	}
	if a, ok := try(strings.ReplaceAll(dev, "_", ":")); ok {
		return a, true
	}
	for _, e := range list {
		if a, ok := firstSender(e.Path); ok {
			if a, ok := try(a.String()); ok {
				return a, true
			}
		}
	}
	return netip.Addr{}, false
}

func firstSender(path string) (netip.Addr, bool) {
	rc, err := logfile.Open(path)
	if err != nil {
		return netip.Addr{}, false
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for i := 0; i < 100 && sc.Scan(); i++ {
		if l, err := logfile.ParseLine(sc.Bytes()); err == nil {
			return l.Sender, true
		}
	}
	return netip.Addr{}, false
}

// repairTail fixes a file whose last line has no newline (a write torn by a crash or
// power loss). If the partial line still starts with a valid timestamp and sender it
// is kept and terminated (the message itself may be cut short); otherwise the
// partial bytes are removed. It returns how many bytes were kept or cut.
func repairTail(path string) (kept, cut int64, err error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, err
		}
		return 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return 0, 0, err
	}
	size := st.Size()
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return 0, 0, err
	}
	if last[0] == '\n' {
		return 0, 0, nil
	}
	const chunk = 64 << 10
	buf := make([]byte, chunk)
	tailStart := int64(0)
	for pos := size; pos > 0; {
		start := max(0, pos-chunk)
		n, err := f.ReadAt(buf[:pos-start], start)
		if err != nil && err != io.EOF {
			return 0, 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			tailStart = start + int64(i) + 1
			break
		}
		pos = start
	}
	tailLen := size - tailStart
	if tailLen <= 1<<20 {
		tail := make([]byte, tailLen)
		if _, err := f.ReadAt(tail, tailStart); err != nil && err != io.EOF {
			return 0, 0, err
		}
		if _, perr := logfile.ParseLine(tail); perr == nil && bytes.IndexByte(tail, 0) < 0 {
			if _, err := f.WriteAt([]byte{'\n'}, size); err != nil {
				return 0, 0, err
			}
			return tailLen, 0, syncFile(f)
		}
	}
	if err := f.Truncate(tailStart); err != nil {
		return 0, 0, err
	}
	return 0, tailLen, syncFile(f)
}

// sameContent reports whether a plain file and a gzip hold the same bytes.
func sameContent(plain, gz string) (bool, error) {
	a, an, err := hashPlain(plain)
	if err != nil {
		return false, err
	}
	b, bn, err := hashGzip(gz)
	if err != nil {
		return false, nil // an unreadable gzip is not the same
	}
	return an == bn && bytes.Equal(a, b), nil
}
