package vendorauth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmailFromTokenResponses(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	idt := enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"email":"dev@example.com"}`)) + ".sig"
	cases := []struct {
		name, body, want string
	}{
		{"claude account.email_address", `{"access_token":"at","account":{"uuid":"u","email_address":"owner@example.com"}}`, "owner@example.com"},
		{"codex id_token email claim", `{"access_token":"at","id_token":"` + idt + `"}`, "dev@example.com"},
		{"account wins over id_token", `{"access_token":"at","id_token":"` + idt + `","account":{"email_address":"a@example.com"}}`, "a@example.com"},
		{"none", `{"access_token":"at"}`, ""},
		{"garbage id_token", `{"access_token":"at","id_token":"not.a-jwt"}`, ""},
	}
	for _, tc := range cases {
		tok, err := tokensFromOAuthJSON([]byte(tc.body))
		if err != nil || tok.Email != tc.want {
			t.Errorf("%s: email=%q err=%v, want %q", tc.name, tok.Email, err, tc.want)
		}
	}
	if EmailFromIDToken("") != "" {
		t.Error("empty id_token must give no email")
	}
}

func TestRefreshErrorClassification(t *testing.T) {
	const (
		transient = RefreshTransient
		account   = RefreshAccountRejected
		client    = RefreshClientRejected
	)
	cases := []struct {
		status int
		body   string
		want   RefreshFailure
	}{
		{400, `{"error":"invalid_grant","error_description":"Refresh token revoked"}`, account},
		{401, `{"error":"invalid_grant"}`, account},
		{200, `{"error":"invalid_grant"}`, account},
		{401, `{"error":{"message":"Your refresh token has already been used","type":"invalid_request_error","code":"refresh_token_reused"}}`, account},
		{400, `{"error":{"code":"refresh_token_expired"}}`, account},
		{400, `{"error":{"code":"refresh_token_invalidated"}}`, account},
		{401, `{"error":"invalid_client"}`, client},
		{400, `{"error":"unauthorized_client"}`, client},
		{400, `{"error":"unsupported_grant_type"}`, client},
		{400, `{"error":"invalid_scope"}`, client},
		{400, `{"error":"invalid_request"}`, client},
		{400, `not json`, transient},
		{403, `<html>blocked by WAF</html>`, transient},
		{404, `{"error":"something_else"}`, transient},
		{200, `{"error":"temporarily_unavailable"}`, transient},
		{429, `{"error":"invalid_grant"}`, transient},
		{500, `{"error":"invalid_grant"}`, transient},
		{503, ``, transient},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		prev := ClaudeTokenURL
		ClaudeTokenURL, HTTPClient = srv.URL, srv.Client()
		_, err := ClaudeRefresh(context.Background(), "rt")
		ClaudeTokenURL, HTTPClient = prev, nil
		srv.Close()
		var re *RefreshError
		if !errors.As(err, &re) {
			t.Fatalf("%d %s: err %v is not a RefreshError", tc.status, tc.body, err)
		}
		if got, _ := RefreshFailureOf(err); got != tc.want {
			t.Errorf("%d %s: failure=%d, want %d", tc.status, tc.body, got, tc.want)
		}
	}
	// A network error is not a token-endpoint answer: transient.
	prev := ClaudeTokenURL
	ClaudeTokenURL = "http://127.0.0.1:1"
	defer func() { ClaudeTokenURL = prev }()
	_, err := ClaudeRefresh(context.Background(), "rt")
	if got, re := RefreshFailureOf(err); err == nil || got != RefreshTransient || re != nil {
		t.Fatalf("network error: %v → %d", err, got)
	}
}

func TestCleanEmail(t *testing.T) {
	for in, want := range map[string]string{
		"dev@example.com":                     "dev@example.com",
		"  dev@example.com ":                  "dev@example.com",
		"Dev <dev@example.com>":               "",
		"dev@example.com\r\nBcc: x@evil.test": "",
		"dev@example.com\nBcc: x@evil.test":   "",
		"not an address":                      "",
		"":                                    "",
	} {
		if got := CleanEmail(in); got != want {
			t.Errorf("CleanEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
