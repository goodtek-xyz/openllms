//go:build !cloud

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/goodtek-xyz/openllms/internal/config"
	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/secrets/file"
)

// openSecrets always uses the local file vault (OSS / self-host build).
func openSecrets(_ context.Context, cfg *config.Config) (secrets.Client, error) {
	fc, err := file.New(cfg.SecretsDir)
	if err != nil {
		return nil, fmt.Errorf("file secrets: %w", err)
	}
	slog.Info("file secrets configured", "dir", cfg.SecretsDir)
	return fc, nil
}
