package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/config"
	"github.com/goodtek-xyz/openllms/internal/db"
	"github.com/goodtek-xyz/openllms/internal/httpserver"
	"github.com/goodtek-xyz/openllms/internal/secrets/file"
	"github.com/goodtek-xyz/openllms/internal/store"
)

// TestOSSE2E_SQLiteFileSecrets_MockUpstream exercises the OSS happy path:
// SQLite + file vault → bootstrap → account → route → attach → chat completions.
func TestOSSE2E_SQLiteFileSecrets_MockUpstream(t *testing.T) {
	dir := t.TempDir()
	dbURL := "sqlite:" + filepath.Join(dir, "llms.db")
	if err := db.Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.ConnectSQLite(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	sec, err := file.New(filepath.Join(dir, "secrets"))
	if err != nil {
		t.Fatalf("file secrets: %v", err)
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-upstream" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad auth"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-oss",
			"choices": []any{
				map[string]any{"message": map[string]string{"role": "assistant", "content": "oss-ok"}},
			},
			"usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 1},
		})
	}))
	t.Cleanup(up.Close)

	st := store.NewSQLite(sqlDB)
	cfg := config.Config{BootstrapToken: "oss-bootstrap"}
	srv := httpserver.New(st, cfg, sec)
	gw := httptest.NewServer(srv.Router())
	t.Cleanup(gw.Close)

	var boot map[string]any
	status := doJSON(t, http.MethodPost, gw.URL+"/control/v1/bootstrap", "oss-bootstrap", "", map[string]string{
		"login": "oss", "project_name": "default", "key_name": "cli",
	}, &boot)
	if status != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%v", status, boot)
	}
	apiKey, _ := boot["api_key"].(string)
	if apiKey == "" {
		t.Fatalf("missing api_key: %v", boot)
	}
	auth := "Bearer " + apiKey

	var acc map[string]any
	status = doJSON(t, http.MethodPost, gw.URL+"/control/v1/accounts", "", auth, map[string]any{
		"vendor": "openai", "name": "mock", "api_key": "sk-upstream", "base_url": up.URL + "/v1",
	}, &acc)
	if status != http.StatusCreated {
		t.Fatalf("account status=%d body=%v", status, acc)
	}
	accountID, _ := acc["id"].(string)
	if accountID == "" {
		t.Fatalf("missing account id: %v", acc)
	}

	var route map[string]any
	status = doJSON(t, http.MethodPost, gw.URL+"/control/v1/routes", "", auth, map[string]any{
		"slug": "default", "strategy": "sequential", "default_model": "gpt-4o-mini",
	}, &route)
	if status != http.StatusCreated {
		t.Fatalf("route status=%d body=%v", status, route)
	}

	var attach map[string]any
	status = doJSON(t, http.MethodPost, gw.URL+"/control/v1/routes/default/accounts", "", auth, map[string]any{
		"account_id": accountID, "position": 0, "weight": 1,
	}, &attach)
	if status != http.StatusOK {
		t.Fatalf("attach status=%d body=%v", status, attach)
	}

	var patched map[string]any
	status = doJSON(t, http.MethodPatch, gw.URL+"/control/v1/routes/default", "", auth, map[string]any{
		"preset": "balance", "account_ids": []string{accountID},
	}, &patched)
	if status != http.StatusOK {
		t.Fatalf("patch route status=%d body=%v", status, patched)
	}
	if patched["preset"] != "balance" {
		t.Fatalf("preset want balance got %v", patched["preset"])
	}

	var listed map[string]any
	status = doJSON(t, http.MethodGet, gw.URL+"/control/v1/routes", "", auth, nil, &listed)
	if status != http.StatusOK {
		t.Fatalf("list routes status=%d body=%v", status, listed)
	}

	chatReq := map[string]any{
		"model":    "gpt-4o-mini",
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
	raw, _ := json.Marshal(chatReq)
	req, err := http.NewRequest(http.MethodPost, gw.URL+"/r/default/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("oss-ok")) {
		t.Fatalf("unexpected chat body: %s", body)
	}

	var deleted map[string]any
	status = doJSON(t, http.MethodDelete, gw.URL+"/control/v1/routes/default", "", auth, nil, &deleted)
	if status != http.StatusOK {
		t.Fatalf("delete route status=%d body=%v", status, deleted)
	}
}

func doJSON(t *testing.T, method, url, bootstrapToken, auth string, in any, out *map[string]any) int {
	t.Helper()
	raw, _ := json.Marshal(in)
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bootstrapToken != "" {
		req.Header.Set("X-Bootstrap-Token", bootstrapToken)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("json decode %s: %v body=%s", url, err, body)
		}
	}
	return resp.StatusCode
}
