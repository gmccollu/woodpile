package search

import (
	"bytes"
	"context"
	"errors"
	"math"
	"time"

	"github.com/gmccollu/woodpile/internal/hub"
	"github.com/gmccollu/woodpile/internal/store/logfile"
)

// run is the state of one scan (one search request, or one export device).
type run struct {
	e        *Engine
	ctx      context.Context
	deadline time.Time // zero: no budget
	now      time.Time

	fromMs, toMs int64 // lines with fromMs <= received < toMs
	desc         bool
	m            *matcher
	sevs         hub.SeveritySet // 0: all

	need int // results still wanted (bounds the compressed-file ring)
	// noDeadline is set while re-reading a stream's known next line: that
	// is part of returning a result the previous page found, not new reading.
	noDeadline bool
	scanned    int64 // bytes of uncompressed stream read for matching
	files      map[int64]struct{}
	timedOut   bool
	err        error // a context error

	unesc []byte
}

// expired reports whether the budget or the request is over.
func (r *run) expired() bool {
	if r.timedOut || r.err != nil {
		return true
	}
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return true
	}
	if !r.noDeadline && !r.deadline.IsZero() && time.Now().After(r.deadline) {
		r.timedOut = true
		return true
	}
	return false
}

// hit is a matching line.
type hit struct {
	ms       int64
	off, end int64  // the line's offset, and the offset after its '\n'
	line     []byte // the stored line without '\n'
	esc      []byte // the escaped raw message, within line
	file     *fileInfo
}

// check applies the time range, severity and q to one stored line.
func (r *run) check(line []byte) (int64, []byte, bool) {
	ms, esc, ok := splitLine(line)
	if !ok || ms < r.fromMs || ms >= r.toMs {
		return 0, nil, false
	}
	if r.sevs != 0 || r.m.kind != matchAll {
		raw := esc
		if bytes.IndexByte(esc, '\\') >= 0 {
			r.unesc = logfile.Unescape(r.unesc[:0], esc)
			raw = r.unesc
		}
		if r.sevs != 0 && !r.sevs.Has(Severity(raw)) {
			return 0, nil, false
		}
		if !r.m.match(raw) {
			return 0, nil, false
		}
	}
	return ms, esc, true
}

// lines appends the matching lines of buf (complete lines; buf[0] is at offset
// base) to out, in file order. Lines starting at or after limit (if >= 0) are
// left out. With a prefilter it only looks at lines holding a candidate.
func (r *run) lines(buf []byte, base, limit int64, f *fileInfo, out []hit) []hit {
	add := func(ls, le int) {
		if ms, esc, ok := r.check(buf[ls:le]); ok {
			out = append(out, hit{ms: ms, off: base + int64(ls), end: base + int64(le) + 1, line: buf[ls:le], esc: esc, file: f})
		}
	}
	if r.m.prefilter {
		for i := 0; i < len(buf); {
			j := r.m.next(buf, i)
			if j < 0 {
				break
			}
			ls := bytes.LastIndexByte(buf[:j], '\n') + 1
			if limit >= 0 && base+int64(ls) >= limit {
				break
			}
			le := j + bytes.IndexByte(buf[j:], '\n')
			add(ls, le)
			i = le + 1
		}
		return out
	}
	for ls := 0; ls < len(buf); {
		if limit >= 0 && base+int64(ls) >= limit {
			break
		}
		le := ls + bytes.IndexByte(buf[ls:], '\n')
		add(ls, le)
		ls = le + 1
	}
	return out
}

// stream is one device's matching lines in file order (newest first for desc),
// read lazily a block at a time.
//
// Position: in files[fi], the unread region is [rs, pos) for desc and [pos, re)
// for asc, where [rs, re) is the file's region for the time range (re = -1: the
// end of the file). pos = -1 means the file hasn't been started. For a
// compressed file read desc, hint > 0 means a pass that ran out of time had
// read [rs, hint): the next pass covers [hint, pos) first.
type stream struct {
	dev   string
	files []*fileInfo
	fi    int
	pos   int64
	hint  int64
	done  bool // no files left
	// pending: the previous page found this stream's next line (received at
	// pendingMs) at pos but didn't return it. The merge treats it as the head
	// until the stream reads it again.
	pending   bool
	pendingMs int64

	cur      *opened
	rs, re   int64
	fileDone bool // asc: reached the end of the region
	rd       *fwd
	queue    []hit
	qi       int

	// maxLastBefore[i] is the highest LastReceived (ms) of files[:i];
	// minFirstAfter[i] the lowest FirstReceived of files[i:].
	maxLastBefore, minFirstAfter []int64
}

func newStream(dev string, files []*fileInfo) *stream {
	s := &stream{dev: dev, files: files, pos: -1}
	n := len(files)
	s.maxLastBefore = make([]int64, n+1)
	s.minFirstAfter = make([]int64, n+1)
	s.maxLastBefore[0] = math.MinInt64
	for i, f := range files {
		s.maxLastBefore[i+1] = max(s.maxLastBefore[i], f.LastReceived.UnixMilli())
	}
	s.minFirstAfter[n] = math.MaxInt64
	for i := n - 1; i >= 0; i-- {
		s.minFirstAfter[i] = min(s.minFirstAfter[i+1], files[i].FirstReceived.UnixMilli())
	}
	if n == 0 {
		s.done = true
	}
	return s
}

func (s *stream) head() *hit {
	if s.qi < len(s.queue) {
		return &s.queue[s.qi]
	}
	return nil
}

func (s *stream) pop() hit {
	h := s.queue[s.qi]
	s.queue[s.qi] = hit{}
	s.qi++
	return h
}

func (s *stream) close() {
	if s.cur != nil {
		_ = s.cur.Close()
		s.cur = nil
	}
	s.rd = nil
}

// nextFile moves to the next file in scan order.
func (s *stream) nextFile(desc bool) {
	s.close()
	s.pos, s.hint, s.fileDone = -1, 0, false
	if desc {
		s.fi--
	} else {
		s.fi++
	}
	if s.fi < 0 || s.fi >= len(s.files) {
		s.done = true
	}
}

// finished reports whether the current file's region has been read.
func (s *stream) finished(desc bool) bool {
	if s.cur == nil {
		return false
	}
	if desc {
		return s.pos >= 0 && s.pos <= s.rs && s.hint == 0
	}
	return s.fileDone
}

// key is the stream's merge key: its head's received time, or a bound on any
// line it hasn't read yet (an upper bound for desc, from the checkpoints; a
// lower bound for asc).
func (s *stream) key(r *run) (int64, bool) {
	if h := s.head(); h != nil {
		return h.ms, true
	}
	if s.pending {
		return s.pendingMs, true
	}
	fi := s.files[s.fi]
	if r.desc {
		var b int64
		switch {
		case s.pos < 0 || !fi.loaded:
			b = fi.LastReceived.UnixMilli()
			if fi.mayLag(r.now) {
				b = math.MaxInt64
			}
		default:
			b = math.MaxInt64
			if !fi.mayLag(r.now) {
				b = fi.LastReceived.UnixMilli()
			}
			for _, c := range fi.cps {
				if c.Offset >= s.pos {
					b = c.Received.UnixMilli()
					break
				}
			}
		}
		b = max(b, s.maxLastBefore[s.fi])
		return min(b, r.toMs-1), false
	}
	b := fi.FirstReceived.UnixMilli()
	if s.pos >= 0 && fi.loaded {
		for _, c := range fi.cps {
			if c.Offset > s.pos {
				break
			}
			b = max(b, c.Received.UnixMilli()-maxSkew.Milliseconds())
		}
	}
	b = min(b, s.minFirstAfter[s.fi+1])
	return max(b, r.fromMs), false
}

// step does one unit of reading: it opens the next file or reads one block (a
// whole pass for a compressed file read desc), leaving any matches in the queue.
// It does nothing while the queue holds hits.
func (s *stream) step(r *run) {
	if s.head() != nil || s.done {
		return
	}
	if s.pending {
		// One read finds the line again (or finds it gone); after that the
		// stream is ordinary.
		r.noDeadline = true
		defer func() { r.noDeadline, s.pending = false, false }()
	}
	s.queue, s.qi = s.queue[:0], 0
	if s.finished(r.desc) {
		s.nextFile(r.desc)
		return
	}
	fi := s.files[s.fi]
	if s.cur == nil {
		o, err := r.e.open(r.ctx, fi)
		if err != nil {
			if !errors.Is(err, errGone) {
				r.e.log.Warn("search: skipping a log file", "file", fi.Path, "err", err)
			}
			s.nextFile(r.desc)
			return
		}
		s.cur = o
		r.files[fi.ID] = struct{}{}
		s.rs, s.re = regionStart(fi.cps, r.fromMs), regionEnd(fi.cps, r.toMs)
		if s.pos >= 0 && (s.pos < s.rs || (s.re >= 0 && s.pos > s.re)) {
			// A position outside the region (a hand-made cursor): clamp it.
			s.pos = min(max(s.pos, s.rs), max(s.re, s.rs))
		}
	}
	var err error
	switch {
	case !r.desc:
		err = s.stepAsc(r, fi)
	case s.cur.file.Compressed:
		err = s.passDesc(r, fi)
	default:
		err = s.stepDescPlain(r, fi)
	}
	if err != nil {
		// A file that can't be read (truncated, corrupt, or deleted on an OS that
		// doesn't keep open files readable): keep what was found, skip the rest.
		r.e.log.Warn("search: stopped reading a log file", "file", s.cur.file.Path, "err", err)
		s.nextFileKeepQueue(r.desc)
	}
}

// nextFileKeepQueue skips the rest of the current file once the queue drains.
func (s *stream) nextFileKeepQueue(desc bool) {
	if s.head() == nil {
		s.nextFile(desc)
		return
	}
	if desc {
		s.pos, s.hint = s.rs, 0
	} else {
		s.fileDone = true
	}
}

func (s *stream) stepDescPlain(r *run, fi *fileInfo) error {
	end, tail := s.pos, false
	if end < 0 {
		if s.re >= 0 {
			end = s.re
		} else {
			size, err := s.cur.size()
			if err != nil {
				return err
			}
			end, tail = size, true
		}
	}
	buf, start, err := readBack(s.cur.f, s.rs, end, r.e.block, tail)
	if err != nil {
		return err
	}
	r.scanned += int64(len(buf))
	hits := r.lines(buf, start, -1, fi, s.queue[:0])
	for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
		hits[i], hits[j] = hits[j], hits[i]
	}
	s.queue, s.pos = hits, start
	return nil
}

func (s *stream) stepAsc(r *run, fi *fileInfo) error {
	if s.rd == nil {
		start := s.pos
		if start < 0 {
			start = s.rs
		}
		rd, err := s.cur.reader(start)
		if err != nil {
			return err
		}
		s.rd, s.pos = newFwd(rd, start, r.e.block), start
	}
	buf, off, err := s.rd.next()
	if err != nil {
		return err
	}
	if buf == nil {
		s.fileDone = true
		return nil
	}
	r.scanned += int64(len(buf))
	s.queue = r.lines(buf, off, s.re, fi, s.queue[:0])
	s.pos = off + int64(len(buf))
	if s.re >= 0 && s.pos >= s.re {
		s.pos, s.fileDone = s.re, true
	}
	return nil
}

// passDesc reads [max(rs, hint), pos) of a compressed file forward in one pass
// (gzip can't be read backwards) and queues its last matches, newest first. It
// keeps at most need matches and ringBytes of lines; when it had to drop older
// ones, the next pass covers the region before the oldest it kept.
func (s *stream) passDesc(r *run, fi *fileInfo) error {
	lo := max(s.rs, s.hint)
	hi := s.pos
	if hi < 0 {
		hi = s.re
	}
	rd, err := s.cur.reader(lo)
	if err != nil {
		return err
	}
	fr := newFwd(rd, lo, r.e.block)
	ring := newRing(max(r.need, 1), r.e.ringBytes)
	var tmp []hit
	for blocks := 0; ; blocks++ {
		if blocks > 0 && r.expired() {
			// Out of time: everything in [lo, fr.off) has been seen but nothing can
			// be returned before the rest of the pass. Resume the pass from there.
			s.pos, s.hint = hi, fr.off
			if hi < 0 {
				s.pos = -1
			}
			return nil
		}
		buf, off, err := fr.next()
		if err != nil {
			return err
		}
		if buf == nil {
			break
		}
		if hi >= 0 && off >= hi {
			break
		}
		r.scanned += int64(len(buf))
		tmp = r.lines(buf, off, hi, fi, tmp[:0])
		for _, h := range tmp {
			h.line = bytes.Clone(h.line)
			_, h.esc, _ = splitLine(h.line)
			ring.push(h)
		}
		if hi >= 0 && off+int64(len(buf)) >= hi {
			break
		}
	}
	kept, dropped := ring.items()
	frontier := lo
	if dropped {
		frontier = kept[0].off
	}
	q := s.queue[:0]
	for i := len(kept) - 1; i >= 0; i-- {
		q = append(q, kept[i])
	}
	// Without a drop the pass covered [lo, hi): what's left is [rs, lo), which is
	// empty unless the pass resumed at a hint.
	s.queue, s.pos, s.hint = q, frontier, 0
	return nil
}

// ring keeps the newest n hits within a byte budget.
type ring struct {
	buf      []hit
	head     int
	bytes    int
	max      int
	maxBytes int
	dropped  bool
}

func newRing(n, maxBytes int) *ring { return &ring{max: n, maxBytes: maxBytes} }

func (g *ring) push(h hit) {
	g.buf = append(g.buf, h)
	g.bytes += len(h.line)
	for len(g.buf)-g.head > g.max || (g.bytes > g.maxBytes && len(g.buf)-g.head > 1) {
		g.bytes -= len(g.buf[g.head].line)
		g.buf[g.head] = hit{}
		g.head++
		g.dropped = true
	}
	if g.head > 4096 && g.head*2 > len(g.buf) {
		g.buf = append(g.buf[:0], g.buf[g.head:]...)
		g.head = 0
	}
}

func (g *ring) items() ([]hit, bool) { return g.buf[g.head:], g.dropped }
