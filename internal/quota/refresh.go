package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/google/uuid"
)

// Refresher pulls provider usage into account quota columns.
type Refresher struct {
	Store   *store.Store
	Secrets secrets.Client
	Log     *slog.Logger
}

// RefreshAllOAuth fetches Codex/Claude usage for every oauth account.
func (r *Refresher) RefreshAllOAuth(ctx context.Context) (updated, failed int) {
	if r == nil || r.Store == nil || r.Secrets == nil {
		return 0, 0
	}
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	list, err := r.Store.ListOAuthAccounts(ctx)
	if err != nil {
		log.Warn("list oauth accounts", "err", err)
		return 0, 0
	}
	for _, a := range list {
		if err := r.refreshOne(ctx, a); err != nil {
			failed++
			log.Warn("provider quota", "vendor", a.Vendor, "name", a.Name, "err", err)
			continue
		}
		updated++
	}
	return updated, failed
}

// RefreshProjectOAuth fetches Codex/Claude usage for a single project's oauth
// accounts, scoping provider quota refresh to the authenticated caller.
func (r *Refresher) RefreshProjectOAuth(ctx context.Context, projectID uuid.UUID) (updated, failed int) {
	if r == nil || r.Store == nil || r.Secrets == nil {
		return 0, 0
	}
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	list, err := r.Store.ListOAuthAccountsByProject(ctx, projectID)
	if err != nil {
		log.Warn("list project oauth accounts", "project_id", projectID, "err", err)
		return 0, 0
	}
	for _, a := range list {
		if err := r.refreshOne(ctx, a); err != nil {
			failed++
			log.Warn("provider quota", "vendor", a.Vendor, "name", a.Name, "err", err)
			continue
		}
		updated++
	}
	return updated, failed
}

func (r *Refresher) refreshOne(ctx context.Context, a store.Account) error {
	raw, err := r.Secrets.Get(ctx, a.InfisicalPath, vendor.SecretName)
	if err != nil {
		return err
	}
	var cred secrets.CredentialJSON
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return err
	}
	tok := cred.AccessToken
	if tok == "" {
		tok = cred.APIKey
	}
	if tok == "" {
		return fmt.Errorf("missing access_token")
	}
	var snap Snapshot
	switch strings.ToLower(a.Vendor) {
	case "codex", "openai":
		snap, err = FetchCodex(ctx, tok, cred.ChatGPTAccountID)
	case "claude", "anthropic":
		snap, err = FetchClaude(ctx, tok)
	default:
		return fmt.Errorf("no provider quota fetcher for vendor %s", a.Vendor)
	}
	if err != nil {
		return err
	}
	pct := snap.RemainingPct
	var w5pct, w7pct *float64
	var w5reset, w7reset *time.Time
	if snap.Window5h != nil {
		v := snap.Window5h.RemainingPct
		w5pct, w5reset = &v, snap.Window5h.ResetAt
	}
	if snap.Window7d != nil {
		v := snap.Window7d.RemainingPct
		w7pct, w7reset = &v, snap.Window7d.ResetAt
	}
	return r.Store.SetAccountQuotaWindowsID(ctx, a.ID, &pct, snap.ResetAt, w5pct, w5reset, w7pct, w7reset)
}
