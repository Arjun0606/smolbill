package api

import (
	"sync"
	"time"
)

// rateLimiter is a tiny per-key token bucket — enough to stop a single client from
// hammering the ingest hot path, with no external dependency. Keyed by API key (or
// remote IP when unauthenticated). Real wall-clock time; this is infra, not billing
// math, so it never touches the deterministic engine.
type rateLimiter struct {
	mu      sync.Mutex
	rps     float64
	burst   float64
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	if burst < 1 {
		burst = 1
	}
	return &rateLimiter{rps: rps, burst: float64(burst), buckets: map[string]*bucket{}}
}

// allow reports whether a request for key may proceed, consuming one token.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b := rl.buckets[key]
	if b == nil {
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	// Refill at rps, capped at burst.
	b.tokens += now.Sub(b.last).Seconds() * rl.rps
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
