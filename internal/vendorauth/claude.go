package vendorauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Public Claude Code OAuth client (no secret). Same as the official CLI.
const ClaudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

var (
	ClaudeAuthorizeURL = "https://claude.ai/oauth/authorize"
	ClaudeTokenURL     = "https://console.anthropic.com/v1/oauth/token"
	ClaudeRedirectURI  = "https://console.anthropic.com/oauth/code/callback"
	ClaudeScope        = "org:create_api_key user:profile user:inference"
)

// ClaudePending is the PKCE session shown to the user (code=true, no localhost).
type ClaudePending struct {
	AuthURL  string
	Verifier string
	State    string
}

func ClaudeStart() (ClaudePending, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return ClaudePending{}, err
	}
	// Anthropic's authorize page rejects a random state with "Invalid request format"
	// (브라우저: 인증 실패). Claude Code / known clients set state == PKCE verifier.
	state := verifier
	u, err := url.Parse(ClaudeAuthorizeURL)
	if err != nil {
		return ClaudePending{}, err
	}
	q := u.Query()
	q.Set("code", "true")
	q.Set("client_id", ClaudeClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", ClaudeRedirectURI)
	q.Set("scope", ClaudeScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return ClaudePending{AuthURL: u.String(), Verifier: verifier, State: state}, nil
}

func ClaudeExchange(ctx context.Context, pasted, verifier, state string) (Tokens, error) {
	code, returnedState := splitClaudeCode(pasted)
	if code == "" {
		return Tokens{}, fmt.Errorf("empty authorization code")
	}
	if returnedState == "" {
		returnedState = state
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", ClaudeClientID)
	form.Set("code", code)
	form.Set("redirect_uri", ClaudeRedirectURI)
	form.Set("code_verifier", verifier)
	form.Set("state", returnedState)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ClaudeTokenURL, strings.NewReader(form.Encode()))
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
		return Tokens{}, fmt.Errorf("claude token: %s %s", res.Status, truncate(b, 300))
	}
	return tokensFromOAuthJSON(b)
}

// splitClaudeCode extracts code and optional state from a paste. Accepts:
//
//	code#state
//	code=...#state
//	full callback URL with ?code=&state= (or # fragment)
func splitClaudeCode(pasted string) (code, state string) {
	s := strings.TrimSpace(pasted)
	if s == "" {
		return "", ""
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "http") {
		if u, err := url.Parse(s); err == nil {
			q := u.Query()
			code = strings.TrimSpace(q.Get("code"))
			state = strings.TrimSpace(q.Get("state"))
			if code == "" && u.Fragment != "" {
				// rare: code in fragment as code=x&state=y
				fq, _ := url.ParseQuery(u.Fragment)
				code = strings.TrimSpace(fq.Get("code"))
				if state == "" {
					state = strings.TrimSpace(fq.Get("state"))
				}
			}
			if code != "" {
				return code, state
			}
		}
	}
	s = strings.TrimPrefix(s, "code=")
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	if i := strings.Index(s, "&state="); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len("&state="):])
	}
	return s, ""
}
