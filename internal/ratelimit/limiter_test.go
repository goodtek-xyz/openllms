package ratelimit_test

import (
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/ratelimit"
)

func TestAllow(t *testing.T) {
	l := ratelimit.NewMemory(2, time.Minute)
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("first two")
	}
	if l.Allow("a") {
		t.Fatal("third should deny")
	}
	if !l.Allow("b") {
		t.Fatal("other key")
	}
}
