package act

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/httpx"
)

var t0 = time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)

func plan(t *testing.T, c *Catalog, q SemanticQuery) (*SemanticPlan, error) {
	t.Helper()
	if q.From.IsZero() {
		q.From, q.To = t0.AddDate(0, 0, -30), t0
	}
	return c.Plan(q)
}

func mustCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := NewCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalogIsValid(t *testing.T) {
	c := mustCatalog(t)
	if len(c.Metrics()) != len(semanticMetrics) {
		t.Fatal("metrics lost")
	}
	// Every metric plans for every dimension it declares, with each shape of request.
	for _, m := range c.Metrics() {
		for _, d := range append([]string{""}, c.dimsOf(m)...) {
			p, err := plan(t, c, SemanticQuery{Metric: m.ID, Dimension: d})
			if err != nil {
				t.Errorf("%s by %q: %v", m.ID, d, err)
				continue
			}
			if !strings.Contains(p.SQL, "tenant_id = app_tenant()") || !strings.HasPrefix(p.SQL, "select ") {
				t.Errorf("%s: %s", m.ID, p.SQL)
			}
		}
	}
	// A careless catalogue edit is refused at start-up.
	saved := semanticMetrics
	defer func() { semanticMetrics = saved }()
	for _, bad := range []SemanticMetric{
		{ID: "x", Table: "alert; drop table alert", TimeColumn: "created_at", Agg: "count(*)"},
		{ID: "x", Table: "alert", TimeColumn: "created_at", Agg: "count(*) from secrets --"},
		{ID: "x", Table: "alert", TimeColumn: "created_at", Agg: "count(*)", Where: "1=1"},
		{ID: "x", Table: "alert", TimeColumn: "created_at", Agg: "count(*)", Dims: map[string]string{"nope": "kind"}},
		{ID: "x", Table: "alert", TimeColumn: "created_at", Agg: "count(*)", Dims: map[string]string{"kind": "kind || (select 1)"}},
		{ID: "x", Table: "alert", TimeColumn: "created_at", Agg: "count(*)", Dims: map[string]string{"day": "created_at"}},
		{ID: "alerts", Table: "alert", TimeColumn: "created_at", Agg: "count(*)"}, // duplicate id
	} {
		semanticMetrics = append(append([]SemanticMetric{}, saved...), bad)
		if _, err := NewCatalog(nil); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestPlannerRefusesAndBindsInjection(t *testing.T) {
	c := mustCatalog(t)
	bad := []SemanticQuery{
		{Metric: "alerts; drop table alert"},
		{Metric: "alert"},
		{Metric: ""},
		{Metric: "alerts", Dimension: "severity) union select password_hash from app_user --"},
		{Metric: "alerts", Dimension: "gtin"},
		{Metric: "alerts", Dimension: "severity", Filters: []SemanticFilter{{Dimension: "day", Value: "x"}}},
		{Metric: "alerts", Filters: []SemanticFilter{{Dimension: "severity = 'a' or 1=1 --", Value: "x"}}},
		{Metric: "alerts", Filters: []SemanticFilter{{Dimension: "gtin", Value: "x"}}},
		{Metric: "alerts", Filters: []SemanticFilter{{Dimension: "severity", Value: ""}}},
		{Metric: "alerts", Filters: []SemanticFilter{{Dimension: "severity", Value: strings.Repeat("x", 201)}}},
		{Metric: "alerts", Filters: []SemanticFilter{{"kind", "a"}, {"kind", "b"}, {"kind", "c"}, {"kind", "d"}}},
		{Metric: "alerts", From: t0, To: t0},
		{Metric: "alerts", From: t0, To: t0.AddDate(0, 0, -1)},
		{Metric: "alerts", From: t0.AddDate(-5, 0, 0), To: t0},
		{Metric: "alerts", From: t0},
	}
	for _, q := range bad {
		if _, err := plan(t, c, q); err == nil {
			t.Errorf("accepted %+v", q)
		} else if e, ok := err.(*httpx.Error); !ok || e.Status != 400 {
			t.Errorf("%+v: %v", q, err)
		}
	}
	// A hostile filter value is a bound parameter, never part of the statement.
	evil := "x' or '1'='1'; drop table alert; --"
	p, err := plan(t, c, SemanticQuery{Metric: "alerts", Dimension: "severity", Filters: []SemanticFilter{{Dimension: "status", Value: evil}}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.SQL, "drop") || strings.Contains(p.SQL, "1'='1") {
		t.Fatalf("value leaked into SQL: %s", p.SQL)
	}
	found := false
	for _, a := range p.Args {
		found = found || a == evil
	}
	if !found || len(p.Args) != 4 {
		t.Fatalf("args %v", p.Args)
	}
	// Whatever is asked, the statement keeps one fixed shape built from catalogue identifiers.
	shape := regexp.MustCompile(`^select [^;]+ from [a-z0-9_]+ where tenant_id = app_tenant\(\) and \([a-z_]+\) >= \$1 and \([a-z_]+\) < \$2( and coalesce\(\([a-z_:]+\)::text, ''\) = \$\d+)*( group by 1 order by [0-9a-z ,]+)? limit \$\d+$`)
	for _, m := range c.Metrics() {
		for _, d := range c.dimsOf(m) {
			p, _ := plan(t, c, SemanticQuery{Metric: m.ID, Dimension: d, Filters: []SemanticFilter{{Dimension: firstNonTime(c, m), Value: "zz9leak"}}})
			if p != nil && firstNonTime(c, m) != "" && (!shape.MatchString(p.SQL) || strings.Contains(p.SQL, "--") || strings.Contains(p.SQL, "zz9leak")) {
				t.Errorf("unexpected statement shape: %s", p.SQL)
			}
		}
	}
	// Limits are clamped.
	p, _ = plan(t, c, SemanticQuery{Metric: "alerts", Limit: 99999})
	if p.Query.Limit != semanticMaxLimit {
		t.Errorf("limit %d", p.Query.Limit)
	}
	p, _ = plan(t, c, SemanticQuery{Metric: "alerts"})
	if p.Query.Limit != 50 {
		t.Errorf("default limit %d", p.Query.Limit)
	}
}

func firstNonTime(c *Catalog, m SemanticMetric) string {
	for _, d := range c.dimsOf(m) {
		if !c.dims[d].Time {
			return d
		}
	}
	return ""
}

type kpiStub struct{}

func (kpiStub) List() []KPIInfo {
	return []KPIInfo{{ID: "otif_pct", Name: "On time, in full", Unit: "%"}}
}

func (kpiStub) Compute(context.Context, pgx.Tx, string, time.Time, time.Time) (*float64, []KPIPoint, error) {
	return nil, nil, nil
}

func TestKPIMetricsInTheCatalogue(t *testing.T) {
	c, err := NewCatalog(kpiStub{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := c.metrics["kpi.otif_pct"]
	if !ok || m.KPI != "otif_pct" || strings.Join(c.dimsOf(m), ",") != "day" {
		t.Fatalf("kpi metric: %+v", m)
	}
	if _, err := plan(t, c, SemanticQuery{Metric: "kpi.otif_pct", Dimension: "day"}); err != nil {
		t.Error(err)
	}
	for _, q := range []SemanticQuery{{Metric: "kpi.otif_pct", Dimension: "status"}, {Metric: "kpi.otif_pct", Filters: []SemanticFilter{{"status", "x"}}}} {
		if _, err := plan(t, c, q); err == nil {
			t.Errorf("accepted %+v", q)
		}
	}
	if q, ok := c.ParseQuestion("What was our on time, in full by day last 14 days?", t0); !ok || q.Metric != "kpi.otif_pct" || q.Dimension != "day" {
		t.Errorf("parse: %+v %v", q, ok)
	}
}

func TestParseQuestion(t *testing.T) {
	c := mustCatalog(t)
	day := func(d int) time.Time { return t0.AddDate(0, 0, d) }
	cases := []struct {
		q    string
		ok   bool
		want SemanticQuery
	}{
		{"How many alerts by severity in the last 7 days?", true, SemanticQuery{Metric: "alerts", Dimension: "severity", From: day(-7), To: t0}},
		{"alerts per status this month", true, SemanticQuery{Metric: "alerts", Dimension: "status", From: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), To: t0}},
		{"Show EPCIS events by business step since 2026-03-01", true, SemanticQuery{Metric: "epcis_events", Dimension: "biz_step", From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), To: t0}},
		{"number of shipments daily last 2 weeks", true, SemanticQuery{Metric: "shipments", Dimension: "day", From: day(-14), To: t0}},
		{"cases by priority yesterday", true, SemanticQuery{Metric: "cases", Dimension: "priority", From: time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)}},
		{"agent actions by agent last month", true, SemanticQuery{Metric: "agent_actions", Dimension: "agent", From: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}},
		{"regulatory reports by connector this year", true, SemanticQuery{Metric: "regulatory_reports", Dimension: "connector", From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), To: t0}},
		{"alerts monthly last year", true, SemanticQuery{Metric: "alerts", Dimension: "month", From: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"alerts last week", true, SemanticQuery{Metric: "alerts", From: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)}},
		{"alerts this week", true, SemanticQuery{Metric: "alerts", From: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC), To: t0}},
		{"alerts today", true, SemanticQuery{Metric: "alerts", From: time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC), To: t0}},
		{"alerts in the past 3 months", true, SemanticQuery{Metric: "alerts", From: t0.AddDate(0, -3, 0), To: t0}},
		{"alerts over the past 1 year", true, SemanticQuery{Metric: "alerts", From: t0.AddDate(-1, 0, 0), To: t0}},
		{"alerts by gtin", true, SemanticQuery{Metric: "alerts", From: day(-30), To: t0}}, // gtin is not a dimension of alerts: ignored
		{"tell me a joke", false, SemanticQuery{}},
		{"palerts", false, SemanticQuery{}}, // word boundaries
	}
	for _, tc := range cases {
		got, ok := c.ParseQuestion(tc.q, t0)
		if ok != tc.ok || got.Metric != tc.want.Metric || got.Dimension != tc.want.Dimension || !got.From.Equal(tc.want.From) || (ok && !got.To.Equal(tc.want.To)) {
			t.Errorf("%q: %+v %v, want %+v", tc.q, got, ok, tc.want)
		}
		if ok {
			if _, err := c.Plan(got); err != nil {
				t.Errorf("%q: plan: %v", tc.q, err)
			}
		}
	}
	// A longer name wins over a shorter one it contains.
	if q, _ := c.ParseQuestion("serial number updates by status", t0); q.Metric != "serial_updates" {
		t.Errorf("longest match: %+v", q)
	}
}

func TestQuestionQuery(t *testing.T) {
	got := questionQuery(`What does the batch-release procedure say about "quarantine" or 12345 — and (drop table)?`)
	if got != "batch-release or procedure or say or quarantine or 12345 or drop or table" {
		t.Errorf("query %q", got)
	}
	if questionQuery("What is it?") != "" || questionQuery("") != "" {
		t.Error("stop words only must give an empty query")
	}
	if n := strings.Count(questionQuery(strings.Repeat("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike ", 2)), " or "); n != 11 {
		t.Errorf("word cap: %d", n)
	}
	for _, q := range []string{"how many alerts", "total cases", "trend of x", "alerts today"} {
		if !looksQuantitative(q) {
			t.Errorf("%q", q)
		}
	}
	if looksQuantitative("What does the procedure say") {
		t.Error("not quantitative")
	}
}
