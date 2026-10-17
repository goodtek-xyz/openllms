package router

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryAfter reads the HTTP Retry-After header: delay-seconds or an HTTP-date (RFC 9110 §10.2.3). The provider's
// value is used as given.
func RetryAfter(hdr http.Header, now time.Time) (time.Duration, bool) {
	ra := strings.TrimSpace(hdr.Get("Retry-After"))
	if ra == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(ra); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// RateLimitWait is how long a provider says to wait after a 429, from what it sent; ok is false when it said nothing
// and the caller falls back to back-off. Sources, in order:
//   - Retry-After (RFC 9110; Anthropic and OpenAI rate-limit docs: "wait that many seconds before retrying").
//   - Anthropic `anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-reset`: "The time when the ... rate
//     limit will be fully replenished, provided in RFC 3339 format" — the latest one among the limits whose
//     `-remaining` is 0 (https://platform.claude.com/docs/en/api/rate-limits#response-headers).
//   - OpenAI `x-ratelimit-reset-{requests,tokens,project-tokens}` (durations like "1s", "6m0s") — the longest among the
//     limits whose `x-ratelimit-remaining-*` is 0 (https://developers.openai.com/api/docs/guides/rate-limits).
//   - Anthropic monthly spend cap (`error.details.error_code` = `enforced_spend_limit_reached`): usage "pauses until
//     00:00 UTC on the first day of the next month" (same Anthropic page, "Reaching your spend cap").
//   - ChatGPT Codex usage limit (`error.type` = `usage_limit_reached`, `error.resets_at` in Unix seconds), as the
//     official Codex CLI reads it (openai/codex codex-rs/codex-api/src/api_bridge.rs @ bc9cad3473).
func RateLimitWait(hdr http.Header, body []byte, now time.Time) (time.Duration, bool) {
	if hdr != nil {
		if d, ok := RetryAfter(hdr, now); ok {
			return d, true
		}
		var waits []time.Duration
		for _, l := range []string{"requests", "tokens", "input-tokens", "output-tokens"} {
			if strings.TrimSpace(hdr.Get("anthropic-ratelimit-"+l+"-remaining")) != "0" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(hdr.Get("anthropic-ratelimit-"+l+"-reset"))); err == nil {
				waits = append(waits, max(t.Sub(now), 0))
			}
		}
		for _, l := range []string{"requests", "tokens", "project-tokens"} {
			if strings.TrimSpace(hdr.Get("x-ratelimit-remaining-"+l)) != "0" {
				continue
			}
			if d, err := time.ParseDuration(strings.TrimSpace(hdr.Get("x-ratelimit-reset-" + l))); err == nil && d >= 0 {
				waits = append(waits, d)
			}
		}
		if len(waits) > 0 {
			longest := waits[0]
			for _, w := range waits[1:] {
				longest = max(longest, w)
			}
			return longest, true
		}
	}
	var e struct {
		Error struct {
			Type     string `json:"type"`
			ResetsAt *int64 `json:"resets_at"`
			Details  struct {
				ErrorCode string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return 0, false
	}
	switch {
	case e.Error.Details.ErrorCode == "enforced_spend_limit_reached":
		u := now.UTC()
		next := time.Date(u.Year(), u.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return next.Sub(now), true
	case e.Error.Type == "usage_limit_reached" && e.Error.ResetsAt != nil:
		return max(time.Unix(*e.Error.ResetsAt, 0).Sub(now), 0), true
	}
	return 0, false
}
