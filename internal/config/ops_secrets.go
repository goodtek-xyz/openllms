package config

import (
	"context"
	"log/slog"
	"strings"

	"github.com/goodtek-xyz/openllms/internal/secrets"
)

// Infisical path for gateway ops secrets (not customer account credentials).
const OpsGitHubSecretPath = "/llms/_ops/github"

// HydrateFromSecrets fills empty GitHub OAuth fields from Infisical.
// Env values always win. Missing Infisical secrets are non-fatal (logged).
func (c *Config) HydrateFromSecrets(ctx context.Context, secret secrets.Client) {
	if secret == nil {
		return
	}
	if strings.TrimSpace(c.GitHubClientID) == "" {
		if v, err := secret.Get(ctx, OpsGitHubSecretPath, "client_id"); err == nil {
			if v = strings.TrimSpace(v); v != "" {
				c.GitHubClientID = v
				slog.Info("github_client_id loaded from infisical", "path", OpsGitHubSecretPath)
			}
		} else {
			slog.Debug("infisical github client_id", "err", err)
		}
	}
	if strings.TrimSpace(c.GitHubClientSecret) == "" {
		if v, err := secret.Get(ctx, OpsGitHubSecretPath, "client_secret"); err == nil {
			if v = strings.TrimSpace(v); v != "" {
				c.GitHubClientSecret = v
				slog.Info("github_client_secret loaded from infisical", "path", OpsGitHubSecretPath)
			}
		} else {
			slog.Debug("infisical github client_secret", "err", err)
		}
	}
}
