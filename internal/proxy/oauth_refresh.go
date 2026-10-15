package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/goodtekxyz/openllms/internal/secrets"
	"github.com/goodtekxyz/openllms/internal/store"
	"github.com/goodtekxyz/openllms/internal/vendor"
	"github.com/goodtekxyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

const (
	oauthPutMaxAttempts = 3
	oauthPutBackoff     = 50 * time.Millisecond
	// DefaultOAuthRefreshLead refreshes when expires_at is within this window.
	DefaultOAuthRefreshLead = time.Hour
)

type refreshGates struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (g *refreshGates) lock(id string) *sync.Mutex {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*sync.Mutex)
	}
	l, ok := g.m[id]
	if !ok {
		l = &sync.Mutex{}
		g.m[id] = l
	}
	g.mu.Unlock()
	return l
}

func (e *Engine) log() *slog.Logger {
	if e != nil && e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Engine) oauthLock(accountID uuid.UUID) *sync.Mutex {
	return e.refreshGates.lock(accountID.String())
}

func (e *Engine) invalidateSecretCache(path, name string) {
	if inv, ok := e.Secrets.(secrets.CacheInvalidator); ok {
		inv.Invalidate(path, name)
	}
}

func (e *Engine) storePendingCred(accountID uuid.UUID, cred secrets.CredentialJSON) {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.pendingCreds == nil {
		e.pendingCreds = make(map[string]secrets.CredentialJSON)
	}
	e.pendingCreds[accountID.String()] = cred
}

func (e *Engine) takePendingCred(accountID uuid.UUID) (secrets.CredentialJSON, bool) {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.pendingCreds == nil {
		return secrets.CredentialJSON{}, false
	}
	c, ok := e.pendingCreds[accountID.String()]
	return c, ok
}

func (e *Engine) clearPendingCred(accountID uuid.UUID) {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.pendingCreds == nil {
		return
	}
	delete(e.pendingCreds, accountID.String())
}

func credsRotated(fresh, old secrets.CredentialJSON) bool {
	if fresh.AccessToken != "" && old.AccessToken != "" && fresh.AccessToken != old.AccessToken {
		return true
	}
	if fresh.RefreshToken != "" && old.RefreshToken != "" && fresh.RefreshToken != old.RefreshToken {
		return true
	}
	return false
}

func putCredentialWithRetry(ctx context.Context, sec secrets.Client, path, name, raw string) error {
	var err error
	for i := 0; i < oauthPutMaxAttempts; i++ {
		err = sec.Put(ctx, path, name, raw)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(oauthPutBackoff * time.Duration(i+1)):
		}
	}
	return err
}

// refreshOAuthCredential serializes refresh per account, re-reads durable secrets
// before rotating, and never drops newly issued tokens when Put fails.
func (e *Engine) refreshOAuthCredential(ctx context.Context, acc *store.Account, old secrets.CredentialJSON) (secrets.CredentialJSON, error) {
	mu := e.oauthLock(acc.ID)
	mu.Lock()
	defer mu.Unlock()

	log := e.log().With("account_id", acc.ID.String(), "vendor", acc.Vendor, "name", acc.Name)

	if pending, ok := e.takePendingCred(acc.ID); ok {
		raw, _ := json.Marshal(pending)
		if err := putCredentialWithRetry(ctx, e.Secrets, acc.InfisicalPath, vendor.SecretName, string(raw)); err != nil {
			log.Error("oauth pending put still failing", "err", err)
			e.storePendingCred(acc.ID, pending)
		} else {
			e.clearPendingCred(acc.ID)
			log.Info("oauth pending credential persisted")
		}
		if credsRotated(pending, old) || pending.AccessToken != "" {
			return pending, nil
		}
	}

	e.invalidateSecretCache(acc.InfisicalPath, vendor.SecretName)
	fresh, _, err := e.loadCredential(ctx, acc)
	if err == nil && credsRotated(fresh, old) {
		log.Info("oauth already rotated; skipping refresh")
		return fresh, nil
	}
	refreshTok := old.RefreshToken
	if err == nil && fresh.RefreshToken != "" {
		refreshTok = fresh.RefreshToken
	}
	if strings.TrimSpace(refreshTok) == "" {
		return secrets.CredentialJSON{}, errString("missing refresh_token")
	}

	log.Info("oauth refresh starting")
	toks, err := vendorauth.Refresh(ctx, acc.Vendor, refreshTok)
	if err != nil {
		log.Warn("oauth refresh failed", "err", err)
		return secrets.CredentialJSON{}, err
	}
	e.recordVendorEmail(ctx, acc, toks.Email)
	cred := secrets.CredentialJSON{
		AccessToken:      toks.AccessToken,
		RefreshToken:     toks.RefreshToken,
		ExpiresAt:        toks.ExpiresAt,
		IDToken:          toks.IDToken,
		ChatGPTAccountID: toks.ChatGPTAccountID,
	}
	if cred.RefreshToken == "" {
		cred.RefreshToken = refreshTok
	}
	if cred.IDToken == "" {
		// Refresh responses may omit id_token (OIDC Core §12.2); keep the stored one, the source of the account email.
		cred.IDToken = old.IDToken
		if fresh.IDToken != "" {
			cred.IDToken = fresh.IDToken
		}
	}
	if cred.ChatGPTAccountID == "" {
		cred.ChatGPTAccountID = old.ChatGPTAccountID
		if fresh.ChatGPTAccountID != "" {
			cred.ChatGPTAccountID = fresh.ChatGPTAccountID
		}
	}
	b, _ := json.Marshal(cred)
	if err := putCredentialWithRetry(ctx, e.Secrets, acc.InfisicalPath, vendor.SecretName, string(b)); err != nil {
		e.storePendingCred(acc.ID, cred)
		log.Error("oauth refresh put failed; keeping new tokens in memory", "err", err)
		return cred, nil
	}
	e.clearPendingCred(acc.ID)
	log.Info("oauth refresh persisted")
	return cred, nil
}

type errString string

func (e errString) Error() string { return string(e) }

// RefreshExpiringOAuth proactively rotates oauth credentials whose expires_at
// is within lead (default 1h). Safe under the same per-account refresh lock.
func (e *Engine) RefreshExpiringOAuth(ctx context.Context, accounts []store.Account, lead time.Duration) (updated, failed, skipped int) {
	if e == nil || e.Secrets == nil {
		return 0, 0, 0
	}
	if lead <= 0 {
		lead = DefaultOAuthRefreshLead
	}
	now := time.Now().UTC()
	for i := range accounts {
		acc := accounts[i]
		if !strings.EqualFold(acc.AuthType, "oauth") || acc.ReauthRequiredAt != nil {
			skipped++ // a reauth account waits for its re-link; refreshing cannot help
			continue
		}
		cred, _, err := e.loadCredential(ctx, &acc)
		if err != nil {
			failed++
			e.log().Warn("oauth proactive load", "account_id", acc.ID.String(), "err", err)
			continue
		}
		if !expiresWithin(cred.ExpiresAt, now, lead) {
			skipped++
			continue
		}
		if strings.TrimSpace(cred.RefreshToken) == "" {
			skipped++
			continue
		}
		if _, err := e.refreshOAuthCredential(ctx, &acc, cred); err != nil {
			failed++
			e.handleRefreshFailure(ctx, &acc, cred, err, &Result{})
			continue
		}
		updated++
	}
	return updated, failed, skipped
}

// recordVendorEmail stores the account email the vendor reported in a refresh response (Claude account object, or the
// id_token email claim for Codex). Only emails the gateway received from the vendor itself are stored: they decide
// where reauth notifications go.
func (e *Engine) recordVendorEmail(ctx context.Context, acc *store.Account, email string) {
	if e.Store == nil || email == "" || (acc.Email != nil && *acc.Email == email) {
		return
	}
	if err := e.Store.SetAccountEmail(ctx, acc.ID, email); err != nil {
		e.log().Warn("account email update", "account_id", acc.ID.String(), "err", err)
		return
	}
	acc.Email = &email
}

func expiresWithin(expiresAt string, now time.Time, lead time.Duration) bool {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, expiresAt)
		if err != nil {
			return false
		}
	}
	return !t.After(now.Add(lead))
}
