package httpserver_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goodtekxyz/openllms/internal/config"
	"github.com/goodtekxyz/openllms/internal/db"
	"github.com/goodtekxyz/openllms/internal/httpserver"
	"github.com/goodtekxyz/openllms/internal/secrets/file"
	"github.com/goodtekxyz/openllms/internal/store"
	"github.com/goodtekxyz/openllms/internal/vendor"
	"github.com/google/uuid"
)

// Linking an account whose (vendor, name) already exists replaces its login and puts it back into routing. Tokens a
// client posts are unverified: neither an "email" field nor an id_token in the body sets the account email (it is
// where reauth notifications go); API-key accounts have none either.
func TestRelinkClearsReauthAndRecordsEmail(t *testing.T) {
	dir := t.TempDir()
	dbURL := "sqlite:" + filepath.Join(dir, "llms.db")
	if err := db.Migrate(dbURL); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.ConnectSQLite(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sec, _ := file.New(filepath.Join(dir, "secrets"))
	st := store.NewSQLite(sqlDB)
	gw := httptest.NewServer(httpserver.New(st, config.Config{BootstrapToken: "boot"}, sec).Router())
	t.Cleanup(gw.Close)

	var boot map[string]any
	if s := doJSON(t, http.MethodPost, gw.URL+"/control/v1/bootstrap", "boot", "", map[string]string{"login": "u", "project_name": "default", "key_name": "cli"}, &boot); s != http.StatusCreated {
		t.Fatalf("bootstrap %d %v", s, boot)
	}
	auth := "Bearer " + boot["api_key"].(string)
	enc := base64.RawURLEncoding.EncodeToString
	idt := enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"email":"dev@example.com"}`)) + ".sig"

	link := func(at string) (int, map[string]any) {
		var out map[string]any
		s := doJSON(t, http.MethodPost, gw.URL+"/control/v1/accounts", "", auth, map[string]any{
			"vendor": "codex", "name": "work", "access_token": at, "refresh_token": "rt", "id_token": idt,
			"email": "victim@example.com",
		}, &out)
		return s, out
	}
	code, created := link("at-1")
	if code != http.StatusCreated || created["relinked"] != false {
		t.Fatalf("create %d %v", code, created)
	}
	accID := uuid.MustParse(created["id"].(string))
	if first, err := st.MarkAccountReauthRequired(context.Background(), accID); err != nil || !first {
		t.Fatalf("mark: %v %v", first, err)
	}
	accounts := func() map[string]map[string]any {
		var out map[string]any
		if s := doJSON(t, http.MethodGet, gw.URL+"/control/v1/accounts", "", auth, nil, &out); s != http.StatusOK {
			t.Fatalf("list %d", s)
		}
		m := map[string]map[string]any{}
		for _, raw := range out["accounts"].([]any) {
			a := raw.(map[string]any)
			m[a["name"].(string)] = a
		}
		return m
	}
	if a := accounts()["work"]; a["health"] != "reauth" || a["reauth_required_at"] == nil || a["email"] != nil {
		t.Fatalf("flagged account = %v", a)
	}

	code, relinked := link("at-2")
	if code != http.StatusOK || relinked["relinked"] != true || relinked["id"] != created["id"] {
		t.Fatalf("relink %d %v", code, relinked)
	}
	if a := accounts()["work"]; a["health"] != "ok" || a["reauth_required_at"] != nil {
		t.Fatalf("relinked account = %v", a)
	}
	raw, _ := sec.Get(context.Background(), vendor.InfisicalPath(boot["project_id"].(string), "codex", "work"), vendor.SecretName)
	if !strings.Contains(raw, "at-2") {
		t.Fatalf("secret not replaced: %s", raw)
	}

	var key map[string]any
	if s := doJSON(t, http.MethodPost, gw.URL+"/control/v1/accounts", "", auth, map[string]any{
		"vendor": "openai", "name": "key", "api_key": "sk-x", "email": "ignored@example.com",
	}, &key); s != http.StatusCreated {
		t.Fatalf("api key account %d %v", s, key)
	}
	if a := accounts()["key"]; a["email"] != nil {
		t.Fatalf("api-key account email = %v, want none", a["email"])
	}
}
