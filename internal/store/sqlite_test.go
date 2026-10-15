package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/db"
	"github.com/goodtek-xyz/openllms/internal/store"
)

func TestSQLiteBootstrapLookupRouteAccount(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "llms.db")
	url := "sqlite:" + dbPath

	if err := db.Migrate(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.ConnectSQLite(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	st := store.NewSQLite(sqlDB)
	ctx := context.Background()

	projectID, key, err := st.Bootstrap(ctx, "oss-user", "default", "cli")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if key == "" {
		t.Fatal("empty api key")
	}

	ac, err := st.LookupByPlaintext(ctx, key)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if ac.ProjectID != projectID {
		t.Fatalf("project mismatch: %s vs %s", ac.ProjectID, projectID)
	}

	rt, err := st.CreateRoute(ctx, projectID, "default", "sequential", "gpt-4o-mini", []byte(`{}`))
	if err != nil {
		t.Fatalf("create route: %v", err)
	}

	acc, err := st.CreateAccount(ctx, projectID, "openai", "main", "api_key", "/projects/"+projectID.String()+"/openai/main", "https://api.openai.com/v1")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := st.AttachAccount(ctx, rt.ID, acc.ID, 0, 1); err != nil {
		t.Fatalf("attach: %v", err)
	}

	pool, err := st.ListRoutePool(ctx, rt.ID)
	if err != nil {
		t.Fatalf("list pool: %v", err)
	}
	if len(pool) != 1 || pool[0].ID != acc.ID {
		t.Fatalf("pool=%v", pool)
	}

	keyID := ac.KeyID
	if err := st.InsertUsage(ctx, projectID, &rt.ID, &acc.ID, &keyID, "gpt-4o-mini", 200, 12, 3, 5, ""); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if err := st.InsertUsage(ctx, projectID, &rt.ID, nil, &keyID, "", 503, 0, 0, 0, "no_usable_accounts"); err != nil {
		t.Fatalf("usage nil account: %v", err)
	}

	until := time.Now().UTC().Add(30 * time.Second)
	if err := st.SetCooldown(ctx, acc.ID, until); err != nil {
		t.Fatalf("cooldown: %v", err)
	}
	gotUntil, ok, err := st.EarliestRouteCooldown(ctx, rt.ID)
	if err != nil || !ok {
		t.Fatalf("earliest cooldown ok=%v err=%v", ok, err)
	}
	if gotUntil.Before(time.Now()) {
		t.Fatalf("earliest=%v", gotUntil)
	}
	usable, err := st.ListRouteAccounts(ctx, rt.ID)
	if err != nil {
		t.Fatalf("list usable: %v", err)
	}
	if len(usable) != 0 {
		t.Fatalf("expected cooled account excluded, got %d", len(usable))
	}

	n, err := st.RefreshQuotasFromUsage(ctx)
	if err != nil {
		t.Fatalf("refresh quotas: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 quota row, got %d", n)
	}

	users, err := st.ListAdminUsers(ctx)
	if err != nil {
		t.Fatalf("admin users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("users=%d", len(users))
	}

	adminRoutes, err := st.ListAdminRoutes(ctx)
	if err != nil {
		t.Fatalf("admin routes: %v", err)
	}
	if len(adminRoutes) != 1 {
		t.Fatalf("admin routes=%d", len(adminRoutes))
	}
	if adminRoutes[0]["slug"] != "default" {
		t.Fatalf("slug=%v", adminRoutes[0]["slug"])
	}
	adminPool, ok := adminRoutes[0]["pool"].([]string)
	if !ok || len(adminPool) != 1 || adminPool[0] != "openai:main" {
		t.Fatalf("pool=%v", adminRoutes[0]["pool"])
	}

	if err := st.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if st.Dialect() != store.DialectSQLite {
		t.Fatalf("dialect=%v", st.Dialect())
	}
}
