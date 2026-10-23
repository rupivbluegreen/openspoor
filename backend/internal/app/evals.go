package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/act"
)

// EvalAgents runs the golden datasets of the governed agents (all, or the named ones) and prints the
// score table on out. With a tenant slug the scores are also recorded for that tenant (audited), which
// is what the autonomy ladder consults. The datasets are synthetic: the table is not an accuracy figure.
func (a *App) EvalAgents(ctx context.Context, out io.Writer, tenantSlug string, agents []string) ([]act.AgentEval, error) {
	var ac *act.Module
	for _, m := range a.Modules {
		if x, ok := m.(*act.Module); ok {
			ac = x
		}
	}
	if ac == nil {
		return nil, errors.New("the act module is not loaded")
	}
	res, err := ac.RunEvals(ctx, agents...)
	if err != nil {
		return nil, err
	}
	act.FormatEvalTable(out, res)
	if tenantSlug == "" {
		return res, nil
	}
	var tenant uuid.UUID
	err = a.Deps.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id from tenant where slug=$1 and status='active'`, tenantSlug).Scan(&tenant)
	})
	if err != nil {
		return res, fmt.Errorf("tenant %q: %w", tenantSlug, err)
	}
	if err := ac.RecordEvals(ctx, tenant, "system:eval-agents", res); err != nil {
		return res, err
	}
	fmt.Fprintf(out, "Scores recorded for tenant %s.\n", tenantSlug)
	return res, nil
}
