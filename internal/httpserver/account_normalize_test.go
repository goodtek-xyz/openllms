package httpserver

import "testing"

// An Anthropic API key posted as an OAuth access token is stored as an API-key account; OAuth tokens and other
// vendors are left alone.
func TestNormalizeAnthropicAPIKeyPostedAsOAuth(t *testing.T) {
	in := accountCreateInput{Vendor: "claude", AccessToken: " sk-ant-api03-k ", RefreshToken: "rt", AuthType: "oauth"}
	in.normalize()
	if in.AuthType != "api_key" || in.APIKey != "sk-ant-api03-k" || in.AccessToken != "" || in.RefreshToken != "" {
		t.Fatalf("got %+v", in)
	}
	oat := accountCreateInput{Vendor: "claude", AccessToken: "sk-ant-oat01-t"}
	oat.normalize()
	if oat.AuthType != "oauth" || oat.AccessToken != "sk-ant-oat01-t" || oat.APIKey != "" {
		t.Fatalf("oauth token changed: %+v", oat)
	}
	other := accountCreateInput{Vendor: "codex", AccessToken: "sk-ant-api03-k"}
	other.normalize()
	if other.AuthType != "oauth" || other.AccessToken == "" {
		t.Fatalf("non-Anthropic vendor changed: %+v", other)
	}
}
