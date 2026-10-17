package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSHA256SUMS(t *testing.T) {
	h := strings.Repeat("ab", 32)
	sums, err := parseSHA256SUMS(strings.NewReader(h + "  llms_darwin_arm64\n" + strings.ToUpper(h) + " *llms_linux_amd64\n\n"))
	if err != nil || sums["llms_darwin_arm64"] != h || sums["llms_linux_amd64"] != h {
		t.Fatalf("sums=%v err=%v", sums, err)
	}
	if _, err := parseSHA256SUMS(strings.NewReader("<html>not found</html>\n")); err == nil {
		t.Fatal("an HTML body must not parse as checksums")
	}
}

// fakeBinary is a script that answers `version` like the CLI does.
func fakeBinary(version string) []byte {
	return []byte("#!/bin/sh\necho \"llms " + version + " (abc) test/test\"\n")
}

func distServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/dist/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// The file that runs is replaced in place after the checksum and a test run; a bad checksum or a binary that does
// not run leaves it untouched.
func TestReplaceBinary(t *testing.T) {
	newBin := fakeBinary("v0.2.0")
	srv := distServer(t, map[string][]byte{"llms_test_test": newBin, "broken": []byte("#!/bin/sh\nexit 3\n")})
	defer srv.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "llms")
	old := fakeBinary("v0.1.0")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := replaceBinary(ctx, srv.URL, "llms_test_test", strings.Repeat("0", 64), target); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("bad checksum: %v", err)
	}
	if _, err := replaceBinary(ctx, srv.URL, "broken", sum([]byte("#!/bin/sh\nexit 3\n")), target); err == nil {
		t.Fatal("a binary that fails `version` must not be installed")
	}
	if b, _ := os.ReadFile(target); string(b) != string(old) {
		t.Fatal("target changed after failed updates")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".llms-update-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}

	v, err := replaceBinary(ctx, srv.URL, "llms_test_test", sum(newBin), target)
	if err != nil || !strings.Contains(v, "v0.2.0") {
		t.Fatalf("update: v=%q err=%v", v, err)
	}
	if b, _ := os.ReadFile(target); string(b) != string(newBin) {
		t.Fatal("target not replaced")
	}
	if st, _ := os.Stat(target); st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("replaced binary not executable: %v", st.Mode())
	}
}

func TestDistMissingIsNoDist(t *testing.T) {
	srv := distServer(t, map[string][]byte{})
	defer srv.Close()
	if _, err := distGet(context.Background(), srv.URL, "SHA256SUMS"); !errors.Is(err, errNoDist) {
		t.Fatalf("404 dist: %v", err)
	}
	if _, err := fetchLatestVersion(context.Background(), srv.URL); !errors.Is(err, errNoDist) {
		t.Fatalf("404 VERSION: %v", err)
	}
	if err := noDistHelp(srv.URL, errNoDist); err == nil || !strings.Contains(err.Error(), "go install") {
		t.Fatalf("help: %v", err)
	}
}

// An older llms earlier in PATH is reported; none when PATH runs this one.
func TestShadowingLLMS(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(d, "llms"), fakeBinary("v0"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	self, _ := filepath.EvalSymlinks(filepath.Join(b, "llms"))
	t.Setenv("PATH", a+string(os.PathListSeparator)+b)
	if got := shadowingLLMS(self); got == "" || !strings.HasPrefix(got, mustEval(t, a)) {
		t.Fatalf("shadow: %q", got)
	}
	t.Setenv("PATH", b+string(os.PathListSeparator)+a)
	if got := shadowingLLMS(self); got != "" {
		t.Fatalf("no shadow expected: %q", got)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
