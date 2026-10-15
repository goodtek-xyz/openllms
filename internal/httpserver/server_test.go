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

func TestHealth(t *testing.T) {
	s := httpserver.New(&store.Store{}, config.Config{HTTPAddr: ":0"}, nil)
	// /health must not require DB
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	// Mount only health via a tiny shim: call handler through router needs pool for /ready
	// Use dedicated check: build router with nil pool — /health OK, /ready will NPE.
	// So test handler indirectly by creating server with nil and only hitting /health
	// after we extract — simplest: re-get router and only test /health which doesn't use pool.

	mux := s.Router()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("unexpected body: %v", body)
	}
}
