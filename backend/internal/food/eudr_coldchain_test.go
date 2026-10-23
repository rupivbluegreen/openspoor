package food_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/food"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

func poly(x, y, s float64) map[string]any {
	return map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{x, y}, {x + s, y}, {x + s, y + s}, {x, y + s}, {x, y}}}}
}

func TestEUDRReadiness(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "v@green.test", "Viewer")
	integ := foodUser(t, e, tok, "i@green.test", "Integration")
	ops := foodUser(t, e, tok, "o@green.test", "Ops")
	base := "/api/v1/food/eudr"

	// Settings: default operator; Viewer reads, cannot write; invalid role refused; audited.
	var st food.EUDRSettings
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/settings", nil), &st)
	if st.DDSRole != "operator" || len(st.Roles) != 2 || len(st.Notes) == 0 {
		t.Fatalf("settings: %+v", st)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", base+"/settings", map[string]any{"ddsRole": "downstream_trader"}), nil)
	e.Must(http.StatusForbidden, e.Call(integ, "PUT", base+"/settings", map[string]any{"ddsRole": "downstream_trader"}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "PUT", base+"/settings", map[string]any{"ddsRole": "king"}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "PUT", base+"/settings", map[string]any{"ddsRole": "operator", "bogus": 1}), nil)

	// Dates: defaults labelled unverified; a tenant can set and confirm, then reset.
	var dates struct{ Items []food.RefDate }
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/dates", nil), &dates)
	if len(dates.Items) != 2 || dates.Items[0].Date != "2026-12-30" || dates.Items[1].Date != "2027-06-30" || !dates.Items[0].IsDefault || dates.Items[0].Status != food.UnverifiedLabel ||
		!strings.Contains(dates.Items[0].Status, "unverified, check the Official Journal") {
		t.Fatalf("dates: %+v", dates.Items)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", base+"/dates/large_medium_operators", map[string]any{"date": "2026-12-30"}), nil)
	e.Must(http.StatusNotFound, e.Call(tok, "PUT", base+"/dates/nope", map[string]any{"date": "2026-12-30"}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "PUT", base+"/dates/large_medium_operators", map[string]any{"date": "30/12/2026"}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "PUT", base+"/dates/large_medium_operators", map[string]any{"date": "2026-12-30", "note": strings.Repeat("x", 501)}), nil)
	e.Must(http.StatusOK, e.Call(ops, "PUT", base+"/dates/large_medium_operators", map[string]any{"date": "2027-01-15", "note": "per counsel", "verified": true}), &dates)
	if dates.Items[0].Date != "2027-01-15" || !dates.Items[0].Verified || dates.Items[0].IsDefault || strings.Contains(dates.Items[0].Status, "unverified") || dates.Items[1].Status != food.UnverifiedLabel {
		t.Fatalf("dates after set: %+v", dates.Items)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "DELETE", base+"/dates/large_medium_operators", nil), nil)
	e.Must(http.StatusNotFound, e.Call(tok, "DELETE", base+"/dates/nope", nil), nil)
	e.Must(http.StatusNoContent, e.Call(ops, "DELETE", base+"/dates/large_medium_operators", nil), nil)
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/dates", nil), &dates)
	if dates.Items[0].Date != "2026-12-30" || !dates.Items[0].IsDefault {
		t.Fatalf("dates reset: %+v", dates.Items)
	}

	// Plots: a good polygon, an overlapping one, a self-intersecting bow-tie, a point above the size rule.
	mk := func(name string, geo any, extra map[string]any) string {
		t.Helper()
		body := map[string]any{"name": name, "country": "CO", "commodity": "coffee", "geojson": geo}
		for k, v := range extra {
			body[k] = v
		}
		var p struct{ ID string }
		e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/plots", body), &p)
		return p.ID
	}
	a := mk("A", poly(-75.600001, 4.500001, 0.001), map[string]any{"deforestationFree": true})
	b := mk("B", poly(-75.599501, 4.500501, 0.001), nil) // overlaps A
	far := mk("Far", poly(-70.600001, 4.500001, 0.001), map[string]any{"deforestationFree": true})
	bow := mk("Bow", map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{-60.000001, 1.000001}, {-59.999001, 1.001001}, {-59.999001, 1.000001}, {-60.000001, 1.001001}, {-60.000001, 1.000001}}}}, nil)
	pt := mk("Pin", map[string]any{"type": "Point", "coordinates": []float64{-75.5997, 4.5007}}, map[string]any{"areaHa": 1}) // inside A and B, 4 decimals

	var all struct {
		Items       []food.PlotReport
		Summary     map[string]int
		ReasonCodes []string
		Notes       []string
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/plots/validation", nil), &all)
	byName := map[string]food.PlotReport{}
	for _, r := range all.Items {
		byName[r.Name] = r
	}
	has := func(r food.PlotReport, code string) bool {
		for _, x := range r.Reasons {
			if x.Code == code {
				return true
			}
		}
		return false
	}
	if len(all.Items) != 5 || len(all.ReasonCodes) == 0 || len(all.Notes) == 0 || all.Summary["invalid"] != 1 {
		t.Fatalf("validation: %+v", all.Summary)
	}
	if r := byName["A"]; len(r.Overlaps) != 2 || !has(r, "overlap") || r.Status != "warnings" || r.GeometryType != "Polygon" {
		t.Errorf("A: %+v", r)
	}
	if r := byName["Far"]; r.Status != "valid" || len(r.Overlaps) != 0 || len(r.Reasons) != 0 {
		t.Errorf("Far: %+v", r)
	}
	if r := byName["Bow"]; r.Status != "invalid" || !has(r, "self_intersection") {
		t.Errorf("Bow: %+v", r)
	}
	if r := byName["Pin"]; !has(r, "low_precision") || !has(r, "overlap") {
		t.Errorf("Pin: %+v", r)
	}
	var one struct {
		Report food.PlotReport
		Notes  []string
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/plots/"+far+"/validation", nil), &one)
	if one.Report.Name != "Far" || one.Report.PlotID.String() != far {
		t.Fatalf("one: %+v", one)
	}
	e.Must(http.StatusNotFound, e.Call(viewer, "GET", base+"/plots/00000000-0000-0000-0000-000000000001/validation", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/plots/nope/validation", nil), nil)

	// Operator statement + checklist.
	dds := func(extra map[string]any) map[string]any {
		body := map[string]any{"commodity": "coffee", "hsCode": "0901", "description": "Green coffee", "quantity": 100, "countryOfProduction": "CO", "plotIds": []string{far}}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	var d food.DDS
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"operatorRole": "downstream_trader"})), nil) // does not match the tenant role
	e.Must(http.StatusCreated, e.Call(tok, "POST", base+"/dds", dds(nil)), &d)
	if d.Kind != "statement" || d.OperatorRole != "operator" || d.Status != "ready" {
		t.Fatalf("dds: %+v", d)
	}
	var cl food.Checklist
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/dds/"+d.ID.String()+"/checklist", nil), &cl)
	if !cl.Complete || len(cl.Items) < 10 || len(cl.Notes) == 0 {
		t.Fatalf("checklist: %+v", cl)
	}
	var bad food.DDS
	e.Must(http.StatusCreated, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"plotIds": []string{a, bow}})), &bad)
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/dds/"+bad.ID.String()+"/checklist", nil), &cl)
	statuses := map[string]string{}
	for _, it := range cl.Items {
		statuses[it.Key] = it.Status
	}
	if cl.Complete || statuses["plots_valid"] != "missing" || statuses["plots_no_overlap"] != "warning" || statuses["deforestation"] != "warning" {
		t.Fatalf("bad checklist: %+v", cl)
	}
	e.Must(http.StatusNotFound, e.Call(viewer, "GET", base+"/dds/00000000-0000-0000-0000-000000000001/checklist", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/dds/x/checklist", nil), nil)
	_ = b
	_ = pt

	// Switch to downstream trader: statement is a reference to the upstream one.
	var after food.EUDRSettings
	e.Must(http.StatusOK, e.Call(ops, "PUT", base+"/settings", map[string]any{"ddsRole": "downstream_trader", "reason": "we buy from an importer"}), &after)
	if after.DDSRole != "downstream_trader" {
		t.Fatalf("role: %+v", after)
	}
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"plotIds": nil})), nil)                                                          // no upstream reference
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"upstreamReference": "UP-1"})), nil)                                             // plots not accepted
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"plotIds": nil, "upstreamReference": "UP-1", "operatorRole": "operator"})), nil) // role mismatch
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"plotIds": nil, "upstreamReference": strings.Repeat("x", 201)})), nil)
	var ref food.DDS
	e.Must(http.StatusCreated, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"plotIds": nil, "upstreamReference": " UP-1 "})), &ref)
	if ref.Kind != "upstream_reference" || ref.Status != "referenced" || ref.UpstreamReference != "UP-1" || ref.OperatorRole != "downstream_trader" || !strings.HasPrefix(ref.Reference, "REF-") || len(ref.PlotIDs) != 0 {
		t.Fatalf("ref: %+v", ref)
	}
	e.Must(http.StatusCreated, e.Call(tok, "POST", base+"/dds", dds(map[string]any{"plotIds": nil, "upstreamReference": "UP-2", "upstreamOperator": "Importer BV", "reference": "MINE-2"})), nil)
	e.Must(http.StatusConflict, e.Call(tok, "POST", base+"/dds/"+ref.ID.String()+"/submit", nil), nil) // references are not submitted
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/dds/"+ref.ID.String()+"/checklist", nil), &cl)
	if !cl.Complete || cl.Kind != "upstream_reference" {
		t.Fatalf("ref checklist: %+v", cl)
	}

	// Audit entries for every write.
	for _, action := range []string{"food.eudr.settings", "food.eudr.date.set", "food.eudr.date.reset", "eudr.dds.create"} {
		var au struct {
			Items []struct{ Action, Reason string }
		}
		e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action="+action, nil), &au)
		if len(au.Items) == 0 {
			t.Errorf("no audit entry for %s", action)
		}
	}

	// Tenant isolation: another food tenant sees none of this.
	_, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "other", Name: "Other", AdminEmail: "a@other.test", AdminPassword: testkit.Password, Verticals: []string{"food"}})
	if err != nil {
		t.Fatal(err)
	}
	var otok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "other", "email": "a@other.test", "password": testkit.Password}), &otok)
	e.Must(http.StatusOK, e.Call(otok.Token, "GET", base+"/plots/validation", nil), &all)
	if len(all.Items) != 0 {
		t.Fatalf("leak: %+v", all.Items)
	}
	e.Must(http.StatusNotFound, e.Call(otok.Token, "GET", base+"/plots/"+a+"/validation", nil), nil)
	e.Must(http.StatusNotFound, e.Call(otok.Token, "GET", base+"/dds/"+d.ID.String()+"/checklist", nil), nil)
	var os food.EUDRSettings
	e.Must(http.StatusOK, e.Call(otok.Token, "GET", base+"/settings", nil), &os)
	if os.DDSRole != "operator" {
		t.Fatalf("other tenant settings: %+v", os)
	}
}

type excursion struct {
	ID          string
	Disposition string
	DeviceID    string
}

func TestColdChainDisposition(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "v@green.test", "Viewer")
	ops := foodUser(t, e, tok, "o@green.test", "Ops")
	integ := foodUser(t, e, tok, "i@green.test", "Integration")
	base := "/api/v1/food/cold-chain"

	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	temps := []float64{4, 5, 9, 10, 9, 5}
	var rs []map[string]any
	for i, c := range temps {
		rs = append(rs, map[string]any{"time": t0.Add(time.Duration(i) * 10 * time.Minute), "tempC": c})
	}
	e.Must(http.StatusAccepted, e.Call(ops, "POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "truck-1", "minC": 2, "maxC": 8, "readings": rs}), nil)
	e.Must(http.StatusAccepted, e.Call(ops, "POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "quiet", "minC": 2, "maxC": 8,
		"readings": []map[string]any{{"time": t0, "tempC": 5}}}), nil)
	e.Drain()

	var list struct{ Items []excursion }
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/excursions", nil), &list)
	if len(list.Items) != 1 || list.Items[0].Disposition != "open" {
		t.Fatalf("excursions: %+v", list)
	}
	id := list.Items[0].ID
	exc := base + "/excursions/" + id
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/excursions?disposition=bogus", nil), nil)

	// MKT over the excursion window and over a device window.
	var rep food.ExcursionReport
	e.Must(http.StatusOK, e.Call(viewer, "GET", exc, nil), &rep)
	if rep.Stats == nil || rep.Stats.Count != 4 || rep.Stats.MKTC < rep.Stats.MeanC || rep.Stats.MKTC > 10 || rep.Stats.ActivationKJ != food.DefaultActivationEnergyKJ || len(rep.Readings) != 4 {
		t.Fatalf("report: %+v", rep)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", exc+"?activationEnergyKJ=120", nil), &rep)
	if rep.Stats.ActivationKJ != 120 {
		t.Fatalf("activation: %+v", rep.Stats)
	}
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", exc+"?activationEnergyKJ=0", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", exc+"?activationEnergyKJ=abc", nil), nil)
	var w struct{ Stats food.TempStats }
	q := "?deviceId=truck-1&from=" + t0.Format(time.RFC3339) + "&to=" + t0.Add(time.Hour).Format(time.RFC3339)
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/mkt"+q, nil), &w)
	if w.Stats.Count != 6 || w.Stats.MinC != 4 || w.Stats.MaxC != 10 || !w.Stats.EvenSampling {
		t.Fatalf("window: %+v", w.Stats)
	}
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/mkt", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/mkt?deviceId=nobody&from="+t0.Format(time.RFC3339)+"&to="+t0.Add(time.Hour).Format(time.RFC3339), nil), nil) // no readings
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/mkt"+q+"&activationEnergyKJ=5000", nil), nil)
	e.Must(http.StatusNotFound, e.Call(viewer, "GET", base+"/excursions/00000000-0000-0000-0000-000000000001", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/excursions/x", nil), nil)

	// Lots to hold.
	for _, tlc := range []string{"COLD-1", "COLD-2"} {
		e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/lots", map[string]any{"tlc": tlc, "description": "Yoghurt", "quantity": 10, "uom": "KGM", "locationGln": farmGLN, "origin": "harvested"}), nil)
	}
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "COLD-2", "quantity": 10, "uom": "KGM", "locationGln": farmGLN,
		"shipTo": processorGLN, "shipToName": "Dairy", "refDocType": "BOL", "refDocNumber": "B1"}), nil) // COLD-2 becomes shipped
	lotStatus := func(tlc string) string {
		var l struct {
			Items []struct{ TLC, Status string }
		}
		e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/lots", nil), &l)
		for _, x := range l.Items {
			if x.TLC == tlc {
				return x.Status
			}
		}
		return ""
	}
	if lotStatus("COLD-2") != "shipped" {
		t.Fatalf("COLD-2 is %s", lotStatus("COLD-2"))
	}

	post := func(status int, tok string, body map[string]any, out any) {
		t.Helper()
		e.Must(status, e.Call(tok, "POST", exc+"/disposition", body), out)
	}
	sign := func(tok, entityID string) string {
		t.Helper()
		var s struct{ ID string }
		e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "approval", "reason": "disposition", "entity": "food_excursion", "entityId": entityID}), &s)
		return s.ID
	}
	// Roles and validation.
	post(http.StatusForbidden, viewer, map[string]any{"disposition": "quarantine", "reason": "r"}, nil)
	post(http.StatusForbidden, integ, map[string]any{"disposition": "quarantine", "reason": "r"}, nil)
	post(http.StatusBadRequest, ops, map[string]any{"disposition": "burn", "reason": "r"}, nil)
	post(http.StatusBadRequest, ops, map[string]any{"disposition": "quarantine", "reason": "  "}, nil)
	post(http.StatusBadRequest, ops, map[string]any{"disposition": "quarantine", "reason": "r", "tlcs": []string{"NOPE"}}, nil)
	e.Must(http.StatusNotFound, e.Call(ops, "POST", base+"/excursions/00000000-0000-0000-0000-000000000001/disposition", map[string]any{"disposition": "quarantine", "reason": "r"}), nil)
	e.Must(http.StatusBadRequest, e.Call(ops, "POST", base+"/excursions/x/disposition", map[string]any{"disposition": "quarantine", "reason": "r"}), nil)
	e.Must(http.StatusBadRequest, e.Call(ops, "POST", exc+"/disposition", map[string]any{"disposition": "quarantine", "reason": "r", "extra": 1}), nil)

	// Quarantine holds the lots; the transformation can no longer consume them.
	var dsp food.Disposition
	post(http.StatusCreated, ops, map[string]any{"disposition": "quarantine", "reason": "Door left open 30 min", "tlcs": []string{"COLD-1", "COLD-2", "COLD-1"}}, &dsp)
	if dsp.Disposition != "quarantine" || len(dsp.TLCs) != 2 || dsp.PriorStatus["COLD-2"] != "shipped" || dsp.SignatureID != nil {
		t.Fatalf("quarantine: %+v", dsp)
	}
	if lotStatus("COLD-1") != "quarantined" || lotStatus("COLD-2") != "quarantined" {
		t.Fatal("lots not quarantined")
	}
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", "/api/v1/food/transformations", map[string]any{"inputs": []map[string]any{{"tlc": "COLD-1", "quantity": 1}}, "output": map[string]any{"tlc": "X1", "description": "x", "quantity": 1}}), nil)
	post(http.StatusConflict, ops, map[string]any{"disposition": "quarantine", "reason": "again"}, nil)
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/excursions?disposition=quarantine", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("filter: %+v", list)
	}

	// Release needs a signature: none, someone else's, a signature on another entity.
	post(http.StatusPreconditionRequired, ops, map[string]any{"disposition": "release", "reason": "Within stability data"}, nil)
	post(http.StatusPreconditionRequired, ops, map[string]any{"disposition": "release", "reason": "ok", "signatureId": sign(tok, id)}, nil)             // admin's signature, Ops caller
	post(http.StatusPreconditionRequired, ops, map[string]any{"disposition": "release", "reason": "ok", "signatureId": sign(ops, "someone-else")}, nil) // wrong entity id
	sig := sign(ops, id)
	post(http.StatusCreated, ops, map[string]any{"disposition": "release", "reason": "Within stability data (QA review 42)", "signatureId": sig}, &dsp)
	if dsp.Disposition != "release" || dsp.SignedBy != "o@green.test" || dsp.Meaning != "approval" || len(dsp.TLCs) != 2 {
		t.Fatalf("release: %+v", dsp)
	}
	if lotStatus("COLD-1") != "active" || lotStatus("COLD-2") != "shipped" {
		t.Fatalf("lots after release: %s %s", lotStatus("COLD-1"), lotStatus("COLD-2"))
	}
	// Final: no further change, and the signature cannot be reused.
	post(http.StatusConflict, ops, map[string]any{"disposition": "destroy", "reason": "changed my mind", "signatureId": sign(ops, id)}, nil)

	// The report lists the history and lots; CSV and JSON downloads are audited.
	e.Must(http.StatusOK, e.Call(viewer, "GET", exc, nil), &rep)
	if rep.Excursion.Disposition != "release" || len(rep.History) != 2 || len(rep.Lots) != 2 {
		t.Fatalf("history: %+v", rep)
	}
	r := e.Call(viewer, "GET", exc+"/report?format=csv", nil)
	body := string(r.Body)
	if r.Status != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") || !strings.Contains(body, "MKT") || !strings.Contains(body, "release") ||
		!strings.Contains(body, "signed by o@green.test") || !strings.Contains(body, "time_utc,temp_c") || !strings.Contains(body, "TODO(spec)") {
		t.Fatalf("csv: %d %s", r.Status, body)
	}
	r = e.Call(viewer, "GET", exc+"/report", nil)
	if r.Status != http.StatusOK || !strings.Contains(r.Header.Get("Content-Disposition"), ".json") || !strings.Contains(string(r.Body), `"mktC"`) {
		t.Fatalf("json: %d %s", r.Status, r.Body)
	}
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", exc+"/report?format=pdf", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", exc+"/report?activationEnergyKJ=-1", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/excursions/x/report", nil), nil)
	e.Must(http.StatusNotFound, e.Call(viewer, "GET", base+"/excursions/00000000-0000-0000-0000-000000000001/report", nil), nil)

	for _, action := range []string{"food.coldchain.disposition.quarantine", "food.coldchain.disposition.release", "food.coldchain.report.export"} {
		var au struct {
			Items []struct{ Action, Reason string }
		}
		e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action="+action, nil), &au)
		if len(au.Items) == 0 {
			t.Errorf("no audit entry for %s", action)
		}
		if strings.HasSuffix(action, "release") && au.Items[0].Reason != "Within stability data (QA review 42)" {
			t.Errorf("audit reason: %+v", au.Items)
		}
	}

	// A second excursion: direct destroy with signature; lots go to destroyed, and not twice.
	base2 := t0.Add(24 * time.Hour)
	var rs2 []map[string]any
	for i, c := range []float64{5, 12, 5} {
		rs2 = append(rs2, map[string]any{"time": base2.Add(time.Duration(i) * 10 * time.Minute), "tempC": c})
	}
	e.Must(http.StatusAccepted, e.Call(ops, "POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "truck-2", "minC": 2, "maxC": 8, "readings": rs2}), nil)
	e.Drain()
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/excursions?disposition=open", nil), &list)
	if len(list.Items) != 1 || list.Items[0].DeviceID != "truck-2" {
		t.Fatalf("open: %+v", list)
	}
	id2 := list.Items[0].ID
	exc2 := base + "/excursions/" + id2 + "/disposition"
	e.Must(http.StatusCreated, e.Call(ops, "POST", exc2, map[string]any{"disposition": "destroy", "reason": "Above limit for too long", "tlcs": []string{"COLD-1"}, "signatureId": sign(ops, id2)}), &dsp)
	if lotStatus("COLD-1") != "destroyed" {
		t.Fatalf("COLD-1: %s", lotStatus("COLD-1"))
	}
	var quarantineOn struct{ ID string }
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/excursions?disposition=destroy", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("destroyed list: %+v", list)
	}
	_ = quarantineOn

	// Third: a destroyed lot cannot be destroyed or quarantined again.
	var rs3 []map[string]any
	for i, c := range []float64{5, 13, 5} {
		rs3 = append(rs3, map[string]any{"time": base2.Add(48*time.Hour + time.Duration(i)*10*time.Minute), "tempC": c})
	}
	e.Must(http.StatusAccepted, e.Call(ops, "POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "truck-3", "minC": 2, "maxC": 8, "readings": rs3}), nil)
	e.Drain()
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/excursions?disposition=open", nil), &list)
	id3 := list.Items[0].ID
	exc3 := base + "/excursions/" + id3 + "/disposition"
	e.Must(http.StatusConflict, e.Call(ops, "POST", exc3, map[string]any{"disposition": "quarantine", "reason": "r", "tlcs": []string{"COLD-1"}}), nil)
	e.Must(http.StatusConflict, e.Call(ops, "POST", exc3, map[string]any{"disposition": "destroy", "reason": "r", "tlcs": []string{"COLD-1"}, "signatureId": sign(ops, id3)}), nil)

	// Tenant isolation: another food tenant sees no excursion or disposition.
	_, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "other", Name: "Other", AdminEmail: "a@other.test", AdminPassword: testkit.Password, Verticals: []string{"food"}})
	if err != nil {
		t.Fatal(err)
	}
	var otok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "other", "email": "a@other.test", "password": testkit.Password}), &otok)
	e.Must(http.StatusOK, e.Call(otok.Token, "GET", base+"/excursions", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("leak: %+v", list)
	}
	e.Must(http.StatusNotFound, e.Call(otok.Token, "POST", exc+"/disposition", map[string]any{"disposition": "quarantine", "reason": "r"}), nil)
	e.Must(http.StatusNotFound, e.Call(otok.Token, "GET", exc, nil), nil)
}

func TestColdChainNoReadings(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	ops := foodUser(t, e, tok, "o@green.test", "Ops")
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	e.Must(http.StatusAccepted, e.Call(ops, "POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "d", "minC": 2, "maxC": 8,
		"readings": []map[string]any{{"time": t0, "tempC": 12}, {"time": t0.Add(time.Minute), "tempC": 5}}}), nil)
	e.Drain()
	var list struct{ Items []excursion }
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/cold-chain/excursions", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("list: %+v", list)
	}
	// Remove the readings so the report has none (the excursion row stays).
	c, err := pgx.Connect(context.Background(), e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(context.Background(), `delete from sensor_reading`); err != nil {
		t.Fatal(err)
	}
	c.Close(context.Background())
	var rep food.ExcursionReport
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/cold-chain/excursions/"+list.Items[0].ID, nil), &rep)
	if rep.Stats != nil || rep.StatsError == "" || len(rep.Readings) != 0 {
		t.Fatalf("no readings: %+v", rep)
	}
	r := e.Call(tok, "GET", "/api/v1/food/cold-chain/excursions/"+list.Items[0].ID+"/report?format=csv", nil)
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), "statistics unavailable") {
		t.Fatalf("csv: %s", r.Body)
	}
}
