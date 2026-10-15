package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/userauth"
)

func TestConsolePageRedirectsToInstall(t *testing.T) {
	s := &Server{user: &userauth.Manager{}}
	req := httptest.NewRequest(http.MethodGet, "/console", nil)
	rec := httptest.NewRecorder()
	s.handleConsolePage(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("console page: want 301 got %d %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/install" {
		t.Fatalf("console redirect Location=%q want /install", loc)
	}
}

func TestConsoleOverviewUnauthorized(t *testing.T) {
	s := &Server{
		user:  &userauth.Manager{},
		admin: disabledAdmin{},
	}
	req := httptest.NewRequest(http.MethodGet, "/console/api/overview", nil)
	rec := httptest.NewRecorder()
	s.handleConsoleOverview(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", rec.Code)
	}
}

func TestConsolePageViaRouter(t *testing.T) {
	s := &Server{}
	r := s.Router()
	req := httptest.NewRequest(http.MethodGet, "/console", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently || !strings.HasSuffix(rec.Header().Get("Location"), "/install") {
		t.Fatalf("router /console: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}
