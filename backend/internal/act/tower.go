package act

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// Case is a work-queue item.
type Case struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	Kind      string     `json:"kind" db:"kind"`
	Title     string     `json:"title" db:"title"`
	Status    string     `json:"status" db:"status"`
	Priority  int        `json:"priority" db:"priority"`
	Score     float64    `json:"score" db:"score"`
	Assignee  string     `json:"assignee" db:"assignee"`
	Entity    string     `json:"entity" db:"entity"`
	EntityID  string     `json:"entityId" db:"entity_id"`
	Source    string     `json:"source" db:"source"`
	SourceID  string     `json:"sourceId" db:"source_id"`
	Notes     []any      `json:"notes" db:"notes"`
	DueAt     *time.Time `json:"dueAt,omitempty" db:"due_at"`
	CreatedAt time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time  `json:"updatedAt" db:"updated_at"`
}

const caseCols = `id, kind, title, status, priority, score, assignee, entity, entity_id, source, source_id, notes, due_at, created_at, updated_at`

// slaHours is the target resolution time per priority.
var slaHours = map[int]int{1: 4, 2: 24, 3: 72, 4: 168}

// OpenCase creates a case (or returns the open one for the same source).
func OpenCase(ctx context.Context, tx pgx.Tx, in caseInput) (Case, error) {
	if in.Priority < 1 || in.Priority > 4 {
		in.Priority = 3
	}
	if in.Title == "" {
		return Case{}, httpx.BadRequest("title is required")
	}
	if in.Kind == "" {
		in.Kind = "manual"
	}
	due := time.Now().Add(time.Duration(slaHours[in.Priority]) * time.Hour)
	c, err := db.One[Case](ctx, tx, `insert into work_case (id, tenant_id, kind, title, priority, score, entity, entity_id, source, source_id, due_at)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10)
		on conflict (tenant_id, source, source_id) where source_id <> '' and status <> 'resolved' do update set updated_at=now()
		returning `+caseCols, ids.New(), in.Kind, in.Title, in.Priority, float64(5-in.Priority)*25, in.Entity, in.EntityID, in.Source, in.SourceID, due)
	if err != nil {
		return c, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "case.open", Entity: "work_case", EntityID: c.ID.String(), After: c})
	return c, err
}

// queue orders open cases by priority, SLA breach and age (the prioritized work queue).
const queueSQL = `select ` + caseCols + ` from work_case where status <> 'resolved'
	order by priority, (due_at < now()) desc, created_at limit 200`

func (m *Module) listCases(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var items []Case
		var err error
		if status == "" || status == "open" {
			items, err = db.List[Case](r.Context(), tx, queueSQL)
		} else {
			items, err = db.List[Case](r.Context(), tx, `select `+caseCols+` from work_case where ($1='all' or status=$1) order by updated_at desc limit 500`, status)
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Case]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createCase(w http.ResponseWriter, r *http.Request) error {
	var in caseInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Source = "manual"
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		c, err := OpenCase(r.Context(), tx, in)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, c)
		}
		return err
	})
}

func (m *Module) updateCase(w http.ResponseWriter, r *http.Request) error {
	id, err := idsParam(r)
	if err != nil {
		return err
	}
	var in struct {
		Status   *string `json:"status"`
		Priority *int    `json:"priority"`
		Assignee *string `json:"assignee"`
		Note     string  `json:"note"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Status != nil && *in.Status != "open" && *in.Status != "in_progress" && *in.Status != "resolved" {
		return httpx.BadRequest("status must be open, in_progress or resolved")
	}
	if in.Priority != nil && (*in.Priority < 1 || *in.Priority > 4) {
		return httpx.BadRequest("priority must be 1-4")
	}
	p := auth.MustFrom(r.Context())
	var note any
	if strings.TrimSpace(in.Note) != "" {
		note = []map[string]string{{"by": p.Actor(), "at": time.Now().UTC().Format(time.RFC3339), "text": in.Note}}
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		c, err := db.One[Case](r.Context(), tx, `update work_case set status=coalesce($2,status), priority=coalesce($3,priority), assignee=coalesce($4,assignee),
			notes = case when $5::jsonb is null then notes else notes || $5::jsonb end, updated_at=now() where id=$1 returning `+caseCols, id, in.Status, in.Priority, in.Assignee, note)
		if err != nil {
			return err
		}
		if in.Status != nil && *in.Status == "resolved" && c.Source == "alert" {
			if _, err := tx.Exec(r.Context(), `update alert set status='resolved', resolved_at=now() where id::text=$1 and status='open'`, c.SourceID); err != nil {
				return err
			}
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "case.update", Entity: "work_case", EntityID: id.String(), After: in}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, c)
		return nil
	})
}

// Monitor is one control-tower signal.
type Monitor struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Value       float64 `json:"value"`
	Status      string  `json:"status"` // ok | warning | critical
	Description string  `json:"description"`
	Link        string  `json:"link"`
}

type monitorDef struct {
	id, name, desc, link, sql string
	warn, crit                float64
}

var monitors = []monitorDef{
	{"alerts_critical", "Critical alerts", "Open error-severity alerts", "/comply/alerts", `select count(*) from alert where status='open' and severity='error'`, 1, 5},
	{"regulatory_rejections", "Regulatory rejections (24 h)", "Hub rejections in the last 24 hours", "/comply/reports", `select count(*) from regulatory_report where status in ('rejected','failed') and updated_at > now() - interval '24 hours'`, 1, 10},
	{"epcis_rejected", "Rejected EPCIS documents (24 h)", "Capture documents rejected with errors", "/trace/inbox", `select count(*) from epcis_capture where status='rejected' and created_at > now() - interval '24 hours'`, 1, 10},
	{"pools_low", "Serial pools running low", "Pools under their low watermark", "/trace/serialization", `select count(*) from serial_pool where (capacity - next_counter)::numeric * 100 < capacity::numeric * low_watermark_pct`, 1, 3},
	{"partners_down", "Partners down", "Active partners failing health checks", "/link/partners", `select count(*) from partner where status='active' and health->>'status'='down'`, 1, 3},
	{"certs_expiring", "Supplier certificates expiring (30 d)", "Certificates expiring within 30 days", "/link/suppliers", `select count(*) from supplier_record where valid_to < current_date + 30`, 1, 5},
	{"shipments_exception", "Shipment exceptions", "Inbound shipments not matching their ASN", "/link/shipments", `select count(*) from shipment where status='exception'`, 1, 5},
	{"actions_pending", "Agent actions awaiting approval", "Proposals waiting for a human decision", "/act/approvals", `select count(*) from agent_action where status='proposed'`, 10, 50},
	{"cases_overdue", "Cases past SLA", "Open cases past their due time", "/act/cases", `select count(*) from work_case where status <> 'resolved' and due_at < now()`, 1, 5},
}

func (m *Module) controlTower(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		out := make([]Monitor, 0, len(monitors))
		for _, md := range monitors {
			var v float64
			if err := tx.QueryRow(r.Context(), md.sql).Scan(&v); err != nil {
				return err
			}
			st := "ok"
			switch {
			case v >= md.crit:
				st = "critical"
			case v >= md.warn:
				st = "warning"
			}
			out = append(out, Monitor{ID: md.id, Name: md.name, Value: v, Status: st, Description: md.desc, Link: md.link})
		}
		queue, err := db.List[Case](r.Context(), tx, queueSQL)
		if err != nil {
			return err
		}
		var pending int
		_ = tx.QueryRow(r.Context(), `select count(*) from agent_action where status='proposed'`).Scan(&pending)
		httpx.JSON(w, http.StatusOK, map[string]any{"monitors": out, "queue": queue, "cases": queue, "pendingActions": pending, "generatedAt": time.Now().UTC()})
		return nil
	})
}
