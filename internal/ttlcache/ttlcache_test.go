package ttlcache_test

import (
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/ttlcache"
)

func TestCacheHitMissExpire(t *testing.T) {
	c := ttlcache.New[string, string](50 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("empty")
	}
	c.Set("a", "1")
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Fatalf("hit got %q %v", v, ok)
	}
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Fatal("deleted")
	}
	c.Set("b", "2")
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("b"); ok {
		t.Fatal("expired")
	}
}

func TestCacheDisabled(t *testing.T) {
	c := ttlcache.New[string, int](0)
	c.Set("x", 1)
	if _, ok := c.Get("x"); ok {
		t.Fatal("disabled cache should miss")
	}
}
