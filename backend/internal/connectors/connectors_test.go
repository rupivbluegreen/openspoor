package connectors_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

type report struct {
	ID, Status, Kind string
	Alert            map[string]any
}

func waitReports(t *testing.T, e *testkit.Env, want int, done func([]report) bool) []report {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		e.Drain()
		var rs struct{ Items []report }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/reports", nil), &rs)
		if len(rs.Items) >= want && done(rs.Items) {
			return rs.Items
		}
		if time.Now().After(deadline) {
			t.Fatalf("reports not settled: %+v", rs.Items)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func settled(rs []report) bool {
	for _, r := range rs {
		if r.Status == "queued" || r.Status == "submitted" {
			return false
		}
	}
	return true
}

func TestEUFMDRejectionFixResubmit(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Ibuprofen 400mg", "targetMarkets": []string{"NL", "DE"}}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 3}, "Idempotency-Key", "k"), &a)

	var inst struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "eu-fmd-emvs", "config": map[string]string{"apiKey": "secret-123"}}), &inst)
	var insts struct {
		Items []struct {
			Config     map[string]string
			SecretsSet map[string]bool
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/connectors/instances", nil), &insts)
	if insts.Items[0].Config["apiKey"] != "" || !insts.Items[0].SecretsSet["apiKey"] {
		t.Fatalf("secret leaked or lost: %+v", insts.Items[0])
	}

	var epcs []string
	for _, s := range a.Serials {
		u, _ := gs1.SGTINURN(gtin, s, 7)
		epcs = append(epcs, u)
	}
	exp := time.Now().AddDate(3, 0, 0).Format("2006-01-02")
	comm := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: time.Now().UTC().Add(-time.Hour), EventTimeZoneOffset: "+00:00",
		EPCList: epcs, ILMD: map[string]any{"lotNumber": "B-77", "itemExpirationDate": exp}}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{comm}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)

	// The hub does not know the product yet: rejected, with an actionable alert.
	rs := waitReports(t, e, 1, settled)
	if rs[0].Status != "rejected" || rs[0].Kind != "pack-data-upload" {
		t.Fatalf("expected rejection: %+v", rs)
	}
	var alerts struct {
		Items []struct {
			ID     string
			Detail map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=regulatory.", nil), &alerts)
	if len(alerts.Items) != 1 || alerts.Items[0].Detail["suggestedFix"] == "" || alerts.Items[0].Detail["reportId"] != rs[0].ID {
		t.Fatalf("alert: %+v", alerts.Items)
	}
	if packs, _ := alerts.Items[0].Detail["packs"].([]any); len(packs) > 3 {
		t.Fatalf("packs: %v", packs)
	}

	// Fix at the hub, then one-click resubmit.
	mod := findConnectors(e)
	mod.Mocks.RegisterEUProduct(gtin)
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/reports/"+rs[0].ID+"/resubmit", nil), nil)
	rs = waitReports(t, e, 1, settled)
	if rs[0].Status != "accepted" {
		t.Fatalf("after resubmit: %+v", rs)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=regulatory.", nil), &alerts)
	if len(alerts.Items) != 0 {
		t.Fatal("alert not resolved after resubmit")
	}

	// Dispensing one pack produces a status update.
	disp := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "dispensing", Disposition: "dispensed", EventTime: time.Now().UTC(), EventTimeZoneOffset: "+00:00", EPCList: epcs[:1]}
	b, _ = lib.WriteJSON(lib.NewDocument([]lib.Event{disp}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	rs = waitReports(t, e, 2, settled)
	for _, r := range rs {
		if r.Status != "accepted" {
			t.Fatalf("status update: %+v", rs)
		}
	}

	// Rule packs flag the product's missing detail for EU.
	var f struct{ Items []struct{ Pack, Rule string } }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/rulepacks/evaluate", nil), &f)
	found := false
	for _, x := range f.Items {
		if x.Pack == "eu-fmd" && x.Rule == "product-code-gtin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rule pack findings: %+v", f.Items)
	}
	var packs struct{ Items []struct{ ID string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/rulepacks", nil), &packs)
	if len(packs.Items) < 5 {
		t.Fatalf("rule packs: %d", len(packs.Items))
	}
}

func findConnectors(e *testkit.Env) *connectors.Module {
	for _, m := range e.App.Modules {
		if c, ok := m.(*connectors.Module); ok {
			return c
		}
	}
	e.T.Fatal("connectors module missing")
	return nil
}
