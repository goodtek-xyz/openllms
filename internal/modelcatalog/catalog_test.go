package modelcatalog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/modelcatalog"
	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

func TestOpenAIListNeverUsesAccountRefs(t *testing.T) {
	sec := memory.New()
	path := "/llms/p/accounts/deepseek/a"
	_ = sec.Put(context.Background(), path, "credential", `{"api_key":"sk"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "deepseek-chat", "owned_by": "deepseek"},
				{"id": "codex:should-skip", "owned_by": "x"},
			},
		})
	}))
	defer srv.Close()

	d := modelcatalog.New(sec, srv.Client(), time.Minute)
	acc := store.Account{
		ID: uuid.New(), Vendor: "deepseek", Name: "work", AuthType: "api_key",
		InfisicalPath: path, BaseURL: srv.URL + "/v1",
	}
	rt := &store.Route{Slug: "demo", Strategy: "sequential"}
	cat := d.ForRoute(context.Background(), rt, []store.Account{acc})
	list := modelcatalog.OpenAIList(cat)
	data := list["data"].([]map[string]any)
	if len(data) != 1 || data[0]["id"] != "deepseek-chat" {
		t.Fatalf("data=%v", data)
	}
	if cat.SuggestedModel != "deepseek-chat" {
		t.Fatalf("suggested=%q", cat.SuggestedModel)
	}
}

func TestCodexOAuthFetchesLiveModels(t *testing.T) {
	sec := memory.New()
	path := "/llms/p/accounts/codex/a"
	_ = sec.Put(context.Background(), path, "credential", `{"access_token":"tok","chatgpt_account_id":"acc-9"}`)

	var sawQuery, sawAuth, sawAcct, sawOriginator bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		sawQuery = r.URL.Query().Get("client_version") == "0.144.1"
		sawAuth = strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
		sawAcct = r.Header.Get("ChatGPT-Account-Id") == "acc-9"
		sawOriginator = r.Header.Get("originator") == "codex_cli_rs"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"slug": "gpt-5.5", "supported_in_api": true, "visibility": "list"},
				{"slug": "gpt-5.4-mini", "supported_in_api": true, "visibility": "list"},
				{"slug": "hidden-lab", "supported_in_api": true, "visibility": "hidden"},
				{"slug": "not-for-api", "supported_in_api": false, "visibility": "list"},
				{"slug": "codex:skip-me", "supported_in_api": true, "visibility": "list"},
			},
		})
	}))
	defer srv.Close()

	d := modelcatalog.New(sec, srv.Client(), time.Minute)
	acc := store.Account{
		ID: uuid.New(), Vendor: "codex", Name: "codex-goodtek", AuthType: "oauth",
		InfisicalPath: path, BaseURL: srv.URL,
	}
	rt := &store.Route{Slug: "codex-quota-first", Strategy: "quota_aware"}
	cat := d.ForRoute(context.Background(), rt, []store.Account{acc})
	if !sawQuery || !sawAuth || !sawAcct || !sawOriginator {
		t.Fatalf("request headers/query incomplete query=%v auth=%v acct=%v originator=%v", sawQuery, sawAuth, sawAcct, sawOriginator)
	}
	if len(cat.Accounts) != 1 || cat.Accounts[0].Status != "ok" {
		t.Fatalf("account=%+v", cat.Accounts)
	}
	if cat.Accounts[0].Error != "" {
		t.Fatalf("unexpected error note: %q", cat.Accounts[0].Error)
	}
	if cat.Accounts[0].ID != "codex:codex-goodtek" {
		t.Fatalf("account id should remain ref: %s", cat.Accounts[0].ID)
	}
	list := modelcatalog.OpenAIList(cat)
	data := list["data"].([]map[string]any)
	ids := map[string]bool{}
	for _, row := range data {
		id, _ := row["id"].(string)
		ids[id] = true
	}
	if !ids["gpt-5.5"] || !ids["gpt-5.4-mini"] {
		t.Fatalf("expected live slugs, got %v", data)
	}
	if ids["hidden-lab"] || ids["not-for-api"] || ids["codex:skip-me"] {
		t.Fatalf("filtered models leaked: %v", data)
	}
	if cat.SuggestedModel != "gpt-5.5" {
		t.Fatalf("suggested=%q", cat.SuggestedModel)
	}
}

func TestCodexOAuthFallsBackWhenUpstreamFails(t *testing.T) {
	sec := memory.New()
	path := "/llms/p/accounts/codex/a"
	_ = sec.Put(context.Background(), path, "credential", `{"access_token":"tok","chatgpt_account_id":"acc"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	d := modelcatalog.New(sec, srv.Client(), time.Minute)
	acc := store.Account{
		ID: uuid.New(), Vendor: "codex", Name: "codex-goodtek", AuthType: "oauth",
		InfisicalPath: path, BaseURL: srv.URL,
	}
	rt := &store.Route{Slug: "r", Strategy: "sequential"}
	cat := d.ForRoute(context.Background(), rt, []store.Account{acc})
	if cat.Accounts[0].Status != "ok" {
		t.Fatalf("status=%s err=%s", cat.Accounts[0].Status, cat.Accounts[0].Error)
	}
	if cat.Accounts[0].Error != "upstream_unavailable_using_known_list:upstream_401" {
		t.Fatalf("error note=%q", cat.Accounts[0].Error)
	}
	if cat.SuggestedModel != "gpt-5.5" {
		t.Fatalf("suggested=%q", cat.SuggestedModel)
	}
}

func TestCodexOAuthRefreshesOn401ThenSucceeds(t *testing.T) {
	sec := memory.New()
	path := "/llms/p/accounts/codex/a"
	_ = sec.Put(context.Background(), path, "credential", `{"access_token":"old","refresh_token":"rt","chatgpt_account_id":"acc-9"}`)

	var modelsHits int
	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		modelsHits++
		auth := r.Header.Get("Authorization")
		if auth != "Bearer fresh" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"slug": "gpt-5.5-live", "supported_in_api": true, "visibility": "list"},
			},
		})
	}))
	defer modelsSrv.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh", "refresh_token": "rt2", "expires_in": 3600,
		})
	}))
	defer tokenSrv.Close()
	prev := vendorauth.CodexTokenURL
	vendorauth.CodexTokenURL = tokenSrv.URL
	t.Cleanup(func() { vendorauth.CodexTokenURL = prev })

	d := modelcatalog.New(sec, modelsSrv.Client(), time.Minute)
	acc := store.Account{
		ID: uuid.New(), Vendor: "codex", Name: "codex-goodtek", AuthType: "oauth",
		InfisicalPath: path, BaseURL: modelsSrv.URL,
	}
	rt := &store.Route{Slug: "r", Strategy: "sequential"}
	cat := d.ForRoute(context.Background(), rt, []store.Account{acc})
	if cat.Accounts[0].Error != "" {
		t.Fatalf("error=%q", cat.Accounts[0].Error)
	}
	if cat.SuggestedModel != "gpt-5.5-live" {
		t.Fatalf("suggested=%q hits=%d", cat.SuggestedModel, modelsHits)
	}
	if modelsHits < 2 {
		t.Fatalf("expected retry after refresh, hits=%d", modelsHits)
	}
}

func TestParseCodexModelsJSON(t *testing.T) {
	raw := []byte(`{"models":[{"slug":"gpt-5.5","supported_in_api":true,"visibility":"list"}]}`)
	models, err := modelcatalog.ParseCodexModelsJSONForTest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.5" {
		t.Fatalf("%+v", models)
	}
}

func TestDiscoveryErrorNoAccountAsModel(t *testing.T) {
	sec := memory.New()
	// no secret put → error, no fallback for deepseek
	d := modelcatalog.New(sec, http.DefaultClient, time.Minute)
	acc := store.Account{
		ID: uuid.New(), Vendor: "deepseek", Name: "x", AuthType: "api_key",
		InfisicalPath: "/missing", BaseURL: "http://127.0.0.1:1",
	}
	rt := &store.Route{Slug: "r", Strategy: "sequential"}
	cat := d.ForRoute(context.Background(), rt, []store.Account{acc})
	if cat.Accounts[0].Status != "error" {
		t.Fatalf("status=%s", cat.Accounts[0].Status)
	}
	list := modelcatalog.OpenAIList(cat)
	data := list["data"].([]map[string]any)
	if len(data) != 0 {
		t.Fatalf("expected empty openai list, got %v", data)
	}
}
