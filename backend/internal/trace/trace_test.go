package trace_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

const (
	gtin = "09506000134352"
	locA = "urn:epc:id:sgln:9506000.00001.0"
	locB = "urn:epc:id:sgln:0614141.00002.0"
	locC = "urn:epc:id:sgln:0614141.00003.0"
)

func userToken(t *testing.T, e *testkit.Env, tenant, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": tenant, "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

// packs registers the product and allocates n serials, returning their SGTIN URNs.
func packs(t *testing.T, e *testkit.Env, n int) []string {
	t.Helper()
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Amoxicillin 500mg"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": n}, "Idempotency-Key", "k"), &a)
	var out []string
	for _, s := range a.Serials {
		u, _ := gs1.SGTINURN(gtin, s, 7)
		out = append(out, u)
	}
	return out
}

func capture(t *testing.T, e *testkit.Env, evs ...lib.Event) {
	t.Helper()
	b, err := lib.WriteJSON(lib.NewDocument(evs))
	if err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()
}

func obj(at time.Time, step, disp, loc string, epcs ...string) lib.Event {
	ev := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: step, Disposition: disp, EventTime: at, EventTimeZoneOffset: "+00:00", EPCList: epcs}
	if loc != "" {
		ev.BizLocation = &lib.Location{ID: loc}
	}
	return ev
}

// scenario captures a lot of six packs with a gap, a missing receipt and a double shipment.
func scenario(t *testing.T, e *testkit.Env) []string {
	t.Helper()
	u := packs(t, e, 6)
	t0 := time.Now().Add(-10 * time.Hour).UTC().Truncate(time.Second)
	comm := obj(t0, "commissioning", "active", locA, u...)
	comm.Action = "ADD"
	comm.ILMD = map[string]any{"lotNumber": "L1", "itemExpirationDate": "2030-01-31"}
	ship := obj(t0.Add(time.Hour), "shipping", "in_transit", locA, u[0], u[1], u[2])
	ship.DestinationList = []lib.Destination{{Type: "owning_party", Destination: locB}}
	recv := obj(t0.Add(2*time.Hour), "receiving", "in_progress", locB, u[0], u[1])
	storeB := obj(t0.Add(3*time.Hour), "storing", "available", locB, u[2])
	storeC := obj(t0.Add(4*time.Hour), "storing", "available", locC, u[3])
	ship4a := obj(t0.Add(5*time.Hour), "shipping", "in_transit", "", u[4])
	ship4b := obj(t0.Add(6*time.Hour), "shipping", "in_transit", "", u[4])
	capture(t, e, comm, ship, recv, storeB, storeC, ship4a, ship4b)
	return u
}

type balance struct {
	Lots []struct {
		Lot            string
		Serials        int
		Commissioned   int
		Shipped        int
		Received       int
		Decommissioned int
		Destroyed      int
		Inflow         int
		Outflow        int
		ExpectedOnHand int
		Discrepancy    int
		Allowed        int
		Closed         bool
		Anomalies      []struct{ Epc string }
	}
	Settings  struct{ IsDefault bool }
	Truncated bool
	Notes     []string
}

func TestMassBalanceAndGaps(t *testing.T) {
	e := testkit.New(t)
	scenario(t, e)
	viewer := userToken(t, e, "acme", "viewer@acme.test", "Viewer")

	var b balance
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/balance?gtin="+gtin, nil), &b)
	if len(b.Lots) != 1 || !b.Settings.IsDefault || len(b.Notes) < 3 {
		t.Fatalf("balance: %+v", b)
	}
	l := b.Lots[0]
	if l.Lot != "L1" || l.Serials != 6 || l.Commissioned != 6 || l.Shipped != 4 || l.Received != 2 || l.Decommissioned != 0 || l.Destroyed != 0 {
		t.Fatalf("counts: %+v", l)
	}
	if l.Inflow != 8 || l.Outflow != 5 || l.ExpectedOnHand != 4 || l.Discrepancy != 1 || l.Allowed != 0 || l.Closed || len(l.Anomalies) != 1 {
		t.Fatalf("balance figures: %+v", l)
	}
	// Viewers may read.
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/trace/balance?gtin="+gtin+"&lot=L1", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/trace/balance?gtin=12345678901234", nil), nil)

	var g struct {
		Lots []struct {
			Lot     string
			Summary struct{ High, Medium, Low int }
			Gaps    []struct{ Kind, Severity, Epc string }
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/balance/gaps?gtin="+gtin, nil), &g)
	if len(g.Lots) != 1 || g.Lots[0].Summary.High != 1 || g.Lots[0].Summary.Medium != 1 {
		t.Fatalf("gaps: %+v", g)
	}
	kinds := map[string]string{}
	for _, x := range g.Lots[0].Gaps {
		kinds[x.Kind] = x.Severity
	}
	if kinds["no_link"] != "high" || kinds["shipped_not_received"] != "medium" || len(kinds) != 2 {
		t.Fatalf("gap kinds: %v", kinds)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/balance/gaps?gtin="+gtin+"&minSeverity=high", nil), &g)
	if len(g.Lots[0].Gaps) != 1 {
		t.Fatalf("minSeverity: %+v", g)
	}
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/trace/balance/gaps?gtin="+gtin+"&minSeverity=urgent", nil), nil)

	// Writes: Viewer is read-only.
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/trace/balance/run", map[string]any{"gtin": gtin}), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", "/api/v1/trace/balance/settings", map[string]any{"toleranceUnits": 1}), nil)

	var run struct {
		Reports []struct {
			ID, Status string
		}
		Alerts []struct {
			Kind    string
			Created bool
		}
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/balance/run", map[string]any{"gtin": gtin}), &run)
	if len(run.Reports) != 1 || run.Reports[0].Status != "discrepancy" || len(run.Alerts) != 2 || !run.Alerts[0].Created {
		t.Fatalf("run: %+v", run)
	}
	var alerts struct {
		Items []struct {
			Kind, Severity string
			Detail         map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=trace.", nil), &alerts)
	if len(alerts.Items) != 2 {
		t.Fatalf("alerts: %+v", alerts)
	}
	for _, a := range alerts.Items {
		if _, ok := a.Detail["caseProposal"]; ok {
			t.Fatalf("no case proposal without the setting: %+v", a)
		}
	}
	// A second run reuses the open alerts.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/balance/run", map[string]any{"gtin": gtin, "lot": "L1"}), &run)
	if run.Alerts[0].Created || run.Alerts[1].Created {
		t.Fatalf("alerts must be deduplicated: %+v", run.Alerts)
	}

	// Tolerance of one unit closes the balance and resolves its alert; a case is proposed for the gap.
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/trace/balance/settings", map[string]any{"tolerancePercent": 101}), nil)
	var s struct {
		ToleranceUnits int
		ProposeCase    bool
		IsDefault      bool
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/trace/balance/settings", map[string]any{"toleranceUnits": 1, "tolerancePercent": 0.5, "proposeCase": true, "checkpointHours": 12}), &s)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/balance/settings", nil), &s)
	if s.ToleranceUnits != 1 || !s.ProposeCase || s.IsDefault {
		t.Fatalf("settings: %+v", s)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/balance/run", map[string]any{"gtin": gtin}), &run)
	if run.Reports[0].Status != "balanced" || len(run.Alerts) != 1 || run.Alerts[0].Kind != "trace.custody_gap" {
		t.Fatalf("run with tolerance: %+v", run)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=trace.custody_gap", nil), &alerts)
	if _, ok := alerts.Items[0].Detail["caseProposal"]; ok {
		t.Log("custody alert already open keeps its first detail")
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=trace.balance_discrepancy&status=resolved", nil), &alerts)
	if len(alerts.Items) != 1 {
		t.Fatalf("balance alert should be resolved: %+v", alerts)
	}
	var reps struct{ Items []struct{ Status string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/balance/reports?gtin="+gtin, nil), &reps)
	if len(reps.Items) != 3 || reps.Items[0].Status != "balanced" {
		t.Fatalf("reports: %+v", reps)
	}
	var au struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=200", nil), &au)
	n := map[string]int{}
	for _, a := range au.Items {
		n[a.Action]++
	}
	if n["trace.balance.run"] != 3 || n["trace.settings.update"] != 1 {
		t.Fatalf("audit entries: %v", n)
	}
}

func TestCaseProposalInAlert(t *testing.T) {
	e := testkit.New(t)
	scenario(t, e)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/trace/balance/settings", map[string]any{"proposeCase": true, "checkpointHours": 24}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/balance/run", map[string]any{"gtin": gtin}), nil)
	var alerts struct {
		Items []struct {
			Kind   string
			Detail struct {
				CaseProposal struct {
					Endpoint string
					Body     map[string]any
				}
			}
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=trace.", nil), &alerts)
	if len(alerts.Items) != 2 {
		t.Fatalf("alerts: %+v", alerts)
	}
	for _, a := range alerts.Items {
		if !strings.HasPrefix(a.Detail.CaseProposal.Endpoint, "POST ") || a.Detail.CaseProposal.Body["gtin"] != gtin || a.Detail.CaseProposal.Body["kind"] != "suspect" {
			t.Fatalf("case proposal missing: %+v", a)
		}
	}
	// Nothing was opened.
	var inv struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/investigations", nil), &inv)
	if len(inv.Items) != 0 {
		t.Fatalf("a case must not be opened automatically: %+v", inv)
	}
}

func TestTenantIsolation(t *testing.T) {
	e := testkit.New(t)
	scenario(t, e)
	_, other := e.NewTenant("globex", "admin@globex.test")
	var b balance
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/trace/balance?gtin="+gtin, nil), &b)
	if len(b.Lots) != 0 {
		t.Fatalf("another tenant must see nothing: %+v", b)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/balance/run", map[string]any{"gtin": gtin}), nil)
	var reps struct{ Items []any }
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/trace/balance/reports", nil), &reps)
	if len(reps.Items) != 0 {
		t.Fatalf("reports leak across tenants: %+v", reps)
	}
	e.Must(http.StatusNotFound, e.Call(other, "POST", "/api/v1/trace/recalls", map[string]any{"gtin": gtin, "lot": "L1", "reason": "x"}), nil)
}

func TestAutoBalance(t *testing.T) {
	e := testkit.New(t)
	scenario(t, e)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/trace/balance/settings", map[string]any{"balanceAuto": true, "checkpointHours": 24}), nil)
	runSchedule(t, e, "trace.balance")
	var reps struct{ Items []struct{ Status string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/balance/reports", nil), &reps)
	if len(reps.Items) != 1 || reps.Items[0].Status != "discrepancy" {
		t.Fatalf("auto run: %+v", reps)
	}
	runSchedule(t, e, "trace.balance") // nothing changed since: no second report
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/balance/reports", nil), &reps)
	if len(reps.Items) != 1 {
		t.Fatalf("unchanged lots must not be re-run: %+v", reps)
	}
}

// runSchedule runs one of the trace module's periodic tasks once.
func runSchedule(t *testing.T, e *testkit.Env, name string) {
	t.Helper()
	for _, m := range e.App.Modules {
		if s, ok := m.(kernel.Scheduled); ok {
			for _, sc := range s.Schedules() {
				if sc.Name == name {
					if err := sc.Run(context.Background()); err != nil {
						t.Fatal(err)
					}
					return
				}
			}
		}
	}
	t.Fatalf("no schedule %s", name)
}

// shipTo ships the packs to a destination (none when dest is empty).
func shipTo(at time.Time, dest string, epcs ...string) lib.Event {
	ev := obj(at.UTC(), "shipping", "in_transit", "", epcs...)
	if dest != "" {
		ev.DestinationList = []lib.Destination{{Type: "owning_party", Destination: dest}}
	}
	return ev
}

func TestSerialTreeStatsAndState(t *testing.T) {
	e := testkit.New(t)
	u := scenario(t, e)
	viewer := userToken(t, e, "acme", "viewer@acme.test", "Viewer")
	sscc := gs1.WithCheckDigit("09506000" + "00000001" + "")
	sscc = gs1.WithCheckDigit(sscc[:17])
	pallet, err := gs1.SSCCURN(sscc, 7)
	if err != nil {
		t.Fatal(err)
	}
	agg := lib.Event{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", EventTime: time.Now().Add(-time.Hour).UTC(), EventTimeZoneOffset: "+00:00", ParentID: pallet, ChildEPCs: []string{u[3], u[5]}}
	capture(t, e, agg)

	var s struct {
		Epc   string
		State struct {
			Status    string
			Lot       string
			ParentEpc string
		}
		Ancestors []struct{ Epc string }
		History   []map[string]any
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/trace/serials/"+u[3], nil), &s)
	if s.State.Lot != "L1" || len(s.Ancestors) != 1 || len(s.History) < 2 || s.State.ParentEpc == "" {
		t.Fatalf("serial: %+v", s)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/x?id="+u[5], nil), &s)
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/trace/serials/urn:epc:id:sgtin:9506000.013435.NOPE", nil), nil)

	var tree struct {
		Epc      string
		Children []struct{ Epc string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/tree/x?id="+pallet, nil), &tree)
	if len(tree.Children) != 2 {
		t.Fatalf("tree: %+v", tree)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/trace/tree/x?id=urn:epc:id:sscc:9506000.0999999999", nil), nil)

	var st struct {
		Events, Serials int
		ByStatus        map[string]int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/stats", nil), &st)
	if st.Events != 8 || st.Serials < 6 {
		t.Fatalf("stats: %+v", st)
	}
	var list struct{ Items []struct{ Lot string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/state?gtin="+gtin+"&lot=L1&limit=3", nil), &list)
	if len(list.Items) != 3 {
		t.Fatalf("state: %+v", list)
	}
	if (&trace.Module{}).Name() != "trace" {
		t.Fatal("name")
	}
}
