package stats

import (
	"sync"
	"time"
)

// Rate limit for stat reports, per device ID. A box legitimately
// bursts when it syncs a whole library of per-player/per-game totals,
// so the allowance is generous; it exists to stop a runaway or
// hostile client from hammering the endpoint, not to meter honest
// syncs.
const (
	reportLimit  = 300
	reportWindow = time.Minute
)

// deviceLimiter is a sliding-window limiter keyed by device ID. It
// mirrors the lobby server's per-IP limiter, with the addition that
// idle keys are dropped so the map cannot grow without bound as
// devices come and go.
type deviceLimiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
	limit  int
	window time.Duration
	now    func() time.Time
}

func newDeviceLimiter(limit int, window time.Duration) *deviceLimiter {
	return &deviceLimiter{
		events: make(map[string][]time.Time),
		limit:  limit,
		window: window,
		now:    time.Now,
	}
}

// allow records an attempt for key and reports whether it is within
// the limit. Every call counts, accepted or not.
func (l *deviceLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	kept := l.events[key][:0]
	for _, t := range l.events[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.events, key)
	} else {
		l.events[key] = kept
	}
	if len(kept) >= l.limit {
		return false
	}
	l.events[key] = append(kept, now)
	return true
}
