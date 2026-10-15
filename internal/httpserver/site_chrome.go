package httpserver

import (
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strings"

	"github.com/goodtek-xyz/openllms/internal/billing"
)

const (
	siteHeaderPartial = "static/partials/site-header.html"
	siteFooterPartial = "static/partials/site-footer.html"

	// Markers — one chrome concept; aliases kept for existing HTML bodies.
	markerSiteHeader  = "<!--LLMS_SITE_HEADER-->"
	markerSiteFooter  = "<!--LLMS_SITE_FOOTER-->"
	markerPubHeader   = "<!--LLMS_PUBLIC_HEADER-->"
	markerPubFooter   = "<!--LLMS_PUBLIC_FOOTER-->"
	markerAppHeader   = "<!--LLMS_APP_HEADER-->"
	markerAppFooter   = "<!--LLMS_APP_FOOTER-->"
	markerAdminHeader = "<!--LLMS_ADMIN_HEADER-->"
)

type siteChromeLocale struct {
	HomeLabel, APILabel, InstallLabel, SelfHostLabel, BillingLabel string
	APIDocsLabel                                                   string
	HomeHref, APIHref, InstallHref, SelfHostHref                   string
	BillingHref, APIDocsHref, OpenllmsHref                         string
	NavAria                                                        string
	ThemeSystem, ThemeLight, ThemeDark                             string
	LangSummary, LangAria                                          string
	SettingsLabel, AppearanceLabel, LanguageLabel                  string
	LoginLabel                                                     string
	Tagline                                                        string
	ProductAria, ProductTitle                                      string
	AppsAria, AppsTitle                                            string
	LegalAria, LegalTitle                                          string
	GTHome, GTAbout, GTContact                                     string
	PrivacyLabel, DisclaimerLabel                                  string
	LogoLabel                                                      string
	LogoHref                                                       string
}

func siteChromeLocaleFor(lang string) siteChromeLocale {
	switch lang {
	case "en":
		return siteChromeLocale{
			HomeLabel: "Home", APILabel: "API", InstallLabel: "Get started",
			SelfHostLabel: "Self-Hosted", BillingLabel: "Cloud", APIDocsLabel: "API docs",
			HomeHref: "/en", APIHref: "/en#api", InstallHref: "/en/install", SelfHostHref: "/en#self-host",
			NavAria: "Main", ThemeSystem: "Theme: System", ThemeLight: "Theme: Light",
			ThemeDark: "Theme: Dark", LangSummary: "English", LangAria: "Language",
			SettingsLabel: "Settings", AppearanceLabel: "Appearance", LanguageLabel: "Language",
			LoginLabel:  "Log in",
			Tagline:     "Turn ChatGPT & Claude subs into one API. Many accounts, quota managed.",
			ProductAria: "Product", ProductTitle: "Product",
			AppsAria: "Apps", AppsTitle: "Apps",
			LegalAria: "Legal", LegalTitle: "Legal",
			GTHome: "Home", GTAbout: "About", GTContact: "Contact",
			PrivacyLabel: "Privacy", DisclaimerLabel: "Disclaimer",
			LogoLabel: "llms", LogoHref: "/en",
		}
	case "ja":
		return siteChromeLocale{
			HomeLabel: "ホーム", APILabel: "API", InstallLabel: "はじめる",
			SelfHostLabel: "セルフホステッド", BillingLabel: "クラウド", APIDocsLabel: "API ドキュメント",
			HomeHref: "/ja", APIHref: "/ja#api", InstallHref: "/ja/install", SelfHostHref: "/ja#self-host",
			NavAria: "メイン", ThemeSystem: "テーマ: システム", ThemeLight: "テーマ: ライト",
			ThemeDark: "テーマ: ダーク", LangSummary: "日本語", LangAria: "言語",
			SettingsLabel: "設定", AppearanceLabel: "表示", LanguageLabel: "言語",
			LoginLabel:  "ログイン",
			Tagline:     "サブスクを API に。複数アカウントも URL ひとつ、残り枠は自動。",
			ProductAria: "製品", ProductTitle: "製品",
			AppsAria: "アプリ", AppsTitle: "アプリ",
			LegalAria: "法的情報", LegalTitle: "法的情報",
			GTHome: "ホーム", GTAbout: "About", GTContact: "お問い合わせ",
			PrivacyLabel: "プライバシー", DisclaimerLabel: "免責事項",
			LogoLabel: "llms", LogoHref: "/ja",
		}
	case "zh":
		return siteChromeLocale{
			HomeLabel: "首页", APILabel: "API", InstallLabel: "开始使用",
			SelfHostLabel: "自托管", BillingLabel: "云端", APIDocsLabel: "API 文档",
			HomeHref: "/zh", APIHref: "/zh#api", InstallHref: "/zh/install", SelfHostHref: "/zh#self-host",
			NavAria: "主导航", ThemeSystem: "主题: 系统", ThemeLight: "主题: 浅色",
			ThemeDark: "主题: 深色", LangSummary: "中文", LangAria: "语言",
			SettingsLabel: "设置", AppearanceLabel: "外观", LanguageLabel: "语言",
			LoginLabel:  "登录",
			Tagline:     "用订阅做 API。多账号一个地址，剩余额度自动管理。",
			ProductAria: "产品", ProductTitle: "产品",
			AppsAria: "应用", AppsTitle: "应用",
			LegalAria: "法律", LegalTitle: "法律",
			GTHome: "首页", GTAbout: "关于", GTContact: "联系",
			PrivacyLabel: "隐私政策", DisclaimerLabel: "免责声明",
			LogoLabel: "llms", LogoHref: "/zh",
		}
	default:
		return siteChromeLocale{
			HomeLabel: "홈", APILabel: "API", InstallLabel: "시작하기",
			SelfHostLabel: "셀프 호스티드", BillingLabel: "클라우드", APIDocsLabel: "API 문서",
			HomeHref: "/", APIHref: "/#api", InstallHref: "/ko/install", SelfHostHref: "/#self-host",
			NavAria: "메인", ThemeSystem: "테마: 시스템", ThemeLight: "테마: 라이트",
			ThemeDark: "테마: 다크", LangSummary: "한국어", LangAria: "언어",
			SettingsLabel: "설정", AppearanceLabel: "화면", LanguageLabel: "언어",
			LoginLabel:  "로그인",
			Tagline:     "이미 내고있는 구독비용으로 API. 여러 계정은 하나로, 잔여량은 알아서.",
			ProductAria: "제품", ProductTitle: "제품",
			AppsAria: "앱", AppsTitle: "앱",
			LegalAria: "법적 고지", LegalTitle: "법적 고지",
			GTHome: "홈", GTAbout: "소개", GTContact: "문의",
			PrivacyLabel: "개인정보처리방침", DisclaimerLabel: "면책 조항",
			LogoLabel: "llms", LogoHref: "/",
		}
	}
}

func serveChromeHTML(w http.ResponseWriter, r *http.Request, bodyFile, activeNav, lang, gaID string) {
	body, err := fs.ReadFile(publicStatic, bodyFile)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	headerTpl, err := fs.ReadFile(publicStatic, siteHeaderPartial)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	footerTpl, err := fs.ReadFile(publicStatic, siteFooterPartial)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	loc := siteChromeLocaleFor(lang)
	loc.BillingHref = billingPathForLang(lang)
	loc.APIDocsHref = apiDocsPathForLang(lang)
	loc.OpenllmsHref = openllmsHrefForLang(lang)
	if activeNav == "admin" {
		loc.LogoLabel = "llms admin"
		loc.LogoHref = "/admin"
	}
	header := buildSiteHeader(string(headerTpl), activeNav, lang, loc)
	footer := buildSiteFooter(string(footerTpl), activeNav, lang, loc)

	out := string(body)
	out = replaceFirstAny(out, header, markerSiteHeader, markerPubHeader, markerAppHeader, markerAdminHeader)
	out = replaceFirstAny(out, footer, markerSiteFooter, markerPubFooter, markerAppFooter)
	out = applyChromePageTokens(out, loc)
	out = maybeInjectGA(out, gaID, activeNav)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(out))
}

func replaceFirstAny(haystack, replacement string, markers ...string) string {
	for _, m := range markers {
		if strings.Contains(haystack, m) {
			return strings.Replace(haystack, m, replacement, 1)
		}
	}
	return haystack
}

func buildSiteHeader(header, activeNav, lang string, loc siteChromeLocale) string {
	activeAttr := "aria-current=\"page\""
	h := strings.ReplaceAll(header, "{{ACTIVE_HOME}}", attrIf(activeNav == "home", activeAttr))
	h = strings.ReplaceAll(h, "{{ACTIVE_API}}", attrIf(activeNav == "api", activeAttr))
	h = strings.ReplaceAll(h, "{{ACTIVE_INSTALL}}", attrIf(activeNav == "install", activeAttr))
	h = strings.ReplaceAll(h, "{{ACTIVE_SELFHOST}}", attrIf(activeNav == "selfhost", activeAttr))
	h = strings.ReplaceAll(h, "{{ACTIVE_BILLING}}", attrIf(activeNav == "billing", activeAttr))
	h = strings.ReplaceAll(h, "{{HOME_LABEL}}", loc.HomeLabel)
	h = strings.ReplaceAll(h, "{{API_LABEL}}", loc.APILabel)
	h = strings.ReplaceAll(h, "{{INSTALL_LABEL}}", loc.InstallLabel)
	h = strings.ReplaceAll(h, "{{SELFHOST_LABEL}}", loc.SelfHostLabel)
	h = strings.ReplaceAll(h, "{{BILLING_LABEL}}", loc.BillingLabel)
	h = strings.ReplaceAll(h, "{{HOME_HREF}}", loc.HomeHref)
	h = strings.ReplaceAll(h, "{{API_HREF}}", loc.APIHref)
	h = strings.ReplaceAll(h, "{{INSTALL_HREF}}", loc.InstallHref)
	h = strings.ReplaceAll(h, "{{SELFHOST_HREF}}", loc.SelfHostHref)
	h = strings.ReplaceAll(h, "{{BILLING_HREF}}", loc.BillingHref)
	h = strings.ReplaceAll(h, "{{LOGO_HREF}}", loc.LogoHref)
	h = strings.ReplaceAll(h, "{{LOGO_LABEL}}", loc.LogoLabel)
	h = strings.ReplaceAll(h, "{{NAV_ARIA}}", loc.NavAria)
	h = strings.ReplaceAll(h, "{{THEME_SYSTEM}}", loc.ThemeSystem)
	h = strings.ReplaceAll(h, "{{THEME_LIGHT}}", loc.ThemeLight)
	h = strings.ReplaceAll(h, "{{THEME_DARK}}", loc.ThemeDark)
	h = strings.ReplaceAll(h, "{{LANG_SUMMARY}}", loc.LangSummary)
	h = strings.ReplaceAll(h, "{{LANG_ARIA}}", loc.LangAria)

	page := "landing"
	switch activeNav {
	case "install":
		page = "install"
	case "billing":
		page = "billing"
	case "api":
		page = "api"
	case "login":
		page = "login"
	}
	h = strings.ReplaceAll(h, "{{LANG_KO_HREF}}", publicLangHref("ko", page))
	h = strings.ReplaceAll(h, "{{LANG_EN_HREF}}", publicLangHref("en", page))
	h = strings.ReplaceAll(h, "{{LANG_JA_HREF}}", publicLangHref("ja", page))
	h = strings.ReplaceAll(h, "{{LANG_ZH_HREF}}", publicLangHref("zh", page))
	langCurrent := "aria-current=\"true\""
	h = strings.ReplaceAll(h, "{{LANG_KO_CURRENT}}", attrIf(lang == "ko", langCurrent))
	h = strings.ReplaceAll(h, "{{LANG_EN_CURRENT}}", attrIf(lang == "en", langCurrent))
	h = strings.ReplaceAll(h, "{{LANG_JA_CURRENT}}", attrIf(lang == "ja", langCurrent))
	h = strings.ReplaceAll(h, "{{LANG_ZH_CURRENT}}", attrIf(lang == "zh", langCurrent))
	h = strings.ReplaceAll(h, "{{LOGIN_LABEL}}", loc.LoginLabel)
	h = strings.ReplaceAll(h, "{{LOGIN_HREF}}", loginPathForLang(lang))
	// Soft-open: hide public Login CTA (web login only dumps users on /install).
	loginNav := ""
	if !billing.SoftOpen {
		loginNav = fmt.Sprintf(
			`<a class="btn btn-primary btn-sm nav-login" id="nav-login" href="%s">%s</a>`,
			loginPathForLang(lang), loc.LoginLabel,
		)
	}
	h = strings.ReplaceAll(h, "{{LOGIN_NAV}}", loginNav)
	return h
}

func buildSiteFooter(footer, activeNav, lang string, loc siteChromeLocale) string {
	f := footer
	repl := map[string]string{
		"{{HOME_HREF}}": loc.HomeHref, "{{API_HREF}}": loc.APIHref, "{{INSTALL_HREF}}": loc.InstallHref,
		"{{SELFHOST_HREF}}": loc.SelfHostHref,
		"{{HOME_LABEL}}":    loc.HomeLabel, "{{API_LABEL}}": loc.APILabel, "{{INSTALL_LABEL}}": loc.InstallLabel,
		"{{SELFHOST_LABEL}}": loc.SelfHostLabel, "{{BILLING_LABEL}}": loc.BillingLabel,
		"{{API_DOCS_LABEL}}": loc.APIDocsLabel, "{{TAGLINE}}": loc.Tagline,
		"{{PRODUCT_ARIA}}": loc.ProductAria, "{{PRODUCT_TITLE}}": loc.ProductTitle,
		"{{APPS_ARIA}}": loc.AppsAria, "{{APPS_TITLE}}": loc.AppsTitle,
		"{{LEGAL_ARIA}}": loc.LegalAria, "{{LEGAL_TITLE}}": loc.LegalTitle,
		"{{GT_HOME}}": loc.GTHome, "{{GT_ABOUT}}": loc.GTAbout, "{{GT_CONTACT}}": loc.GTContact,
		"{{PRIVACY_LABEL}}": loc.PrivacyLabel, "{{DISCLAIMER_LABEL}}": loc.DisclaimerLabel,
		"{{BILLING_HREF}}": loc.BillingHref, "{{API_DOCS_HREF}}": loc.APIDocsHref,
		"{{OPENLLMS_HREF}}":    loc.OpenllmsHref,
		"{{SETTINGS_LABEL}}":   loc.SettingsLabel,
		"{{APPEARANCE_LABEL}}": loc.AppearanceLabel,
		"{{LANGUAGE_LABEL}}":   loc.LanguageLabel,
		"{{THEME_SYSTEM}}":     loc.ThemeSystem,
		"{{THEME_LIGHT}}":      loc.ThemeLight,
		"{{THEME_DARK}}":       loc.ThemeDark,
	}
	for k, v := range repl {
		f = strings.ReplaceAll(f, k, v)
	}
	page := "landing"
	switch activeNav {
	case "install":
		page = "install"
	case "billing":
		page = "billing"
	case "api":
		page = "api"
	case "login":
		page = "login"
	}
	f = strings.ReplaceAll(f, "{{LANG_KO_HREF}}", publicLangHref("ko", page))
	f = strings.ReplaceAll(f, "{{LANG_EN_HREF}}", publicLangHref("en", page))
	f = strings.ReplaceAll(f, "{{LANG_JA_HREF}}", publicLangHref("ja", page))
	f = strings.ReplaceAll(f, "{{LANG_ZH_HREF}}", publicLangHref("zh", page))
	langCurrent := "aria-current=\"true\""
	f = strings.ReplaceAll(f, "{{LANG_KO_CURRENT}}", attrIf(lang == "ko", langCurrent))
	f = strings.ReplaceAll(f, "{{LANG_EN_CURRENT}}", attrIf(lang == "en", langCurrent))
	f = strings.ReplaceAll(f, "{{LANG_JA_CURRENT}}", attrIf(lang == "ja", langCurrent))
	f = strings.ReplaceAll(f, "{{LANG_ZH_CURRENT}}", attrIf(lang == "zh", langCurrent))
	return f
}

// publicLangHref returns language-switch targets. Korean always goes through
// /ko[…] so the lang cookie is rewritten before the canonical / or /install page.
func publicLangHref(lang, page string) string {
	switch page {
	case "install":
		if lang == "ko" {
			return "/ko/install"
		}
		return installPathForLang(lang)
	case "billing":
		if lang == "ko" {
			return "/ko/billing"
		}
		return billingPathForLang(lang)
	case "api":
		if lang == "ko" {
			return "/ko/api"
		}
		return apiDocsPathForLang(lang)
	case "login":
		if lang == "ko" {
			return "/ko/login"
		}
		return loginPathForLang(lang)
	}
	if lang == "ko" {
		return "/ko"
	}
	return landingPathForLang(lang)
}

func applyChromePageTokens(out string, loc siteChromeLocale) string {
	repl := map[string]string{
		"{{BILLING_HREF}}":  loc.BillingHref,
		"{{API_DOCS_HREF}}": loc.APIDocsHref,
		"{{OPENLLMS_HREF}}": loc.OpenllmsHref,
		"{{INSTALL_HREF}}":  loc.InstallHref,
		"{{HOME_HREF}}":     loc.HomeHref,
		"{{API_HREF}}":      loc.APIHref,
		"{{SELFHOST_HREF}}": loc.SelfHostHref,
	}
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

func attrIf(ok bool, attr string) string {
	if ok {
		return attr
	}
	return ""
}

func serveChromeHTMLBytes(w http.ResponseWriter, r *http.Request, body, activeNav, lang, gaID string) {
	headerTpl, err := fs.ReadFile(publicStatic, siteHeaderPartial)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	footerTpl, err := fs.ReadFile(publicStatic, siteFooterPartial)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	loc := siteChromeLocaleFor(lang)
	loc.BillingHref = billingPathForLang(lang)
	loc.APIDocsHref = apiDocsPathForLang(lang)
	loc.OpenllmsHref = openllmsHrefForLang(lang)
	if activeNav == "admin" {
		loc.LogoLabel = "llms admin"
		loc.LogoHref = "/admin"
	}
	header := buildSiteHeader(string(headerTpl), activeNav, lang, loc)
	footer := buildSiteFooter(string(footerTpl), activeNav, lang, loc)
	out := body
	out = replaceFirstAny(out, header, markerSiteHeader, markerPubHeader, markerAppHeader, markerAdminHeader)
	out = replaceFirstAny(out, footer, markerSiteFooter, markerPubFooter, markerAppFooter)
	out = applyChromePageTokens(out, loc)
	out = maybeInjectGA(out, gaID, activeNav)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(out))
}

var gaMeasurementIDRE = regexp.MustCompile(`^G-[A-Z0-9]+$`)

func publicPageGetsGA(activeNav string) bool {
	switch activeNav {
	case "home", "install", "api":
		return true
	default:
		return false
	}
}

func maybeInjectGA(html, measurementID, activeNav string) string {
	id := strings.TrimSpace(measurementID)
	if id == "" || !publicPageGetsGA(activeNav) || !gaMeasurementIDRE.MatchString(id) {
		return html
	}
	if !strings.Contains(html, "</head>") {
		return html
	}
	snippet := fmt.Sprintf(`<!-- Google tag (gtag.js) -->
<script async src="https://www.googletagmanager.com/gtag/js?id=%[1]s"></script>
<script>
  window.dataLayer = window.dataLayer || [];
  function gtag(){dataLayer.push(arguments);}
  gtag('js', new Date());
  gtag('config', '%[1]s');
</script>
`, id)
	return strings.Replace(html, "</head>", snippet+"</head>", 1)
}

// Compatibility wrappers — same injector.
func servePublicHTML(w http.ResponseWriter, r *http.Request, bodyFile, activeNav, lang, gaID string) {
	serveChromeHTML(w, r, bodyFile, activeNav, lang, gaID)
}

func serveAppPage(w http.ResponseWriter, r *http.Request, bodyFile, activeNav, locale string) {
	serveChromeHTML(w, r, bodyFile, activeNav, locale, "")
}

func serveAdminPage(w http.ResponseWriter, r *http.Request) {
	serveChromeHTML(w, r, "static/admin.html", "admin", "ko", "")
}

func (s *Server) gaMeasurementID() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.GAMeasurementID)
}

func (s *Server) servePublicHTML(w http.ResponseWriter, r *http.Request, bodyFile, activeNav, lang string) {
	servePublicHTML(w, r, bodyFile, activeNav, lang, s.gaMeasurementID())
}
