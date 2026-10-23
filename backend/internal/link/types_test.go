package link_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/edi"
)

type typeInfo struct {
	Key, Name, Category, Flow string
	Standards                 struct{ X12, EDIFACT, EdifactBgm string }
	Formats                   struct{ X12Write, X12Read, EDIFACTWrite, EDIFACTRead, IDocWrite bool }
	Required                  []string
	Fields                    []string
}

type typeList struct {
	Items      []typeInfo
	Total      int
	Categories []struct {
		Key   string
		Count int
	}
}

func forecastDoc(number string) map[string]any {
	return map[string]any{"type": "forecast", "payload": map[string]any{
		"number": number, "buyer": map[string]string{"name": "Globex", "gln": "0614141000005"}, "seller": map[string]string{"name": "Acme", "gln": "9506000000008"},
		"dates": map[string]string{"period_start": "2026-11-01", "period_end": "2026-11-30"},
		"lines": []map[string]any{
			{"gtin": "09506000134352", "quantity": 100, "uom": "EA", "status": "firm", "dates": map[string]string{"delivery_requested": "2026-11-10"}},
			{"gtin": "09506000134352", "quantity": 80, "uom": "EA", "status": "planning", "dates": map[string]string{"delivery_requested": "2026-11-24"}}}}}
}

func orderAckDoc(number string) map[string]any {
	return map[string]any{"type": "order_ack", "payload": map[string]any{
		"number": number, "status": "changed", "currency": "EUR", "references": map[string]string{"po": "PO-4711"},
		"buyer": map[string]string{"name": "Globex", "gln": "0614141000005"}, "seller": map[string]string{"name": "Acme", "gln": "9506000000008"},
		"lines": []map[string]any{{"gtin": "09506000134352", "quantity": 90, "uom": "EA", "unitPrice": 3.25, "status": "changed"}}}}
}

func TestTransactionTypeRegistryEndpoint(t *testing.T) {
	e := testkit.New(t)
	var all typeList
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/types", nil), &all)
	if all.Total != len(edi.Types) || all.Total < 40 || len(all.Categories) != len(edi.Categories) {
		t.Fatalf("types: total %d, %d categories", all.Total, len(all.Categories))
	}
	got := map[string]typeInfo{}
	for _, ti := range all.Items {
		got[ti.Key] = ti
	}
	fc := got["forecast"]
	if fc.Name == "" || fc.Category != "planning" || fc.Standards.X12 != "830" || fc.Standards.EDIFACT != "DELFOR" || !fc.Formats.X12Write || !fc.Formats.X12Read || !fc.Formats.EDIFACTWrite || fc.Formats.IDocWrite {
		t.Fatalf("forecast: %+v", fc)
	}
	if o := got["order"]; o.Standards.X12 != "850" || o.Standards.EDIFACT != "ORDERS" || o.Standards.EdifactBgm != "220" || !o.Formats.IDocWrite {
		t.Fatalf("order: %+v", o)
	}
	if c := got["grocery_order"]; c.Formats.X12Write || c.Formats.EDIFACTWrite || c.Standards.X12 != "875" {
		t.Fatalf("grocery_order is catalogued only: %+v", c)
	}
	for _, f := range []string{"dates.period_start", "lines.status", "lines.dates.delivery_requested"} {
		if !contains(fc.Fields, f) {
			t.Errorf("forecast fields lack %s", f)
		}
	}
	total := 0
	for _, c := range all.Categories {
		total += c.Count
		if c.Count == 0 {
			t.Errorf("category %s is empty", c.Key)
		}
	}
	if total != all.Total {
		t.Errorf("category counts add up to %d, want %d", total, all.Total)
	}

	var ord typeList
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/types?category=ordering", nil), &ord)
	if ord.Total == 0 || ord.Total >= all.Total {
		t.Fatalf("ordering filter: %d", ord.Total)
	}
	for _, ti := range ord.Items {
		if ti.Category != "ordering" {
			t.Errorf("filter let %s through", ti.Key)
		}
	}
	var q typeList
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/types?q=830", nil), &q)
	if q.Total != 1 || q.Items[0].Key != "forecast" {
		t.Fatalf("q=830: %+v", q.Items)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/types?q=orders", nil), &q)
	if q.Total == 0 {
		t.Fatal("q matches the EDIFACT message")
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/types?q=zzzz", nil), &q)
	if q.Total != 0 || q.Items == nil {
		t.Fatalf("no match is an empty list: %+v", q)
	}
	// A viewer can read the registry.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "v@acme.test", "roles": []string{"Viewer"}, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "v@acme.test", "password": testkit.Password}), &tok)
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/b2b/types", nil), nil)
	e.Must(http.StatusForbidden, e.Call(tok.Token, "POST", "/api/v1/b2b/documents", forecastDoc("FC-V")), nil)
	e.Must(http.StatusUnauthorized, e.Call("", "GET", "/api/v1/b2b/types", nil), nil)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestDocumentsOfAnyRegistryType(t *testing.T) {
	e := testkit.New(t)
	var fc, ack struct {
		ID, Type, Number, Status, Direction string
		Total                               *float64
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", forecastDoc("FC-1")), &fc)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", orderAckDoc("ACK-1")), &ack)
	if fc.Type != "forecast" || fc.Status != "draft" || ack.Type != "order_ack" || ack.Total == nil || *ack.Total != 292.5 {
		t.Fatalf("created: %+v %+v", fc, ack)
	}
	// Stored payloads keep the registry fields.
	var back struct {
		Payload struct {
			Status string
			Dates  map[string]string
			Lines  []struct {
				Status string
				Dates  map[string]string
			}
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents/"+fc.ID, nil), &back)
	if back.Payload.Dates["period_end"] != "2026-11-30" || back.Payload.Lines[1].Status != "planning" || back.Payload.Lines[0].Dates["delivery_requested"] != "2026-11-10" {
		t.Fatalf("payload: %+v", back.Payload)
	}

	// Validation against the type's required fields and rules.
	bad := func(name string, mut func(p map[string]any), want string) {
		t.Helper()
		d := forecastDoc("FC-BAD")
		mut(d["payload"].(map[string]any))
		r := e.Do("POST", "/api/v1/b2b/documents", d)
		if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), want) {
			t.Errorf("%s: HTTP %d %s, want %q", name, r.Status, r.Body, want)
		}
	}
	bad("no period", func(p map[string]any) { delete(p, "dates") }, "dates.period_start is required")
	bad("reversed period", func(p map[string]any) {
		p["dates"] = map[string]string{"period_start": "2026-12-01", "period_end": "2026-11-01"}
	}, "must not be after")
	bad("bad status", func(p map[string]any) { p["lines"].([]map[string]any)[0]["status"] = "maybe" }, `is not allowed for forecast (allowed: firm, planning)`)
	bad("zero quantity", func(p map[string]any) { p["lines"].([]map[string]any)[0]["quantity"] = 0 }, "quantity must be greater than zero")
	bad("no lines", func(p map[string]any) { p["lines"] = []any{} }, "at least one line is required")
	bad("unknown date", func(p map[string]any) {
		p["dates"] = map[string]string{"period_start": "2026-11-01", "period_end": "2026-11-30", "moon": "2026-11-02"}
	}, `is not allowed for forecast (allowed: period_start, period_end)`)
	if r := e.Do("POST", "/api/v1/b2b/documents", map[string]any{"type": "teleport", "payload": map[string]any{"number": "X", "lines": []any{}}}); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "unknown document type") {
		t.Errorf("unknown type: %d %s", r.Status, r.Body)
	}
	d := forecastDoc("FC-MIX")
	d["type"] = "order"
	d["payload"].(map[string]any)["type"] = "forecast"
	if r := e.Do("POST", "/api/v1/b2b/documents", d); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "does not match") {
		t.Errorf("type mismatch: %d %s", r.Status, r.Body)
	}
	// The six original types keep working as before.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order("", "PO-OLD")), nil)

	// Renderings: only where the type has a writer.
	x12 := e.Do("GET", "/api/v1/b2b/documents/"+fc.ID+"/edi?format=x12", nil)
	if x12.Status != 200 || !strings.Contains(string(x12.Body), "ST*830*0001") || !strings.Contains(string(x12.Body), "FST*100*C*D*20261110") {
		t.Fatalf("830: %d %s", x12.Status, x12.Body)
	}
	edf := e.Do("GET", "/api/v1/b2b/documents/"+ack.ID+"/edi?format=edifact", nil)
	if edf.Status != 200 || !strings.Contains(string(edf.Body), "UNH+1+ORDRSP:D:96A") || !strings.Contains(string(edf.Body), "BGM+231+ACK-1+4") {
		t.Fatalf("ORDRSP: %d %s", edf.Status, edf.Body)
	}
	if r := e.Do("GET", "/api/v1/b2b/documents/"+ack.ID+"/edi?format=idoc", nil); r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "no idoc rendering") {
		t.Errorf("IDoc for a type without mapping: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/v1/b2b/documents/"+ack.ID+"/edi?format=pdf", nil); r.Status != http.StatusBadRequest {
		t.Errorf("unknown format: %d", r.Status)
	}
	var cd struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", map[string]any{"type": "grocery_order", "payload": map[string]any{
		"number": "CD-1", "lines": []map[string]any{{"gtin": "09506000134352", "quantity": 1}}}}), &cd)
	for _, f := range []string{"x12", "edifact", "idoc"} {
		if r := e.Do("GET", "/api/v1/b2b/documents/"+cd.ID+"/edi?format="+f, nil); r.Status != http.StatusBadRequest {
			t.Errorf("catalogued type rendered as %s: %d", f, r.Status)
		}
	}

	// Drafts are not messages: the log stays empty until a document is sent.
	var log logPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?kind=b2b", nil), &log)
	if len(log.Items) != 0 {
		t.Errorf("message log: %+v", log.Items)
	}
}

func TestImportEveryReadableFormat(t *testing.T) {
	e := testkit.New(t)
	ack := edi.Document{Type: "order_ack", Number: "ACK-7", Date: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Currency: "EUR", Status: "accepted",
		References: map[string]string{"po": "PO-7"}, Buyer: edi.Party{Name: "Globex", GLN: "0614141000005"}, Seller: edi.Party{Name: "Acme", GLN: "9506000000008"},
		Lines: []edi.Line{{GTIN: "09506000134352", Quantity: 5, UOM: "EA", UnitPrice: 2, Status: "accepted"}}}
	if err := ack.Validate(); err != nil {
		t.Fatal(err)
	}
	var in struct{ ID, Type, Number, Direction string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "content": edi.X12(ack, edi.Envelope{})}), &in)
	if in.Type != "order_ack" || in.Number != "ACK-7" || in.Direction != "inbound" {
		t.Fatalf("X12 855 import: %+v", in)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "edifact", "content": edi.EDIFACT(ack, edi.Envelope{})}), &in)
	if in.Type != "order_ack" {
		t.Fatalf("EDIFACT ORDRSP import: %+v", in)
	}
	// ORDRSP is shared by order_ack and order_change_ack: the caller names the type.
	chg := edi.Document{Type: "order_change_ack", Number: "CA-1", Date: ack.Date, Status: "accepted", References: map[string]string{"po": "PO-7"},
		Lines: []edi.Line{{GTIN: "09506000134352", Quantity: 5, UOM: "EA", Status: "deleted"}}}
	if err := chg.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "edifact", "type": "order_change_ack", "content": edi.EDIFACT(chg, edi.Envelope{})}), &in)
	if in.Type != "order_change_ack" || in.Number != "CA-1" {
		t.Fatalf("hinted import: %+v", in)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "edifact", "content": edi.EDIFACT(chg, edi.Envelope{})}), &in)
	if in.Type != "order_change_ack" {
		t.Fatalf("an unhinted import falls through to the type that validates: %+v", in)
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "edifact", "type": "asn", "content": edi.EDIFACT(chg, edi.Envelope{})}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "content": "ISA*00*"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "edifact", "content": "UNH+1+ZZZZZZ:D:96A:UN'"}), nil)
	// The original 850 import is unchanged.
	po := edi.Document{Type: "order", Number: "PO-OLD", Date: ack.Date, Buyer: ack.Buyer, Seller: ack.Seller, Lines: []edi.Line{{GTIN: "09506000134352", Quantity: 5, UnitPrice: 2}}}
	_ = po.Validate()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "content": edi.X12(po, edi.Envelope{})}), &in)
	if in.Type != "order" {
		t.Fatalf("850 import: %+v", in)
	}
}

func TestMappingTargetsComeFromTheRegistry(t *testing.T) {
	for _, ty := range edi.Types {
		got := link.MappingTargets(ty.Key)
		want := ty.Targets()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: mapping targets differ from the registry", ty.Key)
		}
	}
	if link.MappingTargets("nope") != nil {
		t.Error("unknown type has no targets")
	}
	remit := link.MappingTargets("payment_remittance")
	if !contains(remit, "lines.ref") || !contains(remit, "amounts.payment") || contains(remit, "lines.gtin") {
		t.Errorf("payment_remittance targets: %v", remit)
	}

	e := testkit.New(t)
	var sug struct {
		Fields  []struct{ Target, Source string }
		Targets []string
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/suggest", map[string]any{"docType": "forecast", "columns": []string{"PO Number", "GTIN", "Qty"}}), &sug)
	if !contains(sug.Targets, "dates.period_start") || !contains(sug.Targets, "lines.status") || len(sug.Fields) != 3 {
		t.Fatalf("suggest: %+v", sug)
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/b2b/mappings/suggest", map[string]any{"docType": "teleport", "columns": []string{"a"}}), nil)
}

const forecastCSV = "Forecast;Start;End;Customer;Buyer GLN;GTIN;Qty;Week;Kind\n" +
	"FC-100;2026-11-01;2026-11-30;Globex;0614141000005;09506000134352;100;2026-11-10;firm\n" +
	"FC-100;2026-11-01;2026-11-30;Globex;0614141000005;09506000134352;80;2026-11-24;planning\n"

func forecastMapping() map[string]any {
	f := func(target, source string) map[string]any { return map[string]any{"target": target, "source": source} }
	return map[string]any{"name": "Globex forecasts", "docType": "forecast", "format": "csv", "options": map[string]any{"delimiter": ";"},
		"fields": []map[string]any{f("number", "Forecast"), f("dates.period_start", "Start"), f("dates.period_end", "End"), f("buyer.name", "Customer"), f("buyer.gln", "Buyer GLN"),
			f("lines.gtin", "GTIN"), f("lines.quantity", "Qty"), f("lines.dates.delivery_requested", "Week"), f("lines.status", "Kind"), f("note", "Customer"),
			{"target": "parties.carrier.name", "transforms": []map[string]any{{"op": "constant", "value": "Fast Freight"}}}}}
}

func TestMappingStudioWithRegistryTypes(t *testing.T) {
	e := testkit.New(t)
	in := forecastMapping()
	// carrier is not a party of a forecast: the mapping is rejected with the registry's field list.
	r := e.Do("POST", "/api/v1/b2b/mappings", in)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "parties.carrier.name") {
		t.Fatalf("a target outside the type: %d %s", r.Status, r.Body)
	}
	fields := in["fields"].([]map[string]any)
	in["fields"] = fields[:len(fields)-1]
	var mp struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", in), &mp)

	var res mapResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": forecastCSV}), &res)
	if len(res.Errors) != 0 || len(res.Documents) != 1 || len(res.Documents[0].Lines) != 2 {
		t.Fatalf("preview: %+v", res)
	}
	var doc struct {
		Type  string
		Dates map[string]string
		Note  string
		Lines []struct {
			Status string
			Dates  map[string]string
		}
	}
	var raw struct{ Documents []json.RawMessage }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": forecastCSV}), &raw)
	if err := json.Unmarshal(raw.Documents[0], &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Type != "forecast" || doc.Dates["period_end"] != "2026-11-30" || doc.Note != "Globex" || doc.Lines[1].Status != "planning" || doc.Lines[1].Dates["delivery_requested"] != "2026-11-24" {
		t.Fatalf("mapped document: %+v", doc)
	}

	// Importing through the mapping stores a validated forecast.
	var imp struct {
		Documents []struct{ Type, Number string }
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "mapped", "mappingId": mp.ID, "content": forecastCSV}), &imp)
	if len(imp.Documents) != 1 || imp.Documents[0].Type != "forecast" || imp.Documents[0].Number != "FC-100" {
		t.Fatalf("mapped import: %+v", imp)
	}

	// Validation errors of the registry type show up as row errors.
	broken := strings.Replace(forecastCSV, "firm", "sure", 1)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": broken}), &res)
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, `"sure" is not allowed`) {
		t.Fatalf("expected a status error: %+v", res.Errors)
	}
	badDate := strings.Replace(forecastCSV, "2026-11-30", "30/11/2026", 1)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": badDate}), &res)
	if len(res.Errors) == 0 || res.Errors[0].Field != "dates.period_end" || !strings.Contains(res.Errors[0].Message, "YYYY-MM-DD") {
		t.Fatalf("expected a date error: %+v", res.Errors)
	}
}

func TestReferenceLineMappingAndAmounts(t *testing.T) {
	e := testkit.New(t)
	f := func(target, source string) map[string]any { return map[string]any{"target": target, "source": source} }
	var mp struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", map[string]any{"name": "Remittances", "docType": "payment_remittance", "format": "csv", "options": map[string]any{"delimiter": ","},
		"fields": []map[string]any{f("number", "Advice"), f("amounts.payment", "Paid"), f("dates.effective", "Value date"), f("parties.payer.name", "Payer"), f("parties.payer.gln", "Payer GLN"),
			f("lines.ref", "Invoice"), f("lines.refType", "Type"), f("lines.amount", "Amount")}}), &mp)
	csv := "Advice,Paid,Value date,Payer,Payer GLN,Invoice,Type,Amount\nRA-9,125.5,2026-10-30,Globex,0614141000005,INV-1,invoice,100\nRA-9,125.5,2026-10-30,Globex,0614141000005,INV-2,invoice,25.5\n"
	var imp struct {
		Documents []struct{ ID, Type, Number string }
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "mapped", "mappingId": mp.ID, "content": csv}), &imp)
	if len(imp.Documents) != 1 || imp.Documents[0].Type != "payment_remittance" {
		t.Fatalf("remittance import: %+v", imp)
	}
	var res mapResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": strings.Replace(csv, "125.5", "x", 2)}), &res)
	if len(res.Errors) == 0 || res.Errors[0].Field != "amounts.payment" {
		t.Fatalf("amount error: %+v", res.Errors)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": strings.Replace(csv, ",100\n", ",abc\n", 1)}), &res)
	if len(res.Errors) == 0 || res.Errors[0].Field != "lines.amount" {
		t.Fatalf("line amount error: %+v", res.Errors)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": strings.Replace(csv, "2026-10-30", "30-10-2026", 1)}), &res)
	if len(res.Errors) == 0 {
		t.Fatal("date error expected")
	}
	// Lines dates must be dates too.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", map[string]any{"name": "Fc2", "docType": "forecast", "format": "csv", "options": map[string]any{"delimiter": ","},
		"fields": []map[string]any{f("number", "n"), f("lines.gtin", "g"), f("lines.dates.delivery_requested", "d")}}), &mp)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mappingId": mp.ID, "content": "n,g,d\nF1,09506000134352,soon\n"}), &res)
	if len(res.Errors) == 0 || res.Errors[0].Field != "lines.dates.delivery_requested" {
		t.Fatalf("line date error: %+v", res.Errors)
	}
}

// A document of a new type travels between nodes like the original six.
func TestRegistryTypeOverFederation(t *testing.T) {
	a, b := testkit.New(t), testkit.New(t)
	linkOf(a)
	linkOf(b)
	aToB := peer(t, a, b)
	_ = peer(t, b, a)
	var p struct{ ID string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/partners", map[string]any{"name": "Node B", "kind": "wholesaler", "endpoints": map[string]string{"federation": aToB}}), &p)
	d := forecastDoc("FC-FED")
	d["partnerId"] = p.ID
	var created struct{ ID string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/b2b/documents", d), &created)
	a.Must(http.StatusOK, a.Do("POST", "/api/v1/b2b/documents/"+created.ID+"/send", nil), nil)
	a.Drain()
	b.Drain()
	var inb struct {
		Items []struct{ Number, Type, Direction string }
	}
	b.Must(http.StatusOK, b.Do("GET", "/api/v1/b2b/documents?direction=inbound&type=forecast", nil), &inb)
	if len(inb.Items) != 1 || inb.Items[0].Number != "FC-FED" {
		t.Fatalf("node B: %+v", inb.Items)
	}
	// The message log lists it with its type and the same categories as before.
	var log logPage
	a.Must(http.StatusOK, a.Do("GET", "/api/v1/messages?kind=b2b", nil), &log)
	if len(log.Items) != 1 || log.Items[0].Type != "forecast" || log.Items[0].Kind != "b2b" {
		t.Fatalf("message log: %+v", log.Items)
	}
}
