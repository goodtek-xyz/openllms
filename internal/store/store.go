package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/goodtek-xyz/openllms/internal/apikey"
	"github.com/goodtek-xyz/openllms/internal/ttlcache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Project struct {
	ID      uuid.UUID
	OwnerID uuid.UUID
	Name    string
}

type APIKey struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Name      string
	Prefix    string
	RouteID   *uuid.UUID
}

type AuthContext struct {
	KeyID     uuid.UUID
	ProjectID uuid.UUID
	KeyName   string
	Prefix    string
	RouteID   *uuid.UUID
}

type Store struct {
	db DB

	authCache  *ttlcache.Cache[string, AuthContext] // key = key_hash
	routeCache *ttlcache.Cache[string, Route]       // key = projectID|slug
}

const (
	DefaultAuthCacheTTL  = 30 * time.Second
	DefaultRouteCacheTTL = 30 * time.Second
)

func New(pool *pgxpool.Pool) *Store {
	return NewWithCacheTTL(pool, DefaultAuthCacheTTL, DefaultRouteCacheTTL)
}

func NewWithCacheTTL(pool *pgxpool.Pool, authTTL, routeTTL time.Duration) *Store {
	return NewDB(newPGX(pool), authTTL, routeTTL)
}

// NewSQLite builds a Store backed by SQLite (OSS / self-host).
func NewSQLite(sqldb *sql.DB) *Store {
	return NewSQLiteWithCacheTTL(sqldb, DefaultAuthCacheTTL, DefaultRouteCacheTTL)
}

// NewSQLiteWithCacheTTL is NewSQLite with explicit auth/route cache TTLs.
func NewSQLiteWithCacheTTL(sqldb *sql.DB, authTTL, routeTTL time.Duration) *Store {
	return NewDB(NewSQLiteDB(sqldb), authTTL, routeTTL)
}

// NewDB builds a Store from a dialect-aware DB handle.
func NewDB(db DB, authTTL, routeTTL time.Duration) *Store {
	return &Store{
		db:         db,
		authCache:  ttlcache.New[string, AuthContext](authTTL),
		routeCache: ttlcache.New[string, Route](routeTTL),
	}
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
}

// Dialect returns the active SQL dialect.
func (s *Store) Dialect() Dialect {
	return s.db.Dialect()
}

func routeCacheKey(projectID uuid.UUID, slug string) string {
	return projectID.String() + "|" + slug
}

func (s *Store) Bootstrap(ctx context.Context, login, projectName, keyName string) (projectID uuid.UUID, plaintext string, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, "", err
	}
	defer tx.Rollback(ctx)

	userID := uuid.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO users (id, login) VALUES ($1, $2)`, userID, login,
	)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert user: %w", err)
	}

	projectID = uuid.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO projects (id, owner_id, name) VALUES ($1, $2, $3)`,
		projectID, userID, projectName,
	)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert project: %w", err)
	}

	plaintext, prefix, hash, err := apikey.Generate()
	if err != nil {
		return uuid.Nil, "", err
	}
	keyID := uuid.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO api_keys (id, project_id, name, key_prefix, key_hash) VALUES ($1, $2, $3, $4, $5)`,
		keyID, projectID, keyName, prefix, hash,
	)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, "", err
	}
	return projectID, plaintext, nil
}

func (s *Store) CreateKey(ctx context.Context, projectID uuid.UUID, name string, routeID *uuid.UUID) (plaintext string, err error) {
	plaintext, prefix, hash, err := apikey.Generate()
	if err != nil {
		return "", err
	}
	keyID := uuid.New()
	_, err = s.db.Exec(ctx,
		`INSERT INTO api_keys (id, project_id, name, key_prefix, key_hash, route_id) VALUES ($1, $2, $3, $4, $5, $6)`,
		keyID, projectID, name, prefix, hash, routeID,
	)
	if err != nil {
		return "", err
	}
	return plaintext, nil
}

func (s *Store) LookupByPlaintext(ctx context.Context, plaintext string) (*AuthContext, error) {
	hash := apikey.Hash(plaintext)
	if s.authCache != nil {
		if ac, ok := s.authCache.Get(hash); ok {
			cp := ac
			return &cp, nil
		}
	}
	var ac AuthContext
	var routeID *uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, name, key_prefix, route_id
		FROM api_keys
		WHERE key_hash = $1 AND revoked_at IS NULL
	`, hash).Scan(&ac.KeyID, &ac.ProjectID, &ac.KeyName, &ac.Prefix, &routeID)
	if err != nil {
		if isNoRows(err) {
			return nil, fmt.Errorf("invalid api key")
		}
		return nil, err
	}
	ac.RouteID = routeID
	if s.authCache != nil {
		s.authCache.Set(hash, ac)
	}
	cp := ac
	return &cp, nil
}

// KeyInfo is the public view of an api_keys row (never exposes key_hash).
type KeyInfo struct {
	ID        uuid.UUID
	Name      string
	Prefix    string
	RouteID   *uuid.UUID
	RevokedAt *time.Time
	CreatedAt time.Time
}

// ListKeys returns every key for the project (active and revoked), newest first.
// Callers filter on RevokedAt for "active only" views.
func (s *Store) ListKeys(ctx context.Context, projectID uuid.UUID) ([]KeyInfo, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, key_prefix, route_id, revoked_at, created_at
		FROM api_keys WHERE project_id = $1 ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyInfo
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.RouteID, &k.RevokedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// invalidateAuthCache drops a cached lookup by key_hash (e.g. after revoke).
func (s *Store) invalidateAuthCache(hash string) {
	if s.authCache != nil {
		s.authCache.Delete(hash)
	}
}

// RevokeKey sets revoked_at on a single key owned by projectID and evicts it
// from the auth cache so it can no longer authenticate.
func (s *Store) RevokeKey(ctx context.Context, projectID, keyID uuid.UUID) error {
	var hash string
	err := s.db.QueryRow(ctx, `
		UPDATE api_keys SET revoked_at = now()
		WHERE id = $1 AND project_id = $2 AND revoked_at IS NULL
		RETURNING key_hash
	`, keyID, projectID).Scan(&hash)
	if err != nil {
		if isNoRows(err) {
			return fmt.Errorf("key not found")
		}
		return err
	}
	s.invalidateAuthCache(hash)
	return nil
}

// CountActiveKeysByName returns how many non-revoked keys match name for the project.
func (s *Store) CountActiveKeysByName(ctx context.Context, projectID uuid.UUID, name string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM api_keys
		WHERE project_id = $1 AND name = $2 AND revoked_at IS NULL
	`, projectID, name).Scan(&n)
	return n, err
}

// RevokeKeysByName revokes every active key matching name for the project
// (e.g. explicit rotate before minting a fresh one) and returns the count revoked.
func (s *Store) RevokeKeysByName(ctx context.Context, projectID uuid.UUID, name string) (int, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE api_keys SET revoked_at = now()
		WHERE project_id = $1 AND name = $2 AND revoked_at IS NULL
		RETURNING key_hash
	`, projectID, name)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return n, err
		}
		s.invalidateAuthCache(hash)
		n++
	}
	return n, rows.Err()
}

func (s *Store) UserCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

type Route struct {
	ID           uuid.UUID
	ProjectID    uuid.UUID
	Slug         string
	Strategy     string
	Config       []byte
	DefaultModel string
}

func (s *Store) CreateRoute(ctx context.Context, projectID uuid.UUID, slug, strategy, defaultModel string, configJSON []byte) (*Route, error) {
	if strategy == "" {
		strategy = "sequential"
	}
	if configJSON == nil {
		configJSON = []byte("{}")
	}
	rt := Route{
		ID:           uuid.New(),
		ProjectID:    projectID,
		Slug:         slug,
		Strategy:     strategy,
		Config:       configJSON,
		DefaultModel: defaultModel,
	}
	err := s.db.QueryRow(ctx, `
		INSERT INTO routes (id, project_id, slug, strategy, config, default_model)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, project_id, slug, strategy, config, default_model
	`, rt.ID, projectID, slug, strategy, string(configJSON), defaultModel).Scan(
		&rt.ID, &rt.ProjectID, &rt.Slug, &rt.Strategy, &rt.Config, &rt.DefaultModel,
	)
	if err != nil {
		return nil, err
	}
	if s.routeCache != nil {
		s.routeCache.Set(routeCacheKey(projectID, slug), rt)
	}
	return &rt, nil
}

func (s *Store) GetRouteBySlug(ctx context.Context, projectID uuid.UUID, slug string) (*Route, error) {
	key := routeCacheKey(projectID, slug)
	if s.routeCache != nil {
		if rt, ok := s.routeCache.Get(key); ok {
			cp := rt
			return &cp, nil
		}
	}
	var rt Route
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, slug, strategy, config, default_model
		FROM routes WHERE project_id = $1 AND slug = $2
	`, projectID, slug).Scan(&rt.ID, &rt.ProjectID, &rt.Slug, &rt.Strategy, &rt.Config, &rt.DefaultModel)
	if err != nil {
		if isNoRows(err) {
			return nil, fmt.Errorf("route not found")
		}
		return nil, err
	}
	if s.routeCache != nil {
		s.routeCache.Set(key, rt)
	}
	cp := rt
	return &cp, nil
}

func (s *Store) GetRouteByID(ctx context.Context, id uuid.UUID) (*Route, error) {
	var rt Route
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, slug, strategy, config, default_model
		FROM routes WHERE id = $1
	`, id).Scan(&rt.ID, &rt.ProjectID, &rt.Slug, &rt.Strategy, &rt.Config, &rt.DefaultModel)
	if err != nil {
		if isNoRows(err) {
			return nil, fmt.Errorf("route not found")
		}
		return nil, err
	}
	return &rt, nil
}

func (s *Store) ListRoutes(ctx context.Context, projectID uuid.UUID) ([]Route, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, slug, strategy, config, default_model
		FROM routes WHERE project_id = $1 ORDER BY created_at
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Route
	for rows.Next() {
		var rt Route
		if err := rows.Scan(&rt.ID, &rt.ProjectID, &rt.Slug, &rt.Strategy, &rt.Config, &rt.DefaultModel); err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

// AuthorizeRoute ensures the API key may access the route.
func (s *Store) AuthorizeRoute(ac *AuthContext, rt *Route) error {
	if ac.ProjectID != rt.ProjectID {
		return fmt.Errorf("forbidden")
	}
	if ac.RouteID != nil && *ac.RouteID != rt.ID {
		return fmt.Errorf("forbidden")
	}
	return nil
}

type Account struct {
	ID                  uuid.UUID
	ProjectID           uuid.UUID
	Vendor              string
	Name                string
	AuthType            string
	InfisicalPath       string
	BaseURL             string
	Health              string
	DefaultModel        string
	Weight              int
	Position            int
	QuotaRemainingPct   *float64
	QuotaResetAt        *time.Time
	QuotaUpdatedAt      *time.Time
	Quota5hRemainingPct *float64
	Quota5hResetAt      *time.Time
	Quota7dRemainingPct *float64
	Quota7dResetAt      *time.Time
	Email               *string    // vendor account email captured at link time; nil = unknown
	ReauthRequiredAt    *time.Time // set when the account needs a human re-link; such accounts are not routed
}

func (s *Store) CreateAccount(ctx context.Context, projectID uuid.UUID, vendorName, name, authType, infisicalPath, baseURL string, defaultModel ...string) (*Account, error) {
	if authType == "" {
		authType = "api_key"
	}
	dm := ""
	if len(defaultModel) > 0 {
		dm = strings.TrimSpace(defaultModel[0])
	}
	a := Account{
		ID:            uuid.New(),
		ProjectID:     projectID,
		Vendor:        vendorName,
		Name:          name,
		AuthType:      authType,
		InfisicalPath: infisicalPath,
		BaseURL:       baseURL,
		Health:        "ok",
		DefaultModel:  dm,
	}
	err := s.db.QueryRow(ctx, `
		INSERT INTO accounts (id, project_id, vendor, name, auth_type, infisical_path, base_url, default_model)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, project_id, vendor, name, auth_type, infisical_path, base_url, health, default_model
	`, a.ID, projectID, vendorName, name, authType, infisicalPath, baseURL, dm).Scan(
		&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.DefaultModel,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// UpdateAccountDefaultModel sets the chat model used when a route picks this seat
// and the client request does not override with a compatible model id.
func (s *Store) UpdateAccountDefaultModel(ctx context.Context, projectID, accountID uuid.UUID, defaultModel string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE accounts SET default_model = $3
		WHERE id = $1 AND project_id = $2
	`, accountID, projectID, strings.TrimSpace(defaultModel))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("account not found")
	}
	return nil
}

func (s *Store) UpdateRoute(ctx context.Context, projectID uuid.UUID, slug, strategy, defaultModel string, configJSON []byte) (*Route, error) {
	if strategy == "" {
		strategy = "sequential"
	}
	if configJSON == nil {
		configJSON = []byte("{}")
	}
	var rt Route
	err := s.db.QueryRow(ctx, `
		UPDATE routes
		SET strategy = $3, config = $4::jsonb, default_model = $5
		WHERE project_id = $1 AND slug = $2
		RETURNING id, project_id, slug, strategy, config, default_model
	`, projectID, slug, strategy, string(configJSON), defaultModel).Scan(
		&rt.ID, &rt.ProjectID, &rt.Slug, &rt.Strategy, &rt.Config, &rt.DefaultModel,
	)
	if err != nil {
		if isNoRows(err) {
			return nil, fmt.Errorf("route not found")
		}
		return nil, err
	}
	if s.routeCache != nil {
		s.routeCache.Set(routeCacheKey(projectID, slug), rt)
	}
	return &rt, nil
}

// ListRoutePool returns all accounts attached to a route (including cooldown), ordered by position.
func (s *Store) ListRoutePool(ctx context.Context, routeID uuid.UUID) ([]Account, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.project_id, a.vendor, a.name, a.auth_type, a.infisical_path, a.base_url, a.health, a.default_model,
		       ra.weight, ra.position, a.quota_remaining_pct, a.quota_reset_at, a.quota_updated_at, a.quota_5h_remaining_pct, a.quota_5h_reset_at, a.quota_7d_remaining_pct, a.quota_7d_reset_at,
		       a.email, a.reauth_required_at
		FROM route_accounts ra
		JOIN accounts a ON a.id = ra.account_id
		WHERE ra.route_id = $1
		ORDER BY ra.position ASC, a.created_at ASC
	`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.DefaultModel, &a.Weight, &a.Position, &a.QuotaRemainingPct, &a.QuotaResetAt, &a.QuotaUpdatedAt, &a.Quota5hRemainingPct, &a.Quota5hResetAt, &a.Quota7dRemainingPct, &a.Quota7dResetAt, &a.Email, &a.ReauthRequiredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReplaceRouteAccounts sets the route membership to exactly accountIDs (order = position).
// When weights is nil or shorter than accountIDs, missing entries default to 1.
func (s *Store) ReplaceRouteAccounts(ctx context.Context, routeID uuid.UUID, accountIDs []uuid.UUID, weights []int) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM route_accounts WHERE route_id = $1`, routeID); err != nil {
		return err
	}
	for i, aid := range accountIDs {
		w := 1
		if i < len(weights) && weights[i] > 0 {
			w = weights[i]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO route_accounts (route_id, account_id, position, weight)
			VALUES ($1, $2, $3, $4)
		`, routeID, aid, i, w); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) AttachAccount(ctx context.Context, routeID, accountID uuid.UUID, position, weight int) error {
	if weight <= 0 {
		weight = 1
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO route_accounts (route_id, account_id, position, weight)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (route_id, account_id) DO UPDATE SET position = EXCLUDED.position, weight = EXCLUDED.weight
	`, routeID, accountID, position, weight)
	return err
}

func (s *Store) ListRouteAccounts(ctx context.Context, routeID uuid.UUID) ([]Account, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.project_id, a.vendor, a.name, a.auth_type, a.infisical_path, a.base_url, a.health, a.default_model,
		       ra.weight, ra.position, a.quota_remaining_pct, a.quota_reset_at, a.quota_updated_at, a.quota_5h_remaining_pct, a.quota_5h_reset_at, a.quota_7d_remaining_pct, a.quota_7d_reset_at,
		       a.email, a.reauth_required_at
		FROM route_accounts ra
		JOIN accounts a ON a.id = ra.account_id
		WHERE ra.route_id = $1
		  AND (a.cooldown_until IS NULL OR a.cooldown_until < now())
		  AND a.reauth_required_at IS NULL
		ORDER BY ra.position ASC, a.created_at ASC
	`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.DefaultModel, &a.Weight, &a.Position, &a.QuotaRemainingPct, &a.QuotaResetAt, &a.QuotaUpdatedAt, &a.Quota5hRemainingPct, &a.Quota5hResetAt, &a.Quota7dRemainingPct, &a.Quota7dResetAt, &a.Email, &a.ReauthRequiredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DetachRouteAccount(ctx context.Context, routeID, accountID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM route_accounts WHERE route_id = $1 AND account_id = $2
	`, routeID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not attached")
	}
	return nil
}

func (s *Store) DeleteRoute(ctx context.Context, projectID uuid.UUID, slug string) error {
	rt, err := s.GetRouteBySlug(ctx, projectID, slug)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM routes WHERE id = $1 AND project_id = $2`, rt.ID, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("route not found")
	}
	if s.routeCache != nil {
		s.routeCache.Delete(routeCacheKey(projectID, slug))
	}
	return nil
}

func (s *Store) GetAccount(ctx context.Context, projectID, accountID uuid.UUID) (*Account, error) {
	var a Account
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, vendor, name, auth_type, infisical_path, base_url, health, default_model, email, reauth_required_at
		FROM accounts WHERE id = $1 AND project_id = $2
	`, accountID, projectID).Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.DefaultModel, &a.Email, &a.ReauthRequiredAt)
	if err != nil {
		if isNoRows(err) {
			return nil, fmt.Errorf("account not found")
		}
		return nil, err
	}
	return &a, nil
}

// DeleteAccount removes the account row (route_accounts cascade via FK;
// usage_events.account_id is set NULL). Returns the account's infisical_path
// so the caller can best-effort delete the upstream secret.
func (s *Store) DeleteAccount(ctx context.Context, projectID, accountID uuid.UUID) (infisicalPath string, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT infisical_path FROM accounts WHERE id = $1 AND project_id = $2
	`, accountID, projectID).Scan(&infisicalPath)
	if err != nil {
		if isNoRows(err) {
			return "", fmt.Errorf("account not found")
		}
		return "", err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM accounts WHERE id = $1 AND project_id = $2`, accountID, projectID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("account not found")
	}
	return infisicalPath, nil
}

func (s *Store) InsertUsage(ctx context.Context, projectID uuid.UUID, routeID, accountID, keyID *uuid.UUID, model string, status, latencyMs, tin, tout int, errMsg string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO usage_events (id, project_id, route_id, account_id, api_key_id, model, status_code, latency_ms, tokens_in, tokens_out, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, uuid.New(), projectID, routeID, accountID, keyID, model, status, latencyMs, tin, tout, errMsg)
	return err
}

func (s *Store) SetCooldown(ctx context.Context, accountID uuid.UUID, until time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE accounts SET cooldown_until = $2, health = 'cooldown' WHERE id = $1`, accountID, until)
	return err
}

// EarliestRouteCooldown returns the soonest cooldown_until still in the future among
// accounts attached to the route. ok=false when none are cooling down.
func (s *Store) EarliestRouteCooldown(ctx context.Context, routeID uuid.UUID) (until time.Time, ok bool, err error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.cooldown_until
		FROM route_accounts ra
		JOIN accounts a ON a.id = ra.account_id
		WHERE ra.route_id = $1
		  AND a.cooldown_until IS NOT NULL
		  AND a.cooldown_until > now()
	`, routeID)
	if err != nil {
		return time.Time{}, false, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return time.Time{}, false, err
		}
		t, perr := coerceTime(raw)
		if perr != nil {
			return time.Time{}, false, perr
		}
		if t.After(now) && (!ok || t.Before(until)) {
			until = t
			ok = true
		}
	}
	return until, ok, rows.Err()
}

func coerceTime(raw any) (time.Time, error) {
	switch v := raw.(type) {
	case time.Time:
		return v, nil
	case *time.Time:
		if v == nil {
			return time.Time{}, fmt.Errorf("nil time")
		}
		return *v, nil
	case string:
		return parseFlexibleTime(v)
	case []byte:
		return parseFlexibleTime(string(v))
	default:
		return time.Time{}, fmt.Errorf("unsupported time type %T", raw)
	}
}

func parseFlexibleTime(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999 +0000 UTC",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q", s)
}

func (s *Store) MarkAccountHealthy(ctx context.Context, accountID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE accounts SET cooldown_until = NULL, health = 'ok' WHERE id = $1`, accountID)
	return err
}

func (s *Store) MarkAccountUnhealthy(ctx context.Context, accountID uuid.UUID, until time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE accounts SET cooldown_until = $2, health = 'error' WHERE id = $1`, accountID, until)
	return err
}

// MarkAccountReauthRequired flags an account that needs a human re-link: it leaves routing until RelinkAccount
// clears the flag. Only the first caller of an incident gets first=true (the conditional UPDATE matches one row
// once, also across gateway replicas), so exactly one notification is sent. health = 'reauth' is for display only;
// other writers overwrite health, the flag is reauth_required_at.
func (s *Store) MarkAccountReauthRequired(ctx context.Context, accountID uuid.UUID) (first bool, err error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE accounts SET reauth_required_at = now(), health = 'reauth'
		WHERE id = $1 AND reauth_required_at IS NULL
	`, accountID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetAccountEmail records the vendor account email (as reported by the vendor at link or refresh time).
func (s *Store) SetAccountEmail(ctx context.Context, accountID uuid.UUID, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `UPDATE accounts SET email = $2 WHERE id = $1`, accountID, email)
	return err
}

// GetAccountByName returns the project's account with this vendor and name (the UNIQUE key), or nil when none.
func (s *Store) GetAccountByName(ctx context.Context, projectID uuid.UUID, vendorName, name string) (*Account, error) {
	var a Account
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, vendor, name, auth_type, infisical_path, base_url, health, default_model, email, reauth_required_at
		FROM accounts WHERE project_id = $1 AND vendor = $2 AND name = $3
	`, projectID, vendorName, name).Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.DefaultModel, &a.Email, &a.ReauthRequiredAt)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// RelinkAccount is a new login for an existing account (same project, vendor, name): it takes the new auth type,
// secret path and base URL, keeps the default model unless one is given and the email unless a new one is known,
// and puts the account back into routing (clears reauth_required_at and cooldown, health ok).
func (s *Store) RelinkAccount(ctx context.Context, projectID, accountID uuid.UUID, authType, infisicalPath, baseURL, defaultModel, email string) (*Account, error) {
	var emailArg *string
	if e := strings.TrimSpace(email); e != "" {
		emailArg = &e
	}
	var a Account
	err := s.db.QueryRow(ctx, `
		UPDATE accounts
		SET auth_type = $3, infisical_path = $4, base_url = $5,
		    default_model = CASE WHEN $6 = '' THEN default_model ELSE $6 END,
		    email = COALESCE($7, email),
		    reauth_required_at = NULL, cooldown_until = NULL, health = 'ok'
		WHERE id = $1 AND project_id = $2
		RETURNING id, project_id, vendor, name, auth_type, infisical_path, base_url, health, default_model, email, reauth_required_at
	`, accountID, projectID, authType, infisicalPath, baseURL, strings.TrimSpace(defaultModel), emailArg).Scan(
		&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.DefaultModel, &a.Email, &a.ReauthRequiredAt,
	)
	if err != nil {
		if isNoRows(err) {
			return nil, fmt.Errorf("account not found")
		}
		return nil, err
	}
	return &a, nil
}

func (s *Store) ListAllAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, vendor, name, auth_type, infisical_path, base_url, health,
		       quota_remaining_pct, quota_reset_at, quota_updated_at, quota_5h_remaining_pct, quota_5h_reset_at, quota_7d_remaining_pct, quota_7d_reset_at,
		       email, reauth_required_at
		FROM accounts ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.QuotaRemainingPct, &a.QuotaResetAt, &a.QuotaUpdatedAt, &a.Quota5hRemainingPct, &a.Quota5hResetAt, &a.Quota7dRemainingPct, &a.Quota7dResetAt, &a.Email, &a.ReauthRequiredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpsertGitHubUser(ctx context.Context, githubID, login string) (userID, projectID uuid.UUID, plaintextKey string, created bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", false, err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE github_id = $1`, githubID).Scan(&userID)
	if isNoRows(err) {
		userID = uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO users (id, github_id, login, last_login_at) VALUES ($1, $2, $3, now())
		`, userID, githubID, login)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", false, err
		}
		projectID = uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO projects (id, owner_id, name) VALUES ($1, $2, 'default')`, projectID, userID)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", false, err
		}
		pt, prefix, hash, gerr := apikey.Generate()
		if gerr != nil {
			return uuid.Nil, uuid.Nil, "", false, gerr
		}
		_, err = tx.Exec(ctx, `INSERT INTO api_keys (id, project_id, name, key_prefix, key_hash) VALUES ($1, $2, 'cli', $3, $4)`, uuid.New(), projectID, prefix, hash)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return uuid.Nil, uuid.Nil, "", false, err
		}
		return userID, projectID, pt, true, nil
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, "", false, err
	}
	_, _ = tx.Exec(ctx, `UPDATE users SET login = $2, last_login_at = now() WHERE id = $1`, userID, login)
	err = tx.QueryRow(ctx, `SELECT id FROM projects WHERE owner_id = $1 ORDER BY created_at LIMIT 1`, userID).Scan(&projectID)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, uuid.Nil, "", false, err
	}
	// Existing user: keep active cli keys across re-login. Plaintext cannot be
	// recovered from the hash — return empty key when one already exists.
	// Mint only if the project has no active cli key (e.g. user revoked all).
	n, err := s.CountActiveKeysByName(ctx, projectID, "cli")
	if err != nil {
		return uuid.Nil, uuid.Nil, "", false, err
	}
	if n > 0 {
		return userID, projectID, "", false, nil
	}
	pt, err := s.CreateKey(ctx, projectID, "cli", nil)
	return userID, projectID, pt, false, err
}

// AdminUserRow is a cross-tenant summary row for the operator dashboard.
type AdminUserRow struct {
	ID           uuid.UUID
	GitHubID     *string
	Login        string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
	ProjectID    uuid.UUID
	AccountCount int
	RouteCount   int
	ActiveKeys   int
	UsageMonth   int64
}

func (s *Store) ListAdminUsers(ctx context.Context) ([]AdminUserRow, error) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := s.db.Query(ctx, `
		SELECT u.id, u.github_id, u.login, u.created_at, u.last_login_at,
		       p.id AS project_id,
		       (SELECT count(*) FROM accounts a WHERE a.project_id = p.id) AS account_count,
		       (SELECT count(*) FROM routes r WHERE r.project_id = p.id) AS route_count,
		       (SELECT count(*) FROM api_keys k WHERE k.project_id = p.id AND k.revoked_at IS NULL) AS active_keys,
		       (SELECT count(*) FROM usage_events e WHERE e.project_id = p.id AND e.created_at >= $1) AS usage_month
		FROM users u
		JOIN projects p ON p.id = (
			SELECT id FROM projects WHERE owner_id = u.id ORDER BY created_at ASC LIMIT 1
		)
		ORDER BY COALESCE(u.last_login_at, u.created_at) DESC, u.created_at DESC
	`, monthStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminUserRow
	for rows.Next() {
		var row AdminUserRow
		if err := rows.Scan(
			&row.ID, &row.GitHubID, &row.Login, &row.CreatedAt, &row.LastLoginAt,
			&row.ProjectID, &row.AccountCount, &row.RouteCount, &row.ActiveKeys, &row.UsageMonth,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) ListAdminAccounts(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.vendor, a.name, a.auth_type, a.health, a.quota_remaining_pct,
		       a.quota_updated_at, u.login, p.id AS project_id, a.email, a.reauth_required_at
		FROM accounts a
		JOIN projects p ON p.id = a.project_id
		JOIN users u ON u.id = p.owner_id
		ORDER BY u.login, a.vendor, a.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var (
			id, projectID    uuid.UUID
			vendor, name     string
			authType, health string
			login            string
			quotaPct         *float64
			quotaUpdated     *time.Time
			email            *string
			reauthAt         *time.Time
		)
		if err := rows.Scan(&id, &vendor, &name, &authType, &health, &quotaPct, &quotaUpdated, &login, &projectID, &email, &reauthAt); err != nil {
			return nil, err
		}
		m := map[string]any{
			"id": id.String(), "vendor": vendor, "name": name, "auth_type": authType,
			"health": health, "login": login, "project_id": projectID.String(),
			"ref": vendor + ":" + name,
		}
		if quotaPct != nil {
			m["quota_remaining_pct"] = *quotaPct
		}
		if quotaUpdated != nil {
			m["quota_updated_at"] = quotaUpdated.UTC().Format(time.RFC3339)
		}
		if email != nil {
			m["email"] = *email
		}
		if reauthAt != nil {
			m["health"] = "reauth" // the flag, not the free-text health, decides
			m["reauth_required_at"] = reauthAt.UTC().Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListAdminRoutes returns cross-tenant routes with owner login and pool membership.
func (s *Store) ListAdminRoutes(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.project_id, r.slug, r.strategy, r.default_model, u.login
		FROM routes r
		JOIN projects p ON p.id = r.project_id
		JOIN users u ON u.id = p.owner_id
		ORDER BY u.login, r.slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type routeMeta struct {
		id, projectID                       uuid.UUID
		slug, strategy, defaultModel, login string
	}
	var metas []routeMeta
	for rows.Next() {
		var m routeMeta
		if err := rows.Scan(&m.id, &m.projectID, &m.slug, &m.strategy, &m.defaultModel, &m.login); err != nil {
			return nil, err
		}
		metas = append(metas, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		pool, err := s.ListRoutePool(ctx, m.id)
		if err != nil {
			return nil, err
		}
		refs := make([]string, 0, len(pool))
		healths := make([]string, 0, len(pool))
		unhealthy := 0
		for _, a := range pool {
			refs = append(refs, a.Vendor+":"+a.Name)
			healths = append(healths, a.Health)
			if a.Health != "ok" {
				unhealthy++
			}
		}
		out = append(out, map[string]any{
			"id":            m.id.String(),
			"project_id":    m.projectID.String(),
			"login":         m.login,
			"slug":          m.slug,
			"strategy":      m.strategy,
			"default_model": m.defaultModel,
			"pool":          refs,
			"pool_health":   healths,
			"pool_size":     len(refs),
			"unhealthy":     unhealthy,
		})
	}
	return out, nil
}

func (s *Store) InsertNotifyLog(ctx context.Context, kind, subject, body, status, detail string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO admin_notify_log (id, kind, subject, body, status, detail)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, uuid.New(), kind, subject, body, status, detail)
	return err
}

func (s *Store) ListAccounts(ctx context.Context, projectID uuid.UUID) ([]Account, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, vendor, name, auth_type, infisical_path, base_url, health, default_model,
		       quota_remaining_pct, quota_reset_at, quota_updated_at, quota_5h_remaining_pct, quota_5h_reset_at, quota_7d_remaining_pct, quota_7d_reset_at,
		       email, reauth_required_at
		FROM accounts WHERE project_id = $1 ORDER BY vendor, name
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.DefaultModel, &a.QuotaRemainingPct, &a.QuotaResetAt, &a.QuotaUpdatedAt, &a.Quota5hRemainingPct, &a.Quota5hResetAt, &a.Quota7dRemainingPct, &a.Quota7dResetAt, &a.Email, &a.ReauthRequiredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetAccountQuota(ctx context.Context, projectID, accountID uuid.UUID, remainingPct *float64, resetAt *time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE accounts
		SET quota_remaining_pct = $3, quota_reset_at = $4, quota_updated_at = now()
		WHERE id = $1 AND project_id = $2
	`, accountID, projectID, remainingPct, resetAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("account not found")
	}
	return nil
}

// RefreshQuotasFromUsage sets remaining_pct from calendar-month account tokens vs project soft_cap_tokens
// for API-key accounts only. OAuth rows are filled by provider fetch (Codex/Claude usage APIs).
func (s *Store) RefreshQuotasFromUsage(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if s.db.Dialect() == DialectSQLite {
		return s.refreshQuotasSQLite(ctx, monthStart, nil)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE accounts a
		SET quota_remaining_pct = greatest(
		      0,
		      least(
		        100,
		        100.0 * (1.0 - coalesce((
		          SELECT sum(tokens_in + tokens_out)::float8
		          FROM usage_events ue
		          WHERE ue.account_id = a.id AND ue.created_at >= $1
		        ), 0) / nullif(coalesce(p.soft_cap_tokens, 1000000)::float8, 0))
		      )
		    ),
		    quota_updated_at = now()
		FROM projects p
		WHERE a.project_id = p.id
		  AND lower(coalesce(a.auth_type, 'api_key')) <> 'oauth'
	`, monthStart)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) RefreshQuotasFromUsageForProject(ctx context.Context, projectID uuid.UUID) (int, error) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if s.db.Dialect() == DialectSQLite {
		return s.refreshQuotasSQLite(ctx, monthStart, &projectID)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE accounts a
		SET quota_remaining_pct = greatest(
		      0,
		      least(
		        100,
		        100.0 * (1.0 - coalesce((
		          SELECT sum(tokens_in + tokens_out)::float8
		          FROM usage_events ue
		          WHERE ue.account_id = a.id AND ue.created_at >= $1
		        ), 0) / nullif(coalesce(p.soft_cap_tokens, 1000000)::float8, 0))
		      )
		    ),
		    quota_updated_at = now()
		FROM projects p
		WHERE a.project_id = p.id
		  AND a.project_id = $2
		  AND lower(coalesce(a.auth_type, 'api_key')) <> 'oauth'
	`, monthStart, projectID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) ListOAuthAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, vendor, name, auth_type, infisical_path, base_url, health,
		       quota_remaining_pct, quota_reset_at, quota_updated_at, quota_5h_remaining_pct, quota_5h_reset_at, quota_7d_remaining_pct, quota_7d_reset_at,
		       email, reauth_required_at
		FROM accounts
		WHERE lower(coalesce(auth_type, '')) = 'oauth'
		ORDER BY vendor, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.QuotaRemainingPct, &a.QuotaResetAt, &a.QuotaUpdatedAt, &a.Quota5hRemainingPct, &a.Quota5hResetAt, &a.Quota7dRemainingPct, &a.Quota7dResetAt, &a.Email, &a.ReauthRequiredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListOAuthAccountsByProject returns oauth accounts scoped to a single project
// (used to limit quota refresh / background work to the caller's project).
func (s *Store) ListOAuthAccountsByProject(ctx context.Context, projectID uuid.UUID) ([]Account, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, vendor, name, auth_type, infisical_path, base_url, health,
		       quota_remaining_pct, quota_reset_at, quota_updated_at, quota_5h_remaining_pct, quota_5h_reset_at, quota_7d_remaining_pct, quota_7d_reset_at,
		       email, reauth_required_at
		FROM accounts
		WHERE project_id = $1 AND lower(coalesce(auth_type, '')) = 'oauth'
		ORDER BY vendor, name
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Vendor, &a.Name, &a.AuthType, &a.InfisicalPath, &a.BaseURL, &a.Health, &a.QuotaRemainingPct, &a.QuotaResetAt, &a.QuotaUpdatedAt, &a.Quota5hRemainingPct, &a.Quota5hResetAt, &a.Quota7dRemainingPct, &a.Quota7dResetAt, &a.Email, &a.ReauthRequiredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAccountQuotaID updates quota fields by account id (background / provider refresh).
func (s *Store) SetAccountQuotaID(ctx context.Context, accountID uuid.UUID, remainingPct *float64, resetAt *time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE accounts
		SET quota_remaining_pct = $2, quota_reset_at = $3, quota_updated_at = now()
		WHERE id = $1
	`, accountID, remainingPct, resetAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("account not found")
	}
	return nil
}

// SetAccountQuotaWindowsID persists tightest mirror + dual 5h/7d windows.
func (s *Store) SetAccountQuotaWindowsID(ctx context.Context, accountID uuid.UUID, remainingPct *float64, resetAt *time.Time, rem5h *float64, reset5h *time.Time, rem7d *float64, reset7d *time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE accounts
		SET quota_remaining_pct = $2, quota_reset_at = $3, quota_updated_at = now(),
		    quota_5h_remaining_pct = $4, quota_5h_reset_at = $5,
		    quota_7d_remaining_pct = $6, quota_7d_reset_at = $7
		WHERE id = $1
	`, accountID, remainingPct, resetAt, rem5h, reset5h, rem7d, reset7d)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("account not found")
	}
	return nil
}

func (s *Store) StatusSnapshot(ctx context.Context, projectID uuid.UUID) (accounts []Account, routes []Route, err error) {
	accounts, err = s.ListAccounts(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	routes, err = s.ListRoutes(ctx, projectID)
	return accounts, routes, err
}

func (s *Store) ProjectCaps(ctx context.Context, projectID uuid.UUID) (softUSD *float64, softTokens *int64, err error) {
	var usd *float64
	var tok *int64
	err = s.db.QueryRow(ctx, `SELECT soft_cap_usd, soft_cap_tokens FROM projects WHERE id = $1`, projectID).Scan(&usd, &tok)
	return usd, tok, err
}

func (s *Store) UsageTotals(ctx context.Context, projectID uuid.UUID, since time.Time) (requests int64, tokensIn, tokensOut int64, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(tokens_in),0), coalesce(sum(tokens_out),0)
		FROM usage_events WHERE project_id = $1 AND created_at >= $2
	`, projectID, since).Scan(&requests, &tokensIn, &tokensOut)
	return
}

func (s *Store) SetProjectCaps(ctx context.Context, projectID uuid.UUID, softUSD *float64, softTokens *int64) error {
	_, err := s.db.Exec(ctx, `UPDATE projects SET soft_cap_usd = $2, soft_cap_tokens = $3 WHERE id = $1`, projectID, softUSD, softTokens)
	return err
}

func (s *Store) MonthStartUTC(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (s *Store) MonthTokenUsage(ctx context.Context, projectID uuid.UUID) (int64, error) {
	now := time.Now().UTC()
	since := s.MonthStartUTC(now)
	_, tin, tout, err := s.UsageTotals(ctx, projectID, since)
	if err != nil {
		return 0, err
	}
	return tin + tout, nil
}

// OverSoftTokenCap returns whether monthly token usage meets cap. cap<=0 means no limit.
func (s *Store) OverSoftTokenCap(ctx context.Context, projectID uuid.UUID, cap int64) (over bool, used int64, err error) {
	if cap <= 0 {
		return false, 0, nil
	}
	used, err = s.MonthTokenUsage(ctx, projectID)
	if err != nil {
		return false, 0, err
	}
	return used >= cap, used, nil
}

// refreshQuotasSQLite recomputes remaining_pct in Go (SQLite lacks UPDATE…FROM).
func (s *Store) refreshQuotasSQLite(ctx context.Context, monthStart time.Time, projectID *uuid.UUID) (int, error) {
	q := `
		SELECT a.id, coalesce(p.soft_cap_tokens, 1000000)
		FROM accounts a
		JOIN projects p ON p.id = a.project_id
		WHERE lower(coalesce(a.auth_type, 'api_key')) <> 'oauth'
	`
	args := []any{}
	if projectID != nil {
		q += ` AND a.project_id = $1`
		args = append(args, *projectID)
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type row struct {
		id  uuid.UUID
		cap int64
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.cap); err != nil {
			return 0, err
		}
		if r.cap <= 0 {
			r.cap = 1000000
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, r := range list {
		var used int64
		err := s.db.QueryRow(ctx, `
			SELECT coalesce(sum(tokens_in + tokens_out), 0)
			FROM usage_events WHERE account_id = $1 AND created_at >= $2
		`, r.id, monthStart).Scan(&used)
		if err != nil {
			return n, err
		}
		pct := 100.0 * (1.0 - float64(used)/float64(r.cap))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		_, err = s.db.Exec(ctx, `
			UPDATE accounts SET quota_remaining_pct = $2, quota_updated_at = now() WHERE id = $1
		`, r.id, pct)
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
