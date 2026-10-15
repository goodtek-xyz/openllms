package httpserver

import (
	"io/fs"
	"net/http"
	"strings"
)

type loginCopy struct {
	Lang    string
	Title   string
	Skip    string
	H1      string
	Lead    string
	GitHub  string
	Legal   string
	Privacy string
	Terms   string
	And     string
}

func loginCopyFor(lang string) loginCopy {
	switch lang {
	case "en":
		return loginCopy{
			Lang: "en", Title: "llms — Log in", Skip: "Skip to content",
			H1: "Welcome back", Lead: "Sign in with GitHub to continue.",
			GitHub: "Continue with GitHub",
			Legal:  "By signing in, you agree to our", Privacy: "Privacy Policy", Terms: "Terms",
			And: "and",
		}
	case "ja":
		return loginCopy{
			Lang: "ja", Title: "llms — ログイン", Skip: "本文へ",
			H1: "おかえりなさい", Lead: "GitHub でサインインして続きから。",
			GitHub: "GitHub で続ける",
			Legal:  "サインインすると、", Privacy: "プライバシーポリシー", Terms: "利用規約",
			And: "と",
		}
	case "zh":
		return loginCopy{
			Lang: "zh", Title: "llms — 登录", Skip: "跳到正文",
			H1: "欢迎回来", Lead: "使用 GitHub 登录以继续。",
			GitHub: "使用 GitHub 继续",
			Legal:  "登录即表示你同意我们的", Privacy: "隐私政策", Terms: "条款",
			And: "和",
		}
	default:
		return loginCopy{
			Lang: "ko", Title: "llms — 로그인", Skip: "본문으로",
			H1: "다시 오신 것을 환영합니다", Lead: "GitHub로 로그인하고 이어서 사용하세요.",
			GitHub: "GitHub로 계속하기",
			Legal:  "로그인하면", Privacy: "개인정보처리방침", Terms: "이용약관",
			And: "및",
		}
	}
}

func serveLoginHTML(w http.ResponseWriter, r *http.Request, lang string) {
	c := loginCopyFor(lang)
	raw, err := fs.ReadFile(publicStatic, "static/login.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	billingHref := billingPathForLang(lang)
	out := string(raw)
	repl := map[string]string{
		"{{LOGIN_LANG}}":    c.Lang,
		"{{LOGIN_TITLE}}":   c.Title,
		"{{LOGIN_SKIP}}":    c.Skip,
		"{{LOGIN_H1}}":      c.H1,
		"{{LOGIN_LEAD}}":    c.Lead,
		"{{LOGIN_GITHUB}}":  c.GitHub,
		"{{LOGIN_LEGAL}}":   c.Legal,
		"{{LOGIN_PRIVACY}}": c.Privacy,
		"{{LOGIN_TERMS}}":   c.Terms,
		"{{LOGIN_AND}}":     c.And,
		"{{BILLING_HREF}}":  billingHref,
		"{{LOGIN_HREF}}":    loginPathForLang(lang),
		"{{INSTALL_HREF}}":  installPathForLang(lang),
	}
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	serveChromeHTMLBytes(w, r, out, "login", lang, "")
}
