package act

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
)

// investigation-assistant watches open DSCSA investigations that are close to their due date, or
// have gone a day without evidence, and proposes assembling the tracing answer (through the dscsa
// module's own function) with a list of what is still missing. It never changes an
// investigation's status, closes it, confirms a finding or signs: a person reviews the answer and
// signs. When the answer already exists but evidence is missing, it only opens a case that lists
// the gaps. The due dates are the tenant's investigation policy (dscsa module), not rules of this agent.
const (
	investigationDueSoon = 24 * time.Hour // "approaching": due within this window, or overdue
	investigationNoEvid  = 24 * time.Hour // an investigation this old without evidence is flagged
)

type investigationAssistant struct{}

func (investigationAssistant) polished()    {}
func (investigationAssistant) Name() string { return "investigation-assistant" }
func (investigationAssistant) Description() string {
	return "For open DSCSA investigations that are near their due date or lack evidence, proposes assembling the tracing answer and lists what is missing. A person reviews and signs; the agent never closes or confirms anything."
}

func (investigationAssistant) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `select id, kind, status, title, due_at, answered_at, created_at, findings, cardinality(epcs), cardinality(not_found)
		from investigation where status <> 'closed' order by due_at nulls last, created_at limit 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var id uuid.UUID
		var kind, status, title string
		var due, answered *time.Time
		var created time.Time
		var raw []byte
		var scope, notFound int
		if err := rows.Scan(&id, &kind, &status, &title, &due, &answered, &created, &raw, &scope, &notFound); err != nil {
			return nil, err
		}
		var findings []struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(raw, &findings)
		evidence := false
		for _, f := range findings {
			evidence = evidence || f.Kind == "evidence" || f.Kind == "verification"
		}
		now := time.Now()
		dueSoon := due != nil && due.Before(now.Add(investigationDueSoon))
		stale := !evidence && now.Sub(created) >= investigationNoEvid
		if !dueSoon && !stale {
			continue
		}
		var missing, reasoning []string
		label := title
		if label == "" {
			label = kind + " " + id.String()[:8]
		}
		reasoning = append(reasoning, fmt.Sprintf("Investigation %q (%s, status %s).", label, kind, status))
		if due != nil {
			if due.Before(now) {
				reasoning = append(reasoning, fmt.Sprintf("Overdue by %s (due %s).", dur(now.Sub(*due)), due.UTC().Format("2006-01-02 15:04 UTC")))
			} else {
				reasoning = append(reasoning, fmt.Sprintf("Due in %s (%s).", dur(due.Sub(now)), due.UTC().Format("2006-01-02 15:04 UTC")))
			}
		}
		if answered == nil {
			missing = append(missing, "the tracing answer has not been assembled")
		}
		if !evidence {
			missing = append(missing, "no evidence or verification finding is recorded")
		}
		if notFound > 0 {
			missing = append(missing, fmt.Sprintf("%d identifiers in scope were not found in the serial records", notFound))
		}
		if scope == 0 {
			missing = append(missing, "no identifiers are in scope")
		}
		if len(missing) > 0 {
			reasoning = append(reasoning, "Missing: "+strings.Join(missing, "; ")+".")
		}
		reasoning = append(reasoning, "The agent only assembles a draft of the answer; a person reviews it, records findings and signs.")
		prio := 3
		if dueSoon {
			prio = 2
		}
		if answered == nil && scope > 0 {
			out = append(out, Proposal{Tool: "investigation.assemble_answer", Title: "Assemble tracing answer: " + label,
				Summary:   "Assemble the tracing answer from the stored events and transactions so a person can review it before the due date.",
				Reasoning: reasoning, Input: map[string]any{"investigationId": id},
				Preview:    map[string]any{"identifiers": scope, "notFound": notFound, "missing": missing},
				Confidence: 0.9, Entity: "investigation", EntityID: id.String(), DedupeKey: "investigation-answer:" + id.String()})
			continue
		}
		if len(missing) == 0 {
			continue
		}
		ct := "Investigation needs attention: " + label
		out = append(out, Proposal{Tool: "case.open", Title: ct, Summary: "List what is missing so the investigation can be completed.", Reasoning: reasoning,
			Input:   caseInput{Kind: "investigation.missing", Title: ct, Priority: prio, Entity: "investigation", EntityID: id.String(), Source: "investigation-assistant", SourceID: id.String(), Notes: reasoning},
			Preview: map[string]any{"priority": prio, "missing": missing}, Confidence: 0.8, Entity: "investigation", EntityID: id.String(), DedupeKey: "investigation-missing:" + id.String()})
	}
	return out, rows.Err()
}

func dur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// --- tool ---

type investigationAnswerTool struct{ m *Module }

func (investigationAnswerTool) Name() string { return "investigation.assemble_answer" }
func (investigationAnswerTool) Scope() []string {
	return []string{auth.RoleCompliance}
}
func (investigationAnswerTool) Risk() string { return "low" }

// Reversible is false: storing the answer is what lets a tracing request close, and it is
// recorded on the investigation's timeline; assembling again simply replaces it.
func (investigationAnswerTool) Reversible() bool { return false }
func (t investigationAnswerTool) Execute(ctx context.Context, tx pgx.Tx, input json.RawMessage) (any, any, error) {
	var in struct {
		InvestigationID uuid.UUID `json:"investigationId"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, nil, err
	}
	if t.m.Tracing == nil {
		return nil, nil, errors.New("the tracing module is not available")
	}
	var prev *time.Time
	if err := tx.QueryRow(ctx, `select answered_at from investigation where id=$1`, in.InvestigationID).Scan(&prev); err != nil {
		return nil, nil, httpx.NotFound("unknown investigation")
	}
	_, summary, err := t.m.Tracing.Assemble(ctx, tx, in.InvestigationID)
	return map[string]any{"answeredAt": prev}, summary, err
}
func (investigationAnswerTool) Revert(context.Context, pgx.Tx, json.RawMessage, json.RawMessage) error {
	return httpx.Conflict("an assembled tracing answer cannot be reverted; assemble it again to replace it")
}
