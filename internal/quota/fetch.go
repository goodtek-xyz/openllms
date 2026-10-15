package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Window is one rate-limit window (5h or 7d).
type Window struct {
	RemainingPct float64
	ResetAt      *time.Time
}

// Snapshot is remaining headroom for routing / status.
// RemainingPct/ResetAt/Source mirror the tightest (highest utilization) window.
// Window5h / Window7d hold both provider windows when present.
type Snapshot struct {
	RemainingPct float64
	ResetAt      *time.Time
	Source       string // e.g. codex:7d
	Window5h     *Window
	Window7d     *Window
}

// HTTPClient is overridable in tests.
var HTTPClient *http.Client

func client() *http.Client {
	if HTTPClient != nil {
		return HTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

var (
	CodexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
	ClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
)

// UsageError is a non-2xx answer of a usage endpoint.
type UsageError struct {
	Vendor string // "codex" or "claude"
	Status int
	Text   string // status line + truncated body
}

func (e *UsageError) Error() string { return e.Vendor + " usage: " + e.Text }

// FetchCodex calls ChatGPT Codex usage (undocumented; same as Codex CLI).
func FetchCodex(ctx context.Context, accessToken, accountID string) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CodexUsageURL, nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	req.Header.Set("User-Agent", "codex-cli")
	res, err := client().Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return Snapshot{}, &UsageError{Vendor: "codex", Status: res.StatusCode, Text: res.Status + " " + truncate(b, 200)}
	}
	return ParseCodexUsage(b)
}

// FetchClaude calls Anthropic oauth usage (undocumented; same as Claude Code /usage).
func FetchClaude(ctx context.Context, accessToken string) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ClaudeUsageURL, nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/2.1.72")
	req.Header.Set("Accept", "application/json")
	res, err := client().Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return Snapshot{}, &UsageError{Vendor: "claude", Status: res.StatusCode, Text: res.Status + " " + truncate(b, 200)}
	}
	return ParseClaudeUsage(b)
}

func ParseCodexUsage(b []byte) (Snapshot, error) {
	var out struct {
		RateLimit struct {
			PrimaryWindow   *codexWindow `json:"primary_window"`
			SecondaryWindow *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Snapshot{}, err
	}
	w5 := windowFromCodex(out.RateLimit.PrimaryWindow)
	w7 := windowFromCodex(out.RateLimit.SecondaryWindow)
	return assembleSnapshot("codex", w5, w7)
}

type codexWindow struct {
	UsedPercent float64 `json:"used_percent"`
	ResetAt     int64   `json:"reset_at"`
}

func windowFromCodex(w *codexWindow) *Window {
	if w == nil {
		return nil
	}
	return &Window{
		RemainingPct: clampPct(100 - w.UsedPercent),
		ResetAt:      unixOrNil(w.ResetAt),
	}
}

func ParseClaudeUsage(b []byte) (Snapshot, error) {
	var out struct {
		FiveHour *claudeBucket `json:"five_hour"`
		SevenDay *claudeBucket `json:"seven_day"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Snapshot{}, err
	}
	w5 := windowFromClaude(out.FiveHour)
	w7 := windowFromClaude(out.SevenDay)
	return assembleSnapshot("claude", w5, w7)
}

type claudeBucket struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

func windowFromClaude(w *claudeBucket) *Window {
	if w == nil {
		return nil
	}
	return &Window{
		RemainingPct: clampPct(100 - w.Utilization),
		ResetAt:      parseRFC3339(w.ResetsAt),
	}
}

func assembleSnapshot(vendor string, w5, w7 *Window) (Snapshot, error) {
	if w5 == nil && w7 == nil {
		return Snapshot{}, fmt.Errorf("%s usage: no windows", vendor)
	}
	snap := Snapshot{Window5h: w5, Window7d: w7}
	// Tightest = highest utilization = lowest remaining.
	type cand struct {
		label string
		w     *Window
	}
	cands := []cand{{"5h", w5}, {"7d", w7}}
	bestRem := 101.0
	found := false
	for _, c := range cands {
		if c.w == nil {
			continue
		}
		found = true
		if c.w.RemainingPct < bestRem {
			bestRem = c.w.RemainingPct
			snap.RemainingPct = c.w.RemainingPct
			snap.ResetAt = c.w.ResetAt
			snap.Source = vendor + ":" + c.label
		}
	}
	if !found {
		return Snapshot{}, fmt.Errorf("%s usage: no windows", vendor)
	}
	return snap, nil
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func unixOrNil(sec int64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

func parseRFC3339(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil
		}
	}
	t = t.UTC()
	return &t
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}
