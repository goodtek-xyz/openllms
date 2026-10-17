package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatUserSection(t *testing.T) {
	got := formatUserSection("goodtekxyz", "https://llms.goodtek.xyz", false)
	if !strings.Contains(got, "USER\n") || !strings.Contains(got, "goodtekxyz") || !strings.Contains(got, "https://llms.goodtek.xyz") {
		t.Fatalf("unexpected: %q", got)
	}
	got = formatUserSection("", "http://127.0.0.1:8080", true)
	if !strings.Contains(got, "LLMS_API_KEY from env") {
		t.Fatalf("expected env hint: %q", got)
	}
}

func TestClearCreds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfg := filepath.Join(dir, ".config", "llms")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(cfg, "credentials.json")
	if err := os.WriteFile(cred, []byte(`{"login":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearCreds(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cred); !os.IsNotExist(err) {
		t.Fatalf("credentials still present: %v", err)
	}
	if err := clearCreds(); err != nil {
		t.Fatalf("second clear should be ok: %v", err)
	}
}

func TestResolveLoginAPIKey(t *testing.T) {
	key, unchanged, err := resolveLoginAPIKey(map[string]any{
		"api_key": "sk-gt-new", "login": "ops",
	}, "sk-gt-old")
	if err != nil || key != "sk-gt-new" || unchanged {
		t.Fatalf("new key: %q unchanged=%v err=%v", key, unchanged, err)
	}

	key, unchanged, err = resolveLoginAPIKey(map[string]any{
		"api_key_unchanged": true, "login": "ops",
	}, "sk-gt-old")
	if err != nil || key != "sk-gt-old" || !unchanged {
		t.Fatalf("keep local: %q unchanged=%v err=%v", key, unchanged, err)
	}

	_, _, err = resolveLoginAPIKey(map[string]any{
		"api_key_unchanged": true, "login": "ops",
	}, "")
	if err == nil {
		t.Fatal("expected error when no gateway key and no local key")
	}
}

// A device without a local key asks the gateway for a key of its own; with a key it asks for nothing; --rotate-key
// asks only to rotate.
func TestLoginRequestBody(t *testing.T) {
	b := loginRequestBody("gho", false, "", "laptop")
	if b["device_key"] != true || b["device_name"] != "laptop" || b["rotate_cli_key"] != nil {
		t.Fatalf("no local key: %v", b)
	}
	b = loginRequestBody("gho", false, "sk-gt-local", "laptop")
	if _, ok := b["device_key"]; ok || b["rotate_cli_key"] != nil {
		t.Fatalf("local key: %v", b)
	}
	b = loginRequestBody("gho", true, "", "laptop")
	if b["rotate_cli_key"] != true || b["device_key"] != nil {
		t.Fatalf("rotate: %v", b)
	}
}

// A saved key counts only for the gateway it was saved for.
func TestLocalKeyFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if k := localKeyFor("https://llms.example"); k != "" {
		t.Fatalf("no file: %q", k)
	}
	if err := saveCreds(credsFile{APIBase: "https://stage.llms.example/", APIKey: "sk-gt-stage", Login: "ops"}); err != nil {
		t.Fatal(err)
	}
	if k := localKeyFor("https://llms.example"); k != "" {
		t.Fatalf("other gateway's key counted: %q", k)
	}
	if k := localKeyFor("https://stage.llms.example"); k != "sk-gt-stage" {
		t.Fatalf("same gateway: %q", k)
	}
}
