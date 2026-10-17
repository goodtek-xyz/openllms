//go:build !cloud

package httpserver

import (
	"net/http"
	"time"

	"github.com/goodtek-xyz/openllms/internal/config"
	"github.com/goodtek-xyz/openllms/internal/modelcatalog"
	"github.com/goodtek-xyz/openllms/internal/proxy"
	"github.com/goodtek-xyz/openllms/internal/secrets"
	"github.com/goodtek-xyz/openllms/internal/store"
	"github.com/goodtek-xyz/openllms/internal/userauth"
)

// New constructs the OSS gateway server (no Cloud overlay).
func New(st *store.Store, cfg config.Config, secret secrets.Client) *Server {
	userSecret := cfg.BillingSessionSecret
	if userSecret == "" {
		userSecret = cfg.AdminSessionSecret
	}
	user := &userauth.Manager{
		Secret: []byte(userSecret),
		TTL:    12 * time.Hour,
		Secure: cfg.AdminCookieSecure,
	}
	limiter, affinity := sharedState(cfg)
	return &Server{
		cfg:           cfg,
		store:         st,
		secret:        secret,
		proxy:         &proxy.Engine{Store: st, Secrets: secret, HTTP: proxy.NewUpstreamClient(), Affinity: affinity},
		limit:         limiter,
		models:        modelcatalog.New(secret, &http.Client{Timeout: 12 * time.Second}, modelcatalog.DefaultCacheTTL),
		admin:         disabledAdmin{},
		user:          user,
		mailer:        noopMailer{},
		vendorPending: newVendorPendingStore(),
	}
}
