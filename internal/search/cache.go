package search

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// Compressed files can only be read forwards from their start, so paging
// through one would decompress it again for every page. Two small caches keep
// deep pagination linear:
//
//   - passCache keeps the matches of a recent desc pass over a compressed file
//     (up to ringBytes per pass), so the next pages are served from memory
//     until they reach the oldest kept match.
//   - readerCache parks an asc reader positioned where a page stopped, so the
//     next page continues decompressing from there.
//
// Both are keyed by file ID (stable across rename; compressed files never
// change) and, for passes, by the search's parameters and resolved range.
// Losing an entry only costs the re-read.

type passKey struct {
	file int64
	run  string
}

type passEntry struct {
	key      passKey
	lo       int64 // the region start the pass began at
	frontier int64 // hits holds every match in [frontier, end)
	end      int64 // where the pass stopped (a line boundary)
	eof      bool  // the pass ran to the end of the file
	hits     []hit // offset order
	bytes    int64
}

type passCache struct {
	mu    sync.Mutex
	max   int64
	size  int64
	ll    list.List // *passEntry, most recently used first
	byKey map[passKey]*list.Element
	hits  atomic.Int64 // for tests
}

func newPassCache(maxBytes int64) *passCache {
	return &passCache{max: maxBytes, byKey: map[passKey]*list.Element{}}
}

// get returns an entry that can serve the region [lo, pos) (pos -1: the end
// of the file), or nil.
func (c *passCache) get(k passKey, lo, pos int64) *passEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	el := c.byKey[k]
	if el == nil {
		return nil
	}
	e := el.Value.(*passEntry)
	// pos must be past the frontier: at the frontier the entry has nothing left
	// for this region, and serving it again would make no progress.
	if e.lo != lo || (pos < 0 && !e.eof) || (pos >= 0 && (pos <= e.frontier || pos > e.end)) {
		return nil
	}
	c.ll.MoveToFront(el)
	c.hits.Add(1)
	return e
}

func (c *passCache) put(e *passEntry) {
	for _, h := range e.hits {
		e.bytes += int64(len(h.line)) + 80
	}
	if e.bytes > c.max/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.byKey[e.key]; old != nil {
		c.size -= old.Value.(*passEntry).bytes
		c.ll.Remove(old)
	}
	c.byKey[e.key] = c.ll.PushFront(e)
	c.size += e.bytes
	for c.size > c.max {
		last := c.ll.Back()
		le := last.Value.(*passEntry)
		c.ll.Remove(last)
		delete(c.byKey, le.key)
		c.size -= le.bytes
	}
}

// parked is an asc reader of a compressed file left where a page stopped.
type parked struct {
	o       *opened
	rd      *fwd
	last    []byte // the last block it returned, which may hold unreturned hits
	lastOff int64
	timer   *time.Timer
}

type readerCache struct {
	mu     sync.Mutex
	max    int
	ttl    time.Duration
	byFile map[int64][]*parked
	n      int
	order  []*parked    // oldest first
	hits   atomic.Int64 // for tests
}

func newReaderCache(max int, ttl time.Duration) *readerCache {
	return &readerCache{max: max, ttl: ttl, byFile: map[int64][]*parked{}}
}

// take returns a parked reader of file that can continue at off, or nil.
func (c *readerCache) take(file int64, path string, off int64) *parked {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.byFile[file] {
		// last must end exactly where the reader's buffer starts, or the bytes
		// in between would be lost.
		if p.o.file.Path == path && p.lastOff <= off && off <= p.rd.off && p.lastOff+int64(len(p.last)) == p.rd.off {
			c.removeLocked(file, p)
			p.timer.Stop()
			c.hits.Add(1)
			return p
		}
	}
	return nil
}

func (c *readerCache) park(file int64, p *parked) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byFile[file] = append(c.byFile[file], p)
	c.order = append(c.order, p)
	c.n++
	p.timer = time.AfterFunc(c.ttl, func() {
		c.mu.Lock()
		found := c.removeLocked(file, p)
		c.mu.Unlock()
		if found {
			p.o.Close()
		}
	})
	for c.n > c.max {
		old := c.order[0]
		for id, ps := range c.byFile {
			for _, q := range ps {
				if q == old {
					c.removeLocked(id, q)
					q.timer.Stop()
					q.o.Close()
				}
			}
		}
	}
}

func (c *readerCache) removeLocked(file int64, p *parked) bool {
	ps := c.byFile[file]
	for i, q := range ps {
		if q == p {
			c.byFile[file] = append(ps[:i:i], ps[i+1:]...)
			if len(c.byFile[file]) == 0 {
				delete(c.byFile, file)
			}
			for j, o := range c.order {
				if o == p {
					c.order = append(c.order[:j:j], c.order[j+1:]...)
					break
				}
			}
			c.n--
			return true
		}
	}
	return false
}

// prepend puts b (which ends where r's buffer starts) back in front of r, so r
// continues at off.
func (r *fwd) prepend(b []byte, off int64) {
	nb := make([]byte, max(len(r.buf), len(b)+r.n))
	copy(nb, b)
	copy(nb[len(b):], r.buf[:r.n])
	r.buf, r.n, r.off = nb, len(b)+r.n, off
}
