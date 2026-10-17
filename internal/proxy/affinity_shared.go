package proxy

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SessionAffinity remembers the account that last served a session key (soft sticky, for prompt cache).
type SessionAffinity interface {
	Get(key string) (uuid.UUID, bool)
	Set(key string, accountID uuid.UUID)
}

// AffinityKV is the shared store a SharedAffinity keeps entries in (a *valkey.Client).
type AffinityKV interface {
	Get(ctx context.Context, key string) (string, error)
	SetPX(ctx context.Context, key, value string, ttl time.Duration) error
}

// SharedAffinity keeps session affinity in Valkey so a session sticks to its account whichever gateway replica
// serves the next request. Writes also go to a process-local copy, and every store error is answered from it, so an
// outage only shrinks stickiness back to one replica.
type SharedAffinity struct {
	store  AffinityKV
	local  *Affinity
	ttl    time.Duration
	prefix string
}

func NewSharedAffinity(store AffinityKV, ttl time.Duration) *SharedAffinity {
	if ttl <= 0 {
		ttl = DefaultAffinityTTL
	}
	return &SharedAffinity{store: store, local: NewAffinity(ttl), ttl: ttl, prefix: "llms:aff:"}
}

func (s *SharedAffinity) Get(key string) (uuid.UUID, bool) {
	if s == nil || key == "" {
		return uuid.Nil, false
	}
	ctx := context.Background() // bounded by the store's own request timeout (valkey.DefaultRequestTimeout)
	v, err := s.store.Get(ctx, s.prefix+key)
	if err != nil {
		return s.local.Get(key) // also covers "not set": the local copy is at most as fresh as the store
	}
	id, err := uuid.Parse(v)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

func (s *SharedAffinity) Set(key string, accountID uuid.UUID) {
	if s == nil || key == "" || accountID == uuid.Nil {
		return
	}
	s.local.Set(key, accountID)
	ctx := context.Background() // bounded by the store's own request timeout (valkey.DefaultRequestTimeout)
	_ = s.store.SetPX(ctx, s.prefix+key, accountID.String(), s.ttl)
}
