package secrets_test

import (
	"testing"

	"github.com/goodtek-xyz/openllms/internal/secrets"
)

func TestBearerToken(t *testing.T) {
	if (secrets.CredentialJSON{APIKey: "a"}).BearerToken() != "a" {
		t.Fatal("api_key")
	}
	if (secrets.CredentialJSON{AccessToken: "b"}).BearerToken() != "b" {
		t.Fatal("access_token")
	}
	// Stale api_key must not shadow a live OAuth access_token.
	if (secrets.CredentialJSON{APIKey: "sk-stale", AccessToken: "eyJlive"}).BearerToken() != "eyJlive" {
		t.Fatal("access_token must win when both present")
	}
}
