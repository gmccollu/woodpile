package server

import (
	"net/netip"
	"sync"
	"time"

	"github.com/gmccollu/woodpile/internal/model"
)

// liveDevices keeps each sender's last-heard time and a one-minute rate in
// memory, from the sink. The index has the same data, but only as of the store's
// last index update (every 5 s), in whole-minute buckets, so a device list read
// from it shows an active device as heard seconds ago and a rate diluted by the
// partial minute. These counters reset at restart; the index is the fallback.
type liveDevices struct {
	mu        sync.RWMutex
	devs      map[netip.Addr]*liveDevice
	lastSweep int64 // unix ms of the last eviction pass
}

// rateSeconds is the window of the live rate.
const rateSeconds = 60

// liveIdle is how long a sender may be silent before its entry (about 1 KiB)
// is dropped (security review H1: entries used to be kept forever, so a stream
// of spoofed source addresses grew memory without bound). The device list then
// uses the index's last-heard time and rate, as after a restart.
const liveIdle = 10 * time.Minute

type liveDevice struct {
	mu    sync.Mutex
	first int64 // unix ms of the first message since start
	last  int64 // unix ms of the latest message
	// Per-second counts in a ring, each slot tagged with its unix second.
	count [rateSeconds]int64
	sec   [rateSeconds]int64
}

func newLiveDevices() *liveDevices { return &liveDevices{devs: map[netip.Addr]*liveDevice{}} }

// add counts one message from addr received at t. At most once a minute (of
// received time) it also drops senders idle for over liveIdle.
func (l *liveDevices) add(addr netip.Addr, t time.Time) {
	ms := t.UnixMilli()
	l.mu.RLock()
	d := l.devs[addr]
	sweep := ms-l.lastSweep >= time.Minute.Milliseconds()
	l.mu.RUnlock()
	if d == nil || sweep {
		l.mu.Lock()
		if ms-l.lastSweep >= time.Minute.Milliseconds() {
			l.lastSweep = ms
			l.evictLocked(ms)
		}
		if d = l.devs[addr]; d == nil {
			d = &liveDevice{}
			l.devs[addr] = d
		}
		l.mu.Unlock()
	}
	sec := t.Unix()
	i := int(sec % rateSeconds)
	d.mu.Lock()
	if d.first == 0 || ms < d.first {
		d.first = ms
	}
	if ms > d.last {
		d.last = ms
	}
	switch {
	case sec > d.sec[i]: // the slot's second is over a minute old: reuse it
		d.sec[i], d.count[i] = sec, 1
	case sec == d.sec[i]:
		d.count[i]++
	}
	// sec < d.sec[i]: a message received over a minute before a newer one
	// (out-of-order clock reads); it's outside the window anyway.
	d.mu.Unlock()
}

// evictLocked drops every sender last heard more than liveIdle before nowMs.
func (l *liveDevices) evictLocked(nowMs int64) {
	cutoff := nowMs - liveIdle.Milliseconds()
	for a, d := range l.devs {
		d.mu.Lock()
		last := d.last
		d.mu.Unlock()
		if last < cutoff {
			delete(l.devs, a)
		}
	}
}

// len is the number of senders tracked.
func (l *liveDevices) len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.devs)
}

// liveStat is one device's live numbers.
type liveStat struct {
	LastHeard  time.Time
	MsgsPerMin int64
}

// snapshot returns every sender heard since start, by device ID. The rate is the
// messages of the last rateSeconds (the current second included), scaled to a
// minute over the time the device has actually been sending since start (at
// least minRateWindow).
func (l *liveDevices) snapshot(now time.Time) map[string]liveStat {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]liveStat, len(l.devs))
	nowSec := now.Unix()
	for a, d := range l.devs {
		d.mu.Lock()
		var n int64
		for i := range d.sec {
			if s := d.sec[i]; s > nowSec-rateSeconds && s <= nowSec {
				n += d.count[i]
			}
		}
		first, last := d.first, d.last
		d.mu.Unlock()
		window := time.Duration(rateSeconds) * time.Second
		if since := now.Sub(time.UnixMilli(first)); since < window {
			window = max(since, minRateWindow)
		}
		out[model.DeviceID(a)] = liveStat{
			LastHeard:  time.UnixMilli(last).UTC(),
			MsgsPerMin: int64(float64(n)*float64(time.Minute)/float64(window) + 0.5),
		}
	}
	return out
}
