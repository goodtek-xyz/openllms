package proxy_test

import (
	"encoding/json"
	"testing"

	"github.com/goodtek-xyz/openllms/internal/proxy"
	"github.com/goodtek-xyz/openllms/internal/store"
)

func TestResolveOutboundModelAccountDefault(t *testing.T) {
	acc := &store.Account{Vendor: "codex", DefaultModel: "gpt-5.6-luna"}
	if got := proxy.ResolveOutboundModel("", acc, "gpt-reserve"); got != "gpt-5.6-luna" {
		t.Fatalf("account default: %q", got)
	}
	if got := proxy.ResolveOutboundModel("", acc, ""); got != "gpt-5.6-luna" {
		t.Fatalf("account default no route: %q", got)
	}
	acc.DefaultModel = ""
	if got := proxy.ResolveOutboundModel("", acc, "gpt-reserve"); got != "gpt-reserve" {
		t.Fatalf("route default: %q", got)
	}
}

func TestResolveOutboundModelRequestOverride(t *testing.T) {
	acc := &store.Account{Vendor: "codex", DefaultModel: "gpt-5.6-luna"}
	if got := proxy.ResolveOutboundModel("gpt-reserve", acc, ""); got != "gpt-reserve" {
		t.Fatalf("compatible override: %q", got)
	}
}

func TestResolveOutboundModelIncompatibleFallsToAccountDefault(t *testing.T) {
	claude := &store.Account{Vendor: "claude", DefaultModel: "claude-sonnet-4-6"}
	got := proxy.ResolveOutboundModel("gpt-5.6-luna", claude, "")
	if got != "claude-sonnet-4-6" {
		t.Fatalf("want account default on family mismatch, got %q", got)
	}
}

func TestApplyAccountModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`)
	claude := &store.Account{Vendor: "claude", DefaultModel: "claude-sonnet-4-6"}
	out := proxy.ApplyAccountModel(body, claude, "")
	var peek struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(out, &peek); err != nil {
		t.Fatal(err)
	}
	if peek.Model != "claude-sonnet-4-6" {
		t.Fatalf("model=%q body=%s", peek.Model, out)
	}
}

func TestResolveOutboundModelSkipsCrossVendorRouteDefault(t *testing.T) {
	claude := &store.Account{Vendor: "claude"}
	// No account default: a gpt-* route default must not be sent to Anthropic.
	if got := proxy.ResolveOutboundModel("gpt-5.6-luna", claude, "gpt-5.6-luna"); got == "gpt-5.6-luna" && proxy.AccountServesModel(claude, got) {
		t.Fatalf("unexpected servable pick %q", got)
	}
	if got := proxy.ResolveOutboundModel("gpt-5.6-luna", claude, "claude-sonnet-4-6"); got != "claude-sonnet-4-6" {
		t.Fatalf("compatible route default: %q", got)
	}
}

func TestFilterServableAccountsDropsSeatWithoutCompatibleModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna"}`)
	accs := []store.Account{
		{Vendor: "claude", Name: "no-default"},
		{Vendor: "claude", Name: "with-default", DefaultModel: "claude-sonnet-4-6"},
		{Vendor: "codex", Name: "codex"},
	}
	got := proxy.FilterServableAccounts(accs, body, "gpt-5.6-luna")
	if len(got) != 2 || got[0].Name != "with-default" || got[1].Name != "codex" {
		t.Fatalf("filtered=%+v", got)
	}
	// No servable seat at all → keep original so the upstream error surfaces.
	only := []store.Account{{Vendor: "claude", Name: "no-default"}}
	if got := proxy.FilterServableAccounts(only, body, "gpt-5.6-luna"); len(got) != 1 {
		t.Fatalf("expected original list, got %+v", got)
	}
}
