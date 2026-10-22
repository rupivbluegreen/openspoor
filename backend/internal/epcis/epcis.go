// Package epcis is the EPCIS 2.0 repository: capture (JSON-LD and XML, bulk documents),
// the standard query interface, named queries and webhook subscriptions, and validation
// against the CBV and master data.
package epcis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/serialization"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Job kinds enqueued in the capture transaction (transactional outbox).
const (
	JobProject    = "trace.project"  // handled by the trace module
	JobNotify     = "epcis.notify"   // webhook subscriptions
	JobArchive    = "epcis.archive"  // compressed document to S3
	TopicCaptured = "epcis.captured" // bus event after projection
)

// MaxDocumentBytes caps capture bodies (≈100k events of typical size fit comfortably).
const MaxDocumentBytes = 512 << 20

var (
	capturedEvents = promauto.NewCounter(prometheus.CounterOpts{Name: "openspoor_epcis_events_captured_total", Help: "EPCIS events persisted"})
	rejectedDocs   = promauto.NewCounter(prometheus.CounterOpts{Name: "openspoor_epcis_documents_rejected_total", Help: "EPCIS documents rejected"})
	captureSeconds = promauto.NewHistogram(prometheus.HistogramOpts{Name: "openspoor_epcis_capture_seconds", Help: "Capture latency (ack)",
		Buckets: prometheus.ExponentialBuckets(0.005, 2, 14)})
)

// Module is the EPCIS bounded context.
type Module struct {
	kernel.Deps
	Serials   *serialization.Module
	Client    *http.Client
	onCapture []string
	// Gate, when set, may refuse a capture before it is stored (SaaS quotas).
	Gate func(ctx context.Context, tenant uuid.UUID, events int) error
}

// OnCapture registers a job kind enqueued (payload {"captureId"}) in every capture
// transaction — the outbox through which other modules react to new events.
func (m *Module) OnCapture(kind string) { m.onCapture = append(m.onCapture, kind) }

// New creates the module.
func New(d kernel.Deps, s *serialization.Module) *Module {
	return &Module{Deps: d, Serials: s, Client: &http.Client{Timeout: 15 * time.Second}}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "epcis" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Route("/epcis", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleIntegration, auth.RoleOps, auth.RoleCompliance))
		r.Post("/capture", httpx.Handler(m.capture))
		r.Get("/capture", httpx.Handler(m.listCaptures))
		r.Get("/capture/{id}", httpx.Handler(m.getCapture))
		r.Get("/events", httpx.Handler(m.queryEvents))
		r.Get("/events/{eventID}", httpx.Handler(m.getEvent))
		r.Get("/queries", httpx.Handler(m.listQueries))
		r.Post("/queries", httpx.Handler(m.createQuery))
		r.Get("/queries/{name}/events", httpx.Handler(m.runNamedQuery))
		r.Get("/subscriptions", httpx.Handler(m.listSubs))
		r.Post("/subscriptions", httpx.Handler(m.createSub))
		r.Delete("/subscriptions/{id}", httpx.Handler(m.deleteSub))
	})
}

// RegisterJobs implements kernel.Worker.
func (m *Module) RegisterJobs(r *jobs.Runner) {
	r.Handle(JobNotify, m.notifyJob)
	r.Handle("epcis.deliver", m.deliverJob)
}

// Capture is a capture job record.
type Capture struct {
	ID         uuid.UUID          `json:"captureId" db:"id"`
	Status     string             `json:"status" db:"status"`
	Format     string             `json:"format" db:"format"`
	EventCount int                `json:"eventCount" db:"event_count"`
	ErrorCount int                `json:"errorCount" db:"error_count"`
	Errors     []lib.FieldError   `json:"errors" db:"errors"`
	Source     string             `json:"source" db:"source"`
	CreatedBy  string             `json:"createdBy" db:"created_by"`
	CreatedAt  time.Time          `json:"createdAt" db:"created_at"`
	FinishedAt *time.Time         `json:"finishedAt,omitempty" db:"finished_at"`
	Duplicates int                `json:"duplicatesSkipped" db:"-"`
	Timing     map[string]float64 `json:"timingMs,omitempty" db:"-"`
}

const captureCols = `id, status, format, event_count, error_count, errors, source, created_by, created_at, finished_at`

// CaptureError is returned when a document is rejected.
type CaptureError struct {
	Capture Capture
}

func (e *CaptureError) Error() string {
	return fmt.Sprintf("document rejected with %d errors", e.Capture.ErrorCount)
}

func (m *Module) capture(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxDocumentBytes+1))
	if err != nil {
		return httpx.BadRequest("cannot read body")
	}
	if len(body) > MaxDocumentBytes {
		return &httpx.Error{Status: http.StatusRequestEntityTooLarge, Msg: "document exceeds 512 MiB; split it"}
	}
	c, err := m.Capture(r.Context(), r.Header.Get("Content-Type"), body, r.Header.Get("X-Source"))
	var ce *CaptureError
	if errors.As(err, &ce) {
		w.Header().Set("Content-Type", "application/problem+json")
		httpx.JSON(w, http.StatusUnprocessableEntity, map[string]any{"title": "EPCIS document rejected", "status": 422,
			"captureId": ce.Capture.ID, "errors": ce.Capture.Errors, "eventCount": ce.Capture.EventCount})
		return nil
	}
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/epcis/capture/"+c.ID.String())
	httpx.JSON(w, http.StatusAccepted, c)
	return nil
}

type row struct {
	id                                         uuid.UUID
	eventID, typ, action, bizStep, disposition string
	eventTime                                  time.Time
	tz                                         string
	readPoint, bizLocation, parent             *string
	epcs                                       []string
	quantities, bizTx, srcDest, ilmd, doc      []byte
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func rawOrNil(v any, empty bool) []byte {
	if empty {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

type migrationKey struct{}

// WithMigration marks a capture as a legacy migration: serials need not come from Open Spoor
// pools (they were issued by the previous system); GTIN and double-commission checks still apply.
func WithMigration(ctx context.Context) context.Context {
	return context.WithValue(ctx, migrationKey{}, true)
}

func isMigration(ctx context.Context) bool { v, _ := ctx.Value(migrationKey{}).(bool); return v }

// Capture validates and persists a document atomically. Either every event is stored
// or none is and the per-event errors are returned (never silently dropped).
func (m *Module) Capture(ctx context.Context, contentType string, body []byte, source string) (Capture, error) {
	start := time.Now()
	p := auth.MustFrom(ctx)
	timing := map[string]float64{}
	mark := func(k string, t0 time.Time) { timing[k] = float64(time.Since(t0).Microseconds()) / 1000 }

	t0 := time.Now()
	doc, format, err := lib.Parse(contentType, body)
	if err != nil {
		return Capture{}, httpx.BadRequest(err.Error())
	}
	if len(doc.Body.EventList) == 0 {
		return Capture{}, httpx.BadRequest("document has no events")
	}
	mark("parse", t0)
	if m.Gate != nil {
		if err := m.Gate(ctx, p.TenantID, len(doc.Body.EventList)); err != nil {
			return Capture{}, err
		}
	}
	t0 = time.Now()
	errs := lib.ValidateDocument(doc)
	mark("validate", t0)
	c := Capture{ID: ids.New(), Format: format, EventCount: len(doc.Body.EventList), Source: source, CreatedBy: p.Actor(), Errors: []lib.FieldError{}}
	recordTime := time.Now().UTC().Truncate(time.Microsecond)

	err = m.DB.Tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		// Idempotent capture: events already stored (same eventID) are skipped before any
		// business rule runs, so re-sending a document is harmless.
		t0 := time.Now()
		eventIDs := make([]string, len(doc.Body.EventList))
		_ = lib.ParallelFor(len(eventIDs), func(i int) error {
			e := &doc.Body.EventList[i]
			if e.EventID == "" {
				e.EventID = lib.HashID(e)
			}
			eventIDs[i] = e.EventID
			return nil
		})
		dupRows, err := tx.Query(ctx, `select event_id from epcis_event where tenant_id = app_tenant() and event_id = any($1)`, eventIDs)
		if err != nil {
			return err
		}
		dups := map[string]bool{}
		for dupRows.Next() {
			var s string
			if err := dupRows.Scan(&s); err != nil {
				dupRows.Close()
				return err
			}
			dups[s] = true
		}
		dupRows.Close()
		total := len(doc.Body.EventList)
		if len(dups) > 0 {
			fresh := doc.Body.EventList[:0:0]
			for _, e := range doc.Body.EventList {
				if !dups[e.EventID] {
					fresh = append(fresh, e)
				}
			}
			doc.Body.EventList = fresh
		}
		mark("dedupe", t0)
		t0 = time.Now()
		if len(errs) == 0 {
			errs = m.validateBusiness(ctx, tx, doc)
		}
		mark("businessRules", t0)
		if len(errs) > 0 {
			return &CaptureError{}
		}
		t0 = time.Now()
		rows := make([]row, len(doc.Body.EventList))
		err = lib.ParallelFor(len(rows), func(i int) error {
			e := &doc.Body.EventList[i]
			if e.EventID == "" {
				e.EventID = lib.HashID(e)
			}
			e.RecordTime = &recordTime
			d, err := json.Marshal(e)
			if err != nil {
				return err
			}
			rw := row{id: ids.New(), eventID: e.EventID, typ: e.Type, action: e.Action, bizStep: e.BizStep, disposition: e.Disposition,
				eventTime: e.EventTime, tz: e.EventTimeZoneOffset, parent: strp(lib.Canonical(e.ParentID)), epcs: lib.CanonicalAll(e.AllEPCs()),
				quantities: rawOrNil(e.QuantityList, len(e.QuantityList) == 0), bizTx: rawOrNil(e.BizTransactionList, len(e.BizTransactionList) == 0),
				srcDest: rawOrNil(map[string]any{"source": e.SourceList, "destination": e.DestinationList}, len(e.SourceList)+len(e.DestinationList) == 0),
				ilmd:    rawOrNil(e.ILMD, len(e.ILMD) == 0), doc: d}
			if e.ReadPoint != nil {
				rw.readPoint = strp(lib.Canonical(e.ReadPoint.ID))
			}
			if e.BizLocation != nil {
				rw.bizLocation = strp(lib.Canonical(e.BizLocation.ID))
			}
			rows[i] = rw
			return nil
		})
		if err != nil {
			return err
		}
		mark("prepare", t0)
		t0 = time.Now()
		if _, err := tx.Exec(ctx, `select ensure_epcis_partition($1)`, recordTime); err != nil {
			return err
		}
		// COPY is not allowed on RLS tables: stage into a temp table, then INSERT…SELECT (RLS-checked).
		if _, err := tx.Exec(ctx, `create temp table epcis_stage (like epcis_event including defaults) on commit drop`); err != nil {
			return err
		}
		n, err := tx.CopyFrom(ctx, pgx.Identifier{"epcis_stage"},
			[]string{"id", "tenant_id", "capture_id", "event_id", "event_type", "action", "biz_step", "disposition", "event_time", "event_tz", "record_time",
				"read_point", "biz_location", "parent_id", "epcs", "quantities", "biz_transactions", "source_dest", "ilmd", "doc"},
			pgx.CopyFromFunc(func() func() ([]any, error) {
				i := 0
				return func() ([]any, error) {
					for i < len(rows) && dups[rows[i].eventID] {
						i++
					}
					if i >= len(rows) {
						return nil, nil
					}
					x := rows[i]
					i++
					return []any{x.id, p.TenantID, c.ID, x.eventID, x.typ, strp(x.action), strp(x.bizStep), strp(x.disposition), x.eventTime, x.tz, recordTime,
						x.readPoint, x.bizLocation, x.parent, x.epcs, x.quantities, x.bizTx, x.srcDest, x.ilmd, x.doc}, nil
				}
			}()))
		if err != nil {
			return err
		}
		mark("stageCopy", t0)
		t0 = time.Now()
		if _, err := tx.Exec(ctx, `insert into epcis_event select * from epcis_stage`); err != nil {
			return err
		}
		mark("copy", t0)
		c.Duplicates = total - int(n)
		c.Status = "accepted"
		t0 = time.Now()
		if _, err := tx.Exec(ctx, `insert into epcis_capture (id, tenant_id, status, format, event_count, error_count, errors, source, created_by)
			values ($1, app_tenant(), $2, $3, $4, 0, '[]', $5, $6)`, c.ID, c.Status, format, n, source, c.CreatedBy); err != nil {
			return err
		}
		c.EventCount = int(n)
		if err := m.commission(ctx, tx, doc); err != nil {
			return err
		}
		tid := p.TenantID
		payload := map[string]any{"captureId": c.ID}
		if _, err := jobs.Enqueue(ctx, tx, &tid, JobProject, payload); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, &tid, JobNotify, payload); err != nil {
			return err
		}
		for _, kind := range m.onCapture {
			if _, err := jobs.Enqueue(ctx, tx, &tid, kind, payload); err != nil {
				return err
			}
		}
		_, err = audit.Write(ctx, tx, audit.Record{Action: "epcis.capture", Entity: "epcis_capture", EntityID: c.ID.String(),
			After: map[string]any{"events": n, "duplicates": c.Duplicates, "format": format, "source": source}})
		mark("commit_prep", t0)
		return err
	})
	var ce *CaptureError
	if errors.As(err, &ce) {
		rejectedDocs.Inc()
		c.Status, c.Errors, c.ErrorCount = "rejected", errs, len(errs)
		if len(c.Errors) > 1000 {
			c.Errors = c.Errors[:1000]
		}
		_ = m.DB.Tx(ctx, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into epcis_capture (id, tenant_id, status, format, event_count, error_count, errors, source, created_by, finished_at)
				values ($1, app_tenant(), 'rejected', $2, $3, $4, $5, $6, $7, now())`, c.ID, format, c.EventCount, c.ErrorCount, c.Errors, source, c.CreatedBy)
			return err
		})
		return c, &CaptureError{Capture: c}
	}
	if err != nil {
		return c, err
	}
	capturedEvents.Add(float64(c.EventCount))
	captureSeconds.Observe(time.Since(start).Seconds())
	c.CreatedAt = recordTime
	timing["total"] = float64(time.Since(start).Microseconds()) / 1000
	c.Timing = timing
	m.Bus.Publish(ctx, "epcis.accepted", p.TenantID, c.ID.String(), map[string]any{"captureId": c.ID, "events": c.EventCount})
	return c, nil
}

// commission records commissioned serials in pool bitmaps.
func (m *Module) commission(ctx context.Context, tx pgx.Tx, doc *lib.Document) error {
	byGTIN := map[string][]string{}
	for i := range doc.Body.EventList {
		e := &doc.Body.EventList[i]
		if e.Type != lib.ObjectEvent || e.Action != lib.ActionAdd || e.BizStep != "commissioning" {
			continue
		}
		for _, id := range e.EPCList {
			if g, s, _ := lib.Key(lib.Canonical(id)); g != "" && s != "" {
				byGTIN[g] = append(byGTIN[g], s)
			}
		}
	}
	if len(byGTIN) == 0 {
		return nil
	}
	gtins := make([]string, 0, len(byGTIN))
	for g := range byGTIN {
		gtins = append(gtins, g)
	}
	sort.Strings(gtins)
	chk, err := m.Serials.NewChecker(ctx, tx, gtins)
	if err != nil {
		return err
	}
	for _, g := range gtins {
		if _, err := chk.MarkCommissioned(ctx, g, byGTIN[g]); err != nil {
			return err
		}
	}
	return nil
}

// validateBusiness checks events against master data and current state:
// unknown GTINs and unallocated/double-commissioned serials on commissioning,
// and invalid aggregations (self-parenting, a child in two parents, a child already packed elsewhere).
func (m *Module) validateBusiness(ctx context.Context, tx pgx.Tx, doc *lib.Document) []lib.FieldError {
	var errs []lib.FieldError
	add := func(i int, e *lib.Event, field, msg string) {
		if len(errs) < 5000 {
			errs = append(errs, lib.FieldError{Index: i, EventID: e.EventID, Field: field, Message: msg})
		}
	}
	commGTINs := map[string]bool{}
	var children []string
	for i := range doc.Body.EventList {
		e := &doc.Body.EventList[i]
		if e.Type == lib.ObjectEvent && e.Action == lib.ActionAdd && e.BizStep == "commissioning" {
			for _, id := range e.EPCList {
				if g, _, _ := lib.Key(lib.Canonical(id)); g != "" {
					commGTINs[g] = true
				}
			}
		}
		if e.Type == lib.AggregationEvent && e.Action == lib.ActionAdd {
			children = append(children, lib.CanonicalAll(e.ChildEPCs)...)
		}
	}
	gtins := make([]string, 0, len(commGTINs))
	for g := range commGTINs {
		gtins = append(gtins, g)
	}
	known, err := masterdata.KnownGTINs(ctx, tx, gtins)
	if err != nil {
		return []lib.FieldError{{Index: -1, Field: "document", Message: "master data lookup failed"}}
	}
	chk, err := m.Serials.NewChecker(ctx, tx, gtins)
	if err != nil {
		return []lib.FieldError{{Index: -1, Field: "document", Message: "serial pool lookup failed"}}
	}
	// Current parents of children being packed.
	curParent := map[string]string{}
	if len(children) > 0 {
		rows, err := tx.Query(ctx, `select epc, parent_epc from serial_state where tenant_id = app_tenant() and epc = any($1) and parent_epc is not null`, children)
		if err == nil {
			for rows.Next() {
				var c, p string
				if rows.Scan(&c, &p) == nil {
					curParent[c] = p
				}
			}
			rows.Close()
		}
	}
	// FF1-decrypt every commissioned serial once, in parallel.
	var cg, cs []string
	for i := range doc.Body.EventList {
		e := &doc.Body.EventList[i]
		if e.Type == lib.ObjectEvent && e.Action == lib.ActionAdd && e.BizStep == "commissioning" {
			for _, id := range e.EPCList {
				if g, s, _ := lib.Key(lib.Canonical(id)); g != "" && s != "" {
					cg, cs = append(cg, g), append(cs, s)
				}
			}
		}
	}
	statuses := chk.StatusMany(cg, cs)
	statusOf := make(map[string]serialization.SerialStatus, len(cs))
	for i := range cs {
		statusOf[cg[i]+"|"+cs[i]] = statuses[i]
	}
	// Serials of GTINs without an Open Spoor pool (partner or legacy serials): reject
	// commissioning of a serial that already exists in the repository.
	var unmanaged []string
	for i := range cs {
		if statuses[i] == serialization.StatusUnmanaged || (isMigration(ctx) && statuses[i] == serialization.StatusUnallocated) {
			unmanaged = append(unmanaged, gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: cg[i], Serial: cs[i]}))
		}
	}
	existing := map[string]bool{}
	if len(unmanaged) > 0 {
		rows, err := tx.Query(ctx, `select epc from serial_state where tenant_id = app_tenant() and epc = any($1) and status <> 'decommissioned'`, unmanaged)
		if err == nil {
			for rows.Next() {
				var e string
				if rows.Scan(&e) == nil {
					existing[e] = true
				}
			}
			rows.Close()
		}
	}
	seenComm := map[string]int{}
	inDocParent := map[string]string{}
	for i := range doc.Body.EventList {
		e := &doc.Body.EventList[i]
		if e.Type == lib.ObjectEvent && e.Action == lib.ActionAdd && e.BizStep == "commissioning" {
			for _, id := range e.EPCList {
				c := lib.Canonical(id)
				g, s, _ := lib.Key(c)
				if g == "" || s == "" {
					continue
				}
				if _, ok := known[g]; !ok {
					add(i, e, "epcList", "unknown GTIN "+g+" (add it to master data first)")
					continue
				}
				if j, dup := seenComm[c]; dup {
					add(i, e, "epcList", fmt.Sprintf("serial %s commissioned twice in this document (also event %d)", s, j))
					continue
				}
				seenComm[c] = i
				if existing[c] {
					add(i, e, "epcList", "serial "+s+" already exists in the repository (serials are never reissued)")
					continue
				}
				st := statusOf[g+"|"+s]
				if isMigration(ctx) && st == serialization.StatusUnallocated {
					continue
				}
				switch st {
				case serialization.StatusUnallocated:
					add(i, e, "epcList", "serial "+s+" was never allocated from a pool for GTIN "+g)
				case serialization.StatusCommitted:
					add(i, e, "epcList", "serial "+s+" is already commissioned (serials are never reissued)")
				}
			}
		}
		if e.Type == lib.AggregationEvent && e.Action == lib.ActionAdd {
			parent := lib.Canonical(e.ParentID)
			for _, id := range e.ChildEPCs {
				c := lib.Canonical(id)
				if c == parent {
					add(i, e, "childEPCs", "an object cannot contain itself: "+id)
					continue
				}
				if p, ok := inDocParent[c]; ok && p != parent {
					add(i, e, "childEPCs", "child "+id+" is aggregated into two parents in this document")
				}
				inDocParent[c] = parent
				if p, ok := curParent[c]; ok && p != parent {
					add(i, e, "childEPCs", "child "+id+" is already packed in "+p+"; unpack it first (AggregationEvent DELETE)")
				}
			}
		}
	}
	return errs
}

func (m *Module) listCaptures(w http.ResponseWriter, r *http.Request) error {
	limit := httpx.IntQuery(r, "limit", 50, 1, 500)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Capture](r.Context(), tx, `select `+captureCols+` from epcis_capture where ($1='' or status=$1) order by created_at desc limit $2`,
			r.URL.Query().Get("status"), limit)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Capture]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getCapture(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		c, err := db.One[Capture](r.Context(), tx, `select `+captureCols+` from epcis_capture where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, c)
		}
		return err
	})
}
