package dscsa

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
)

// Typed entry points shared by the HTTP handlers and the governed agents in the act module
// (wired through small interfaces in internal/app). They apply the same checks as the UI.

// AssembleTracingAnswer builds the tracing answer for an open investigation, stores it on the
// investigation (which is what allows a tracing request to close), records the timeline event and
// the audit entry, and returns the stored JSON with a small summary. It never changes the status
// of the investigation and signs nothing.
func (m *Module) AssembleTracingAnswer(ctx context.Context, tx pgx.Tx, id uuid.UUID) (json.RawMessage, map[string]any, error) {
	inv, err := db.One[Investigation](ctx, tx, `select `+invCols+` from investigation where id=$1 for update`, id)
	if err != nil {
		return nil, nil, err
	}
	if inv.Status == StatusClosed {
		return nil, nil, httpx.Conflict("investigation is closed")
	}
	ans, err := m.BuildTracingAnswer(ctx, tx, inv)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(ans)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `update investigation set tracing_answer=$2, answered_at=now(), updated_at=now() where id=$1`, id, b); err != nil {
		return nil, nil, err
	}
	summary := map[string]any{"items": len(ans.Items), "transactions": len(ans.Transactions), "notFound": len(ans.NotFound), "truncated": ans.Truncated}
	if err := addEvent(ctx, tx, id, "tracing_answer", inv.Status, inv.Status, "", summary); err != nil {
		return nil, nil, err
	}
	if _, err := audit.Write(ctx, tx, audit.Record{Action: "investigation.tracing_answer", Entity: "investigation", EntityID: id.String(), After: summary}); err != nil {
		return nil, nil, err
	}
	return b, summary, nil
}

// PreviewTracingAnswer builds the tracing answer without storing anything (a read).
func (m *Module) PreviewTracingAnswer(ctx context.Context, tx pgx.Tx, id uuid.UUID) (json.RawMessage, map[string]any, error) {
	inv, err := db.One[Investigation](ctx, tx, `select `+invCols+` from investigation where id=$1`, id)
	if err != nil {
		return nil, nil, err
	}
	ans, err := m.BuildTracingAnswer(ctx, tx, inv)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(ans)
	return b, map[string]any{"items": len(ans.Items), "transactions": len(ans.Transactions), "notFound": len(ans.NotFound), "truncated": ans.Truncated}, err
}
