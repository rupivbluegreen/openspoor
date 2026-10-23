package app

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/ratelimit"
)

// newLimiter builds the request limiter of the authenticated API from the configuration, or nil
// when limiting is switched off (OPENSPOOR_RATE_LIMIT=false).
func newLimiter(cfg config.Config, d *db.DB) (*ratelimit.Limiter, error) {
	if !cfg.RateLimit {
		return nil, nil
	}
	plans, err := ratelimit.ParsePlans(cfg.RateLimitPlans, ratelimit.DefaultPlans())
	if err != nil {
		return nil, err
	}
	slog.Info("api rate limits (plan=tenantRps/burst/keyRps/burst)", "plans", ratelimit.Describe(plans))
	return ratelimit.New(plans, func(ctx context.Context, tenant uuid.UUID) (string, error) {
		var plan string
		err := d.OperatorTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select plan from tenant where id=$1`, tenant).Scan(&plan)
		})
		return plan, err
	}), nil
}
