package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// reauthColumns are added by postgres 000013 / sqlite 000005.
var reauthColumns = []string{"email", "reauth_required_at"}

// roundTripLatest applies every migration, steps the latest one down and up again,
// checking that the latest migration's accounts columns appear, vanish and return.
func roundTripLatest(t *testing.T, fsys embed.FS, dir, migrateURL string, columns func() map[string]bool) {
	t.Helper()
	src, err := iofs.New(fsys, dir)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL)
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	defer m.Close()

	want := func(present bool) {
		t.Helper()
		cols := columns()
		for _, c := range reauthColumns {
			if cols[c] != present {
				t.Fatalf("accounts.%s present=%v, want %v", c, cols[c], present)
			}
		}
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("up: %v", err)
	}
	want(true)
	if err := m.Steps(-1); err != nil {
		t.Fatalf("down one: %v", err)
	}
	want(false)
	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	want(true)
	if v, dirty, err := m.Version(); err != nil || dirty {
		t.Fatalf("version=%d dirty=%v err=%v", v, dirty, err)
	}
}

func TestSQLiteMigrationsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	roundTripLatest(t, migrationSQLiteFS, "migrations_sqlite", "sqlite://"+path, func() map[string]bool {
		conn, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer conn.Close()
		return columnSet(t, conn, `SELECT name FROM pragma_table_info('accounts')`)
	})
}

// TestPostgresMigrationsRoundTrip runs against a throwaway Postgres (e.g. `make dev-db`):
//
//	LLMS_TEST_POSTGRES_URL='postgres://llms:llms@127.0.0.1:54329/llms?sslmode=disable' go test ./internal/db/
//
// It migrates that database, so never point it at a shared one.
func TestPostgresMigrationsRoundTrip(t *testing.T) {
	url := os.Getenv("LLMS_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("LLMS_TEST_POSTGRES_URL not set")
	}
	pool, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	roundTripLatest(t, migrationFS, "migrations", url, func() map[string]bool {
		rows, err := pool.Query(context.Background(),
			`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'accounts'`)
		if err != nil {
			t.Fatalf("columns: %v", err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out[n] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return out
	})
}

func columnSet(t *testing.T, conn *sql.DB, q string) map[string]bool {
	t.Helper()
	rows, err := conn.Query(q)
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[n] = true
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(names)
	t.Logf("accounts columns: %v", names)
	return out
}
