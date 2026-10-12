package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a small sliding-window limiter keyed by an arbitrary
// string (usually client IP, or IP+lobby for join attempts). It exists
// to slow password guessing and lobby spam, not to be a precise quota
// system.
type rateLimiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
	limit  int
	window time.Duration
	now    func() time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		events: make(map[string][]time.Time),
		limit:  limit,
		window: window,
		now:    time.Now,
	}
}

// allow records an attempt for key and reports whether it is within
// the limit. Every call counts, success or failure.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	cutoff := now.Add(-rl.window)
	kept := rl.events[key][:0]
	for _, t := range rl.events[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rl.limit {
		rl.events[key] = kept
		return false
	}
	rl.events[key] = append(kept, now)
	return true
}

// clientIP extracts the host part of the request's remote address.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
