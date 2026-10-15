package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is runtime configuration for llms-gateway.
type Config struct {
	HTTPAddr    string
	DatabaseURL string

	BootstrapToken string

	GitHubClientID     string
	GitHubClientSecret string // web OAuth code exchange; device flow (CLI) does not need it
	RateLimitPerMin    int
	// ValkeyURL (valkey:// / rediss://…): share rate-limit counters and session affinity across gateway replicas.
	// Empty: both stay process-local.
	ValkeyURL string

	// Infisical (managed secret store); validated when features need them.
	InfisicalSiteURL      string
	InfisicalProjectID    string
	InfisicalEnvironment  string
	InfisicalClientID     string
	InfisicalClientSecret string
	// SecretsDir is the local file-vault root when Infisical is not configured (OSS / self-host).
	SecretsDir string

	// Process-local TTL caches (no shared cache). 0 disables that cache.
	SecretCacheTTL time.Duration
	AuthCacheTTL   time.Duration
	RouteCacheTTL  time.Duration

	// Admin dashboard (GitHub device login + allowlist).
	AdminGitHubLogins  string
	AdminSessionSecret string
	AdminCookieSecure  bool

	// Signup / re-auth notification email (Resend or SMTP). Defaults per edition: defaultNotifyEmail*.
	ResendAPIKey    string
	SMTPHost        string
	SMTPPort        string
	SMTPUser        string
	SMTPPass        string
	NotifyEmailTo   string
	NotifyEmailFrom string
	// Optional Discord webhook (channel → Integrations → Webhooks) for ops alerts; empty = no Discord.
	NotifyDiscordWebhookURL string

	// Public site URL of this deployment (used for OAuth callbacks, webhooks and links).
	PublicBaseURL string

	// GAMeasurementID is the public Google Analytics measurement ID (G-…).
	// Empty disables the gtag snippet. Set only on production hosts.
	GAMeasurementID string

	// DistDir is a host directory of prebuilt CLI binaries served at /dist/*
	// (llms_linux_arm64, VERSION, …). Empty disables the route.
	DistDir string

	// Billing — Polar (card) + Unifi Pay (crypto).
	BillingMock               bool
	BillingEnforce            bool // when true, create account/route/key requires entitlement
	Polar                     billingPolar
	Unifi                     billingUnifi
	BillingCheckoutSuccessURL string
	BillingCheckoutCancelURL  string
	BillingPortalReturnURL    string
	BillingSessionSecret      string // user billing cookie; falls back to ADMIN_SESSION_SECRET
}

type billingPolar struct {
	APIBase        string
	AccessToken    string
	OrgSlug        string
	ProductStarter string
	ProductPro     string
	PriceStarter   string
	PricePro       string
	WebhookSecret  string
}

type billingUnifi struct {
	BaseURL     string
	APIKey      string
	APISecret   string
	StoreID     string
	ServiceName string
	CallbackURL string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:              envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		BootstrapToken:        os.Getenv("BOOTSTRAP_TOKEN"),
		GitHubClientID:        os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:    os.Getenv("GITHUB_CLIENT_SECRET"),
		RateLimitPerMin:       envInt("RATE_LIMIT_PER_MIN", 120),
		ValkeyURL:             strings.TrimSpace(os.Getenv("VALKEY_URL")),
		InfisicalSiteURL:      envOr("INFISICAL_SITE_URL", ""),
		InfisicalProjectID:    os.Getenv("INFISICAL_PROJECT_ID"),
		InfisicalEnvironment:  envOr("INFISICAL_ENVIRONMENT", "dev"),
		InfisicalClientID:     os.Getenv("INFISICAL_CLIENT_ID"),
		InfisicalClientSecret: os.Getenv("INFISICAL_CLIENT_SECRET"),
		SecretsDir:            envOr("LLMS_SECRETS_DIR", "./data/secrets"),
		SecretCacheTTL:        envDuration("SECRET_CACHE_TTL", 2*time.Minute),
		AuthCacheTTL:          envDuration("AUTH_CACHE_TTL", 30*time.Second),
		RouteCacheTTL:         envDuration("ROUTE_CACHE_TTL", 30*time.Second),
		AdminGitHubLogins:     os.Getenv("ADMIN_GITHUB_LOGINS"),
		AdminSessionSecret:    os.Getenv("ADMIN_SESSION_SECRET"),
		AdminCookieSecure:     envOr("ADMIN_COOKIE_SECURE", "true") != "false",
		ResendAPIKey:          os.Getenv("RESEND_API_KEY"),
		SMTPHost:              os.Getenv("SMTP_HOST"),
		SMTPPort:              envOr("SMTP_PORT", "587"),
		SMTPUser:              os.Getenv("SMTP_USER"),
		SMTPPass:              os.Getenv("SMTP_PASS"),
		NotifyEmailTo:         envOr("NOTIFY_EMAIL_TO", defaultNotifyEmailTo),
		NotifyEmailFrom:       envOr("NOTIFY_EMAIL_FROM", defaultNotifyEmailFrom),
		PublicBaseURL:         strings.TrimRight(envOr("PUBLIC_BASE_URL", defaultPublicBaseURL), "/"),
		GAMeasurementID:       strings.TrimSpace(os.Getenv("GA_MEASUREMENT_ID")),
		DistDir:               strings.TrimSpace(os.Getenv("LLMS_DIST_DIR")),
		BillingMock:           os.Getenv("BILLING_MOCK") == "true" || os.Getenv("BILLING_MOCK") == "1",
		BillingEnforce:        os.Getenv("BILLING_ENFORCE") == "true" || os.Getenv("BILLING_ENFORCE") == "1",
		Polar: billingPolar{
			APIBase:        "", // set below from POLAR_API_BASE_URL or prod/sandbox default
			AccessToken:    os.Getenv("POLAR_ORGANIZATION_ACCESS_TOKEN"),
			OrgSlug:        os.Getenv("POLAR_ORGANIZATION_SLUG"),
			ProductStarter: os.Getenv("POLAR_PRODUCT_STARTER"),
			ProductPro:     os.Getenv("POLAR_PRODUCT_PRO"),
			PriceStarter:   os.Getenv("POLAR_PRICE_STARTER"),
			PricePro:       os.Getenv("POLAR_PRICE_PRO"),
			WebhookSecret:  os.Getenv("POLAR_WEBHOOK_SECRET"),
		},
		Unifi: billingUnifi{
			BaseURL:     envOr("UNIFI_PAY_BASE_URL", "https://app-api-pay.unifi.me"),
			APIKey:      os.Getenv("UNIFI_PAY_API_KEY"),
			APISecret:   os.Getenv("UNIFI_PAY_API_SECRET"),
			StoreID:     os.Getenv("UNIFI_PAY_STORE_ID"),
			ServiceName: envOr("UNIFI_PAY_SERVICE_NAME", "llms"),
			CallbackURL: os.Getenv("UNIFI_PAY_CALLBACK_URL"),
		},
		BillingCheckoutSuccessURL: os.Getenv("BILLING_CHECKOUT_SUCCESS_URL"),
		BillingCheckoutCancelURL:  os.Getenv("BILLING_CHECKOUT_CANCEL_URL"),
		BillingPortalReturnURL:    os.Getenv("BILLING_PORTAL_RETURN_URL"),
		BillingSessionSecret:      os.Getenv("BILLING_SESSION_SECRET"),
	}
	polarBase := os.Getenv("POLAR_API_BASE_URL")
	if polarBase == "" {
		mock := os.Getenv("BILLING_MOCK") == "true" || os.Getenv("BILLING_MOCK") == "1"
		if mock {
			polarBase = "https://sandbox-api.polar.sh/v1"
		} else {
			polarBase = "https://api.polar.sh/v1"
		}
	}
	cfg.Polar.APIBase = polarBase
	cfg.NotifyDiscordWebhookURL = strings.TrimSpace(os.Getenv("NOTIFY_DISCORD_WEBHOOK_URL"))
	if cfg.BillingCheckoutSuccessURL == "" {
		cfg.BillingCheckoutSuccessURL = cfg.PublicBaseURL + "/billing?ok=1"
	}
	if cfg.BillingCheckoutCancelURL == "" {
		cfg.BillingCheckoutCancelURL = cfg.PublicBaseURL + "/billing?canceled=1"
	}
	if cfg.BillingPortalReturnURL == "" {
		cfg.BillingPortalReturnURL = cfg.PublicBaseURL + "/billing"
	}
	if cfg.Unifi.CallbackURL == "" {
		cfg.Unifi.CallbackURL = cfg.PublicBaseURL + "/billing/webhooks/unifi"
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	_, err := fmt.Sscanf(v, "%d", &n)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// envDuration parses Go durations ("30s", "2m"). Empty → fallback. "0" → disabled (0).
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if v == "0" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}
