package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/goodtek-xyz/openllms/internal/router"
	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/goodtek-xyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

// ReauthEvent reports an account that needs a human re-link. It carries no credentials.
type ReauthEvent struct {
	AccountID uuid.UUID
	ProjectID uuid.UUID
	Vendor    string
	Name      string
	Email     string // vendor account email, only when the gateway got it from the vendor
	Since     time.Time
	Reason    string
}

// OAuthClientEvent reports a token refresh the provider rejected for the OAuth client or request (RFC 6749 §5.2
// invalid_client, unauthorized_client, …): a gateway configuration problem that affects every account of the vendor.
type OAuthClientEvent struct {
	Vendor    string
	Code      string
	Status    int
	AccountID uuid.UUID // the account whose refresh showed it
}

// ReauthNotifier tells people about these events. Calls happen on the request path and must not block. Delivery is
// at most once: the reauth flag itself is durable and shown in the console and admin view, so a lost notification
// leaves the state visible.
type ReauthNotifier interface {
	AccountReauthRequired(ev ReauthEvent)
	OAuthClientRejected(ev OAuthClientEvent)
}

// handleRefreshFailure acts on a failed refresh of acc's credential (old: the credential the refresh started from).
// Only an account-level rejection (invalid_grant and the OpenAI equivalents) flags the account; a client-level one is
// reported once as a configuration error; everything else is transient and keeps the cooldown.
func (e *Engine) handleRefreshFailure(ctx context.Context, acc *store.Account, old secrets.CredentialJSON, err error, res *Result) {
	kind, re := vendorauth.RefreshFailureOf(err)
	switch kind {
	case vendorauth.RefreshAccountRejected:
		e.flagIfUnchanged(ctx, acc, res, fmt.Sprintf("token refresh rejected: HTTP %d %s", re.Status, re.Code), old)
	case vendorauth.RefreshClientRejected:
		e.reportClientRejected(acc, re)
	}
}

// handleRetried acts on the upstream's answer to a just-refreshed token. 401 means the provider rejects this login
// itself, not an expired token: the account needs a re-link. 403 is not taken as proof (it is also used for
// permissions, plans and regional blocks) and keeps the cooldown.
func (e *Engine) handleRetried(ctx context.Context, acc *store.Account, old, refreshed secrets.CredentialJSON, res *Result) {
	if res.StatusCode == http.StatusUnauthorized {
		e.flagIfUnchanged(ctx, acc, res, "upstream 401 right after a successful token refresh", refreshed, old)
	}
}

// confirmUnauthorized decides on a 401 for an OAuth account without a refresh token (nothing to renew it with). One
// 401 can be a passing upstream fault, so it is only an observation; the account is flagged when it answers 401
// again at least one 401 cooldown later — two failures a full cooldown apart, like a probe's failure threshold. The
// observation is per gateway process and is cleared by any other answer.
func (e *Engine) confirmUnauthorized(ctx context.Context, acc *store.Account, res *Result) {
	now := time.Now()
	v, seen := e.unauthorized.LoadOrStore(acc.ID, now)
	if !seen || now.Sub(v.(time.Time)) < router.CooldownForStatus(http.StatusUnauthorized, 0) {
		return
	}
	e.unauthorized.Delete(acc.ID)
	res.ReauthRequired = e.markReauthRequired(ctx, acc, "upstream 401 twice, a cooldown apart, and no refresh token")
}

// UsageAnswer applies the request path's rule to the provider usage poll (quota.Refresher.OnUsageAnswer): an OAuth
// login without a refresh token that answers 401 twice, a 401 cooldown apart, needs a re-link; any other answer clears
// the observation. Such an account may get no traffic at all, so the poll is the only place its death shows.
func (e *Engine) UsageAnswer(ctx context.Context, acc store.Account, status int, hasRefreshToken bool) {
	if e == nil || acc.ReauthRequiredAt != nil {
		return
	}
	if status != http.StatusUnauthorized {
		e.unauthorized.Delete(acc.ID)
		return
	}
	if hasRefreshToken {
		return // the refresh flows decide (proactive refresh, request-path refresh on 401)
	}
	e.confirmUnauthorized(ctx, &acc, &Result{})
}

// flagIfUnchanged flags acc unless its stored credential is no longer any of known: a re-link (or another replica's
// rotation) replaced it while this request ran, so the failure belongs to the old login.
func (e *Engine) flagIfUnchanged(ctx context.Context, acc *store.Account, res *Result, reason string, known ...secrets.CredentialJSON) {
	if e.credentialReplaced(ctx, acc, known...) {
		res.CredentialReplaced = true
		return
	}
	res.ReauthRequired = e.markReauthRequired(ctx, acc, reason)
}

func (e *Engine) credentialReplaced(ctx context.Context, acc *store.Account, known ...secrets.CredentialJSON) bool {
	if e.Secrets == nil {
		return false
	}
	e.invalidateSecretCache(acc.InfisicalPath, vendor.SecretName)
	cur, _, err := e.loadCredential(ctx, acc)
	if err != nil {
		return false // cannot tell; the failure stands
	}
	for _, k := range known {
		if cur.AccessToken == k.AccessToken && cur.RefreshToken == k.RefreshToken {
			return false
		}
	}
	return true
}

// reportClientRejected logs a client-level refresh rejection and notifies once per vendor and code per process.
func (e *Engine) reportClientRejected(acc *store.Account, re *vendorauth.RefreshError) {
	e.log().Error("oauth refresh rejected for the client, not the account: check the OAuth client configuration",
		"vendor", acc.Vendor, "status", re.Status, "code", re.Code, "account_id", acc.ID.String())
	key := strings.ToLower(acc.Vendor) + "|" + re.Code
	if _, dup := e.clientRejected.LoadOrStore(key, struct{}{}); dup || e.Reauth == nil {
		return
	}
	e.Reauth.OAuthClientRejected(OAuthClientEvent{Vendor: acc.Vendor, Code: re.Code, Status: re.Status, AccountID: acc.ID})
}

// markReauthRequired takes the account out of routing and, for the first detection of the incident (across
// replicas), hands the event to the notifier. It reports whether the flag is set; when it could not be written the
// caller keeps the usual cooldown.
func (e *Engine) markReauthRequired(ctx context.Context, acc *store.Account, reason string) bool {
	if e.Store == nil {
		return false
	}
	// The flag must land even when the client already went away.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	first, err := e.Store.MarkAccountReauthRequired(mctx, acc.ID)
	log := e.log().With("account_id", acc.ID.String(), "vendor", acc.Vendor, "name", acc.Name)
	if err != nil {
		log.Error("mark account reauth required", "err", err)
		return false
	}
	log.Warn("account needs re-link; removed from routing", "reason", reason, "first", first)
	if !first || e.Reauth == nil {
		return true
	}
	ev := ReauthEvent{
		AccountID: acc.ID, ProjectID: acc.ProjectID, Vendor: acc.Vendor, Name: acc.Name,
		Since: time.Now().UTC(), Reason: reason,
	}
	if acc.Email != nil {
		ev.Email = *acc.Email
	}
	e.Reauth.AccountReauthRequired(ev)
	return true
}
