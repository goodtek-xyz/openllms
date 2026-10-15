package config

import (
	"context"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
)

func TestHydrateFromSecrets(t *testing.T) {
	mem := memory.New()
	_ = mem.Put(context.Background(), OpsGitHubSecretPath, "client_id", " from-infisical-id ")
	_ = mem.Put(context.Background(), OpsGitHubSecretPath, "client_secret", "from-infisical-secret")

	cfg := Config{}
	cfg.HydrateFromSecrets(context.Background(), mem)
	if cfg.GitHubClientID != "from-infisical-id" || cfg.GitHubClientSecret != "from-infisical-secret" {
		t.Fatalf("got id=%q secret=%q", cfg.GitHubClientID, cfg.GitHubClientSecret)
	}

	cfg2 := Config{GitHubClientID: "env-id", GitHubClientSecret: "env-secret"}
	cfg2.HydrateFromSecrets(context.Background(), mem)
	if cfg2.GitHubClientID != "env-id" || cfg2.GitHubClientSecret != "env-secret" {
		t.Fatalf("env should win: id=%q secret=%q", cfg2.GitHubClientID, cfg2.GitHubClientSecret)
	}

	cfg3 := Config{}
	cfg3.HydrateFromSecrets(context.Background(), nil)
	if cfg3.GitHubClientID != "" || cfg3.GitHubClientSecret != "" {
		t.Fatal("nil secret client should no-op")
	}
}
