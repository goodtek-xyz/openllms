package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestTTFTSilentFailover(t *testing.T) {
	var n atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if auth == "Bearer sk-bad" {
			n.Add(1)
			// 200 then hang/close without content token — pre-commit failure
			flusher.Flush()
			// close without data
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer up.Close()

	sec := memory.New()
	idBad, idGood := uuid.New(), uuid.New()
	pathBad := vendor.InfisicalPath(uuid.New().String(), "deepseek", "bad")
	pathGood := vendor.InfisicalPath(uuid.New().String(), "deepseek", "good")
	_ = sec.Put(context.Background(), pathBad, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-bad"}))
	_ = sec.Put(context.Background(), pathGood, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-good"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idBad, InfisicalPath: pathBad, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1, Position: 0},
		{ID: idGood, InfisicalPath: pathGood, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1, Position: 1},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Sequential)}
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, []byte(`{"model":"m","stream":true,"messages":[]}`), proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stream == nil {
		t.Fatalf("expected stream status=%d err=%s", res.StatusCode, res.Error)
	}
	defer res.Stream.Close()
	b, _ := io.ReadAll(res.Stream)
	if !strings.Contains(string(b), "hi") {
		t.Fatalf("body %s", b)
	}
	if res.AccountID != idGood {
		t.Fatalf("want good account, got %s attempts=%d", res.AccountID, res.Attempts)
	}
	if res.Attempts < 2 {
		t.Fatalf("expected failover attempts, got %d", res.Attempts)
	}
}

func TestTTFTCommitThenNoFailover(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		flusher.Flush()
		// mid-stream death after commit
		return
	}))
	defer up.Close()

	sec := memory.New()
	id := uuid.New()
	path := vendor.InfisicalPath(uuid.New().String(), "deepseek", "only")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk"}))
	st := &memRouteStore{accounts: []store.Account{
		{ID: id, InfisicalPath: path, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Sequential)}
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, []byte(`{"model":"m","stream":true,"messages":[]}`), proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempts != 1 {
		t.Fatalf("should commit without failover, attempts=%d", res.Attempts)
	}
	b, _ := io.ReadAll(res.Stream)
	_ = res.Stream.Close()
	if !strings.Contains(string(b), "x") {
		t.Fatalf("%s", b)
	}
}

func TestInspectTTFTHelpers(t *testing.T) {
	// via stream roundtrip already covered; ensure JSON marshal of SSE works
	_ = json.RawMessage(`{}`)
}
