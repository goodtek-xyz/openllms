package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/health"
	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/google/uuid"
)

type memHealth struct {
	accounts []store.Account
	healthy  map[uuid.UUID]bool
}

func (m *memHealth) ListAllAccounts(ctx context.Context) ([]store.Account, error) {
	return m.accounts, nil
}
func (m *memHealth) MarkAccountHealthy(ctx context.Context, id uuid.UUID) error {
	m.healthy[id] = true
	return nil
}
func (m *memHealth) MarkAccountUnhealthy(ctx context.Context, id uuid.UUID, until time.Time) error {
	_ = until
	m.healthy[id] = false
	return nil
}

func TestProbeMarksHealthyAndUnhealthy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(401)
	}))
	defer up.Close()

	sec := memory.New()
	idOK, idBad := uuid.New(), uuid.New()
	pOK := vendor.InfisicalPath(uuid.New().String(), "deepseek", "ok")
	pBad := vendor.InfisicalPath(uuid.New().String(), "deepseek", "bad")
	_ = sec.Put(context.Background(), pOK, vendor.SecretName, `{"api_key":"good"}`)
	_ = sec.Put(context.Background(), pBad, vendor.SecretName, `{"api_key":"bad"}`)

	st := &memHealth{
		accounts: []store.Account{
			{ID: idOK, InfisicalPath: pOK, BaseURL: up.URL + "/v1", Vendor: "deepseek"},
			{ID: idBad, InfisicalPath: pBad, BaseURL: up.URL + "/v1", Vendor: "deepseek"},
		},
		healthy: map[uuid.UUID]bool{},
	}
	pr := &health.Prober{Store: st, Secrets: sec, HTTP: up.Client()}
	ok, bad, err := pr.ProbeOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok != 1 || bad != 1 {
		t.Fatalf("ok=%d bad=%d", ok, bad)
	}
	if !st.healthy[idOK] || st.healthy[idBad] {
		t.Fatalf("%v", st.healthy)
	}
}

var _ = secrets.CredentialJSON{}

func TestOAuthCodexProbeTreatsNon2xxAsHealthyUnlessAuthDeath(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.WriteHeader(404)
	}))
	defer up.Close()

	sec := memory.New()
	id := uuid.New()
	path := vendor.InfisicalPath(uuid.New().String(), "codex", "oauth")
	_ = sec.Put(context.Background(), path, vendor.SecretName, `{"access_token":"tok","chatgpt_account_id":"acct"}`)
	st := &memHealth{
		accounts: []store.Account{{
			ID: id, InfisicalPath: path, BaseURL: up.URL, Vendor: "codex", AuthType: "oauth",
		}},
		healthy: map[uuid.UUID]bool{},
	}
	pr := &health.Prober{Store: st, Secrets: sec, HTTP: up.Client()}
	ok, bad, err := pr.ProbeOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok != 1 || bad != 0 || !st.healthy[id] {
		t.Fatalf("ok=%d bad=%d healthy=%v", ok, bad, st.healthy)
	}
}
