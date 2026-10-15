package memory_test

import (
	"context"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/secrets/memory"
)

func TestPutGetDelete(t *testing.T) {
	c := memory.New()
	ctx := context.Background()
	if _, err := c.Get(ctx, "/p", "credential"); err == nil {
		t.Fatal("expected error for missing secret")
	}
	if err := c.Put(ctx, "/p", "credential", "v1"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "/p", "credential")
	if err != nil || got != "v1" {
		t.Fatalf("got %q err %v", got, err)
	}
	if err := c.Delete(ctx, "/p", "credential"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "/p", "credential"); err == nil {
		t.Fatal("expected error after delete")
	}
	// Deleting an already-missing key must not error.
	if err := c.Delete(ctx, "/p", "credential"); err != nil {
		t.Fatalf("delete of missing key should be nil, got %v", err)
	}
}
