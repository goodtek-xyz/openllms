package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTask080PublicSiteFixes(t *testing.T) {
	s := &Server{}
	r := s.Router()

	// 1) Self-Hosted labels
	for path, want := range map[string]string{
		"/":   "셀프 호스티드</a>",
		"/en": "Self-Hosted</a>",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s missing %q (code %d)", path, want, rec.Code)
		}
	}

	// 2) openllms lang-aware README
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "README.ko.md") {
		t.Fatal("ko home should link openllms README.ko.md")
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/en", nil))
	body := rec.Body.String()
	if strings.Contains(body, "README.ko.md") {
		t.Fatal("en home must not link KO readme")
	}
	if !strings.Contains(body, "github.com/goodtek-xyz/openllms") {
		t.Fatal("en home missing openllms repo link")
	}

	// 3) HTML API docs
	for _, path := range []string{"/api", "/en/api"} {
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s want html 200, got %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(rec.Body.String(), "chat/completions") {
			t.Fatalf("%s missing endpoint docs", path)
		}
	}
	// markdown still available for machines
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/LLMS_API.md", nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "markdown") {
		t.Fatalf("LLMS_API.md should remain markdown, got %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	// 4) shared content band on install
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/install", nil))
	if !strings.Contains(rec.Body.String(), "content-band") {
		t.Fatal("install should use shared content-band")
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/content.css", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "content-band") {
		t.Fatal("content.css missing")
	}

	// 5) billing HTML is retired (404); EN home must not link to it
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/en/billing", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/en/billing want 404 got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/en", nil))
	if strings.Contains(rec.Body.String(), "/billing") {
		t.Fatal("en home must not link to billing")
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/billing", nil)
	req.AddCookie(&http.Cookie{Name: publicLangCookie, Value: "en"})
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/billing want 404 got %d", rec.Code)
	}
}
