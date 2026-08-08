package security

import (
	"sync"
	"time"
)

const maxLimiterBuckets = 65536

type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
	nextGC  time.Time
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
		nextGC:  time.Now().Add(time.Minute),
	}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if !now.Before(l.nextGC) {
		for key, candidate := range l.buckets {
			if now.Sub(candidate.start) >= l.window {
				delete(l.buckets, key)
			}
		}
		l.nextGC = now.Add(l.window)
	}
	b, ok := l.buckets[key]
	if ok && now.Sub(b.start) >= l.window {
		l.buckets[key] = &bucket{count: 1, start: now}
		return true
	}
	if !ok {
		if len(l.buckets) >= maxLimiterBuckets {
			return false
		}
		l.buckets[key] = &bucket{count: 1, start: now}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}
