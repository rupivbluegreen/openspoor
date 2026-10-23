package act

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/httpx"
)

// The semantic layer answers "metric by dimension over a period" from the catalogue
// (semantic_catalog.go) and nothing else. The planner is the only code that writes SQL for it, and
// it writes only parameterised, read-only statements: identifiers come from the catalogue, values
// (period, filters, limit) are bound parameters, and execution happens in a savepoint of the
// caller's tenant transaction set to read only with a statement timeout. A request, or a model's
// suggestion, can name a metric or dimension but can never contribute SQL text.

const (
	semanticDefaultDays = 30
	semanticMaxDays     = 1100
	semanticMaxFilters  = 3
	semanticMaxLimit    = 400
)

var (
	reIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	// Aggregates and column expressions in the catalogue are checked against these shapes when the
	// module starts, so a careless catalogue edit fails fast.
	reAgg  = regexp.MustCompile(`^(count\(\*\)|coalesce\(sum\([a-z_]+\), 0\))$`)
	reExpr = regexp.MustCompile(`^[a-z_]+(::text)?$`)
)

// KPIInfo describes a KPI the library offers.
type KPIInfo struct {
	ID, Name, Description, Unit, Category string
}

// KPIPoint is one day of a KPI series.
type KPIPoint struct {
	Day   string
	Value *float64
}

// KPIProvider exposes the KPI library (internal/ecosystem) to the semantic layer; internal/app
// wires it, because act may not import ecosystem.
type KPIProvider interface {
	List() []KPIInfo
	// Compute returns the KPI's value over [from, to) and its daily series.
	Compute(ctx context.Context, tx pgx.Tx, id string, from, to time.Time) (value *float64, series []KPIPoint, err error)
}

// SemanticQuery is a request: one metric, optionally one dimension and equality filters, over a period.
type SemanticQuery struct {
	Metric    string           `json:"metric"`
	Dimension string           `json:"dimension,omitempty"`
	From      time.Time        `json:"from"`
	To        time.Time        `json:"to"`
	Filters   []SemanticFilter `json:"filters,omitempty"`
	Limit     int              `json:"limit,omitempty"`
}

// SemanticFilter keeps rows where a dimension equals a value (bound as a parameter).
type SemanticFilter struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
}

// SemanticRow is one group of a result.
type SemanticRow struct {
	Key   string   `json:"key"`
	Value *float64 `json:"value"`
}

// SemanticResult is the answer to a SemanticQuery.
type SemanticResult struct {
	Metric      string           `json:"metric"`
	Name        string           `json:"name"`
	Unit        string           `json:"unit"`
	Dimension   string           `json:"dimension,omitempty"`
	From        time.Time        `json:"from"`
	To          time.Time        `json:"to"`
	Filters     []SemanticFilter `json:"filters,omitempty"`
	Description string           `json:"description"`
	Rows        []SemanticRow    `json:"rows"`
	Total       *float64         `json:"total,omitempty"`
}

// Catalog is the validated metric and dimension registry.
type Catalog struct {
	metrics map[string]SemanticMetric
	order   []string
	dims    map[string]SemanticDimension
	kpi     KPIProvider
}

// NewCatalog validates the built-in catalogue and adds the KPI library's entries when a provider is given.
func NewCatalog(kpi KPIProvider) (*Catalog, error) {
	c := &Catalog{metrics: map[string]SemanticMetric{}, dims: map[string]SemanticDimension{}, kpi: kpi}
	for _, d := range semanticDimensions {
		if !reIdent.MatchString(d.ID) || c.dims[d.ID].ID != "" {
			return nil, fmt.Errorf("semantic: bad or duplicate dimension %q", d.ID)
		}
		c.dims[d.ID] = d
	}
	add := func(m SemanticMetric) error {
		if c.metrics[m.ID].ID != "" {
			return fmt.Errorf("semantic: duplicate metric %q", m.ID)
		}
		c.metrics[m.ID] = m
		c.order = append(c.order, m.ID)
		return nil
	}
	for _, m := range semanticMetrics {
		switch {
		case !reIdent.MatchString(m.ID), !reIdent.MatchString(m.Table), !reIdent.MatchString(m.TimeColumn), !reAgg.MatchString(m.Agg), m.Where != "":
			return nil, fmt.Errorf("semantic: metric %q has an unsafe definition", m.ID)
		}
		for id, expr := range m.Dims {
			if d, ok := c.dims[id]; !ok || d.Time || !reExpr.MatchString(expr) {
				return nil, fmt.Errorf("semantic: metric %q has an invalid dimension %q", m.ID, id)
			}
		}
		if err := add(m); err != nil {
			return nil, err
		}
	}
	if kpi != nil {
		for _, k := range kpi.List() {
			if err := add(SemanticMetric{ID: "kpi." + k.ID, Name: k.Name, Description: k.Description, Unit: k.Unit, KPI: k.ID,
				Synonyms: []string{strings.ToLower(k.Name)}}); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

// Metrics lists the metrics in catalogue order.
func (c *Catalog) Metrics() []SemanticMetric {
	out := make([]SemanticMetric, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.metrics[id])
	}
	return out
}

// dimsOf lists the dimension ids a metric supports (time buckets first).
func (c *Catalog) dimsOf(m SemanticMetric) []string {
	out := []string{"day"}
	if m.KPI == "" {
		out = append(out, "week", "month")
		var rest []string
		for id := range m.Dims {
			rest = append(rest, id)
		}
		sort.Strings(rest)
		out = append(out, rest...)
	}
	return out
}

// SemanticPlan is a validated statement: SQL text assembled only from catalogue entries, plus its arguments.
type SemanticPlan struct {
	Query  SemanticQuery
	Metric SemanticMetric
	SQL    string
	Args   []any
}

// Plan validates q against the catalogue and builds the statement. It rejects everything it does not
// recognise: unknown metrics and dimensions, dimensions the metric lacks, filters on time buckets,
// periods that are empty, inverted or longer than about three years, and over-long filter values.
func (c *Catalog) Plan(q SemanticQuery) (*SemanticPlan, error) {
	m, ok := c.metrics[q.Metric]
	if !ok {
		return nil, httpx.BadRequest("unknown metric; GET /api/v1/semantic/catalog lists them")
	}
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) {
		return nil, httpx.BadRequest("from and to are required and to must be after from")
	}
	if q.To.Sub(q.From) > time.Duration(semanticMaxDays)*24*time.Hour {
		return nil, httpx.BadRequest(fmt.Sprintf("the period may be at most %d days", semanticMaxDays))
	}
	supported := map[string]bool{}
	for _, d := range c.dimsOf(m) {
		supported[d] = true
	}
	if q.Dimension != "" && !supported[q.Dimension] {
		return nil, httpx.BadRequest(fmt.Sprintf("metric %s cannot be grouped by %q; it supports %s", m.ID, q.Dimension, strings.Join(c.dimsOf(m), ", ")))
	}
	if len(q.Filters) > semanticMaxFilters {
		return nil, httpx.BadRequest("at most 3 filters")
	}
	if m.KPI != "" && len(q.Filters) > 0 {
		return nil, httpx.BadRequest("KPI metrics take no filters")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, semanticMaxLimit)
	q.Limit = limit
	p := &SemanticPlan{Query: q, Metric: m}
	if m.KPI != "" {
		return p, nil
	}

	tcol := "(" + m.TimeColumn + ")"
	args := []any{q.From, q.To}
	where := []string{"tenant_id = app_tenant()", tcol + " >= $1", tcol + " < $2"}
	for _, f := range q.Filters {
		d, known := c.dims[f.Dimension]
		expr, has := m.Dims[f.Dimension]
		if !known || d.Time || !has {
			return nil, httpx.BadRequest(fmt.Sprintf("metric %s cannot be filtered by %q", m.ID, f.Dimension))
		}
		if f.Value == "" || len(f.Value) > 200 {
			return nil, httpx.BadRequest("a filter value must be 1 to 200 characters")
		}
		args = append(args, f.Value)
		where = append(where, fmt.Sprintf("coalesce((%s)::text, '') = $%d", expr, len(args)))
	}
	sel, group, order := "null::text", "", ""
	if q.Dimension != "" {
		var expr string
		if d := c.dims[q.Dimension]; d.Time {
			expr = fmt.Sprintf("(date_trunc('%s', %s at time zone 'UTC'))::date::text", q.Dimension, m.TimeColumn)
			order = " order by 1 asc"
		} else {
			expr = fmt.Sprintf("coalesce((%s)::text, '(none)')", m.Dims[q.Dimension])
			order = " order by 2 desc nulls last, 1 asc"
		}
		sel, group = expr, " group by 1"
	}
	args = append(args, limit)
	p.SQL = fmt.Sprintf("select %s as dim, (%s)::float8 as value from %s where %s%s%s limit $%d",
		sel, m.Agg, m.Table, strings.Join(where, " and "), group, order, len(args))
	p.Args = args
	return p, nil
}

// describe renders a request in words (for the answer and the audit trail).
func (p *SemanticPlan) describe() string {
	q := p.Query
	s := p.Metric.Name
	if q.Dimension != "" {
		s += " by " + q.Dimension
	}
	for _, f := range q.Filters {
		s += fmt.Sprintf(", %s = %q", f.Dimension, f.Value)
	}
	return fmt.Sprintf("%s, %s to %s", s, q.From.UTC().Format(time.DateOnly), q.To.UTC().Format(time.DateOnly))
}

// RunSemantic plans and executes q inside tx (a tenant transaction). The statement runs in a savepoint
// that is set read only with a statement timeout and always rolled back, so it cannot change data
// even if the catalogue were wrong, and the caller's transaction settings are restored.
func (m *Module) RunSemantic(ctx context.Context, tx pgx.Tx, q SemanticQuery) (SemanticResult, error) {
	cat, err := m.catalog()
	if err != nil {
		return SemanticResult{}, err
	}
	plan, err := cat.Plan(q)
	if err != nil {
		return SemanticResult{}, err
	}
	res := SemanticResult{Metric: plan.Metric.ID, Name: plan.Metric.Name, Unit: plan.Metric.Unit, Dimension: plan.Query.Dimension,
		From: plan.Query.From.UTC(), To: plan.Query.To.UTC(), Filters: plan.Query.Filters, Description: plan.describe(), Rows: []SemanticRow{}}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = sp.Rollback(ctx) }()
	for _, s := range []string{`set local transaction_read_only = on`, `set local statement_timeout = '5s'`} {
		if _, err := sp.Exec(ctx, s); err != nil {
			return res, err
		}
	}
	if plan.Metric.KPI != "" {
		if cat.kpi == nil {
			return res, errors.New("the KPI library is not available")
		}
		v, series, err := cat.kpi.Compute(ctx, sp, plan.Metric.KPI, plan.Query.From, plan.Query.To)
		if err != nil {
			return res, err
		}
		if plan.Query.Dimension == "day" {
			for _, pt := range series {
				res.Rows = append(res.Rows, SemanticRow{Key: pt.Day, Value: pt.Value})
			}
			if len(res.Rows) > plan.Query.Limit {
				res.Rows = res.Rows[:plan.Query.Limit]
			}
		} else {
			res.Rows = append(res.Rows, SemanticRow{Key: "", Value: v})
		}
		res.Total = v
		return res, nil
	}
	rows, err := sp.Query(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	var total float64
	for rows.Next() {
		var key *string
		var v *float64
		if err := rows.Scan(&key, &v); err != nil {
			return res, err
		}
		r := SemanticRow{Value: v}
		if key != nil {
			r.Key = *key
		}
		if v != nil {
			total += *v
		}
		res.Rows = append(res.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if plan.Query.Dimension == "" && len(res.Rows) == 1 {
		res.Total = res.Rows[0].Value
	} else {
		res.Total = &total
	}
	return res, nil
}

func (m *Module) catalog() (*Catalog, error) {
	m.catOnce.Do(func() { m.cat, m.catErr = NewCatalog(m.KPIs) })
	return m.cat, m.catErr
}

// --- endpoints ---

func (m *Module) semanticRoutes(r chi.Router) {
	r.Get("/semantic/catalog", httpx.Handler(m.semanticCatalog))
	r.Post("/semantic/query", httpx.Handler(m.semanticQuery))
}

func (m *Module) semanticCatalog(w http.ResponseWriter, _ *http.Request) error {
	cat, err := m.catalog()
	if err != nil {
		return err
	}
	type metricView struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Unit        string   `json:"unit"`
		Dimensions  []string `json:"dimensions"`
		Source      string   `json:"source"`
	}
	ms := []metricView{}
	for _, mt := range cat.Metrics() {
		src := "table"
		if mt.KPI != "" {
			src = "kpi"
		}
		ms = append(ms, metricView{mt.ID, mt.Name, mt.Description, mt.Unit, cat.dimsOf(mt), src})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"metrics": ms, "dimensions": semanticDimensions})
	return nil
}

func (m *Module) semanticQuery(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Metric    string           `json:"metric"`
		Dimension string           `json:"dimension"`
		From      string           `json:"from"`
		To        string           `json:"to"`
		Days      int              `json:"days"`
		Filters   []SemanticFilter `json:"filters"`
		Limit     int              `json:"limit"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	q := SemanticQuery{Metric: in.Metric, Dimension: in.Dimension, Filters: in.Filters, Limit: in.Limit}
	now := time.Now().UTC()
	switch {
	case in.From != "" || in.To != "":
		var err error
		if q.From, err = parseWhen(in.From); err != nil {
			return httpx.BadRequest("from must be YYYY-MM-DD or RFC 3339")
		}
		if in.To == "" {
			q.To = now
		} else if q.To, err = parseWhen(in.To); err != nil {
			return httpx.BadRequest("to must be YYYY-MM-DD or RFC 3339")
		} else if len(in.To) == len(time.DateOnly) {
			q.To = q.To.AddDate(0, 0, 1) // a date-only end is inclusive
		}
	case in.Days > 0:
		q.From, q.To = now.AddDate(0, 0, -in.Days), now
	default:
		q.From, q.To = now.AddDate(0, 0, -semanticDefaultDays), now
	}
	var res SemanticResult
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		res, err = m.RunSemantic(r.Context(), tx, q)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func parseWhen(s string) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// --- reading a question ---

var (
	reLastN = regexp.MustCompile(`\b(?:last|past|previous)\s+(\d{1,4})\s+(day|week|month|year)s?\b`)
	reSince = regexp.MustCompile(`\bsince\s+(\d{4}-\d{2}-\d{2})\b`)
	reByDim = regexp.MustCompile(`\b(?:by|per|grouped by|split by|broken down by)\s+([a-z ]{2,30})`)
)

func hasPhrase(text, phrase string) bool {
	i := strings.Index(text, phrase)
	for i >= 0 {
		endOK := i+len(phrase) == len(text) || !isWordRune(rune(text[i+len(phrase)]))
		startOK := i == 0 || !isWordRune(rune(text[i-1]))
		if startOK && endOK {
			return true
		}
		j := strings.Index(text[i+1:], phrase)
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return false
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// ParseQuestion reads "metric by dimension over a period" from a natural-language question using the
// catalogue's names and synonyms (no model involved). It returns false when no metric is named.
func (c *Catalog) ParseQuestion(question string, now time.Time) (SemanticQuery, bool) {
	text := strings.ToLower(question)
	now = now.UTC()
	var best SemanticMetric
	bestLen := 0
	for _, id := range c.order {
		m := c.metrics[id]
		for _, name := range append([]string{strings.ToLower(m.Name), strings.ReplaceAll(m.ID, "_", " ")}, m.Synonyms...) {
			if len(name) > bestLen && hasPhrase(text, strings.ToLower(name)) {
				best, bestLen = m, len(name)
			}
		}
	}
	if bestLen == 0 {
		return SemanticQuery{}, false
	}
	q := SemanticQuery{Metric: best.ID, To: now, From: now.AddDate(0, 0, -semanticDefaultDays)}
	// dimension: "by <name>" (or a time word such as daily)
	if mm := reByDim.FindStringSubmatch(text); mm != nil {
		phrase := strings.TrimSpace(mm[1])
		for _, dim := range c.dimsOf(best) {
			d := c.dims[dim]
			for _, n := range append([]string{strings.ToLower(d.Name), dim, strings.ReplaceAll(dim, "_", " ")}, d.Synonyms...) {
				if n != "" && (phrase == n || strings.HasPrefix(phrase, n+" ") || strings.HasPrefix(phrase, n+"s")) {
					q.Dimension = dim
				}
			}
			if q.Dimension != "" {
				break
			}
		}
	}
	if q.Dimension == "" {
		for _, dim := range []string{"day", "week", "month"} {
			for _, n := range c.dims[dim].Synonyms {
				if hasPhrase(text, n) && hasString(c.dimsOf(best), dim) {
					q.Dimension = dim
				}
			}
		}
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case reLastN.MatchString(text):
		mm := reLastN.FindStringSubmatch(text)
		n := 0
		_, _ = fmt.Sscanf(mm[1], "%d", &n)
		switch mm[2] {
		case "day":
			q.From = now.AddDate(0, 0, -n)
		case "week":
			q.From = now.AddDate(0, 0, -7*n)
		case "month":
			q.From = now.AddDate(0, -n, 0)
		case "year":
			q.From = now.AddDate(-n, 0, 0)
		}
	case reSince.MatchString(text):
		if t, err := time.Parse(time.DateOnly, reSince.FindStringSubmatch(text)[1]); err == nil {
			q.From = t
		}
	case hasPhrase(text, "today"):
		q.From = today
	case hasPhrase(text, "yesterday"):
		q.From, q.To = today.AddDate(0, 0, -1), today
	case hasPhrase(text, "this week"):
		q.From = today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	case hasPhrase(text, "last week"):
		start := today.AddDate(0, 0, -((int(today.Weekday())+6)%7)-7)
		q.From, q.To = start, start.AddDate(0, 0, 7)
	case hasPhrase(text, "this month"):
		q.From = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	case hasPhrase(text, "last month"):
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		q.From, q.To = start, start.AddDate(0, 1, 0)
	case hasPhrase(text, "this year"):
		q.From = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	case hasPhrase(text, "last year"):
		q.From = time.Date(now.Year()-1, 1, 1, 0, 0, 0, 0, time.UTC)
		q.To = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return q, true
}

func hasString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
