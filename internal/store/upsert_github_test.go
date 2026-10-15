package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/db"
	"github.com/goodtek-xyz/openllms/internal/store"
)

func TestUpsertGitHubUserKeepsCLIKeyOnRelogin(t *testing.T) {
	dir := t.TempDir()
	url := "sqlite:" + filepath.Join(dir, "llms.db")
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.ConnectSQLite(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.NewSQLite(sqlDB)
	ctx := context.Background()

	_, projectID, key1, created, err := st.UpsertGitHubUser(ctx, "42", "ops")
	if err != nil || !created || key1 == "" {
		t.Fatalf("first upsert: created=%v key=%q err=%v", created, key1, err)
	}
	n, err := st.CountActiveKeysByName(ctx, projectID, "cli")
	if err != nil || n != 1 {
		t.Fatalf("active cli keys=%d err=%v", n, err)
	}

	_, projectID2, key2, created2, err := st.UpsertGitHubUser(ctx, "42", "ops")
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("re-login should not mark created")
	}
	if key2 != "" {
		t.Fatalf("re-login must not return a new plaintext key, got %q", key2)
	}
	if projectID2 != projectID {
		t.Fatalf("project changed: %s vs %s", projectID, projectID2)
	}
	n, err = st.CountActiveKeysByName(ctx, projectID, "cli")
	if err != nil || n != 1 {
		t.Fatalf("after re-login active cli keys=%d err=%v", n, err)
	}

	// Still authenticates with the original key.
	ac, err := st.LookupByPlaintext(ctx, key1)
	if err != nil || ac.ProjectID != projectID {
		t.Fatalf("original key lookup: ac=%v err=%v", ac, err)
	}
}
