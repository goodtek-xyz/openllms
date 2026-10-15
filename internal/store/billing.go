package store

import (
	"context"
	"fmt"
	"time"

	"github.com/goodtek-xyz/openllms/internal/billing"
	"github.com/google/uuid"
)

type ProjectBilling struct {
	ProjectID              uuid.UUID
	OwnerID                uuid.UUID
	GitHubID               string
	Plan                   billing.Plan
	Status                 string
	TrialStartedAt         *time.Time
	TrialEndsAt            *time.Time
	TrialUsed              bool
	UserTrialUsed          bool
	CurrentPeriodStart     *time.Time
	CurrentPeriodEnd       *time.Time
	CancelAtPeriodEnd      bool
	BillingProvider        string
	ProviderCustomerID     string
	ProviderSubscriptionID string
}

func (s *Store) GetProjectBilling(ctx context.Context, projectID uuid.UUID) (ProjectBilling, error) {
	// OSS SQLite schema has no Cloud billing columns — treat as open / unenforced plan.
	if s.Dialect() == DialectSQLite {
		return ProjectBilling{
			ProjectID: projectID,
			Plan:      billing.PlanNone,
			Status:    "none",
		}, nil
	}
	var b ProjectBilling
	var plan, status string
	err := s.db.QueryRow(ctx, `
		SELECT p.id, p.owner_id, COALESCE(u.github_id, ''),
		       COALESCE(p.plan, 'none'), COALESCE(p.billing_status, 'none'),
		       p.trial_started_at, p.trial_ends_at, COALESCE(p.trial_used, false),
		       COALESCE(u.trial_used, false),
		       p.current_period_start, p.current_period_end, COALESCE(p.cancel_at_period_end, false),
		       COALESCE(p.billing_provider, ''), COALESCE(p.provider_customer_id, ''),
		       COALESCE(p.provider_subscription_id, '')
		FROM projects p
		JOIN users u ON u.id = p.owner_id
		WHERE p.id = $1
	`, projectID).Scan(
		&b.ProjectID, &b.OwnerID, &b.GitHubID,
		&plan, &status,
		&b.TrialStartedAt, &b.TrialEndsAt, &b.TrialUsed,
		&b.UserTrialUsed,
		&b.CurrentPeriodStart, &b.CurrentPeriodEnd, &b.CancelAtPeriodEnd,
		&b.BillingProvider, &b.ProviderCustomerID, &b.ProviderSubscriptionID,
	)
	if err != nil {
		return ProjectBilling{}, err
	}
	b.Plan = billing.NormalizePlan(plan)
	b.Status = status
	return b, nil
}

func (b ProjectBilling) Entitlement() billing.Entitlement {
	return billing.Entitlement{
		Plan:                 b.Plan,
		Status:               b.Status,
		TrialEndsAt:          b.TrialEndsAt,
		CurrentPeriodStart:   b.CurrentPeriodStart,
		CurrentPeriodEnd:     b.CurrentPeriodEnd,
		CancelAtPeriodEnd:    b.CancelAtPeriodEnd,
		BillingProvider:      b.BillingProvider,
		ProviderCustomerID:   b.ProviderCustomerID,
		ProviderSubscription: b.ProviderSubscriptionID,
		TrialUsed:            b.UserTrialUsed || b.TrialUsed,
	}
}

// StartTrial is a legacy name for EnsureFreePlan (kept for older call sites / API aliases).
func (s *Store) StartTrial(ctx context.Context, projectID uuid.UUID, now time.Time) error {
	return s.EnsureFreePlan(ctx, projectID, now)
}

// EnsureFreePlan sets plan=free with Free soft-cap when the project is not on an active paid plan.
// This is the Free activation path for Cloud + shared control APIs (not a timed trial).
func (s *Store) EnsureFreePlan(ctx context.Context, projectID uuid.UUID, now time.Time) error {
	b, err := s.GetProjectBilling(ctx, projectID)
	if err != nil {
		return err
	}
	ent := b.Entitlement()
	if ent.Entitled(now) {
		switch ent.EffectivePlan(now) {
		case billing.PlanStarter, billing.PlanPro:
			return nil
		case billing.PlanFree:
			// Already clean free — skip rewrite. Legacy trial→free canonicalize with
			// status still "trialing" falls through so we normalize the row.
			if b.Status == "active" {
				return nil
			}
		}
	}
	lim := billing.PlanFree.Limits()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		UPDATE projects SET
			plan = 'free', billing_status = 'active',
			trial_started_at = COALESCE(trial_started_at, $2),
			trial_ends_at = NULL,
			trial_used = true,
			current_period_start = COALESCE(current_period_start, $2),
			current_period_end = NULL,
			cancel_at_period_end = false,
			soft_cap_tokens = $3
		WHERE id = $1
	`, projectID, now, lim.SoftCap)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE users SET trial_used = true WHERE id = $1`, b.OwnerID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ApplyPaidPeriod(ctx context.Context, projectID uuid.UUID, plan billing.Plan, provider, customerID, subID, txID string, start, end time.Time) error {
	if !plan.ValidPaid() {
		return fmt.Errorf("invalid_plan")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	lim := plan.Limits()
	_, err = tx.Exec(ctx, `
		UPDATE projects SET
			plan = $2, billing_status = 'active', billing_provider = $3,
			provider_customer_id = COALESCE(NULLIF($4, ''), provider_customer_id),
			provider_subscription_id = COALESCE(NULLIF($5, ''), provider_subscription_id),
			current_period_start = $6, current_period_end = $7,
			cancel_at_period_end = false,
			soft_cap_tokens = $8
		WHERE id = $1
	`, projectID, string(plan), provider, customerID, subID, start, end, lim.SoftCap)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO billing_orders (project_id, provider, provider_order_id, transaction_id, plan, amount_usd, period_start, period_end, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'paid')
		ON CONFLICT DO NOTHING
	`, projectID, provider, subID, txID, string(plan), plan.PriceUSD(), start, end)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordBillingEvent(ctx context.Context, provider, eventID, eventType string, payload []byte) error {
	if eventID == "" {
		eventID = uuid.NewString()
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO billing_events (provider, event_id, event_type, payload)
		VALUES ($1, $2, $3, $4::jsonb)
		ON CONFLICT (provider, event_id) DO NOTHING
	`, provider, eventID, eventType, string(payload))
	return err
}

func (s *Store) CountProjectAccounts(ctx context.Context, projectID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE project_id = $1`, projectID).Scan(&n)
	return n, err
}

func (s *Store) CountProjectRoutes(ctx context.Context, projectID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM routes WHERE project_id = $1`, projectID).Scan(&n)
	return n, err
}

func (s *Store) CountProjectActiveKeys(ctx context.Context, projectID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE project_id = $1 AND revoked_at IS NULL`, projectID).Scan(&n)
	return n, err
}

func (s *Store) SetBillingPastDue(ctx context.Context, projectID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE projects SET billing_status = 'past_due' WHERE id = $1`, projectID)
	return err
}

func (s *Store) SetBillingExpired(ctx context.Context, projectID uuid.UUID) error {
	lim := billing.PlanFree.Limits()
	_, err := s.db.Exec(ctx, `
		UPDATE projects SET
			billing_status = 'active', plan = 'free', cancel_at_period_end = false,
			current_period_end = NULL, soft_cap_tokens = $2
		WHERE id = $1
	`, projectID, lim.SoftCap)
	return err
}

// UpsertPendingBillingOrder records a checkout attempt (Unifi requestId in transaction_id).
func (s *Store) UpsertPendingBillingOrder(ctx context.Context, projectID uuid.UUID, provider, orderID, requestID string, plan billing.Plan) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO billing_orders (project_id, provider, provider_order_id, transaction_id, plan, amount_usd, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')
		ON CONFLICT (provider, transaction_id) WHERE transaction_id <> ''
		DO UPDATE SET provider_order_id = EXCLUDED.provider_order_id, plan = EXCLUDED.plan, updated_at = now()
	`, projectID, provider, orderID, requestID, string(plan), plan.PriceUSD())
	return err
}

// PendingOrderByRequestID finds a pending order by provider + request/transaction id.
func (s *Store) PendingOrderByRequestID(ctx context.Context, provider, requestID string) (uuid.UUID, billing.Plan, error) {
	var projectID uuid.UUID
	var plan string
	err := s.db.QueryRow(ctx, `
		SELECT project_id, plan FROM billing_orders
		WHERE provider = $1 AND transaction_id = $2 AND status = 'pending'
		LIMIT 1
	`, provider, requestID).Scan(&projectID, &plan)
	if isNoRows(err) {
		return uuid.Nil, billing.PlanNone, fmt.Errorf("not_found")
	}
	if err != nil {
		return uuid.Nil, billing.PlanNone, err
	}
	return projectID, billing.NormalizePlan(plan), nil
}

func (s *Store) ProjectIDByProviderCustomer(ctx context.Context, provider, customerID string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT id FROM projects WHERE billing_provider = $1 AND provider_customer_id = $2 LIMIT 1
	`, provider, customerID).Scan(&id)
	if isNoRows(err) {
		return uuid.Nil, fmt.Errorf("not_found")
	}
	return id, err
}
