package vendorauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RefreshError is a refresh_token grant (RFC 6749 §6) the token endpoint answered with an error.
type RefreshError struct {
	Vendor string
	Status int    // HTTP status of the token response
	Code   string // OAuth error code from the body, e.g. invalid_grant; "" when the body has none
	Detail string // truncated response body (an error response carries no tokens)
}

func (e *RefreshError) Error() string {
	return fmt.Sprintf("%s refresh: %d %s", e.Vendor, e.Status, e.Detail)
}

// RefreshFailure says who has to act on a failed refresh.
type RefreshFailure int

const (
	// RefreshTransient may pass by itself: 5xx, 429, a 4xx without a known OAuth error code (a proxy or WAF page),
	// network errors. The account keeps its cooldown and is retried.
	RefreshTransient RefreshFailure = iota
	// RefreshAccountRejected: the grant itself is dead — RFC 6749 §5.2 invalid_grant (refresh token invalid, expired
	// or revoked) or the equivalent codes of the OpenAI (Codex) token endpoint. Only a new login fixes it.
	RefreshAccountRejected
	// RefreshClientRejected: the request or the OAuth client is wrong for every account — RFC 6749 §5.2
	// invalid_request, invalid_client, unauthorized_client, unsupported_grant_type, invalid_scope. A gateway
	// configuration or code problem, not the account's.
	RefreshClientRejected
)

// accountRejectedCodes end one account's grant. invalid_grant is RFC 6749 §5.2; the refresh_token_* codes are what
// the OpenAI auth server returns for a Codex (ChatGPT) refresh token that expired, was already used, or was
// invalidated by a newer login (as the Codex CLI handles them).
var accountRejectedCodes = map[string]bool{
	"invalid_grant":             true,
	"refresh_token_expired":     true,
	"refresh_token_reused":      true,
	"refresh_token_invalidated": true,
}

var clientRejectedCodes = map[string]bool{
	"invalid_request":        true,
	"invalid_client":         true,
	"unauthorized_client":    true,
	"unsupported_grant_type": true,
	"invalid_scope":          true,
}

// Failure classifies the error. RFC 6749 §5.2 error responses are 400 (401 for invalid_client); a known code in a
// 2xx body counts too. 429 and 5xx are transient whatever the body says. The OpenAI (Codex) token endpoint answers a
// dead refresh token with 401 and codes it does not document ("Could not validate your refresh token"); the Codex CLI
// treats every 401 from it as permanent (openai/codex codex-rs/login/src/auth/manager.rs, request_chatgpt_token_refresh),
// so a Codex 401 without a client-level code is account-level here too.
func (e *RefreshError) Failure() RefreshFailure {
	if e.Status == http.StatusTooManyRequests || e.Status >= 500 || (e.Status >= 300 && e.Status < 400) {
		return RefreshTransient
	}
	switch {
	case accountRejectedCodes[e.Code]:
		return RefreshAccountRejected
	case clientRejectedCodes[e.Code]:
		return RefreshClientRejected
	case e.Status == http.StatusUnauthorized && e.Vendor == "codex":
		return RefreshAccountRejected
	}
	return RefreshTransient
}

// RefreshFailureOf classifies err; anything that is not a RefreshError (network, context) is transient.
func RefreshFailureOf(err error) (RefreshFailure, *RefreshError) {
	var re *RefreshError
	if errors.As(err, &re) {
		return re.Failure(), re
	}
	return RefreshTransient, nil
}

// oauthErrorCode reads the error code of a token endpoint error body: RFC 6749 §5.2 {"error": "<code>"}, or the
// OpenAI shape {"error": {"code": "<code>", ...}}.
func oauthErrorCode(b []byte) string {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &body) != nil || len(body.Error) == 0 {
		return ""
	}
	var code string
	if json.Unmarshal(body.Error, &code) == nil {
		return strings.TrimSpace(code)
	}
	var obj struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body.Error, &obj) == nil {
		return strings.TrimSpace(obj.Code)
	}
	return ""
}

// ClaudeRefresh exchanges a stored refresh_token for a new Claude access token.
func ClaudeRefresh(ctx context.Context, refreshToken string) (Tokens, error) {
	return refreshAt(ctx, "claude", ClaudeTokenURL, ClaudeClientID, refreshToken)
}

// CodexRefresh exchanges a stored refresh_token for a new Codex/ChatGPT access token.
func CodexRefresh(ctx context.Context, refreshToken string) (Tokens, error) {
	return refreshAt(ctx, "codex", CodexTokenURL, CodexClientID, refreshToken)
}

func refreshAt(ctx context.Context, vendor, tokenURL, clientID, refreshToken string) (Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := httpClient().Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return Tokens{}, &RefreshError{Vendor: vendor, Status: res.StatusCode, Code: oauthErrorCode(b), Detail: truncate(b, 300)}
	}
	tok, err := tokensFromRefreshJSON(b, refreshToken)
	var oe *OAuthError
	if errors.As(err, &oe) {
		return Tokens{}, &RefreshError{Vendor: vendor, Status: res.StatusCode, Code: oe.Code, Detail: oe.Error()}
	}
	return tok, err
}

// Refresh dispatches to the vendor-specific refresh flow. Vendor is matched
// case-insensitively; both the display name (claude/codex) and the upstream
// name (anthropic/openai) are accepted.
func Refresh(ctx context.Context, vendor, refreshToken string) (Tokens, error) {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "claude", "anthropic":
		return ClaudeRefresh(ctx, refreshToken)
	case "codex", "openai":
		return CodexRefresh(ctx, refreshToken)
	default:
		return Tokens{}, fmt.Errorf("no oauth refresh for vendor %q", vendor)
	}
}

// tokensFromRefreshJSON parses an oauth refresh response, falling back to the
// prior refresh_token when the upstream omits rotation (common for refresh
// grants) and preserving ChatGPTAccountID extraction from the new tokens.
func tokensFromRefreshJSON(b []byte, prevRefreshToken string) (Tokens, error) {
	tok, err := tokensFromOAuthJSON(b)
	if err != nil {
		return Tokens{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = prevRefreshToken
	}
	return tok, nil
}
