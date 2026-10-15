package proxy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/valkey"
	"github.com/google/uuid"
)

type fakeKV struct {
	mu   sync.Mutex
	m    map[string]string
	fail bool
}

func (f *fakeKV) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", errors.New("down")
	}
	v, ok := f.m[key]
	if !ok {
		return "", valkey.ErrNil
	}
	return v, nil
}

func (f *fakeKV) SetPX(_ context.Context, key, value string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("down")
	}
	if f.m == nil {
		f.m = map[string]string{}
	}
	f.m[key] = value
	return nil
}

// A session pinned on one replica is seen by the other; a store outage falls back to what this replica set itself.
func TestSharedAffinityAcrossReplicas(t *testing.T) {
	kv := &fakeKV{}
	a, b := NewSharedAffinity(kv, time.Hour), NewSharedAffinity(kv, time.Hour)
	acc1, acc2 := uuid.New(), uuid.New()
	a.Set("s1", acc1)
	if got, ok := b.Get("s1"); !ok || got != acc1 {
		t.Fatalf("replica b: %v %v, want %v", got, ok, acc1)
	}
	b.Set("s1", acc2)
	if got, _ := a.Get("s1"); got != acc2 {
		t.Fatalf("replica a after b's update: %v, want %v", got, acc2)
	}
	if _, ok := a.Get("unknown"); ok {
		t.Fatal("unknown key must miss")
	}
	kv.fail = true
	if got, ok := b.Get("s1"); !ok || got != acc2 {
		t.Fatalf("outage: b's own copy %v %v, want %v", got, ok, acc2)
	}
	if _, ok := b.Get("never-set-here"); ok {
		t.Fatal("outage: a key this replica never set misses")
	}
	var _ SessionAffinity = a
	var _ SessionAffinity = NewAffinity(0)
	var _ AffinityKV = (*valkey.Client)(nil)
}
