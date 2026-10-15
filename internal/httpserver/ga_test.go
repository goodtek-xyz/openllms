package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/config"
)

func TestPublicGAInjection(t *testing.T) {
	const id = "G-XSL83K1VME"
	on := &Server{cfg: config.Config{GAMeasurementID: id}}
	off := &Server{}

	onR := on.Router()
	offR := off.Router()

	for _, path := range []string{"/", "/en", "/en/install", "/en/api"} {
		body := fetchHTML(t, onR, path)
		if !strings.Contains(body, "googletagmanager.com/gtag/js?id="+id) {
			t.Fatalf("%s missing gtag script src", path)
		}
		if !strings.Contains(body, "gtag('config', '"+id+"')") {
			t.Fatalf("%s missing gtag config", path)
		}
		offBody := fetchHTML(t, offR, path)
		if strings.Contains(offBody, "googletagmanager.com") {
			t.Fatalf("%s should not inject GA when unset", path)
		}
	}

	// Admin pages must stay GA-free even when the ID is set (unit path; /admin is cloud-only).
	adminHTML := `<!DOCTYPE html><html><head><title>admin</title></head><body><!--LLMS_ADMIN_HEADER--></body></html>`
	rec := httptest.NewRecorder()
	serveChromeHTMLBytes(rec, httptest.NewRequest(http.MethodGet, "/admin", nil), adminHTML, "admin", "ko", id)
	if strings.Contains(rec.Body.String(), "googletagmanager.com") {
		t.Fatal("admin chrome must not include gtag")
	}

	// Invalid ID must not inject.
	bad := &Server{cfg: config.Config{GAMeasurementID: "not-a-ga-id"}}
	badBody := fetchHTML(t, bad.Router(), "/")
	if strings.Contains(badBody, "googletagmanager.com") {
		t.Fatal("invalid GA id must not inject")
	}
}

func TestMaybeInjectGA(t *testing.T) {
	in := "<html><head><title>t</title></head><body></body></html>"
	got := maybeInjectGA(in, "G-XSL83K1VME", "home")
	if !strings.Contains(got, "G-XSL83K1VME") || !strings.Contains(got, "</head>") {
		t.Fatalf("inject failed: %s", got)
	}
	if maybeInjectGA(in, "G-XSL83K1VME", "admin") != in {
		t.Fatal("admin must skip")
	}
	if maybeInjectGA(in, "", "home") != in {
		t.Fatal("empty must skip")
	}
}
