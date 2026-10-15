package proxy_test

import (
	"context"
	"encoding/json"
	"github.com/goodtek-xyz/openllms/internal/vendorauth"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestCallAccountViaMockUpstream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-up" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-1",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			"usage":   map[string]int{"prompt_tokens": 3, "completion_tokens": 1},
		})
	}))
	defer up.Close()

	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "deepseek", "default")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-up"}))

	acc := &store.Account{
		ID:            uuid.New(),
		Vendor:        "deepseek",
		Name:          "default",
		InfisicalPath: path,
		BaseURL:       up.URL + "/v1",
	}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	res, err := eng.CallAccountForTest(context.Background(), acc, body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d body %s", res.StatusCode, res.Body)
	}
	if res.TokensIn != 3 || res.TokensOut != 1 {
		t.Fatalf("usage %+v", res)
	}
}

func TestStreamPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\"}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer up.Close()

	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "deepseek", "default")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-up"}))
	acc := &store.Account{ID: uuid.New(), InfisicalPath: path, BaseURL: up.URL + "/v1", Vendor: "deepseek"}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.CallAccountForTest(context.Background(), acc, []byte(`{"model":"x","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stream == nil {
		t.Fatal("expected stream")
	}
	defer res.Stream.Close()
	b, _ := io.ReadAll(res.Stream)
	if !strings.Contains(string(b), "[DONE]") {
		t.Fatalf("body %s", b)
	}
}

type memRouteStore struct {
	accounts []store.Account
	cool     map[uuid.UUID]time.Time
	routes   map[string]*store.Route
	byRoute  map[uuid.UUID][]store.Account

	mu     sync.Mutex
	reauth map[uuid.UUID]time.Time
	emails map[uuid.UUID]string
}

func (m *memRouteStore) pool(routeID uuid.UUID) []store.Account {
	src := m.accounts
	if m.byRoute != nil {
		if a, ok := m.byRoute[routeID]; ok {
			src = a
		}
	}
	return src
}

func (m *memRouteStore) ListRouteAccounts(ctx context.Context, routeID uuid.UUID) ([]store.Account, error) {
	_ = ctx
	now := time.Now()
	var out []store.Account
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.pool(routeID) {
		if until, ok := m.cool[a.ID]; ok && until.After(now) {
			continue
		}
		if _, ok := m.reauth[a.ID]; ok {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// MarkAccountReauthRequired mirrors the store's conditional UPDATE: only the first call per incident wins.
func (m *memRouteStore) MarkAccountReauthRequired(ctx context.Context, accountID uuid.UUID) (bool, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reauth[accountID]; ok {
		return false, nil
	}
	if m.reauth == nil {
		m.reauth = map[uuid.UUID]time.Time{}
	}
	m.reauth[accountID] = time.Now()
	return true, nil
}

func (m *memRouteStore) SetAccountEmail(ctx context.Context, accountID uuid.UUID, email string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.emails == nil {
		m.emails = map[uuid.UUID]string{}
	}
	m.emails[accountID] = email
	return nil
}

func (m *memRouteStore) ListRoutePool(ctx context.Context, routeID uuid.UUID) ([]store.Account, error) {
	_ = ctx
	src := m.pool(routeID)
	out := make([]store.Account, len(src))
	copy(out, src)
	return out, nil
}

func (m *memRouteStore) EarliestRouteCooldown(ctx context.Context, routeID uuid.UUID) (time.Time, bool, error) {
	_ = ctx
	now := time.Now()
	var earliest time.Time
	found := false
	for _, a := range m.pool(routeID) {
		until, ok := m.cool[a.ID]
		if !ok || !until.After(now) {
			continue
		}
		if !found || until.Before(earliest) {
			earliest = until
			found = true
		}
	}
	return earliest, found, nil
}

func (m *memRouteStore) SetCooldown(ctx context.Context, accountID uuid.UUID, until time.Time) error {
	_ = ctx
	if m.cool == nil {
		m.cool = map[uuid.UUID]time.Time{}
	}
	m.cool[accountID] = until
	return nil
}

func (m *memRouteStore) MarkAccountHealthy(ctx context.Context, accountID uuid.UUID) error {
	_ = ctx
	if m.cool != nil {
		delete(m.cool, accountID)
	}
	return nil
}

func (m *memRouteStore) GetRouteBySlug(ctx context.Context, projectID uuid.UUID, slug string) (*store.Route, error) {
	_ = ctx
	_ = projectID
	if m.routes == nil {
		return nil, nil
	}
	return m.routes[slug], nil
}

func TestFallbackRouteChain(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer sk-primary" {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fb"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	sec := memory.New()
	idP, idF := uuid.New(), uuid.New()
	pathP := vendor.InfisicalPath(uuid.New().String(), "deepseek", "p")
	pathF := vendor.InfisicalPath(uuid.New().String(), "deepseek", "f")
	_ = sec.Put(context.Background(), pathP, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-primary"}))
	_ = sec.Put(context.Background(), pathF, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-fallback"}))

	primaryID, fallbackID := uuid.New(), uuid.New()
	projectID := uuid.New()
	st := &memRouteStore{
		cool: map[uuid.UUID]time.Time{},
		routes: map[string]*store.Route{
			"backup": {ID: fallbackID, ProjectID: projectID, Slug: "backup", Strategy: "sequential"},
		},
		byRoute: map[uuid.UUID][]store.Account{
			primaryID:  {{ID: idP, InfisicalPath: pathP, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1}},
			fallbackID: {{ID: idF, InfisicalPath: pathF, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1}},
		},
	}
	cfg, _ := json.Marshal(map[string]any{"fallback_slugs": []string{"backup"}})
	rt := &store.Route{ID: primaryID, ProjectID: projectID, Slug: "main", Strategy: "sequential", Config: cfg}
	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client()}
	res, err := eng.ChatCompletions(context.Background(), projectID, rt, []byte(`{"model":"m","messages":[]}`), proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.AccountID != idF {
		t.Fatalf("status=%d account=%s body=%s", res.StatusCode, res.AccountID, res.Body)
	}
}

func TestDispatchFailoverRoundRobin(t *testing.T) {
	hits := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		hits[auth]++
		if auth == "Bearer sk-a" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "ok",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer up.Close()

	sec := memory.New()
	idA, idB := uuid.New(), uuid.New()
	pathA := vendor.InfisicalPath(uuid.New().String(), "deepseek", "a")
	pathB := vendor.InfisicalPath(uuid.New().String(), "deepseek", "b")
	_ = sec.Put(context.Background(), pathA, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-a"}))
	_ = sec.Put(context.Background(), pathB, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-b"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idA, Vendor: "deepseek", Name: "a", InfisicalPath: pathA, BaseURL: up.URL + "/v1", Weight: 1, Position: 0},
		{ID: idB, Vendor: "deepseek", Name: "b", InfisicalPath: pathB, BaseURL: up.URL + "/v1", Weight: 1, Position: 1},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.RoundRobin)}
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)

	// Force first pick to be A by using sequential for deterministic first attempt, then also test RR.
	rt.Strategy = string(router.Sequential)
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d body %s", res.StatusCode, res.Body)
	}
	if res.AccountID != idB {
		t.Fatalf("want failover to B, got %s attempts=%d", res.AccountID, res.Attempts)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts=%d", res.Attempts)
	}
	if _, ok := st.cool[idA]; !ok {
		t.Fatal("expected cooldown on A")
	}
}

func TestDispatchFailoverWeighted(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer sk-heavy" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	sec := memory.New()
	idH, idL := uuid.New(), uuid.New()
	pathH := vendor.InfisicalPath(uuid.New().String(), "deepseek", "h")
	pathL := vendor.InfisicalPath(uuid.New().String(), "deepseek", "l")
	_ = sec.Put(context.Background(), pathH, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-heavy"}))
	_ = sec.Put(context.Background(), pathL, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-light"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idH, InfisicalPath: pathH, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 100, Position: 0},
		{ID: idL, InfisicalPath: pathL, BaseURL: up.URL + "/v1", Vendor: "deepseek", Weight: 1, Position: 1},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Weighted)}
	// Weighted almost always picks heavy first; after 429 must land on light.
	var ok bool
	for i := 0; i < 20; i++ {
		res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, []byte(`{"model":"m","messages":[]}`), proxy.DispatchMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == 200 && res.AccountID == idL && res.Attempts >= 2 {
			ok = true
			break
		}
		// reset cool so pool is full again
		st.cool = map[uuid.UUID]time.Time{}
	}
	if !ok {
		t.Fatal("weighted did not failover to light account")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestClaudeOAuthUsesBearer(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oauth-at" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-api-key") != "" {
			t.Errorf("x-api-key set")
		}
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Errorf("beta %q", r.Header.Get("anthropic-beta"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"m"}`))
	}))
	defer up.Close()
	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "claude", "work")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "oauth-at"}))
	acc := &store.Account{ID: uuid.New(), Vendor: "claude", AuthType: "oauth", InfisicalPath: path, BaseURL: up.URL}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.CallAnthropicAccountForTest(context.Background(), acc, []byte(`{"model":"claude-3","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d body %s", res.StatusCode, res.Body)
	}
}

func TestCodexOAuthSetsAccountHeader(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer codex-at" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("ChatGPT-Account-Id") != "acct-9" {
			t.Errorf("account %q", r.Header.Get("ChatGPT-Account-Id"))
		}
		if r.Header.Get("originator") != "codex_cli_rs" {
			t.Errorf("originator %q", r.Header.Get("originator"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer up.Close()
	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "codex", "a")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "codex-at", ChatGPTAccountID: "acct-9"}))
	acc := &store.Account{ID: uuid.New(), Vendor: "codex", AuthType: "oauth", InfisicalPath: path, BaseURL: up.URL}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.CallAccountForTest(context.Background(), acc, []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d body %s", res.StatusCode, res.Body)
	}
	if !strings.Contains(string(res.Body), `"content":"hi"`) {
		t.Fatalf("body %s", res.Body)
	}
}

func TestImagesGenerationsViaMockUpstream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-up" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("missing tools")
		}
		tool, _ := tools[0].(map[string]any)
		if tool["type"] != "image_generation" {
			t.Fatalf("tool %+v", tool)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"aaa\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))
	}))
	defer up.Close()

	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "openai", "imgs")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-up"}))
	acc := &store.Account{
		ID: uuid.New(), Vendor: "openai", AuthType: "api_key",
		InfisicalPath: path, BaseURL: up.URL + "/v1",
	}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.CallImagesAccountForTest(context.Background(), acc, []byte(`{"model":"gpt-image-1","prompt":"a cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d body %s", res.StatusCode, res.Body)
	}
	if !strings.Contains(string(res.Body), "b64_json") {
		t.Fatalf("body %s", res.Body)
	}
}

func TestImagesRejectsClaudeOAuth(t *testing.T) {
	eng := &proxy.Engine{Secrets: memory.New(), HTTP: http.DefaultClient}
	claude := &store.Account{ID: uuid.New(), Vendor: "claude", AuthType: "oauth"}
	res, err := eng.CallImagesAccountForTest(context.Background(), claude, []byte(`{"prompt":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 || res.Error != "images_unsupported_account" {
		t.Fatalf("claude: %+v", res)
	}
}

func TestImagesCodexOAuthViaResponses(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer codex-at" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("ChatGPT-Account-Id") != "acct-9" {
			t.Errorf("account %q", r.Header.Get("ChatGPT-Account-Id"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("missing tools")
		}
		tool, _ := tools[0].(map[string]any)
		if tool["type"] != "image_generation" {
			t.Fatalf("tool %+v", tool)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"aaaBBB\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))
	}))
	defer up.Close()
	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "codex", "img")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "codex-at", ChatGPTAccountID: "acct-9"}))
	acc := &store.Account{ID: uuid.New(), Vendor: "codex", AuthType: "oauth", InfisicalPath: path, BaseURL: up.URL}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.CallImagesAccountForTest(context.Background(), acc, []byte(`{"model":"gpt-image-1","prompt":"a cat","size":"1024x1024"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d err %s body %s", res.StatusCode, res.Error, res.Body)
	}
	if !strings.Contains(string(res.Body), `"b64_json":"aaaBBB"`) {
		t.Fatalf("body %s", res.Body)
	}
}

func TestClaudeChatCompletionsBridgeToMessages(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":2}}`))
	}))
	defer up.Close()
	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "claude", "work")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-ant-test"}))
	acc := &store.Account{ID: uuid.New(), Vendor: "claude", AuthType: "api_key", InfisicalPath: path, BaseURL: up.URL}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"web_search","description":"s","parameters":{"type":"object"}}}]}`)
	res, err := eng.CallAccountForTest(context.Background(), acc, body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", res.StatusCode, res.Body)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path=%s", gotPath)
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("upstream tools missing: %+v", gotBody)
	}
	var parsed map[string]any
	if json.Unmarshal(res.Body, &parsed) != nil {
		t.Fatal("response not json")
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("object=%v", parsed["object"])
	}
}

func TestOpenAIAPIKeyToolsRoutesToResponses(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer up.Close()
	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "openai", "work")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-up"}))
	acc := &store.Account{ID: uuid.New(), Vendor: "openai", AuthType: "api_key", InfisicalPath: path, BaseURL: up.URL + "/v1"}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"web_search","description":"s","parameters":{"type":"object"}}}]}`)
	res, err := eng.CallAccountForTest(context.Background(), acc, body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s err=%s", res.StatusCode, res.Body, res.Error)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path=%s", gotPath)
	}
	var parsed map[string]any
	if json.Unmarshal(res.Body, &parsed) != nil {
		t.Fatal("bad json")
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("object=%v", parsed["object"])
	}
}

func TestOpenAIAPIKeyNoToolsPassthroughChatCompletions(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "x", "object": "chat.completion",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer up.Close()
	sec := memory.New()
	path := vendor.InfisicalPath(uuid.New().String(), "openai", "plain")
	_ = sec.Put(context.Background(), path, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-up"}))
	acc := &store.Account{ID: uuid.New(), Vendor: "openai", AuthType: "api_key", InfisicalPath: path, BaseURL: up.URL + "/v1"}
	eng := &proxy.Engine{Secrets: sec, HTTP: up.Client()}
	res, err := eng.CallAccountForTest(context.Background(), acc, []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path=%s want passthrough", gotPath)
	}
}

func TestEmptyMembershipVsAllCooldown(t *testing.T) {
	sec := memory.New()
	routeID := uuid.New()
	st := &memRouteStore{cool: map[uuid.UUID]time.Time{}}
	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: http.DefaultClient, Selector: router.NewSelector()}
	rt := &store.Route{ID: routeID, Strategy: string(router.Sequential)}
	body := []byte(`{"model":"m","messages":[]}`)

	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 || res.Error != "no_accounts" {
		t.Fatalf("empty pool: status=%d err=%s body=%s", res.StatusCode, res.Error, res.Body)
	}

	idA := uuid.New()
	st.accounts = []store.Account{{ID: idA, Vendor: "deepseek", Name: "a", Weight: 1}}
	st.cool[idA] = time.Now().Add(45 * time.Second)

	res, err = eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 503 || res.Error != "no_usable_accounts" {
		t.Fatalf("all cooldown: status=%d err=%s body=%s", res.StatusCode, res.Error, res.Body)
	}
	if ra := res.Header.Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After")
	}
	if !strings.Contains(string(res.Body), "no_usable_accounts") {
		t.Fatalf("body=%s", res.Body)
	}
}

func TestFailoverAttemptsRecorded(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer sk-a" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "ok",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer up.Close()

	sec := memory.New()
	idA, idB := uuid.New(), uuid.New()
	pathA := vendor.InfisicalPath(uuid.New().String(), "deepseek", "a")
	pathB := vendor.InfisicalPath(uuid.New().String(), "deepseek", "b")
	_ = sec.Put(context.Background(), pathA, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-a"}))
	_ = sec.Put(context.Background(), pathB, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-b"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idA, Vendor: "deepseek", Name: "a", InfisicalPath: pathA, BaseURL: up.URL + "/v1", Weight: 1, Position: 0},
		{ID: idB, Vendor: "deepseek", Name: "b", InfisicalPath: pathB, BaseURL: up.URL + "/v1", Weight: 1, Position: 1},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Sequential)}
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, []byte(`{"model":"m","messages":[]}`), proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.AccountID != idB {
		t.Fatalf("status=%d account=%s", res.StatusCode, res.AccountID)
	}
	if len(res.Failovers) != 1 || res.Failovers[0].AccountID != idA {
		t.Fatalf("failovers=%+v", res.Failovers)
	}
	if res.Failovers[0].StatusCode != 429 {
		t.Fatalf("failover status=%d", res.Failovers[0].StatusCode)
	}
}

func TestDispatchFailoverOn401UsesNextAccountDefaultModel(t *testing.T) {
	var sawModels []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawModels = append(sawModels, body.Model+"|"+auth)
		if auth == "Bearer sk-codex-dead" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-svcacct","code":"invalid_api_key"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "ok",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer up.Close()

	sec := memory.New()
	idCodex, idClaude := uuid.New(), uuid.New()
	pathCodex := vendor.InfisicalPath(uuid.New().String(), "codex", "dead")
	pathClaude := vendor.InfisicalPath(uuid.New().String(), "claude", "ok")
	_ = sec.Put(context.Background(), pathCodex, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "sk-codex-dead"}))
	_ = sec.Put(context.Background(), pathClaude, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "sk-claude-ok"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idCodex, Vendor: "codex", AuthType: "oauth", Name: "dead", InfisicalPath: pathCodex, BaseURL: up.URL, Weight: 1, Position: 0, DefaultModel: "gpt-5.6-luna"},
		{ID: idClaude, Vendor: "claude", AuthType: "oauth", Name: "ok", InfisicalPath: pathClaude, BaseURL: up.URL, Weight: 1, Position: 1, DefaultModel: "claude-sonnet-4-6"},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Sequential)}
	// Client asks for gpt; dead Codex seat cools down; Claude seat uses its default_model.
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`)
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.AccountID != idClaude {
		t.Fatalf("status=%d account=%s attempts=%d body=%s", res.StatusCode, res.AccountID, res.Attempts, res.Body)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts=%d saw=%v", res.Attempts, sawModels)
	}
	// One 401 without a refresh token is only an observation: the seat cools down; it is flagged for re-link only
	// when it answers 401 again after the cooldown (TestUnauthorizedWithoutRefreshTokenNeedsConfirmation).
	if _, ok := st.cool[idCodex]; !ok {
		t.Fatal("expected cooldown on dead codex seat")
	}
	if _, flagged := st.reauth[idCodex]; flagged {
		t.Fatal("a single 401 must not flag the seat")
	}
	if len(sawModels) < 2 || !strings.Contains(sawModels[1], "claude-sonnet-4-6|Bearer sk-claude-ok") {
		t.Fatalf("expected next seat default model, saw=%v", sawModels)
	}
}


// Reproduces the 2026-09-28 404: Claude seat with empty default_model on a
// codex route (route default gpt-*) must be skipped, not sent gpt-5.6-luna.
func TestDispatchSkipsClaudeSeatWithoutDefaultOnGptRoute(t *testing.T) {
	var sawModels []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawModels = append(sawModels, body.Model+"|"+r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "ok",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer up.Close()

	sec := memory.New()
	idClaude, idCodex := uuid.New(), uuid.New()
	pathClaude := vendor.InfisicalPath(uuid.New().String(), "claude", "nodefault")
	pathCodex := vendor.InfisicalPath(uuid.New().String(), "codex", "ok")
	_ = sec.Put(context.Background(), pathClaude, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "sk-claude"}))
	_ = sec.Put(context.Background(), pathCodex, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "sk-codex"}))

	st := &memRouteStore{accounts: []store.Account{
		{ID: idClaude, Vendor: "claude", AuthType: "oauth", Name: "nodefault", InfisicalPath: pathClaude, BaseURL: up.URL, Weight: 1, Position: 0},
		{ID: idCodex, Vendor: "codex", AuthType: "oauth", Name: "ok", InfisicalPath: pathCodex, BaseURL: up.URL, Weight: 1, Position: 1, DefaultModel: "gpt-5.6-luna"},
	}, cool: map[uuid.UUID]time.Time{}}

	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Sequential), DefaultModel: "gpt-5.6-luna"}
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`)
	res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.AccountID != idCodex || res.Attempts != 1 {
		t.Fatalf("status=%d account=%s attempts=%d saw=%v", res.StatusCode, res.AccountID, res.Attempts, sawModels)
	}
	for _, m := range sawModels {
		if strings.HasSuffix(m, "Bearer sk-claude") {
			t.Fatalf("claude seat without default must not be called, saw=%v", sawModels)
		}
	}
}

// Regression for the 2026-10 incident: an OAuth seat whose refresh token was revoked answered 401 on every request
// and, with only a 5 min cooldown, stayed first in line for days. Now the rejected refresh takes it out of routing
// (no cooldown), so the next request goes straight to the healthy seat.
func TestDispatchRevokedOAuthSeatLeavesRouting(t *testing.T) {
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tok.Close()
	prev, prevHTTP := vendorauth.ClaudeTokenURL, vendorauth.HTTPClient
	vendorauth.ClaudeTokenURL, vendorauth.HTTPClient = tok.URL, tok.Client()
	t.Cleanup(func() { vendorauth.ClaudeTokenURL, vendorauth.HTTPClient = prev, prevHTTP })

	var deadHits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer revoked-at" {
			deadHits.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "ok", "choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer up.Close()

	sec := memory.New()
	idDead, idOK := uuid.New(), uuid.New()
	pathDead := vendor.InfisicalPath(uuid.New().String(), "codex", "revoked")
	pathOK := vendor.InfisicalPath(uuid.New().String(), "deepseek", "ok")
	_ = sec.Put(context.Background(), pathDead, vendor.SecretName, mustJSON(secrets.CredentialJSON{AccessToken: "revoked-at", RefreshToken: "revoked-rt"}))
	_ = sec.Put(context.Background(), pathOK, vendor.SecretName, mustJSON(secrets.CredentialJSON{APIKey: "sk-ok"}))
	st := &memRouteStore{accounts: []store.Account{
		{ID: idDead, Vendor: "claude", AuthType: "oauth", Name: "revoked", InfisicalPath: pathDead, BaseURL: up.URL, Weight: 1, Position: 0},
		{ID: idOK, Vendor: "deepseek", AuthType: "api_key", Name: "ok", InfisicalPath: pathOK, BaseURL: up.URL, Weight: 1, Position: 1},
	}, cool: map[uuid.UUID]time.Time{}}
	eng := &proxy.Engine{Store: st, Secrets: sec, HTTP: up.Client(), Selector: router.NewSelector()}
	rt := &store.Route{ID: uuid.New(), Strategy: string(router.Sequential)}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	for i := 0; i < 3; i++ {
		res, err := eng.ChatCompletions(context.Background(), uuid.New(), rt, body, proxy.DispatchMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 || res.AccountID != idOK {
			t.Fatalf("request %d: status=%d account=%s", i, res.StatusCode, res.AccountID)
		}
	}
	if deadHits.Load() != 1 {
		t.Fatalf("revoked seat tried %d times, want 1 (it must leave routing after the rejected refresh)", deadHits.Load())
	}
	if _, cooled := st.cool[idDead]; cooled {
		t.Fatal("a reauth seat needs no cooldown")
	}
	if _, ok := st.reauth[idDead]; !ok {
		t.Fatal("revoked seat not flagged for re-link")
	}
}
