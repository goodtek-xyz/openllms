package quota

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/google/uuid"
)

func TestRefreshPathCodexCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": 12.0, "reset_at": 99},
			},
		})
	}))
	defer srv.Close()
	prev := CodexUsageURL
	CodexUsageURL = srv.URL
	HTTPClient = srv.Client()
	t.Cleanup(func() { CodexUsageURL = prev; HTTPClient = nil })

	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "codex", "a")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{
		AccessToken: "t", ChatGPTAccountID: "acct",
	}))
	raw, _ := sec.Get(context.Background(), path, vendor.SecretName)
	var cred secrets.CredentialJSON
	_ = json.Unmarshal([]byte(raw), &cred)
	snap, err := FetchCodex(context.Background(), cred.AccessToken, cred.ChatGPTAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RemainingPct != 88 {
		t.Fatalf("%v", snap.RemainingPct)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Accounts waiting for a re-link and API-key credentials are not polled; a 401 reaches OnUsageAnswer.
func TestRefreshListSkipsAndReports401(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Provided authentication token is expired."}}`))
	}))
	defer srv.Close()
	prevC, prevA := CodexUsageURL, ClaudeUsageURL
	CodexUsageURL, ClaudeUsageURL, HTTPClient = srv.URL, srv.URL, srv.Client()
	t.Cleanup(func() { CodexUsageURL, ClaudeUsageURL, HTTPClient = prevC, prevA, nil })

	sec := memory.New()
	put := func(v, name string, cred secrets.CredentialJSON) store.Account {
		path := vendor.InfisicalPath(uuid.New().String(), v, name)
		_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(cred))
		return store.Account{ID: uuid.New(), Vendor: v, Name: name, AuthType: "oauth", InfisicalPath: path}
	}
	since := time.Now()
	waiting := put("claude", "waiting", secrets.CredentialJSON{AccessToken: "at"})
	waiting.ReauthRequiredAt = &since
	apiKey := put("claude", "key", secrets.CredentialJSON{AccessToken: "sk-ant-api03-x"})
	keyOnly := put("codex", "keyonly", secrets.CredentialJSON{APIKey: "sk-proj-x"})
	expired := put("codex", "expired", secrets.CredentialJSON{AccessToken: "at"})

	type answer struct {
		name    string
		status  int
		refresh bool
	}
	var got []answer
	r := &Refresher{Secrets: sec, OnUsageAnswer: func(_ context.Context, a store.Account, status int, refresh bool) {
		got = append(got, answer{a.Name, status, refresh})
	}}
	updated, failed := r.refreshList(context.Background(), []store.Account{waiting, apiKey, keyOnly, expired}, slog.Default())
	if updated != 0 || failed != 1 || hits != 1 {
		t.Fatalf("updated=%d failed=%d usage hits=%d, want 0 1 1", updated, failed, hits)
	}
	if len(got) != 1 || got[0] != (answer{"expired", http.StatusUnauthorized, false}) {
		t.Fatalf("answers=%+v", got)
	}
}
