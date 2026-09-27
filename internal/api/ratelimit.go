package api

import (
	"sync"
	"time"
)

// Limiter is an in-memory sliding-window rate limiter keyed by string
// (email, IP, agent key…). Single node by design, so memory is enough.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
	now    func() time.Time
	sweeps int
}

// NewLimiter allows max events per window per key.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, hits: map[string][]time.Time{}, now: time.Now}
}

// Allow records an event for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cut := now.Add(-l.window)
	h := l.hits[key]
	i := 0
	for i < len(h) && !h[i].After(cut) {
		i++
	}
	h = h[i:]
	if len(h) >= l.max {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	if l.sweeps++; l.sweeps%1024 == 0 {
		for k, v := range l.hits {
			if len(v) == 0 || !v[len(v)-1].After(cut) {
				delete(l.hits, k)
			}
		}
	}
	return true
}
