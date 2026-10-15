package ttlcache

import (
	"sync"
	"time"
)

// Cache is a process-local TTL map. Not shared across gateway replicas (use Valkey later if needed).
type Cache[K comparable, V any] struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[K]entry[V]
}

type entry[V any] struct {
	v   V
	exp time.Time
}

func New[K comparable, V any](ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{ttl: ttl, m: make(map[K]entry[V])}
}

// Enabled is false when TTL <= 0 (caching disabled).
func (c *Cache[K, V]) Enabled() bool {
	return c != nil && c.ttl > 0
}

func (c *Cache[K, V]) Get(key K) (V, bool) {
	var zero V
	if !c.Enabled() {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return zero, false
	}
	if time.Now().After(e.exp) {
		return zero, false // kept for Stale; replaced by the next Set or removed by Delete
	}
	return e.v, true
}

// Stale returns the last value set for key even if it has expired (serve-stale-on-error for a source that is down).
func (c *Cache[K, V]) Stale(key K) (V, bool) {
	var zero V
	if !c.Enabled() {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return zero, false
	}
	return e.v, true
}

func (c *Cache[K, V]) Set(key K, val V) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	c.m[key] = entry[V]{v: val, exp: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *Cache[K, V]) Delete(key K) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}
