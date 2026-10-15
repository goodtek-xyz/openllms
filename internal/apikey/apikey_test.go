package apikey_test

import (
	"testing"

	"github.com/goodtek-xyz/openllms/internal/apikey"
)

func TestGenerateAndVerify(t *testing.T) {
	pt, prefix, hash, err := apikey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !apikey.EqualHash(pt, hash) {
		t.Fatal("hash mismatch")
	}
	if !apikey.EqualHash(pt, apikey.Hash(pt)) {
		t.Fatal("Hash roundtrip")
	}
	if prefix == "" || len(prefix) < 6 {
		t.Fatalf("bad prefix %q", prefix)
	}
	if !apikey.EqualHash(pt+"x", hash) == false {
		// EqualHash should be false
	}
	if apikey.EqualHash(pt+"x", hash) {
		t.Fatal("expected mismatch")
	}
}

func TestParseBearer(t *testing.T) {
	pt, _, _, _ := apikey.Generate()
	got, err := apikey.ParseBearer("Bearer " + pt)
	if err != nil || got != pt {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := apikey.ParseBearer(""); err == nil {
		t.Fatal("expected error")
	}
	if _, err := apikey.ParseBearer("Bearer sk-other"); err == nil {
		t.Fatal("expected invalid prefix error")
	}
}
