package food_test

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/food"
	"github.com/openspoor/openspoor/internal/testkit"
)

// foodUser creates a user with roles in the greenfarm tenant and returns its token.
func foodUser(t *testing.T, e *testkit.Env, admin, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Call(admin, "POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "greenfarm", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

func readCSV(t *testing.T, r testkit.Response) [][]string {
	t.Helper()
	if r.Status != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %s", r.Status, r.Body)
	}
	rows, err := csv.NewReader(strings.NewReader(string(r.Body))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestFoodLotAndCTEValidation(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "v@green.test", "Viewer")
	serial := foodUser(t, e, tok, "s@green.test", "Serialization")
	do := func(status int, method, path string, body any, out any) testkit.Response {
		t.Helper()
		r := e.Call(tok, method, path, body)
		e.Must(status, r, out)
		return r
	}

	// Roles: Viewer reads only; a role outside the list cannot even read.
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/food/lots", nil), nil)
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/food/fsma204/export", nil), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/food/lots", map[string]any{"tlc": "X", "description": "x"}), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/food/recalls", map[string]any{"title": "x"}), nil)
	e.Must(http.StatusForbidden, e.Call(serial, "GET", "/api/v1/food/lots", nil), nil)

	// Lot validation (problem JSON).
	for name, body := range map[string]map[string]any{
		"no tlc":        {"description": "x"},
		"long tlc":      {"tlc": strings.Repeat("A", 65), "description": "x"},
		"negative qty":  {"tlc": "A1", "description": "x", "quantity": -1},
		"bad origin":    {"tlc": "A1", "description": "x", "origin": "grown"},
		"bad harvest":   {"tlc": "A1", "description": "x", "harvestDate": "1/6/2026"},
		"bad pack date": {"tlc": "A1", "description": "x", "packDate": "2026-13-01"},
		"no desc":       {"tlc": "A1"},
		"bad gln":       {"tlc": "A1", "description": "x", "locationGln": "1234567890123"},
		"unknown prod":  {"tlc": "A1", "productId": "00000000-0000-0000-0000-000000000001"},
	} {
		r := e.Call(tok, "POST", "/api/v1/food/lots", body)
		if r.Status != http.StatusBadRequest || !strings.Contains(r.Header.Get("Content-Type"), "problem+json") {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
	}

	// Lot from a product (GTIN and description inherited) and from a site (GLN inherited).
	var p struct{ ID string }
	do(http.StatusCreated, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Baby spinach 200g", "vertical": "food"}, &p)
	var orgs struct{ Items []struct{ ID string } }
	do(http.StatusOK, "GET", "/api/v1/orgs", nil, &orgs)
	orgID := ""
	if len(orgs.Items) > 0 {
		orgID = orgs.Items[0].ID
	} else {
		var o struct{ ID string }
		do(http.StatusCreated, "POST", "/api/v1/orgs", map[string]any{"name": "Green Farm", "kind": "grower"}, &o)
		orgID = o.ID
	}
	var site struct{ ID string }
	do(http.StatusCreated, "POST", "/api/v1/sites", map[string]any{"orgId": orgID, "gln": processorGLN, "name": "Packhouse"}, &site)
	var lot food.Lot
	do(http.StatusCreated, "POST", "/api/v1/food/lots", map[string]any{"tlc": " SPN-1 ", "productId": p.ID, "quantity": 200, "siteId": site.ID, "packDate": "2026-06-02",
		"kdes": map[string]any{"variety": "Space"}}, &lot)
	if lot.TLC != "SPN-1" || lot.GTIN == nil || *lot.GTIN != "09506000134352" || lot.Description != "Baby spinach 200g" || lot.LocationGLN == nil || *lot.LocationGLN != processorGLN ||
		lot.TLCSource != processorGLN || lot.Origin != "initially_packed" || lot.UOM != "EA" || lot.PackDate == nil {
		t.Fatalf("lot: %+v", lot)
	}
	do(http.StatusConflict, "POST", "/api/v1/food/lots", map[string]any{"tlc": "SPN-1", "description": "dup"}, nil)
	do(http.StatusCreated, "POST", "/api/v1/food/lots", map[string]any{"tlc": "FISH-1", "description": "Cod", "origin": "first_land_based_receiving", "quantity": 50, "uom": "KGM", "locationGln": farmGLN}, nil)
	do(http.StatusCreated, "POST", "/api/v1/food/lots", map[string]any{"tlc": "REC-0", "description": "Bought in", "origin": "received"}, nil)

	// CTE validation.
	do(http.StatusBadRequest, "POST", "/api/v1/food/cte", map[string]any{"type": "transformation", "tlc": "SPN-1"}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/cte", map[string]any{"type": "cooling", "tlc": "NOPE"}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/cte", map[string]any{"type": "receiving", "tlc": "IN-1", "refDocNumber": "BOL-9"}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "SPN-1", "shipTo": retailA}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/cte", map[string]any{"type": "teleporting", "tlc": "SPN-1"}, nil)

	// Receiving an unknown lot creates it from the KDEs.
	var c food.CTE
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "receiving", "tlc": "IN-1", "quantity": 30, "uom": "KGM", "locationGln": processorGLN, "shipFrom": farmGLN,
		"refDocType": "BOL", "refDocNumber": "BOL-9", "kdes": map[string]any{"productDescription": "Kale"}}, &c)
	if c.Type != "receiving" || c.ShipFrom != farmGLN {
		t.Fatalf("receiving: %+v", c)
	}
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "receiving", "tlc": "IN-2", "quantity": 5, "shipFrom": farmGLN, "refDocNumber": "BOL-10"}, nil)
	var lots struct{ Items []food.Lot }
	do(http.StatusOK, "GET", "/api/v1/food/lots", nil, &lots)
	desc := map[string]food.Lot{}
	for _, l := range lots.Items {
		desc[l.TLC] = l
	}
	if desc["IN-1"].Description != "Kale" || desc["IN-1"].Origin != "received" || desc["IN-1"].TLCSource != farmGLN || desc["IN-2"].Description != "Received lot IN-2" {
		t.Fatalf("received lots: %+v / %+v", desc["IN-1"], desc["IN-2"])
	}

	// Shipping the full quantity (in two parts) marks the lot shipped.
	t0 := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	ship := func(qty float64, at time.Time, doc string) {
		do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "SPN-1", "quantity": qty, "locationGln": processorGLN, "shipToName": "Corner Shop",
			"refDocType": "BOL", "refDocNumber": doc, "eventTime": at}, nil)
	}
	ship(150, t0, "S-1")
	do(http.StatusOK, "GET", "/api/v1/food/lots?status=shipped", nil, &lots)
	if len(lots.Items) != 0 {
		t.Fatalf("partially shipped lot marked shipped: %+v", lots.Items)
	}
	ship(50, t0.Add(time.Hour), "S-2")
	do(http.StatusOK, "GET", "/api/v1/food/lots?status=shipped", nil, &lots)
	if len(lots.Items) != 1 || lots.Items[0].TLC != "SPN-1" {
		t.Fatalf("shipped: %+v", lots.Items)
	}

	// Transformation validation; a shipped lot can still be an input.
	do(http.StatusBadRequest, "POST", "/api/v1/food/transformations", map[string]any{"output": map[string]any{"tlc": "MIX"}}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/transformations", map[string]any{"inputs": []map[string]any{{"tlc": "SPN-1", "quantity": 1}}}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/transformations", map[string]any{"inputs": []map[string]any{{"tlc": "GHOST", "quantity": 1}}, "output": map[string]any{"tlc": "MIX", "description": "m"}}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/transformations", map[string]any{"inputs": []map[string]any{{"tlc": "IN-1", "quantity": 31}}, "output": map[string]any{"tlc": "MIX", "description": "m"}}, nil)
	do(http.StatusConflict, "POST", "/api/v1/food/transformations", map[string]any{"inputs": []map[string]any{{"tlc": "IN-1", "quantity": 1}}, "output": map[string]any{"tlc": "SPN-1", "description": "m"}}, nil)
	do(http.StatusCreated, "POST", "/api/v1/food/transformations", map[string]any{"inputs": []map[string]any{{"tlc": "IN-1", "quantity": 10}, {"tlc": "SPN-1", "quantity": 200}},
		"output": map[string]any{"tlc": "MIX-1", "productId": p.ID, "quantity": 20}, "eventTime": t0.Add(2 * time.Hour), "kdes": map[string]any{"line": "L2"}}, nil)
	do(http.StatusOK, "GET", "/api/v1/food/lots?status=consumed", nil, &lots)
	if len(lots.Items) != 1 || lots.Items[0].TLC != "SPN-1" {
		t.Fatalf("consumed: %+v", lots.Items)
	}
	do(http.StatusOK, "GET", "/api/v1/food/lots?status=active", nil, &lots)
	for _, l := range lots.Items {
		if l.TLC == "IN-1" && l.Quantity != 20 {
			t.Fatalf("partial consumption: %+v", l)
		}
	}

	// CTE listing filters.
	var ctes struct {
		Items []food.CTE
		Total int
	}
	do(http.StatusOK, "GET", "/api/v1/food/cte?tlc=SPN-1&type=shipping", nil, &ctes)
	if ctes.Total != 2 || ctes.Items[0].RefDocNumber != "S-2" {
		t.Fatalf("cte filter: %+v", ctes.Items)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/food/cte", nil), &ctes)
	if ctes.Total != 7 { // initial packing, first receiving, 2 receivings, 2 shipments, transformation
		var types []string
		for _, x := range ctes.Items {
			types = append(types, x.Type+":"+x.TLC)
		}
		t.Fatalf("all ctes (%d): %v", ctes.Total, types)
	}

	// Export window and lineage scope.
	do(http.StatusBadRequest, "GET", "/api/v1/food/fsma204/export?from=yesterday", nil, nil)
	do(http.StatusBadRequest, "GET", "/api/v1/food/fsma204/export?to=2026/06/03", nil, nil)
	rows := readCSV(t, e.Call(tok, "GET", "/api/v1/food/fsma204/export?from=2026-06-03&to=2026-06-03", nil))
	if len(rows) != 4 { // header + 2 shipments + transformation on June 3rd
		t.Fatalf("window rows: %v", rows)
	}
	if rows[3][5] != "transformation" || rows[3][15] != "IN-1;SPN-1" || !strings.Contains(rows[3][18], `"line":"L2"`) || rows[3][3] != "20" {
		t.Fatalf("transformation row: %v", rows[3])
	}
	if rows[1][2] != "Baby spinach 200g" || rows[1][12] != "Corner Shop" || rows[1][17] != "2026-06-02" {
		t.Fatalf("shipping row: %v", rows[1])
	}
	rows = readCSV(t, e.Call(tok, "GET", "/api/v1/food/fsma204/export?tlc=FISH-1", nil))
	if len(rows) != 2 || rows[1][5] != "first_land_based_receiving" || rows[1][4] != "KGM" {
		t.Fatalf("fish rows: %v", rows)
	}
	var aud struct {
		Items []struct{ Action, EntityID string }
	}
	do(http.StatusOK, "GET", "/api/v1/audit?action=food.fsma204.export", nil, &aud)
	if len(aud.Items) != 3 {
		t.Fatalf("export audit: %+v", aud.Items)
	}

	// Trace of an unknown lot is just itself, with no events.
	var tr struct {
		Sources, Derived []string
		Events           []food.CTE
	}
	do(http.StatusOK, "GET", "/api/v1/food/lots/NOPE/trace", nil, &tr)
	if len(tr.Sources) != 1 || len(tr.Events) != 0 {
		t.Fatalf("unknown trace: %+v", tr)
	}
	do(http.StatusOK, "GET", "/api/v1/food/lots/IN-1/trace", nil, &tr)
	if strings.Join(tr.Derived, ",") != "IN-1,MIX-1" {
		t.Fatalf("forward trace: %+v", tr)
	}
}

func TestPlotsAndEUDR(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	do := func(status int, method, path string, body any, out any) {
		t.Helper()
		e.Must(status, e.Call(tok, method, path, body), out)
	}
	square := [][]float64{{10, 50}, {10.001, 50}, {10.001, 50.001}, {10, 50.001}, {10, 50}}
	bad := map[string]any{
		"missing name":    map[string]any{"country": "DE", "commodity": "wood", "geojson": map[string]any{"type": "Point", "coordinates": []float64{1, 1}}},
		"bad country":     map[string]any{"name": "x", "country": "DEU", "commodity": "wood", "geojson": map[string]any{"type": "Point", "coordinates": []float64{1, 1}}},
		"not geojson":     map[string]any{"name": "x", "country": "DE", "commodity": "wood", "geojson": "nope"},
		"line":            map[string]any{"name": "x", "country": "DE", "commodity": "wood", "geojson": map[string]any{"type": "LineString", "coordinates": [][]float64{{1, 1}, {2, 2}}}},
		"point range":     map[string]any{"name": "x", "country": "DE", "commodity": "wood", "geojson": map[string]any{"type": "Point", "coordinates": []float64{200, 1}}},
		"open ring":       map[string]any{"name": "x", "country": "DE", "commodity": "wood", "geojson": map[string]any{"type": "Polygon", "coordinates": [][][]float64{square[:4]}}},
		"short ring":      map[string]any{"name": "x", "country": "DE", "commodity": "wood", "geojson": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{1, 1}, {2, 2}, {1, 1}}}}},
		"ring out of box": map[string]any{"name": "x", "country": "DE", "commodity": "wood", "geojson": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{1, 1}, {1, 95}, {2, 2}, {1, 1}}}}},
	}
	for name, body := range bad {
		r := e.Call(tok, "POST", "/api/v1/food/plots", body)
		if r.Status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	// A Feature-wrapped polygon, and a large cattle point (allowed without polygon).
	var forest, ranch food.Plot
	do(http.StatusCreated, "POST", "/api/v1/food/plots", map[string]any{"name": "Forest A", "producerName": "Waldbauer", "country": "de", "commodity": "Wood",
		"geojson": map[string]any{"type": "Feature", "properties": map[string]any{}, "geometry": map[string]any{"type": "Polygon", "coordinates": [][][]float64{square}}}}, &forest)
	if forest.Country != "DE" || forest.Commodity != "wood" || forest.AreaHa < 0.6 || forest.AreaHa > 0.9 {
		t.Fatalf("forest: %+v", forest)
	}
	do(http.StatusCreated, "POST", "/api/v1/food/plots", map[string]any{"name": "Ranch", "country": "BR", "commodity": "cattle",
		"geojson": map[string]any{"type": "Point", "coordinates": []float64{-50, -10}}, "areaHa": 500}, &ranch)
	if ranch.AreaHa != 500 {
		t.Fatalf("ranch: %+v", ranch)
	}
	var plots struct{ Items []food.Plot }
	do(http.StatusOK, "GET", "/api/v1/food/plots", nil, &plots)
	if len(plots.Items) != 2 || plots.Items[0].Name != "Forest A" {
		t.Fatalf("plots: %+v", plots.Items)
	}

	// DDS validation.
	base := func(over map[string]any) map[string]any {
		b := map[string]any{"commodity": "wood", "hsCode": "4403", "description": "Logs", "quantity": 10, "countryOfProduction": "DE", "plotIds": []string{forest.ID.String()}}
		for k, v := range over {
			b[k] = v
		}
		return b
	}
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"commodity": "bananas"}), nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"plotIds": []string{}}), nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"hsCode": ""}), nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"quantity": 0}), nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"plotIds": []string{"00000000-0000-0000-0000-000000000001"}}), nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"plotIds": []string{ranch.ID.String()}}), nil)

	var d food.DDS
	do(http.StatusCreated, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"reference": "MY-REF-1", "uom": "MTQ"}), &d)
	geo, _ := json.Marshal(d.Payload["geolocation"])
	if d.Reference != "MY-REF-1" || d.OperatorRole != "operator" || d.UOM != "MTQ" || !strings.Contains(string(geo), `"type":"Polygon"`) || strings.Contains(string(geo), `"geometry":{"geometry"`) {
		t.Fatalf("dds: %+v %s", d, geo)
	}
	var auto food.DDS
	do(http.StatusCreated, "POST", "/api/v1/food/eudr/dds", base(map[string]any{"commodity": "CATTLE", "hsCode": "0102", "plotIds": []string{ranch.ID.String()}, "operatorRole": "operator"}), &auto)
	if !strings.HasPrefix(auto.Reference, "DDS-") || auto.UOM != "KGM" || auto.Payload["totalAreaHa"] != float64(500) {
		t.Fatalf("auto ref: %+v", auto)
	}
	var list struct{ Items []food.DDS }
	do(http.StatusOK, "GET", "/api/v1/food/eudr/dds", nil, &list)
	if len(list.Items) != 2 {
		t.Fatalf("dds list: %d", len(list.Items))
	}
	var got food.DDS
	do(http.StatusOK, "GET", "/api/v1/food/eudr/dds/"+d.ID.String(), nil, &got)
	if got.Reference != "MY-REF-1" {
		t.Fatalf("get dds: %+v", got)
	}
	do(http.StatusNotFound, "GET", "/api/v1/food/eudr/dds/00000000-0000-0000-0000-000000000001", nil, nil)
	do(http.StatusBadRequest, "GET", "/api/v1/food/eudr/dds/nope", nil, nil)
	var sub struct {
		DDS  food.DDS `json:"dds"`
		Note string
	}
	do(http.StatusOK, "POST", "/api/v1/food/eudr/dds/"+d.ID.String()+"/submit", nil, &sub)
	if sub.DDS.Status != "submitted_stub" || !strings.HasPrefix(sub.DDS.SubmissionRef, "LOCAL-") {
		t.Fatalf("submit: %+v", sub)
	}
	do(http.StatusConflict, "POST", "/api/v1/food/eudr/dds/"+d.ID.String()+"/submit", nil, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/eudr/dds/nope/submit", nil, nil)

	// Another tenant with the food vertical sees none of these.
	_, other := e.NewTenant("other", "a@other.test")
	e.Must(http.StatusForbidden, e.Call(other, "GET", "/api/v1/food/plots", nil), nil)
}

func TestRecallLifecycleAndPartnerNotification(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	do := func(status int, method, path string, body any, out any) {
		t.Helper()
		e.Must(status, e.Call(tok, method, path, body), out)
	}
	// A retailer on this node: its directory GLN is retailA and it accepted our invitation.
	_, retTok := e.NewTenant("freshmart", "admin@fresh.test")
	e.Must(http.StatusOK, e.Call(retTok, "PUT", "/api/v1/partners/listing", map[string]any{"name": "FreshMart", "gln": retailA, "kind": "retailer", "country": "NL", "published": true}), nil)
	var inv struct{ Code string }
	do(http.StatusCreated, "POST", "/api/v1/partners/invitations", map[string]any{"email": "admin@fresh.test", "name": "FreshMart"}, &inv)
	e.Must(http.StatusCreated, e.Call(retTok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": inv.Code}), nil)

	do(http.StatusCreated, "POST", "/api/v1/food/lots", map[string]any{"tlc": "TOM-1", "description": "Tomatoes", "quantity": 100, "locationGln": farmGLN, "origin": "harvested"}, nil)
	for i, to := range []struct{ gln, name string }{{retailA, ""}, {retailB, "Market B"}} {
		do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "TOM-1", "quantity": 10 * (i + 1), "locationGln": farmGLN,
			"shipTo": to.gln, "shipToName": to.name, "refDocNumber": "BOL-" + string(rune('A'+i))}, nil)
	}

	do(http.StatusBadRequest, "POST", "/api/v1/food/recalls", map[string]any{"title": "x", "reason": "y"}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/recalls", map[string]any{"title": "x", "tlcs": []string{"TOM-1"}}, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/recalls", map[string]any{"title": "x", "reason": "y", "classification": "IV", "tlcs": []string{"TOM-1"}}, nil)

	var rc food.Recall
	do(http.StatusCreated, "POST", "/api/v1/food/recalls", map[string]any{"title": "Salmonella", "reason": "lab result", "tlcs": []string{"TOM-1"}}, &rc)
	if rc.Classification != "II" || rc.Status != "notifying" || len(rc.AffectedCustomers) != 2 {
		t.Fatalf("recall: %+v", rc)
	}
	byGLN := map[string]food.Customer{}
	for _, c := range rc.AffectedCustomers {
		byGLN[c.GLN] = c
	}
	if !byGLN[retailA].Partner || byGLN[retailA].Name != retailA || byGLN[retailB].Partner || byGLN[retailB].Name != "Market B" || byGLN[retailB].Quantity != 20 {
		t.Fatalf("customers: %+v", rc.AffectedCustomers)
	}
	var full food.Recall
	do(http.StatusOK, "GET", "/api/v1/food/recalls/"+rc.ID.String(), nil, &full)
	channels := map[string]string{}
	for _, n := range full.Notifications {
		channels[n.CustomerGLN] = n.Channel
	}
	if channels[retailA] != "network" || channels[retailB] != "email" {
		t.Fatalf("notification channels: %+v", full.Notifications)
	}
	// The partner tenant receives the recall in its alert inbox.
	var al struct {
		Items []struct {
			Title  string
			Detail map[string]any
		}
	}
	e.Must(http.StatusOK, e.Call(retTok, "GET", "/api/v1/alerts?kind=recall.received", nil), &al)
	if len(al.Items) != 1 || !strings.Contains(al.Items[0].Title, "GREENFARM") && !strings.Contains(al.Items[0].Title, "Green Farm") || al.Items[0].Detail["quantity"] != float64(10) {
		t.Fatalf("partner alert: %+v", al.Items)
	}
	// ...and only its own share; it cannot read our recall.
	e.Must(http.StatusForbidden, e.Call(retTok, "GET", "/api/v1/food/recalls/"+rc.ID.String(), nil), nil)

	var list struct{ Items []food.Recall }
	do(http.StatusOK, "GET", "/api/v1/food/recalls", nil, &list)
	if len(list.Items) != 1 || len(list.Items[0].AffectedLots) != 1 {
		t.Fatalf("recall list: %+v", list.Items)
	}
	do(http.StatusBadRequest, "GET", "/api/v1/food/recalls/nope", nil, nil)
	do(http.StatusNotFound, "GET", "/api/v1/food/recalls/00000000-0000-0000-0000-000000000001", nil, nil)

	// Closing resolves the open recall alert and is audited with the reason.
	var closed food.Recall
	do(http.StatusOK, "POST", "/api/v1/food/recalls/"+rc.ID.String()+"/close", map[string]any{"reason": "all stock retrieved"}, &closed)
	if closed.Status != "closed" || closed.ClosedAt == nil || len(closed.AffectedCustomers) != 2 {
		t.Fatalf("closed: %+v", closed)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/alerts?kind=recall.open", nil), &al)
	if len(al.Items) != 0 {
		t.Fatalf("recall alert still open: %+v", al.Items)
	}
	var aud struct {
		Items []struct{ Action, Reason string }
	}
	do(http.StatusOK, "GET", "/api/v1/audit?action=recall.", nil, &aud)
	if len(aud.Items) != 2 || aud.Items[0].Action != "recall.close" || aud.Items[0].Reason != "all stock retrieved" {
		t.Fatalf("recall audit: %+v", aud.Items)
	}
	do(http.StatusNotFound, "POST", "/api/v1/food/recalls/00000000-0000-0000-0000-000000000001/close", nil, nil)
	do(http.StatusBadRequest, "POST", "/api/v1/food/recalls/nope/close", nil, nil)

	// A pharma-only recall of an unknown batch affects nothing but is still recorded.
	do(http.StatusCreated, "POST", "/api/v1/food/recalls", map[string]any{"title": "Batch", "reason": "r", "classification": "III",
		"pharmaLots": []map[string]string{{"gtin": "9506000134352", "lot": "NONE"}}}, &rc)
	if rc.Affected["pharmaPacks"] != float64(0) || len(rc.AffectedLots) != 0 {
		t.Fatalf("pharma recall: %+v", rc)
	}
}

func TestParseGeometry(t *testing.T) {
	poly := `{"type":"Polygon","coordinates":[[[0,0],[0.01,0],[0.01,0.01],[0,0.01],[0,0]]]}`
	kind, area, err := food.ParseGeometry(json.RawMessage(poly))
	if err != nil || kind != "Polygon" || area < 120 || area > 125 {
		t.Fatalf("polygon: %s %.2f %v", kind, area, err)
	}
	kind, _, err = food.ParseGeometry(json.RawMessage(`{"type":"Feature","geometry":` + poly + `}`))
	if err != nil || kind != "Polygon" {
		t.Fatalf("feature: %s %v", kind, err)
	}
	for _, bad := range []string{`[]`, `{"type":"Point","coordinates":[1]}`, `{"type":"Point","coordinates":[0,91]}`, `{"type":"Polygon","coordinates":"x"}`,
		`{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0.5]]]}`, `{"type":"Polygon","coordinates":[[[0,0],[1],[1,1],[0,0]]]}`, `{"type":"MultiPolygon","coordinates":[]}`,
		`{"type":"Feature"}`} {
		if _, _, err := food.ParseGeometry(json.RawMessage(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if k, a, err := food.ParseGeometry(json.RawMessage(`{"type":"Point","coordinates":[4.9,52.4]}`)); err != nil || k != "Point" || a != 0 {
		t.Fatalf("point: %s %v %v", k, a, err)
	}
}
