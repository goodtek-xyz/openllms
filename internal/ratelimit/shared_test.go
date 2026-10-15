package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeCounter struct {
	mu   sync.Mutex
	n    map[string]int64
	fail bool
}

func (f *fakeCounter) Incr(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return 0, errors.New("down")
	}
	if f.n == nil {
		f.n = map[string]int64{}
	}
	f.n[key]++
	return f.n[key], nil
}

func (f *fakeCounter) PExpire(context.Context, string, time.Duration) error { return nil }

// Two replicas on one store share the key's count; when the store fails each falls back to its own window.
func TestSharedLimiterCountsAcrossReplicas(t *testing.T) {
	store := &fakeCounter{}
	now := time.Unix(1_000_000, 0)
	a, b := NewShared(store, 3, time.Minute), NewShared(store, 3, time.Minute)
	a.now, b.now = func() time.Time { return now }, func() time.Time { return now }
	got := []bool{a.Allow("k"), b.Allow("k"), a.Allow("k"), b.Allow("k")}
	if want := []bool{true, true, true, false}; got[3] != want[3] || !got[0] || !got[1] || !got[2] {
		t.Fatalf("allows %v, want %v (4th hit over the shared limit of 3)", got, want)
	}
	if !a.Allow("other") {
		t.Fatal("another key has its own count")
	}
	now = now.Add(time.Minute)
	if !b.Allow("k") {
		t.Fatal("next window starts a new count")
	}
	store.fail = true
	for i := 0; i < 3; i++ {
		if !a.Allow("k") {
			t.Fatalf("fallback hit %d: want the local window to allow", i+1)
		}
	}
	if a.Allow("k") {
		t.Fatal("fallback still enforces the limit per replica")
	}
	if !a.AllowWithLimit("plan", 1) || a.AllowWithLimit("plan", 1) {
		t.Fatal("per-call limit applies in fallback too")
	}
}
