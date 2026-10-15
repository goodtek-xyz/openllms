package httpserver

import (
	"sync"
	"time"

	"github.com/goodtek-xyz/openllms/internal/vendorauth"
	"github.com/google/uuid"
)

const vendorPendingTTL = 15 * time.Minute

type vendorPendingStore struct {
	mu     sync.Mutex
	claude map[string]claudePendingEntry
	codex  map[string]codexPendingEntry
}

type claudePendingEntry struct {
	Verifier string
	State    string
	Expires  time.Time
}

type codexPendingEntry struct {
	Pending vendorauth.CodexPending
	Expires time.Time
}

func newVendorPendingStore() *vendorPendingStore {
	return &vendorPendingStore{
		claude: make(map[string]claudePendingEntry),
		codex:  make(map[string]codexPendingEntry),
	}
}

func (p *vendorPendingStore) putClaude(verifier, state string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	id := uuid.NewString()
	p.claude[id] = claudePendingEntry{
		Verifier: verifier,
		State:    state,
		Expires:  time.Now().Add(vendorPendingTTL),
	}
	return id
}

func (p *vendorPendingStore) getClaude(id string) (claudePendingEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	e, ok := p.claude[id]
	if !ok || time.Now().After(e.Expires) {
		return claudePendingEntry{}, false
	}
	return e, true
}

func (p *vendorPendingStore) putCodex(pending vendorauth.CodexPending) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	id := uuid.NewString()
	p.codex[id] = codexPendingEntry{
		Pending: pending,
		Expires: time.Now().Add(vendorPendingTTL),
	}
	return id
}

func (p *vendorPendingStore) getCodex(id string) (codexPendingEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	e, ok := p.codex[id]
	if !ok || time.Now().After(e.Expires) {
		return codexPendingEntry{}, false
	}
	return e, true
}

func (p *vendorPendingStore) prune() {
	now := time.Now()
	for id, e := range p.claude {
		if now.After(e.Expires) {
			delete(p.claude, id)
		}
	}
	for id, e := range p.codex {
		if now.After(e.Expires) {
			delete(p.codex, id)
		}
	}
}
