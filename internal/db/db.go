package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

func Migrate(databaseURL string) error {
	if IsSQLite(databaseURL) {
		return MigrateSQLite(databaseURL)
	}
	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	defer m.Close()

	if err := up(m, src); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// up applies the pending migrations. A clean database already at a version newer than every embedded migration was
// migrated by a newer gateway: schema changes are expand/contract (additive first), so this older binary runs on it
// as is. It starts without migrating instead of failing ("no migration found for version N"), which is what lets a
// release be rolled back. A dirty database still fails: it needs a human.
func up(m *migrate.Migrate, src source.Driver) error {
	last, err := lastVersion(src)
	if err != nil {
		return err
	}
	if v, dirty, verr := m.Version(); verr == nil && !dirty && v > last {
		slog.Warn("database schema is newer than this binary's migrations; starting without migrating",
			"db_version", v, "binary_version", last)
		return nil
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// lastVersion returns the newest migration version in src.
func lastVersion(src source.Driver) (uint, error) {
	v, err := src.First()
	if err != nil {
		return 0, fmt.Errorf("first migration: %w", err)
	}
	for {
		next, err := src.Next(v)
		if errors.Is(err, os.ErrNotExist) {
			return v, nil
		}
		if err != nil {
			return 0, fmt.Errorf("next migration after %d: %w", v, err)
		}
		v = next
	}
}

func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	return pool.Ping(ctx)
}
