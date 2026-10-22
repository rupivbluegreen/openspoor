// Package trace maintains read projections over the EPCIS event store: the current state
// of every serial (status, location, parent) and the aggregation tree, and serves serial
// history and parent/child lookups.
package trace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/core/kernel"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// TopicProjected is published after a capture's projection completes.
const TopicProjected = "epcis.captured"

// Module is the trace bounded context.
type Module struct{ kernel.Deps }

// New creates the module.
func New(d kernel.Deps) *Module { return &Module{Deps: d} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "trace" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Route("/trace", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleOps, auth.RoleCompliance, auth.RoleIntegration))
		r.Get("/serials/*", httpx.Handler(m.serial))
		r.Get("/tree/*", httpx.Handler(m.tree))
		r.Get("/stats", httpx.Handler(m.stats))
		r.Get("/state", httpx.Handler(m.listState))
	})
}

// RegisterJobs implements kernel.Worker.
func (m *Module) RegisterJobs(r *jobs.Runner) { r.Handle("trace.project", m.projectJob) }

type change struct {
	status, disposition, bizLocation *string
	parentOp                         string // "", "set", "clear"
	parent                           string
	lot, expiry                      *string
	eventTime                        time.Time
	eventID                          string
}

func sp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// statusFor derives a lifecycle status from an event.
func statusFor(e *lib.Event) *string {
	switch {
	case e.Type == lib.ObjectEvent && e.Action == lib.ActionDelete:
		return sp("decommissioned")
	case e.Disposition != "":
		return sp(e.Disposition)
	case e.BizStep == "commissioning":
		return sp("active")
	}
	return nil
}

func ilmdString(e *lib.Event, keys ...string) *string {
	for _, k := range keys {
		if v, ok := e.ILMD[k].(string); ok && v != "" {
			return &v
		}
		if v, ok := e.ILMD["cbvmda:"+k].(string); ok && v != "" {
			return &v
		}
	}
	return nil
}

// Fold computes the final per-EPC state change of a batch of events (ordered by event time).
func Fold(events []lib.Event) map[string]*change {
	sort.SliceStable(events, func(i, j int) bool { return events[i].EventTime.Before(events[j].EventTime) })
	out := map[string]*change{}
	get := func(epc string, e *lib.Event) *change {
		c := out[epc]
		if c == nil {
			c = &change{}
			out[epc] = c
		}
		c.eventTime, c.eventID = e.EventTime, e.EventID
		return c
	}
	apply := func(c *change, e *lib.Event) {
		if s := statusFor(e); s != nil {
			c.status = s
		}
		if e.Disposition != "" {
			c.disposition = sp(e.Disposition)
		}
		if e.BizLocation != nil && e.BizLocation.ID != "" {
			c.bizLocation = sp(lib.Canonical(e.BizLocation.ID))
		}
	}
	for i := range events {
		e := &events[i]
		switch e.Type {
		case lib.ObjectEvent:
			for _, id := range e.EPCList {
				c := get(lib.Canonical(id), e)
				apply(c, e)
				if e.BizStep == "commissioning" {
					if l := ilmdString(e, "lotNumber"); l != nil {
						c.lot = l
					}
					if x := ilmdString(e, "itemExpirationDate"); x != nil {
						c.expiry = x
					}
				}
			}
		case lib.AggregationEvent:
			parent := lib.Canonical(e.ParentID)
			if parent != "" {
				apply(get(parent, e), e)
			}
			for _, id := range e.ChildEPCs {
				c := get(lib.Canonical(id), e)
				switch e.Action {
				case lib.ActionAdd, lib.ActionObserve:
					if parent != "" {
						c.parentOp, c.parent = "set", parent
					}
				case lib.ActionDelete:
					c.parentOp, c.parent = "clear", ""
				}
			}
		case lib.TransactionEvent:
			for _, id := range append(append([]string{}, e.EPCList...), e.ParentID) {
				if id != "" {
					apply(get(lib.Canonical(id), e), e)
				}
			}
		case lib.TransformationEvent:
			for _, id := range e.InputEPCList {
				c := get(lib.Canonical(id), e)
				c.status = sp("transformed")
			}
			for _, id := range e.OutputEPCList {
				c := get(lib.Canonical(id), e)
				apply(c, e)
				if c.status == nil {
					c.status = sp("active")
				}
			}
		case lib.AssociationEvent:
			parent := lib.Canonical(e.ParentID)
			for _, id := range e.ChildEPCs {
				c := get(lib.Canonical(id), e)
				if e.Action == lib.ActionDelete {
					c.parentOp = "clear"
				} else {
					c.parentOp, c.parent = "set", parent
				}
			}
		}
	}
	return out
}

type capturePayload struct {
	CaptureID uuid.UUID `json:"captureId"`
}

func (m *Module) projectJob(ctx context.Context, j jobs.Job) error {
	var p capturePayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent{Err: err}
	}
	n, err := m.Project(ctx, *j.TenantID, p.CaptureID)
	if err != nil {
		return err
	}
	m.Bus.Publish(ctx, TopicProjected, *j.TenantID, p.CaptureID.String(), map[string]any{"captureId": p.CaptureID, "serialsUpdated": n})
	return nil
}

// Project applies one capture's events to serial_state. Updates never move a serial
// back in time: a row only changes when the incoming event is not older than its last event.
func (m *Module) Project(ctx context.Context, tenant uuid.UUID, captureID uuid.UUID) (int, error) {
	var updated int
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select doc, event_type, action, parent_id, epcs from epcis_event where tenant_id = app_tenant() and capture_id=$1`, captureID)
		if err != nil {
			return err
		}
		var events []lib.Event
		var unpackAll []string // AggregationEvent DELETE without childEPCs
		for rows.Next() {
			var doc []byte
			var typ string
			var action, parent *string
			var epcs []string
			if err := rows.Scan(&doc, &typ, &action, &parent, &epcs); err != nil {
				rows.Close()
				return err
			}
			var e lib.Event
			if err := json.Unmarshal(doc, &e); err != nil {
				rows.Close()
				return err
			}
			if typ == lib.AggregationEvent && action != nil && *action == lib.ActionDelete && len(e.ChildEPCs) == 0 && parent != nil {
				unpackAll = append(unpackAll, *parent)
			}
			events = append(events, e)
		}
		rows.Close()
		changes := Fold(events)
		keys := make([]string, 0, len(changes))
		for k := range changes {
			keys = append(keys, k)
		}
		sort.Strings(keys) // stable lock order avoids deadlocks between concurrent projections
		const batch = 20000
		for lo := 0; lo < len(keys); lo += batch {
			hi := min(lo+batch, len(keys))
			n := hi - lo
			epc, gtin, serial := make([]string, n), make([]*string, n), make([]*string, n)
			status, disp, loc, pop, par, lot, exp, eid := make([]*string, n), make([]*string, n), make([]*string, n), make([]string, n), make([]*string, n), make([]*string, n), make([]*string, n), make([]string, n)
			et := make([]time.Time, n)
			for i, k := range keys[lo:hi] {
				c := changes[k]
				g, s, l := lib.Key(k)
				epc[i], gtin[i], serial[i] = k, sp(g), sp(s)
				status[i], disp[i], loc[i], pop[i], par[i] = c.status, c.disposition, c.bizLocation, c.parentOp, sp(c.parent)
				lot[i], exp[i], et[i], eid[i] = c.lot, c.expiry, c.eventTime, c.eventID
				if lot[i] == nil && l != "" {
					lot[i] = sp(l)
				}
			}
			args := []any{epc, gtin, serial, lot, exp, status, disp, loc, pop, par, et, eid}
			const src = `unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::text[], $11::timestamptz[], $12::text[])
				  as x(epc, gtin, serial, lot, exp, status, disp, loc, pop, par, et, eid)`
			urows, err := tx.Query(ctx, `update serial_state s set
				  lot = coalesce(x.lot, s.lot),
				  expiry = coalesce(case when x.exp ~ '^\d{4}-\d{2}-\d{2}$' then x.exp::date end, s.expiry),
				  status = coalesce(x.status, s.status),
				  disposition = coalesce(x.disp, s.disposition),
				  biz_location = coalesce(x.loc, s.biz_location),
				  parent_epc = case x.pop when 'set' then x.par when 'clear' then null else s.parent_epc end,
				  last_event_time = x.et, last_event_id = x.eid, updated_at = now()
				from `+src+`
				where s.tenant_id = app_tenant() and s.epc = x.epc and s.last_event_time <= x.et
				returning s.epc`, args...)
			if err != nil {
				return err
			}
			done := map[string]bool{}
			for urows.Next() {
				var e string
				if err := urows.Scan(&e); err != nil {
					urows.Close()
					return err
				}
				done[e] = true
			}
			urows.Close()
			if err := urows.Err(); err != nil {
				return err
			}
			updated += len(done)
			if len(done) == n {
				continue
			}
			ct, err := tx.Exec(ctx, `insert into serial_state (tenant_id, epc, gtin, serial, lot, expiry, status, disposition, biz_location, parent_epc, last_event_time, last_event_id)
				select app_tenant(), x.epc, x.gtin, x.serial, x.lot, (case when x.exp ~ '^\d{4}-\d{2}-\d{2}$' then x.exp::date end),
				       coalesce(x.status, 'observed'), x.disp, x.loc, case when x.pop = 'set' then x.par end, x.et, x.eid
				from `+src+` where not (x.epc = any($13::text[]))
				on conflict (tenant_id, epc) do nothing`, append(args, keysOf(done))...)
			if err != nil {
				return err
			}
			updated += int(ct.RowsAffected())
		}
		if len(unpackAll) > 0 {
			if _, err := tx.Exec(ctx, `update serial_state set parent_epc=null, updated_at=now() where parent_epc = any($1)`, unpackAll); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `update epcis_capture set status='completed', finished_at=now() where id=$1`, captureID)
		return err
	})
	return updated, err
}

// State is a serial's projected state.
type State struct {
	EPC           string     `json:"epc" db:"epc"`
	GTIN          *string    `json:"gtin,omitempty" db:"gtin"`
	Serial        *string    `json:"serial,omitempty" db:"serial"`
	Lot           *string    `json:"lot,omitempty" db:"lot"`
	Expiry        *time.Time `json:"expiry,omitempty" db:"expiry"`
	Status        string     `json:"status" db:"status"`
	Disposition   *string    `json:"disposition,omitempty" db:"disposition"`
	BizLocation   *string    `json:"bizLocation,omitempty" db:"biz_location"`
	ParentEPC     *string    `json:"parentEpc,omitempty" db:"parent_epc"`
	LastEventTime time.Time  `json:"lastEventTime" db:"last_event_time"`
	LastEventID   string     `json:"lastEventId" db:"last_event_id"`
}

const stateCols = `epc, gtin, serial, lot, expiry, status, disposition, biz_location, parent_epc, last_event_time, last_event_id`

// Lookup returns the state of one identifier (any form) inside tx.
func Lookup(ctx context.Context, tx pgx.Tx, id string) (State, error) {
	return db.One[State](ctx, tx, `select `+stateCols+` from serial_state where tenant_id = app_tenant() and epc=$1`, lib.Canonical(id))
}

// Ancestors walks up the aggregation chain (nearest first).
func Ancestors(ctx context.Context, tx pgx.Tx, s State) ([]State, error) {
	var out []State
	cur := s
	for i := 0; i < 10 && cur.ParentEPC != nil; i++ {
		p, err := db.One[State](ctx, tx, `select `+stateCols+` from serial_state where tenant_id = app_tenant() and epc=$1`, *cur.ParentEPC)
		if err != nil {
			break
		}
		out = append(out, p)
		cur = p
	}
	return out, nil
}

// History returns every event touching the identifier, oldest first.
func History(ctx context.Context, tx pgx.Tx, id string, limit int) ([]lib.Event, error) {
	rows, err := tx.Query(ctx, `select doc from epcis_event where tenant_id = app_tenant() and epcs @> array[$1]::text[] order by event_time limit $2`, lib.Canonical(id), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lib.Event
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var e lib.Event
		if err := json.Unmarshal(doc, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func epcParam(r *http.Request) string {
	raw := chi.URLParam(r, "*")
	if v, err := url.PathUnescape(raw); err == nil {
		raw = v
	}
	// Digital Links lose a slash in "https://" when routed as a path.
	if len(raw) > 7 && raw[:7] == "https:/" && raw[7] != '/' {
		raw = "https://" + raw[7:]
	}
	if q := r.URL.Query().Get("id"); q != "" {
		raw = q
	}
	return raw
}

func (m *Module) serial(w http.ResponseWriter, r *http.Request) error {
	id := epcParam(r)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		st, err := Lookup(r.Context(), tx, id)
		if err != nil {
			return httpx.NotFound("no events for " + id)
		}
		anc, _ := Ancestors(r.Context(), tx, st)
		hist, err := History(r.Context(), tx, id, 1000)
		if err != nil {
			return err
		}
		kids, err := db.List[State](r.Context(), tx, `select `+stateCols+` from serial_state where tenant_id = app_tenant() and parent_epc=$1 order by epc limit 1000`, st.EPC)
		if err != nil {
			return err
		}
		urn := ""
		if g, s, _ := lib.Key(st.EPC); g != "" && s != "" {
			urn = "" // URN form needs the company prefix length; clients can call /codes/decode
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"epc": st.EPC, "urn": urn, "state": st, "ancestors": anc, "children": kids, "history": hist})
		return nil
	})
}

// Node is an aggregation tree node.
type Node struct {
	EPC      string `json:"epc"`
	Status   string `json:"status"`
	Children []Node `json:"children,omitempty"`
}

func (m *Module) tree(w http.ResponseWriter, r *http.Request) error {
	id := lib.Canonical(epcParam(r))
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		st, err := db.One[State](r.Context(), tx, `select `+stateCols+` from serial_state where tenant_id = app_tenant() and epc=$1`, id)
		if err != nil {
			return httpx.NotFound("unknown identifier")
		}
		// Recursive CTE over the parent links, depth-limited.
		rows, err := tx.Query(r.Context(), `with recursive t(epc, parent_epc, status, depth) as (
				select epc, parent_epc, status, 0 from serial_state where tenant_id = app_tenant() and epc=$1
				union all
				select s.epc, s.parent_epc, s.status, t.depth+1 from serial_state s join t on s.tenant_id = app_tenant() and s.parent_epc = t.epc where t.depth < 6
			) select epc, parent_epc, status from t limit 50000`, st.EPC)
		if err != nil {
			return err
		}
		kids := map[string][]Node{}
		status := map[string]string{}
		for rows.Next() {
			var e, s string
			var p *string
			if err := rows.Scan(&e, &p, &s); err != nil {
				rows.Close()
				return err
			}
			status[e] = s
			if p != nil && e != st.EPC {
				kids[*p] = append(kids[*p], Node{EPC: e, Status: s})
			}
		}
		rows.Close()
		var build func(epc string, depth int) Node
		build = func(epc string, depth int) Node {
			n := Node{EPC: epc, Status: status[epc]}
			if depth > 6 {
				return n
			}
			for _, c := range kids[epc] {
				n.Children = append(n.Children, build(c.EPC, depth+1))
			}
			return n
		}
		httpx.JSON(w, http.StatusOK, build(st.EPC, 0))
		return nil
	})
}

func (m *Module) stats(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var events, captures24, rejected24 int64
		if err := tx.QueryRow(r.Context(), `select count(*) from epcis_event`).Scan(&events); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `select count(*) filter (where status <> 'rejected'), count(*) filter (where status='rejected')
			from epcis_capture where created_at > now() - interval '24 hours'`).Scan(&captures24, &rejected24); err != nil {
			return err
		}
		byStatus := map[string]int64{}
		var serials int64
		rows, err := tx.Query(r.Context(), `select status, count(*) from serial_state group by status`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s string
			var n int64
			if err := rows.Scan(&s, &n); err != nil {
				rows.Close()
				return err
			}
			byStatus[s] = n
			serials += n
		}
		rows.Close()
		type day struct {
			Day    string `json:"day"`
			Events int64  `json:"events"`
		}
		var perDay []day
		drows, err := tx.Query(r.Context(), `select to_char(date_trunc('day', record_time), 'YYYY-MM-DD'), count(*) from epcis_event
			where record_time > now() - interval '14 days' group by 1 order by 1`)
		if err != nil {
			return err
		}
		for drows.Next() {
			var d day
			if err := drows.Scan(&d.Day, &d.Events); err != nil {
				drows.Close()
				return err
			}
			perDay = append(perDay, d)
		}
		drows.Close()
		httpx.JSON(w, http.StatusOK, map[string]any{"events": events, "serials": serials, "captures24h": captures24, "rejected24h": rejected24,
			"byStatus": byStatus, "eventsPerDay": perDay})
		return nil
	})
}

func (m *Module) listState(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := httpx.IntQuery(r, "limit", 100, 1, 1000)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[State](r.Context(), tx, `select `+stateCols+` from serial_state where tenant_id = app_tenant() and ($1='' or gtin=$1) and ($2='' or status=$2) and ($3='' or lot=$3)
			order by last_event_time desc limit $4`, q.Get("gtin"), q.Get("status"), q.Get("lot"), limit)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[State]{Items: items, Total: len(items)})
		}
		return err
	})
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Delta is the exported view of what a batch of events implies for one identifier.
type Delta struct {
	Status      string `json:"status,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	Parent      string `json:"parent,omitempty"`
	ParentSet   bool   `json:"parentSet"`
	ParentClear bool   `json:"parentCleared"`
}

// FinalStates folds events (any order) into the final state each identifier should have.
func FinalStates(events []lib.Event) map[string]Delta {
	out := map[string]Delta{}
	for k, c := range Fold(events) {
		d := Delta{ParentSet: c.parentOp == "set", ParentClear: c.parentOp == "clear", Parent: c.parent}
		if c.status != nil {
			d.Status = *c.status
		}
		if c.disposition != nil {
			d.Disposition = *c.disposition
		}
		out[k] = d
	}
	return out
}
