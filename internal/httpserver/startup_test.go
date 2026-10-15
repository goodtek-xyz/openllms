package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/config"
	"github.com/goodtek-xyz/openllms/internal/httpserver"
	"github.com/goodtek-xyz/openllms/internal/store"
)

func serve(t *testing.T, h http.Handler, method, path string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	var body map[string]any
	if method != http.MethodHead {
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, rr.Body.String(), err)
		}
	}
	return rr.Code, body
}

func TestStartupWhileStarting(t *testing.T) {
	s := httpserver.NewStartup()
	cases := []struct {
		method, path string
		code         int
		key, want    string
	}{
		{http.MethodGet, "/health", http.StatusOK, "status", "ok"},
		{http.MethodGet, "/livez", http.StatusOK, "status", "ok"},
		{http.MethodGet, "/readyz", http.StatusServiceUnavailable, "status", "starting"},
		{http.MethodGet, "/ready", http.StatusServiceUnavailable, "error", "starting"},
		{http.MethodGet, "/v1/models", http.StatusServiceUnavailable, "error", "starting"},
		{http.MethodPost, "/v1/chat/completions", http.StatusServiceUnavailable, "error", "starting"},
		{http.MethodPost, "/health", http.StatusServiceUnavailable, "error", "starting"},
	}
	for _, c := range cases {
		code, body := serve(t, s, c.method, c.path)
		if code != c.code || body[c.key] != c.want {
			t.Errorf("%s %s = %d %v, want %d %s=%s", c.method, c.path, code, body, c.code, c.key, c.want)
		}
	}
	if code, _ := serve(t, s, http.MethodHead, "/health"); code != http.StatusOK {
		t.Errorf("HEAD /health = %d, want 200", code)
	}
}

func TestStartupHandsOverToRouter(t *testing.T) {
	s := httpserver.NewStartup()
	srv := httpserver.New(&store.Store{}, config.Config{HTTPAddr: ":0"}, nil)
	s.Started(srv.Router())

	if code, body := serve(t, s, http.MethodGet, "/readyz"); code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("/readyz after start-up = %d %v, want 200 ready", code, body)
	}
	if code, body := serve(t, s, http.MethodGet, "/livez"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("/livez after start-up = %d %v, want 200 ok", code, body)
	}
	if code, body := serve(t, s, http.MethodGet, "/health"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("/health after start-up = %d %v, want 200 ok", code, body)
	}
	if code, body := serve(t, s, http.MethodGet, "/control/v1/meta"); code != http.StatusOK || body["error"] == "starting" {
		t.Fatalf("/control/v1/meta after start-up = %d %v, want the real handler", code, body)
	}
}
