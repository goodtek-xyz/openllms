package router

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/google/uuid"
)

type Strategy string

const (
	Sequential Strategy = "sequential"
	RoundRobin Strategy = "round_robin"
	Weighted   Strategy = "weighted"
	QuotaAware Strategy = "quota_aware"
	ResetSoon  Strategy = "reset_soon"
	Steward    Strategy = "steward"
	Parallel   Strategy = "parallel"
)

// ApplyPreset maps friendly names to strategy + optional weight hints in route config.
func ApplyPreset(name string) (Strategy, map[string]any) {
	switch name {
	case "failover":
		return Sequential, map[string]any{"preset": "failover"}
	case "fill-first":
		// Same pick as failover (stable first healthy); product meaning = burn one seat then next.
		return Sequential, map[string]any{"preset": "fill-first"}
	case "balance":
		return RoundRobin, map[string]any{"preset": "balance"}
	case "prefer-primary":
		return Weighted, map[string]any{"preset": "prefer-primary", "primary_weight": 80, "secondary_weight": 20}
	case "quota-first", "quota_aware":
		return QuotaAware, map[string]any{"preset": "quota-first"}
	case "reset-soon":
		return ResetSoon, map[string]any{"preset": "reset-soon", "window": "7d", "min_remaining_pct": 1}
	case "steward":
		return Steward, map[string]any{
			"preset": "steward", "window": "7d",
			// Urgency-weighted so almost-empty + soon-reset can beat full + far-reset.
			"w_remaining": 0.3, "w_urgency": 0.6, "w_soft_fail": 0.1,
			"prefer_oauth": true,
		}
	case "parallel", "race":
		return Parallel, map[string]any{"preset": "parallel"}
	default:
		return Sequential, map[string]any{}
	}
}

// PresetFromRoute recovers the friendly preset name from strategy + config.
func PresetFromRoute(strategy string, configJSON []byte) string {
	var cfg map[string]any
	_ = json.Unmarshal(configJSON, &cfg)
	if p, ok := cfg["preset"].(string); ok && p != "" {
		return p
	}
	switch Strategy(strategy) {
	case RoundRobin:
		return "balance"
	case Weighted:
		return "prefer-primary"
	case QuotaAware:
		return "quota-first"
	case ResetSoon:
		return "reset-soon"
	case Steward:
		return "steward"
	case Parallel:
		return "parallel"
	default:
		return "failover"
	}
}

// AttachWeight returns the route_accounts.weight for position under the given route config.
// prefer-primary uses primary_weight (default 80) for position 0 and secondary_weight (default 20) otherwise.
func AttachWeight(configJSON []byte, position int) int {
	var cfg map[string]any
	_ = json.Unmarshal(configJSON, &cfg)
	return AttachWeightFromConfig(cfg, position)
}

// AttachWeightFromConfig is AttachWeight for an already-parsed config map.
func AttachWeightFromConfig(cfg map[string]any, position int) int {
	if cfg == nil {
		return 1
	}
	preset, _ := cfg["preset"].(string)
	if preset != "prefer-primary" {
		return 1
	}
	primary := configInt(cfg, "primary_weight", 80)
	secondary := configInt(cfg, "secondary_weight", 20)
	if position == 0 {
		return primary
	}
	return secondary
}

func configInt(cfg map[string]any, key string, fallback int) int {
	v, ok := cfg[key]
	if !ok || v == nil {
		return fallback
	}
	switch n := v.(type) {
	case int:
		if n > 0 {
			return n
		}
	case int64:
		if n > 0 {
			return int(n)
		}
	case float64:
		if n > 0 {
			return int(n)
		}
	case json.Number:
		i, err := n.Int64()
		if err == nil && i > 0 {
			return int(i)
		}
	}
	return fallback
}

type Selector struct {
	mu       sync.Mutex
	rr       map[uuid.UUID]*uint64 // routeID -> counter
	rand     *randSource
	softFail map[uuid.UUID]time.Time // accountID -> last soft failure
}

type randSource struct {
	mu sync.Mutex
	n  uint64
}

func (r *randSource) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	r.mu.Lock()
	r.n++
	v := r.n
	r.mu.Unlock()
	// Mix for decent distribution without importing math/rand races in tests.
	v ^= v << 13
	v ^= v >> 7
	v ^= v << 17
	return int(v % uint64(n))
}

func NewSelector() *Selector {
	return &Selector{
		rr:       map[uuid.UUID]*uint64{},
		rand:     &randSource{n: uint64(time.Now().UnixNano())},
		softFail: map[uuid.UUID]time.Time{},
	}
}

const SoftFailWindow = 30 * time.Second

// NoteSoftFail records a recent failure for soft deprioritization (not hard exclude).
func (s *Selector) NoteSoftFail(accountID uuid.UUID) {
	s.mu.Lock()
	s.softFail[accountID] = time.Now()
	s.mu.Unlock()
}

func (s *Selector) ClearSoftFail(accountID uuid.UUID) {
	s.mu.Lock()
	delete(s.softFail, accountID)
	s.mu.Unlock()
}

func (s *Selector) recentlyFailed(accountID uuid.UUID, now time.Time) bool {
	t, ok := s.softFail[accountID]
	if !ok {
		return false
	}
	return now.Sub(t) < SoftFailWindow
}

// PreferAccount moves prefer to the front when present (soft sticky).
func PreferAccount(accounts []store.Account, prefer uuid.UUID) []store.Account {
	if prefer == uuid.Nil || len(accounts) < 2 {
		return accounts
	}
	out := make([]store.Account, 0, len(accounts))
	var found *store.Account
	for i := range accounts {
		if accounts[i].ID == prefer {
			cp := accounts[i]
			found = &cp
			continue
		}
		out = append(out, accounts[i])
	}
	if found == nil {
		return accounts
	}
	return append([]store.Account{*found}, out...)
}

// Exclude removes accounts whose IDs are in tried.
func Exclude(accounts []store.Account, tried map[uuid.UUID]struct{}) []store.Account {
	if len(tried) == 0 {
		return accounts
	}
	out := make([]store.Account, 0, len(accounts))
	for _, a := range accounts {
		if _, ok := tried[a.ID]; ok {
			continue
		}
		out = append(out, a)
	}
	return out
}

// PreferFresh puts accounts without recent soft failures first (stable within each group).
func (s *Selector) PreferFresh(accounts []store.Account) []store.Account {
	if len(accounts) < 2 {
		return accounts
	}
	now := time.Now()
	fresh := make([]store.Account, 0, len(accounts))
	stale := make([]store.Account, 0, len(accounts))
	for _, a := range accounts {
		if s.recentlyFailed(a.ID, now) {
			stale = append(stale, a)
		} else {
			fresh = append(fresh, a)
		}
	}
	return append(fresh, stale...)
}

func (s *Selector) Pick(routeID uuid.UUID, strategy Strategy, accounts []store.Account) *store.Account {
	return s.PickWithConfig(routeID, strategy, accounts, nil)
}

// PickWithConfig is Pick with optional route config (used by reset-soon window / min remaining).
func (s *Selector) PickWithConfig(routeID uuid.UUID, strategy Strategy, accounts []store.Account, configJSON []byte) *store.Account {
	if len(accounts) == 0 {
		return nil
	}
	accounts = s.PreferFresh(accounts)
	switch strategy {
	case RoundRobin:
		return s.pickRR(routeID, accounts)
	case Weighted:
		return s.pickWeighted(accounts)
	case QuotaAware:
		return s.pickQuotaAware(accounts)
	case ResetSoon:
		return s.pickResetSoon(accounts, configJSON)
	case Steward:
		return s.pickSteward(accounts, configJSON)
	default:
		return &accounts[0]
	}
}

func (s *Selector) pickQuotaAware(accounts []store.Account) *store.Account {
	best := &accounts[0]
	bestPct := quotaPct(accounts[0])
	for i := 1; i < len(accounts); i++ {
		p := quotaPct(accounts[i])
		if p > bestPct {
			best = &accounts[i]
			bestPct = p
			continue
		}
		if p < bestPct {
			continue
		}
		// Tie on remaining %: prefer earliest QuotaResetAt (nil sorts last).
		if earlierReset(accounts[i], *best) {
			best = &accounts[i]
		}
	}
	return best
}

func (s *Selector) pickResetSoon(accounts []store.Account, configJSON []byte) *store.Account {
	window := "7d"
	minRem := 1.0
	var cfg map[string]any
	if len(configJSON) > 0 {
		_ = json.Unmarshal(configJSON, &cfg)
		if w, ok := cfg["window"].(string); ok && w != "" {
			window = strings.ToLower(w)
		}
		if v, ok := cfg["min_remaining_pct"]; ok {
			switch n := v.(type) {
			case float64:
				minRem = n
			case int:
				minRem = float64(n)
			case json.Number:
				if f, err := n.Float64(); err == nil {
					minRem = f
				}
			}
		}
	}
	var best *store.Account
	for i := range accounts {
		pct, reset := windowQuota(accounts[i], window)
		if pct < minRem {
			continue
		}
		if reset == nil {
			continue
		}
		if best == nil {
			best = &accounts[i]
			continue
		}
		_, bestReset := windowQuota(*best, window)
		if bestReset == nil || reset.Before(*bestReset) {
			best = &accounts[i]
		}
	}
	if best != nil {
		return best
	}
	// Nobody eligible — fall back to quota-first (remaining + reset tie-break).
	return s.pickQuotaAware(accounts)
}

func windowQuota(a store.Account, window string) (pct float64, reset *time.Time) {
	switch window {
	case "5h", "5hr", "five_hour":
		if a.Quota5hRemainingPct != nil {
			return *a.Quota5hRemainingPct, a.Quota5hResetAt
		}
	default: // 7d
		if a.Quota7dRemainingPct != nil {
			return *a.Quota7dRemainingPct, a.Quota7dResetAt
		}
	}
	// Legacy mirror when dual columns empty.
	if a.QuotaRemainingPct == nil {
		return -1, a.QuotaResetAt
	}
	return *a.QuotaRemainingPct, a.QuotaResetAt
}

func (s *Selector) pickSteward(accounts []store.Account, configJSON []byte) *store.Account {
	window := "7d"
	wR, wU, wS := 0.3, 0.6, 0.1
	preferOAuth := true
	var cfg map[string]any
	if len(configJSON) > 0 {
		_ = json.Unmarshal(configJSON, &cfg)
		if w, ok := cfg["window"].(string); ok && w != "" {
			window = strings.ToLower(w)
		}
		wR = configFloat(cfg, "w_remaining", wR)
		wU = configFloat(cfg, "w_urgency", wU)
		wS = configFloat(cfg, "w_soft_fail", wS)
		if v, ok := cfg["prefer_oauth"].(bool); ok {
			preferOAuth = v
		}
	}
	now := time.Now()
	var best *store.Account
	bestScore := math.Inf(-1)
	for i := range accounts {
		pct, reset := windowQuota(accounts[i], window)
		rem := 0.0
		if pct >= 0 {
			rem = pct / 100.0
		}
		urgency := 0.0
		if reset != nil {
			secs := reset.Sub(now).Seconds()
			if secs < 60 {
				secs = 60
			}
			hours := secs / 3600.0
			// Strong curve: ~0.97 at 30m, ~0.01 at 6d
			urgency = 1.0 / (hours + 0.25)
			if urgency > 1 {
				urgency = 1
			}
		}
		soft := 0.0
		if s.recentlyFailed(accounts[i].ID, now) {
			soft = 1.0
		}
		score := wR*rem + wU*urgency - wS*soft
		if preferOAuth && strings.EqualFold(accounts[i].AuthType, "oauth") {
			score += 0.05
		}
		if score > bestScore {
			bestScore = score
			best = &accounts[i]
		}
	}
	if best != nil {
		return best
	}
	return s.pickQuotaAware(accounts)
}

func configFloat(cfg map[string]any, key string, fallback float64) float64 {
	v, ok := cfg[key]
	if !ok || v == nil {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return fallback
}


func quotaPct(a store.Account) float64 {
	if a.QuotaRemainingPct == nil {
		return -1 // unknown last vs known
	}
	return *a.QuotaRemainingPct
}

// earlierReset reports whether a resets sooner than b. Known reset beats nil.
func earlierReset(a, b store.Account) bool {
	if a.QuotaResetAt == nil {
		return false
	}
	if b.QuotaResetAt == nil {
		return true
	}
	return a.QuotaResetAt.Before(*b.QuotaResetAt)
}

func (s *Selector) pickRR(routeID uuid.UUID, accounts []store.Account) *store.Account {
	s.mu.Lock()
	c, ok := s.rr[routeID]
	if !ok {
		var z uint64
		c = &z
		s.rr[routeID] = c
	}
	s.mu.Unlock()
	n := atomic.AddUint64(c, 1) - 1
	return &accounts[int(n%uint64(len(accounts)))]
}

func (s *Selector) pickWeighted(accounts []store.Account) *store.Account {
	total := 0
	for _, a := range accounts {
		w := a.Weight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	r := s.rand.Intn(total)
	for i := range accounts {
		w := accounts[i].Weight
		if w <= 0 {
			w = 1
		}
		if r < w {
			return &accounts[i]
		}
		r -= w
	}
	return &accounts[len(accounts)-1]
}

// IsRetryableStatus reports whether the gateway should soft-fail this account
// and try another seat in the route pool (and eventually fallback_slugs).
//
// Includes auth failures (401/403): a dead Codex OAuth seat must not pin the
// whole route when Claude or another account can still serve the request.
// Invalid client requests (400) stay non-retryable.
func IsRetryableStatus(code int) bool {
	switch code {
	case 401, 403, 429:
		return true
	default:
		return code >= 500
	}
}

// IsSuccessStatus is a completed upstream response that should stick affinity.
func IsSuccessStatus(code int) bool {
	return code >= 200 && code < 300
}

// CooldownForStatus returns hard-cooldown duration. backoffLevel increases for repeated 429s (0-based).
func CooldownForStatus(httpStatus int, backoffLevel int) time.Duration {
	switch httpStatus {
	case 401:
		return 5 * time.Minute
	case 402, 403:
		return 30 * time.Minute
	case 429:
		return ExponentialBackoff(backoffLevel)
	case 500, 502, 504:
		return 10 * time.Second
	case 503:
		return 30 * time.Second
	default:
		if httpStatus >= 400 {
			return 30 * time.Second
		}
		return 5 * time.Second
	}
}

func ExponentialBackoff(level int) time.Duration {
	if level < 0 {
		level = 0
	}
	d := time.Duration(math.Pow(2, float64(level))) * time.Second
	if d > 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}

// CooldownFromHeaders prefers Retry-After (seconds or HTTP date); else status defaults.
func CooldownFromHeaders(status int, hdr http.Header, backoffLevel int) time.Duration {
	if hdr != nil {
		if ra := strings.TrimSpace(hdr.Get("Retry-After")); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
				d := time.Duration(secs) * time.Second
				if d < time.Second {
					d = time.Second
				}
				if d > 2*time.Hour {
					d = 2 * time.Hour
				}
				return d
			}
			if t, err := http.ParseTime(ra); err == nil {
				d := time.Until(t)
				if d < time.Second {
					d = time.Second
				}
				if d > 2*time.Hour {
					d = 2 * time.Hour
				}
				return d
			}
		}
	}
	return CooldownForStatus(status, backoffLevel)
}

const DefaultCooldown = 30 * time.Second
