package ratelimit

import (
	"context"
	"strconv"
	"time"

	"github.com/goodtek-xyz/openllms/internal/valkey"
)

// Limiter is what the gateway asks before serving a key.
type Limiter interface {
	Allow(key string) bool
	AllowWithLimit(key string, limit int) bool
}

// Counter is the shared store a SharedLimiter counts in (a *valkey.Client).
type Counter interface {
	Incr(ctx context.Context, key string) (int64, error)
	PExpire(ctx context.Context, key string, ttl time.Duration) error
}

// SharedLimiter is the same fixed window as MemoryLimiter, counted in Valkey so every gateway replica sees one
// count per key. Whenever the store errors (down, slow, misconfigured) it answers from its process-local limiter, so
// an outage of the shared store degrades to the per-replica limit instead of blocking or opening traffic.
type SharedLimiter struct {
	store  Counter
	local  *MemoryLimiter
	limit  int
	window time.Duration
	prefix string
	now    func() time.Time
}

var _ Counter = (*valkey.Client)(nil)

func NewShared(store Counter, limit int, window time.Duration) *SharedLimiter {
	local := NewMemory(limit, window)
	return &SharedLimiter{store: store, local: local, limit: local.limit, window: local.window, prefix: "llms:rl:", now: time.Now}
}

func (s *SharedLimiter) Allow(key string) bool { return s.AllowWithLimit(key, s.limit) }

func (s *SharedLimiter) AllowWithLimit(key string, limit int) bool {
	if limit <= 0 {
		limit = s.limit
	}
	now := s.now()
	slot := now.UnixNano() / int64(s.window)
	k := s.prefix + key + ":" + strconv.FormatInt(slot, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	n, err := s.store.Incr(ctx, k)
	if err != nil {
		return s.local.AllowWithLimit(key, limit)
	}
	if n == 1 {
		// The window's first hit sets its expiry; two windows keep the key until no replica can still be in it.
		_ = s.store.PExpire(ctx, k, 2*s.window)
	}
	return n <= int64(limit)
}
