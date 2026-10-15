package httpserver

import (
	"log/slog"
	"time"

	"github.com/goodtek-xyz/openllms/internal/config"
	"github.com/goodtek-xyz/openllms/internal/proxy"
	"github.com/goodtek-xyz/openllms/internal/ratelimit"
	"github.com/goodtek-xyz/openllms/internal/valkey"
)

// sharedState builds the per-key rate limiter and the proxy's session affinity: in Valkey when VALKEY_URL is set
// (one count and one sticky account per key across all gateway replicas), else process-local. A bad URL is logged and
// falls back to process-local rather than stopping the gateway; a Valkey outage is handled per call (see valkey).
func sharedState(cfg config.Config) (ratelimit.Limiter, proxy.SessionAffinity) {
	if cfg.ValkeyURL != "" {
		c, err := valkey.New(cfg.ValkeyURL, 250*time.Millisecond)
		if err == nil {
			slog.Info("shared state", "store", "valkey")
			return ratelimit.NewShared(c, cfg.RateLimitPerMin, time.Minute), proxy.NewSharedAffinity(c, proxy.DefaultAffinityTTL)
		}
		slog.Error("VALKEY_URL unusable; rate limit and affinity stay process-local", "err", err)
	}
	return ratelimit.NewMemory(cfg.RateLimitPerMin, time.Minute), proxy.NewAffinity(proxy.DefaultAffinityTTL)
}
