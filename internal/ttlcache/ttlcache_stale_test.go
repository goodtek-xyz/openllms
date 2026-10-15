package ttlcache

import (
	"testing"
	"time"
)

func TestStaleKeepsExpiredValue(t *testing.T) {
	c := New[string, string](time.Nanosecond)
	c.Set("k", "v")
	time.Sleep(time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("Get must miss after the TTL")
	}
	if v, ok := c.Stale("k"); !ok || v != "v" {
		t.Fatalf("Stale = %q %v, want the expired value", v, ok)
	}
	c.Delete("k")
	if _, ok := c.Stale("k"); ok {
		t.Fatal("Stale after Delete must miss")
	}
}
