package vendorauth

import (
	"encoding/base64"
	"encoding/json"
	"net/mail"
	"strings"
	"time"
)

// Tokens is the oauth material stored via the gateway (Infisical).
type Tokens struct {
	AccessToken      string
	RefreshToken     string
	IDToken          string
	ExpiresAt        string
	ChatGPTAccountID string
	Email            string // vendor account email when the response carries one; "" = unknown
}

// OAuthError is an OAuth error response body (RFC 6749 §5.2: "error", optional "error_description").
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

func tokensFromOAuthJSON(b []byte) (Tokens, error) {
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
		// Claude's token response describes the signed-in account; optional, absent on some responses.
		Account struct {
			EmailAddress string `json:"email_address"`
		} `json:"account"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Tokens{}, err
	}
	if out.Error != "" {
		return Tokens{}, &OAuthError{Code: out.Error, Description: out.ErrorDesc}
	}
	if out.AccessToken == "" {
		return Tokens{}, errString("no access_token in oauth response")
	}
	t := Tokens{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		IDToken:      out.IDToken,
	}
	if out.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	t.Email = CleanEmail(out.Account.EmailAddress)
	if t.Email == "" {
		t.Email = EmailFromIDToken(out.IDToken)
	}
	if id := chatgptAccountID(out.IDToken); id != "" {
		t.ChatGPTAccountID = id
	} else {
		t.ChatGPTAccountID = chatgptAccountID(out.AccessToken)
	}
	return t, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func chatgptAccountID(jwt string) string {
	payload := jwtPayload(jwt)
	if payload == nil {
		return ""
	}
	auth, _ := payload["https://api.openai.com/auth"].(map[string]any)
	if auth == nil {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return strings.TrimSpace(id)
}

// EmailFromIDToken returns the OpenID Connect "email" claim of an id_token (Codex / ChatGPT sign-in), or "".
// The token is only decoded, not verified: it came from the token endpoint over TLS and is used for display and
// notifications, not for authorization.
func EmailFromIDToken(idToken string) string {
	payload := jwtPayload(idToken)
	if payload == nil {
		return ""
	}
	email, _ := payload["email"].(string)
	return CleanEmail(email)
}

// CleanEmail returns s as a bare address (RFC 5322 addr-spec, via net/mail) or "" when it is not one. Control
// characters (CR/LF: mail header injection) and display names are rejected.
func CleanEmail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ""
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Name != "" || addr.Address != s {
		return ""
	}
	return addr.Address
}

func jwtPayload(jwt string) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := parts[1]
	if m := len(seg) % 4; m != 0 {
		seg += strings.Repeat("=", 4-m)
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
