package router_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/router"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/google/uuid"
)

func accounts(ids ...string) []store.Account {
	out := make([]store.Account, 0, len(ids))
	for i, id := range ids {
		out = append(out, store.Account{ID: uuid.MustParse(id), Weight: 1, Position: i})
	}
	return out
}

func TestSequentialUsesFirst(t *testing.T) {
	a := accounts(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	)
	sel := router.NewSelector()
	rid := uuid.New()
	got := sel.Pick(rid, router.Sequential, a)
	if got.ID != a[0].ID {
		t.Fatalf("want first")
	}
}

func TestRoundRobinRotates(t *testing.T) {
	a := accounts(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	)
	sel := router.NewSelector()
	rid := uuid.New()
	seen := map[uuid.UUID]int{}
	for i := 0; i < 20; i++ {
		got := sel.Pick(rid, router.RoundRobin, a)
		seen[got.ID]++
	}
	if seen[a[0].ID] == 0 || seen[a[1].ID] == 0 {
		t.Fatalf("expected both accounts used: %v", seen)
	}
	if abs(seen[a[0].ID]-seen[a[1].ID]) > 1 {
		t.Fatalf("unbalanced RR: %v", seen)
	}
}

func TestWeightedPrefersHeavy(t *testing.T) {
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Weight: 9},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Weight: 1},
	}
	sel := router.NewSelector()
	rid := uuid.New()
	counts := map[uuid.UUID]int{}
	for i := 0; i < 1000; i++ {
		got := sel.Pick(rid, router.Weighted, a)
		counts[got.ID]++
	}
	if counts[a[0].ID] < 700 {
		t.Fatalf("expected heavy preferred: %v", counts)
	}
}

func TestPresets(t *testing.T) {
	st, _ := router.ApplyPreset("failover")
	if st != router.Sequential {
		t.Fatal(st)
	}
	st, cfg := router.ApplyPreset("prefer-primary")
	if st != router.Weighted || cfg["primary_weight"].(int) != 80 {
		t.Fatalf("%v %v", st, cfg)
	}
}

func TestRetryable(t *testing.T) {
	if !router.IsRetryableStatus(429) || !router.IsRetryableStatus(503) {
		t.Fatal("retryable")
	}
	if !router.IsRetryableStatus(401) || !router.IsRetryableStatus(403) {
		t.Fatal("auth failures must failover to another seat")
	}
	if router.IsRetryableStatus(400) {
		t.Fatal("400 not retryable")
	}
	if !router.IsSuccessStatus(200) || router.IsSuccessStatus(401) {
		t.Fatal("success status")
	}
}

func TestPreferAccount(t *testing.T) {
	a := accounts(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	)
	got := router.PreferAccount(a, a[1].ID)
	if got[0].ID != a[1].ID {
		t.Fatalf("%v", got)
	}
}

func TestExcludeAndPreferFresh(t *testing.T) {
	a := accounts(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	)
	tried := map[uuid.UUID]struct{}{a[0].ID: {}}
	left := router.Exclude(a, tried)
	if len(left) != 1 || left[0].ID != a[1].ID {
		t.Fatalf("%v", left)
	}
	sel := router.NewSelector()
	sel.NoteSoftFail(a[0].ID)
	ordered := sel.PreferFresh(a)
	if ordered[0].ID != a[1].ID {
		t.Fatalf("want fresh first, got %v", ordered)
	}
}

func TestCooldownRetryAfter(t *testing.T) {
	h := make(http.Header)
	h.Set("Retry-After", "120")
	if d := router.CooldownFromHeaders(503, h, 0); d != 120*time.Second {
		t.Fatalf("got %v", d)
	}
	h.Set("Retry-After", "86400") // a day: the provider's value is used as given, no cap
	if d := router.CooldownFromHeaders(503, h, 0); d != 24*time.Hour {
		t.Fatalf("got %v", d)
	}
}

// RateLimitWait follows what the provider sent after a 429, in the documented order.
func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 10, 17, 6, 0, 0, 0, time.UTC)
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name string
		hdr  http.Header
		body string
		want time.Duration
		ok   bool
	}{
		{"retry-after seconds", hdr("Retry-After", "30"), "", 30 * time.Second, true},
		{"retry-after date", hdr("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat)), "", 90 * time.Second, true},
		{"retry-after wins over reset headers", hdr("Retry-After", "5", "anthropic-ratelimit-tokens-remaining", "0",
			"anthropic-ratelimit-tokens-reset", now.Add(time.Hour).Format(time.RFC3339)), "", 5 * time.Second, true},
		{"anthropic: latest reset among exhausted limits", hdr(
			"anthropic-ratelimit-requests-remaining", "0", "anthropic-ratelimit-requests-reset", now.Add(20*time.Second).Format(time.RFC3339),
			"anthropic-ratelimit-output-tokens-remaining", "0", "anthropic-ratelimit-output-tokens-reset", now.Add(45*time.Second).Format(time.RFC3339),
			"anthropic-ratelimit-input-tokens-remaining", "5000", "anthropic-ratelimit-input-tokens-reset", now.Add(time.Hour).Format(time.RFC3339),
		), "", 45 * time.Second, true},
		{"openai: longest reset among exhausted limits", hdr(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1s",
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s",
			"x-ratelimit-remaining-project-tokens", "57000", "x-ratelimit-reset-project-tokens", "3h",
		), "", 6 * time.Minute, true},
		{"anthropic spend cap: next month 00:00 UTC", nil,
			`{"type":"error","error":{"type":"rate_limit_error","details":{"error_code":"enforced_spend_limit_reached"}}}`,
			time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Sub(now), true},
		{"codex usage limit resets_at", nil,
			`{"error":{"type":"usage_limit_reached","resets_at":` + strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10) + `}}`,
			3 * time.Hour, true},
		{"nothing said", hdr("x-ratelimit-remaining-tokens", "10"), `{"error":{"type":"rate_limit_error"}}`, 0, false},
	}
	for _, tc := range cases {
		got, ok := router.RateLimitWait(tc.hdr, []byte(tc.body), now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestQuotaAwarePrefersHigher(t *testing.T) {
	low, high := 10.0, 80.0
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), QuotaRemainingPct: &low},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), QuotaRemainingPct: &high},
	}
	sel := router.NewSelector()
	got := sel.Pick(uuid.New(), router.QuotaAware, a)
	if got.ID != a[1].ID {
		t.Fatalf("want high quota account")
	}
}

func TestQuotaAwareTieBreakEarliestReset(t *testing.T) {
	pct := 50.0
	soon := time.Now().Add(1 * time.Hour)
	later := time.Now().Add(24 * time.Hour)
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), QuotaRemainingPct: &pct, QuotaResetAt: &later},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), QuotaRemainingPct: &pct, QuotaResetAt: &soon},
	}
	sel := router.NewSelector()
	got := sel.Pick(uuid.New(), router.QuotaAware, a)
	if got.ID != a[1].ID {
		t.Fatalf("want earliest reset on tie, got %v", got.ID)
	}
}

func TestQuotaAwareTieBreakNilResetLast(t *testing.T) {
	pct := 40.0
	soon := time.Now().Add(2 * time.Hour)
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), QuotaRemainingPct: &pct, QuotaResetAt: nil},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), QuotaRemainingPct: &pct, QuotaResetAt: &soon},
	}
	sel := router.NewSelector()
	got := sel.Pick(uuid.New(), router.QuotaAware, a)
	if got.ID != a[1].ID {
		t.Fatalf("want known reset over nil, got %v", got.ID)
	}
}

func TestQuotaFirstPreset(t *testing.T) {
	st, cfg := router.ApplyPreset("quota-first")
	if st != router.QuotaAware || cfg["preset"] != "quota-first" {
		t.Fatalf("%v %v", st, cfg)
	}
}

func TestFillFirstPreset(t *testing.T) {
	st, cfg := router.ApplyPreset("fill-first")
	if st != router.Sequential || cfg["preset"] != "fill-first" {
		t.Fatalf("%v %v", st, cfg)
	}
	if got := router.PresetFromRoute("sequential", mustJSON(t, cfg)); got != "fill-first" {
		t.Fatalf("PresetFromRoute: %s", got)
	}
	a := accounts(
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	)
	sel := router.NewSelector()
	got := sel.Pick(uuid.New(), st, a)
	if got.ID != a[0].ID {
		t.Fatalf("fill-first should pick first healthy")
	}
}

func TestAttachWeightPreferPrimary(t *testing.T) {
	_, cfg := router.ApplyPreset("prefer-primary")
	raw := mustJSON(t, cfg)
	if w := router.AttachWeight(raw, 0); w != 80 {
		t.Fatalf("primary weight=%d", w)
	}
	if w := router.AttachWeight(raw, 1); w != 20 {
		t.Fatalf("secondary weight=%d", w)
	}
	if w := router.AttachWeight([]byte(`{"preset":"failover"}`), 0); w != 1 {
		t.Fatalf("non-prefer weight=%d", w)
	}
}

func TestPresetFromRoute(t *testing.T) {
	if got := router.PresetFromRoute("sequential", []byte(`{"preset":"failover"}`)); got != "failover" {
		t.Fatalf("from config: %s", got)
	}
	if got := router.PresetFromRoute("round_robin", nil); got != "balance" {
		t.Fatalf("from strategy: %s", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestResetSoonPreset(t *testing.T) {
	st, cfg := router.ApplyPreset("reset-soon")
	if st != router.ResetSoon || cfg["preset"] != "reset-soon" {
		t.Fatalf("%v %v", st, cfg)
	}
	if got := router.PresetFromRoute(string(st), mustJSON(t, cfg)); got != "reset-soon" {
		t.Fatalf("preset %s", got)
	}
}

func TestResetSoonPicksEarliest7d(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	later := time.Now().Add(48 * time.Hour)
	remA, remB := 40.0, 80.0
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Quota7dRemainingPct: &remA, Quota7dResetAt: &later},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Quota7dRemainingPct: &remB, Quota7dResetAt: &soon},
	}
	sel := router.NewSelector()
	_, cfg := router.ApplyPreset("reset-soon")
	got := sel.PickWithConfig(uuid.New(), router.ResetSoon, a, mustJSON(t, cfg))
	if got.ID != a[1].ID {
		t.Fatalf("want earliest 7d reset, got %v", got.ID)
	}
}

func TestResetSoonSkipsBelowMin(t *testing.T) {
	soon := time.Now().Add(1 * time.Hour)
	later := time.Now().Add(10 * time.Hour)
	low, high := 0.5, 50.0
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Quota7dRemainingPct: &low, Quota7dResetAt: &soon},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Quota7dRemainingPct: &high, Quota7dResetAt: &later},
	}
	sel := router.NewSelector()
	cfg := map[string]any{"preset": "reset-soon", "window": "7d", "min_remaining_pct": 1}
	got := sel.PickWithConfig(uuid.New(), router.ResetSoon, a, mustJSON(t, cfg))
	if got.ID != a[1].ID {
		t.Fatalf("want eligible seat, got %v", got.ID)
	}
}

func TestStewardPreset(t *testing.T) {
	st, cfg := router.ApplyPreset("steward")
	if st != router.Steward || cfg["preset"] != "steward" {
		t.Fatalf("%v %v", st, cfg)
	}
}

func TestStewardPrefersSoonResetAlmostEmpty(t *testing.T) {
	soon := time.Now().Add(30 * time.Minute)
	later := time.Now().Add(6 * 24 * time.Hour)
	low, high := 8.0, 95.0
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), AuthType: "oauth", Quota7dRemainingPct: &high, Quota7dResetAt: &later},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), AuthType: "oauth", Quota7dRemainingPct: &low, Quota7dResetAt: &soon},
	}
	sel := router.NewSelector()
	_, cfg := router.ApplyPreset("steward")
	got := sel.PickWithConfig(uuid.New(), router.Steward, a, mustJSON(t, cfg))
	if got.ID != a[1].ID {
		t.Fatalf("want almost-empty soon-reset seat, got %v", got.ID)
	}
}

func TestStewardPrefersOAuthOverAPIKey(t *testing.T) {
	reset := time.Now().Add(2 * time.Hour)
	rem := 50.0
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), AuthType: "api_key", Quota7dRemainingPct: &rem, Quota7dResetAt: &reset},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), AuthType: "oauth", Quota7dRemainingPct: &rem, Quota7dResetAt: &reset},
	}
	sel := router.NewSelector()
	_, cfg := router.ApplyPreset("steward")
	got := sel.PickWithConfig(uuid.New(), router.Steward, a, mustJSON(t, cfg))
	if got.ID != a[1].ID {
		t.Fatalf("want oauth seat, got %v auth=%s", got.ID, got.AuthType)
	}
}

func TestStewardSoftFailPenalty(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour)
	rem := 60.0
	a := []store.Account{
		{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), AuthType: "oauth", Quota7dRemainingPct: &rem, Quota7dResetAt: &reset},
		{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), AuthType: "oauth", Quota7dRemainingPct: &rem, Quota7dResetAt: &reset},
	}
	sel := router.NewSelector()
	sel.NoteSoftFail(a[0].ID)
	_, cfg := router.ApplyPreset("steward")
	got := sel.PickWithConfig(uuid.New(), router.Steward, a, mustJSON(t, cfg))
	if got.ID != a[1].ID {
		t.Fatalf("want non-soft-failed seat, got %v", got.ID)
	}
}
