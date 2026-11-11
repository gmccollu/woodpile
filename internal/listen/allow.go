package listen

import (
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/gmccollu/woodpile/internal/config"
)

// allowlist is the parsed sender allowlist. Empty allows everyone.
type allowlist struct {
	prefixes []netip.Prefix
}

func newAllowlist(cfg config.Listeners) (*allowlist, error) {
	ps, err := cfg.Prefixes()
	if err != nil {
		return nil, err
	}
	return &allowlist{prefixes: ps}, nil
}

func (a *allowlist) allowed(ip netip.Addr) bool {
	if len(a.prefixes) == 0 {
		return true
	}
	ip = ip.Unmap().WithZone("")
	for _, p := range a.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// hours is a 24-hour counter in hourly buckets: the current hour and the 23
// before it.
type hours struct {
	n     [24]int64
	stamp [24]int64 // hour number (Unix hours) each bucket holds
}

func hourOf(t time.Time) int64 { return t.Unix() / 3600 }

func (h *hours) add(now time.Time, n int64) {
	hr := hourOf(now)
	i := int(hr % 24)
	if h.stamp[i] != hr {
		h.stamp[i], h.n[i] = hr, 0
	}
	h.n[i] += n
}

func (h *hours) sum(now time.Time) int64 {
	hr := hourOf(now)
	var s int64
	for i := range h.n {
		if d := hr - h.stamp[i]; d >= 0 && d < 24 {
			s += h.n[i]
		}
	}
	return s
}

// maxRejectedIPs bounds the per-IP table (docs/api.md): a flood of spoofed UDP
// sources can't grow it without limit.
const maxRejectedIPs = 10000

// rejects counts messages refused by the allowlist, per IP, with the least
// recently seen IP evicted when the table is full.
type rejects struct {
	mu    sync.Mutex
	max   int
	ips   map[netip.Addr]*rejEntry
	head  *rejEntry // most recently seen
	tail  *rejEntry // least recently seen
	all   hours     // every rejection, including evicted IPs
	total int64     // since start
}

type rejEntry struct {
	ip         netip.Addr
	reason     string // of the latest refusal
	h          hours
	last       time.Time
	prev, next *rejEntry
}

func newRejects(max int) *rejects {
	return &rejects{max: max, ips: map[netip.Addr]*rejEntry{}}
}

func (r *rejects) add(ip netip.Addr, now time.Time, n int64, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total += n
	r.all.add(now, n)
	e := r.ips[ip]
	if e == nil {
		if len(r.ips) >= r.max {
			old := r.tail
			r.unlink(old)
			delete(r.ips, old.ip)
		}
		e = &rejEntry{ip: ip}
		r.ips[ip] = e
	} else {
		r.unlink(e)
	}
	r.pushFront(e)
	e.h.add(now, n)
	e.reason = reason
	if now.After(e.last) {
		e.last = now
	}
}

func (r *rejects) unlink(e *rejEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		r.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		r.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (r *rejects) pushFront(e *rejEntry) {
	e.next = r.head
	if r.head != nil {
		r.head.prev = e
	}
	r.head = e
	if r.tail == nil {
		r.tail = e
	}
}

// list returns IPs with rejections in the last 24 h, by count descending, then
// most recent first, at most limit.
func (r *rejects) list(now time.Time, limit int) []RejectedSender {
	r.mu.Lock()
	out := make([]RejectedSender, 0, min(len(r.ips), 64))
	// Walk from the most recent; everything after the first entry older than 24 h
	// is older still, so drop it.
	for e := r.head; e != nil; {
		next := e.next
		c := e.h.sum(now)
		if c == 0 && now.Sub(e.last) >= 24*time.Hour {
			for x := e; x != nil; {
				n := x.next
				r.unlink(x)
				delete(r.ips, x.ip)
				x = n
			}
			break
		}
		if c > 0 {
			out = append(out, RejectedSender{IP: e.ip.String(), Count24h: c, LastSeen: e.last.UTC(), Reason: e.reason})
		}
		e = next
	}
	r.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count24h != out[j].Count24h {
			return out[i].Count24h > out[j].Count24h
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (r *rejects) total24h(now time.Time) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.all.sum(now)
}

func (r *rejects) totalCount() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

func (r *rejects) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ips)
}
