package act_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/act"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

type semRow struct {
	Key   string
	Value *float64
}

type semResult struct {
	Metric, Dimension, Description string
	Rows                           []semRow
	Total                          *float64
}

func (r semResult) byKey() map[string]float64 {
	m := map[string]float64{}
	for _, x := range r.Rows {
		if x.Value != nil {
			m[x.Key] = *x.Value
		}
	}
	return m
}

func sem(t *testing.T, e *testkit.Env, tok string, body map[string]any) semResult {
	t.Helper()
	var out semResult
	e.Must(http.StatusOK, e.Call(tok, "POST", "/api/v1/semantic/query", body), &out)
	return out
}

func TestSemanticQueries(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")

	var cat struct {
		Metrics []struct {
			ID, Source string
			Dimensions []string
		}
		Dimensions []struct{ ID string }
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/semantic/catalog", nil), &cat)
	haveKPI, haveAlerts := false, false
	for _, m := range cat.Metrics {
		haveKPI = haveKPI || (m.Source == "kpi" && strings.HasPrefix(m.ID, "kpi.") && len(m.Dimensions) == 1)
		haveAlerts = haveAlerts || (m.ID == "alerts" && len(m.Dimensions) > 4)
	}
	if !haveKPI || !haveAlerts || len(cat.Dimensions) < 15 {
		t.Fatalf("catalogue: %+v", cat)
	}

	for i, sev := range []string{"error", "error", "warning", "info"} {
		alert(t, e, uuid.NewString(), map[bool]string{true: "regulatory.rejection", false: "license.expiring"}[i < 2], sev, "Alert "+sev)
	}
	superExec(t, e, `update alert set status='resolved' where severity='info'`)
	superExec(t, e, `insert into alert (id, tenant_id, kind, severity, title, created_at) values (gen_random_uuid(), $1, 'old', 'error', 'Old', now() - interval '200 days')`, e.Tenant.ID)

	// Metric by dimension, as a Viewer (read-only is enough).
	r := sem(t, e, viewer, map[string]any{"metric": "alerts", "dimension": "severity", "days": 30})
	if got := r.byKey(); len(got) != 3 || got["error"] != 2 || got["warning"] != 1 || got["info"] != 1 || r.Total == nil || *r.Total != 4 || r.Rows[0].Key != "error" {
		t.Fatalf("by severity: %+v", r)
	}
	if r := sem(t, e, viewer, map[string]any{"metric": "alerts"}); r.Total == nil || *r.Total != 4 || len(r.Rows) != 1 { // default period 30 days
		t.Fatalf("total: %+v", r)
	}
	if r := sem(t, e, viewer, map[string]any{"metric": "alerts", "days": 365}); *r.Total != 5 {
		t.Fatalf("older alerts: %+v", r)
	}
	r = sem(t, e, viewer, map[string]any{"metric": "alerts", "dimension": "kind", "filters": []map[string]string{{"dimension": "severity", "value": "error"}}})
	if got := r.byKey(); len(got) != 1 || got["regulatory.rejection"] != 2 {
		t.Fatalf("filtered: %+v", r)
	}
	r = sem(t, e, viewer, map[string]any{"metric": "alerts", "dimension": "status", "filters": []map[string]string{{"dimension": "status", "value": "open"}}})
	if got := r.byKey(); len(got) != 1 || got["open"] != 3 {
		t.Fatalf("open: %+v", r)
	}
	today := time.Now().UTC().Format(time.DateOnly)
	r = sem(t, e, viewer, map[string]any{"metric": "alerts", "dimension": "day", "from": today, "to": today}) // date-only end is inclusive
	if got := r.byKey(); len(got) != 1 || got[today] != 4 {
		t.Fatalf("by day: %+v", r)
	}
	r = sem(t, e, viewer, map[string]any{"metric": "alerts", "dimension": "month", "from": "2020-01-01T00:00:00Z", "to": time.Now().UTC().Add(time.Hour).Format(time.RFC3339), "days": 0})
	if len(r.Rows) == 0 {
		t.Fatalf("by month: %+v", r)
	}
	if r := sem(t, e, viewer, map[string]any{"metric": "epcis_events", "dimension": "event_type"}); len(r.Rows) != 0 || r.Total == nil || *r.Total != 0 {
		t.Fatalf("empty metric: %+v", r)
	}
	for _, id := range []string{"serial_updates", "cases", "agent_actions", "regulatory_reports", "shipments", "investigations", "b2b_documents", "food_lot_quantity"} {
		sem(t, e, viewer, map[string]any{"metric": id, "dimension": "week"})
	}

	// Every request is validated against the catalogue: nothing reaches SQL.
	bad := []map[string]any{
		{"metric": "alerts; drop table alert"},
		{"metric": "app_user"},
		{"metric": "alerts", "dimension": "severity) union select password_hash from app_user --"},
		{"metric": "alerts", "dimension": "gtin"},
		{"metric": "alerts", "filters": []map[string]string{{"dimension": "severity = 'x' or 1=1", "value": "x"}}},
		{"metric": "alerts", "from": "yesterday"},
		{"metric": "alerts", "from": "2026-01-01", "to": "nonsense"},
		{"metric": "alerts", "from": "2026-02-01", "to": "2026-01-01"},
		{"metric": "alerts", "days": 5000},
		{"metric": "kpi.otif_pct", "dimension": "status"},
	}
	for _, b := range bad {
		if r := e.Call(viewer, "POST", "/api/v1/semantic/query", b); r.Status != http.StatusBadRequest {
			t.Errorf("%v: HTTP %d %s", b, r.Status, r.Body)
		}
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/semantic/query", "{"), nil)
	// A hostile filter value is data: it matches nothing, and nothing is dropped.
	r = sem(t, e, viewer, map[string]any{"metric": "alerts", "filters": []map[string]string{{"dimension": "status", "value": "open' or '1'='1'; drop table alert; --"}}})
	if *r.Total != 0 {
		t.Fatalf("injection matched rows: %+v", r)
	}
	if r := sem(t, e, viewer, map[string]any{"metric": "alerts"}); *r.Total != 4 {
		t.Fatalf("alerts after the attempt: %+v", r)
	}

	// KPI metrics go through the KPI library (wired by the application).
	k := sem(t, e, viewer, map[string]any{"metric": "kpi.otif_pct", "dimension": "day", "days": 7, "limit": 3})
	if k.Metric != "kpi.otif_pct" || len(k.Rows) == 0 || len(k.Rows) > 3 {
		t.Fatalf("kpi by day: %+v", k)
	}
	if k := sem(t, e, viewer, map[string]any{"metric": "kpi.otif_pct", "days": 7}); len(k.Rows) != 1 {
		t.Fatalf("kpi total: %+v", k)
	}
}

func TestSemanticTenantIsolation(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	globex, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "globex", Name: "Globex", AdminEmail: "admin@globex.test", AdminPassword: testkit.Password})
	if err != nil {
		t.Fatal(err)
	}
	var gt struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "globex", "email": "admin@globex.test", "password": testkit.Password}), &gt)
	for i := 0; i < 3; i++ {
		alert(t, e, uuid.NewString(), "k", "error", "Acme alert")
	}
	for i := 0; i < 5; i++ {
		superExec(t, e, `insert into alert (id, tenant_id, kind, severity, title) values (gen_random_uuid(), $1, 'g', 'warning', 'Globex alert')`, globex.ID)
	}
	a := sem(t, e, e.Token, map[string]any{"metric": "alerts", "dimension": "severity"})
	g := sem(t, e, gt.Token, map[string]any{"metric": "alerts", "dimension": "severity"})
	if a.byKey()["error"] != 3 || len(a.Rows) != 1 || g.byKey()["warning"] != 5 || len(g.Rows) != 1 {
		t.Fatalf("acme %+v globex %+v", a, g)
	}
	// Even a filter naming the other tenant's value cannot reach its rows.
	if r := sem(t, e, e.Token, map[string]any{"metric": "alerts", "filters": []map[string]string{{"dimension": "severity", "value": "warning"}}}); *r.Total != 0 {
		t.Fatalf("filter crossed tenants: %+v", r)
	}
	// The planner's own predicate holds even if row-level security were not applied: run the metric as
	// the table owner (RLS bypassed) inside a tenant context and compare.
	m := actOf(e)
	if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		res, err := m.RunSemantic(ctx, tx, act.SemanticQuery{Metric: "alerts", From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
		if err != nil || res.Total == nil || *res.Total != 3 {
			t.Errorf("RunSemantic: %+v %v", res, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// writingKPIs tries to write from inside the semantic layer's transaction.
type writingKPIs struct{ err error }

func (w *writingKPIs) List() []act.KPIInfo {
	return []act.KPIInfo{{ID: "evil", Name: "Evil KPI", Unit: "count"}}
}
func (w *writingKPIs) Compute(ctx context.Context, tx pgx.Tx, _ string, _, _ time.Time) (*float64, []act.KPIPoint, error) {
	_, w.err = tx.Exec(ctx, `insert into alert (id, tenant_id, kind, title) values (gen_random_uuid(), app_tenant(), 'x', 'written by the semantic layer')`)
	return nil, nil, w.err
}

func TestSemanticExecutionIsReadOnly(t *testing.T) {
	e := testkit.New(t)
	w := &writingKPIs{}
	actOf(e).KPIs = w // before the catalogue is first built
	r := e.Do("POST", "/api/v1/semantic/query", map[string]any{"metric": "kpi.evil"})
	if r.Status == http.StatusOK || w.err == nil || !strings.Contains(w.err.Error(), "read-only") {
		t.Fatalf("write was not refused: HTTP %d %s / %v", r.Status, r.Body, w.err)
	}
	var n struct{ Total int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions", nil), &n) // the transaction is still healthy afterwards
	if r := sem(t, e, e.Token, map[string]any{"metric": "alerts"}); *r.Total != 0 {
		t.Fatalf("a row was written: %+v", r)
	}
}

type pickLLM struct{ reply string }

func (p *pickLLM) Name() string { return "pick" }
func (p *pickLLM) Answer(_ context.Context, system string, msgs []act.ChatMessage) (string, error) {
	if strings.Contains(system, "CATALOGUE") {
		return p.reply, nil
	}
	return "model answer; system had: " + system[strings.Index(system, "QUERY RESULT"):], nil
}

type failingLLM struct{}

func (failingLLM) Name() string { return "failing" }
func (failingLLM) Answer(context.Context, string, []act.ChatMessage) (string, error) {
	return "", errors.New("overloaded")
}

func chat(t *testing.T, e *testkit.Env, q string) (reply string, cites []struct{ Title, Ref string }) {
	t.Helper()
	var out struct {
		Reply     string
		Citations []struct{ Title, Ref string }
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/assistant/chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": q}}}), &out)
	return out.Reply, out.Citations
}

func TestAssistantUsesTheSemanticLayerAndDocuments(t *testing.T) {
	e := testkit.New(t)
	for _, sev := range []string{"error", "error", "warning"} {
		alert(t, e, uuid.NewString(), "k", sev, "Alert")
	}
	// Without a model: the deterministic reader and a rendered answer.
	reply, cites := chat(t, e, "How many alerts by severity in the last 7 days?")
	if !strings.Contains(reply, "Alerts by severity") || !strings.Contains(reply, "error 2") || !strings.Contains(reply, "warning 1") {
		t.Fatalf("reply: %s", reply)
	}
	found := false
	for _, c := range cites {
		found = found || c.Ref == "metric/alerts"
	}
	if !found {
		t.Fatalf("citations: %+v", cites)
	}
	var au struct {
		Items []struct{ After map[string]any }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=assistant.ask", nil), &au)
	if au.Items[0].After["metric"] != "alerts" || au.Items[0].After["dimension"] != "severity" {
		t.Fatalf("audit: %+v", au.Items[0].After)
	}
	if reply, _ := chat(t, e, "How many alerts are there?"); !strings.Contains(reply, "Alerts, ") || !strings.Contains(reply, "3.") {
		t.Fatalf("single value: %s", reply)
	}
	if reply, _ := chat(t, e, "How many shipments by status last year?"); !strings.Contains(reply, "no data in the period") {
		t.Fatalf("empty: %s", reply)
	}
	// Questions that name no metric do not get one.
	if reply, _ := chat(t, e, "How is my workspace doing?"); strings.Contains(reply, " by ") && strings.Contains(reply, "Alerts,") {
		t.Fatalf("unexpected metric: %s", reply)
	}

	// Document passages are cited.
	var d struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/documents", map[string]any{"name": "SOP-12 quarantine.txt", "content": "Quarantined stock is stored in cage 4 until the qualified person signs the release."}), &d)
	reply, cites = chat(t, e, "Where is quarantined stock stored?")
	hit := false
	for _, c := range cites {
		hit = hit || c.Ref == "document/"+d.ID+"#0"
	}
	if !hit || !strings.Contains(reply, "From your documents") || !strings.Contains(reply, "cage 4") {
		t.Fatalf("document citation: %s %+v", reply, cites)
	}

	// With a model: it may pick a metric from the catalogue when the reader finds none, and the choice is validated.
	m := actOf(e)
	llm := &pickLLM{reply: `Sure: {"metric": "cases", "dimension": "status", "days": 10}`}
	m.LLM = llm
	reply, cites = chat(t, e, "What is the overall count of things needing human attention?")
	if !strings.Contains(reply, "QUERY RESULT") || !strings.Contains(reply, `"metric":"cases"`) {
		t.Fatalf("model path: %s", reply)
	}
	got := false
	for _, c := range cites {
		got = got || c.Ref == "metric/cases"
	}
	if !got {
		t.Fatalf("citations: %+v", cites)
	}
	for _, bad := range []string{`{"metric": "alerts; drop table alert"}`, `{"metric": "app_user", "days": 3}`, `{"metric": "alerts", "dimension": "gtin"}`, `{"metric": ""}`, `not json`, `{"metric": 5}`} {
		llm.reply = bad
		reply, cites = chat(t, e, "What is the overall count of things needing human attention?")
		for _, c := range cites {
			if strings.HasPrefix(c.Ref, "metric/") {
				t.Fatalf("model output %q produced %+v", bad, c)
			}
		}
	}
	llm.reply = `{"metric": "alerts", "days": 99999}` // out-of-range days fall back to the default period
	if _, cites = chat(t, e, "What is the overall count of things needing human attention?"); len(cites) == 0 {
		t.Fatal("expected the alerts metric")
	}
	m.LLM = failingLLM{}
	if reply, _ := chat(t, e, "What is the overall count of things needing human attention?"); !strings.Contains(reply, "Model unavailable") {
		t.Fatalf("failing model: %s", reply)
	}
	// A model is not called to pick a metric for a question that asks for no figure.
	m.LLM = &pickLLM{reply: `{"metric": "alerts"}`}
	if reply, _ := chat(t, e, "Explain the quarantine procedure."); strings.Contains(reply, `"metric":"alerts"`) {
		t.Fatalf("model consulted needlessly: %s", reply)
	}
}
