package link_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/testkit"
)

const ordersCSV = "PO Number;Order Date;Customer;Customer GLN;GTIN;Qty;Price;Lot;Expiry\n" +
	"PO-100; 23/10/2026 ;acme;9506000000008;09506000134352;10;2.5;L1;31/12/2027\n" +
	"PO-100;23/10/2026;acme;9506000000008;09506000134353;5;1;L2;31/12/2027\n" +
	"PO-101;24/10/2026;globex;0614141000005;09506000134352;7;2.5;L3;01/06/2028\n"

func orderMapping() map[string]any {
	return map[string]any{
		"name": "Acme orders", "docType": "order", "format": "csv", "options": map[string]any{"delimiter": ";"},
		"fields": []map[string]any{
			{"target": "number", "source": "PO Number", "transforms": []map[string]any{{"op": "trim"}}},
			{"target": "date", "source": "Order Date", "transforms": []map[string]any{{"op": "trim"}, {"op": "date", "from": "DD/MM/YYYY"}}},
			{"target": "buyer.name", "source": "Customer", "transforms": []map[string]any{{"op": "upper"}}},
			{"target": "buyer.gln", "source": "Customer GLN"},
			{"target": "seller.name", "transforms": []map[string]any{{"op": "constant", "value": "Our Company"}}},
			{"target": "currency", "source": "Customer", "transforms": []map[string]any{{"op": "lookup", "table": map[string]string{"acme": "EUR"}, "value": "USD"}}},
			{"target": "references.contract", "source": "Customer", "transforms": []map[string]any{{"op": "concat", "sources": []string{"Customer GLN"}, "separator": "-"}}},
			{"target": "lines.gtin", "source": "GTIN"},
			{"target": "lines.quantity", "source": "Qty"},
			{"target": "lines.unitPrice", "source": "Price"},
			{"target": "lines.lot", "source": "Lot", "transforms": []map[string]any{{"op": "lower"}}},
			{"target": "lines.expiry", "source": "Expiry", "transforms": []map[string]any{{"op": "date", "from": "DD/MM/YYYY", "to": "YYYY-MM-DD"}}},
		},
	}
}

type mapResult struct {
	Columns   []string
	Rows      int
	Sample    []map[string]string
	Documents []struct {
		Type, Number, Currency string
		Date                   string
		Buyer, Seller          struct{ Name, GLN string }
		References             map[string]string
		Lines                  []struct {
			GTIN, Lot, Expiry, UOM string
			Quantity, UnitPrice    float64
		}
	}
	Events []map[string]any
	Errors []struct {
		Row            int
		Field, Message string
	}
}

func TestMappingEngine(t *testing.T) {
	spec := link.MappingSpec{DocType: "order", Format: "csv", Options: link.MappingOptions{Delimiter: ";"}, Fields: []link.MappingField{
		{Target: "number", Source: "PO Number"}, {Target: "lines.gtin", Source: "GTIN"}, {Target: "lines.quantity", Source: "Qty"},
		{Target: "date", Source: "Order Date", Transforms: []link.Transform{{Op: "trim"}, {Op: "date", From: "DD/MM/YYYY"}}},
	}}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err := link.ApplyMapping(spec, []byte(ordersCSV))
	if err != nil || len(res.Documents) != 2 || len(res.Documents[0].Lines) != 2 || res.Rows != 3 || len(res.Errors) != 0 {
		t.Fatalf("csv: %v %+v", err, res)
	}
	if d := res.Documents[0]; d.Date.Format("2006-01-02") != "2026-10-23" || d.Lines[1].Quantity != 5 || d.Currency != "USD" || d.Lines[0].Line != 1 {
		t.Fatalf("doc: %+v", d)
	}

	// Invalid specs.
	for name, s := range map[string]link.MappingSpec{
		"docType":      {DocType: "x", Format: "csv"},
		"format":       {DocType: "order", Format: "pdf"},
		"flat columns": {DocType: "order", Format: "flat"},
		"flat column":  {DocType: "order", Format: "flat", Options: link.MappingOptions{Columns: []link.FlatColumn{{Name: "a"}}}},
		"delimiter":    {DocType: "order", Format: "csv", Options: link.MappingOptions{Delimiter: ";;"}},
		"no fields":    {DocType: "order", Format: "csv"},
		"target":       {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "nope", Source: "a"}}},
		"twice":        {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "number", Source: "a"}, {Target: "number", Source: "b"}}},
		"no source":    {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "number"}}},
		"no number":    {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "lines.gtin", Source: "a"}}},
		"op":           {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "number", Source: "a", Transforms: []link.Transform{{Op: "x"}}}}},
		"date":         {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "number", Source: "a", Transforms: []link.Transform{{Op: "date"}}}}},
		"lookup":       {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "number", Source: "a", Transforms: []link.Transform{{Op: "lookup"}}}}},
		"concat":       {DocType: "order", Format: "csv", Fields: []link.MappingField{{Target: "number", Source: "a", Transforms: []link.Transform{{Op: "concat"}}}}},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: invalid spec accepted", name)
		}
	}

	// JSON with nested lines, recordsPath and linesPath.
	js := `{"data":{"orders":[{"po":"A1","buyer":{"gln":"9506000000008"},"items":[{"gtin":"09506000134352","qty":2},{"gtin":"09506000134353","qty":3.5}]},
		{"po":"A2","items":[{"gtin":"09506000134352","qty":"x"}]}, {"po":"","items":[{"gtin":"1"}]}]}}`
	jspec := link.MappingSpec{DocType: "asn", Format: "json", Options: link.MappingOptions{RecordsPath: "data.orders", LinesPath: "items"}, Fields: []link.MappingField{
		{Target: "number", Source: "po"}, {Target: "buyer.gln", Source: "buyer.gln"}, {Target: "lines.gtin", Source: "gtin"}, {Target: "lines.quantity", Source: "qty"},
	}}
	res, err = link.ApplyMapping(jspec, []byte(js))
	if err != nil || len(res.Documents) != 2 || len(res.Documents[0].Lines) != 2 || res.Documents[0].Lines[1].Quantity != 3.5 || res.Documents[0].Buyer.GLN != "9506000000008" {
		t.Fatalf("json: %v %+v", err, res)
	}
	if len(res.Errors) != 3 || res.Errors[0].Row != 2 || res.Errors[0].Field != "lines.quantity" || res.Errors[1].Field != "number" || !strings.Contains(res.Errors[2].Message, "at least one line") {
		t.Fatalf("json errors: %+v", res.Errors)
	}
	if !strings.Contains(strings.Join(res.Columns, ","), "buyer.gln") {
		t.Fatalf("json columns: %v", res.Columns)
	}
	// A single JSON object is one record; missing recordsPath is an error; bad JSON is an error.
	if res, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "json", Fields: jspec.Fields[:1]}, []byte(`{"po":"X","list":[1,true,null]}`)); err != nil || res.Rows != 1 {
		t.Fatalf("single object: %v %+v", err, res)
	}
	if _, err := link.ApplyMapping(jspec, []byte(`{"x":1}`)); err == nil {
		t.Fatal("missing recordsPath accepted")
	}
	if _, err := link.ApplyMapping(jspec, []byte(`{`)); err == nil {
		t.Fatal("bad JSON accepted")
	}

	// Fixed-width file.
	flat := "HEADER LINE\nPO-9      09506000134352   12\n\nPO-9      09506000134353    1\nPO-X\n"
	fspec := link.MappingSpec{DocType: "order", Format: "flat", Options: link.MappingOptions{SkipLines: 1, Columns: []link.FlatColumn{
		{Name: "po", Start: 1, Length: 10}, {Name: "gtin", Start: 11, Length: 14}, {Name: "qty", Start: 25, Length: 5}}},
		Fields: []link.MappingField{{Target: "number", Source: "po", Transforms: []link.Transform{{Op: "trim"}}}, {Target: "lines.gtin", Source: "gtin"},
			{Target: "lines.quantity", Source: "qty"}}}
	res, err = link.ApplyMapping(fspec, []byte(flat))
	if err != nil || len(res.Documents) != 2 || len(res.Documents[0].Lines) != 2 || res.Documents[0].Lines[0].Quantity != 12 {
		t.Fatalf("flat: %v %+v", err, res)
	}

	// CSV without header, tab-separated; transform errors are reported per row.
	tsv := "A\t2026-13-45\tx\nB\t2026-01-02\t09506000134352\n"
	tspec := link.MappingSpec{DocType: "order", Format: "csv", Options: link.MappingOptions{NoHeader: true, Delimiter: "tab"}, Fields: []link.MappingField{
		{Target: "number", Source: "col1"}, {Target: "date", Source: "col2", Transforms: []link.Transform{{Op: "date", From: "YYYY-MM-DD"}}},
		{Target: "lines.gtin", Source: "col3", Transforms: []link.Transform{{Op: "lookup", Table: map[string]string{"09506000134352": "09506000134352"}}}},
	}}
	res, err = link.ApplyMapping(tspec, []byte(tsv))
	if err != nil || len(res.Errors) != 2 || res.Errors[0].Field != "date" || !strings.Contains(res.Errors[1].Message, "no lookup entry") || len(res.Documents) != 1 {
		t.Fatalf("tsv: %v %+v", err, res)
	}
	if _, err := link.ApplyMapping(tspec, []byte("a\t\"b\n")); err == nil {
		t.Fatal("broken CSV accepted")
	}

	// EPCIS events.
	ev := "time,step,epcs,loc,class,count,po\n2026-10-20T10:00:00+02:00,shipping,urn:epc:id:sgtin:9506000.134352.1;urn:epc:id:sgtin:9506000.134352.2,urn:epc:id:sgln:9506000.00000.0,urn:epc:class:lgtin:9506000.134352.L1,4,urn:epcglobal:cbv:bt:9506000000008:PO1\nnot-a-date,shipping,,,,,\n2026-10-20,shipping,urn:epc:id:sgtin:9506000.134352.3,,c,x,\n"
	espec := link.MappingSpec{DocType: "epcis", Format: "csv", Fields: []link.MappingField{
		{Target: "eventTime", Source: "time"}, {Target: "bizStep", Source: "step"}, {Target: "epcList", Source: "epcs"}, {Target: "readPoint", Source: "loc"},
		{Target: "bizLocation", Source: "loc"}, {Target: "quantity.epcClass", Source: "class"}, {Target: "quantity.quantity", Source: "count"},
		{Target: "bizTransaction.id", Source: "po"}, {Target: "action", Transforms: []link.Transform{{Op: "constant", Value: "OBSERVE"}}},
	}}
	if err := espec.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err = link.ApplyMapping(espec, []byte(ev))
	if err != nil || len(res.Events) != 1 || len(res.Events[0].EPCList) != 2 || res.Events[0].EventTimeZoneOffset != "+02:00" || res.Events[0].ReadPoint == nil ||
		len(res.Events[0].QuantityList) != 1 || len(res.Events[0].BizTransactionList) != 1 {
		t.Fatalf("epcis: %v %+v", err, res)
	}
	if len(res.Errors) != 2 || res.Errors[0].Field != "eventTime" || res.Errors[1].Field != "quantity.quantity" {
		t.Fatalf("epcis errors: %+v", res.Errors)
	}

	// Suggestions from header names.
	sug := link.SuggestMapping("order", []string{"PO Number", "Order Date", "GTIN", "Qty", "Unit Price", "Batch", "Expiry Date", "Something"})
	got := map[string]string{}
	for _, f := range sug {
		got[f.Target] = f.Source
	}
	if got["number"] != "PO Number" || got["date"] != "Order Date" || got["lines.quantity"] != "Qty" || got["lines.unitPrice"] != "Unit Price" || got["lines.lot"] != "Batch" || len(sug) != 7 {
		t.Fatalf("suggest: %+v", got)
	}
	if s := link.SuggestMapping("epcis", []string{"items.0.epc", "eventTime"}); len(s) != 2 {
		t.Fatalf("suggest epcis: %+v", s)
	}
	if len(link.MappingTargets("epcis")) == 0 || len(link.MappingTargets("order")) < 20 {
		t.Fatal("targets")
	}
}

func TestMappingStudioAPI(t *testing.T) {
	e := testkit.New(t)
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Acme Pharma", "kind": "wholesaler"}), &p)
	in := orderMapping()
	in["partnerId"] = p.ID

	// Validation.
	bad := orderMapping()
	bad["name"] = " "
	problem(t, e.Do("POST", "/api/v1/b2b/mappings", bad), 400, "name is required")
	bad = orderMapping()
	bad["fields"] = []map[string]any{{"target": "nope", "source": "x"}}
	problem(t, e.Do("POST", "/api/v1/b2b/mappings", bad), 400, "unknown target field")
	bad = orderMapping()
	bad["partnerId"] = "00000000-0000-0000-0000-000000000001"
	problem(t, e.Do("POST", "/api/v1/b2b/mappings", bad), 400, "unknown partnerId")
	problem(t, e.Do("POST", "/api/v1/b2b/mappings", "{"), 400, "invalid JSON")

	var mp struct {
		ID, Name, DocType string
		Fields            []map[string]any
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", in), &mp)
	problem(t, e.Do("POST", "/api/v1/b2b/mappings", in), 409, "conflict")

	// Preview a saved mapping and an inline one.
	var res mapResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": ordersCSV}), &res)
	if len(res.Documents) != 2 || res.Rows != 3 || len(res.Errors) != 0 || len(res.Sample) != 3 || len(res.Columns) != 9 {
		t.Fatalf("preview: %+v", res)
	}
	d := res.Documents[0]
	if d.Number != "PO-100" || d.Date[:10] != "2026-10-23" || d.Buyer.Name != "ACME" || d.Seller.Name != "Our Company" || d.Currency != "EUR" ||
		d.References["contract"] != "acme-9506000000008" || d.Lines[0].Lot != "l1" || d.Lines[0].Expiry != "2027-12-31" || d.Lines[0].UOM != "EA" {
		t.Fatalf("mapped document: %+v", d)
	}
	if res.Documents[1].Currency != "USD" {
		t.Fatalf("lookup default: %+v", res.Documents[1])
	}
	broken := strings.Replace(ordersCSV, ";10;", ";ten;", 1)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": in, "contentBase64": base64.StdEncoding.EncodeToString([]byte(broken))}), &res)
	if len(res.Errors) != 1 || res.Errors[0].Row != 1 || res.Errors[0].Field != "lines.quantity" || !strings.Contains(res.Errors[0].Message, "not a number") {
		t.Fatalf("preview errors: %+v", res.Errors)
	}
	// A preview before any field is mapped returns columns and sample rows only.
	res = mapResult{}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": map[string]any{"docType": "order", "format": "csv", "options": map[string]any{"delimiter": ";"}}, "content": ordersCSV}), &res)
	if len(res.Columns) != 9 || len(res.Documents) != 0 {
		t.Fatalf("unmapped preview: %+v", res)
	}
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": map[string]any{"format": "pdf"}, "content": "x"}), 400, "format must be")
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": map[string]any{"docType": "order", "format": "csv", "fields": []map[string]any{{"target": "x", "source": "y"}}}, "content": "x"}), 400, "unknown target")
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"content": "x"}), 400, "mappingId or mapping")
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID}), 400, "content is required")
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "contentBase64": "%%"}), 400, "base64")
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": "00000000-0000-0000-0000-000000000001", "content": "x"}), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": map[string]any{"docType": "order", "format": "json"}, "content": "{"}), 400, "cannot read JSON")

	// Suggest.
	var sug struct {
		Fields  []struct{ Target, Source string }
		Targets []string
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/suggest", map[string]any{"docType": "order", "columns": res.Columns}), &sug)
	if len(sug.Fields) < 5 || len(sug.Targets) == 0 {
		t.Fatalf("suggest: %+v", sug)
	}
	problem(t, e.Do("POST", "/api/v1/b2b/mappings/suggest", map[string]any{"docType": "nope"}), 400, "unknown docType")

	// List, get, update, filters.
	var list struct{ Items []struct{ ID string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/mappings?partnerId="+p.ID+"&docType=order", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("list: %+v", list)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/mappings?docType=asn", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("filtered list: %+v", list)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/mappings/"+mp.ID, nil), &mp)
	in["name"] = "Acme orders v2"
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/b2b/mappings/"+mp.ID, in), &mp)
	if mp.Name != "Acme orders v2" {
		t.Fatalf("update: %+v", mp)
	}
	problem(t, e.Do("PUT", "/api/v1/b2b/mappings/00000000-0000-0000-0000-000000000001", in), 404, "not found")
	problem(t, e.Do("PUT", "/api/v1/b2b/mappings/"+mp.ID, bad), 400, "unknown")
	problem(t, e.Do("PUT", "/api/v1/b2b/mappings/x", in), 400, "invalid id")
	problem(t, e.Do("PUT", "/api/v1/b2b/mappings/"+mp.ID, "{"), 400, "invalid JSON")

	// Apply on inbound: errors store nothing; a clean file stores every document.
	r := e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "csv", "mappingId": mp.ID, "content": broken})
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "lines.quantity") {
		t.Fatalf("import with errors: %d %s", r.Status, r.Body)
	}
	var imp struct {
		Documents []struct{ Number, Direction, Status string }
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "csv", "partnerId": p.ID, "content": ordersCSV}), &imp)
	if len(imp.Documents) != 2 || imp.Documents[0].Direction != "inbound" || imp.Documents[0].Status != "received" {
		t.Fatalf("import: %+v", imp)
	}
	var docs struct{ Items []doc }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents?direction=inbound", nil), &docs)
	if len(docs.Items) != 2 || docs.Items[0].PartnerID == nil || *docs.Items[0].PartnerID != p.ID {
		t.Fatalf("stored inbound: %+v", docs.Items)
	}
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "json", "partnerId": p.ID, "content": "[]"}), 400, "exactly one saved json mapping")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "csv", "content": "a"}), 400, "mappingId or partnerId")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "csv", "mappingId": "00000000-0000-0000-0000-000000000001", "content": "a"}), 400, "unknown mappingId")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "csv", "mappingId": mp.ID, "content": "PO Number\n"}), 400, "no rows")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "csv", "mappingId": mp.ID}), 400, "content is required")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "csv", "mappingId": mp.ID, "content": "a\n\"b"}), 400, "cannot read CSV")

	// EPCIS mapping captures events into the repository.
	var em struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", map[string]any{"name": "Scans", "docType": "epcis", "format": "json", "fields": []map[string]any{
		{"target": "eventTime", "source": "at"}, {"target": "bizStep", "source": "step"}, {"target": "epcList", "source": "epc"},
		{"target": "action", "transforms": []map[string]any{{"op": "constant", "value": "OBSERVE"}}},
	}}), &em)
	var cap struct {
		CaptureID string
		Events    int
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "mapped", "mappingId": em.ID,
		"content": `[{"at":"2026-10-20T10:00:00Z","step":"receiving","epc":"urn:epc:id:sscc:9506000.0000000017"}]`}), &cap)
	if cap.CaptureID == "" || cap.Events != 1 {
		t.Fatalf("epcis import: %+v", cap)
	}
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "json", "mappingId": em.ID, "content": `[]`}), 400, "no rows")

	// Viewer reads but cannot write; another tenant sees nothing.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/b2b/mappings", nil), nil)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/b2b/mappings"}, {"PUT", "/api/v1/b2b/mappings/" + mp.ID}, {"DELETE", "/api/v1/b2b/mappings/" + mp.ID},
		{"POST", "/api/v1/b2b/mappings/preview"},
	} {
		problem(t, e.Call(viewer, c.method, c.path, in), 403, "missing role")
	}
	_, other := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/b2b/mappings", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("tenant isolation: %+v", list)
	}
	problem(t, e.Call(other, "GET", "/api/v1/b2b/mappings/"+mp.ID, nil), 404, "not found")
	problem(t, e.Call(other, "DELETE", "/api/v1/b2b/mappings/"+mp.ID, nil), 404, "not found")

	// Delete and audit trail.
	r = e.Do("DELETE", "/api/v1/b2b/mappings/"+mp.ID, nil)
	if r.Status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	problem(t, e.Do("DELETE", "/api/v1/b2b/mappings/x", nil), 400, "invalid id")
	problem(t, e.Do("GET", "/api/v1/b2b/mappings/x", nil), 400, "invalid id")
	for _, a := range []string{"b2b.mapping.create", "b2b.mapping.update", "b2b.mapping.delete", "b2b.mapping.apply"} {
		var aud struct{ Items []any }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action="+a, nil), &aud)
		if len(aud.Items) == 0 {
			t.Errorf("no audit entry %s", a)
		}
	}
}
