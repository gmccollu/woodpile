package search

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/store/logfile"
)

// errGone means a file is no longer there: deleted by retention, or it couldn't
// be found under any of its names. Readers skip it.
var errGone = errors.New("search: file is gone")

// fileInfo is one file of a device's stream, with its checkpoints once loaded.
type fileInfo struct {
	index.File
	cps    []index.Checkpoint
	loaded bool
}

// key orders a device's files: by first received time, then ID (as the index
// does).
func (f *fileInfo) key() fileKey { return fileKey{f.FirstReceived.UnixMilli(), f.ID} }

type fileKey struct {
	first int64
	id    int64
}

func (a fileKey) less(b fileKey) bool {
	if a.first != b.first {
		return a.first < b.first
	}
	return a.id < b.id
}

// mayLag reports whether the index row of f may trail the file on disk: the
// active file (uncompressed, part 0, today or just after midnight) grows
// between the store's index updates.
func (f *fileInfo) mayLag(now time.Time) bool {
	return !f.Compressed && f.Part == 0 && f.Day >= logfile.DayOf(now.Add(-time.Minute))
}

// regionStart is the offset to start reading at for lines received at or after
// fromMs: the last checkpoint whose Received is before it (every line before
// it is older), or 0.
func regionStart(cps []index.Checkpoint, fromMs int64) int64 {
	var off int64
	for _, c := range cps {
		if c.Received.UnixMilli() < fromMs {
			off = c.Offset
		} else {
			break
		}
	}
	return off
}

// regionEnd is the offset to stop reading at for lines received before toMs:
// the first checkpoint whose Received is past toMs by more than maxSkew, or -1
// (the end of the file). Received times within a file are nearly ordered
// (logfile.Checkpoint), so lines after such a checkpoint are all later than
// toMs; maxSkew allows for interleaving and small clock steps.
func regionEnd(cps []index.Checkpoint, toMs int64) int64 {
	for _, c := range cps {
		if c.Received.UnixMilli() > toMs+maxSkew.Milliseconds() {
			return c.Offset
		}
	}
	return -1
}

// maxSkew bounds the end of a scan (regionEnd) and the asc merge's lower
// bound (see logfile.MaxSkew); the start of a scan uses the exact checkpoint
// guarantee.
const maxSkew = logfile.MaxSkew

// opened is an open log file.
type opened struct {
	f     *os.File
	file  index.File // the row it was opened as (Path and Compressed as found on disk)
	ahead *aheadReader
}

func (o *opened) Close() error {
	if o.ahead != nil {
		o.ahead.stop()
		o.ahead = nil
	}
	return o.f.Close()
}

// readAhead is reader for a forward scan: for a compressed file a goroutine
// positions the stream and decompresses a few blocks ahead, so decompression
// runs in parallel with matching and with the other devices' streams. Plain
// files read directly. Only one reader per opened file is in use at a time.
func (o *opened) readAhead(off int64, block int, sem chan struct{}) (io.Reader, error) {
	if !o.file.Compressed {
		return o.reader(off)
	}
	if o.ahead != nil {
		o.ahead.stop()
	}
	a := &aheadReader{ch: make(chan []byte, 4), done: make(chan struct{}), exited: make(chan struct{})}
	o.ahead = a
	go func() {
		defer close(a.exited)
		defer close(a.ch)
		sem <- struct{}{}
		rd, err := o.reader(off)
		<-sem
		if err != nil {
			a.err = err
			return
		}
		for {
			buf := make([]byte, block)
			sem <- struct{}{}
			n, err := io.ReadFull(rd, buf)
			<-sem
			if n > 0 {
				select {
				case a.ch <- buf[:n]:
				case <-a.done:
					return
				}
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				a.err = io.EOF
				return
			}
			if err != nil {
				a.err = err
				return
			}
		}
	}()
	return a, nil
}

// aheadReader is the consumer side of readAhead.
type aheadReader struct {
	ch     chan []byte
	cur    []byte
	err    error // set before ch closes
	done   chan struct{}
	exited chan struct{}
	once   sync.Once
}

func (a *aheadReader) Read(p []byte) (int, error) {
	for len(a.cur) == 0 {
		b, ok := <-a.ch
		if !ok {
			return 0, a.err
		}
		a.cur = b
	}
	n := copy(p, a.cur)
	a.cur = a.cur[n:]
	return n, nil
}

func (a *aheadReader) stop() {
	a.once.Do(func() { close(a.done) })
	<-a.exited
}

// open opens the file with ID id for reading. The index row is looked up just
// before opening, and the file is checked to be the one the row describes:
//
//   - A part rename leaves the active name "<day>.log" to a new file, and the
//     store updates the index a moment later, so the row can briefly point at
//     the wrong file. The first line's received time must equal the first
//     checkpoint's (the time of line 0).
//   - Compression replaces "x.log" with "x.log.gz", so "x.log.gz" is tried too.
//
// When neither name holds the right file it waits briefly for the index to
// catch up, then gives up with errGone. It never returns a different file.
func (e *Engine) open(ctx context.Context, fi *fileInfo) (*opened, error) {
	delay := 20 * time.Millisecond
	for attempt := 0; ; attempt++ {
		row, err := e.idx.File(ctx, fi.ID)
		if errors.Is(err, index.ErrNotFound) {
			return nil, errGone
		}
		if err != nil {
			return nil, err
		}
		if !fi.loaded || row.Path != fi.Path || row.Size != fi.Size {
			// Checkpoints are only ever added (or rebuilt from the same lines), so
			// reload them when the row moved or grew.
			cps, err := e.idx.Checkpoints(ctx, fi.ID)
			if err != nil {
				return nil, err
			}
			fi.cps, fi.loaded = cps, true
		}
		fi.File = row
		names := []string{row.Path}
		if !row.Compressed {
			names = append(names, row.Path+".gz")
		}
		for _, rel := range names {
			o, err := e.tryOpen(rel, row, fi.cps)
			if err == nil {
				return o, nil
			}
			if !errors.Is(err, errMismatch) && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
		if attempt >= 10 || ctx.Err() != nil { // about 1.5 s: the store's index lag is ~1 s
			return nil, errGone
		}
		select {
		case <-ctx.Done():
			return nil, errGone
		case <-time.After(delay):
		}
		delay = min(delay*2, 200*time.Millisecond)
	}
}

var errMismatch = errors.New("search: file doesn't match its index row")

func (e *Engine) tryOpen(rel string, row index.File, cps []index.Checkpoint) (*opened, error) {
	if e.testHookOpen != nil {
		e.testHookOpen(rel)
	}
	f, err := logfile.OpenFile(filepath.Join(e.dataDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	o := &opened{f: f, file: row}
	o.file.Path = rel
	o.file.Compressed = strings.HasSuffix(rel, ".gz")
	if len(cps) > 0 && cps[0].Offset == 0 {
		var head [logfile.TimeLen]byte
		var r io.Reader = f
		var zr *gzip.Reader
		if o.file.Compressed {
			if zr, err = gzip.NewReader(f); err != nil {
				f.Close()
				return nil, errMismatch
			}
			r = zr
		}
		_, err := io.ReadFull(r, head[:])
		ms, ok := parseMillis(head[:])
		if err != nil || !ok || ms != cps[0].Received.UnixMilli() {
			f.Close()
			return nil, errMismatch
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	return o, nil
}

// reader returns a reader of o's uncompressed stream positioned at off (a
// compressed file is decompressed up to it).
func (o *opened) reader(off int64) (io.Reader, error) {
	if !o.file.Compressed {
		if _, err := o.f.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
		return o.f, nil
	}
	if _, err := o.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(o.f)
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if _, err := io.CopyN(io.Discard, zr, off); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	return zr, nil
}

// size is the current length of a plain file.
func (o *opened) size() (int64, error) {
	st, err := o.f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// fwd reads complete lines forward from an offset.
type fwd struct {
	r   io.Reader
	off int64 // offset of buf[0]
	buf []byte
	n   int // valid bytes in buf
	eof bool
}

func newFwd(r io.Reader, off int64, block int) *fwd {
	return &fwd{r: r, off: off, buf: make([]byte, block)}
}

// next returns the next run of complete lines (ending in '\n') and its offset.
// At the end it returns
// a nil slice; a final line without '\n' (being written, or torn) is never
// returned. A line longer than the buffer grows it.
func (r *fwd) next() (lines []byte, off int64, err error) {
	for {
		if r.n > 0 {
			if i := bytes.LastIndexByte(r.buf[:r.n], '\n'); i >= 0 && (r.n == len(r.buf) || r.eof) {
				return r.take(i + 1)
			}
		}
		if r.eof {
			return nil, r.off, nil
		}
		if r.n == len(r.buf) {
			// No newline in a full buffer: a long line. Grow (lines are at most
			// 128 KiB escaped plus the prefix, so this stays small).
			nb := make([]byte, 2*len(r.buf))
			copy(nb, r.buf[:r.n])
			r.buf = nb
		}
		k, err := io.ReadFull(r.r, r.buf[r.n:])
		r.n += k
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			r.eof = true
		} else if err != nil {
			return nil, r.off, err
		}
	}
}

// take returns a copy of buf[:k] (callers keep slices of it) and keeps the rest
// for the next call.
func (r *fwd) take(k int) ([]byte, int64, error) {
	out := make([]byte, k)
	copy(out, r.buf[:k])
	off := r.off
	copy(r.buf, r.buf[k:r.n])
	r.n -= k
	r.off += int64(k)
	return out, off, nil
}

// readBack reads a block of complete lines ending at end (a line start, or the
// end of the file when tail is set, in which case a partial last line is cut
// off), starting at or after lo (a line start). It returns the lines and the
// offset of their first byte; the caller continues with end = that offset.
// When lo == end it returns nothing.
func readBack(f io.ReaderAt, lo, end int64, block int, tail bool) ([]byte, int64, error) {
	for {
		if end <= lo {
			return nil, lo, nil
		}
		start := max(lo, end-int64(block))
		buf := make([]byte, end-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, start, err
		}
		if tail {
			i := bytes.LastIndexByte(buf, '\n')
			if i < 0 {
				if start == lo {
					return nil, lo, nil // only a partial line
				}
				block *= 2
				continue
			}
			end = start + int64(i) + 1
			buf = buf[:i+1]
			tail = false
		}
		if start == lo {
			return buf, start, nil
		}
		// buf[:i+1] is the end of a line that starts before the block.
		i := bytes.IndexByte(buf, '\n')
		if i < 0 || i == len(buf)-1 {
			block *= 2 // no complete line in the block: widen it
			continue
		}
		return buf[i+1:], start + int64(i) + 1, nil
	}
}
