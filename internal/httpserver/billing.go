package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/goodtek-xyz/openllms/internal/apikey"
	"github.com/goodtek-xyz/openllms/internal/authn"
	"github.com/goodtek-xyz/openllms/internal/billing"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/google/uuid"
)

func (s *Server) billingURL() string {
	return s.cfg.PublicBaseURL + "/billing"
}

func (s *Server) monthUsage(ctx context.Context, projectID uuid.UUID) (requests int64, tokens int64, err error) {
	now := time.Now().UTC()
	since := s.store.MonthStartUTC(now)
	reqs, tin, tout, err := s.store.UsageTotals(ctx, projectID, since)
	if err != nil {
		return 0, 0, err
	}
	return reqs, tin + tout, nil
}

func (s *Server) planSoftCap(ctx context.Context, projectID uuid.UUID) int64 {
	if s.cfg.BillingEnforce {
		b, err := s.store.GetProjectBilling(ctx, projectID)
		if err != nil {
			return 0
		}
		return b.Entitlement().Limits(time.Now().UTC()).SoftCap
	}
	_, softTok, err := s.store.ProjectCaps(ctx, projectID)
	if err != nil || softTok == nil {
		return 0
	}
	return *softTok
}

func (s *Server) billingDeny(w http.ResponseWriter, code int, fields map[string]any) {
	fields["billing"] = s.billingURL()
	writeJSON(w, code, fields)
}

func (s *Server) billingStatusPayload(ctx context.Context, projectID uuid.UUID) (map[string]any, error) {
	b, err := s.store.GetProjectBilling(ctx, projectID)
	if err != nil {
		return nil, err
	}
	ent := b.Entitlement()
	now := time.Now().UTC()
	lim := ent.Limits(now)
	reqs, tokens, _ := s.monthUsage(ctx, projectID)
	softCap := lim.SoftCap
	if !s.cfg.BillingEnforce {
		softCap = s.planSoftCap(ctx, projectID)
	}
	out := map[string]any{
		"plan":           string(ent.EffectivePlan(now)),
		"raw_plan":       string(b.Plan),
		"status":         b.Status,
		"entitled":       ent.Entitled(now),
		"free_used":      b.UserTrialUsed || b.TrialUsed,
		"free_available": !ent.Entitled(now),
		// Legacy trial_* keys kept for older CLI/UI; same values as free_*.
		"trial_used":           b.UserTrialUsed || b.TrialUsed,
		"trial_available":      !ent.Entitled(now),
		"trial_ends_at":        nil, // Free is perpetual; never surface a trial clock
		"period_start":         b.CurrentPeriodStart,
		"period_end":           b.CurrentPeriodEnd,
		"cancel_at_period_end": b.CancelAtPeriodEnd,
		"provider":             b.BillingProvider,
		"limits": map[string]any{
			"accounts": lim.Accounts, "routes": lim.Routes, "keys": lim.Keys,
			"rpm": lim.RPM, "soft_cap_tokens": lim.SoftCap,
		},
		"usage_month": map[string]any{
			"requests":        reqs,
			"tokens_total":    tokens,
			"soft_cap_tokens": softCap,
		},
		"soft_open":       billing.SoftOpen,
		"paid_checkout":   billing.PublicPaidCheckout,
		"rails":           s.paymentRails(),
		"public_base_url": s.cfg.PublicBaseURL,
		"billing_url":     s.billingURL(),
		"crew_signal_url": s.cfg.PublicBaseURL + "/install",
	}
	if billing.PublicPaidCheckout {
		out["prices"] = map[string]any{"starter_usd": 5, "pro_usd": 9}
	}
	return out, nil
}

func (s *Server) handleControlBilling(w http.ResponseWriter, r *http.Request) {
	ac, ok := authn.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	payload, err := s.billingStatusPayload(r.Context(), ac.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "billing_lookup_failed"})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleControlBillingFree(w http.ResponseWriter, r *http.Request) {
	ac, ok := authn.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if err := s.store.EnsureFreePlan(r.Context(), ac.ProjectID, time.Now().UTC()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "billing": s.cfg.PublicBaseURL + "/billing"})
		return
	}
	s.handleControlBilling(w, r)
}

// handleControlBillingTrial is a legacy alias for handleControlBillingFree.
func (s *Server) handleControlBillingTrial(w http.ResponseWriter, r *http.Request) {
	s.handleControlBillingFree(w, r)
}

// requireUserSession reuses admin GitHub session when login matches a user with a project.
// Billing page uses the same allowlist-free device login store as admin when present;
// for general users we accept the admin session cookie only if UpsertGitHubUser exists —
// MVP: any valid admin session OR BILLING_MOCK with X-Project-Id for tests.
type billingAuth struct {
	ProjectID uuid.UUID
	Login     string
}

func (s *Server) requireUserSession(w http.ResponseWriter, r *http.Request) (billingAuth, bool) {
	if s.user != nil && s.user.Enabled() {
		if sess, err := s.user.Session(r); err == nil && sess.Login != "" {
			_, projectID, _, _, err := s.store.UpsertGitHubUser(r.Context(), sess.GitHubID, sess.Login)
			if err == nil {
				return billingAuth{ProjectID: projectID, Login: sess.Login}, true
			}
		}
	}
	if s.store != nil {
		if tok, err := apikey.ParseBearer(r.Header.Get("Authorization")); err == nil {
			ac, err := s.store.LookupByPlaintext(r.Context(), tok)
			if err == nil && ac != nil {
				return billingAuth{ProjectID: ac.ProjectID, Login: ac.KeyName}, true
			}
		}
	}
	if s.admin != nil {
		if sess, err := s.admin.Session(r); err == nil && sess.Login != "" {
			_, projectID, _, _, err := s.store.UpsertGitHubUser(r.Context(), sess.GitHubID, sess.Login)
			if err == nil {
				return billingAuth{ProjectID: projectID, Login: sess.Login}, true
			}
		}
	}
	if s.cfg.BillingMock {
		if pid := r.URL.Query().Get("project_id"); pid != "" {
			id, err := uuid.Parse(pid)
			if err == nil {
				return billingAuth{ProjectID: id, Login: "mock"}, true
			}
		}
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": "unauthorized",
		"hint":  "sign in via GitHub on /login or send Authorization: Bearer sk-gt…",
	})
	return billingAuth{}, false
}

func (s *Server) requirePlanEntitled(w http.ResponseWriter, r *http.Request, projectID uuid.UUID) bool {
	if !s.cfg.BillingEnforce {
		return true
	}
	b, err := s.store.GetProjectBilling(r.Context(), projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "billing_lookup_failed"})
		return false
	}
	ent := b.Entitlement()
	now := time.Now().UTC()
	if !ent.Entitled(now) {
		if err := s.store.EnsureFreePlan(r.Context(), projectID, now); err == nil {
			return true
		}
		s.billingDeny(w, http.StatusPaymentRequired, map[string]any{
			"error":           "plan_required",
			"hint":            "activate free cloud with llms free; if you need cloud, run llms signal",
			"plan":            string(ent.EffectivePlan(now)),
			"free_used":       ent.TrialUsed,
			"free_available":  true,
			"trial_used":      ent.TrialUsed,
			"trial_available": true,
		})
		return false
	}
	return true
}

func (s *Server) requireProParallel(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, preset, strategy string) bool {
	if !s.cfg.BillingEnforce {
		return true
	}
	isParallel := preset == "parallel" || preset == "race" || strategy == "parallel"
	if !isParallel {
		return true
	}
	b, err := s.store.GetProjectBilling(r.Context(), projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "billing_lookup_failed"})
		return false
	}
	ent := b.Entitlement()
	now := time.Now().UTC()
	if !ent.Entitled(now) {
		return s.requirePlanEntitled(w, r, projectID)
	}
	plan := ent.EffectivePlan(now)
	if plan != billing.PlanPro {
		s.billingDeny(w, http.StatusPaymentRequired, map[string]any{
			"error":    "plan_preset_required",
			"resource": "parallel",
			"hint":     "parallel preset is Pro-only; use failover, fill-first, balance, prefer-primary, quota-first, reset-soon, or steward",
			"plan":     string(plan),
			"required": string(billing.PlanPro),
		})
		return false
	}
	return true
}

func (s *Server) planRPMLimit(ctx context.Context, projectID uuid.UUID) int {
	if !s.cfg.BillingEnforce {
		return s.cfg.RateLimitPerMin
	}
	b, err := s.store.GetProjectBilling(ctx, projectID)
	if err != nil {
		return s.cfg.RateLimitPerMin
	}
	lim := b.Entitlement().Limits(time.Now().UTC())
	if lim.RPM > 0 {
		return lim.RPM
	}
	return s.cfg.RateLimitPerMin
}

func (s *Server) requirePlanCapacity(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, kind string) bool {
	if !s.cfg.BillingEnforce {
		return true
	}
	b, err := s.store.GetProjectBilling(r.Context(), projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "billing_lookup_failed"})
		return false
	}
	ent := b.Entitlement()
	now := time.Now().UTC()
	if !ent.Entitled(now) {
		s.billingDeny(w, http.StatusPaymentRequired, map[string]any{
			"error":           "plan_required",
			"hint":            "activate free cloud with llms free; if you need cloud, run llms signal",
			"plan":            string(ent.EffectivePlan(now)),
			"free_used":       ent.TrialUsed,
			"free_available":  true,
			"trial_used":      ent.TrialUsed,
			"trial_available": true,
		})
		return false
	}
	lim := ent.Limits(now)
	var n int
	switch kind {
	case "account":
		n, err = s.store.CountProjectAccounts(r.Context(), projectID)
		if err == nil && lim.Accounts > 0 && n >= lim.Accounts {
			s.billingDeny(w, http.StatusPaymentRequired, map[string]any{
				"error":    "plan_limit",
				"resource": "accounts",
				"limit":    lim.Accounts,
				"used":     n,
				"plan":     string(ent.EffectivePlan(now)),
				"hint":     planLimitHint("accounts"),
			})
			return false
		}
	case "route":
		n, err = s.store.CountProjectRoutes(r.Context(), projectID)
		if err == nil && lim.Routes > 0 && n >= lim.Routes {
			s.billingDeny(w, http.StatusPaymentRequired, map[string]any{
				"error":    "plan_limit",
				"resource": "routes",
				"limit":    lim.Routes,
				"used":     n,
				"plan":     string(ent.EffectivePlan(now)),
				"hint":     planLimitHint("routes"),
			})
			return false
		}
	case "key":
		n, err = s.store.CountProjectActiveKeys(r.Context(), projectID)
		if err == nil && lim.Keys > 0 && n >= lim.Keys {
			s.billingDeny(w, http.StatusPaymentRequired, map[string]any{
				"error":    "plan_limit",
				"resource": "keys",
				"limit":    lim.Keys,
				"used":     n,
				"plan":     string(ent.EffectivePlan(now)),
				"hint":     planLimitHint("keys"),
			})
			return false
		}
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "count_failed"})
		return false
	}
	return true
}

func (s *Server) handleBillingPage(w http.ResponseWriter, r *http.Request) {
	// Soft-open: public billing HTML is retired. CLI: llms free / llms plan / llms signal.
	http.NotFound(w, r)
}

func (s *Server) handleBillingStatus(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	payload, err := s.billingStatusPayload(r.Context(), ac.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "billing_lookup_failed"})
		return
	}
	payload["login"] = ac.Login
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleBillingLogout(w http.ResponseWriter, r *http.Request) {
	if s.user.Enabled() {
		s.user.Clear(w)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBillingStartFree(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if err := s.store.EnsureFreePlan(r.Context(), ac.ProjectID, time.Now().UTC()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	s.handleBillingStatus(w, r)
}

// handleBillingStartTrial is a legacy alias for handleBillingStartFree.
func (s *Server) handleBillingStartTrial(w http.ResponseWriter, r *http.Request) {
	s.handleBillingStartFree(w, r)
}

func planLimitHint(resource string) string {
	if billing.SoftOpen {
		return "Free cloud limit for " + resource + "; tell us you need cloud with llms signal"
	}
	return "upgrade plan for more " + resource
}

func softCapHint() string {
	if billing.SoftOpen {
		return "monthly Free cloud usage limit; tell us you need cloud with llms signal or wait for calendar month reset"
	}
	return "monthly token soft cap for your plan; upgrade or wait for calendar month reset"
}

func (s *Server) crewSignalPayload(ctx context.Context) map[string]any {
	var st store.CrewSignalStats
	if s.store != nil {
		var err error
		st, err = s.store.CountCrewSignals(ctx, time.Now().UTC())
		if err != nil {
			st = store.CrewSignalStats{}
		}
	}
	return map[string]any{
		"week_count":    st.WeekCount,
		"total_count":   st.TotalCount,
		"soft_open":     billing.SoftOpen,
		"paid_checkout": billing.PublicPaidCheckout,
		"goodtek_url":   "https://goodtek.xyz/",
		"vibecrew_url":  "https://vibecrew.jp/",
		"billing_url":   s.cfg.PublicBaseURL + "/install",
		"note_max":      store.MaxCrewSignalNoteRunes,
	}
}

func (s *Server) handleBillingCrewSignalGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.crewSignalPayload(r.Context()))
}

func (s *Server) handleBillingCrewSignalPost(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	s.writeCrewSignal(w, r, ac.ProjectID, ac.Login)
}

func (s *Server) handleControlCrewSignal(w http.ResponseWriter, r *http.Request) {
	ac, ok := authn.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	s.writeCrewSignal(w, r, ac.ProjectID, ac.KeyName)
}

func (s *Server) writeCrewSignal(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, login string) {
	var body struct {
		Note string `json:"note"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	err := s.store.InsertCrewSignal(r.Context(), projectID, login, body.Note, time.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrCrewSignalAlreadyToday):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "crew_signal_already_today",
				"hint":  "one Crew Signal per day — thanks; see status with llms plan",
			})
		case errors.Is(err, store.ErrCrewSignalUnavailable):
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"error": "crew_signal_unavailable",
				"hint":  "Crew Signal is on the hosted hub (llms.goodtek.xyz)",
			})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "crew_signal_failed"})
		}
		return
	}
	out := s.crewSignalPayload(r.Context())
	out["ok"] = true
	out["login"] = login
	writeJSON(w, http.StatusOK, out)
}
