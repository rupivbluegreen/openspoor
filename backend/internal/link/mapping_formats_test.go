package link_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/edi"
)

func sampleOrderDoc() edi.Document {
	return edi.Document{Type: "order", Number: "PO-4711", Date: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), Currency: "EUR",
		Buyer: edi.Party{Name: "Globex Wholesale", GLN: "0614141000005"}, Seller: edi.Party{Name: "Acme Pharma", GLN: "9506000000008"},
		ShipTo: edi.Party{Name: "Globex DC", GLN: "0614141000012"}, References: map[string]string{"contract": "C-9"},
		Dates: map[string]string{"delivery_requested": "2026-11-02"},
		Lines: []edi.Line{{Line: 1, GTIN: "09506000134352", Description: "Tablets", Quantity: 10, UOM: "EA", UnitPrice: 2.5, Lot: "L1", Expiry: "2028-01-31"},
			{Line: 2, GTIN: "09506000134369", Quantity: 4, UOM: "EA", UnitPrice: 1}}}
}

func has(cols []string, want ...string) bool {
	set := map[string]bool{}
	for _, c := range cols {
		set[c] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func suggestSpec(t *testing.T, docType, format string, o link.MappingOptions, content []byte) link.MappingSpec {
	t.Helper()
	pre, err := link.ApplyMapping(link.MappingSpec{DocType: docType, Format: format, Options: o}, content)
	if err != nil {
		t.Fatal(err)
	}
	spec := link.MappingSpec{DocType: docType, Format: format, Options: o, Fields: link.SuggestMapping(docType, pre.Columns)}
	if err := spec.Validate(); err != nil {
		t.Fatalf("suggested spec: %v (%+v)", err, spec.Fields)
	}
	return spec
}

func checkOrder(t *testing.T, res link.MapResult, d edi.Document) {
	t.Helper()
	if len(res.Errors) != 0 || len(res.Documents) != 1 {
		t.Fatalf("errors %+v documents %d", res.Errors, len(res.Documents))
	}
	got := res.Documents[0]
	if got.Number != d.Number || got.Date.Format("2006-01-02") != "2026-10-23" || got.Buyer.GLN != d.Buyer.GLN || got.Seller.GLN != d.Seller.GLN || got.Dates["delivery_requested"] != "2026-11-02" {
		t.Fatalf("header: %+v", got)
	}
	if len(got.Lines) != 2 || got.Lines[0].GTIN != d.Lines[0].GTIN || got.Lines[0].Quantity != 10 || got.Lines[1].GTIN != d.Lines[1].GTIN || got.Lines[1].Quantity != 4 || got.Lines[0].UOM != "EA" {
		t.Fatalf("lines: %+v", got.Lines)
	}
}

func TestMappingX12Source(t *testing.T) {
	d := sampleOrderDoc()
	content := []byte(edi.X12(d, edi.Envelope{Control: 7}))
	pre, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "x12"}, content)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Rows != 2 || !has(pre.Columns, "BEG.03", "BEG.05", "N1(BY).04", "N1(SE).02", "DTM(002).02", "PO1.02", "PO1.07", "PID.05", "ISA.06", "GS.01") {
		t.Fatalf("rows %d columns %v", pre.Rows, pre.Columns)
	}
	// Line values are relative to the line; the header repeats for every line.
	if pre.Sample[0]["PO1.07"] != "09506000134352" || pre.Sample[1]["PO1.07"] != "09506000134369" || pre.Sample[1]["BEG.03"] != "PO-4711" {
		t.Fatalf("sample %+v", pre.Sample)
	}
	spec := suggestSpec(t, "order", "x12", link.MappingOptions{}, content)
	res, err := link.ApplyMapping(spec, content)
	if err != nil {
		t.Fatal(err)
	}
	checkOrder(t, res, d)
	if res.Documents[0].Lines[0].Description != "Tablets" {
		t.Errorf("description: %+v", res.Documents[0].Lines[0])
	}
}

func TestMappingEDIFACTSource(t *testing.T) {
	d := sampleOrderDoc()
	content := []byte(edi.EDIFACT(d, edi.Envelope{Control: 7}))
	pre, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "edifact"}, content)
	if err != nil {
		t.Fatal(err)
	}
	if !has(pre.Columns, "BGM.02", "DTM(137).01.02", "DTM(137).01", "NAD(BY).02.01", "NAD(SU).04", "LIN.03.01", "LIN.03.02", "QTY.01.02", "QTY.01.03", "PRI.01.02", "UNB.02.01", "CNT.01.02") {
		t.Fatalf("columns %v", pre.Columns)
	}
	spec := suggestSpec(t, "order", "edifact", link.MappingOptions{}, content)
	res, err := link.ApplyMapping(spec, content)
	if err != nil {
		t.Fatal(err)
	}
	checkOrder(t, res, d)
	if res.Documents[0].Lines[0].Lot != "L1" || res.Documents[0].Lines[0].Expiry != "2028-01-31" || res.Documents[0].Currency != "EUR" {
		t.Errorf("lot/expiry/currency: %+v", res.Documents[0])
	}
	// Header segments after UNS (CNT here) stay in the header and are available to every line.
	if pre.Sample[0]["CNT.01.02"] != "2" || pre.Sample[1]["CNT.01.02"] != "2" {
		t.Errorf("summary values missing from the header: %+v", pre.Sample[0])
	}
}

func TestMappingEDIMultipleMessagesAndLineSegment(t *testing.T) {
	a, b := sampleOrderDoc(), sampleOrderDoc()
	b.Number = "PO-4712"
	// Two EDIFACT messages in one interchange: take the second message out of a second file.
	one, two := edi.EDIFACT(a, edi.Envelope{Control: 1}), edi.EDIFACT(b, edi.Envelope{Control: 2})
	msgB := two[strings.Index(two, "UNH+"):strings.Index(two, "UNZ+")]
	combined := strings.Replace(one, "UNZ+1+1", msgB+"UNZ+2+1", 1)
	spec := link.MappingSpec{DocType: "order", Format: "edifact", Fields: []link.MappingField{
		{Target: "number", Source: "BGM.02"}, {Target: "lines.gtin", Source: "LIN.03.01"}, {Target: "lines.quantity", Source: "QTY.01.02"},
	}}
	res, err := link.ApplyMapping(spec, []byte(combined))
	if err != nil || len(res.Documents) != 2 || res.Documents[1].Number != "PO-4712" || len(res.Documents[1].Lines) != 2 || len(res.Errors) != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	// An X12 810 opens its lines at IT1; naming the segment explicitly gives the same.
	inv := edi.Document{Type: "invoice", Number: "INV-1", Date: a.Date, Currency: "EUR", Buyer: a.Buyer, Seller: a.Seller, References: map[string]string{"po": "PO-4711"}, Lines: a.Lines}
	content := []byte(edi.X12(inv, edi.Envelope{}))
	for _, ls := range []string{"", "IT1"} {
		spec := link.MappingSpec{DocType: "invoice", Format: "x12", Options: link.MappingOptions{LinesPath: ls}, Fields: []link.MappingField{
			{Target: "number", Source: "BIG.02"}, {Target: "references.po", Source: "BIG.04"}, {Target: "lines.gtin", Source: "IT1.07"}, {Target: "lines.quantity", Source: "IT1.02"},
		}}
		res, err := link.ApplyMapping(spec, content)
		if err != nil || len(res.Documents) != 1 || len(res.Documents[0].Lines) != 2 || res.Documents[0].References["po"] != "PO-4711" || len(res.Errors) != 0 {
			t.Fatalf("linesPath %q: %v %+v", ls, err, res)
		}
	}
	// A line segment that does not occur leaves the record as a single row of header values.
	spec.Options.LinesPath = "ZZZ"
	spec.Fields = spec.Fields[:1]
	res, err = link.ApplyMapping(spec, []byte(one))
	if err != nil || res.Rows != 1 || len(res.Documents) != 1 {
		t.Fatalf("no such line segment: %v %+v", err, res)
	}
}

func TestMappingEDISpecValidation(t *testing.T) {
	f := []link.MappingField{{Target: "number", Source: "BGM.02"}}
	for _, ok := range []string{"", "LIN", "PO1"} {
		if err := (&link.MappingSpec{DocType: "order", Format: "edifact", Options: link.MappingOptions{LinesPath: ok}, Fields: f}).Validate(); err != nil {
			t.Errorf("linesPath %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"lin", "LIN.01", "L", "LINEXX"} {
		if err := (&link.MappingSpec{DocType: "order", Format: "x12", Options: link.MappingOptions{LinesPath: bad}, Fields: f}).Validate(); err == nil || !strings.Contains(err.Error(), "segment ID") {
			t.Errorf("linesPath %q accepted: %v", bad, err)
		}
	}
	if err := (&link.MappingSpec{DocType: "order", Format: "xml", Fields: f}).Validate(); err != nil {
		t.Errorf("xml: %v", err)
	}
}

const orderXML = `<?xml version="1.0" encoding="UTF-8"?>
<ns:Orders xmlns:ns="urn:example:orders">
  <!-- two orders -->
  <ns:Order id="A1" currency="EUR">
    <ns:Header><ns:OrderNumber>PO-A1</ns:OrderNumber><ns:OrderDate>2026-10-23</ns:OrderDate><ns:Buyer gln="0614141000005">Globex</ns:Buyer></ns:Header>
    <ns:Lines>
      <ns:Line><ns:GTIN>09506000134352</ns:GTIN><ns:Qty>10</ns:Qty></ns:Line>
      <ns:Line><ns:GTIN>09506000134369</ns:GTIN><ns:Qty>4</ns:Qty></ns:Line>
    </ns:Lines>
  </ns:Order>
  <ns:Order id="A2" currency="USD">
    <ns:Header><ns:OrderNumber>PO-A2</ns:OrderNumber><ns:OrderDate>2026-10-24</ns:OrderDate><ns:Buyer gln="0614141000005">Globex</ns:Buyer></ns:Header>
    <ns:Lines><ns:Line><ns:GTIN>09506000134352</ns:GTIN><ns:Qty>1.5</ns:Qty></ns:Line></ns:Lines>
  </ns:Order>
</ns:Orders>`

func TestMappingXMLSource(t *testing.T) {
	o := link.MappingOptions{RecordsPath: "Order", LinesPath: "Lines.Line"}
	pre, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "xml", Options: o}, []byte(orderXML))
	if err != nil {
		t.Fatal(err)
	}
	if pre.Rows != 3 || !has(pre.Columns, "@id", "@currency", "Header.OrderNumber", "Header.Buyer.@gln", "Header.Buyer.#text", "GTIN", "Qty") {
		t.Fatalf("rows %d columns %v", pre.Rows, pre.Columns)
	}
	spec := link.MappingSpec{DocType: "order", Format: "xml", Options: o, Fields: []link.MappingField{
		{Target: "number", Source: "Header.OrderNumber"}, {Target: "date", Source: "Header.OrderDate", Transforms: []link.Transform{{Op: "date", From: "YYYY-MM-DD"}}},
		{Target: "currency", Source: "@currency"}, {Target: "buyer.gln", Source: "Header.Buyer.@gln"}, {Target: "buyer.name", Source: "Header.Buyer.#text"},
		{Target: "lines.gtin", Source: "GTIN"}, {Target: "lines.quantity", Source: "Qty"},
	}}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err := link.ApplyMapping(spec, []byte(orderXML))
	if err != nil || len(res.Errors) != 0 || len(res.Documents) != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	a, b := res.Documents[0], res.Documents[1]
	if a.Number != "PO-A1" || a.Currency != "EUR" || a.Buyer.Name != "Globex" || a.Buyer.GLN != "0614141000005" || len(a.Lines) != 2 || a.Lines[1].Quantity != 4 {
		t.Fatalf("first: %+v", a)
	}
	// One <Line> is a single object, not an array, and still counts as a line.
	if b.Number != "PO-A2" || b.Currency != "USD" || len(b.Lines) != 1 || b.Lines[0].Quantity != 1.5 {
		t.Fatalf("second: %+v", b)
	}
	// Suggestions work from the XML paths by their last name part.
	sug := link.SuggestMapping("order", pre.Columns)
	got := map[string]string{}
	for _, f := range sug {
		got[f.Target] = f.Source
	}
	if got["number"] != "Header.OrderNumber" || got["lines.gtin"] != "GTIN" || got["lines.quantity"] != "Qty" || got["date"] != "Header.OrderDate" {
		t.Fatalf("suggestions: %+v", got)
	}
}

func TestMappingXMLShapes(t *testing.T) {
	spec := link.MappingSpec{DocType: "order", Format: "xml", Fields: []link.MappingField{{Target: "number", Source: "no"}, {Target: "lines.gtin", Source: "gtin"}, {Target: "lines.quantity", Source: "qty"}}}
	// A single record without recordsPath: the root's children are the values, repeated children are indexed.
	res, err := link.ApplyMapping(spec, []byte(`<order><no>X1</no><gtin>09506000134352</gtin><qty>2</qty></order>`))
	if err != nil || len(res.Documents) != 1 || res.Documents[0].Number != "X1" || len(res.Errors) != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	pre, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "xml"}, []byte(`<o><item><sku>a</sku></item><item><sku>b</sku></item><note>hi</note><empty/></o>`))
	if err != nil || !has(pre.Columns, "item.0.sku", "item.1.sku", "note", "empty") {
		t.Fatalf("%v %v", err, pre.Columns)
	}
	// ISO-8859-1 content and a leaf-only root.
	latin := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><o><no>M\xfcller</no></o>"
	pre, err = link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "xml"}, []byte(latin))
	if err != nil || pre.Sample[0]["no"] != "Müller" {
		t.Fatalf("latin1: %v %+v", err, pre.Sample)
	}
	pre, err = link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "xml"}, []byte(`<only>text</only>`))
	if err != nil || pre.Sample[0]["#text"] != "text" {
		t.Fatalf("leaf root: %v %+v", err, pre.Sample)
	}
}

// Files that cannot be read at all are reported as errors, not mapped.
func TestMappingSourceFailures(t *testing.T) {
	deep := strings.Repeat("<a>", 80) + strings.Repeat("</a>", 80)
	x12 := edi.X12(sampleOrderDoc(), edi.Envelope{})
	for name, c := range map[string]struct {
		format, content, want string
	}{
		"x12 given text":          {"x12", "hello, this is a csv file\na,b", "cannot read X12"},
		"x12 truncated":           {"x12", "ISA*00*", "truncated"},
		"x12 without ST":          {"x12", "ISA*00*          *00*          *ZZ*A              *ZZ*B              *260701*0000*U*00401*000000001*0*P*>~IEA*0*000000001~", "no transaction set"},
		"edifact given x12":       {"edifact", x12, "is an X12 interchange"},
		"x12 given edifact":       {"x12", edi.EDIFACT(sampleOrderDoc(), edi.Envelope{}), "is an EDIFACT interchange"},
		"edifact without message": {"edifact", "UNA:+.? 'UNB+UNOC:3+A+B+260701:0000+1'UNZ+0+1'", "no message"},
		"xml given csv":           {"xml", "a,b\n1,2", "cannot read XML"},
		"xml malformed":           {"xml", "<a><b></a>", "cannot read XML"},
		"xml two roots":           {"xml", "<a/><b/>", "more than one root"},
		"xml empty":               {"xml", "   ", "no root element"},
		"xml too deep":            {"xml", deep, "nested deeper"},
		"xml encoding":            {"xml", `<?xml version="1.0" encoding="UTF-16"?><a/>`, "not supported"},
	} {
		_, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: c.format}, []byte(c.content))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
	// A readable file that maps badly reports the rows.
	bad := strings.Replace(x12, "PO1*1*10*", "PO1*1*ten*", 1)
	spec := suggestSpec(t, "order", "x12", link.MappingOptions{}, []byte(x12))
	res, err := link.ApplyMapping(spec, []byte(bad))
	if err != nil || len(res.Errors) != 1 || res.Errors[0].Field != "lines.quantity" || !strings.Contains(res.Errors[0].Message, "not a number") {
		t.Fatalf("%v %+v", err, res.Errors)
	}
}

func TestMappingFormatsAPI(t *testing.T) {
	e := testkit.New(t)
	d := sampleOrderDoc()
	x12 := edi.X12(d, edi.Envelope{Control: 5})
	pre, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: "x12"}, []byte(x12))
	if err != nil {
		t.Fatal(err)
	}
	fields := link.SuggestMapping("order", pre.Columns)

	// Suggestions over the API from segment paths.
	var sug struct {
		Fields []struct{ Target, Source string }
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/suggest", map[string]any{"docType": "order", "columns": pre.Columns}), &sug)
	if len(sug.Fields) != len(fields) || len(sug.Fields) < 8 {
		t.Fatalf("suggest: %+v", sug)
	}

	// Preview with an inline X12 mapping, then save X12, EDIFACT and XML mappings (the format check allows them).
	var res mapResult
	inline := map[string]any{"docType": "order", "format": "x12", "fields": fields}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": inline, "content": x12}), &res)
	if len(res.Documents) != 1 || res.Documents[0].Number != "PO-4711" || len(res.Documents[0].Lines) != 2 || len(res.Errors) != 0 {
		t.Fatalf("x12 preview: %+v", res)
	}
	res = mapResult{}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": map[string]any{"docType": "order", "format": "x12"}, "content": x12}), &res)
	if len(res.Columns) == 0 || len(res.Documents) != 0 {
		t.Fatalf("unmapped x12 preview: %+v", res)
	}
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Globex", "kind": "wholesaler"}), &p)
	var mx struct{ ID, Format string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", map[string]any{"name": "Globex X12 orders", "partnerId": p.ID, "docType": "order", "format": "x12", "fields": fields}), &mx)
	if mx.Format != "x12" {
		t.Fatalf("saved: %+v", mx)
	}
	efields := link.SuggestMapping("order", mustColumns(t, "edifact", edi.EDIFACT(d, edi.Envelope{})))
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", map[string]any{"name": "Globex EDIFACT orders", "docType": "order", "format": "edifact", "fields": efields}), nil)
	xmap := map[string]any{"name": "Globex XML orders", "docType": "order", "format": "xml", "options": map[string]any{"recordsPath": "Order", "linesPath": "Lines.Line"}, "fields": []map[string]any{
		{"target": "number", "source": "Header.OrderNumber"}, {"target": "lines.gtin", "source": "GTIN"}, {"target": "lines.quantity", "source": "Qty"}}}
	var mxml struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", xmap), &mxml)
	bad := map[string]any{"name": "bad lines", "docType": "order", "format": "x12", "options": map[string]any{"linesPath": "lines"}, "fields": fields}
	problem(t, e.Do("POST", "/api/v1/b2b/mappings", bad), 400, "segment ID")

	// Import through the saved X12 mapping: format x12 plus mappingId maps; without mappingId it is parsed natively.
	var imp struct {
		Documents []struct{ Number, Direction, Status string }
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "mappingId": mx.ID, "partnerId": p.ID, "content": x12}), &imp)
	if len(imp.Documents) != 1 || imp.Documents[0].Number != "PO-4711" || imp.Documents[0].Direction != "inbound" {
		t.Fatalf("mapped x12 import: %+v", imp)
	}
	var native struct{ Number, Direction string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "content": x12}), &native)
	if native.Number != "PO-4711" {
		t.Fatalf("native x12 import: %+v", native)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "xml", "mappingId": mxml.ID, "content": orderXML}), &imp)
	if len(imp.Documents) != 2 || imp.Documents[1].Number != "PO-A2" {
		t.Fatalf("xml import: %+v", imp)
	}
	// By partner: the partner has exactly one X12 mapping.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "mapped", "mappingId": mx.ID, "content": x12}), &imp)

	// Failing imports store nothing and say why.
	var before struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents", nil), &before)
	badX12 := strings.Replace(x12, "PO1*1*10*", "PO1*1*ten*", 1)
	r := e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "mappingId": mx.ID, "content": badX12})
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "lines.quantity") {
		t.Fatalf("failing X12 import: %d %s", r.Status, r.Body)
	}
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "mappingId": mx.ID, "content": "not an interchange"}), 400, "cannot read X12")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "xml", "mappingId": mxml.ID, "content": "<a><b></a>"}), 400, "cannot read XML")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "xml", "mappingId": mxml.ID, "content": "<Orders/>"}), 400, "not found")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "mappingId": mx.ID}), 400, "content is required")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "edifact", "mappingId": mx.ID, "content": edi.EDIFACT(d, edi.Envelope{})}), 400, "is an EDIFACT interchange")
	var after struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents", nil), &after)
	if len(after.Items) != len(before.Items) {
		t.Errorf("failing imports stored documents: %d -> %d", len(before.Items), len(after.Items))
	}

	// Viewer is read-only for the new formats too.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(viewer, "POST", "/api/v1/b2b/mappings/preview", map[string]any{"mapping": inline, "content": x12}), 403, "missing role")
	problem(t, e.Call(viewer, "POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "mappingId": mx.ID, "content": x12}), 403, "missing role")
	var aud struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=b2b.mapping.apply", nil), &aud)
	if len(aud.Items) < 3 {
		t.Errorf("mapping.apply audit entries: %d", len(aud.Items))
	}
}

func mustColumns(t *testing.T, format, content string) []string {
	t.Helper()
	res, err := link.ApplyMapping(link.MappingSpec{DocType: "order", Format: format}, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return res.Columns
}
