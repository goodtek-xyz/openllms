package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/db"
	"github.com/goodtek-xyz/openllms/internal/githubauth"
	"github.com/goodtek-xyz/openllms/internal/store"
)

// The GitHub login path end to end on a real store: a second device without a local key gets a key of its own and
// every key that worked before keeps working; a device that has its key mints nothing; only --rotate-key revokes.
func TestGitHubLoginDeviceKeys(t *testing.T) {
	ctx := context.Background()
	url := "sqlite:" + filepath.Join(t.TempDir(), "llms.db")
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.ConnectSQLite(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.NewSQLite(sqlDB)

	const ghID, login = 4242, "octo"
	_, projectID, firstKey, created, err := st.UpsertGitHubUser(ctx, "4242", login) // sign-up on the first device
	if err != nil || !created || firstKey == "" {
		t.Fatalf("sign-up: created=%v key=%v err=%v", created, firstKey != "", err)
	}
	appKey, err := st.CreateKey(ctx, projectID, "app-prod", nil) // a key an app uses
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{store: st, githubUser: func(context.Context, string) (*githubauth.User, error) {
		return &githubauth.User{ID: ghID, Login: login}, nil
	}}
	call := func(body map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		s.handleGitHubAuth(w, httptest.NewRequest(http.MethodPost, "/control/v1/auth/github", bytes.NewReader(b)))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	works := func(key string) bool {
		t.Helper()
		ac, err := st.LookupByPlaintext(ctx, key)
		return err == nil && ac != nil && ac.ProjectID == projectID
	}
	active := func() map[string]int {
		keys, err := st.ListKeys(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		n := map[string]int{}
		for _, k := range keys {
			if k.RevokedAt == nil {
				n[k.Name]++
			}
		}
		return n
	}

	// Second device, no local key.
	out := call(map[string]any{"access_token": "gho", "device_key": true, "device_name": "laptop"})
	devKey, _ := out["api_key"].(string)
	if devKey == "" || out["key_name"] != "cli:laptop" || out["api_key_unchanged"] != nil {
		t.Fatalf("device login: %v", out)
	}
	if !works(devKey) || !works(firstKey) || !works(appKey) {
		t.Fatalf("after device login: device=%v first=%v app=%v", works(devKey), works(firstKey), works(appKey))
	}

	// The same device logs in again with its key: nothing minted, nothing revoked.
	out = call(map[string]any{"access_token": "gho"})
	if out["api_key"] != nil || out["api_key_unchanged"] != true {
		t.Fatalf("re-login with a local key: %v", out)
	}
	if got := active(); got["cli"] != 1 || got["cli:laptop"] != 1 || got["app-prod"] != 1 {
		t.Fatalf("active keys after re-login: %v", got)
	}

	// Explicit rotate: only the "cli" keys are replaced.
	out = call(map[string]any{"access_token": "gho", "rotate_cli_key": true})
	rotated, _ := out["api_key"].(string)
	if rotated == "" || works(firstKey) || !works(rotated) || !works(devKey) || !works(appKey) {
		t.Fatalf("rotate: new=%v old cli=%v device=%v app=%v", works(rotated), works(firstKey), works(devKey), works(appKey))
	}
}
