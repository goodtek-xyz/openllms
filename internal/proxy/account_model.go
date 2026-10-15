package proxy

import (
	"encoding/json"
	"strings"

	"github.com/goodtek-xyz/openllms/internal/store"
)

// VendorFamily collapses vendor ids into model-family buckets.
func VendorFamily(vendor string) string {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "claude", "anthropic":
		return "claude"
	case "codex", "openai":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(vendor))
	}
}

func modelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"), strings.Contains(m, "codex"):
		return "openai"
	default:
		return ""
	}
}

// AccountServesModel reports whether acc's vendor family can run model.
// Unknown model ids are treated as compatible (pass-through).
func AccountServesModel(acc *store.Account, model string) bool {
	if acc == nil || strings.TrimSpace(model) == "" {
		return true
	}
	mf := modelFamily(model)
	if mf == "" {
		return true
	}
	vf := VendorFamily(acc.Vendor)
	if vf == "" {
		return true
	}
	return mf == vf
}

// ResolveOutboundModel picks the upstream model for one seat:
//  1. request model, when the seat can serve that family (external override)
//  2. else account.default_model (set at connect / later update)
//  3. else route.default_model, when the seat can serve that family
//  4. else the raw request model (last resort)
func ResolveOutboundModel(requestModel string, acc *store.Account, routeDefault string) string {
	next, _ := resolveOutboundModel(requestModel, acc, routeDefault)
	return next
}

// resolveOutboundModel also reports whether the pick is servable by acc
// (false only for the last-resort cross-vendor pass-through).
func resolveOutboundModel(requestModel string, acc *store.Account, routeDefault string) (string, bool) {
	req := strings.TrimSpace(requestModel)
	if req != "" && AccountServesModel(acc, req) {
		return req, true
	}
	if acc != nil {
		if d := strings.TrimSpace(acc.DefaultModel); d != "" {
			return d, true
		}
	}
	if d := strings.TrimSpace(routeDefault); d != "" && AccountServesModel(acc, d) {
		return d, true
	}
	return req, AccountServesModel(acc, req)
}

// FilterServableAccounts drops seats that have no model they can run for this
// request (e.g. a Claude seat without default_model on a route whose default
// is gpt-*). When no seat qualifies the original list is returned so the
// upstream error still surfaces.
func FilterServableAccounts(accounts []store.Account, body []byte, routeDefault string) []store.Account {
	var peek struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &peek)
	out := make([]store.Account, 0, len(accounts))
	for i := range accounts {
		if _, ok := resolveOutboundModel(peek.Model, &accounts[i], routeDefault); ok {
			out = append(out, accounts[i])
		}
	}
	if len(out) == 0 {
		return accounts
	}
	return out
}

// ApplyAccountModel rewrites JSON "model" for the selected account.
func ApplyAccountModel(body []byte, acc *store.Account, routeDefault string) []byte {
	if len(body) == 0 || acc == nil {
		return body
	}
	var peek struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &peek)
	next := ResolveOutboundModel(peek.Model, acc, routeDefault)
	if next == "" || next == strings.TrimSpace(peek.Model) {
		return body
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return body
	}
	b, err := json.Marshal(next)
	if err != nil {
		return body
	}
	raw["model"] = b
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}
