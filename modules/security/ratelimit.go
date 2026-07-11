package security

import (
	"sync"
	"time"
)

type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	count int
	start time.Time
}

func NewLimiter(perMin int) *Limiter {
	if perMin <= 0 {
		perMin = 20
	}
	return &Limiter{
		limit:   perMin,
		window:  time.Minute,
		buckets: map[string]*bucket{},
	}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok || now.Sub(b.start) >= l.window {
		l.buckets[key] = &bucket{count: 1, start: now}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}
