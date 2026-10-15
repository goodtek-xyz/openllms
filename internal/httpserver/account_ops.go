package httpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/goodtekxyz/openllms/internal/secrets"
	"github.com/goodtekxyz/openllms/internal/store"
	"github.com/goodtekxyz/openllms/internal/vendor"
	"github.com/goodtekxyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

type accountCreateInput struct {
	Vendor       string `json:"vendor"`
	Name         string `json:"name"`
	APIKey       string `json:"api_key"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
	IDToken      string `json:"id_token"`
	ChatGPTAcct  string `json:"chatgpt_account_id"`
	AuthType     string `json:"auth_type"`
	BaseURL      string `json:"base_url"`
	DefaultModel string `json:"default_model"`
	// vendorEmail is the account email the gateway itself received from the vendor's token endpoint (console sign-in
	// flows). Never taken from the request body: a client could name any address, and notifications go there.
	vendorEmail string
}

// normalize fills the defaults the link path relies on (account name, auth type).
func (in *accountCreateInput) normalize() {
	if in.Name == "" {
		in.Name = "default"
	}
	if in.AuthType == "" {
		if in.AccessToken != "" {
			in.AuthType = "oauth"
		} else {
			in.AuthType = "api_key"
		}
	}
}

// accountEmail is the vendor account email to record: only one the gateway received from the vendor. Tokens posted
// by a client (CLI) are unverified — an id_token there is just client input — so such accounts get their email from
// the vendor at the next refresh instead. API-key accounts have none.
func (in accountCreateInput) accountEmail() string {
	if in.AuthType != "oauth" {
		return ""
	}
	return vendorauth.CleanEmail(in.vendorEmail)
}

// requireAccountCapacity gates a new account on the plan; linking an existing account again (same vendor and
// name) replaces its login and does not use capacity.
func (s *Server) requireAccountCapacity(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, in accountCreateInput) bool {
	in.normalize()
	existing, err := s.store.GetAccountByName(r.Context(), projectID, in.Vendor, in.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return false
	}
	if existing != nil {
		return true
	}
	return s.requirePlanCapacity(w, r, projectID, "account")
}

// writeLinkedAccount answers a link: 201 for a new account, 200 for a re-link of an existing one.
func writeLinkedAccount(w http.ResponseWriter, acc *store.Account, relinked bool) {
	code := http.StatusCreated
	if relinked {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{
		"id": acc.ID.String(), "vendor": acc.Vendor, "name": acc.Name, "relinked": relinked,
		"infisical_path": acc.InfisicalPath, "base_url": acc.BaseURL, "default_model": acc.DefaultModel,
	})
}

// createAccountForProjectInner links an upstream account: it stores the credential, then creates the account or,
// when (vendor, name) already exists, re-links it — new login, email, and back into routing. Callers gate plan
// capacity with requireAccountCapacity.
func (s *Server) createAccountForProjectInner(ctx context.Context, projectID uuid.UUID, in accountCreateInput) (*store.Account, bool, int, map[string]any) {
	if s.secret == nil {
		return nil, false, http.StatusServiceUnavailable, map[string]any{
			"error": "infisical_not_configured",
			"hint":  "configure a secret store: LLMS_SECRETS_DIR (file) or INFISICAL_* (managed)",
		}
	}
	if in.Vendor == "" {
		return nil, false, http.StatusBadRequest, map[string]any{"error": "vendor required"}
	}
	in.normalize()
	if in.AuthType == "api_key" && in.APIKey == "" {
		return nil, false, http.StatusBadRequest, map[string]any{"error": "api_key required for auth_type=api_key"}
	}
	if in.AuthType == "oauth" && in.AccessToken == "" {
		return nil, false, http.StatusBadRequest, map[string]any{"error": "access_token required for auth_type=oauth"}
	}
	if in.BaseURL == "" {
		in.BaseURL = vendor.DefaultBaseURLFor(in.Vendor, in.AuthType)
	}
	path := vendor.InfisicalPath(projectID.String(), in.Vendor, in.Name)
	cred := secrets.CredentialJSON{
		APIKey: in.APIKey, AccessToken: in.AccessToken, RefreshToken: in.RefreshToken,
		ExpiresAt: in.ExpiresAt, IDToken: in.IDToken, ChatGPTAccountID: in.ChatGPTAcct,
	}
	raw, _ := json.Marshal(cred)
	if err := s.secret.Put(ctx, path, vendor.SecretName, string(raw)); err != nil {
		return nil, false, http.StatusBadGateway, map[string]any{"error": "infisical_put_failed", "detail": err.Error()}
	}
	email := in.accountEmail()
	existing, err := s.store.GetAccountByName(ctx, projectID, in.Vendor, in.Name)
	if err != nil {
		return nil, false, http.StatusInternalServerError, map[string]any{"error": err.Error()}
	}
	if existing != nil {
		acc, err := s.store.RelinkAccount(ctx, projectID, existing.ID, in.AuthType, path, in.BaseURL, in.DefaultModel, email)
		if err != nil {
			return nil, false, http.StatusInternalServerError, map[string]any{"error": err.Error()}
		}
		return acc, true, http.StatusOK, nil
	}
	acc, err := s.store.CreateAccount(ctx, projectID, in.Vendor, in.Name, in.AuthType, path, in.BaseURL, in.DefaultModel)
	if err != nil {
		return nil, false, http.StatusInternalServerError, map[string]any{"error": err.Error()}
	}
	if email != "" {
		if err := s.store.SetAccountEmail(ctx, acc.ID, email); err != nil {
			return nil, false, http.StatusInternalServerError, map[string]any{"error": err.Error()}
		}
		acc.Email = &email
	}
	return acc, false, http.StatusCreated, nil
}

func tokensToAccountInput(vendor string, name string, t vendorauth.Tokens) accountCreateInput {
	return accountCreateInput{
		Vendor:       vendor,
		Name:         name,
		AuthType:     "oauth",
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    t.ExpiresAt,
		IDToken:      t.IDToken,
		ChatGPTAcct:  t.ChatGPTAccountID,
		vendorEmail:  t.Email,
	}
}
