// Package migration helps companies move off a legacy track-and-trace system: import serial
// numbers and their history from CSV exports or EPCIS (1.2/2.0) documents, and run in shadow
// mode — feed Open Spoor the same Level 3 input as the legacy system, then diff the legacy
// system's output against Open Spoor's state and report every mismatch.
package migration

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Module is the migration bounded context.
type Module struct {
	kernel.Deps
	EPCIS *epcis.Module
	Trace *trace.Module
}

// New creates the module.
func New(d kernel.Deps, ep *epcis.Module, tr *trace.Module) *Module {
	return &Module{Deps: d, EPCIS: ep, Trace: tr}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "migration" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleIntegration))
		r.Post("/migration/serials", httpx.Handler(m.importSerials))
		r.Post("/migration/epcis", httpx.Handler(m.importEPCIS))
		r.Post("/shadow/runs", httpx.Handler(m.shadowRun))
		r.Get("/shadow/runs", httpx.Handler(m.listRuns))
		r.Get("/shadow/runs/{id}", httpx.Handler(m.getRun))
	})
}

// ImportResult summarizes a migration import.
type ImportResult struct {
	Rows       int         `json:"rows"`
	Events     int         `json:"events"`
	CaptureIDs []uuid.UUID `json:"captureIds"`
	Errors     []string    `json:"errors"`
}

type serialRow struct {
	gtin, serial, lot, expiry, status, parent string
	at                                        time.Time
}

// importSerials reads CSV with header gtin,serial[,lot,expiry,status,parent,event_time] and
// turns it into commissioning, aggregation and decommissioning events (source "migration").
// Status values: active (default), dispensed, destroyed, decommissioned, recalled, expired.
func (m *Module) importSerials(w http.ResponseWriter, r *http.Request) error {
	cr := csv.NewReader(io.LimitReader(r.Body, 256<<20))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	head, err := cr.Read()
	if err != nil {
		return httpx.BadRequest("empty CSV")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	if _, ok := col["gtin"]; !ok {
		return httpx.BadRequest("CSV needs gtin and serial columns")
	}
	if _, ok := col["serial"]; !ok {
		return httpx.BadRequest("CSV needs gtin and serial columns")
	}
	get := func(rec []string, k string) string {
		if i, ok := col[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	res := ImportResult{Errors: []string{}}
	var rows []serialRow
	line := 1
	now := time.Now().UTC().Truncate(time.Second)
	for {
		rec, err := cr.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		sr := serialRow{gtin: gs1.NormalizeGTIN(get(rec, "gtin")), serial: get(rec, "serial"), lot: get(rec, "lot"), expiry: get(rec, "expiry"),
			status: strings.ToLower(get(rec, "status")), parent: get(rec, "parent"), at: now}
		if !gs1.ValidGTIN(sr.gtin) || sr.serial == "" || len(sr.serial) > 20 {
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: invalid gtin or serial", line))
			continue
		}
		if v := get(rec, "event_time"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("line %d: event_time must be RFC 3339", line))
				continue
			}
			sr.at = t
		}
		if sr.expiry != "" {
			if _, err := time.Parse("2006-01-02", sr.expiry); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("line %d: expiry must be YYYY-MM-DD", line))
				continue
			}
		}
		rows = append(rows, sr)
	}
	res.Rows = len(rows)
	if len(rows) == 0 {
		httpx.JSON(w, http.StatusOK, res)
		return nil
	}
	events := Events(rows)
	res.Events = len(events)
	ctx := epcis.WithMigration(r.Context())
	for i := 0; i < len(events); i += 20_000 {
		doc, _ := lib.WriteJSON(lib.NewDocument(events[i:min(i+20_000, len(events))]))
		c, err := m.EPCIS.Capture(ctx, "application/ld+json", doc, "migration:csv")
		var ce *epcis.CaptureError
		if errors.As(err, &ce) {
			for _, fe := range ce.Capture.Errors {
				res.Errors = append(res.Errors, fe.Error())
			}
			break
		}
		if err != nil {
			return err
		}
		res.CaptureIDs = append(res.CaptureIDs, c.ID)
	}
	if err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "migration.import.serials", Entity: "migration", EntityID: "csv", After: map[string]any{"rows": res.Rows, "events": res.Events, "errors": len(res.Errors)}})
		return err
	}); err != nil {
		return err
	}
	status := http.StatusOK
	if len(res.Errors) > 0 && len(res.CaptureIDs) == 0 {
		status = http.StatusUnprocessableEntity
	}
	httpx.JSON(w, status, res)
	return nil
}

var finalStep = map[string][2]string{"dispensed": {"dispensing", "dispensed"}, "destroyed": {"destroying", "destroyed"}, "recalled": {"holding", "recalled"},
	"expired": {"holding", "expired"}, "decommissioned": {"decommissioning", ""}}

// Events converts legacy serial rows into EPCIS events (exported for tests and tooling).
func Events(rows []serialRow) []lib.Event {
	type key struct {
		gtin, lot, exp string
		at             time.Time
	}
	groups := map[key][]string{}
	var order []key
	parents := map[string][]string{}
	final := map[string][]string{}
	var cpLen = 7
	for _, r := range rows {
		u, err := gs1.SGTINURN(r.gtin, r.serial, cpLen)
		if err != nil {
			continue
		}
		k := key{r.gtin, r.lot, r.expiry, r.at}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], u)
		if r.parent != "" {
			parents[r.parent] = append(parents[r.parent], u)
		}
		if _, ok := finalStep[r.status]; ok {
			final[r.status] = append(final[r.status], u)
		}
	}
	var out []lib.Event
	var latest time.Time
	for _, k := range order {
		epcs := groups[k]
		for i := 0; i < len(epcs); i += 1000 {
			e := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: k.at, EventTimeZoneOffset: "+00:00",
				EPCList: epcs[i:min(i+1000, len(epcs))], Extensions: map[string]any{"openspoor:migrated": true}}
			if k.lot != "" || k.exp != "" {
				e.ILMD = map[string]any{}
				if k.lot != "" {
					e.ILMD["lotNumber"] = k.lot
				}
				if k.exp != "" {
					e.ILMD["itemExpirationDate"] = k.exp
				}
			}
			out = append(out, e)
		}
		if k.at.After(latest) {
			latest = k.at
		}
	}
	pkeys := make([]string, 0, len(parents))
	for p := range parents {
		pkeys = append(pkeys, p)
	}
	sort.Strings(pkeys)
	for _, p := range pkeys {
		out = append(out, lib.Event{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", EventTime: latest.Add(time.Second), EventTimeZoneOffset: "+00:00",
			ParentID: p, ChildEPCs: parents[p], Extensions: map[string]any{"openspoor:migrated": true}})
	}
	fkeys := make([]string, 0, len(final))
	for s := range final {
		fkeys = append(fkeys, s)
	}
	sort.Strings(fkeys)
	for _, s := range fkeys {
		st := finalStep[s]
		e := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: st[0], Disposition: st[1], EventTime: latest.Add(2 * time.Second), EventTimeZoneOffset: "+00:00",
			EPCList: final[s], Extensions: map[string]any{"openspoor:migrated": true}}
		if s == "decommissioned" {
			e.Action = "DELETE"
		}
		out = append(out, e)
	}
	return out
}

// importEPCIS loads legacy EPCIS history (1.2 XML or 2.0) without pool checks.
func (m *Module) importEPCIS(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, epcis.MaxDocumentBytes))
	if err != nil {
		return httpx.BadRequest("cannot read body")
	}
	c, err := m.EPCIS.Capture(epcis.WithMigration(r.Context()), r.Header.Get("Content-Type"), body, "migration:epcis")
	var ce *epcis.CaptureError
	if errors.As(err, &ce) {
		return httpx.Unprocessable("legacy document rejected", ce.Capture.Errors)
	}
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, c)
	return nil
}

// Mismatch is one difference between legacy output and Open Spoor state.
type Mismatch struct {
	EPC    string `json:"epc"`
	Field  string `json:"field"`
	Legacy string `json:"legacy"`
	Ours   string `json:"openSpoor"`
}

// Run is a shadow comparison.
type Run struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Label      string     `json:"label" db:"label"`
	Compared   int        `json:"compared" db:"compared"`
	Matched    int        `json:"matched" db:"matched"`
	Mismatches []Mismatch `json:"mismatches" db:"mismatches"`
	CreatedAt  time.Time  `json:"createdAt" db:"created_at"`
	Agreement  float64    `json:"agreementPct" db:"-"`
}

// Compare diffs the state implied by legacy events with Open Spoor's projected state.
func Compare(ctx context.Context, tx pgx.Tx, legacy []lib.Event) (int, []Mismatch, error) {
	want := trace.FinalStates(legacy)
	keys := make([]string, 0, len(want))
	for k := range want {
		if g, s, _ := lib.Key(k); (g != "" && s != "") || strings.Contains(k, "/00/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	ours := map[string]trace.State{}
	rows, err := tx.Query(ctx, `select epc, status, coalesce(parent_epc,'') from serial_state where tenant_id = app_tenant() and epc = any($1)`, keys)
	if err != nil {
		return 0, nil, err
	}
	for rows.Next() {
		var s trace.State
		var parent string
		if err := rows.Scan(&s.EPC, &s.Status, &parent); err != nil {
			rows.Close()
			return 0, nil, err
		}
		if parent != "" {
			s.ParentEPC = &parent
		}
		ours[s.EPC] = s
	}
	rows.Close()
	var out []Mismatch
	for _, k := range keys {
		w := want[k]
		o, ok := ours[k]
		if !ok {
			out = append(out, Mismatch{EPC: k, Field: "exists", Legacy: "yes", Ours: "no"})
			continue
		}
		if w.Status != "" && w.Status != o.Status {
			out = append(out, Mismatch{EPC: k, Field: "status", Legacy: w.Status, Ours: o.Status})
		}
		op := ""
		if o.ParentEPC != nil {
			op = *o.ParentEPC
		}
		switch {
		case w.ParentSet && w.Parent != op:
			out = append(out, Mismatch{EPC: k, Field: "parent", Legacy: w.Parent, Ours: op})
		case w.ParentClear && op != "":
			out = append(out, Mismatch{EPC: k, Field: "parent", Legacy: "", Ours: op})
		}
	}
	return len(keys), out, nil
}

// shadowRun accepts {label, legacyDocument} — the EPCIS output the legacy system produced
// for the same Level 3 input — and stores the comparison report.
func (m *Module) shadowRun(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Label          string `json:"label"`
		LegacyDocument string `json:"legacyDocument"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	doc, _, err := lib.Parse("", []byte(in.LegacyDocument))
	if err != nil {
		return httpx.BadRequest("legacy document: " + err.Error())
	}
	lib.ValidateDocument(doc)
	if in.Label == "" {
		in.Label = "shadow " + time.Now().UTC().Format(time.RFC3339)
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		n, mism, err := Compare(r.Context(), tx, doc.Body.EventList)
		if err != nil {
			return err
		}
		affected := map[string]bool{}
		for _, x := range mism {
			affected[x.EPC] = true
		}
		stored := mism
		if len(stored) > 1000 {
			stored = stored[:1000]
		}
		if stored == nil {
			stored = []Mismatch{}
		}
		run, err := db.One[Run](r.Context(), tx, `insert into shadow_run (id, tenant_id, label, compared, matched, mismatches) values ($1, app_tenant(), $2,$3,$4,$5)
			returning id, label, compared, matched, mismatches, created_at`, ids.New(), in.Label, n, n-len(affected), stored)
		if err != nil {
			return err
		}
		if len(mism) > 0 {
			if _, err := tx.Exec(r.Context(), `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id) values ($1, app_tenant(), 'migration.shadow_mismatch', 'warning', $2, $3, 'shadow_run', $4)`,
				ids.New(), fmt.Sprintf("Shadow run %q: %d of %d identifiers differ from the legacy system", in.Label, len(affected), n), map[string]any{"runId": run.ID}, run.ID.String()); err != nil {
				return err
			}
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "migration.shadow", Entity: "shadow_run", EntityID: run.ID.String(), After: map[string]any{"compared": n, "mismatches": len(mism)}}); err != nil {
			return err
		}
		run.fill()
		httpx.JSON(w, http.StatusCreated, run)
		return nil
	})
}

func (r *Run) fill() {
	if r.Compared > 0 {
		r.Agreement = float64(r.Matched) * 100 / float64(r.Compared)
	}
}

func (m *Module) listRuns(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Run](r.Context(), tx, `select id, label, compared, matched, '[]'::jsonb as mismatches, created_at from shadow_run order by created_at desc limit 200`)
		for i := range items {
			items[i].fill()
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Run]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getRun(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		run, err := db.One[Run](r.Context(), tx, `select id, label, compared, matched, mismatches, created_at from shadow_run where id=$1`, id)
		run.fill()
		if err == nil {
			httpx.JSON(w, http.StatusOK, run)
		}
		return err
	})
}
