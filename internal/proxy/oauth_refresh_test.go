package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/goodtek-xyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

func TestOAuthRefreshOn401ThenRetry(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		auth := r.Header.Get("Authorization")
		if n == 1 {
			if auth != "Bearer old-at" {
				t.Errorf("first auth %q", auth)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"expired"}`))
			return
		}
		if auth != "Bearer new-at" {
			t.Errorf("retry auth %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-at", "refresh_token": "new-rt", "expires_in": 3600,
		})
	}))
	defer tok.Close()
	prev := vendorauth.ClaudeTokenURL
	vendorauth.ClaudeTokenURL = tok.URL
	prevHTTP := vendorauth.HTTPClient
	vendorauth.HTTPClient = tok.Client()
	t.Cleanup(func() {
		vendorauth.ClaudeTokenURL = prev
		vendorauth.HTTPClient = prevHTTP
	})

	sec := memory.New()
	path := "/llms/p/accounts/claude/work"
	b, _ := json.Marshal(secrets.CredentialJSON{
		AccessToken: "old-at", RefreshToken: "old-rt",
	})
	_ = sec.Put(context.Background(), path, vendor.SecretName, string(b))
	acc := &store.Account{
		ID: uuid.New(), Vendor: "claude", Name: "work", AuthType: "oauth",
		InfisicalPath: path, BaseURL: up.URL, Health: "ok",
	}
	eng := &Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.doUpstream(context.Background(), acc, []byte(`{"model":"m","messages":[]}`), up.URL+"/v1/messages", "x-api-key", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d body %s", res.StatusCode, res.Body)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits %d", hits.Load())
	}
	raw, _ := sec.Get(context.Background(), path, vendor.SecretName)
	if !strings.Contains(raw, "new-at") || !strings.Contains(raw, "new-rt") {
		t.Fatalf("secret not rotated: %s", raw)
	}
	_ = io.Discard
}

func TestOAuthRefreshSingleflight(t *testing.T) {
	var refreshHits atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHits.Add(1)
		time.Sleep(80 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-at", "refresh_token": "new-rt", "expires_in": 3600,
		})
	}))
	defer tok.Close()
	prev := vendorauth.ClaudeTokenURL
	vendorauth.ClaudeTokenURL = tok.URL
	prevHTTP := vendorauth.HTTPClient
	vendorauth.HTTPClient = tok.Client()
	t.Cleanup(func() {
		vendorauth.ClaudeTokenURL = prev
		vendorauth.HTTPClient = prevHTTP
	})

	var upHits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := upHits.Add(1)
		if strings.Contains(r.Header.Get("Authorization"), "old-at") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if n > 0 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer up.Close()

	sec := memory.New()
	path := "/llms/p/accounts/claude/sf"
	b, _ := json.Marshal(secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"})
	_ = sec.Put(context.Background(), path, vendor.SecretName, string(b))
	accID := uuid.New()
	eng := &Engine{Secrets: sec, HTTP: up.Client()}

	var wg sync.WaitGroup
	const n = 8
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			acc := &store.Account{
				ID: accID, Vendor: "claude", Name: "sf", AuthType: "oauth",
				InfisicalPath: path, BaseURL: up.URL,
			}
			res, err := eng.doUpstream(context.Background(), acc, []byte(`{"model":"m"}`), up.URL+"/v1/messages", "Authorization", "Bearer ", true)
			if err != nil {
				errs <- err
				return
			}
			if res.StatusCode != 200 {
				errs <- fmt.Errorf("status %d", res.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if refreshHits.Load() != 1 {
		t.Fatalf("refresh hits=%d want 1", refreshHits.Load())
	}
}

func TestOAuthRefreshSurvivesPutFailure(t *testing.T) {
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-at", "refresh_token": "new-rt", "expires_in": 3600,
		})
	}))
	defer tok.Close()
	prev := vendorauth.ClaudeTokenURL
	vendorauth.ClaudeTokenURL = tok.URL
	prevHTTP := vendorauth.HTTPClient
	vendorauth.HTTPClient = tok.Client()
	t.Cleanup(func() {
		vendorauth.ClaudeTokenURL = prev
		vendorauth.HTTPClient = prevHTTP
	})

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "old-at") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(auth, "new-at") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer up.Close()

	inner := memory.New()
	path := "/llms/p/accounts/claude/putfail"
	b, _ := json.Marshal(secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"})
	_ = inner.Put(context.Background(), path, vendor.SecretName, string(b))
	sec := &flakyPut{inner: inner, failLeft: 100} // all Puts fail during refresh retries
	acc := &store.Account{
		ID: uuid.New(), Vendor: "claude", Name: "putfail", AuthType: "oauth",
		InfisicalPath: path, BaseURL: up.URL,
	}
	eng := &Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.doUpstream(context.Background(), acc, []byte(`{"model":"m"}`), up.URL+"/v1/messages", "Authorization", "Bearer ", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("want 200 despite Put fail, got %d", res.StatusCode)
	}
	// Durable store still old
	raw, _ := inner.Get(context.Background(), path, vendor.SecretName)
	if strings.Contains(raw, "new-at") {
		t.Fatalf("expected durable secret still old, got %s", raw)
	}
	// Next refresh path should retry pending Put when flaky allows
	sec.failLeft = 0
	cred, rerr := eng.refreshOAuthCredential(context.Background(), acc, secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if cred.AccessToken != "new-at" {
		t.Fatalf("cred %+v", cred)
	}
	raw, _ = inner.Get(context.Background(), path, vendor.SecretName)
	if !strings.Contains(raw, "new-at") {
		t.Fatalf("pending put should have persisted: %s", raw)
	}
}

func TestOAuthAlreadyRotatedSkipsRefresh(t *testing.T) {
	var refreshHits atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"should_not_call"}`))
	}))
	defer tok.Close()
	prev := vendorauth.ClaudeTokenURL
	vendorauth.ClaudeTokenURL = tok.URL
	prevHTTP := vendorauth.HTTPClient
	vendorauth.HTTPClient = tok.Client()
	t.Cleanup(func() {
		vendorauth.ClaudeTokenURL = prev
		vendorauth.HTTPClient = prevHTTP
	})

	sec := memory.New()
	path := "/llms/p/accounts/claude/rotated"
	// Durable already has new tokens (another goroutine persisted them).
	b, _ := json.Marshal(secrets.CredentialJSON{AccessToken: "new-at", RefreshToken: "new-rt"})
	_ = sec.Put(context.Background(), path, vendor.SecretName, string(b))
	acc := &store.Account{
		ID: uuid.New(), Vendor: "claude", Name: "rotated", AuthType: "oauth",
		InfisicalPath: path,
	}
	eng := &Engine{Secrets: sec}
	old := secrets.CredentialJSON{AccessToken: "old-at", RefreshToken: "old-rt"}
	cred, err := eng.refreshOAuthCredential(context.Background(), acc, old)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "new-at" || cred.RefreshToken != "new-rt" {
		t.Fatalf("cred %+v", cred)
	}
	if refreshHits.Load() != 0 {
		t.Fatalf("refresh should be skipped, hits=%d", refreshHits.Load())
	}
}

func TestRefreshExpiringOAuth(t *testing.T) {
	var refreshHits atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "pro-at", "refresh_token": "pro-rt", "expires_in": 3600,
		})
	}))
	defer tok.Close()
	prev := vendorauth.CodexTokenURL
	vendorauth.CodexTokenURL = tok.URL
	prevHTTP := vendorauth.HTTPClient
	vendorauth.HTTPClient = tok.Client()
	t.Cleanup(func() {
		vendorauth.CodexTokenURL = prev
		vendorauth.HTTPClient = prevHTTP
	})

	sec := memory.New()
	pathSoon := "/llms/p/accounts/codex/soon"
	pathLater := "/llms/p/accounts/codex/later"
	soonExp := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)
	laterExp := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	_ = sec.Put(context.Background(), pathSoon, vendor.SecretName, mustCredJSON(secrets.CredentialJSON{
		AccessToken: "a1", RefreshToken: "r1", ExpiresAt: soonExp,
	}))
	_ = sec.Put(context.Background(), pathLater, vendor.SecretName, mustCredJSON(secrets.CredentialJSON{
		AccessToken: "a2", RefreshToken: "r2", ExpiresAt: laterExp,
	}))
	accounts := []store.Account{
		{ID: uuid.New(), Vendor: "codex", Name: "soon", AuthType: "oauth", InfisicalPath: pathSoon},
		{ID: uuid.New(), Vendor: "codex", Name: "later", AuthType: "oauth", InfisicalPath: pathLater},
		{ID: uuid.New(), Vendor: "codex", Name: "key", AuthType: "api_key", InfisicalPath: "/x"},
	}
	eng := &Engine{Secrets: sec}
	updated, failed, skipped := eng.RefreshExpiringOAuth(context.Background(), accounts, time.Hour)
	if updated != 1 || failed != 0 {
		t.Fatalf("updated=%d failed=%d skipped=%d", updated, failed, skipped)
	}
	if refreshHits.Load() != 1 {
		t.Fatalf("refresh hits=%d", refreshHits.Load())
	}
	raw, _ := sec.Get(context.Background(), pathSoon, vendor.SecretName)
	if !strings.Contains(raw, "pro-at") {
		t.Fatalf("soon not rotated: %s", raw)
	}
	raw2, _ := sec.Get(context.Background(), pathLater, vendor.SecretName)
	if strings.Contains(raw2, "pro-at") {
		t.Fatalf("later should not rotate: %s", raw2)
	}
}

func TestExpiresWithin(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if !expiresWithin(now.Add(30*time.Minute).Format(time.RFC3339), now, time.Hour) {
		t.Fatal("expected within")
	}
	if expiresWithin(now.Add(2*time.Hour).Format(time.RFC3339), now, time.Hour) {
		t.Fatal("expected outside")
	}
	if expiresWithin("", now, time.Hour) {
		t.Fatal("empty")
	}
}

type flakyPut struct {
	inner    *memory.Client
	failLeft int32
}

func (f *flakyPut) Put(ctx context.Context, path, name, value string) error {
	if atomic.AddInt32(&f.failLeft, -1) >= 0 {
		return fmt.Errorf("injected put failure")
	}
	return f.inner.Put(ctx, path, name, value)
}

func (f *flakyPut) Get(ctx context.Context, path, name string) (string, error) {
	return f.inner.Get(ctx, path, name)
}

func (f *flakyPut) Delete(ctx context.Context, path, name string) error {
	return f.inner.Delete(ctx, path, name)
}

func mustCredJSON(c secrets.CredentialJSON) string {
	b, _ := json.Marshal(c)
	return string(b)
}
