package store_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/goodtek-xyz/openllms/internal/db"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/google/uuid"
)

// testStores returns a migrated SQLite store, plus a Postgres store when LLMS_TEST_POSTGRES_URL is set
// (e.g. `make dev-db` and postgres://llms:llms@127.0.0.1:54329/llms?sslmode=disable).
func testStores(t *testing.T) map[string]*store.Store {
	t.Helper()
	ctx := context.Background()
	out := map[string]*store.Store{}

	url := "sqlite:" + filepath.Join(t.TempDir(), "llms.db")
	if err := db.Migrate(url); err != nil {
		t.Fatalf("sqlite migrate: %v", err)
	}
	sqlDB, err := db.ConnectSQLite(ctx, url)
	if err != nil {
		t.Fatalf("sqlite connect: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	out["sqlite"] = store.NewSQLite(sqlDB)

	if pgURL := os.Getenv("LLMS_TEST_POSTGRES_URL"); pgURL != "" {
		if err := db.Migrate(pgURL); err != nil {
			t.Fatalf("postgres migrate: %v", err)
		}
		pool, err := db.Connect(ctx, pgURL)
		if err != nil {
			t.Fatalf("postgres connect: %v", err)
		}
		t.Cleanup(pool.Close)
		out["postgres"] = store.New(pool)
	}
	return out
}

func TestAccountReauthLifecycle(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			projectID, _, err := st.Bootstrap(ctx, "reauth-"+uuid.NewString()[:8], "default", "cli")
			if err != nil {
				t.Fatal(err)
			}
			rt, err := st.CreateRoute(ctx, projectID, "r-"+uuid.NewString()[:8], "sequential", "m", []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			mk := func(n string) *store.Account {
				a, err := st.CreateAccount(ctx, projectID, "claude", n, "oauth", "/p/"+n, "https://example.invalid")
				if err != nil {
					t.Fatal(err)
				}
				if err := st.AttachAccount(ctx, rt.ID, a.ID, 0, 1); err != nil {
					t.Fatal(err)
				}
				return a
			}
			broken, ok := mk("broken"), mk("ok")

			// Two replicas detect the same incident at once: exactly one wins and notifies.
			var wg sync.WaitGroup
			firsts := make([]bool, 2)
			for i := range firsts {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					f, err := st.MarkAccountReauthRequired(ctx, broken.ID)
					if err != nil {
						t.Error(err)
					}
					firsts[i] = f
				}(i)
			}
			wg.Wait()
			if firsts[0] == firsts[1] {
				t.Fatalf("first flags = %v, want exactly one true", firsts)
			}
			if again, _ := st.MarkAccountReauthRequired(ctx, broken.ID); again {
				t.Fatal("a later detection of the same incident must not count as first")
			}

			routed, err := st.ListRouteAccounts(ctx, rt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(routed) != 1 || routed[0].ID != ok.ID {
				t.Fatalf("routed = %v, want only the healthy account", ids(routed))
			}
			pool, _ := st.ListRoutePool(ctx, rt.ID)
			var flagged *store.Account
			for i := range pool {
				if pool[i].ID == broken.ID {
					flagged = &pool[i]
				}
			}
			if flagged == nil || flagged.ReauthRequiredAt == nil || flagged.Health != "reauth" {
				t.Fatalf("pool entry = %+v, want reauth_required_at set and health reauth", flagged)
			}
			if time.Since(*flagged.ReauthRequiredAt) > time.Hour || time.Until(*flagged.ReauthRequiredAt) > time.Hour {
				t.Fatalf("reauth_required_at = %v, want about now", *flagged.ReauthRequiredAt)
			}

			// Re-link: same (project, vendor, name) → flag, cooldown cleared; email recorded.
			found, err := st.GetAccountByName(ctx, projectID, "claude", "broken")
			if err != nil || found == nil || found.ID != broken.ID {
				t.Fatalf("GetAccountByName = %+v, %v", found, err)
			}
			if missing, err := st.GetAccountByName(ctx, projectID, "claude", "nope"); err != nil || missing != nil {
				t.Fatalf("GetAccountByName(missing) = %+v, %v; want nil, nil", missing, err)
			}
			if err := st.SetCooldown(ctx, broken.ID, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			re, err := st.RelinkAccount(ctx, projectID, broken.ID, "oauth", "/p/broken", "https://example.invalid", "", "owner@example.com")
			if err != nil {
				t.Fatal(err)
			}
			if re.ReauthRequiredAt != nil || re.Health != "ok" || re.Email == nil || *re.Email != "owner@example.com" ||
				re.DefaultModel != broken.DefaultModel {
				t.Fatalf("relinked = %+v", re)
			}
			routed, _ = st.ListRouteAccounts(ctx, rt.ID)
			if len(routed) != 2 {
				t.Fatalf("routed after re-link = %v, want both (cooldown cleared too)", ids(routed))
			}
			// A re-link that learns no email keeps the known one; the vendor's later report replaces it.
			re, _ = st.RelinkAccount(ctx, projectID, broken.ID, "oauth", "/p/broken", "https://example.invalid", "m2", "")
			if re.Email == nil || *re.Email != "owner@example.com" || re.DefaultModel != "m2" {
				t.Fatalf("relink without email = %+v", re)
			}
			_ = st.SetAccountEmail(ctx, broken.ID, "new@example.com")
			_ = st.SetAccountEmail(ctx, ok.ID, "")
			got, _ := st.GetAccount(ctx, projectID, broken.ID)
			gotOK, _ := st.GetAccount(ctx, projectID, ok.ID)
			if *got.Email != "new@example.com" || gotOK.Email != nil {
				t.Fatalf("emails = %v, %v", *got.Email, gotOK.Email)
			}
			// The next incident notifies again.
			if f, _ := st.MarkAccountReauthRequired(ctx, broken.ID); !f {
				t.Fatal("after a re-link a new incident must count as first")
			}
		})
	}
}

func ids(as []store.Account) []uuid.UUID {
	out := make([]uuid.UUID, len(as))
	for i, a := range as {
		out[i] = a.ID
	}
	return out
}
