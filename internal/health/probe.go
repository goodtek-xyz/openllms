package health

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/vendor"
	"github.com/google/uuid"
)

type Store interface {
	ListAllAccounts(ctx context.Context) ([]store.Account, error)
	MarkAccountHealthy(ctx context.Context, accountID uuid.UUID) error
	MarkAccountUnhealthy(ctx context.Context, accountID uuid.UUID, until time.Time) error
}

type Prober struct {
	Store   Store
	Secrets secrets.Client
	HTTP    *http.Client
	CoolFor time.Duration
}

func (p *Prober) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 12 * time.Second}
}

func (p *Prober) cool() time.Duration {
	if p.CoolFor > 0 {
		return p.CoolFor
	}
	return 60 * time.Second
}

// ProbeOnce checks each account. API-key OpenAI-compatible seats get a real
// chat/completions ping; OAuth seats use authenticated /models with strict 2xx.
func (p *Prober) ProbeOnce(ctx context.Context) (ok, bad int, err error) {
	if p.Store == nil || p.Secrets == nil {
		return 0, 0, nil
	}
	accounts, err := p.Store.ListAllAccounts(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, a := range accounts {
		if err := ctx.Err(); err != nil {
			return ok, bad, err
		}
		if probeAccount(ctx, p.client(), p.Secrets, &a) {
			_ = p.Store.MarkAccountHealthy(ctx, a.ID)
			ok++
		} else {
			_ = p.Store.MarkAccountUnhealthy(ctx, a.ID, time.Now().Add(p.cool()))
			bad++
		}
	}
	return ok, bad, nil
}

func (p *Prober) Start(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 2 * time.Minute
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			_, _, _ = p.ProbeOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func probeAccount(ctx context.Context, client *http.Client, sec secrets.Client, a *store.Account) bool {
	raw, err := sec.Get(ctx, a.InfisicalPath, vendor.SecretName)
	if err != nil {
		return false
	}
	var cred secrets.CredentialJSON
	if json.Unmarshal([]byte(raw), &cred) != nil {
		return false
	}
	token := cred.BearerToken()
	if token == "" {
		return false
	}
	base := strings.TrimRight(a.BaseURL, "/")
	if base == "" {
		base = vendor.DefaultBaseURLFor(a.Vendor, a.AuthType)
	}

	// Prefer a real chat completions probe for OpenAI-compatible API-key seats.
	if !strings.EqualFold(a.AuthType, "oauth") && supportsChatProbe(a.Vendor) {
		return probeChatCompletions(ctx, client, base, token)
	}
	return probeModels(ctx, client, a, cred, base, token)
}

func supportsChatProbe(vendorName string) bool {
	switch strings.ToLower(strings.TrimSpace(vendorName)) {
	case "deepseek", "openai", "codex", "kimi", "moonshot", "glm":
		return true
	default:
		return false
	}
}

func probeChatCompletions(ctx context.Context, client *http.Client, base, token string) bool {
	url := strings.TrimRight(base, "/") + "/chat/completions"
	body, _ := json.Marshal(map[string]any{
		"model":      "gpt-4o-mini",
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	// 200 = healthy. 404 model-not-found still proves auth works — treat as ok.
	// 401/403 = dead key. 429 = auth ok but limited — still usable.
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return false
	}
	return res.StatusCode > 0 && res.StatusCode < 500
}

func probeModels(ctx context.Context, client *http.Client, a *store.Account, cred secrets.CredentialJSON, base, token string) bool {
	url := strings.TrimRight(base, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if strings.EqualFold(a.AuthType, "oauth") {
		v := strings.ToLower(a.Vendor)
		if (v == "codex" || v == "openai") && cred.ChatGPTAccountID != "" {
			req.Header.Set("ChatGPT-Account-Id", cred.ChatGPTAccountID)
		}
		if v == "claude" || v == "anthropic" {
			req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	// OAuth Codex/Claude often expose no usable /models (404/405/5xx) while
	// Responses/Messages chat still works. Only treat auth death as unhealthy;
	// otherwise keep the seat in the pool (chat soft-fail still cooldowns).
	if strings.EqualFold(a.AuthType, "oauth") {
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return false
		}
		return res.StatusCode > 0 && res.StatusCode < 500
	}
	// Strict for API-key seats: only 2xx counts. 404 must not look "ok".
	return res.StatusCode >= 200 && res.StatusCode < 300
}
