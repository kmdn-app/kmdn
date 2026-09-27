package mcp

import (
	"sync"
	"time"
)

// Default limits per key (docs/specs/12-mcp.md#authentication-agent-keys).
const (
	DefaultPerMinute  = 120
	DefaultConcurrent = 10
)

// limiter is a token bucket (refilling perMinute tokens a minute) and a
// concurrency cap per key, in memory (single node).
type limiter struct {
	perMinute, concurrent int
	now                   func() time.Time

	mu   sync.Mutex
	keys map[string]*bucket
}

type bucket struct {
	tokens   float64
	at       time.Time
	inFlight int
	touched  time.Time // last time last_used_at was written
}

func newLimiter(perMinute, concurrent int) *limiter {
	if perMinute <= 0 {
		perMinute = DefaultPerMinute
	}
	if concurrent <= 0 {
		concurrent = DefaultConcurrent
	}
	return &limiter{perMinute: perMinute, concurrent: concurrent, now: time.Now, keys: map[string]*bucket{}}
}

// acquire takes a token and a concurrency slot. When refused, retry says
// how long to wait.
func (l *limiter) acquire(id string) (release func(), retry time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.keys[id]
	if b == nil {
		b = &bucket{tokens: float64(l.perMinute), at: now}
		l.keys[id] = b
	}
	rate := float64(l.perMinute) / 60 // tokens per second
	b.tokens = min(float64(l.perMinute), b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	if b.inFlight >= l.concurrent {
		return nil, time.Second, false
	}
	if b.tokens < 1 {
		return nil, time.Duration((1-b.tokens)/rate*float64(time.Second)) + time.Millisecond, false
	}
	b.tokens--
	b.inFlight++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			b.inFlight--
			l.mu.Unlock()
		})
	}, 0, true
}

// shouldTouch reports whether last use is worth writing (at most every 30 s).
func (l *limiter) shouldTouch(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.keys[id]
	now := l.now()
	if b == nil || now.Sub(b.touched) < 30*time.Second {
		return false
	}
	b.touched = now
	return true
}
