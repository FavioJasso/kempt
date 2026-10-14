package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter is a per-key token bucket: capacity tokens, refilled evenly over
// period. Idle buckets are forgotten after a while.
type limiter struct {
	mu       sync.Mutex
	capacity float64
	rate     float64 // tokens per second
	buckets  map[string]*bucket
	now      func() time.Time
	lastGC   time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

func newLimiter(capacity int, period time.Duration) *limiter {
	return &limiter{
		capacity: float64(capacity),
		rate:     float64(capacity) / period.Seconds(),
		buckets:  map[string]*bucket{},
		now:      time.Now,
	}
}

// allow takes one token for key. When none is left it returns false and how
// long until the next token.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastGC) > time.Minute {
		l.gc(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, seen: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.capacity, b.tokens+now.Sub(b.seen).Seconds()*l.rate)
	b.seen = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// gc drops buckets that have refilled completely.
func (l *limiter) gc(now time.Time) {
	full := time.Duration(l.capacity / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.seen) > full {
			delete(l.buckets, k)
		}
	}
	l.lastGC = now
}

// clientIP returns the address used for rate limiting. X-Forwarded-For is
// honored only behind a trusted reverse proxy (Railway, Fly, nginx): the
// proxy appends the real client to it, so the right-most entry across every
// X-Forwarded-For line is the one it vouches for. Otherwise a client could
// reset its bucket by sending a different header on each request.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := strings.Join(r.Header.Values("X-Forwarded-For"), ","); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
