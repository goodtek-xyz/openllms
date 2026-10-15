package httpserver

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/goodtek-xyz/openllms/internal/billing"
)

const publicLangCookie = "llms_lang"

// negotiatePublicLang picks ko|en|ja|zh: explicit cookie (lang menu) wins, else Accept-Language, else ko.
func negotiatePublicLang(r *http.Request) string {
	if c, err := r.Cookie(publicLangCookie); err == nil {
		if lang := normalizePublicLang(c.Value); lang != "" {
			return lang
		}
	}
	return langFromAccept(r.Header.Get("Accept-Language"))
}

func langFromAccept(header string) string {
	if strings.TrimSpace(header) == "" {
		return "ko"
	}
	bestLang := ""
	bestQ := -1.0
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		q := 1.0
		code := part
		if i := strings.Index(part, ";"); i >= 0 {
			code = strings.TrimSpace(part[:i])
			for _, param := range strings.Split(part[i+1:], ";") {
				param = strings.TrimSpace(param)
				if strings.HasPrefix(param, "q=") {
					if v, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64); err == nil {
						q = v
					}
				}
			}
		}
		mapped := mapAcceptTag(code)
		if mapped == "" {
			continue
		}
		if q > bestQ {
			bestQ = q
			bestLang = mapped
		}
	}
	if bestLang == "" {
		return "ko"
	}
	return bestLang
}

func mapAcceptTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" || tag == "*" {
		return ""
	}
	primary := tag
	if i := strings.Index(tag, "-"); i >= 0 {
		primary = tag[:i]
	}
	switch primary {
	case "ko":
		return "ko"
	case "en":
		return "en"
	case "ja":
		return "ja"
	case "zh":
		return "zh"
	default:
		return ""
	}
}

func setPublicLangCookie(w http.ResponseWriter, lang string) {
	http.SetCookie(w, &http.Cookie{
		Name:     publicLangCookie,
		Value:    lang,
		Path:     "/",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		HttpOnly: false, // readable if we ever mirror in JS; not a secret
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})
}

func landingPathForLang(lang string) string {
	if lang == "ko" {
		return "/"
	}
	return "/" + lang
}

func installPathForLang(lang string) string {
	if lang == "ko" {
		return "/install"
	}
	return "/" + lang + "/install"
}

func billingPathForLang(lang string) string {
	if lang == "ko" {
		return "/billing"
	}
	return "/" + lang + "/billing"
}

func apiDocsPathForLang(lang string) string {
	if lang == "ko" {
		return "/api"
	}
	return "/" + lang + "/api"
}

func loginPathForLang(lang string) string {
	if lang == "ko" {
		return "/login"
	}
	return "/" + lang + "/login"
}

func openllmsHrefForLang(lang string) string {
	base := "https://github.com/goodtek-xyz/openllms"
	switch lang {
	case "ko":
		return base + "/blob/main/README.ko.md"
	case "ja":
		return base + "/blob/main/README.ja.md"
	case "zh":
		return base + "/blob/main/README.zh.md"
	default:
		return base
	}
}

func (s *Server) handlePublicLanding(w http.ResponseWriter, r *http.Request) {
	lang := negotiatePublicLang(r)
	if lang != "ko" {
		http.Redirect(w, r, landingPathForLang(lang), http.StatusFound)
		return
	}
	setPublicLangCookie(w, "ko")
	s.servePublicHTML(w, r, "static/i18n/ko/index.html", "home", "ko")
}

func (s *Server) handlePublicLandingLang(w http.ResponseWriter, r *http.Request) {
	lang := normalizePublicLang(chi.URLParam(r, "lang"))
	if lang == "" {
		http.NotFound(w, r)
		return
	}
	if lang == "ko" {
		setPublicLangCookie(w, "ko")
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	setPublicLangCookie(w, lang)
	s.servePublicHTML(w, r, "static/i18n/"+lang+"/index.html", "home", lang)
}

func (s *Server) handlePublicInstallHTML(w http.ResponseWriter, r *http.Request) {
	lang := negotiatePublicLang(r)
	if lang != "ko" {
		http.Redirect(w, r, installPathForLang(lang), http.StatusFound)
		return
	}
	setPublicLangCookie(w, "ko")
	s.servePublicHTML(w, r, "static/i18n/ko/install.html", "install", "ko")
}

func (s *Server) handlePublicInstallHTMLLang(w http.ResponseWriter, r *http.Request) {
	lang := normalizePublicLang(chi.URLParam(r, "lang"))
	if lang == "" {
		http.NotFound(w, r)
		return
	}
	if lang == "ko" {
		setPublicLangCookie(w, "ko")
		http.Redirect(w, r, "/install", http.StatusFound)
		return
	}
	setPublicLangCookie(w, lang)
	s.servePublicHTML(w, r, "static/i18n/"+lang+"/install.html", "install", lang)
}

func (s *Server) handlePublicBillingHTML(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func (s *Server) handlePublicBillingHTMLLang(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func (s *Server) handlePublicAPIDocsHTML(w http.ResponseWriter, r *http.Request) {
	lang := negotiatePublicLang(r)
	if lang != "ko" {
		http.Redirect(w, r, apiDocsPathForLang(lang), http.StatusFound)
		return
	}
	setPublicLangCookie(w, "ko")
	s.servePublicHTML(w, r, "static/i18n/ko/api.html", "api", "ko")
}

func (s *Server) handlePublicAPIDocsHTMLLang(w http.ResponseWriter, r *http.Request) {
	lang := normalizePublicLang(chi.URLParam(r, "lang"))
	if lang == "" {
		http.NotFound(w, r)
		return
	}
	if lang == "ko" {
		setPublicLangCookie(w, "ko")
		http.Redirect(w, r, "/api", http.StatusFound)
		return
	}
	setPublicLangCookie(w, lang)
	s.servePublicHTML(w, r, "static/i18n/"+lang+"/api.html", "api", lang)
}

func (s *Server) handlePublicLoginHTML(w http.ResponseWriter, r *http.Request) {
	if billing.SoftOpen {
		http.Redirect(w, r, installPathForLang(negotiatePublicLang(r)), http.StatusFound)
		return
	}
	lang := negotiatePublicLang(r)
	if lang != "ko" {
		http.Redirect(w, r, loginPathForLang(lang), http.StatusFound)
		return
	}
	setPublicLangCookie(w, "ko")
	serveLoginHTML(w, r, "ko")
}

func (s *Server) handlePublicLoginHTMLLang(w http.ResponseWriter, r *http.Request) {
	lang := normalizePublicLang(chi.URLParam(r, "lang"))
	if lang == "" {
		http.NotFound(w, r)
		return
	}
	if billing.SoftOpen {
		http.Redirect(w, r, installPathForLang(lang), http.StatusFound)
		return
	}
	if lang == "ko" {
		setPublicLangCookie(w, "ko")
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	setPublicLangCookie(w, lang)
	serveLoginHTML(w, r, lang)
}
