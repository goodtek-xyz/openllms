package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/ttlcache"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/goodtek-xyz/openllms/internal/vendorauth"
)

const DefaultCacheTTL = 5 * time.Minute

// Model is a chat-callable upstream model id.
type Model struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

type AccountModels struct {
	ID           string  `json:"id"` // vendor:name (account ref, not a chat model)
	Provider     string  `json:"provider"`
	Label        string  `json:"label"`
	Models       []Model `json:"models"`
	DefaultModel string  `json:"default_model"`
	Status       string  `json:"status"` // ok | error
	Error        string  `json:"error,omitempty"`
}

type UnionModel struct {
	ID         string   `json:"id"`
	Providers  []string `json:"providers"`
	AccountIDs []string `json:"account_ids"`
}

type RouteModels struct {
	Route          string          `json:"route"`
	Strategy       string          `json:"strategy"`
	Accounts       []AccountModels `json:"accounts"`
	Models         []UnionModel    `json:"models"`
	SuggestedModel string          `json:"suggested_model"`
}

type cacheEntry struct {
	Models []Model
	Status string
	Error  string
}

// Discoverer loads provider-backed chat models for route accounts.
type Discoverer struct {
	Secrets secrets.Client
	HTTP    *http.Client
	cache   *ttlcache.Cache[string, cacheEntry]
}

func New(sec secrets.Client, httpClient *http.Client, cacheTTL time.Duration) *Discoverer {
	if cacheTTL <= 0 {
		cacheTTL = DefaultCacheTTL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 12 * time.Second}
	}
	return &Discoverer{
		Secrets: sec,
		HTTP:    httpClient,
		cache:   ttlcache.New[string, cacheEntry](cacheTTL),
	}
}

func accountRef(a store.Account) string {
	return a.Vendor + ":" + a.Name
}

// ForRoute discovers models for every account on the route and builds the union.
func (d *Discoverer) ForRoute(ctx context.Context, rt *store.Route, accounts []store.Account) RouteModels {
	out := RouteModels{
		Route:    rt.Slug,
		Strategy: rt.Strategy,
		Accounts: make([]AccountModels, 0, len(accounts)),
	}
	union := map[string]*UnionModel{}
	order := make([]string, 0)

	for _, a := range accounts {
		am := d.forAccount(ctx, a)
		out.Accounts = append(out.Accounts, am)
		if am.Status != "ok" {
			continue
		}
		for _, m := range am.Models {
			if m.ID == "" || looksLikeAccountRef(m.ID) {
				continue
			}
			u, ok := union[m.ID]
			if !ok {
				u = &UnionModel{ID: m.ID, Providers: nil, AccountIDs: nil}
				union[m.ID] = u
				order = append(order, m.ID)
			}
			u.Providers = appendUnique(u.Providers, strings.ToLower(a.Vendor))
			u.AccountIDs = appendUnique(u.AccountIDs, am.ID)
		}
	}
	for _, id := range order {
		out.Models = append(out.Models, *union[id])
	}
	out.SuggestedModel = suggest(rt.DefaultModel, out)
	return out
}

// OpenAIList returns OpenAI-compatible {object,data} ids only (unique real models).
func OpenAIList(cat RouteModels) map[string]any {
	data := make([]map[string]any, 0, len(cat.Models))
	for _, m := range cat.Models {
		owned := ""
		if len(m.Providers) > 0 {
			owned = m.Providers[0]
		}
		data = append(data, map[string]any{"id": m.ID, "object": "model", "owned_by": owned})
	}
	return map[string]any{"object": "list", "data": data}
}

func (d *Discoverer) forAccount(ctx context.Context, a store.Account) AccountModels {
	ref := accountRef(a)
	am := AccountModels{
		ID:       ref,
		Provider: strings.ToLower(a.Vendor),
		Label:    a.Name,
		Models:   nil,
		Status:   "ok",
	}
	if d == nil || d.Secrets == nil {
		am.Status = "error"
		am.Error = "secrets_unavailable"
		return am
	}
	cacheKey := a.ID.String()
	if d.cache != nil {
		if hit, ok := d.cache.Get(cacheKey); ok {
			am.Models = hit.Models
			am.Status = hit.Status
			am.Error = hit.Error
			am.DefaultModel = defaultOf(am.Models)
			return am
		}
	}

	models, err := d.discoverLive(ctx, &a)
	if err != nil || len(models) == 0 {
		models = fallbackModels(a.Vendor, a.AuthType)
		if len(models) == 0 {
			am.Status = "error"
			if err != nil {
				am.Error = err.Error()
			} else {
				am.Error = "no_models"
			}
			if d.cache != nil {
				d.cache.Set(cacheKey, cacheEntry{Status: am.Status, Error: am.Error})
			}
			return am
		}
		// Fallback used after live miss — still ok for consumers; keep cause for operators.
		if err != nil {
			am.Error = "upstream_unavailable_using_known_list:" + err.Error()
		} else {
			am.Error = "upstream_unavailable_using_known_list:empty"
		}
	}
	am.Models = models
	am.DefaultModel = defaultOf(models)
	if d.cache != nil {
		d.cache.Set(cacheKey, cacheEntry{Models: models, Status: "ok", Error: am.Error})
	}
	return am
}

func (d *Discoverer) discoverLive(ctx context.Context, a *store.Account) ([]Model, error) {
	raw, err := d.Secrets.Get(ctx, a.InfisicalPath, vendor.SecretName)
	if err != nil {
		return nil, err
	}
	var cred secrets.CredentialJSON
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return nil, fmt.Errorf("bad_secret")
	}
	token := cred.BearerToken()
	if token == "" {
		return nil, fmt.Errorf("missing_token")
	}

	models, status, err := d.fetchModelsOnce(ctx, a, cred, token)
	if err == nil {
		return models, nil
	}
	// OAuth: one refresh+retry on auth failure (models list does not go through the proxy 401 path).
	if isOAuthAccount(a) && isAuthFailure(status, err) && cred.RefreshToken != "" {
		fresh, rerr := d.refreshOAuthCred(ctx, a, cred)
		if rerr != nil {
			return nil, fmt.Errorf("%w; refresh_failed:%v", err, rerr)
		}
		models, _, err2 := d.fetchModelsOnce(ctx, a, fresh, fresh.BearerToken())
		if err2 != nil {
			return nil, err2
		}
		return models, nil
	}
	return nil, err
}

func isOAuthAccount(a *store.Account) bool {
	return a != nil && strings.EqualFold(a.AuthType, "oauth")
}

func isAuthFailure(status int, err error) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "upstream_401") || strings.Contains(s, "upstream_403")
}

func (d *Discoverer) refreshOAuthCred(ctx context.Context, a *store.Account, old secrets.CredentialJSON) (secrets.CredentialJSON, error) {
	toks, err := vendorauth.Refresh(ctx, a.Vendor, old.RefreshToken)
	if err != nil {
		return secrets.CredentialJSON{}, err
	}
	fresh := secrets.CredentialJSON{
		AccessToken:      toks.AccessToken,
		RefreshToken:     toks.RefreshToken,
		ExpiresAt:        toks.ExpiresAt,
		IDToken:          toks.IDToken,
		ChatGPTAccountID: toks.ChatGPTAccountID,
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = old.RefreshToken
	}
	if fresh.ChatGPTAccountID == "" {
		fresh.ChatGPTAccountID = old.ChatGPTAccountID
	}
	b, err := json.Marshal(fresh)
	if err != nil {
		return fresh, err
	}
	if err := d.Secrets.Put(ctx, a.InfisicalPath, vendor.SecretName, string(b)); err != nil {
		return fresh, err
	}
	if inv, ok := d.Secrets.(secrets.CacheInvalidator); ok {
		inv.Invalidate(a.InfisicalPath, vendor.SecretName)
	}
	return fresh, nil
}

func (d *Discoverer) fetchModelsOnce(ctx context.Context, a *store.Account, cred secrets.CredentialJSON, token string) ([]Model, int, error) {
	base := strings.TrimRight(a.BaseURL, "/")
	if isCodexOAuth(a) {
		def := vendor.DefaultBaseURLFor(a.Vendor, a.AuthType)
		// Prefer ChatGPT Codex backend; keep explicit non-OpenAI bases (tests / private mirrors).
		if base == "" || strings.Contains(base, "api.openai.com") {
			base = def
		}
	} else if base == "" {
		base = vendor.DefaultBaseURLFor(a.Vendor, a.AuthType)
	}
	if base == "" {
		return nil, 0, fmt.Errorf("no_base_url")
	}

	var url string
	v := strings.ToLower(a.Vendor)
	switch {
	case isCodexOAuth(a):
		url = strings.TrimRight(base, "/") + "/models?client_version=" + codexClientVersion
	case v == "claude" || v == "anthropic":
		base = strings.TrimSuffix(base, "/v1")
		url = strings.TrimRight(base, "/") + "/v1/models"
	default:
		url = base + "/models"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	applyListAuth(req, a, cred, token)
	res, err := d.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, res.StatusCode, fmt.Errorf("upstream_%d", res.StatusCode)
	}
	var models []Model
	if isCodexOAuth(a) {
		models, err = parseCodexModelsJSON(b)
	} else {
		models, err = parseModelsJSON(b, a.Vendor)
	}
	if err != nil {
		return nil, res.StatusCode, err
	}
	return models, res.StatusCode, nil
}

// Codex CLI catalog query; keep aligned with current Codex client releases.
const codexClientVersion = "0.144.1"

func applyListAuth(req *http.Request, a *store.Account, cred secrets.CredentialJSON, token string) {
	oauth := strings.EqualFold(a.AuthType, "oauth")
	v := strings.ToLower(a.Vendor)
	switch {
	case oauth && (v == "claude" || v == "anthropic"):
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", "claude-code/1.0")
		req.Header.Set("Accept", "application/json")
	case oauth && (v == "codex" || v == "openai"):
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", "codex_cli_rs/"+codexClientVersion)
		req.Header.Set("originator", "codex_cli_rs")
		req.Header.Set("Accept", "application/json")
		if cred.ChatGPTAccountID != "" {
			req.Header.Set("ChatGPT-Account-Id", cred.ChatGPTAccountID)
		}
	case v == "claude" || v == "anthropic":
		req.Header.Set("x-api-key", token)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Accept", "application/json")
	default:
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
	}
}

func parseModelsJSON(b []byte, vendorName string) ([]Model, error) {
	var openai struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &openai); err == nil && len(openai.Data) > 0 {
		out := make([]Model, 0, len(openai.Data))
		for _, row := range openai.Data {
			if row.ID == "" || looksLikeAccountRef(row.ID) {
				continue
			}
			owned := row.OwnedBy
			if owned == "" {
				owned = strings.ToLower(vendorName)
			}
			out = append(out, Model{ID: row.ID, OwnedBy: owned})
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	// Anthropic: { "data": [ { "id": "claude-..." } ] } same shape; also { "models": [...] }
	var alt struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &alt); err == nil && len(alt.Models) > 0 {
		out := make([]Model, 0, len(alt.Models))
		for _, row := range alt.Models {
			if row.ID == "" || looksLikeAccountRef(row.ID) {
				continue
			}
			out = append(out, Model{ID: row.ID, OwnedBy: "anthropic"})
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("unparseable_models")
}

// parseCodexModelsJSON reads ChatGPT Codex backend GET /models payloads.
// Shape: { "models": [ { "slug": "gpt-5.5", "supported_in_api": true, "visibility": "list", ... } ] }
func parseCodexModelsJSON(b []byte) ([]Model, error) {
	var payload struct {
		Models []struct {
			Slug           string `json:"slug"`
			ID             string `json:"id"`
			SupportedInAPI *bool  `json:"supported_in_api"`
			Visibility     string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, fmt.Errorf("unparseable_codex_models")
	}
	if len(payload.Models) == 0 {
		return nil, fmt.Errorf("empty_codex_models")
	}

	type cand struct {
		id             string
		supportedInAPI bool
		visibility     string
	}
	cands := make([]cand, 0, len(payload.Models))
	anySupportedFlag := false
	for _, row := range payload.Models {
		id := strings.TrimSpace(row.Slug)
		if id == "" {
			id = strings.TrimSpace(row.ID)
		}
		if id == "" || looksLikeAccountRef(id) {
			continue
		}
		vis := strings.ToLower(strings.TrimSpace(row.Visibility))
		if vis == "hidden" {
			continue
		}
		supported := true
		if row.SupportedInAPI != nil {
			anySupportedFlag = true
			supported = *row.SupportedInAPI
		}
		cands = append(cands, cand{id: id, supportedInAPI: supported, visibility: vis})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("empty_codex_models")
	}

	out := make([]Model, 0, len(cands))
	seen := map[string]struct{}{}
	for _, c := range cands {
		if anySupportedFlag && !c.supportedInAPI {
			continue
		}
		if _, ok := seen[c.id]; ok {
			continue
		}
		seen[c.id] = struct{}{}
		out = append(out, Model{ID: c.id, OwnedBy: "openai"})
	}
	if len(out) == 0 {
		// All rows marked supported_in_api=false — still surface slugs rather than fail hard.
		for _, c := range cands {
			if _, ok := seen[c.id]; ok {
				continue
			}
			seen[c.id] = struct{}{}
			out = append(out, Model{ID: c.id, OwnedBy: "openai"})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty_codex_models")
	}
	return out, nil
}

func isCodexOAuth(a *store.Account) bool {
	if a == nil || !strings.EqualFold(a.AuthType, "oauth") {
		return false
	}
	v := strings.ToLower(a.Vendor)
	return v == "codex" || v == "openai"
}

// Known lists when live discovery fails (network/auth). Prefer upstream when available.
func fallbackModels(vendorName, authType string) []Model {
	v := strings.ToLower(vendorName)
	oauth := strings.EqualFold(authType, "oauth")
	switch {
	case oauth && (v == "codex" || v == "openai"):
		return []Model{
			{ID: "gpt-5.5", OwnedBy: "openai"},
			{ID: "gpt-5.4", OwnedBy: "openai"},
			{ID: "gpt-5.4-mini", OwnedBy: "openai"},
		}
	case oauth && (v == "claude" || v == "anthropic"):
		return []Model{
			{ID: "claude-sonnet-4-5", OwnedBy: "anthropic"},
			{ID: "claude-opus-4-5", OwnedBy: "anthropic"},
			{ID: "claude-haiku-4-5", OwnedBy: "anthropic"},
		}
	default:
		return nil
	}
}

// ParseCodexModelsJSONForTest exposes Codex backend model parsing for unit tests.
func ParseCodexModelsJSONForTest(b []byte) ([]Model, error) {
	return parseCodexModelsJSON(b)
}

func looksLikeAccountRef(id string) bool {
	// Gateway historically listed "codex:name" as model ids — never treat those as chat models.
	if !strings.Contains(id, ":") {
		return false
	}
	left, _, ok := strings.Cut(id, ":")
	if !ok {
		return false
	}
	switch strings.ToLower(left) {
	case "codex", "openai", "claude", "anthropic", "deepseek", "kimi", "moonshot", "glm":
		return true
	default:
		return false
	}
}

func defaultOf(models []Model) string {
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}

func suggest(routeDefault string, cat RouteModels) string {
	if routeDefault != "" && !looksLikeAccountRef(routeDefault) {
		for _, m := range cat.Models {
			if m.ID == routeDefault {
				return routeDefault
			}
		}
	}
	if len(cat.Models) > 0 {
		return cat.Models[0].ID
	}
	for _, a := range cat.Accounts {
		if a.DefaultModel != "" {
			return a.DefaultModel
		}
	}
	return ""
}

func appendUnique(ss []string, v string) []string {
	for _, s := range ss {
		if s == v {
			return ss
		}
	}
	return append(ss, v)
}
