package backoff

import (
	"context"
	"testing"
	"time"
)

func TestNextGrowsToCap(t *testing.T) {
	b := Backoff{Base: 100 * time.Millisecond, Cap: time.Second}
	want := []time.Duration{100, 200, 400, 800, 1000, 1000}
	for i, w := range want {
		b.Next()
		if b.ceil != w*time.Millisecond {
			t.Fatalf("pause %d: ceiling = %v, want %v", i, b.ceil, w*time.Millisecond)
		}
	}
}

func TestNextBaseAboveCap(t *testing.T) {
	b := Backoff{Base: 5 * time.Second, Cap: time.Second}
	for i := 0; i < 3; i++ {
		if d := b.Next(); d > time.Second {
			t.Fatalf("pause %d = %v, above cap", i, d)
		}
	}
}

func TestNextStaysWithinBoundsAndSpreads(t *testing.T) {
	const samples = 2000
	seen := map[time.Duration]bool{}
	for i := 0; i < samples; i++ {
		b := Backoff{Base: time.Second, Cap: 30 * time.Second}
		for n := 0; n < 8; n++ {
			d := b.Next()
			if d < 0 || d > b.ceil {
				t.Fatalf("pause %d = %v, outside [0, %v]", n, d, b.ceil)
			}
			if n == 0 {
				seen[d] = true
			}
		}
	}
	// Without jitter every first pause would be the same; with full jitter they spread over [0, Base].
	if len(seen) < samples/2 {
		t.Fatalf("only %d distinct first pauses out of %d, want them spread", len(seen), samples)
	}
}

func TestNextZero(t *testing.T) {
	var b Backoff
	if d := b.Next(); d != 0 {
		t.Fatalf("zero Backoff pause = %v, want 0", d)
	}
}

func TestSleep(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("Sleep = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("Sleep on an ended context = %v, want context.Canceled", err)
	}
}
