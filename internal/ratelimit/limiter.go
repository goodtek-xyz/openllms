package ratelimit

import (
	"sync"
	"time"
)

// MemoryLimiter is a simple per-key fixed window limiter (process-local).
type MemoryLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	start time.Time
	count int
}

func NewMemory(limit int, window time.Duration) *MemoryLimiter {
	if limit <= 0 {
		limit = 60
	}
	if window <= 0 {
		window = time.Minute
	}
	return &MemoryLimiter{limit: limit, window: window, buckets: map[string]*bucket{}}
}

func (m *MemoryLimiter) Allow(key string) bool {
	return m.AllowWithLimit(key, m.limit)
}

// AllowWithLimit checks a per-key cap; limit<=0 uses the default limit.
func (m *MemoryLimiter) AllowWithLimit(key string, limit int) bool {
	if limit <= 0 {
		limit = m.limit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	b, ok := m.buckets[key]
	if !ok || now.Sub(b.start) >= m.window {
		m.buckets[key] = &bucket{start: now, count: 1}
		return true
	}
	if b.count >= limit {
		return false
	}
	b.count++
	return true
}
