package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/goodtek-xyz/openllms/internal/githubauth"
	"github.com/goodtek-xyz/openllms/internal/router"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/goodtek-xyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type credsFile struct {
	APIBase string `json:"api_base"`
	APIKey  string `json:"api_key"`
	Login   string `json:"login"`
}

func credsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "llms", "credentials.json")
}

func loadCreds() (credsFile, error) {
	b, err := os.ReadFile(credsPath())
	if err != nil {
		return credsFile{}, err
	}
	var c credsFile
	return c, json.Unmarshal(b, &c)
}

func saveCreds(c credsFile) error {
	dir := filepath.Dir(credsPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(credsPath(), b, 0o600)
}

// resolveLoginAPIKey picks the gateway-issued key, or keeps the previous local
// key when re-login leaves api_key unchanged (empty).
func resolveLoginAPIKey(out map[string]any, prevAPIKey string) (key string, unchanged bool, err error) {
	key, _ = out["api_key"].(string)
	unchanged, _ = out["api_key_unchanged"].(bool)
	login, _ := out["login"].(string)
	if key == "" {
		key = prevAPIKey
	}
	if key == "" {
		if login == "" {
			login = "unknown"
		}
		return "", unchanged, fmt.Errorf("logged in as %s but no api_key returned and no local key — run: llms key create", login)
	}
	return key, unchanged, nil
}

func clearCreds() error {
	err := os.Remove(credsPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// formatUserSection renders the status board USER header from local session + base URL.
func formatUserSection(login, base string, keyFromEnv bool) string {
	var b strings.Builder
	b.WriteString("USER\n")
	if login != "" {
		b.WriteString("  ")
		b.WriteString(login)
	} else if keyFromEnv {
		b.WriteString("  (LLMS_API_KEY from env)")
	} else {
		b.WriteString("  (unknown login)")
	}
	if base != "" {
		b.WriteString("  ")
		b.WriteString(base)
	}
	b.WriteByte('\n')
	return b.String()
}

func apiBase() string {
	if v := os.Getenv("LLMS_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if c, err := loadCreds(); err == nil && c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return "https://llms.goodtek.xyz"
}

func apiKey() string {
	if v := os.Getenv("LLMS_API_KEY"); v != "" {
		return v
	}
	if c, err := loadCreds(); err == nil {
		return c.APIKey
	}
	return ""
}

func doJSON(ctx context.Context, method, path string, body any, auth bool) (map[string]any, int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase()+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+apiKey())
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{"raw": string(b)}
	}
	return out, res.StatusCode, nil
}

func billingURL() string {
	return strings.TrimRight(apiBase(), "/") + "/install"
}

func printBillingError(out map[string]any, code int) error {
	if code == http.StatusPaymentRequired || code == http.StatusTooManyRequests {
		if u, ok := out["billing"].(string); ok && u != "" {
			fmt.Fprintf(os.Stderr, "Billing: %s\n", u)
		} else {
			fmt.Fprintf(os.Stderr, "Billing: %s\n", billingURL())
		}
	}
	return fmt.Errorf("%v", out)
}

func activateFreePlan(ctx context.Context) (map[string]any, int, error) {
	out, code, err := doJSON(ctx, http.MethodPost, "/control/v1/billing/free", map[string]any{}, true)
	if err == nil && code != http.StatusNotFound {
		return out, code, nil
	}
	// Older gateways only expose the legacy /trial path (same EnsureFreePlan).
	return doJSON(ctx, http.MethodPost, "/control/v1/billing/trial", map[string]any{}, true)
}

func freePlanAvailable(out map[string]any) bool {
	if v, ok := out["free_available"].(bool); ok {
		return v
	}
	v, _ := out["trial_available"].(bool)
	return v
}

func ensureHubEntitled(ctx context.Context) error {
	out, code, err := doJSON(ctx, http.MethodGet, "/control/v1/billing", nil, true)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("billing status: %v", out)
	}
	if entitled, _ := out["entitled"].(bool); entitled {
		return nil
	}
	if freePlanAvailable(out) {
		freeOut, freeCode, err := activateFreePlan(ctx)
		if err != nil {
			return err
		}
		if freeCode < 300 {
			if entitled, _ := freeOut["entitled"].(bool); entitled {
				fmt.Fprintln(os.Stderr, "Activated free cloud (1 account / 1 route / 1 key to start).")
				return nil
			}
		}
	}
	return printBillingError(out, http.StatusPaymentRequired)
}

func cmdPlan() *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Show hub plan and limits (free cloud soft-open)",
		Long: `Show current hub entitlement.

Soft-open: free hosted cloud for now. Prefer self-host? openllms.
Activate with: llms free
Need the cloud path: llms signal`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			out, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/billing", nil, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("%v", out)
			}
			fmt.Println("PLAN")
			fmt.Printf("  plan       %v\n", out["plan"])
			fmt.Printf("  status     %v\n", out["status"])
			fmt.Printf("  entitled   %v\n", out["entitled"])
			if out["period_end"] != nil {
				fmt.Printf("  period_end %v\n", out["period_end"])
			}
			if lim, ok := out["limits"].(map[string]any); ok {
				fmt.Printf("  limits     accounts=%v routes=%v keys=%v rpm=%v soft_cap=%s\n",
					lim["accounts"], lim["routes"], lim["keys"], lim["rpm"], formatUsageCount(lim["soft_cap_tokens"]))
			}
			if u, ok := out["usage_month"].(map[string]any); ok {
				fmt.Printf("  usage      requests=%s tokens=%s soft_cap=%s\n",
					formatUsageCount(u["requests"]),
					formatUsageCount(u["tokens_total"]),
					formatUsageCount(u["soft_cap_tokens"]))
			}
			if u, ok := out["billing_url"].(string); ok && u != "" {
				fmt.Printf("  billing    %s\n", u)
			} else {
				fmt.Printf("  billing    %s\n", billingURL())
			}
			if u, ok := out["crew_signal_url"].(string); ok && u != "" {
				fmt.Printf("  signal     %s\n", u)
			} else {
				fmt.Printf("  signal     %s#crew-signal\n", billingURL())
			}
			plan, _ := out["plan"].(string)
			if plan == "" || plan == "none" || plan == "free" {
				fmt.Println("  tip        llms free (activate cloud) · llms signal (tell us you need cloud)")
			}
			return nil
		},
	}
}

func cmdFree() *cobra.Command {
	return &cobra.Command{
		Use:     "free",
		Aliases: []string{"trial"}, // legacy alias; hidden from command list as primary name is free
		Short:   "Activate free cloud (1 account / 1 route / 1 key to start)",
		Long: `Activate the free hosted cloud plan.

Starter seats for trying the hub. Prefer running your own box? Use openllms.
If cloud matters to how you work: llms signal`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			out, code, err := activateFreePlan(context.Background())
			if err != nil {
				return err
			}
			if code >= 300 {
				return printBillingError(out, code)
			}
			fmt.Printf("Free active — plan %v entitled=%v\n", out["plan"], out["entitled"])
			return nil
		},
	}
}

func cmdUpgrade() *cobra.Command {
	var open bool
	c := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"signal"},
		Short:   "Tell us you need the cloud (demand signal)",
		Long: `Soft-open: hosted cloud is free for now; paid checkout is paused.

Sends a crew signal (one per day). Prefer CLI over the old billing page
tells goodtek + VibeCrew that builders need the cloud path.

Also: llms signal (alias).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			url := billingURL() + "#crew-signal"
			if apiKey() != "" {
				out, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/billing", nil, true)
				if err == nil && code < 300 {
					if u, ok := out["crew_signal_url"].(string); ok && u != "" {
						url = u
					} else if u, ok := out["billing_url"].(string); ok && u != "" {
						url = u + "#crew-signal"
					}
				}
				// Best-effort: send signal from CLI when logged in (empty note).
				sig, sc, serr := doJSON(context.Background(), http.MethodPost, "/control/v1/crew-signal", map[string]any{"note": ""}, true)
				if serr == nil && sc < 300 {
					fmt.Printf("Noted — you need cloud. week=%v total=%v\n", sig["week_count"], sig["total_count"])
					fmt.Println(url)
					fmt.Println("goodtek · https://goodtek.xyz/  ·  VibeCrew · https://vibecrew.jp/")
					return nil
				}
				if sc == http.StatusConflict {
					fmt.Println("Already told us today — thanks.")
					fmt.Println(url)
					return nil
				}
			}
			fmt.Println(url)
			fmt.Println("Sign in (llms login), then tell us if you need the hosted cloud — free for now.")
			fmt.Println("goodtek · https://goodtek.xyz/  ·  VibeCrew · https://vibecrew.jp/")
			if open {
				fmt.Println("(Open the URL in your browser.)")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&open, "open", false, "Print only; browser open is manual")
	return c
}

func cmdLogout() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove local credentials (sk-gt session)",
		Long:  "Deletes ~/.config/llms/credentials.json. Does not revoke the gateway API key or GitHub OAuth.",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := credsPath()
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Println("Already logged out (no credentials file).")
				if os.Getenv("LLMS_API_KEY") != "" {
					fmt.Println("Note: LLMS_API_KEY is still set in the environment.")
				}
				return nil
			}
			if err := clearCreds(); err != nil {
				return err
			}
			fmt.Printf("Logged out. Removed %s\n", path)
			if os.Getenv("LLMS_API_KEY") != "" {
				fmt.Println("Note: LLMS_API_KEY is still set in the environment.")
			}
			return nil
		},
	}
}

func cmdLogin() *cobra.Command {
	var base string
	var rotateKey bool
	c := &cobra.Command{
		Use:   "login",
		Short: "GitHub device login → store sk-gt for control API",
		RunE: func(cmd *cobra.Command, args []string) error {
			if base == "" {
				base = apiBase()
			}
			base = strings.TrimRight(base, "/")
			if err := os.Setenv("LLMS_API_BASE", base); err != nil {
				return err
			}
			ctx := context.Background()
			clientID := os.Getenv("GITHUB_CLIENT_ID")
			if clientID == "" {
				if meta, code, err := doJSON(ctx, http.MethodGet, "/control/v1/meta", nil, false); err == nil && code < 300 {
					clientID, _ = meta["github_client_id"].(string)
				}
			}
			if clientID == "" {
				return fmt.Errorf("GITHUB_CLIENT_ID required (set env or ensure gateway /control/v1/meta exposes it — register a GitHub OAuth App with Device Flow enabled)")
			}
			dc, uc, uri, interval, err := githubauth.DeviceStart(ctx, clientID)
			if err != nil {
				return err
			}
			fmt.Printf("Open %s and enter code: %s\n", uri, uc)
			tok, err := githubauth.PollUntilToken(ctx, clientID, dc, interval)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "GitHub authorized. Contacting gateway…")
			body := map[string]any{"access_token": tok}
			if rotateKey {
				body["rotate_cli_key"] = true
			}
			out, code, err := doJSON(ctx, http.MethodPost, "/control/v1/auth/github", body, false)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("gateway auth: %v", out)
			}
			prevKey := ""
			if prev, err := loadCreds(); err == nil {
				prevKey = prev.APIKey
			}
			key, unchanged, err := resolveLoginAPIKey(out, prevKey)
			if err != nil {
				return err
			}
			login, _ := out["login"].(string)
			if err := saveCreds(credsFile{APIBase: strings.TrimRight(base, "/"), APIKey: key, Login: login}); err != nil {
				return err
			}
			fmt.Printf("Logged in as %s. Credentials saved to %s\n", login, credsPath())
			if unchanged {
				fmt.Println("Existing CLI API key kept (re-login does not rotate sk-gt).")
			} else {
				fmt.Println("API key stored locally (shown once by gateway).")
			}
			fmt.Println("Next: llms connect  (or llms add) — Free plan activates automatically on first connect.")
			fmt.Println("Plans: llms plan · llms free · llms signal  (cloud free for now · tell us with llms signal)")
			return nil
		},
	}
	c.Flags().StringVar(&base, "api-base", "", "Gateway base URL (default LLMS_API_BASE, credentials, or https://llms.goodtek.xyz)")
	c.Flags().BoolVar(&rotateKey, "rotate-key", false, "Revoke existing cli sk-gt and mint a new one (explicit only)")
	return c
}

func cmdStatus() *cobra.Command {
	var refresh bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show accounts and routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			if refresh {
				out, code, err := doJSON(context.Background(), http.MethodPost, "/control/v1/quota/refresh", map[string]any{}, true)
				if err != nil {
					return err
				}
				if code >= 300 {
					return fmt.Errorf("quota refresh: %v", out)
				}
				fmt.Fprintf(os.Stderr, "Quota refresh: oauth_updated=%v oauth_failed=%v usage_heuristic=%v\n",
					out["oauth_updated"], out["oauth_failed"], out["usage_heuristic_updated"])
			}
			out, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/status", nil, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("%v", out)
			}
			login := ""
			if c, err := loadCreds(); err == nil {
				login = c.Login
			}
			fmt.Print(formatUserSection(login, apiBase(), os.Getenv("LLMS_API_KEY") != ""))
			fmt.Println("ACCOUNTS")
			nAcc := 0
			if arr, ok := out["accounts"].([]any); ok {
				nAcc = len(arr)
				refW := maxRefWidth(arr)
				now := time.Now().UTC()
				for _, raw := range arr {
					m := raw.(map[string]any)
					fmt.Println(formatAccountLine(m, refW, now))
				}
			}
			if nAcc == 0 {
				fmt.Println("  (none)  llms connect  — Claude/Codex: browser login; DeepSeek: API key")
				fmt.Println("  Cursor is a client — set its Base URL to a route, not a vendor here.")
			}
			fmt.Println("ROUTES")
			if arr, ok := out["routes"].([]any); ok {
				for _, raw := range arr {
					m := raw.(map[string]any)
					fmt.Printf("  %-20s %-12s %s\n", m["slug"], m["strategy"], m["openai_base"])
				}
			}
			fmt.Println("USAGE (month UTC)")
			if u, ok := out["usage_month"].(map[string]any); ok {
				fmt.Println(formatUsageMonthLine(u))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&refresh, "refresh", false, "Fetch provider quota (Codex/Claude) then show status")
	return c
}

func cmdAdd() *cobra.Command {
	return &cobra.Command{
		Use:     "add",
		Aliases: []string{"connect"},
		Short:   "Connect a vendor account (browser login or API key)",
		Long: `Connect an upstream vendor account. Alias: llms connect.

Claude and Codex default to browser/device login (no localhost callback).
API-key vendors: DeepSeek, OpenAI, Kimi, GLM.
Cursor is a client — point its Base URL at a route from llms route; it is not a vendor.

If you have not subscribed yet, the first connect activates the Free plan automatically.

Interactive menus: Esc cancels. Confirm only when deleting.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitInteractive(runAddWizard())
		},
	}
}

func runAddWizard() error {
	if apiKey() == "" {
		return fmt.Errorf("not logged in — run: llms login")
	}
	if err := ensureHubEntitled(context.Background()); err != nil {
		return err
	}
	var vend, name, key, baseURL, authType, access, refresh string
	var expires, chatgptAcct, idToken string

	if err := runSelect("Vendor", []huh.Option[string]{
		huh.NewOption("claude", "claude"),
		huh.NewOption("codex", "codex"),
		huh.NewOption("deepseek", "deepseek"),
		huh.NewOption("openai", "openai"),
		huh.NewOption("kimi", "kimi"),
		huh.NewOption("glm", "glm"),
	}, &vend); err != nil {
		return err
	}

	authOpts := []huh.Option[string]{
		huh.NewOption("API key", "api_key"),
		huh.NewOption("OAuth tokens (paste)", "oauth"),
	}
	if vend == "claude" || vend == "codex" {
		authOpts = []huh.Option[string]{
			huh.NewOption("Browser login", "browser"),
			huh.NewOption("API key", "api_key"),
			huh.NewOption("OAuth tokens (paste)", "oauth"),
		}
	}
	if err := formErr(huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Auth").Options(authOpts...).Value(&authType),
		huh.NewInput().Title("Account name").Placeholder("work").Value(&name),
	)).Run()); err != nil {
		return err
	}
	if name == "" {
		name = "default"
	}

	switch authType {
	case "browser":
		toks, err := runVendorBrowserLogin(vend)
		if err != nil {
			return err
		}
		authType = "oauth"
		access, refresh = toks.AccessToken, toks.RefreshToken
		expires, chatgptAcct, idToken = toks.ExpiresAt, toks.ChatGPTAccountID, toks.IDToken
	case "api_key":
		if err := formErr(huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("API key").EchoMode(huh.EchoModePassword).Value(&key),
			huh.NewInput().Title("Base URL (optional)").Value(&baseURL),
		)).Run()); err != nil {
			return err
		}
	default:
		if err := formErr(huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Access token").EchoMode(huh.EchoModePassword).Value(&access),
			huh.NewInput().Title("Refresh token (optional)").EchoMode(huh.EchoModePassword).Value(&refresh),
			huh.NewInput().Title("Base URL (optional)").Value(&baseURL),
		)).Run()); err != nil {
			return err
		}
	}

	var defaultModel string
	hint := ""
	switch vend {
	case "claude", "anthropic":
		hint = "claude-sonnet-4-6"
	case "codex", "openai":
		hint = "gpt-5.6-luna"
	}
	if err := formErr(huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Default model (used when a route picks this account)").Placeholder(hint).Value(&defaultModel),
	)).Run()); err != nil {
		return err
	}
	if defaultModel == "" {
		defaultModel = hint
	}

	body := map[string]string{"vendor": vend, "name": name, "auth_type": authType, "default_model": defaultModel}
	if authType == "oauth" {
		body["access_token"] = access
		body["refresh_token"] = refresh
		body["expires_at"] = expires
		body["chatgpt_account_id"] = chatgptAcct
		body["id_token"] = idToken
	} else {
		body["api_key"] = key
	}
	if baseURL != "" {
		body["base_url"] = baseURL
	} else if authType == "oauth" {
		body["base_url"] = vendor.DefaultBaseURLFor(vend, authType)
	}
	out, code, err := doJSON(context.Background(), http.MethodPost, "/control/v1/accounts", body, true)
	if err != nil {
		return err
	}
	if code >= 300 {
		return printBillingError(out, code)
	}
	fmt.Printf("Connected %s:%s id=%v default_model=%s\n", vend, name, out["id"], defaultModel)
	return nil
}

func runVendorBrowserLogin(vend string) (vendorauth.Tokens, error) {
	ctx := context.Background()
	switch vend {
	case "claude":
		p, err := vendorauth.ClaudeStart()
		if err != nil {
			return vendorauth.Tokens{}, err
		}
		fmt.Printf("Open %s\n", p.AuthURL)
		fmt.Fprintln(os.Stderr, "Approve in the browser, then paste the authorization code (Esc cancels).")
		fmt.Fprintln(os.Stderr, "(Copy the code from the callback page, or paste the full callback URL / code#state.)")
		var pasted string
		if err := runInput("Claude authorization code", "", &pasted); err != nil {
			return vendorauth.Tokens{}, err
		}
		return vendorauth.ClaudeExchange(ctx, pasted, p.Verifier, p.State)
	case "codex":
		p, err := vendorauth.CodexStart(ctx)
		if err != nil {
			return vendorauth.Tokens{}, err
		}
		fmt.Printf("Open %s and enter code: %s\n", p.VerifyURL, p.UserCode)
		return vendorauth.CodexPollUntil(ctx, p)
	default:
		return vendorauth.Tokens{}, fmt.Errorf("browser login is for Claude and Codex; Cursor is a client (route Base URL), not a vendor")
	}
}

func cmdRoute() *cobra.Command {
	root := &cobra.Command{Use: "route", Short: "List / create / update / delete routes"}
	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			out, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/routes", nil, true)
			if err != nil || code >= 300 {
				return fmt.Errorf("list routes: %v %v", err, out)
			}
			arr, _ := out["routes"].([]any)
			if len(arr) == 0 {
				fmt.Println("(no routes — llms route create)")
				return nil
			}
			fmt.Printf("%-22s %-14s %s\n", "SLUG", "PRESET", "ACCOUNTS")
			for _, raw := range arr {
				m, _ := raw.(map[string]any)
				preset, _ := m["preset"].(string)
				if preset == "" {
					preset, _ = m["strategy"].(string)
				}
				acc := ""
				if refs, ok := m["accounts"].([]any); ok && len(refs) > 0 {
					parts := make([]string, 0, len(refs))
					for _, r := range refs {
						parts = append(parts, fmt.Sprint(r))
					}
					acc = strings.Join(parts, ", ")
				}
				fmt.Printf("%-22s %-14s %s\n", m["slug"], preset, acc)
			}
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "create",
		Short: "Interactive route wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitInteractive(runRouteCreateWizard())
		},
	})

	var updatePreset, updateAccounts string
	updateCmd := &cobra.Command{
		Use:   "update [slug]",
		Short: "Update route preset and/or accounts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := ""
			if len(args) == 1 {
				slug = args[0]
			}
			return exitInteractive(runRouteUpdateWizard(slug, updatePreset, updateAccounts))
		},
	}
	updateCmd.Flags().StringVar(&updatePreset, "preset", "", "failover|fill-first|balance|prefer-primary|quota-first|reset-soon|steward|parallel")
	updateCmd.Flags().StringVar(&updateAccounts, "accounts", "", "comma-separated account UUIDs (replaces membership)")
	root.AddCommand(updateCmd)

	var rmYes bool
	rmCmd := &cobra.Command{
		Use:     "rm [slug]",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete a route",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := ""
			if len(args) == 1 {
				slug = args[0]
			}
			return exitInteractive(runRouteRmWizard(slug, rmYes))
		},
	}
	rmCmd.Flags().BoolVar(&rmYes, "yes", false, "skip confirmation")
	root.AddCommand(rmCmd)

	root.AddCommand(&cobra.Command{
		Use:   "url [slug]",
		Short: "Print OpenAI base URL for a route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(apiBase() + "/r/" + args[0] + "/v1")
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "key [slug]",
		Short: "Mint a route-scoped sk-gt (shown once)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			if err := ensureHubEntitled(context.Background()); err != nil {
				return err
			}
			slug := args[0]
			out, code, err := doJSON(context.Background(), http.MethodPost, "/control/v1/keys", map[string]any{
				"name": "route:" + slug, "route_slug": slug,
			}, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return printBillingError(out, code)
			}
			fmt.Printf("Route %s\nURL  %s\nKey  %s\n", slug, apiBase()+"/r/"+slug+"/v1", out["api_key"])
			fmt.Println("(store the key now — it will not be shown again)")
			return nil
		},
	})
	return root
}

func runRouteCreateWizard() error {
	if apiKey() == "" {
		return fmt.Errorf("not logged in")
	}
	accOut, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/accounts", nil, true)
	if err != nil || code >= 300 {
		return fmt.Errorf("list accounts: %v %v", err, accOut)
	}
	var accOpts []huh.Option[string]
	if arr, ok := accOut["accounts"].([]any); ok {
		for _, raw := range arr {
			m := raw.(map[string]any)
			label := fmt.Sprintf("%s:%s", m["vendor"], m["name"])
			accOpts = append(accOpts, huh.NewOption(label, m["id"].(string)))
		}
	}
	if len(accOpts) == 0 {
		return fmt.Errorf("no accounts — run llms add first")
	}
	if err := ensureHubEntitled(context.Background()); err != nil {
		return err
	}
	var slug, preset string
	var selected []string
	for {
		if err := runInput("Route slug", "claude-failover", &slug); err != nil {
			return err
		}
		slug = strings.TrimSpace(slug)
		if slug != "" {
			break
		}
		fmt.Fprintln(os.Stderr, "slug required")
	}
	preset = "quota-first"
	if err := runRouteConfigForm(&preset, accOpts, &selected); err != nil {
		return err
	}
	if len(selected) == 0 {
		return fmt.Errorf("select at least one account")
	}
	out, code, err := doJSON(context.Background(), http.MethodPost, "/control/v1/routes", map[string]any{
		"slug": slug, "preset": preset,
	}, true)
	if err != nil {
		return err
	}
	if code >= 300 {
		return printBillingError(out, code)
	}
	_, cfg := router.ApplyPreset(preset)
	cfgJSON, _ := json.Marshal(cfg)
	for i, id := range selected {
		_, _, _ = doJSON(context.Background(), http.MethodPost, "/control/v1/routes/"+slug+"/accounts", map[string]any{
			"account_id": id, "position": i, "weight": router.AttachWeight(cfgJSON, i),
		}, true)
	}
	base := apiBase() + "/r/" + slug + "/v1"
	fmt.Printf("Route %s created\nURL  %s\n", slug, base)
	fmt.Printf("Next  llms route key %s   # mint client sk-gt (shown once)\n", slug)
	fmt.Printf("      llms env %s         # export OPENAI_BASE_URL + login key\n", slug)
	fmt.Printf("      llms credentials    # show local session\n")
	return nil
}

func runRouteUpdateWizard(slug, updatePreset, updateAccounts string) error {
	if apiKey() == "" {
		return fmt.Errorf("not logged in — run: llms login")
	}
	if err := ensureHubEntitled(context.Background()); err != nil {
		return err
	}
	routesOut, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/routes", nil, true)
	if err != nil || code >= 300 {
		return fmt.Errorf("list routes: %v %v", err, routesOut)
	}
	routeRows, _ := routesOut["routes"].([]any)
	if len(routeRows) == 0 {
		return fmt.Errorf("no routes — create one: llms route create")
	}
	routeBySlug := map[string]map[string]any{}
	var slugOpts []huh.Option[string]
	for _, raw := range routeRows {
		m, _ := raw.(map[string]any)
		s, _ := m["slug"].(string)
		if s == "" {
			continue
		}
		routeBySlug[s] = m
		slugOpts = append(slugOpts, huh.NewOption(s, s))
	}

	nonInteractive := updatePreset != "" || updateAccounts != ""
	var selected []string
	if updateAccounts != "" {
		for _, p := range strings.Split(updateAccounts, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				selected = append(selected, p)
			}
		}
	}
	preset := updatePreset

	if !nonInteractive {
		if slug == "" {
			if err := runSelect("Route", slugOpts, &slug); err != nil {
				return err
			}
		}
		cur, ok := routeBySlug[slug]
		if !ok {
			return fmt.Errorf("route %q not found — llms route list", slug)
		}
		curPreset, _ := cur["preset"].(string)
		if curPreset == "" {
			curPreset, _ = cur["strategy"].(string)
		}
		preset = curPreset
		if ids, ok := cur["account_ids"].([]any); ok {
			for _, id := range ids {
				selected = append(selected, fmt.Sprint(id))
			}
		}
		accOut, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/accounts", nil, true)
		if err != nil || code >= 300 {
			return fmt.Errorf("list accounts: %v %v", err, accOut)
		}
		var accOpts []huh.Option[string]
		if arr, ok := accOut["accounts"].([]any); ok {
			for _, raw := range arr {
				m := raw.(map[string]any)
				label := fmt.Sprintf("%s:%s", m["vendor"], m["name"])
				accOpts = append(accOpts, huh.NewOption(label, m["id"].(string)))
			}
		}
		if len(accOpts) == 0 {
			return fmt.Errorf("no accounts — run llms add first")
		}
		if err := runRouteConfigForm(&preset, accOpts, &selected); err != nil {
			return err
		}
		if len(selected) == 0 {
			return fmt.Errorf("select at least one account")
		}
		return patchRoute(slug, preset, selected, true)
	}

	if slug == "" {
		return fmt.Errorf("route required")
	}
	if _, ok := routeBySlug[slug]; !ok {
		return fmt.Errorf("route %q not found — llms route list", slug)
	}
	if preset == "" && len(selected) == 0 {
		return fmt.Errorf("nothing to update — pass --preset and/or --accounts, or use interactive mode")
	}
	return patchRoute(slug, preset, selected, updateAccounts != "")
}

func patchRoute(slug, preset string, accountIDs []string, setAccounts bool) error {
	body := map[string]any{}
	if preset != "" {
		body["preset"] = preset
	}
	if setAccounts {
		body["account_ids"] = accountIDs
	}
	out, code, err := doJSON(context.Background(), http.MethodPatch, "/control/v1/routes/"+slug, body, true)
	if err != nil {
		return err
	}
	if code >= 300 {
		return printBillingError(out, code)
	}
	fmt.Printf("Route %s updated\nURL  %s\n", slug, apiBase()+"/r/"+slug+"/v1")
	if p, _ := out["preset"].(string); p != "" {
		fmt.Printf("Preset %s\n", p)
	}
	return nil
}

func runRouteRmWizard(slug string, yes bool) error {
	if apiKey() == "" {
		return fmt.Errorf("not logged in — run: llms login")
	}
	if slug == "" {
		out, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/routes", nil, true)
		if err != nil || code >= 300 {
			return fmt.Errorf("list routes: %v %v", err, out)
		}
		arr, _ := out["routes"].([]any)
		if len(arr) == 0 {
			return fmt.Errorf("no routes")
		}
		var opts []huh.Option[string]
		for _, raw := range arr {
			m, _ := raw.(map[string]any)
			s, _ := m["slug"].(string)
			if s != "" {
				opts = append(opts, huh.NewOption(s, s))
			}
		}
		if err := runSelect("Delete route", opts, &slug); err != nil {
			return err
		}
	}
	if !yes {
		var confirm bool
		if err := runConfirm(fmt.Sprintf("Delete route %q?", slug), &confirm); err != nil {
			return err
		}
		if !confirm {
			return errCancelled
		}
	}
	out, code, err := doJSON(context.Background(), http.MethodDelete, "/control/v1/routes/"+slug, nil, true)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("%v", out)
	}
	fmt.Printf("Deleted route %s\n", slug)
	return nil
}

func routePresetOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("failover (sequential)", "failover"),
		huh.NewOption("fill-first (burn one then next)", "fill-first"),
		huh.NewOption("balance (round_robin)", "balance"),
		huh.NewOption("prefer-primary (weighted)", "prefer-primary"),
		huh.NewOption("quota-first (quota_aware)", "quota-first"),
		huh.NewOption("reset-soon (earliest window reset)", "reset-soon"),
		huh.NewOption("steward (remaining × reset urgency)", "steward"),
		huh.NewOption("parallel / race (API-key only)", "parallel"),
	}
}

func cmdEnv() *cobra.Command {
	return &cobra.Command{
		Use:   "env [slug]",
		Short: "Print export lines for a route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("export OPENAI_BASE_URL=%q\n", apiBase()+"/r/"+args[0]+"/v1")
			fmt.Printf("export OPENAI_API_KEY=%q\n", apiKey())
			return nil
		},
	}
}

func cmdKey() *cobra.Command {
	root := &cobra.Command{Use: "key", Short: "List / create / revoke sk-gt API keys"}
	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List project API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			out, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/keys", nil, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("%v", out)
			}
			fmt.Println("KEYS")
			arr, _ := out["keys"].([]any)
			if len(arr) == 0 {
				fmt.Println("  (none)")
				return nil
			}
			for _, raw := range arr {
				m := raw.(map[string]any)
				status := "active"
				if rev, _ := m["revoked"].(bool); rev {
					status = "revoked"
				}
				fmt.Printf("  %s  %-16s  %s  %v\n", m["id"], m["name"], m["key_prefix"], status)
			}
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "create [name]",
		Short: "Create a project-scoped key (shown once)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			name := "default"
			if len(args) == 1 {
				name = args[0]
			}
			out, code, err := doJSON(context.Background(), http.MethodPost, "/control/v1/keys", map[string]any{"name": name}, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("%v", out)
			}
			fmt.Printf("Key  %s\n(store now — will not be shown again)\n", out["api_key"])
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "revoke [id]",
		Short: "Revoke a key by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			out, code, err := doJSON(context.Background(), http.MethodPost, "/control/v1/keys/"+args[0]+"/revoke", map[string]any{}, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("%v", out)
			}
			fmt.Printf("Revoked %s\n", args[0])
			return nil
		},
	})
	return root
}

func resolveAccountID(ref string) (string, error) {
	if _, err := uuid.Parse(ref); err == nil {
		return ref, nil
	}
	accOut, code, err := doJSON(context.Background(), http.MethodGet, "/control/v1/accounts", nil, true)
	if err != nil || code >= 300 {
		return "", fmt.Errorf("list accounts: %v %v", err, accOut)
	}
	if arr, ok := accOut["accounts"].([]any); ok {
		for _, raw := range arr {
			m := raw.(map[string]any)
			label := fmt.Sprintf("%s:%s", m["vendor"], m["name"])
			if label == ref {
				return fmt.Sprint(m["id"]), nil
			}
		}
	}
	return "", fmt.Errorf("account %q not found — run: llms status", ref)
}

func cmdAccount() *cobra.Command {
	root := &cobra.Command{
		Use:   "account",
		Short: "Inspect or update upstream accounts",
	}
	root.AddCommand(&cobra.Command{
		Use:   "model <vendor:name|account-id> <model-id>",
		Short: "Set the default chat model for an account (changeable later)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			id, err := resolveAccountID(args[0])
			if err != nil {
				return err
			}
			out, code, err := doJSON(context.Background(), http.MethodPatch, "/control/v1/accounts/"+id, map[string]any{
				"default_model": args[1],
			}, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("%v", out)
			}
			fmt.Printf("Updated %s default_model=%s\n", args[0], args[1])
			return nil
		},
	})
	return root
}

func cmdDisconnect() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect [vendor:name|account-id]",
		Short: "Remove an upstream account (and best-effort Infisical secret)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiKey() == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			id, err := resolveAccountID(args[0])
			if err != nil {
				return err
			}
			out, code, err := doJSON(context.Background(), http.MethodDelete, "/control/v1/accounts/"+id, nil, true)
			if err != nil {
				return err
			}
			if code >= 300 {
				return fmt.Errorf("%v", out)
			}
			fmt.Printf("Disconnected %s (secret_deleted=%v)\n", args[0], out["secret_deleted"])
			return nil
		},
	}
}

func cmdModels() *cobra.Command {
	var filter string
	c := &cobra.Command{
		Use:   "models [slug]",
		Short: "List real chat model ids for a route (searchable)",
		Long: `Surfaces provider-backed models from GET /control/v1/routes/{slug}/models.

Model ids are upstream chat models (e.g. gpt-5.5), never vendor:account refs.
Omit slug to pick a route. Use --filter to narrow by id, provider, or account.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitInteractive(runModels(args, filter))
		},
	}
	c.Flags().StringVar(&filter, "filter", "", "Substring filter on model id, provider, or account ref")
	return c
}

func runModels(args []string, filter string) error {
	if apiKey() == "" {
		return fmt.Errorf("not logged in — run: llms login")
	}
	ctx := context.Background()
	slug := ""
	if len(args) == 1 {
		slug = strings.TrimSpace(args[0])
	}
	if slug == "" {
		picked, err := pickRouteSlug(ctx)
		if err != nil {
			return err
		}
		slug = picked
	}
	out, code, err := doJSON(ctx, http.MethodGet, "/control/v1/routes/"+slug+"/models", nil, true)
	if err != nil {
		return err
	}
	if code == http.StatusNotFound {
		return fmt.Errorf("route %q not found — create one: llms route create", slug)
	}
	if code >= 300 {
		return fmt.Errorf("models: %v", out)
	}
	route, _ := out["route"].(string)
	if route == "" {
		route = slug
	}
	strategy, _ := out["strategy"].(string)
	suggested, _ := out["suggested_model"].(string)
	rows := filterModelRows(parseUnionModels(out["models"]), filter)
	var accounts []any
	if arr, ok := out["accounts"].([]any); ok {
		accounts = arr
	}
	fmt.Print(formatModelsBoard(route, strategy, suggested, rows, accounts))

	// Interactive pick when TTY and no --filter (list-only otherwise).
	if filter == "" && isInteractive() && len(rows) > 0 {
		chosen, err := pickModelID(rows)
		if err != nil {
			return err
		}
		if chosen != "" {
			fmt.Printf("\nSELECTED  %s\n", chosen)
		}
	}
	return nil
}

func pickRouteSlug(ctx context.Context) (string, error) {
	out, code, err := doJSON(ctx, http.MethodGet, "/control/v1/routes", nil, true)
	if err != nil {
		return "", err
	}
	if code >= 300 {
		return "", fmt.Errorf("list routes: %v", out)
	}
	var slugs []string
	if arr, ok := out["routes"].([]any); ok {
		for _, raw := range arr {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			s := strings.TrimSpace(fmt.Sprint(m["slug"]))
			if s != "" && s != "<nil>" {
				slugs = append(slugs, s)
			}
		}
	}
	if len(slugs) == 0 {
		return "", fmt.Errorf("no routes — create one: llms route create")
	}
	if len(slugs) == 1 {
		return slugs[0], nil
	}
	if !isInteractive() {
		return "", fmt.Errorf("multiple routes — pass a slug: llms models <%s>", strings.Join(slugs, "|"))
	}
	var chosen string
	opts := make([]huh.Option[string], 0, len(slugs))
	for _, s := range slugs {
		opts = append(opts, huh.NewOption(s, s))
	}
	if err := runSelectFilter("Route", opts, &chosen); err != nil {
		return "", err
	}
	return chosen, nil
}

func pickModelID(rows []modelRow) (string, error) {
	opts := make([]huh.Option[string], 0, len(rows)+1)
	opts = append(opts, huh.NewOption("(list only — no select)", ""))
	for _, r := range rows {
		label := r.ID
		if len(r.AccountIDs) > 0 {
			label = fmt.Sprintf("%s  (%s)", r.ID, strings.Join(r.AccountIDs, ", "))
		}
		opts = append(opts, huh.NewOption(label, r.ID))
	}
	var chosen string
	if err := runSelectFilter("Select model", opts, &chosen); err != nil {
		return "", err
	}
	return chosen, nil
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// silence unused import if build tags change
var _ = time.Second

func cmdCredentials() *cobra.Command {
	return &cobra.Command{
		Use:     "credentials",
		Aliases: []string{"creds", "cred"},
		Short:   "Show local session (api_base, login, sk-gt)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadCreds()
			if err != nil || c.APIKey == "" {
				return fmt.Errorf("not logged in — run: llms login")
			}
			base := c.APIBase
			if base == "" {
				base = apiBase()
			}
			fmt.Println("CREDENTIALS")
			fmt.Printf("  file      %s\n", credsPath())
			fmt.Printf("  login     %s\n", c.Login)
			fmt.Printf("  api_base  %s\n", base)
			fmt.Printf("  api_key   %s\n", c.APIKey)
			fmt.Println("ROUTES")
			fmt.Println("  llms route list")
			fmt.Println("  llms env <slug>           # OPENAI_BASE_URL + this api_key")
			fmt.Println("  llms route key <slug>     # mint a route-scoped sk-gt (shown once)")
			return nil
		},
	}
}

func main() {
	http.DefaultClient = &http.Client{Timeout: 45 * time.Second}
	root := &cobra.Command{
		Use:   "llms",
		Short: "goodtek llms CLI — connect accounts, mint routes, status",
		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			switch cmd.Name() {
			case "version", "completion", "help", "__complete":
				return
			}
			maybePrintUpdateNotice(cmd.Context())
		},
	}
	root.AddCommand(cmdLogin(), cmdLogout(), cmdCredentials(), cmdStatus(), cmdAdd(), cmdAccount(), cmdRoute(), cmdEnv(), cmdModels(), cmdKey(), cmdDisconnect(), cmdPlan(), cmdFree(), cmdUpgrade(), cmdVersion())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
