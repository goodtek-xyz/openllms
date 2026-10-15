package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestSessionKeyOrder(t *testing.T) {
	body := []byte(`{"session_id":"from-body","prompt_cache_key":"pck","messages":[{"role":"user","content":"hi"}]}`)
	h := make(http.Header)
	h.Set("x-session-id", "from-hdr")
	if got := proxy.SessionKey(h, body); got != "from-body" {
		t.Fatalf("body wins: %q", got)
	}
	bodyLLMS := []byte(`{"llms":{"session_key":"from-llms"},"messages":[{"role":"user","content":"hi"}]}`)
	if got := proxy.SessionKey(nil, bodyLLMS); got != "from-llms" {
		t.Fatalf("llms.session_key: %q", got)
	}
	body2 := []byte(`{"prompt_cache_key":"pck","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`)
	if got := proxy.SessionKey(h, body2); got != "from-hdr" {
		t.Fatalf("hdr: %q", got)
	}
	if got := proxy.SessionKey(nil, body2); got != "pck" {
		t.Fatalf("pck: %q", got)
	}
	body3 := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`)
	k := proxy.SessionKey(nil, body3)
	if k == "" || k[:2] != "h:" {
		t.Fatalf("hash key %q", k)
	}
}

func TestStickyPrefersThenRebinds(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer sk-a" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = jsonEncodeOK(w)
	}))
	defer up.Close()

	sec := memory.New()
	idA, idB := uuid.New(), uuid.New()
	pathA := vendor.InfisicalPath(uuid.New().String(), "deepseek", "a")
	pathB := vendor.InfisicalPath(uuid.New().String(), "deepseek", "b")
	_ = sec.Put(context.Background(), pathA, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-a"}))
	_ = sec.Put(context.Background(), pathB, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-b"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idA, InfisicalPath: pathA, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1, Position: 0},
		{ID: idB, InfisicalPath: pathB, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1, Position: 1},
	}, cool: map[uuid.UUID]time.Time{}}

	aff := proxy.NewAffinity(time.Hour)
	aff.Set("sess-1", idA) // sticky to A which will 429

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector(), Affinity: aff}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.RoundRobin)}
	body := []byte(`{"model":"m","session_id":"sess-1","messages":[{"role":"user","content":"hi"}]}`)
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.AccountID != idB {
		t.Fatalf("status=%d account=%s", res.StatusCode, res.AccountID)
	}
	got, ok := aff.Get("sess-1")
	if !ok || got != idB {
		t.Fatalf("affinity rebind want B got %v ok=%v", got, ok)
	}

	// Next call with cool cleared for A but sticky B should hit B first under sequential prefer.
	st.cool = map[uuid.UUID]time.Time{}
	rt.Strategy = string(router.Sequential)
	// Make A succeed too — sticky B should still be preferred (front).
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = jsonEncodeOK(w)
	}))
	defer up2.Close()
	st.accounts[0].BaseURL = up2.URL + "/v1"
	st.accounts[1].BaseURL = up2.URL + "/v1"
	res2, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.AccountID != idB {
		t.Fatalf("sticky prefer B, got %s", res2.AccountID)
	}
}

func jsonEncodeOK(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	return err
}
