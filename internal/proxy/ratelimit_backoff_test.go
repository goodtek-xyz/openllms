package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A 429 that carries no wait cools the account on the SDK back-off: ceilings 0.5 s, 1 s, 2 s, 4 s, 8 s, then 8 s, full
// jitter below each; an answer (the dispatch deletes the entry) starts over. Other statuses keep their own rules.
func TestCooldownAfterBacksOffWithoutProviderWait(t *testing.T) {
	e := &Engine{}
	acc := uuid.New()
	bare := &Result{StatusCode: http.StatusTooManyRequests, Header: http.Header{}, Body: []byte(`{"error":{"type":"rate_limit_error"}}`)}
	for i, ceil := range []time.Duration{500, 1000, 2000, 4000, 8000, 8000} {
		ceil *= time.Millisecond
		if d := e.cooldownAfter(acc, bare); d < 0 || d > ceil {
			t.Fatalf("429 #%d: cooldown %v, want within [0, %v]", i+1, d, ceil)
		}
	}
	e.rateLimited.Delete(acc) // what the dispatch does after a successful answer
	if d := e.cooldownAfter(acc, bare); d > rateBackoffBase {
		t.Fatalf("after an answer: %v, want within the first step %v", d, rateBackoffBase)
	}
	other := uuid.New()
	if d := e.cooldownAfter(other, bare); d > rateBackoffBase {
		t.Fatalf("levels are per account: %v", d)
	}
	withWait := &Result{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"30"}}}
	if d := e.cooldownAfter(acc, withWait); d != 30*time.Second {
		t.Fatalf("Retry-After 30: %v", d)
	}
	if d := e.cooldownAfter(acc, &Result{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}}); d != 30*time.Second {
		t.Fatalf("503 keeps the status table: %v", d)
	}
}
