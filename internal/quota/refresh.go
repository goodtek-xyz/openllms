package quota

import (
	"context"
	"encoding/json"
	"errors"
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
	// OnUsageAnswer, when set, hears the HTTP status of each usage call (the proxy engine treats a repeated 401 of a
	// login without a refresh token like the same answer on the request path). hasRefreshToken: it can be renewed.
	OnUsageAnswer func(ctx context.Context, a store.Account, status int, hasRefreshToken bool)
}

// errNoUsageEndpoint: the credential is not an OAuth access token (e.g. an API key), and the usage endpoints only
// answer OAuth tokens. Not a failure; there is just no subscription quota to read.
var errNoUsageEndpoint = errors.New("no oauth access token: no usage endpoint")

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
	return r.refreshList(ctx, list, log)
}

// refreshList skips accounts waiting for a re-link (out of routing; their token is known dead) and credentials
// without a usage endpoint, so neither is polled or logged every cycle.
func (r *Refresher) refreshList(ctx context.Context, list []store.Account, log *slog.Logger) (updated, failed int) {
	for _, a := range list {
		if a.ReauthRequiredAt != nil {
			continue
		}
		if err := r.refreshOne(ctx, a); err != nil {
			if errors.Is(err, errNoUsageEndpoint) {
				log.Debug("provider quota skipped", "vendor", a.Vendor, "name", a.Name, "reason", err)
				continue
			}
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
	return r.refreshList(ctx, list, log)
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
	tok := strings.TrimSpace(cred.AccessToken)
	if tok == "" || vendor.IsAnthropicAPIKey(tok) {
		return errNoUsageEndpoint
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
	if r.OnUsageAnswer != nil {
		var ue *UsageError
		switch {
		case err == nil:
			r.OnUsageAnswer(ctx, a, 200, strings.TrimSpace(cred.RefreshToken) != "")
		case errors.As(err, &ue):
			r.OnUsageAnswer(ctx, a, ue.Status, strings.TrimSpace(cred.RefreshToken) != "")
		}
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
