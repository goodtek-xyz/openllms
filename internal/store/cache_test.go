package store

import (
	"context"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/apikey"
	"github.com/google/uuid"
)

func TestLookupByPlaintextUsesCache(t *testing.T) {
	s := NewWithCacheTTL(nil, time.Minute, time.Minute)
	pt := "sk-gt-cache-test-key"
	want := AuthContext{
		KeyID:     uuid.New(),
		ProjectID: uuid.New(),
		KeyName:   "cli",
		Prefix:    "sk-gt",
	}
	s.authCache.Set(apikey.Hash(pt), want)
	got, err := s.LookupByPlaintext(context.Background(), pt)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyID != want.KeyID || got.ProjectID != want.ProjectID {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestInvalidateAuthCache(t *testing.T) {
	s := NewWithCacheTTL(nil, time.Minute, time.Minute)
	hash := apikey.Hash("sk-gt-revoke-test")
	s.authCache.Set(hash, AuthContext{KeyID: uuid.New()})
	if _, ok := s.authCache.Get(hash); !ok {
		t.Fatal("expected cache entry before invalidate")
	}
	s.invalidateAuthCache(hash)
	if _, ok := s.authCache.Get(hash); ok {
		t.Fatal("expected cache entry removed after invalidate")
	}
	// Nil cache must not panic.
	nilCache := &Store{}
	nilCache.invalidateAuthCache(hash)
}

func TestGetRouteBySlugUsesCache(t *testing.T) {
	s := NewWithCacheTTL(nil, time.Minute, time.Minute)
	pid := uuid.New()
	want := Route{
		ID: uuid.New(), ProjectID: pid, Slug: "demo", Strategy: "sequential", Config: []byte("{}"),
	}
	s.routeCache.Set(routeCacheKey(pid, "demo"), want)
	got, err := s.GetRouteBySlug(context.Background(), pid, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Slug != want.Slug {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
