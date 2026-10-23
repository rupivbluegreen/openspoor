package ecosystem

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
)

// The KPI library: formula-defined indicators computed from data the platform already holds
// (B2B documents, shipments, reports, alerts, cases, investigations, VRS requests, serial
// state, recalls, cold-chain excursions). Definitions live in kpi_defs.go; this file is the
// engine (aggregation, daily series, goals, peer values) and the API.

// Kind says how a KPI's event rows are aggregated.
type Kind string

// Kinds.
const (
	// KindRatio: rows carry a numerator (a) and a denominator (b); value = scale * sum(a) / sum(b).
	// Means are ratios too (a = sum of values, b = count).
	KindRatio Kind = "ratio"
	// KindSum: rows carry a quantity (a); value = sum(a), 0 when there are no rows.
	KindSum Kind = "sum"
	// KindQuantile: each row is one sample (a); value = the q-quantile of the samples.
	KindQuantile Kind = "quantile"
	// KindSnapshot: the value is read as of now; a daily history exists only when SeriesSQL does.
	KindSnapshot Kind = "snapshot"
)

// Directions.
const (
	HigherBetter = "higher"
	LowerBetter  = "lower"
)

// KPI is one library entry.
type KPI struct {
	ID          string
	Name        string
	Category    string
	Unit        string
	Direction   string // HigherBetter or LowerBetter
	Description string
	Kind        Kind
	// Scale multiplies a ratio (100 for percentages); 0 means 1.
	Scale float64
	// Q is the quantile of a KindQuantile KPI.
	Q float64
	// SQL selects the event rows (tenant_id uuid, day date, a float8, b float8) inside the window
	// $1 (inclusive start) to $2 (exclusive end), both timestamptz. For a snapshot it selects one
	// float8 value.
	SQL string
	// SeriesSQL gives a snapshot's history: rows (day date, v float8) for the days from $1 to $2.
	SeriesSQL string
	// BucketSQL gives a snapshot's breakdown: rows (label text, n bigint) in display order.
	BucketSQL string
	// Peer marks KPIs that make sense to compare with anonymised peers (ratios and quantiles
	// that do not scale with volume).
	Peer bool
}

// Category groups KPIs in the console.
type KPICategory struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// row is one event row of a KPI query.
type row struct {
	Tenant uuid.UUID
	Day    time.Time
	A, B   float64
}

// Point is one day of a series; Value is null when the day has no data.
type Point struct {
	Day   string   `json:"day"`
	Value *float64 `json:"value"`
}

// Window is the period KPIs are computed over: Days whole UTC days ending now.
type Window struct {
	From, To time.Time
	Days     int
}

// NewWindow builds the window of the last days days (today included).
func NewWindow(now time.Time, days int) Window {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	return Window{From: start, To: now, Days: days}
}

func (k KPI) scale() float64 {
	if k.Scale == 0 {
		return 1
	}
	return k.Scale
}

func ptr(v float64) *float64 { return &v }

// aggregate reduces rows to the KPI's value and the number of samples behind it
// (denominator for ratios, rows otherwise). A ratio without denominator has no value.
func (k KPI) aggregate(rows []row) (*float64, int64) {
	switch k.Kind {
	case KindRatio:
		var a, b float64
		for _, r := range rows {
			a, b = a+r.A, b+r.B
		}
		if b == 0 {
			return nil, 0
		}
		return ptr(round(k.scale()*a/b, 4)), int64(math.Round(b))
	case KindSum:
		var a float64
		for _, r := range rows {
			a += r.A
		}
		return ptr(round(a, 4)), int64(len(rows))
	case KindQuantile:
		if len(rows) == 0 {
			return nil, 0
		}
		xs := make([]float64, len(rows))
		for i, r := range rows {
			xs[i] = r.A
		}
		return ptr(round(percentile(xs, k.Q), 4)), int64(len(rows))
	}
	return nil, 0
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// days lists the UTC dates of the window, oldest first.
func (w Window) days() []time.Time {
	var out []time.Time
	for d := w.From; !d.After(w.To); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// series computes the daily values of an event KPI: one point per day of the window; days without
// data are null (ratios, quantiles) or zero (sums).
func (k KPI) series(rows []row, w Window) []Point {
	byDay := map[string][]row{}
	for _, r := range rows {
		d := r.Day.Format("2006-01-02")
		byDay[d] = append(byDay[d], r)
	}
	out := []Point{}
	for _, d := range w.days() {
		key := d.Format("2006-01-02")
		v, _ := k.aggregate(byDay[key])
		out = append(out, Point{Day: key, Value: v})
	}
	return out
}

// peerValues reduces rows to one value per tenant (used for the anonymised peer comparison).
func (k KPI) peerValues(rows []row) map[uuid.UUID]float64 {
	by := map[uuid.UUID][]row{}
	for _, r := range rows {
		by[r.Tenant] = append(by[r.Tenant], r)
	}
	out := map[uuid.UUID]float64{}
	for t, rs := range by {
		if v, _ := k.aggregate(rs); v != nil {
			out[t] = *v
		}
	}
	return out
}

// fetch runs the KPI's event query.
func (k KPI) fetch(ctx context.Context, tx pgx.Tx, w Window) ([]row, error) {
	rs, err := tx.Query(ctx, k.SQL, w.From, w.To)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.Tenant, &r.Day, &r.A, &r.B); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// Bucket is one slice of a snapshot KPI's breakdown.
type Bucket struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// Result is a computed KPI.
type Result struct {
	Value     *float64 `json:"value"`
	Samples   int64    `json:"samples"`
	Breakdown []Bucket `json:"breakdown,omitempty"`
}

// Compute evaluates the KPI over the window.
func (k KPI) Compute(ctx context.Context, tx pgx.Tx, w Window) (Result, error) {
	if k.Kind == KindSnapshot {
		var v *float64
		if err := tx.QueryRow(ctx, k.SQL).Scan(&v); err != nil {
			return Result{}, err
		}
		res := Result{Value: v}
		if v != nil {
			res.Value = ptr(round(*v, 4))
		}
		if k.BucketSQL != "" {
			rs, err := tx.Query(ctx, k.BucketSQL)
			if err != nil {
				return res, err
			}
			defer rs.Close()
			for rs.Next() {
				var b Bucket
				if err := rs.Scan(&b.Label, &b.Count); err != nil {
					return res, err
				}
				res.Breakdown = append(res.Breakdown, b)
			}
			return res, rs.Err()
		}
		return res, nil
	}
	rows, err := k.fetch(ctx, tx, w)
	if err != nil {
		return Result{}, err
	}
	v, n := k.aggregate(rows)
	return Result{Value: v, Samples: n}, nil
}

// SeriesOf computes the daily series. available is false for snapshot KPIs without history.
func (k KPI) SeriesOf(ctx context.Context, tx pgx.Tx, w Window) (points []Point, available bool, err error) {
	if k.Kind == KindSnapshot {
		if k.SeriesSQL == "" {
			return []Point{}, false, nil
		}
		rs, err := tx.Query(ctx, k.SeriesSQL, w.From, w.To)
		if err != nil {
			return nil, false, err
		}
		defer rs.Close()
		points = []Point{}
		for rs.Next() {
			var day time.Time
			var v *float64
			if err := rs.Scan(&day, &v); err != nil {
				return nil, false, err
			}
			points = append(points, Point{Day: day.Format("2006-01-02"), Value: v})
		}
		return points, true, rs.Err()
	}
	rows, err := k.fetch(ctx, tx, w)
	if err != nil {
		return nil, false, err
	}
	return k.series(rows, w), true, nil
}

// --- registry access ---

// KPICategories lists the categories in display order.
var KPICategories = []KPICategory{
	{"delivery", "Delivery"},
	{"exchange", "Partner exchange"},
	{"compliance", "Compliance"},
	{"operations", "Operations"},
	{"verification", "Verification"},
	{"inventory", "Inventory"},
	{"safety", "Recall and cold chain"},
}

// KPIByID returns a library KPI.
func KPIByID(id string) (KPI, bool) {
	for _, k := range Library {
		if k.ID == id {
			return k, true
		}
	}
	return KPI{}, false
}

// --- goals ---

type goal struct {
	Target    float64   `json:"target"`
	Note      string    `json:"note,omitempty"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// goalStatus tags a value against the tenant's goal: "met" or "missed" (empty without both).
func goalStatus(direction string, value *float64, g *goal) string {
	if g == nil || value == nil {
		return ""
	}
	if (direction == HigherBetter && *value >= g.Target) || (direction == LowerBetter && *value <= g.Target) {
		return "met"
	}
	return "missed"
}

func loadGoals(ctx context.Context, tx pgx.Tx) (map[string]*goal, error) {
	rs, err := tx.Query(ctx, `select kpi_id, target::float8, note, updated_by, updated_at from kpi_goal`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]*goal{}
	for rs.Next() {
		var id string
		var g goal
		if err := rs.Scan(&id, &g.Target, &g.Note, &g.UpdatedBy, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out[id] = &g
	}
	return out, rs.Err()
}

// --- API ---

func parseDays(r *http.Request) (int, error) {
	s := r.URL.Query().Get("days")
	if s == "" {
		return 30, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 365 {
		return 0, httpx.BadRequest("days must be between 1 and 365")
	}
	return n, nil
}

type kpiView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Category        string   `json:"category"`
	Unit            string   `json:"unit"`
	Direction       string   `json:"direction"`
	Description     string   `json:"description"`
	Kind            Kind     `json:"kind"`
	Value           *float64 `json:"value"`
	Samples         int64    `json:"samples"`
	Breakdown       []Bucket `json:"breakdown,omitempty"`
	SeriesAvailable bool     `json:"seriesAvailable"`
	Peer            bool     `json:"peerComparable"`
	Goal            *goal    `json:"goal,omitempty"`
	GoalStatus      string   `json:"goalStatus,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func view(k KPI) kpiView {
	return kpiView{ID: k.ID, Name: k.Name, Category: k.Category, Unit: k.Unit, Direction: k.Direction, Description: k.Description, Kind: k.Kind,
		SeriesAvailable: k.Kind != KindSnapshot || k.SeriesSQL != "", Peer: k.Peer}
}

// listKPIs lists the library with current values over ?days (default 30) and the tenant's goals.
// A KPI whose query fails is returned with an error instead of failing the whole list.
func (m *Module) listKPIs(w http.ResponseWriter, r *http.Request) error {
	days, err := parseDays(r)
	if err != nil {
		return err
	}
	cat := r.URL.Query().Get("category")
	win := NewWindow(time.Now(), days)
	out := []kpiView{}
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		goals, err := loadGoals(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, k := range Library {
			if cat != "" && k.Category != cat {
				continue
			}
			v := view(k)
			// A failing KPI query aborts the transaction; savepoints keep the others usable.
			sp, err := tx.Begin(r.Context())
			if err != nil {
				return err
			}
			res, cerr := k.Compute(r.Context(), sp, win)
			if cerr != nil {
				_ = sp.Rollback(r.Context())
				v.Error = "cannot compute: " + cerr.Error()
			} else if err := sp.Commit(r.Context()); err != nil {
				return err
			}
			v.Value, v.Samples, v.Breakdown = res.Value, res.Samples, res.Breakdown
			v.Goal = goals[k.ID]
			v.GoalStatus = goalStatus(k.Direction, v.Value, v.Goal)
			out = append(out, v)
		}
		return nil
	})
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, k := range Library {
		counts[k.Category]++
	}
	cats := []map[string]any{}
	for _, c := range KPICategories {
		cats = append(cats, map[string]any{"key": c.Key, "name": c.Name, "count": counts[c.Key]})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out), "categories": cats, "days": days, "generatedAt": time.Now().UTC()})
	return nil
}

func (m *Module) kpiSeries(w http.ResponseWriter, r *http.Request) error {
	k, ok := KPIByID(chi.URLParam(r, "id"))
	if !ok {
		return httpx.NotFound("unknown KPI")
	}
	days, err := parseDays(r)
	if err != nil {
		return err
	}
	var points []Point
	var available bool
	if err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		points, available, err = k.SeriesOf(r.Context(), tx, NewWindow(time.Now(), days))
		return err
	}); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": k.ID, "name": k.Name, "unit": k.Unit, "direction": k.Direction, "days": days, "available": available, "points": points})
	return nil
}

func (m *Module) putGoal(w http.ResponseWriter, r *http.Request) error {
	k, ok := KPIByID(chi.URLParam(r, "id"))
	if !ok {
		return httpx.NotFound("unknown KPI")
	}
	var in struct {
		Target *float64 `json:"target"`
		Note   string   `json:"note"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Target == nil || math.IsNaN(*in.Target) || math.IsInf(*in.Target, 0) {
		return httpx.BadRequest("target is required")
	}
	if k.Unit == "%" && (*in.Target < 0 || *in.Target > 100) {
		return httpx.BadRequest("a percentage target must be between 0 and 100")
	}
	if *in.Target < 0 {
		return httpx.BadRequest("target must not be negative")
	}
	me := auth.MustFrom(r.Context())
	var g goal
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := loadGoals(r.Context(), tx)
		if err != nil {
			return err
		}
		g = goal{Target: *in.Target, Note: in.Note, UpdatedBy: me.AuditActor(), UpdatedAt: time.Now().UTC()}
		if _, err := tx.Exec(r.Context(), `insert into kpi_goal (tenant_id, kpi_id, target, note, updated_by, updated_at) values (app_tenant(), $1, $2, $3, $4, $5)
			on conflict (tenant_id, kpi_id) do update set target = excluded.target, note = excluded.note, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			k.ID, g.Target, g.Note, g.UpdatedBy, g.UpdatedAt); err != nil {
			return err
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "kpi.goal.set", Entity: "kpi", EntityID: k.ID, Before: before[k.ID], After: g})
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, g)
	return nil
}

func (m *Module) deleteGoal(w http.ResponseWriter, r *http.Request) error {
	k, ok := KPIByID(chi.URLParam(r, "id"))
	if !ok {
		return httpx.NotFound("unknown KPI")
	}
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := loadGoals(r.Context(), tx)
		if err != nil {
			return err
		}
		if before[k.ID] == nil {
			return httpx.NotFound("no goal set for this KPI")
		}
		if _, err := tx.Exec(r.Context(), `delete from kpi_goal where kpi_id = $1`, k.ID); err != nil {
			return err
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "kpi.goal.remove", Entity: "kpi", EntityID: k.ID, Before: before[k.ID]})
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// libraryPeerRows fetches the all-tenant event rows of a peer-comparable KPI for the last 30 days
// (operator transaction, rows carry tenant_id).
func libraryPeerValues(ctx context.Context, tx pgx.Tx, k KPI, w Window) (map[uuid.UUID]float64, error) {
	if !k.Peer || k.Kind == KindSnapshot {
		return nil, errors.New("KPI has no peer comparison")
	}
	rows, err := k.fetch(ctx, tx, w)
	if err != nil {
		return nil, err
	}
	return k.peerValues(rows), nil
}
