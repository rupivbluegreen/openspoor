package connectors_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// sql runs a statement against the test database outside the API (job scheduling only).
func sql(t *testing.T, e *testkit.Env, stmt string, args ...any) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.DBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, stmt, args...); err != nil {
		t.Fatal(err)
	}
}

// settle runs background jobs, pulling delayed retries and polls forward, until reports settle.
func settle(t *testing.T, e *testkit.Env, want int) []connectors.Report {
	t.Helper()
	for i := 0; i < 60; i++ {
		sql(t, e, `update job set run_at=now() where status='queued' and kind like 'connectors.%'`)
		e.Drain()
		var rs struct{ Items []connectors.Report }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/reports", nil), &rs)
		done := len(rs.Items) >= want
		for _, r := range rs.Items {
			if r.Status == "queued" || r.Status == "submitted" {
				done = false
			}
		}
		if done {
			return rs.Items
		}
	}
	var rs struct{ Items []connectors.Report }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/reports", nil), &rs)
	t.Fatalf("reports did not settle: %+v", rs.Items)
	return nil
}

func login(t *testing.T, e *testkit.Env, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

func packs(t *testing.T, e *testkit.Env, gtin string, markets []string, n int, key string) []string {
	t.Helper()
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Paracetamol 500mg", "targetMarkets": markets}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": markets[0]}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": n}, "Idempotency-Key", key), &a)
	var out []string
	for _, s := range a.Serials {
		u, _ := gs1.SGTINURN(gtin, s, 7)
		out = append(out, u)
	}
	return out
}

func captureDoc(t *testing.T, e *testkit.Env, evs ...lib.Event) string {
	t.Helper()
	b, _ := lib.WriteJSON(lib.NewDocument(evs))
	var c struct{ CaptureID string }
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), &c)
	if c.CaptureID == "" {
		t.Fatal("no capture id")
	}
	return c.CaptureID
}

func sgln(gln string) string { return gs1.DefaultResolver + "/414/" + gln }

func TestSARSDDispatchWithContainersAndRoles(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "v@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")

	var cat struct {
		Items []struct{ ID, Market string }
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/connectors", nil), &cat)
	if len(cat.Items) != 2 || cat.Items[0].ID != "eu-fmd-emvs" || cat.Items[1].ID != "sa-rsd" {
		t.Fatalf("catalog: %+v", cat.Items)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/connectors/instances", map[string]any{"connector": "sa-rsd"}), nil)
	e.Must(http.StatusForbidden, e.Call(ops, "GET", "/api/v1/reports", nil), nil)

	// Instance validation.
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "xx-none"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sa-rsd", "config": map[string]string{"colour": "red"}}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sa-rsd", "config": map[string]string{"baseUrl": ""}}), nil)
	var inst struct {
		ID, Name   string
		Config     map[string]string
		SecretsSet map[string]bool
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sa-rsd", "config": map[string]string{"username": "u", "password": "pw"}}), &inst)
	if inst.Config["baseUrl"] != "local-mock" || inst.Config["password"] != "" || !inst.SecretsSet["password"] || !strings.Contains(inst.Name, "SFDA") {
		t.Fatalf("instance: %+v", inst)
	}

	// Rename keeps the sealed secret; bad ids and unknown keys are rejected.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"name": "RSD prod", "config": map[string]string{"baseUrl": "local-mock", "username": "u2", "password": ""}}), &inst)
	if inst.Name != "RSD prod" || inst.Config["username"] != "u2" || !inst.SecretsSet["password"] {
		t.Fatalf("update: %+v", inst)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{}), &inst)
	if inst.Name != "RSD prod" || inst.Config["username"] != "u2" {
		t.Fatalf("empty update must keep values: %+v", inst)
	}
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"config": map[string]string{"nope": "x"}}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/connectors/instances/bad-id", map[string]any{}), nil)
	e.Must(http.StatusNotFound, e.Do("PUT", "/api/v1/connectors/instances/00000000-0000-0000-0000-000000000001", map[string]any{}), nil)

	gtin := gs1.WithCheckDigit("0628100000001")
	epcs := packs(t, e, gtin, []string{"SA"}, 3, "sa")
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	sscc := "urn:epc:id:sscc:6281000.0000000016"
	plant := gs1.WithCheckDigit("628100000100")
	receiver := gs1.WithCheckDigit("628100000200")
	captureDoc(t, e,
		lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: epcs,
			BizLocation: &lib.Location{ID: sgln(plant)}, ILMD: map[string]any{"lotNumber": "L7", "itemExpirationDate": "2030-12-31"}},
		lib.Event{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", EventTime: t0.Add(time.Minute), EventTimeZoneOffset: "+00:00", ParentID: sscc, ChildEPCs: epcs})
	rs := settle(t, e, 1)
	if len(rs) != 1 || rs[0].Kind != "production" || rs[0].Status != "accepted" {
		t.Fatalf("production: %+v", rs)
	}
	if !strings.Contains(string(rs[0].Payload), `"fromGln":"`+plant+`"`) || !strings.Contains(string(rs[0].Payload), `"batch":"L7"`) {
		t.Fatalf("production payload: %s", rs[0].Payload)
	}

	// Dispatch of the case without a receiver: rejected; the case is expanded to its packs.
	captureDoc(t, e, lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", Disposition: "in_transit", EventTime: t0.Add(2 * time.Minute), EventTimeZoneOffset: "+00:00",
		EPCList: []string{sscc}, ReadPoint: &lib.Location{ID: sgln(plant)}})
	rs = settle(t, e, 2)
	var rej connectors.Report
	for _, r := range rs {
		if r.Kind == "dispatch" {
			rej = r
		}
	}
	if rej.Status != "rejected" || len(rej.Packs) != 3 || rej.Packs[0].Lot != "L7" || rej.Packs[0].Expiry != "2030-12-31" {
		t.Fatalf("dispatch: %+v", rej)
	}
	var al struct {
		Items []struct {
			Title  string
			Detail map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=regulatory.", nil), &al)
	if len(al.Items) != 1 || !strings.Contains(al.Items[0].Detail["suggestedFix"].(string), "receiver's GLN") || !strings.Contains(al.Items[0].Title, "DISPATCH") {
		t.Fatalf("alert: %+v", al.Items)
	}
	var one connectors.Report
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/reports/"+rej.ID.String(), nil), &one)
	if one.ID != rej.ID || len(one.Rejection) == 0 || len(one.Alert) == 0 {
		t.Fatalf("get report: %+v", one)
	}
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/reports/xyz", nil), nil)
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/reports/00000000-0000-0000-0000-000000000001", nil), nil)

	// Shipping with an owning-party destination reaches the hub with the receiver GLN.
	captureDoc(t, e, lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", Disposition: "in_transit", EventTime: t0.Add(3 * time.Minute), EventTimeZoneOffset: "+00:00",
		EPCList: []string{sscc}, SourceList: []lib.Source{{Type: "owning_party", Source: sgln(plant)}}, DestinationList: []lib.Destination{{Type: "owning_party", Destination: sgln(receiver)}}})
	settle(t, e, 3)
	var acc struct{ Items []connectors.Report }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/reports?status=accepted&connector=sa-rsd", nil), &acc)
	if len(acc.Items) != 2 || !strings.Contains(string(acc.Items[0].Payload), `"toGln":"`+receiver+`"`) {
		t.Fatalf("accepted dispatch: %+v", acc.Items)
	}
	// Accepted reports cannot be resubmitted.
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/reports/"+acc.Items[0].ID.String()+"/resubmit", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/reports/xyz/resubmit", nil), nil)

	// Tenant isolation: another tenant sees none of this.
	_, other := e.NewTenant("globex", "admin@globex.test")
	var none struct{ Items []connectors.Report }
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/reports", nil), &none)
	if len(none.Items) != 0 {
		t.Fatalf("leaked reports: %d", len(none.Items))
	}
	e.Must(http.StatusNotFound, e.Call(other, "GET", "/api/v1/reports/"+rej.ID.String(), nil), nil)
	e.Must(http.StatusConflict, e.Call(other, "POST", "/api/v1/reports/"+rej.ID.String()+"/resubmit", nil), nil)

	// Disabling the instance stops new reports.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"enabled": false}), nil)
	captureDoc(t, e, lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "receiving", EventTime: t0.Add(4 * time.Minute), EventTimeZoneOffset: "+00:00", EPCList: epcs})
	if rs := settle(t, e, 3); len(rs) != 3 {
		t.Fatalf("disabled instance still reports: %d", len(rs))
	}

	var aud struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=connector.", nil), &aud)
	if len(aud.Items) != 4 { // create + 3 updates
		t.Fatalf("connector audit: %+v", aud.Items)
	}
}

func TestEUFMDSubmitFailuresRetriesAndManualSubmit(t *testing.T) {
	e := testkit.New(t)
	var hits atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	var broken struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "eu-fmd-emvs", "name": "EU broken", "config": map[string]string{"baseUrl": down.URL}}), &broken)

	gtin := "09506000134352"
	nl := packs(t, e, gtin, []string{"NL"}, 2, "nl")
	us := packs(t, e, "00614141000012", []string{"US"}, 1, "us")
	exp := time.Now().AddDate(2, 0, 0).Format("2006-01-02")
	t0 := time.Now().UTC().Add(-time.Hour)
	commission := func(epcs []string, lot string, at time.Time) lib.Event {
		return lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: at, EventTimeZoneOffset: "+00:00", EPCList: epcs,
			ILMD: map[string]any{"lotNumber": lot, "itemExpirationDate": exp}}
	}
	// US-only products are not reported to the EU hub.
	captureDoc(t, e, commission(us, "U1", t0))
	cid := captureDoc(t, e, commission(nl, "N1", t0.Add(time.Minute)))

	rs := settle(t, e, 1)
	if len(rs) != 1 || rs[0].Status != "failed" || rs[0].Attempts != 6 || int(hits.Load()) != 6 {
		t.Fatalf("retries: %+v hits=%d", rs, hits.Load())
	}
	var al struct {
		Items []struct {
			ID, Title string
			Detail    map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=regulatory.", nil), &al)
	if len(al.Items) != 1 || !strings.Contains(al.Items[0].Title, "submission failed after retries") || al.Items[0].Detail["retryable"] != true {
		t.Fatalf("failure alert: %+v", al.Items)
	}

	// Point the instance at the mock hub, register the product, resubmit: accepted, alert resolved.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+broken.ID, map[string]any{"config": map[string]string{"baseUrl": "local-mock"}}), nil)
	mod := findConnectors(e)
	if mod.Name() != "connectors" {
		t.Fatal(mod.Name())
	}
	mod.RegisterDemoHubProduct(gtin)
	var re connectors.Report
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/reports/"+rs[0].ID.String()+"/resubmit", nil), &re)
	if re.Status != "queued" {
		t.Fatalf("resubmit: %+v", re)
	}
	rs = settle(t, e, 1)
	if rs[0].Status != "accepted" || rs[0].SubmissionRef == "" {
		t.Fatalf("after resubmit: %+v", rs[0])
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=regulatory.", nil), &al)
	if len(al.Items) != 0 {
		t.Fatalf("alert not resolved: %+v", al.Items)
	}

	// A second instance added later can be fed an earlier capture by hand.
	var second struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "eu-fmd-emvs", "name": "EU second", "enabled": true}), &second)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances/xyz/submit", map[string]any{"captureId": cid}), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/connectors/instances/00000000-0000-0000-0000-000000000001/submit", map[string]any{"captureId": cid}), nil)
	var job struct{ JobID int64 }
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/connectors/instances/"+second.ID+"/submit", map[string]any{"captureId": cid}), &job)
	if job.JobID == 0 {
		t.Fatal("no job id")
	}
	rs = settle(t, e, 2)
	var forSecond []connectors.Report
	for _, r := range rs {
		if r.InstanceID.String() == second.ID {
			forSecond = append(forSecond, r)
		}
	}
	// The hub already holds these serials from the first instance: duplicate, not retryable.
	if len(forSecond) != 1 || forSecond[0].Status != "rejected" {
		t.Fatalf("manual submit: %+v", rs)
	}
	var alert map[string]any
	_ = json.Unmarshal(forSecond[0].Alert, &alert)
	if alert["retryable"] != false || !strings.Contains(alert["suggestedFix"].(string), "already exist") {
		t.Fatalf("duplicate alert: %v", alert)
	}
	// Re-running the same capture is idempotent.
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/connectors/instances/"+second.ID+"/submit", map[string]any{"captureId": cid}), nil)
	if rs := settle(t, e, 2); len(rs) != 2 {
		t.Fatalf("duplicate reports: %d", len(rs))
	}
}

func TestPollGivesUpWhenHubNeverAnswers(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	findConnectors(e).Mocks.RegisterEUProduct(gtin)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "eu-fmd-emvs"}), nil)
	epcs := packs(t, e, gtin, []string{"DE"}, 1, "de")
	captureDoc(t, e, lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", EventTime: time.Now().UTC().Add(-time.Hour), EventTimeZoneOffset: "+00:00", EPCList: epcs,
		ILMD: map[string]any{"lotNumber": "D1", "itemExpirationDate": time.Now().AddDate(1, 0, 0).Format("2006-01-02")}})
	e.Drain() // projection, dispatch and submit; the first poll is scheduled in the future
	var rs struct{ Items []connectors.Report }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/reports", nil), &rs)
	if len(rs.Items) != 1 || rs.Items[0].Status != "submitted" {
		t.Fatalf("submitted: %+v", rs.Items)
	}
	// Pretend 40 polls already came back pending: the next pending answer fails the report.
	sql(t, e, `update job set payload = jsonb_set(payload, '{poll}', '40'), run_at=now() where kind='connectors.poll' and status='queued'`)
	e.Drain()
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/reports", nil), &rs)
	if rs.Items[0].Status != "failed" || !strings.Contains(string(rs.Items[0].Response), "PENDING") {
		t.Fatalf("poll limit: %+v", rs.Items[0])
	}
}

func TestRulePacks(t *testing.T) {
	e := testkit.New(t)
	var pk struct {
		ID, Market string
		Rules      []struct{ ID string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/rulepacks/us-dscsa", nil), &pk)
	if pk.Market != "US" || len(pk.Rules) == 0 {
		t.Fatalf("pack: %+v", pk)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/rulepacks/xx-none", nil), nil)

	var f struct {
		Items []struct{ Pack, Rule, Entity string }
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/rulepacks/evaluate", nil), &f)
	if len(f.Items) != 0 {
		t.Fatalf("no products, no findings: %+v", f.Items)
	}
	packs(t, e, "00614141000012", []string{"US"}, 1, "us") // no NDC
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/rulepacks/evaluate", nil), &f)
	got := map[string]bool{}
	for _, x := range f.Items {
		got[x.Pack+"/"+x.Rule] = true
		if x.Pack == "eu-fmd" || x.Pack == "sa-rsd" {
			t.Fatalf("pack for another market applied: %+v", x)
		}
	}
	if !got["us-dscsa/ndc-present"] {
		t.Fatalf("findings: %+v", f.Items)
	}
}
