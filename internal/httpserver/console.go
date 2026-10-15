package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/goodtekxyz/openllms/internal/modelcatalog"
	"github.com/goodtekxyz/openllms/internal/proxy"
	"github.com/goodtekxyz/openllms/internal/quota"
	routerpkg "github.com/goodtekxyz/openllms/internal/router"
	"github.com/goodtekxyz/openllms/internal/vendor"
	"github.com/goodtekxyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

func (s *Server) handleConsolePage(w http.ResponseWriter, r *http.Request) {
	// Web console retired — CLI + /install. Keep /console/api for a transition period.
	http.Redirect(w, r, "/install", http.StatusMovedPermanently)
}

func (s *Server) consoleURL() string {
	// Console UI retired — advertise install (CLI path) in meta/me payloads.
	return s.cfg.PublicBaseURL + "/install"
}

func (s *Server) handleConsoleMe(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"login":      ac.Login,
		"project_id": ac.ProjectID.String(),
		"console":    s.consoleURL(),
		"billing":    s.cfg.PublicBaseURL + "/install",
	})
}

func (s *Server) handleConsoleLogout(w http.ResponseWriter, r *http.Request) {
	if s.user.Enabled() {
		s.user.Clear(w)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleConsoleOverview(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	payload, err := s.consoleOverviewPayload(r.Context(), ac.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) consoleOverviewPayload(ctx context.Context, projectID uuid.UUID) (map[string]any, error) {
	billing, err := s.billingStatusPayload(ctx, projectID)
	if err != nil {
		return nil, err
	}
	accounts, routes, err := s.store.StatusSnapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	keys, err := s.store.ListKeys(ctx, projectID)
	if err != nil {
		return nil, err
	}
	accOut := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		health, glyph := accountHealth(a)
		accOut = append(accOut, map[string]any{
			"id": a.ID.String(), "vendor": a.Vendor, "name": a.Name,
			"health": health, "glyph": glyph, "auth_type": a.AuthType,
			"email": a.Email, "reauth_required_at": a.ReauthRequiredAt,
			"quota_remaining_pct": a.QuotaRemainingPct, "quota_reset_at": a.QuotaResetAt,
			"quota_5h_remaining_pct": a.Quota5hRemainingPct, "quota_5h_reset_at": a.Quota5hResetAt,
			"quota_7d_remaining_pct": a.Quota7dRemainingPct, "quota_7d_reset_at": a.Quota7dResetAt,
		})
	}
	rtOut := make([]map[string]any, 0, len(routes))
	for _, rt := range routes {
		mem, _ := s.store.ListRoutePool(ctx, rt.ID)
		memIDs := make([]string, 0, len(mem))
		memRefs := make([]string, 0, len(mem))
		for _, m := range mem {
			memIDs = append(memIDs, m.ID.String())
			memRefs = append(memRefs, m.Vendor+":"+m.Name)
		}
		rtOut = append(rtOut, map[string]any{
			"id": rt.ID.String(), "slug": rt.Slug, "strategy": rt.Strategy,
			"preset":        routerpkg.PresetFromRoute(rt.Strategy, rt.Config),
			"default_model": rt.DefaultModel,
			"openai_base":   "/r/" + rt.Slug + "/v1",
			"account_ids":   memIDs,
			"account_refs":  memRefs,
		})
	}
	keyOut := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		row := map[string]any{
			"id": k.ID.String(), "name": k.Name, "key_prefix": k.Prefix,
			"revoked": k.RevokedAt != nil, "created_at": k.CreatedAt,
		}
		if k.RouteID != nil {
			row["route_id"] = k.RouteID.String()
		}
		keyOut = append(keyOut, row)
	}
	return map[string]any{
		"billing":  billing,
		"accounts": accOut,
		"routes":   rtOut,
		"keys":     keyOut,
		"public_base_url": s.cfg.PublicBaseURL,
	}, nil
}

func (s *Server) ensureConsoleEntitled(w http.ResponseWriter, r *http.Request, projectID uuid.UUID) bool {
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
	if ent.Entitled(now) {
		return true
	}
	if err := s.store.EnsureFreePlan(r.Context(), projectID, now); err == nil {
		return true
	}
	if !s.requirePlanEntitled(w, r, projectID) {
		return false
	}
	return true
}

func (s *Server) handleConsoleFree(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if err := s.store.EnsureFreePlan(r.Context(), ac.ProjectID, time.Now().UTC()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "billing": s.billingURL()})
		return
	}
	s.handleConsoleOverview(w, r)
}

// handleConsoleTrial is a legacy alias for handleConsoleFree.
func (s *Server) handleConsoleTrial(w http.ResponseWriter, r *http.Request) {
	s.handleConsoleFree(w, r)
}

func (s *Server) handleConsoleQuotaRefresh(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	var updated, failed int
	if s.secret != nil {
		ref := &quota.Refresher{Store: s.store, Secrets: s.secret}
		updated, failed = ref.RefreshProjectOAuth(r.Context(), ac.ProjectID)
	}
	var oauthRotated int
	if s.proxy != nil && s.store != nil {
		if list, err := s.store.ListOAuthAccountsByProject(r.Context(), ac.ProjectID); err == nil {
			oauthRotated, _, _ = s.proxy.RefreshExpiringOAuth(r.Context(), list, proxy.DefaultOAuthRefreshLead)
		}
	}
	n, err := s.store.RefreshQuotasFromUsageForProject(r.Context(), ac.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"oauth_updated": updated, "oauth_failed": failed, "oauth_rotated": oauthRotated, "usage_heuristic_updated": n,
	})
}

func (s *Server) handleConsoleCreateAccount(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	var body accountCreateInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return
	}
	if !s.requireAccountCapacity(w, r, ac.ProjectID, body) {
		return
	}
	acc, relinked, code, errBody := s.createAccountForProjectInner(r.Context(), ac.ProjectID, body)
	if acc == nil {
		writeJSON(w, code, errBody)
		return
	}
	writeLinkedAccount(w, acc, relinked)
}

func (s *Server) handleConsoleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid account id"})
		return
	}
	path, err := s.store.DeleteAccount(r.Context(), ac.ProjectID, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	secretDeleted := false
	if s.secret != nil && path != "" {
		if err := s.secret.Delete(r.Context(), path, vendor.SecretName); err == nil {
			secretDeleted = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "secret_deleted": secretDeleted})
}

func (s *Server) handleConsoleConnectClaudeStart(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireUserSession(w, r); !ok {
		return
	}
	p, err := vendorauth.ClaudeStart()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	id := s.vendorPending.putClaude(p.Verifier, p.State)
	writeJSON(w, http.StatusOK, map[string]any{
		"pending_id": id,
		"auth_url":   p.AuthURL,
		"hint":       "Authorize in browser, then paste the authorization code below.",
	})
}

func (s *Server) handleConsoleConnectClaudeComplete(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	var body struct {
		PendingID string `json:"pending_id"`
		Code      string `json:"code"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PendingID == "" || body.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pending_id and code required"})
		return
	}
	if !s.requireAccountCapacity(w, r, ac.ProjectID, accountCreateInput{Vendor: "claude", Name: body.Name, AuthType: "oauth"}) {
		return
	}
	entry, ok := s.vendorPending.getClaude(body.PendingID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pending_session_expired"})
		return
	}
	toks, err := vendorauth.ClaudeExchange(r.Context(), body.Code, entry.Verifier, entry.State)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	in := tokensToAccountInput("claude", body.Name, toks)
	acc, relinked, code, errBody := s.createAccountForProjectInner(r.Context(), ac.ProjectID, in)
	if acc == nil {
		writeJSON(w, code, errBody)
		return
	}
	writeLinkedAccount(w, acc, relinked)
}

func (s *Server) handleConsoleConnectCodexStart(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireUserSession(w, r); !ok {
		return
	}
	p, err := vendorauth.CodexStart(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	id := s.vendorPending.putCodex(p)
	writeJSON(w, http.StatusOK, map[string]any{
		"pending_id":       id,
		"user_code":        p.UserCode,
		"verification_uri": p.VerifyURL,
		"interval":         p.Interval.Seconds(),
	})
}

func (s *Server) handleConsoleConnectCodexPoll(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	var body struct {
		PendingID string `json:"pending_id"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PendingID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pending_id required"})
		return
	}
	entry, ok := s.vendorPending.getCodex(body.PendingID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pending_session_expired"})
		return
	}
	toks, pending, err := vendorauth.CodexPollOnce(r.Context(), entry.Pending)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	if pending {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending"})
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	in := tokensToAccountInput("codex", body.Name, toks)
	if !s.requireAccountCapacity(w, r, ac.ProjectID, in) {
		return
	}
	acc, relinked, code, errBody := s.createAccountForProjectInner(r.Context(), ac.ProjectID, in)
	if acc == nil {
		writeJSON(w, code, errBody)
		return
	}
	writeLinkedAccount(w, acc, relinked)
}

func (s *Server) handleConsoleCreateRoute(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	if !s.requirePlanCapacity(w, r, ac.ProjectID, "route") {
		return
	}
	var body struct {
		Slug         string          `json:"slug"`
		Strategy     string          `json:"strategy"`
		Preset       string          `json:"preset"`
		DefaultModel string          `json:"default_model"`
		Config       json.RawMessage `json:"config"`
		AccountIDs   []string        `json:"account_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "slug required"})
		return
	}
	if body.Preset != "" {
		st, cfg := routerpkg.ApplyPreset(body.Preset)
		body.Strategy = string(st)
		if body.Config == nil {
			b, _ := json.Marshal(cfg)
			body.Config = b
		}
	}
	if !s.requireProParallel(w, r, ac.ProjectID, body.Preset, body.Strategy) {
		return
	}
	rt, err := s.store.CreateRoute(r.Context(), ac.ProjectID, body.Slug, body.Strategy, body.DefaultModel, body.Config)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for i, aidStr := range body.AccountIDs {
		aid, err := uuid.Parse(aidStr)
		if err != nil {
			continue
		}
		if _, err := s.store.GetAccount(r.Context(), ac.ProjectID, aid); err != nil {
			continue
		}
		_ = s.store.AttachAccount(r.Context(), rt.ID, aid, i, routerpkg.AttachWeight(rt.Config, i))
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": rt.ID.String(), "slug": rt.Slug, "openai_base": "/r/" + rt.Slug + "/v1",
	})
}

func (s *Server) handleConsoleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	slug := chi.URLParam(r, "slug")
	rt, err := s.store.GetRouteBySlug(r.Context(), ac.ProjectID, slug)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "route not found"})
		return
	}
	var body struct {
		Strategy     string          `json:"strategy"`
		Preset       string          `json:"preset"`
		DefaultModel *string         `json:"default_model"`
		Config       json.RawMessage `json:"config"`
		AccountIDs   []string        `json:"account_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return
	}
	strategy := rt.Strategy
	config := rt.Config
	if body.Preset != "" {
		st, cfg := routerpkg.ApplyPreset(body.Preset)
		strategy = string(st)
		b, _ := json.Marshal(cfg)
		config = b
	} else if body.Strategy != "" {
		strategy = body.Strategy
		if body.Config != nil {
			config = body.Config
		}
	} else if body.Config != nil {
		config = body.Config
	}
	if !s.requireProParallel(w, r, ac.ProjectID, body.Preset, strategy) {
		return
	}
	defaultModel := rt.DefaultModel
	if body.DefaultModel != nil {
		defaultModel = *body.DefaultModel
	}
	updated, err := s.store.UpdateRoute(r.Context(), ac.ProjectID, slug, strategy, defaultModel, config)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if body.AccountIDs != nil {
		ids := make([]uuid.UUID, 0, len(body.AccountIDs))
		seen := map[uuid.UUID]bool{}
		for _, aidStr := range body.AccountIDs {
			aid, err := uuid.Parse(aidStr)
			if err != nil || seen[aid] {
				continue
			}
			if _, err := s.store.GetAccount(r.Context(), ac.ProjectID, aid); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "account not found", "account_id": aidStr})
				return
			}
			seen[aid] = true
			ids = append(ids, aid)
		}
		weights := make([]int, len(ids))
		for i := range ids {
			weights[i] = routerpkg.AttachWeight(updated.Config, i)
		}
		if err := s.store.ReplaceRouteAccounts(r.Context(), updated.ID, ids, weights); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	} else if body.Preset != "" {
		_ = s.reweightRouteAccounts(r.Context(), updated.ID, updated.Config)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "slug": updated.Slug, "strategy": updated.Strategy,
		"preset": routerpkg.PresetFromRoute(updated.Strategy, updated.Config),
	})
}

func (s *Server) handleConsoleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	slug := chi.URLParam(r, "slug")
	if err := s.store.DeleteRoute(r.Context(), ac.ProjectID, slug); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": slug})
}

func (s *Server) handleConsoleAttachAccount(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	slug := chi.URLParam(r, "slug")
	rt, err := s.store.GetRouteBySlug(r.Context(), ac.ProjectID, slug)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "route not found"})
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		Position  int    `json:"position"`
		Weight    int    `json:"weight"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AccountID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "account_id required"})
		return
	}
	aid, err := uuid.Parse(body.AccountID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid account_id"})
		return
	}
	if _, err := s.store.GetAccount(r.Context(), ac.ProjectID, aid); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "account not found"})
		return
	}
	weight := body.Weight
	if weight <= 0 || routerpkg.PresetFromRoute(rt.Strategy, rt.Config) == "prefer-primary" {
		weight = routerpkg.AttachWeight(rt.Config, body.Position)
	}
	if err := s.store.AttachAccount(r.Context(), rt.ID, aid, body.Position, weight); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleConsoleDetachAccount(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	slug := chi.URLParam(r, "slug")
	aid, err := uuid.Parse(chi.URLParam(r, "accountId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid account id"})
		return
	}
	rt, err := s.store.GetRouteBySlug(r.Context(), ac.ProjectID, slug)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "route not found"})
		return
	}
	if err := s.store.DetachRouteAccount(r.Context(), rt.ID, aid); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleConsoleRouteModels(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")
	rt, err := s.store.GetRouteBySlug(r.Context(), ac.ProjectID, slug)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "route not found"})
		return
	}
	accounts, err := s.store.ListRouteAccounts(r.Context(), rt.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if s.models == nil {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{}})
		return
	}
	cat := s.models.ForRoute(r.Context(), rt, accounts)
	writeJSON(w, http.StatusOK, modelcatalog.OpenAIList(cat))
}

func (s *Server) handleConsoleListKeys(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	list, err := s.store.ListKeys(r.Context(), ac.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, k := range list {
		row := map[string]any{
			"id": k.ID.String(), "name": k.Name, "key_prefix": k.Prefix,
			"revoked": k.RevokedAt != nil,
		}
		if k.RouteID != nil {
			row["route_id"] = k.RouteID.String()
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *Server) handleConsoleCreateKey(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	if !s.requirePlanCapacity(w, r, ac.ProjectID, "key") {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Slug  string `json:"route_slug"`
		Route string `json:"route_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		body.Name = "default"
	}
	var routeID *uuid.UUID
	if body.Route != "" {
		id, err := uuid.Parse(body.Route)
		if err == nil {
			rt, err := s.store.GetRouteByID(r.Context(), id)
			if err == nil && rt.ProjectID == ac.ProjectID {
				routeID = &rt.ID
			}
		}
	} else if body.Slug != "" {
		rt, err := s.store.GetRouteBySlug(r.Context(), ac.ProjectID, body.Slug)
		if err == nil {
			routeID = &rt.ID
		}
	}
	plaintext, err := s.store.CreateKey(r.Context(), ac.ProjectID, body.Name, routeID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"api_key": plaintext,
		"warning": "store api_key now; it will not be shown again",
	})
}

func (s *Server) handleConsoleRevokeKey(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if !s.ensureConsoleEntitled(w, r, ac.ProjectID) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid key id"})
		return
	}
	if err := s.store.RevokeKey(r.Context(), ac.ProjectID, id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) reweightRouteAccounts(ctx context.Context, routeID uuid.UUID, config []byte) error {
	accs, err := s.store.ListRoutePool(ctx, routeID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(accs))
	weights := make([]int, len(accs))
	for i, a := range accs {
		ids[i] = a.ID
		weights[i] = routerpkg.AttachWeight(config, i)
	}
	return s.store.ReplaceRouteAccounts(ctx, routeID, ids, weights)
}
