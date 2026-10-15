package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/config"
)

func TestPublicDistDisabled(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	r := s.Router()
	req := httptest.NewRequest(http.MethodGet, "/dist/llms_linux_arm64", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", rec.Code)
	}
}

func TestPublicDistServesFile(t *testing.T) {
	dir := t.TempDir()
	payload := strings.Repeat("x", 120000)
	if err := os.WriteFile(filepath.Join(dir, "llms_linux_arm64"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("v0.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "COMMIT"), []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: config.Config{
		DistDir:       dir,
		PublicBaseURL: "https://llms.goodtek.xyz",
	}}
	r := s.Router()

	req := httptest.NewRequest(http.MethodGet, "/dist/llms_linux_arm64", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.Len() != len(payload) {
		t.Fatalf("body len %d", rec.Body.Len())
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "llms_linux_arm64") {
		t.Fatalf("disposition %q", rec.Header().Get("Content-Disposition"))
	}

	req = httptest.NewRequest(http.MethodGet, "/dist/../llms_linux_arm64", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal want 404 got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/dist", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/dist/llms_linux_arm64") {
		t.Fatalf("index %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/control/v1/meta", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`"cli_dist"`, `"cli_dist_ready":true`, `"cli_version":"v0.1.1"`, `"cli_commit":"abc123"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("meta missing %s: %s", want, body)
		}
	}
}

func TestInstallShPrefersDist(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "LLMS_DIST_BASE") || !strings.Contains(body, "/dist/") {
		t.Fatalf("install.sh should prefer gateway dist: %s", body[:min(200, len(body))])
	}
}
