package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/router"
	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/goodtek-xyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

// reauthStore records reauth flags with the store's first-wins semantics; other AccountStore methods are unused.
type reauthStore struct {
	AccountStore
	mu      sync.Mutex
	flagged map[uuid.UUID]int // detections per account
	emails  map[uuid.UUID]string
}

func (s *reauthStore) MarkAccountReauthRequired(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flagged == nil {
		s.flagged = map[uuid.UUID]int{}
	}
	s.flagged[id]++
	return s.flagged[id] == 1, nil
}

func (s *reauthStore) SetAccountEmail(_ context.Context, id uuid.UUID, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emails == nil {
		s.emails = map[uuid.UUID]string{}
	}
	s.emails[id] = email
	return nil
}

func (s *reauthStore) detections(id uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flagged[id]
}

type notifyRecorder struct {
	mu      sync.Mutex
	events  []ReauthEvent
	clients []OAuthClientEvent
}

func (r *notifyRecorder) OAuthClientRejected(ev OAuthClientEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients = append(r.clients, ev)
}

func (r *notifyRecorder) AccountReauthRequired(ev ReauthEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *notifyRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// useTokenEndpoint points the Claude token URL at h for the test.
func useTokenEndpoint(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	tok := httptest.NewServer(h)
	t.Cleanup(tok.Close)
	prev, prevHTTP := vendorauth.ClaudeTokenURL, vendorauth.HTTPClient
	vendorauth.ClaudeTokenURL, vendorauth.HTTPClient = tok.URL, tok.Client()
	t.Cleanup(func() { vendorauth.ClaudeTokenURL, vendorauth.HTTPClient = prev, prevHTTP })
	return tok
}

func oauthAccount(t *testing.T, sec secrets.Client, upURL string, cred secrets.CredentialJSON) *store.Account {
	t.Helper()
	path := "/llms/p/accounts/claude/" + uuid.NewString()[:8]
	b, _ := json.Marshal(cred)
	_ = sec.Put(context.Background(), path, vendor.SecretName, string(b))
	email := "owner@example.com"
	return &store.Account{
		ID: uuid.New(), ProjectID: uuid.New(), Vendor: "claude", Name: "work", AuthType: "oauth",
		InfisicalPath: path, BaseURL: upURL, Email: &email,
	}
}

func TestReauthDetection(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-at", "expires_in": 3600})
	}
	status := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}
	}
	cases := []struct {
		name       string
		token      http.HandlerFunc
		retryCode  int // upstream answer to the refreshed token
		wantReauth bool
		wantClient bool // reported once to the admin as a client configuration error
	}{
		{"refresh 400 invalid_grant", status(400, `{"error":"invalid_grant"}`), 200, true, false},
		{"refresh 200 with invalid_grant body", status(200, `{"error":"invalid_grant"}`), 200, true, false},
		{"refresh 401 codex refresh_token_reused", status(401, `{"error":{"code":"refresh_token_reused"}}`), 200, true, false},
		{"refresh 401 invalid_client", status(401, `{"error":"invalid_client"}`), 200, false, true},
		{"refresh 400 unsupported_grant_type", status(400, `{"error":"unsupported_grant_type"}`), 200, false, true},
		{"refresh 403 WAF page", status(403, `<html>denied</html>`), 200, false, false},
		{"refresh 400 without code", status(400, `bad`), 200, false, false},
		{"refresh 503", status(503, `upstream down`), 200, false, false},
		{"refresh 429", status(429, `{"error":"rate_limited"}`), 200, false, false},
		{"refresh 500 with error code", status(500, `{"error":"invalid_grant"}`), 200, false, false},
		{"refreshed token rejected 401", ok, 401, true, false},
		{"refreshed token rejected 403", ok, 403, false, false},
		{"refreshed token works", ok, 200, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTokenEndpoint(t, tc.token)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.Header.Get("Authorization"), "old-at") {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(tc.retryCode)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer up.Close()
			sec := memory.New()
			acc := oauthAccount(t, sec, up.URL, secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"})
			st, rec := &reauthStore{}, &notifyRecorder{}
			eng := &Engine{Store: st, Secrets: sec, HTTP: up.Client(), Reauth: rec}

			res, err := eng.doUpstream(context.Background(), acc, []byte(`{"model":"m"}`), up.URL+"/v1/messages", "Authorization", "Bearer ", true)
			if err != nil {
				t.Fatal(err)
			}
			flagged := st.detections(acc.ID) > 0
			if flagged != tc.wantReauth || res.ReauthRequired != tc.wantReauth {
				t.Fatalf("flagged=%v ReauthRequired=%v, want %v (status %d)", flagged, res.ReauthRequired, tc.wantReauth, res.StatusCode)
			}
			if got := len(rec.clients) == 1; got != tc.wantClient {
				t.Fatalf("client-error reports = %d, want reported=%v", len(rec.clients), tc.wantClient)
			}
			if tc.wantReauth {
				if rec.count() != 1 {
					t.Fatalf("notifications = %d, want 1", rec.count())
				}
				ev := rec.events[0]
				if ev.AccountID != acc.ID || ev.ProjectID != acc.ProjectID || ev.Email != "owner@example.com" || ev.Reason == "" {
					t.Fatalf("event = %+v", ev)
				}
				if strings.Contains(ev.Reason, "old-rt") || strings.Contains(ev.Reason, "new-at") {
					t.Fatalf("event reason leaks a token: %q", ev.Reason)
				}
			} else if rec.count() != 0 {
				t.Fatalf("notifications = %d, want 0", rec.count())
			}
		})
	}
}

// A client-level rejection is reported once per vendor and code, however many accounts and requests hit it.
func TestClientRejectedReportedOnce(t *testing.T) {
	useTokenEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer up.Close()
	sec := memory.New()
	st, rec := &reauthStore{}, &notifyRecorder{}
	eng := &Engine{Store: st, Secrets: sec, HTTP: up.Client(), Reauth: rec}
	for i := 0; i < 3; i++ {
		acc := oauthAccount(t, sec, up.URL, secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"})
		_, _ = eng.doUpstream(context.Background(), acc, []byte(`{}`), up.URL, "Authorization", "Bearer ", true)
	}
	if len(rec.clients) != 1 || rec.count() != 0 || len(st.flagged) != 0 {
		t.Fatalf("client reports=%d reauth=%d flagged=%d, want 1, 0, 0", len(rec.clients), rec.count(), len(st.flagged))
	}
}

// A 401 for an OAuth account without a refresh token is flagged only when seen again a full 401 cooldown later;
// any other answer in between clears the observation.
func TestUnauthorizedWithoutRefreshTokenNeedsConfirmation(t *testing.T) {
	var code atomic.Int32
	code.Store(http.StatusUnauthorized)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(int(code.Load())) }))
	defer up.Close()
	sec := memory.New()
	acc := oauthAccount(t, sec, up.URL, secrets.CredentialJSON{AccessToken: "at"})
	st, rec := &reauthStore{}, &notifyRecorder{}
	eng := &Engine{Store: st, Secrets: sec, HTTP: up.Client(), Reauth: rec}
	call := func() *Result {
		t.Helper()
		res, err := eng.doUpstream(context.Background(), acc, []byte(`{}`), up.URL, "Authorization", "Bearer ", true)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	backdate := func() { // the first observation was a full cooldown ago
		eng.unauthorized.Store(acc.ID, time.Now().Add(-router.CooldownForStatus(http.StatusUnauthorized, 0)))
	}

	if call().ReauthRequired || call().ReauthRequired {
		t.Fatal("401s within one cooldown must not flag")
	}
	backdate()
	code.Store(http.StatusOK)
	call() // a success clears the observation
	code.Store(http.StatusUnauthorized)
	if call().ReauthRequired {
		t.Fatal("the observation must restart after a success")
	}
	backdate()
	if res := call(); !res.ReauthRequired || st.detections(acc.ID) != 1 || rec.count() != 1 {
		t.Fatalf("second 401 a cooldown later: ReauthRequired=%v detections=%d notifications=%d", res.ReauthRequired, st.detections(acc.ID), rec.count())
	}
}

// A re-link during an in-flight request: the failure belongs to the old login, so the account is not flagged and
// not cooled down.
func TestRelinkDuringRefreshIsNotFlagged(t *testing.T) {
	sec := memory.New()
	var acc *store.Account
	useTokenEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		// The owner re-links while the old refresh token is being rejected.
		b, _ := json.Marshal(secrets.CredentialJSON{AccessToken: "relinked-at", RefreshToken: "relinked-rt"})
		_ = sec.Put(context.Background(), acc.InfisicalPath, vendor.SecretName, string(b))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer up.Close()
	acc = oauthAccount(t, sec, up.URL, secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"})
	st, rec := &reauthStore{}, &notifyRecorder{}
	eng := &Engine{Store: st, Secrets: sec, HTTP: up.Client(), Reauth: rec}
	res, _ := eng.doUpstream(context.Background(), acc, []byte(`{}`), up.URL, "Authorization", "Bearer ", true)
	if res.ReauthRequired || !res.CredentialReplaced || st.detections(acc.ID) != 0 || rec.count() != 0 {
		t.Fatalf("ReauthRequired=%v CredentialReplaced=%v detections=%d notifications=%d",
			res.ReauthRequired, res.CredentialReplaced, st.detections(acc.ID), rec.count())
	}
}

// Concurrent requests (and replicas, through the store's conditional UPDATE) detect the same incident; one
// notification goes out.
func TestReauthNotifiesOncePerIncident(t *testing.T) {
	useTokenEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()
	sec := memory.New()
	acc := oauthAccount(t, sec, up.URL, secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"})
	st, rec := &reauthStore{}, &notifyRecorder{}
	eng := &Engine{Store: st, Secrets: sec, HTTP: up.Client(), Reauth: rec}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := *acc
			_, _ = eng.doUpstream(context.Background(), &a, []byte(`{"model":"m"}`), up.URL+"/v1/messages", "Authorization", "Bearer ", true)
		}()
	}
	wg.Wait()
	if st.detections(acc.ID) < 1 || rec.count() != 1 {
		t.Fatalf("detections=%d notifications=%d, want ≥1 and exactly 1", st.detections(acc.ID), rec.count())
	}
}

func idToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

// A refresh response without id_token (allowed: OIDC Core §12.2) keeps the stored one.
func TestRefreshKeepsStoredIDToken(t *testing.T) {
	useTokenEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-at", "refresh_token": "new-rt", "expires_in": 3600})
	})
	sec := memory.New()
	idt := idToken(map[string]any{"email": "dev@example.com"})
	acc := oauthAccount(t, sec, "http://unused.invalid", secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt", IDToken: idt})
	eng := &Engine{Secrets: sec}
	old, _, _ := eng.loadCredential(context.Background(), acc)
	cred, err := eng.refreshOAuthCredential(context.Background(), acc, old)
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _ := eng.loadCredential(context.Background(), acc)
	if cred.IDToken != idt || stored.IDToken != idt || stored.AccessToken != "new-at" {
		t.Fatalf("id_token lost: returned %q stored %+v", cred.IDToken, stored)
	}
}

func TestRefreshExpiringOAuthFlagsRejected(t *testing.T) {
	var tokenHits atomic.Int32
	useTokenEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		tokenHits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	})
	sec := memory.New()
	soon := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	dead := oauthAccount(t, sec, "http://unused.invalid", secrets.CredentialJSON{AccessToken: "at", RefreshToken: "rt", ExpiresAt: soon})
	waiting := oauthAccount(t, sec, "http://unused.invalid", secrets.CredentialJSON{AccessToken: "at", RefreshToken: "rt", ExpiresAt: soon})
	since := time.Now()
	waiting.ReauthRequiredAt = &since

	st, rec := &reauthStore{}, &notifyRecorder{}
	eng := &Engine{Store: st, Secrets: sec, Reauth: rec}
	updated, failed, skipped := eng.RefreshExpiringOAuth(context.Background(), []store.Account{*dead, *waiting}, time.Hour)
	if updated != 0 || failed != 1 || skipped != 1 {
		t.Fatalf("updated=%d failed=%d skipped=%d", updated, failed, skipped)
	}
	if tokenHits.Load() != 1 {
		t.Fatalf("token endpoint hits = %d, want 1 (the waiting reauth account is not refreshed)", tokenHits.Load())
	}
	if st.detections(dead.ID) != 1 || rec.count() != 1 {
		t.Fatalf("detections=%d notifications=%d", st.detections(dead.ID), rec.count())
	}
}

// The account email comes from the vendor's refresh response (id_token email claim), never from client input, and
// replaces a stale one.
func TestRefreshRecordsVendorEmail(t *testing.T) {
	useTokenEndpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-at", "expires_in": 3600, "id_token": idToken(map[string]any{"email": "dev@example.com"}),
		})
	})
	sec := memory.New()
	acc := oauthAccount(t, sec, "http://unused.invalid", secrets.CredentialJSON{
		AccessToken: "old-at", RefreshToken: "old-rt", IDToken: idToken(map[string]any{"email": "forged@example.com"}),
	})
	st := &reauthStore{}
	eng := &Engine{Store: st, Secrets: sec}
	old, _, _ := eng.loadCredential(context.Background(), acc)
	if _, err := eng.refreshOAuthCredential(context.Background(), acc, old); err != nil {
		t.Fatal(err)
	}
	if st.emails[acc.ID] != "dev@example.com" || acc.Email == nil || *acc.Email != "dev@example.com" {
		t.Fatalf("stored email %q, account email %v", st.emails[acc.ID], acc.Email)
	}
}

// The usage poll applies the request path's rule: two 401s a cooldown apart, no refresh token → re-link; any other
// answer restarts the observation; a renewable login is left to the refresh flows.
func TestUsageAnswerNeedsConfirmation(t *testing.T) {
	sec := memory.New()
	acc := oauthAccount(t, sec, "http://unused.invalid", secrets.CredentialJSON{AccessToken: "at"})
	st, rec := &reauthStore{}, &notifyRecorder{}
	eng := &Engine{Store: st, Secrets: sec, Reauth: rec}
	ctx := context.Background()
	backdate := func() {
		eng.unauthorized.Store(acc.ID, time.Now().Add(-router.CooldownForStatus(http.StatusUnauthorized, 0)))
	}

	eng.UsageAnswer(ctx, *acc, http.StatusUnauthorized, false)
	eng.UsageAnswer(ctx, *acc, http.StatusUnauthorized, false)
	if st.detections(acc.ID) != 0 {
		t.Fatal("401s within one cooldown must not flag")
	}
	backdate()
	eng.UsageAnswer(ctx, *acc, http.StatusOK, false)
	eng.UsageAnswer(ctx, *acc, http.StatusUnauthorized, false)
	if st.detections(acc.ID) != 0 {
		t.Fatal("a success must restart the observation")
	}
	backdate()
	eng.UsageAnswer(ctx, *acc, http.StatusUnauthorized, true)
	if st.detections(acc.ID) != 0 {
		t.Fatal("a login with a refresh token is left to the refresh flows")
	}
	eng.UsageAnswer(ctx, *acc, http.StatusUnauthorized, false)
	if st.detections(acc.ID) != 1 || rec.count() != 1 {
		t.Fatalf("second 401 a cooldown later: detections=%d notifications=%d", st.detections(acc.ID), rec.count())
	}
	since := time.Now()
	acc.ReauthRequiredAt = &since
	backdate()
	eng.UsageAnswer(ctx, *acc, http.StatusUnauthorized, false)
	if st.detections(acc.ID) != 1 {
		t.Fatal("an account already waiting for a re-link is not flagged again")
	}
}

// A Codex refresh token the OpenAI token endpoint rejects with 401 (undocumented body) flags the account once,
// instead of being retried every poll.
func TestRefreshExpiringOAuthFlagsCodex401(t *testing.T) {
	var hits atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Could not validate your refresh token. Please try signing in again.","type":"invalid_request_error"}}`))
	}))
	defer tok.Close()
	prev, prevHTTP := vendorauth.CodexTokenURL, vendorauth.HTTPClient
	vendorauth.CodexTokenURL, vendorauth.HTTPClient = tok.URL, tok.Client()
	t.Cleanup(func() { vendorauth.CodexTokenURL, vendorauth.HTTPClient = prev, prevHTTP })

	sec := memory.New()
	soon := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	acc := oauthAccount(t, sec, "http://unused.invalid", secrets.CredentialJSON{AccessToken: "at", RefreshToken: "rt", ExpiresAt: soon})
	acc.Vendor = "codex"
	st, rec := &reauthStore{}, &notifyRecorder{}
	eng := &Engine{Store: st, Secrets: sec, Reauth: rec}
	if _, failed, _ := eng.RefreshExpiringOAuth(context.Background(), []store.Account{*acc}, time.Hour); failed != 1 {
		t.Fatalf("failed=%d, want 1", failed)
	}
	if st.detections(acc.ID) != 1 || rec.count() != 1 || hits.Load() != 1 {
		t.Fatalf("detections=%d notifications=%d token hits=%d", st.detections(acc.ID), rec.count(), hits.Load())
	}
}

// An Anthropic API key stored on an OAuth account authenticates as a key (x-api-key), not as a bearer token, which
// Anthropic answers with 401 "x-api-key header is required".
func TestAnthropicAPIKeyOnOAuthAccountUsesXAPIKey(t *testing.T) {
	acc := &store.Account{Vendor: "claude", AuthType: "oauth"}
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	applyUpstreamAuth(req, acc, secrets.CredentialJSON{AccessToken: "sk-ant-api03-test"}, "sk-ant-api03-test", false, "x-api-key", "")
	if got := req.Header.Get("x-api-key"); got != "sk-ant-api03-test" {
		t.Fatalf("x-api-key=%q", got)
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("anthropic-beta") != "" {
		t.Fatalf("API key sent with OAuth headers: %v", req.Header)
	}
	req = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	applyUpstreamAuth(req, acc, secrets.CredentialJSON{AccessToken: "sk-ant-oat01-test"}, "sk-ant-oat01-test", false, "x-api-key", "")
	if req.Header.Get("Authorization") != "Bearer sk-ant-oat01-test" || req.Header.Get("x-api-key") != "" {
		t.Fatalf("OAuth token headers: %v", req.Header)
	}
}
