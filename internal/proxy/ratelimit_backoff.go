package proxy

import (
	"net/http"
	"sync"
	"time"

	"github.com/goodtek-xyz/openllms/internal/backoff"
	"github.com/goodtek-xyz/openllms/internal/router"
	"github.com/google/uuid"
)

// When a 429 says nothing about how long to wait, the account cools down on a growing back-off. Its numbers are the
// official SDKs' retry delays: INITIAL_RETRY_DELAY 0.5 s, MAX_RETRY_DELAY 8 s (anthropics/anthropic-sdk-python and
// openai/openai-python src/*/_constants.py @ 5fe6a7531f / 9998c1a380); OpenAI: "If Retry-After is missing, increase
// the delay between retries and add a small random delay" (https://developers.openai.com/api/docs/guides/rate-limits).
// The jitter is full jitter (internal/backoff). Levels are per account and per gateway process; any answer resets.
const (
	rateBackoffBase = 500 * time.Millisecond
	rateBackoffCap  = 8 * time.Second
)

type rateBackoff struct {
	mu sync.Mutex
	b  backoff.Backoff
}

// cooldownAfter is how long acc stays out of routing after the failed result res.
func (e *Engine) cooldownAfter(acc uuid.UUID, res *Result) time.Duration {
	if res.StatusCode != http.StatusTooManyRequests {
		return router.CooldownFromHeaders(res.StatusCode, res.Header, 0)
	}
	if d, ok := router.RateLimitWait(res.Header, res.Body, time.Now()); ok {
		return d
	}
	v, _ := e.rateLimited.LoadOrStore(acc, &rateBackoff{b: backoff.Backoff{Base: rateBackoffBase, Cap: rateBackoffCap}})
	rb := v.(*rateBackoff)
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.b.Next()
}
