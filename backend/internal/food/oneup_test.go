package food_test

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/food"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/gs1"
)

var (
	supplierGLN = gs1.WithCheckDigit("950600005000")
	plantGLN    = gs1.WithCheckDigit("950600006000")
	custA       = gs1.WithCheckDigit("950600007000")
	custB       = gs1.WithCheckDigit("950600008000")
)

func foodOf(e *testkit.Env) *food.Module {
	for _, m := range e.App.Modules {
		if f, ok := m.(*food.Module); ok {
			return f
		}
	}
	return nil
}

// reportFixture: lettuce received from a supplier, own spinach harvested, both transformed into
// a salad that is shipped to two customers (one with a name that needs CSV quoting).
func reportFixture(t *testing.T, e *testkit.Env, tok string) time.Time {
	t.Helper()
	do := func(status int, method, path string, body any) {
		t.Helper()
		e.Must(status, e.Call(tok, method, path, body), nil)
	}
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	do(http.StatusCreated, "POST", "/api/v1/partners", map[string]any{"name": "Valley Growers", "gln": supplierGLN})
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "receiving", "tlc": "LET-R1", "quantity": 200, "uom": "KGM", "locationGln": plantGLN,
		"shipFrom": supplierGLN, "refDocType": "BOL", "refDocNumber": "BOL-77", "eventTime": t0, "kdes": map[string]any{"productDescription": "Iceberg lettuce"}})
	do(http.StatusCreated, "POST", "/api/v1/food/lots", map[string]any{"tlc": "SPI-H1", "description": "Spinach", "quantity": 100, "uom": "KGM",
		"locationGln": plantGLN, "origin": "harvested", "harvestDate": "2026-06-01"})
	do(http.StatusCreated, "POST", "/api/v1/food/transformations", map[string]any{
		"inputs": []map[string]any{{"tlc": "LET-R1", "quantity": 150}, {"tlc": "SPI-H1", "quantity": 100}},
		"output": map[string]any{"tlc": "MIX-9", "description": "Leafy mix 250g", "quantity": 1000, "uom": "EA", "locationGln": plantGLN}, "eventTime": t0.Add(24 * time.Hour)})
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "MIX-9", "quantity": 600, "uom": "EA", "locationGln": plantGLN,
		"shipTo": custA, "shipToName": `Fresh "Market", Ltd`, "refDocType": "ASN", "refDocNumber": "ASN-1", "eventTime": t0.Add(48 * time.Hour)})
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "MIX-9", "quantity": 400, "uom": "EA", "locationGln": plantGLN,
		"shipTo": custB, "shipToName": "=HYPERLINK(1)", "refDocType": "ASN", "refDocNumber": "ASN-2", "eventTime": t0.Add(10 * 24 * time.Hour)})
	// The rest of the lettuce goes straight to a customer.
	do(http.StatusCreated, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "LET-R1", "quantity": 50, "uom": "KGM", "locationGln": plantGLN,
		"shipTo": custA, "shipToName": "Fresh Market", "refDocType": "ASN", "refDocNumber": "ASN-3", "eventTime": t0.Add(3 * 24 * time.Hour)})
	return t0
}

func rel(rows []food.ReportRow) []string {
	var out []string
	for _, r := range rows {
		s := r.Lot + ">" + r.Relation + ">" + r.LotCode
		if r.ViaLot != "" {
			s += "@" + r.ViaLot
		}
		out = append(out, s)
	}
	return out
}

func TestOneUpOneDownReport(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "viewer@green.test", "Viewer")
	reportFixture(t, e, tok)
	get := func(path string) food.Report {
		t.Helper()
		var rep food.Report
		e.Must(http.StatusOK, e.Call(tok, "GET", path, nil), &rep)
		return rep
	}

	// Output lot: inputs are its one-up (internal), the lettuce supplier is reached through LET-R1.
	rep := get("/api/v1/food/one-up-one-down?tlc=MIX-9")
	got := strings.Join(rel(rep.Rows), " | ")
	want := "MIX-9>received_from>LET-R1@LET-R1 | MIX-9>transformation_input>LET-R1 | MIX-9>transformation_input>SPI-H1 | MIX-9>shipped_to>MIX-9 | MIX-9>shipped_to>MIX-9"
	if got != want {
		t.Fatalf("rows:\n got %s\nwant %s", got, want)
	}
	recv := rep.Rows[0]
	if recv.Direction != "one_up" || recv.CounterpartyID != supplierGLN || recv.CounterpartyName != "Valley Growers" || recv.RefDocNumber != "BOL-77" ||
		recv.Quantity != 200 || recv.UOM != "KGM" || recv.ProductDescription != "Iceberg lettuce" {
		t.Fatalf("receipt row: %+v", recv)
	}
	in := rep.Rows[1]
	if in.Quantity != 150 || in.UOM != "KGM" || in.CounterpartyID != plantGLN || in.EventType != "transformation" {
		t.Fatalf("input row: %+v", in)
	}
	if rep.Rows[3].Direction != "one_down" || rep.Rows[3].CounterpartyName != `Fresh "Market", Ltd` || rep.Rows[3].Quantity != 600 {
		t.Fatalf("ship row: %+v", rep.Rows[3])
	}
	if len(rep.Notes) == 0 || !strings.Contains(strings.Join(rep.Notes, " "), "does not prescribe a file format") || rep.Operator != "Green Farm" {
		t.Fatalf("notes/operator: %+v", rep)
	}
	if len(rep.Lots) != 1 || rep.Lots[0].Description != "Leafy mix 250g" {
		t.Fatalf("lots: %+v", rep.Lots)
	}

	// Input lot: customers are reached through the transformation output.
	rep = get("/api/v1/food/one-up-one-down?tlc=LET-R1")
	got = strings.Join(rel(rep.Rows), " | ")
	want = "LET-R1>received_from>LET-R1 | LET-R1>transformation_output>MIX-9 | LET-R1>shipped_to>MIX-9@MIX-9 | LET-R1>shipped_to>LET-R1 | LET-R1>shipped_to>MIX-9@MIX-9"
	if got != want {
		t.Fatalf("rows:\n got %s\nwant %s", got, want)
	}
	if rep.Rows[1].Quantity != 1000 || rep.Rows[1].UOM != "EA" {
		t.Fatalf("output row: %+v", rep.Rows[1])
	}

	// Date filters apply to the events (to is inclusive).
	rep = get("/api/v1/food/one-up-one-down?tlc=MIX-9&from=2026-06-02&to=2026-06-03")
	if got := strings.Join(rel(rep.Rows), " | "); got != "MIX-9>transformation_input>LET-R1 | MIX-9>transformation_input>SPI-H1 | MIX-9>shipped_to>MIX-9" {
		t.Fatalf("window: %s", got)
	}
	rep = get("/api/v1/food/one-up-one-down?tlc=MIX-9&from=2026-06-05")
	if len(rep.Rows) != 1 || rep.Rows[0].RefDocNumber != "ASN-2" {
		t.Fatalf("from only: %+v", rel(rep.Rows))
	}
	// Without lots: every lot with a receipt, shipment or transformation in the period.
	rep = get("/api/v1/food/one-up-one-down?from=2026-06-01&to=2026-06-01")
	if len(rep.Lots) != 1 || rep.Lots[0].TLC != "LET-R1" || len(rep.Rows) != 1 {
		t.Fatalf("period scope: %+v %v", rep.Lots, rel(rep.Rows))
	}
	rep = get("/api/v1/food/one-up-one-down")
	if len(rep.Lots) != 3 || rep.Truncated {
		t.Fatalf("all lots: %+v", rep.Lots)
	}
	rep = get("/api/v1/food/one-up-one-down?tlc=LET-R1,MIX-9&tlc=MIX-9")
	if len(rep.Lots) != 2 || len(rep.Scope.TLCs) != 2 {
		t.Fatalf("multi lots: %+v", rep.Scope)
	}

	// Product scope.
	var p struct{ ID, GTIN string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134376", "name": "Kale", "vertical": "food"}), &p)
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/lots", map[string]any{"tlc": "KALE-1", "productId": p.ID, "quantity": 10}), nil)
	rep = get("/api/v1/food/one-up-one-down?productId=" + p.ID)
	if len(rep.Lots) != 1 || rep.Lots[0].TLC != "KALE-1" || rep.Lots[0].GTIN != "09506000134376" || len(rep.Rows) != 0 {
		t.Fatalf("product scope: %+v", rep)
	}
	rep = get("/api/v1/food/one-up-one-down?gtin=9506000134376")
	if len(rep.Lots) != 1 || rep.Scope.GTIN != "09506000134376" {
		t.Fatalf("gtin scope: %+v", rep)
	}

	// Validation.
	for _, q := range []string{"tlc=NOPE", "from=1/6/2026", "to=2026-13-01", "from=2026-06-05&to=2026-06-01", "format=xml"} {
		e.Must(http.StatusBadRequest, e.Call(tok, "GET", "/api/v1/food/one-up-one-down?"+q, nil), nil)
	}

	// CSV: notes as # comments, escaping of quotes and commas, formulas neutralised.
	r := e.Call(tok, "GET", "/api/v1/food/one-up-one-down?tlc=MIX-9&format=csv", nil)
	if r.Status != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") || !strings.Contains(r.Header.Get("Content-Disposition"), ".csv") {
		t.Fatalf("csv: %d %v", r.Status, r.Header)
	}
	body := string(r.Body)
	if !strings.HasPrefix(body, "# One-up/one-down traceability report\n") || !strings.Contains(body, "# The Regulation does not prescribe a file format or columns") ||
		!strings.Contains(body, "TODO(spec)") || !strings.Contains(body, `"Fresh ""Market"", Ltd"`) {
		t.Fatalf("csv body:\n%s", body)
	}
	cr := csv.NewReader(strings.NewReader(body))
	cr.Comment = '#'
	rows, err := cr.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 || rows[0][0] != "Lot" || rows[0][15] != "Certificates" || rows[4][5] != `Fresh "Market", Ltd` || rows[5][5] != "'=HYPERLINK(1)" || rows[1][7] != "2026-06-01" || rows[1][8] != "08:00:00" {
		t.Fatalf("csv rows: %q", rows)
	}
	// JSON download.
	r = e.Call(tok, "GET", "/api/v1/food/one-up-one-down?tlc=MIX-9&format=json", nil)
	var dl food.Report
	if r.Status != http.StatusOK || !strings.Contains(r.Header.Get("Content-Disposition"), ".json") || json.Unmarshal(r.Body, &dl) != nil || len(dl.Rows) != 5 {
		t.Fatalf("json download: %d %s", r.Status, r.Body)
	}
	// Downloads are audited, the JSON view is not.
	var au struct {
		Total int
		Items []struct{ Actor, Action string }
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action=food.oneupdown.export", nil), &au)
	if au.Total != 2 {
		t.Fatalf("export audit: %+v", au)
	}

	// Viewer can read and download, not create authority requests.
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/food/one-up-one-down?tlc=MIX-9", nil), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/food/one-up-one-down/authority-requests", map[string]any{"authority": "NVWA", "tlcs": []string{"MIX-9"}}), nil)
}

func TestAuthorityRequests(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "viewer@green.test", "Viewer")
	reportFixture(t, e, tok)

	for _, body := range []any{
		map[string]any{"tlcs": []string{"MIX-9"}},
		map[string]any{"authority": "NVWA"},
		map[string]any{"authority": "NVWA", "tlcs": []string{"NOPE"}},
		map[string]any{"authority": "NVWA", "tlcs": []string{"MIX-9"}, "from": "June"},
		"{",
	} {
		e.Must(http.StatusBadRequest, e.Call(tok, "POST", "/api/v1/food/one-up-one-down/authority-requests", body), nil)
	}
	var ar food.AuthorityRequest
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/one-up-one-down/authority-requests", map[string]any{
		"authority": "NVWA", "reference": "REQ 2026/0042", "reason": "Listeria investigation", "tlcs": []string{"MIX-9", "LET-R1", "MIX-9"},
		"from": "2026-06-01", "to": "2026-06-30"}), &ar)
	if ar.Rows != 10 || len(ar.TLCs) != 2 || len(ar.SHA256) != 64 || ar.CreatedBy == "" || ar.Report == nil || len(ar.Report.Rows) != 10 ||
		ar.PeriodFrom == nil || ar.PeriodTo.Format("2006-01-02") != "2026-06-30" || len(ar.Exports) != 0 {
		t.Fatalf("authority request: %+v", ar)
	}

	// Later data changes do not alter the stored report.
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/cte", map[string]any{"type": "shipping", "tlc": "MIX-9", "quantity": 1, "locationGln": plantGLN,
		"shipTo": custB, "refDocNumber": "ASN-9", "eventTime": time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)}), nil)

	var list struct {
		Total int
		Items []food.AuthorityRequest
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/food/one-up-one-down/authority-requests", nil), &list)
	if list.Total != 1 || list.Items[0].Report != nil {
		t.Fatalf("list: %+v", list)
	}
	var one food.AuthorityRequest
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/food/one-up-one-down/authority-requests/"+ar.ID.String(), nil), &one)
	if one.Report == nil || len(one.Report.Rows) != 10 {
		t.Fatalf("get: %+v", one)
	}

	// Exports: CSV and JSON, recorded with the exporter.
	r := e.Call(viewer, "GET", "/api/v1/food/one-up-one-down/authority-requests/"+ar.ID.String()+"/export?format=csv", nil)
	if r.Status != http.StatusOK || !strings.Contains(r.Header.Get("Content-Disposition"), "one-up-one-down-REQ-2026-0042.csv") || strings.Contains(string(r.Body), "ASN-9") {
		t.Fatalf("csv export: %d %v %s", r.Status, r.Header, r.Body)
	}
	cr := csv.NewReader(strings.NewReader(string(r.Body)))
	cr.Comment = '#'
	rows, err := cr.ReadAll()
	if err != nil || len(rows) != 11 {
		t.Fatalf("csv rows: %v %d", err, len(rows))
	}
	r = e.Call(tok, "GET", "/api/v1/food/one-up-one-down/authority-requests/"+ar.ID.String()+"/export", nil)
	var exp struct {
		Request food.AuthorityRequest
		Report  food.Report
	}
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &exp) != nil || exp.Request.Authority != "NVWA" || len(exp.Report.Rows) != 10 {
		t.Fatalf("json export: %d %s", r.Status, r.Body)
	}
	e.Must(http.StatusBadRequest, e.Call(tok, "GET", "/api/v1/food/one-up-one-down/authority-requests/"+ar.ID.String()+"/export?format=pdf", nil), nil)
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/food/one-up-one-down/authority-requests/00000000-0000-0000-0000-000000000001/export", nil), nil)
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/one-up-one-down/authority-requests/"+ar.ID.String(), nil), &one)
	if len(one.Exports) != 2 || one.Exports[0]["actor"] != "user:viewer@green.test" && !strings.Contains(one.Exports[0]["actor"].(string), "viewer@green.test") || one.Exports[0]["format"] != "csv" {
		t.Fatalf("exports: %+v", one.Exports)
	}
	var au struct {
		Total int
		Items []struct{ Actor, Action, EntityID string }
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action=food.oneupdown.authority_request.export", nil), &au)
	if au.Total != 2 || !strings.HasPrefix(au.Items[1].Actor, "user:") || strings.Contains(au.Items[1].Actor, "@") || au.Items[0].EntityID != ar.ID.String() {
		t.Fatalf("audit: %+v", au)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action=food.oneupdown.authority_request.create", nil), &au)
	if au.Total != 1 {
		t.Fatalf("create audit: %+v", au)
	}

	// Tenant isolation: another food tenant sees nothing.
	_, _, err = e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "otherfarm", Name: "Other Farm", AdminEmail: "a@other.test",
		AdminPassword: testkit.Password, Verticals: []string{"food"}})
	if err != nil {
		t.Fatal(err)
	}
	var tk struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "otherfarm", "email": "a@other.test", "password": testkit.Password}), &tk)
	e.Must(http.StatusOK, e.Call(tk.Token, "GET", "/api/v1/food/one-up-one-down/authority-requests", nil), &list)
	if list.Total != 0 {
		t.Fatalf("cross-tenant list: %+v", list)
	}
	e.Must(http.StatusNotFound, e.Call(tk.Token, "GET", "/api/v1/food/one-up-one-down/authority-requests/"+ar.ID.String(), nil), nil)
	e.Must(http.StatusBadRequest, e.Call(tk.Token, "GET", "/api/v1/food/one-up-one-down?tlc=MIX-9", nil), nil)
	var empty food.Report
	e.Must(http.StatusOK, e.Call(tk.Token, "GET", "/api/v1/food/one-up-one-down", nil), &empty)
	if len(empty.Lots) != 0 || len(empty.Rows) != 0 {
		t.Fatalf("cross-tenant report: %+v", empty)
	}
	// A tenant without the food vertical cannot use the report.
	e.Must(http.StatusForbidden, e.Do("GET", "/api/v1/food/one-up-one-down", nil), nil)
}

func TestFoodCertificates(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "viewer@green.test", "Viewer")
	reportFixture(t, e, tok)
	day := func(d int) string { return time.Now().UTC().AddDate(0, 0, d).Format("2006-01-02") }
	post := func(status int, body any, out any) {
		t.Helper()
		e.Must(status, e.Call(tok, "POST", "/api/v1/food/certificates", body), out)
	}
	if s := foodOf(e).Schedules(); len(s) != 1 || s[0].Name != "food.cert_expiry" || s[0].Every != 6*time.Hour {
		t.Fatalf("schedules: %+v", s)
	}

	// A document uploaded to the shared document store is referenced by id.
	var doc struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/documents", map[string]any{"title": "halal.pdf", "contentType": "application/pdf",
		"content": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 certificate"))}), &doc)
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134376", "name": "Leafy mix", "vertical": "food"}), &p)
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/lots", map[string]any{"tlc": "MIX-10", "productId": p.ID, "quantity": 10}), nil)
	var partners struct{ Items []struct{ ID, GLN string } }
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/partners", nil), &partners)

	// Validation.
	lot := []map[string]string{{"targetType": "lot", "target": "MIX-9"}}
	for name, body := range map[string]any{
		"no kind":       map[string]any{"links": lot},
		"no links":      map[string]any{"kind": "halal"},
		"bad date":      map[string]any{"kind": "halal", "validTo": "31/12/2030", "links": lot},
		"bad from":      map[string]any{"kind": "halal", "validFrom": "soon", "links": lot},
		"reversed":      map[string]any{"kind": "halal", "validFrom": day(10), "validTo": day(1), "links": lot},
		"bad type":      map[string]any{"kind": "halal", "links": []map[string]string{{"targetType": "pallet", "target": "x"}}},
		"unknown lot":   map[string]any{"kind": "halal", "links": []map[string]string{{"targetType": "lot", "target": "NOPE"}}},
		"unknown prod":  map[string]any{"kind": "halal", "links": []map[string]string{{"targetType": "product", "target": "00000000-0000-0000-0000-000000000001"}}},
		"unknown gtin":  map[string]any{"kind": "halal", "links": []map[string]string{{"targetType": "product", "target": "09506000134383"}}},
		"bad gln":       map[string]any{"kind": "halal", "links": []map[string]string{{"targetType": "site", "target": "123"}}},
		"unknown doc":   map[string]any{"kind": "halal", "documentId": "00000000-0000-0000-0000-000000000001", "links": lot},
		"unknown party": map[string]any{"kind": "halal", "partnerId": "00000000-0000-0000-0000-000000000001", "links": lot},
		"bad json":      "{",
	} {
		if r := e.Call(tok, "POST", "/api/v1/food/certificates", body); r.Status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/food/certificates", map[string]any{"kind": "halal", "links": lot}), nil)

	// Halal on the lot (expiring), origin on the product (by GTIN; valid), organic on the supplier
	// site (expired), and one not yet valid.
	var halal, origin, organic, future food.LinkedCertificate
	post(http.StatusCreated, map[string]any{"kind": "Halal", "number": "HC-001", "issuer": "Example Halal Board", "scope": "Leafy salads", "validFrom": day(-300),
		"validTo": day(10) + "T00:00:00Z", "documentId": doc.ID, "links": lot}, &halal)
	post(http.StatusCreated, map[string]any{"kind": "origin", "number": "CO-55", "issuer": "Chamber of Commerce", "validTo": day(200),
		"links": []map[string]string{{"targetType": "product", "target": "9506000134376"}}}, &origin)
	post(http.StatusCreated, map[string]any{"kind": "organic", "number": "ORG-1", "issuer": "Control body", "partnerId": partners.Items[0].ID, "validTo": day(-2),
		"links": []map[string]string{{"targetType": "site", "target": supplierGLN}}}, &organic)
	post(http.StatusCreated, map[string]any{"kind": "kosher", "number": "K-1", "validFrom": day(5), "links": lot}, &future)
	if halal.Kind != "halal" || halal.Status != food.CertExpiring || halal.Scope != "Leafy salads" || halal.DocumentID == nil || halal.DocumentID.String() != doc.ID ||
		len(halal.Links) != 1 || origin.Status != food.CertValid || origin.Links[0].Target != p.ID || organic.Status != food.CertExpired ||
		organic.Supplier != "Valley Growers" || future.Status != food.CertNotYetValid {
		t.Fatalf("certificates: %+v\n%+v\n%+v\n%+v", halal, origin, organic, future)
	}
	// The certificates are the supplier compliance hub's records.
	var hub struct {
		Total int
		Items []struct{ ID, Kind string }
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/suppliers/certificates", nil), &hub)
	if hub.Total != 4 {
		t.Fatalf("hub: %+v", hub)
	}
	// A certificate entered in the hub for the supplier covers lots received from it.
	var held struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/suppliers/certificates", map[string]any{"partnerId": partners.Items[0].ID, "kind": "GlobalG.A.P.", "number": "GG-9", "validTo": day(400)}), &held)

	// Link an existing certificate to another lot; duplicates conflict; unknown certificate 404.
	var link food.CertLink
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/certificates/"+origin.ID.String()+"/links", map[string]string{"targetType": "lot", "target": "LET-R1"}), &link)
	e.Must(http.StatusConflict, e.Call(tok, "POST", "/api/v1/food/certificates/"+origin.ID.String()+"/links", map[string]string{"targetType": "lot", "target": "LET-R1"}), nil)
	e.Must(http.StatusNotFound, e.Call(tok, "POST", "/api/v1/food/certificates/00000000-0000-0000-0000-000000000001/links", map[string]string{"targetType": "lot", "target": "LET-R1"}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", "/api/v1/food/certificates/"+origin.ID.String()+"/links", map[string]string{"targetType": "product", "target": "nope"}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", "/api/v1/food/certificates/"+origin.ID.String()+"/links", "{"), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/food/certificates/"+origin.ID.String()+"/links", map[string]string{"targetType": "lot", "target": "SPI-H1"}), nil)

	// Coverage of a lot: directly, via product, via site, via supplier.
	type cov struct {
		TLC          string
		Certificates []food.Coverage
	}
	var c cov
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/food/lots/MIX-9/certificates", nil), &c)
	if len(c.Certificates) != 2 || c.Certificates[0].Number != "HC-001" || c.Certificates[0].Via != "lot" || c.Certificates[1].Status != food.CertNotYetValid {
		t.Fatalf("MIX-9 coverage: %+v", c)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/lots/MIX-10/certificates", nil), &c)
	if len(c.Certificates) != 1 || c.Certificates[0].Via != "product" || c.Certificates[0].Kind != "origin" {
		t.Fatalf("MIX-10 coverage: %+v", c)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/lots/LET-R1/certificates", nil), &c)
	vias := []string{}
	for _, x := range c.Certificates {
		vias = append(vias, x.Kind+"/"+x.Via)
	}
	if strings.Join(vias, ",") != "organic/site,origin/lot,GlobalG.A.P./supplier" {
		t.Fatalf("LET-R1 coverage: %v", vias)
	}
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/food/lots/NOPE/certificates", nil), nil)

	// Listing with filters.
	var list struct {
		Total int
		Items []food.LinkedCertificate
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/certificates", nil), &list)
	if list.Total != 4 || list.Items[0].Number != "ORG-1" {
		t.Fatalf("list: %+v", list)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/certificates?kind=HALAL", nil), &list)
	if list.Total != 1 {
		t.Fatalf("kind filter: %+v", list)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/certificates?status=expired", nil), &list)
	if list.Total != 1 || list.Items[0].Kind != "organic" {
		t.Fatalf("status filter: %+v", list)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/certificates?targetType=lot&target=LET-R1", nil), &list)
	if list.Total != 1 || list.Items[0].Kind != "origin" || len(list.Items[0].Links) != 1 {
		t.Fatalf("target filter: %+v", list)
	}

	// Certificates appear in the one-up/one-down report and, on request, in the FSMA 204 export.
	var rep food.Report
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/food/one-up-one-down?tlc=MIX-9", nil), &rep)
	if len(rep.Lots[0].Certificates) != 2 || rep.Rows[0].LotCode != "LET-R1" || len(rep.Rows[0].Certificates) != 3 ||
		!strings.Contains(strings.Join(rep.Rows[3].Certificates, ","), "halal:HC-001 (Example Halal Board)") {
		t.Fatalf("report certificates: %+v", rep)
	}
	r := e.Call(tok, "GET", "/api/v1/food/fsma204/export", nil)
	if strings.Contains(string(r.Body), "HC-001") {
		t.Fatal("certificates must be opt-in in the FSMA export")
	}
	rows := readCSV(t, e.Call(tok, "GET", "/api/v1/food/fsma204/export?certificates=true", nil))
	found := false
	for _, row := range rows[1:] {
		if row[0] == "MIX-9" && strings.Contains(row[len(row)-1], `"certificates":["halal:HC-001 (Example Halal Board)"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("fsma certificates: %q", rows)
	}

	// Scheduled expiry check: expiring → warning, expired → error, with covered lots; idempotent; audited.
	run := foodOf(e).Schedules()[0].Run
	for i := 0; i < 2; i++ {
		if err := run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var al struct {
		Items []struct {
			Severity, Title, EntityID string
			Detail                    map[string]any
		}
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/alerts?kind=food.certificate_expiring", nil), &al)
	if len(al.Items) != 2 {
		t.Fatalf("alerts: %+v", al)
	}
	sev := map[string]string{}
	lots := map[string]string{}
	for _, a := range al.Items {
		sev[a.EntityID] = a.Severity
		b, _ := json.Marshal(a.Detail["lots"])
		lots[a.EntityID] = string(b)
	}
	if sev[halal.ID.String()] != "warning" || sev[organic.ID.String()] != "error" || lots[halal.ID.String()] != `["MIX-9"]` || lots[organic.ID.String()] != `["LET-R1"]` {
		t.Fatalf("alert severities/lots: %v %v", sev, lots)
	}
	var au struct{ Total int }
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action=food.certificate.expiry_alert", nil), &au)
	if au.Total != 2 {
		t.Fatalf("expiry audit: %+v", au)
	}

	// Unlink: Viewer may not; the link goes, the hub certificate stays; audited.
	e.Must(http.StatusForbidden, e.Call(viewer, "DELETE", "/api/v1/food/certificates/links/"+link.ID.String(), nil), nil)
	e.Must(http.StatusNoContent, e.Call(tok, "DELETE", "/api/v1/food/certificates/links/"+link.ID.String(), nil), nil)
	e.Must(http.StatusNotFound, e.Call(tok, "DELETE", "/api/v1/food/certificates/links/"+link.ID.String(), nil), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "DELETE", "/api/v1/food/certificates/links/x", nil), nil)
	for _, action := range []string{"food.certificate.create", "food.certificate.link", "food.certificate.unlink"} {
		e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action="+action, nil), &au)
		if au.Total == 0 {
			t.Fatalf("no audit for %s", action)
		}
	}

	// Tenant isolation.
	other, otherTok := e.NewTenant("acme2", "a@acme2.test")
	_ = other
	e.Must(http.StatusForbidden, e.Call(otherTok, "GET", "/api/v1/food/certificates", nil), nil)
	_, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "otherfarm", Name: "Other Farm", AdminEmail: "a@other.test",
		AdminPassword: testkit.Password, Verticals: []string{"food"}})
	if err != nil {
		t.Fatal(err)
	}
	var tk struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "otherfarm", "email": "a@other.test", "password": testkit.Password}), &tk)
	e.Must(http.StatusOK, e.Call(tk.Token, "GET", "/api/v1/food/certificates", nil), &list)
	if list.Total != 0 {
		t.Fatalf("cross-tenant certificates: %+v", list)
	}
	e.Must(http.StatusNotFound, e.Call(tk.Token, "POST", "/api/v1/food/certificates/"+halal.ID.String()+"/links", map[string]string{"targetType": "site", "target": supplierGLN}), nil)
	e.Must(http.StatusNotFound, e.Call(tk.Token, "DELETE", "/api/v1/food/certificates/links/"+halal.Links[0].ID.String(), nil), nil)
	e.Must(http.StatusOK, e.Call(tk.Token, "GET", "/api/v1/alerts?kind=food.certificate_expiring", nil), &al)
	if len(al.Items) != 0 {
		t.Fatalf("cross-tenant alerts: %+v", al)
	}
}

func TestCertStatusAndRef(t *testing.T) {
	d := func(s string) *time.Time {
		v, _ := time.Parse("2006-01-02", s)
		return &v
	}
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	for want, c := range map[string]food.Certificate{
		food.CertValid:       {},
		food.CertNotYetValid: {ValidFrom: d("2026-10-24")},
		food.CertExpired:     {ValidTo: d("2026-10-22")},
		food.CertExpiring:    {ValidTo: d("2026-10-23")},
	} {
		if got := food.CertStatus(c, now); got != want {
			t.Fatalf("%s: got %s", want, got)
		}
	}
	if food.CertStatus(food.Certificate{ValidTo: d("2026-11-22")}, now) != food.CertValid || food.CertStatus(food.Certificate{ValidTo: d("2026-11-21")}, now) != food.CertExpiring {
		t.Fatal("30-day window")
	}
	if (food.Certificate{Kind: "halal"}).Ref() != "halal" || (food.Certificate{Kind: "origin", Number: "1", Issuer: "CC"}).Ref() != "origin:1 (CC)" {
		t.Fatal("ref")
	}
	// Without a registry the module degrades: no coverage, no alerts.
	var m food.Module
	if err := m.CertificateExpiryAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
}
