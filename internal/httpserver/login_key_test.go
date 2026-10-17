package httpserver

import (
	"strings"
	"testing"
)

func TestPlanLoginKey(t *testing.T) {
	for _, c := range []struct {
		name                            string
		created, issued, rotate, device bool
		want                            loginKeyPlan
	}{
		{"first login returns the sign-up key", true, true, false, true, loginKeyPlan{}},
		{"project without a cli key returns the one the store minted", false, true, false, true, loginKeyPlan{}},
		{"re-login on a device with its key keeps everything", false, false, false, false, loginKeyPlan{}},
		{"device without a key gets its own, nothing revoked", false, false, false, true, loginKeyPlan{mintName: "cli:laptop"}},
		{"explicit rotate replaces the cli keys", false, false, true, true, loginKeyPlan{revokeCLI: true, mintName: "cli"}},
	} {
		if got := planLoginKey(c.created, c.issued, c.rotate, c.device, "laptop"); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDeviceKeyName(t *testing.T) {
	for in, want := range map[string]string{
		"laptop":                 "cli:laptop",
		"build-01.local":         "cli:build-01.local",
		" desk \n":               "cli:desk",
		"a b;rm -rf":             "cli:abrm-rf",
		"":                       "cli:device",
		"🙂":                      "cli:device",
		strings.Repeat("h", 254): "cli:device",
	} {
		if got := deviceKeyName(in); got != want {
			t.Errorf("deviceKeyName(%q) = %q, want %q", in, got, want)
		}
	}
}
