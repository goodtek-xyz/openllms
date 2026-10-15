// Package backoff spaces out retries: exponential growth with a cap and full jitter, so clients that failed together
// (for example during a database switchover) do not all retry at the same moment.
package backoff

import (
	"context"
	"math/rand/v2"
	"time"
)

// Backoff hands out the pauses between retries. Pause n is a uniformly random duration in [0, min(Cap, Base·2^n)]
// ("full jitter"). The zero value is not useful; set Base and Cap. A Backoff is not safe for concurrent use: create
// one per retry loop.
type Backoff struct {
	Base time.Duration
	Cap  time.Duration

	ceil time.Duration // upper bound of the last pause; 0 before the first
}

// Next returns the next pause.
func (b *Backoff) Next() time.Duration {
	switch {
	case b.ceil == 0:
		b.ceil = min(b.Base, b.Cap)
	case b.ceil < b.Cap:
		b.ceil = min(b.ceil*2, b.Cap)
	}
	if b.ceil <= 0 {
		return 0
	}
	return rand.N(b.ceil + 1)
}

// Sleep waits for d or until ctx ends, whichever comes first, and returns ctx.Err() in the latter case.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
