package billing

import "time"

type Plan string

const (
	PlanNone  Plan = "none"
	PlanFree  Plan = "free"
	PlanTrial Plan = "trial" // deprecated DB/API alias — canonicalizes to free (not a timed trial)

	PlanStarter Plan = "starter"
	PlanPro     Plan = "pro"
)

// Limits for a plan.
// SoftCap 0 = no monthly token soft-cap.
// Accounts/Routes/Keys 0 = unlimited (gates skip when limit <= 0).
// RPM 0 = fall back to server default rate limit.
type Limits struct {
	Accounts int
	Routes   int
	Keys     int
	RPM      int
	SoftCap  int64 // tokens / calendar month; 0 = none
}

var limits = map[Plan]Limits{
	// Free: soft-cap only conversion lever; everything else capped at 1.
	PlanFree: {Accounts: 1, Routes: 1, Keys: 1, RPM: 30, SoftCap: 5_000_000},
	// Legacy rows still store "trial" — same shape as free.
	PlanTrial: {Accounts: 1, Routes: 1, Keys: 1, RPM: 30, SoftCap: 5_000_000},
	// Starter: no soft-cap; slightly more seats than free. RPM only on Free.
	PlanStarter: {Accounts: 3, Routes: 3, Keys: 3, RPM: 0, SoftCap: 0},
	// Pro: no soft-cap; no resource caps (0 = unlimited).
	PlanPro: {Accounts: 0, Routes: 0, Keys: 0, RPM: 0, SoftCap: 0},
}

func (p Plan) Limits() Limits {
	if l, ok := limits[p.Canonical()]; ok {
		return l
	}
	return Limits{}
}

func (p Plan) Canonical() Plan {
	if p == PlanTrial {
		return PlanFree
	}
	return p
}

func (p Plan) PriceUSD() float64 {
	switch p.Canonical() {
	case PlanStarter:
		return 5
	case PlanPro:
		return 9
	default:
		return 0
	}
}

func (p Plan) ValidPaid() bool {
	c := p.Canonical()
	return c == PlanStarter || c == PlanPro
}

// TrialDays kept for older API/docs references; free plan has no trial window.
const TrialDays = 0
const UnifiGrace = 3 * 24 * time.Hour

func NormalizePlan(s string) Plan {
	switch Plan(s) {
	case PlanFree, PlanTrial, PlanStarter, PlanPro, PlanNone:
		return Plan(s).Canonical()
	default:
		return PlanNone
	}
}
