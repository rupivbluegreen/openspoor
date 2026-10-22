package food_test

import (
	"context"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/food"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

func foodTenant(t *testing.T, e *testkit.Env) string {
	_, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "greenfarm", Name: "Green Farm", AdminEmail: "a@green.test",
		AdminPassword: testkit.Password, Verticals: []string{"food", "pharma"}})
	if err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "greenfarm", "email": "a@green.test", "password": testkit.Password}), &tok)
	return tok.Token
}

var (
	farmGLN      = gs1.WithCheckDigit("950600001000")
	processorGLN = gs1.WithCheckDigit("950600002000")
	retailA      = gs1.WithCheckDigit("950600003000")
	retailB      = gs1.WithCheckDigit("950600004000")
)

func TestFoodTraceabilityRecallAndEUDR(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusForbidden, e.Do("GET", "/api/v1/food/lots", nil), nil) // acme has no food vertical
	tok := foodTenant(t, e)
	do := func(status int, method, path string, body any, out any) {
		t.Helper()
		e.Must(status, e.Call(tok, method, path, body), out)
	}

	// Plots: polygon area is computed; a >4 ha point is rejected.
	poly := map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{-74.0, 4.6}, {-73.99, 4.6}, {-73.99, 4.61}, {-74.0, 4.61}, {-74.0, 4.6}}}}
	var plot struct {
		ID     string
		AreaHa float64
	}
	do(http.StatusCreated, "POST", "/api/v1/food/plots", map[string]any{"name": "Finca Norte", "country": "CO", "commodity": "coffee", "geojson": poly}, &plot)
	if plot.AreaHa < 120 || plot.AreaHa > 125 { // ~1.11 km × 1.11 km
		t.Fatalf("area %.2f ha", plot.AreaHa)
	}
	do(http.StatusBadRequest, "POST", "/api/v1/food/plots", map[string]any{"name": "Big point", "country": "CO", "commodity": "coffee",
		"geojson": map[string]any{"type": "Point", "coordinates": []float64{-74, 4.6}}, "areaHa": 10}, nil)

	// Harvest two lettuce lots, cool, ship one to a processor.
	t0 := time.Date(2026, 6, 1, 6, 0, 0, 0, time.UTC)
	for _, tlc := range []string{"LET-001", "LET-002"} {
		do(http.StatusCreated, "POST", "/api/v1/food/lots", map[string]any{"tlc": tlc, "description": "Romaine lettuce", "quantity": 500, "uom": "KGM",
			"locationGln": farmGLN, "origin": "harvested", "harvestDate": "2026-06-01", "plotIds": []string{plot.ID}}, nil)
	}
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "cooling", "tlc": "LET-001", "quantity": 500, "uom": "KGM", "locationGln": farmGLN, "eventTime": t0.Add(time.Hour)}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "LET-001", "quantity": 100, "shipTo": processorGLN}, nil)
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "LET-001", "quantity": 100, "uom": "KGM", "locationGln": farmGLN,
		"shipTo": processorGLN, "shipToName": "Fresh Cuts BV", "refDocType": "BOL", "refDocNumber": "BOL-1", "eventTime": t0.Add(2 * time.Hour)}, nil)

	// Transformation: both lettuce lots into a salad lot, shipped to two retailers.
	do(http.StatusCreated, "POST", "/api/v1/food/transformations", map[string]any{
		"inputs": []map[string]any{{"tlc": "LET-001", "quantity": 400}, {"tlc": "LET-002", "quantity": 500}},
		"output": map[string]any{"tlc": "SALAD-77", "description": "Caesar salad kit", "quantity": 3000, "uom": "EA", "locationGln": farmGLN}, "eventTime": t0.Add(3 * time.Hour)}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/transformations", map[string]any{"inputs": []map[string]any{{"tlc": "LET-002", "quantity": 1}},
		"output": map[string]any{"tlc": "X", "description": "x", "quantity": 1}}, nil) // LET-002 fully consumed
	for i, r := range []struct{ gln, name string }{{retailA, "Retail A"}, {retailB, "Retail B"}} {
		do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "SALAD-77", "quantity": 1000, "locationGln": farmGLN,
			"shipTo": r.gln, "shipToName": r.name, "refDocType": "ASN", "refDocNumber": "ASN-" + string(rune('1'+i)), "eventTime": t0.Add(time.Duration(4+i) * time.Hour)}, nil)
	}

	var tr struct{ Sources, Derived []string }
	do(http.StatusOK, "GET", "/api/v1/food/lots/SALAD-77/trace", nil, &tr)
	if strings.Join(tr.Sources, ",") != "LET-001,LET-002,SALAD-77" {
		t.Fatalf("lineage: %+v", tr)
	}

	// FSMA 204 sortable spreadsheet for the salad lot includes its source lots.
	r := e.Call(tok, "GET", "/api/v1/food/fsma204/export?tlc=SALAD-77", nil)
	rows, err := csv.NewReader(strings.NewReader(string(r.Body))).ReadAll()
	if err != nil || r.Status != 200 {
		t.Fatalf("csv: %v %s", err, r.Body)
	}
	if rows[0][0] != "Traceability Lot Code" || len(rows) != 8 { // header + 2 harvest + cooling + ship + transform + 2 ships
		t.Fatalf("fsma rows: %d\n%s", len(rows), r.Body)
	}
	for i := 2; i < len(rows); i++ {
		if rows[i][6]+rows[i][7] < rows[i-1][6]+rows[i-1][7] {
			t.Fatal("spreadsheet not sorted by time")
		}
	}
	if !strings.Contains(string(r.Body), "LET-001;LET-002") {
		t.Fatal("transformation inputs missing")
	}

	// CTEs are mirrored into the EPCIS repository.
	e.Drain()
	var st struct{ Events int }
	do(http.StatusOK, "GET", "/api/v1/trace/stats", nil, &st)
	if st.Events != 7 {
		t.Fatalf("mirrored events: %d", st.Events)
	}

	// Recall LET-001: forward lineage reaches the salad and all three customers.
	var rc food.Recall
	do(http.StatusCreated, "POST", "/api/v1/food/recalls", map[string]any{"title": "Listeria in romaine", "reason": "positive environmental swab", "classification": "I", "tlcs": []string{"LET-001"}}, &rc)
	if strings.Join(rc.AffectedLots, ",") != "LET-001,SALAD-77" || len(rc.AffectedCustomers) != 3 {
		t.Fatalf("recall scope: lots=%v customers=%+v", rc.AffectedLots, rc.AffectedCustomers)
	}
	var lots struct{ Items []struct{ TLC, Status string } }
	do(http.StatusOK, "GET", "/api/v1/food/lots?status=recalled", nil, &lots)
	if len(lots.Items) != 2 {
		t.Fatalf("recalled lots: %+v", lots.Items)
	}
	var full food.Recall
	do(http.StatusOK, "GET", "/api/v1/food/recalls/"+rc.ID.String(), nil, &full)
	if len(full.Notifications) != 3 {
		t.Fatalf("notifications: %+v", full.Notifications)
	}

	// Pharma batch recall marks packs recalled (VRS and public verify then refuse them).
	var p struct{ ID string }
	do(http.StatusCreated, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Vitamin D"}, &p)
	var pool struct{ ID string }
	do(http.StatusCreated, "POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}, &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 2}, "Idempotency-Key", "k"), &a)
	u, _ := gs1.SGTINURN("09506000134352", a.Serials[0], 7)
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", EventTime: t0, EventTimeZoneOffset: "+00:00",
		EPCList: []string{u}, ILMD: map[string]any{"lotNumber": "VD-9", "itemExpirationDate": "2030-01-31"}}}))
	do(http.StatusAccepted, "POST", "/api/v1/epcis/capture", b, nil)
	e.Drain()
	do(http.StatusCreated, "POST", "/api/v1/food/recalls", map[string]any{"title": "Vitamin D potency", "reason": "OOS assay", "pharmaLots": []map[string]string{{"gtin": "09506000134352", "lot": "VD-9"}}}, nil)
	var scan struct{ Status string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": gs1.HRI(gs1.Pack{GTIN: "09506000134352", Serial: a.Serials[0]}.AIs())}), &scan)
	if scan.Status != "recalled" {
		t.Fatalf("verify after recall: %+v", scan)
	}

	// EUDR due diligence statement from registered plots.
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds", map[string]any{"commodity": "cocoa", "hsCode": "1801", "description": "beans", "quantity": 1000, "countryOfProduction": "CO", "plotIds": []string{plot.ID}}, nil)
	var dds struct {
		ID, Reference, Status string
		Payload               map[string]any
	}
	do(http.StatusCreated, "POST", "/api/v1/food/eudr/dds", map[string]any{"commodity": "coffee", "hsCode": "0901", "description": "Green coffee beans", "quantity": 18000,
		"countryOfProduction": "CO", "plotIds": []string{plot.ID}}, &dds)
	if dds.Status != "ready" || dds.Payload["geolocation"] == nil {
		t.Fatalf("dds: %+v", dds)
	}
	do(http.StatusOK, "POST", "/api/v1/food/eudr/dds/"+dds.ID+"/submit", nil, nil)
}
