package userauth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/userauth"
)

func TestUserSessionIssueAndRead(t *testing.T) {
	m := &userauth.Manager{Secret: []byte("test-secret"), Secure: false}
	rec := httptest.NewRecorder()
	if err := m.Issue(rec, "alice", "123"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	sess, err := m.Session(req)
	if err != nil || sess.Login != "alice" || sess.GitHubID != "123" {
		t.Fatalf("session: %v %v", sess, err)
	}
}
