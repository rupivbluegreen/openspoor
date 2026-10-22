package verify_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

func TestScanAndVerify(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Amoxicillin 500mg"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 3}, "Idempotency-Key", "k"), &a)
	u0, _ := gs1.SGTINURN(gtin, a.Serials[0], 7)
	u1, _ := gs1.SGTINURN(gtin, a.Serials[1], 7)
	t0 := time.Now().Add(-time.Hour).UTC()
	comm := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: t0, EventTimeZoneOffset: "+00:00",
		EPCList: []string{u0, u1}, ILMD: map[string]any{"lotNumber": "A7", "itemExpirationDate": "2030-01-31"}}
	dispense := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "dispensing", Disposition: "dispensed", EventTime: t0.Add(time.Minute), EventTimeZoneOffset: "+00:00", EPCList: []string{u1}}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{comm, dispense}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()

	scan := func(code string) (r struct {
		Verified bool
		Status   string
	}) {
		e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": code}), &r)
		return r
	}
	es := gs1.ElementString(gs1.Pack{GTIN: gtin, Serial: a.Serials[0], Lot: "A7", Expiry: "300131"}.AIs())
	if r := scan("]d2" + es); !r.Verified {
		t.Fatalf("genuine pack: %+v", r)
	}
	if r := scan(gs1.DigitalLink("https://brand.example", gs1.Pack{GTIN: gtin, Serial: a.Serials[0]})); !r.Verified {
		t.Fatalf("digital link: %+v", r)
	}
	if r := scan(gs1.HRI(gs1.Pack{GTIN: gtin, Serial: a.Serials[0], Lot: "FAKE"}.AIs())); r.Status != "suspect" {
		t.Fatalf("wrong lot: %+v", r)
	}
	if r := scan(gs1.HRI(gs1.Pack{GTIN: gtin, Serial: a.Serials[1]}.AIs())); r.Status != "decommissioned" {
		t.Fatalf("dispensed: %+v", r)
	}
	if r := scan(gs1.HRI(gs1.Pack{GTIN: gtin, Serial: "NEVERISSUED1"}.AIs())); r.Status != "not_found" {
		t.Fatalf("never issued: %+v", r)
	}
	if r := scan("(01)00614141000012(21)X"); r.Status != "unknown_product" {
		t.Fatalf("unknown product: %+v", r)
	}
	var alerts struct{ Items []struct{ Kind string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=verify.", nil), &alerts)
	if len(alerts.Items) != 3 {
		t.Fatalf("suspect alerts: %+v", alerts.Items)
	}
}

func TestPlayground(t *testing.T) {
	e := testkit.New(t)
	b, err := os.ReadFile("../../../testdata/epcis/epcis12-object-aggregation.xml")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Valid      bool
		Format     string
		EventCount int
	}
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/playground/validate", map[string]any{"document": string(b)}), &v)
	if !v.Valid || v.Format != "xml" || v.EventCount != 3 {
		t.Fatalf("validate: %+v", v)
	}
	var c struct{ Document string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/playground/convert", map[string]any{"document": string(b), "to": "json"}), &c)
	if !strings.Contains(c.Document, `"bizStep": "commissioning"`) {
		t.Fatalf("convert: %.300s", c.Document)
	}
	var back struct{ Document string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/playground/convert", map[string]any{"document": c.Document, "to": "xml"}), &back)
	if !strings.Contains(back.Document, "urn:epcglobal:cbv:bizstep:packing") {
		t.Fatalf("to xml: %.300s", back.Document)
	}
	var g struct {
		Nodes    []struct{ ID, Kind string }
		Edges    []struct{ Kind string }
		Timeline []any
	}
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/playground/visualize", map[string]any{"document": string(b)}), &g)
	contains := 0
	for _, ed := range g.Edges {
		if ed.Kind == "contains" {
			contains++
		}
	}
	if contains != 2 || len(g.Timeline) != 3 {
		t.Fatalf("visualize: %d contains edges, %d timeline", contains, len(g.Timeline))
	}
	var bad struct {
		Valid  bool
		Errors []any
	}
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/playground/validate", map[string]any{"document": "<not-xml"}), &bad)
	if bad.Valid || len(bad.Errors) == 0 {
		t.Fatalf("invalid doc: %+v", bad)
	}
}
