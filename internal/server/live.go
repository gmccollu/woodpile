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
	mu   sync.RWMutex
	devs map[netip.Addr]*liveDevice
}

// rateSeconds is the window of the live rate.
const rateSeconds = 60

type liveDevice struct {
	mu    sync.Mutex
	first int64 // unix ms of the first message since start
	last  int64 // unix ms of the latest message
	// Per-second counts in a ring, each slot tagged with its unix second.
	count [rateSeconds]int64
	sec   [rateSeconds]int64
}

func newLiveDevices() *liveDevices { return &liveDevices{devs: map[netip.Addr]*liveDevice{}} }

// add counts one message from addr received at t.
func (l *liveDevices) add(addr netip.Addr, t time.Time) {
	l.mu.RLock()
	d := l.devs[addr]
	l.mu.RUnlock()
	if d == nil {
		l.mu.Lock()
		if d = l.devs[addr]; d == nil {
			d = &liveDevice{}
			l.devs[addr] = d
		}
		l.mu.Unlock()
	}
	ms := t.UnixMilli()
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
