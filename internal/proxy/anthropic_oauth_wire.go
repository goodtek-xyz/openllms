package proxy

import (
	"encoding/json"
	"strings"
)

// Anthropic OAuth (sk-ant-oat) Messages API requires Claude Code wire signals.
// Undocumented; required by working third-party OAuth clients. Not full CLIProxy cloak.
// Kept to the minimum the API needs to accept OAuth requests.
const (
	claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."
	// Pinned for Messages + quota/models compatibility; bump when upstream rejects version.
	claudeCodeUserAgent     = "claude-code/2.1.72"
	claudeCodeBillingHeader = "x-anthropic-billing-header: cc_version=2.1.72; cc_entrypoint=cli; cch=llms;"
	claudeCodeBillingPrefix = "x-anthropic-billing-header:"
)

func textSystemBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func isClaudeCodeIdentityText(text string) bool {
	return strings.TrimSpace(text) == claudeCodeIdentity
}

func isClaudeCodeBillingText(text string) bool {
	t := strings.TrimLeft(text, " \t\n\r")
	return strings.HasPrefix(t, claudeCodeBillingPrefix)
}

// ensureClaudeOAuthWire reshapes a Messages JSON body so Anthropic accepts OAuth inference.
// Prepends billing + identity as separate system blocks; keeps caller system as later blocks.
// Idempotent. Does not concatenate custom text into the identity string (that yields opaque 429).
func ensureClaudeOAuthWire(body []byte) []byte {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return body
	}

	caller := collectCallerSystemBlocks(root["system"])
	filtered := make([]map[string]any, 0, len(caller)+2)
	for _, b := range caller {
		text, _ := b["text"].(string)
		if isClaudeCodeIdentityText(text) || isClaudeCodeBillingText(text) {
			continue
		}
		filtered = append(filtered, b)
	}

	system := make([]map[string]any, 0, len(filtered)+2)
	system = append(system, textSystemBlock(claudeCodeBillingHeader), textSystemBlock(claudeCodeIdentity))
	system = append(system, filtered...)
	root["system"] = system

	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

func collectCallerSystemBlocks(system any) []map[string]any {
	if system == nil {
		return nil
	}
	switch s := system.(type) {
	case string:
		t := strings.TrimSpace(s)
		if t == "" {
			return nil
		}
		// If client concatenated identity + custom in one string, split so identity stays exact.
		if strings.HasPrefix(t, claudeCodeIdentity) {
			rest := strings.TrimSpace(strings.TrimPrefix(t, claudeCodeIdentity))
			out := []map[string]any{textSystemBlock(claudeCodeIdentity)}
			if rest != "" {
				out = append(out, textSystemBlock(rest))
			}
			return out
		}
		return []map[string]any{textSystemBlock(t)}
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, item := range s {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			text, _ := m["text"].(string)
			if typ != "" && !strings.EqualFold(typ, "text") {
				out = append(out, m)
				continue
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			if strings.HasPrefix(text, claudeCodeIdentity) && text != claudeCodeIdentity {
				rest := strings.TrimSpace(strings.TrimPrefix(text, claudeCodeIdentity))
				out = append(out, textSystemBlock(claudeCodeIdentity))
				if rest != "" {
					out = append(out, textSystemBlock(rest))
				}
				continue
			}
			out = append(out, textSystemBlock(text))
		}
		return out
	default:
		return nil
	}
}
