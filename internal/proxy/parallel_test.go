package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/proxy"
	"github.com/goodtek-xyz/openllms/internal/router"
	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/google/uuid"
)

func TestParallelPicksFasterTTFT(t *testing.T) {
	var slowHits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if auth == "Bearer sk-slow" {
			slowHits.Add(1)
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"slow\"}}]}\n\n"))
			flusher.Flush()
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"fast\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer up.Close()

	sec := memory.New()
	idSlow, idFast := uuid.New(), uuid.New()
	pathS := vendor.InfisicalPath(uuid.New().String(), "deepseek", "slow")
	pathF := vendor.InfisicalPath(uuid.New().String(), "deepseek", "fast")
	_ = sec.Put(context.Background(), pathS, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-slow"}))
	_ = sec.Put(context.Background(), pathF, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-fast"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idSlow, InfisicalPath: pathS, BaseURL: up.URL + "/v1", Vendor: "deepseek", AuthType: "api_key", Weight: 1},
		{ID: idFast, InfisicalPath: pathF, BaseURL: up.URL + "/v1", Vendor: "deepseek", AuthType: "api_key", Weight: 1},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Parallel)}
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, []byte(`{"model":"m","stream":true,"messages":[]}`), proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AccountID != idFast {
		t.Fatalf("want fast account, got %s strategy=%s", res.AccountID, res.Strategy)
	}
	if res.Strategy != string(router.Parallel) {
		t.Fatalf("strategy %s", res.Strategy)
	}
}

func TestParallelSkipsOAuthUnlessAllowed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer up.Close()
	sec := memory.New()
	id := uuid.New()
	path := vendor.InfisicalPath(uuid.New().String(), "claude", "o")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "tok"}))
	st := &memRouteStore{accounts: []store.Account{
		{ID: id, InfisicalPath: path, BaseURL: up.URL + "/v1", Vendor: "claude", AuthType: "oauth", Weight: 1},
	}, cool: map[uuid.UUID]time.Time{}}
	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Parallel)}
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, []byte(`{"model":"m","messages":[{"role":"user","content":"x"}]}`), proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	// Falls back to sequential — still succeeds
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, res.Error)
	}
}
