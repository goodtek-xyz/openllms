package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// A database migrated by a newer release (version N+1) must not stop this binary (newest migration N) from starting:
// that is what makes rolling back a release possible. A dirty database still fails.
func TestMigrateAcceptsNewerDatabase(t *testing.T) {
	src, err := iofs.New(migrationSQLiteFS, "migrations_sqlite")
	if err != nil {
		t.Fatal(err)
	}
	last, err := lastVersion(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "llms.db")
	url := "sqlite:" + path
	if err := Migrate(url); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	setVersion := func(v uint, dirty bool) {
		t.Helper()
		if _, err := conn.Exec(`UPDATE schema_migrations SET version = ?, dirty = ?`, v, dirty); err != nil {
			t.Fatal(err)
		}
	}

	setVersion(last+1, false)
	if err := Migrate(url); err != nil {
		t.Fatalf("Migrate on a newer clean database = %v, want nil", err)
	}
	var v uint
	if err := conn.QueryRow(`SELECT version FROM schema_migrations`).Scan(&v); err != nil || v != last+1 {
		t.Fatalf("version after start = %d (%v), want %d left untouched", v, err, last+1)
	}

	setVersion(last+1, true)
	if err := Migrate(url); err == nil || !strings.Contains(err.Error(), "Dirty") {
		t.Fatalf("Migrate on a dirty database = %v, want the dirty error", err)
	}
}

// Same against Postgres when LLMS_TEST_POSTGRES_URL is set.
func TestPostgresMigrateAcceptsNewerDatabase(t *testing.T) {
	url := os.Getenv("LLMS_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("LLMS_TEST_POSTGRES_URL not set")
	}
	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	last, err := lastVersion(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(url); err != nil {
		t.Fatal(err)
	}
	pool, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET version = $1, dirty = false`, last+1); err != nil {
		t.Fatal(err)
	}
	defer func() { // runs before pool.Close: put the shared test database back to this binary's version
		if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET version = $1, dirty = false`, last); err != nil {
			t.Errorf("reset version: %v", err)
		}
	}()
	if err := Migrate(url); err != nil {
		t.Fatalf("Migrate on a newer clean database = %v, want nil", err)
	}
}
