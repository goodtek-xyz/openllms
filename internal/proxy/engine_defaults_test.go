package proxy

import (
	"sync"
	"testing"

	"github.com/google/uuid"
)

// An Engine built as a literal with the optional collaborators left nil (as the HTTP server does) is used by
// concurrent requests: the defaults must be created once, without a data race (run with -race), and shared.
// Each round uses a fresh Engine and two goroutines that reach the defaults in opposite order, so a check-then-set in
// either accessor races with the other goroutine; few accesses per round keep the race detector's history intact.
func TestEngineDefaultsConcurrent(t *testing.T) {
	id := uuid.New()
	for round := 0; round < 8; round++ {
		e := &Engine{}
		var (
			wg    sync.WaitGroup
			affs  [2]SessionAffinity
			sels  [2]any
			locks [2]*sync.Mutex
		)
		start := make(chan struct{})
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if i == 0 {
					affs[i] = e.affinity()
					sels[i] = e.selector()
				} else {
					sels[i] = e.selector()
					affs[i] = e.affinity()
				}
				locks[i] = e.oauthLock(id)
			}(i)
		}
		close(start)
		wg.Wait()
		if affs[0] == nil || sels[0] == nil || locks[0] == nil {
			t.Fatal("defaults not created")
		}
		if affs[1] != affs[0] || sels[1] != sels[0] || locks[1] != locks[0] {
			t.Fatalf("round %d: the goroutines got different affinity, selector or refresh lock instances", round)
		}
	}
}
