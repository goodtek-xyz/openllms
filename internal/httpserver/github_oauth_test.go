package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/config"
)

func TestSanitizeOAuthNext(t *testing.T) {
	cases := map[string]string{
		"":               "/install",
		"/billing":       "/install",
		"/en/billing":    "/install",
		"/admin":         "/admin",
		"/install":       "/install",
		"/login":         "/login",
		"/en/login":      "/en/login",
		"/ko/login":      "/ko/login",
		"https://evil/x": "/install",
		"//evil":         "/install",
		"/console":       "/install",
		"/console/extra": "/install",
	}
	for in, want := range cases {
		if got := sanitizeOAuthNext(in); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestGitHubOAuthStartRequiresSecret(t *testing.T) {
	s := &Server{cfg: config.Config{GitHubClientID: "cid", PublicBaseURL: "https://llms.example"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/github?next=/install", nil)
	s.handleGitHubOAuthStart(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestGitHubOAuthStartRedirects(t *testing.T) {
	s := &Server{cfg: config.Config{
		GitHubClientID:     "cid",
		GitHubClientSecret: "sec",
		PublicBaseURL:      "https://llms.example",
		AdminCookieSecure:  false,
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/github?next=/install", nil)
	s.handleGitHubOAuthStart(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("code %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "github.com") {
		t.Fatalf("location %q", loc)
	}
}
