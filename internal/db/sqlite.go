package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

var errNotSQLite = errors.New("not a sqlite DATABASE_URL")

//go:embed migrations_sqlite/*.sql
var migrationSQLiteFS embed.FS

// ConnectSQLite opens a SQLite database file, enabling foreign keys and WAL.
func ConnectSQLite(ctx context.Context, databaseURL string) (*sql.DB, error) {
	path, err := SQLiteFilePath(databaseURL)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir sqlite dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	// Single writer; keep a small pool.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma foreign_keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma journal_mode: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite ping: %w", err)
	}
	return db, nil
}

// MigrateSQLite applies OSS SQLite migrations (core schema, no Cloud billing).
func MigrateSQLite(databaseURL string) error {
	path, err := SQLiteFilePath(databaseURL)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir sqlite dir: %w", err)
		}
	}
	src, err := iofs.New(migrationSQLiteFS, "migrations_sqlite")
	if err != nil {
		return fmt.Errorf("sqlite migration source: %w", err)
	}
	migrateURL := "sqlite://" + path
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL)
	if err != nil {
		return fmt.Errorf("sqlite migrate init: %w", err)
	}
	defer m.Close()
	if err := up(m, src); err != nil {
		return fmt.Errorf("sqlite migrate up: %w", err)
	}
	return nil
}

// PingSQL pings a database/sql handle.
func PingSQL(ctx context.Context, db *sql.DB) error {
	return db.PingContext(ctx)
}
