package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const DefaultAffinityTTL = time.Hour

// Affinity maps session keys to last successful account (soft sticky).
type Affinity struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]affinityEntry
}

type affinityEntry struct {
	AccountID uuid.UUID
	Expires   time.Time
}

func NewAffinity(ttl time.Duration) *Affinity {
	if ttl <= 0 {
		ttl = DefaultAffinityTTL
	}
	return &Affinity{ttl: ttl, m: map[string]affinityEntry{}}
}

func (a *Affinity) Get(key string) (uuid.UUID, bool) {
	if a == nil || key == "" {
		return uuid.Nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.m[key]
	if !ok || time.Now().After(e.Expires) {
		if ok {
			delete(a.m, key)
		}
		return uuid.Nil, false
	}
	return e.AccountID, true
}

func (a *Affinity) Set(key string, accountID uuid.UUID) {
	if a == nil || key == "" || accountID == uuid.Nil {
		return
	}
	a.mu.Lock()
	a.m[key] = affinityEntry{AccountID: accountID, Expires: time.Now().Add(a.ttl)}
	a.mu.Unlock()
}

// SessionKey resolves the sticky key: body session_id, body llms.session_key, X-Session-Id header,
// prompt_cache_key, then a hash of the first system + user text.
func SessionKey(hdr http.Header, body []byte) string {
	var peek struct {
		SessionID      string `json:"session_id"`
		PromptCacheKey string `json:"prompt_cache_key"`
		LLMS           struct {
			SessionKey string `json:"session_key"`
		} `json:"llms"`
		Messages []sessionMsg    `json:"messages"`
		System   json.RawMessage `json:"system"`
	}
	_ = json.Unmarshal(body, &peek)
	if s := strings.TrimSpace(peek.SessionID); s != "" {
		return truncateKey(s)
	}
	if s := strings.TrimSpace(peek.LLMS.SessionKey); s != "" {
		return truncateKey(s)
	}
	if hdr != nil {
		if s := strings.TrimSpace(hdr.Get("x-session-id")); s != "" {
			return truncateKey(s)
		}
		if s := strings.TrimSpace(hdr.Get("X-Session-Id")); s != "" {
			return truncateKey(s)
		}
	}
	if s := strings.TrimSpace(peek.PromptCacheKey); s != "" {
		return truncateKey(s)
	}
	sys := firstSystemText(peek.System, peek.Messages)
	user := firstUserText(peek.Messages)
	if sys == "" && user == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sys + "\n" + user))
	return "h:" + hex.EncodeToString(sum[:16])
}

type sessionMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func truncateKey(s string) string {
	if len(s) > 128 {
		return s[:128]
	}
	return s
}

func firstSystemText(system json.RawMessage, msgs []sessionMsg) string {
	if len(system) > 0 && string(system) != "null" {
		var s string
		if json.Unmarshal(system, &s) == nil {
			return s
		}
	}
	for _, m := range msgs {
		if m.Role == "system" {
			return contentText(m.Content)
		}
	}
	return ""
}

func firstUserText(msgs []sessionMsg) string {
	for _, m := range msgs {
		if m.Role == "user" {
			return contentText(m.Content)
		}
	}
	return ""
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" || p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}
