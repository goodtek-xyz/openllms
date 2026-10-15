package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
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
