package httpserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goodtekxyz/openllms/internal/config"
	"github.com/goodtekxyz/openllms/internal/db"
	"github.com/goodtekxyz/openllms/internal/secrets/file"
	"github.com/goodtekxyz/openllms/internal/store"
	"github.com/goodtekxyz/openllms/internal/vendorauth"
)

// The console sign-in flows exchange the code server-side: the email in that token response came from the vendor
// and is recorded (cleaned); a malformed one is dropped.
func TestServerSideSignInRecordsVendorEmail(t *testing.T) {
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
	sec, _ := file.New(filepath.Join(dir, "secrets"))
	st := store.NewSQLite(sqlDB)
	s := New(st, config.Config{}, sec)
	ctx := context.Background()
	pid, _, err := st.Bootstrap(ctx, "u", "default", "cli")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ email, want string }{
		"claude":  {"owner@example.com", "owner@example.com"},
		"crlf":    {"owner@example.com\r\nBcc: x@evil.test", ""},
		"unknown": {"", ""},
	} {
		in := tokensToAccountInput("claude", name, vendorauth.Tokens{AccessToken: "at", RefreshToken: "rt", Email: tc.email})
		acc, _, code, body := s.createAccountForProjectInner(ctx, pid, in)
		if acc == nil {
			t.Fatalf("%s: %d %v", name, code, body)
		}
		got, _ := st.GetAccount(ctx, pid, acc.ID)
		if (tc.want == "" && got.Email != nil) || (tc.want != "" && (got.Email == nil || *got.Email != tc.want)) {
			t.Fatalf("%s: email = %v, want %q", name, got.Email, tc.want)
		}
	}
}
