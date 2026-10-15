package file_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/secrets/file"
)

func TestPutGetDelete(t *testing.T) {
	root := t.TempDir()
	c, err := file.New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	path := "/llms/proj/accounts/deepseek/default"

	if _, err := c.Get(ctx, path, "credential"); err == nil {
		t.Fatal("expected missing secret error")
	}
	if err := c.Put(ctx, path, "credential", `{"api_key":"sk-test"}`); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, path, "credential")
	if err != nil || got != `{"api_key":"sk-test"}` {
		t.Fatalf("got %q err %v", got, err)
	}
	// Mode should be owner-only when created.
	fp := filepath.Join(root, "llms", "proj", "accounts", "deepseek", "default", "credential.secret")
	fi, err := os.Stat(fp)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("secret file too permissive: %v", fi.Mode())
	}
	if err := c.Delete(ctx, path, "credential"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, path, "credential"); err == nil {
		t.Fatal("expected error after delete")
	}
	if err := c.Delete(ctx, path, "credential"); err != nil {
		t.Fatal(err)
	}
}

func TestRejectPathTraversal(t *testing.T) {
	c, err := file.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Put(ctx, "/a/../../etc", "credential", "x"); err == nil {
		t.Fatal("expected reject ..")
	}
	if err := c.Put(ctx, "/a", "../credential", "x"); err == nil {
		t.Fatal("expected reject bad name")
	}
}

func TestEmptyRoot(t *testing.T) {
	if _, err := file.New(""); err == nil {
		t.Fatal("expected error")
	}
}
