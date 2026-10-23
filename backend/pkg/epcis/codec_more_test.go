package epcis

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func TestCanonicalForms(t *testing.T) {
	sgtinURN := "urn:epc:id:sgtin:0614141.107346.1001"
	canon := Canonical(sgtinURN)
	if !strings.HasPrefix(canon, "https://id.gs1.org/01/") || !strings.HasSuffix(canon, "/21/1001") {
		t.Fatalf("sgtin canonical: %s", canon)
	}
	// A brand-domain Digital Link with extra qualifiers maps to the same key.
	brand := strings.Replace(canon, "https://id.gs1.org", "https://brand.example/path", 1) + "?17=270131"
	if got := Canonical(brand); got != canon {
		t.Fatalf("brand DL %s -> %s, want %s", brand, got, canon)
	}
	g, s, lot := Key(canon)
	if g != "10614141073464" || s != "1001" || lot != "" {
		t.Fatalf("Key: %q %q %q", g, s, lot)
	}

	lgtin := Canonical("urn:epc:class:lgtin:0614141.107346.LOT9")
	if g, s, lot := Key(lgtin); g != "10614141073464" || s != "" || lot != "LOT9" {
		t.Fatalf("lgtin %s -> %q %q %q", lgtin, g, s, lot)
	}
	if got := Canonical("https://example.org/01/10614141073464/10/LOT9"); got != lgtin {
		t.Fatalf("lot DL: %s want %s", got, lgtin)
	}

	sscc := Canonical("urn:epc:id:sscc:9506000.0000000017")
	if !strings.HasPrefix(sscc, "https://id.gs1.org/00/") {
		t.Fatalf("sscc: %s", sscc)
	}
	if got := Canonical("https://example.org/00/" + strings.TrimPrefix(sscc, "https://id.gs1.org/00/")); got != sscc {
		t.Fatalf("sscc DL: %s", got)
	}
	if g, s, l := Key(sscc); g != "" || s != "" || l != "" {
		t.Fatalf("Key(sscc) should have no GTIN: %q %q %q", g, s, l)
	}

	gln := Canonical("urn:epc:id:sgln:9506000.13435.0")
	if !strings.HasPrefix(gln, "https://id.gs1.org/414/") || strings.Contains(gln, "/254/") {
		t.Fatalf("sgln: %s", gln)
	}
	glnKey := strings.TrimPrefix(gln, "https://id.gs1.org/414/")
	if got := Canonical("https://example.org/414/" + glnKey + "/254/7"); got != gln+"/254/7" {
		t.Fatalf("gln with extension: %s", got)
	}
	if got := Canonical("https://example.org/414/" + glnKey + "/254/0"); got != gln {
		t.Fatalf("gln with zero extension: %s", got)
	}
	if got := Canonical("https://example.org/01/00614141073467"); got != "https://id.gs1.org/01/00614141073467" {
		t.Fatalf("class DL: %s", got)
	}

	// Things that are not GS1 keys pass through untouched.
	for _, id := range []string{"urn:epc:idpat:sgtin:0614141.107346.*", "urn:example:thing:1", "https://example.org/about", "urn:epc:id:sgtin:bad"} {
		if got := Canonical(id); got != id {
			t.Errorf("Canonical(%q) = %q", id, got)
		}
	}
	if g, s, l := Key("urn:example:x"); g+s+l != "" {
		t.Fatal("Key of non-DL must be empty")
	}

	all := CanonicalAll([]string{sgtinURN, "", brand, "urn:x:y"})
	if len(all) != 2 || all[0] != canon || all[1] != "urn:x:y" {
		t.Fatalf("CanonicalAll: %v", all)
	}
}

func TestCheckEPC(t *testing.T) {
	ok := []string{
		"urn:epc:id:sgtin:0614141.107346.1", "urn:epc:id:sscc:9506000.0000000017", "urn:epc:id:sgln:9506000.13435.0",
		"urn:epc:id:grai:0614141.12345.400", "https://id.gs1.org/01/00614141073467/21/1", "https://example.org/not-a-dl", "urn:example:x",
	}
	for _, id := range ok {
		if msg := CheckEPC(id); msg != "" {
			t.Errorf("CheckEPC(%q) = %q, want ok", id, msg)
		}
	}
	bad := map[string]string{
		"urn:epc:id:sgtin:junk":                     "junk",
		"urn:epc:id:sscc:junk":                      "junk",
		"urn:epc:id:sgln:junk":                      "junk",
		"https://id.gs1.org/01/00614141073468/21/1": "GTIN check digit",
		"https://id.gs1.org/00/095060000000000171":  "SSCC check digit",
		"just-a-string":                             "must be a URI",
	}
	for id, frag := range bad {
		if msg := CheckEPC(id); !strings.Contains(msg, frag) {
			t.Errorf("CheckEPC(%q) = %q, want mention of %q", id, msg, frag)
		}
	}
}

func TestValidateRules(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	epc := []string{"urn:epc:id:sgtin:0614141.107346.1"}
	cases := []struct {
		name   string
		e      Event
		fields []string
	}{
		{"valid object", Event{Type: ObjectEvent, Action: "OBSERVE", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: epc}, nil},
		{"missing time and bad tz", Event{Type: ObjectEvent, Action: "OBSERVE", EventTimeZoneOffset: "+25:00", EPCList: epc}, []string{"eventTime", "eventTimeZoneOffset"}},
		{"object without epcs or action", Event{Type: ObjectEvent, EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"action", "epcList"}},
		{"aggregation delete needs parent only", Event{Type: AggregationEvent, Action: "DELETE", EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"parentID"}},
		{"aggregation add needs children", Event{Type: AggregationEvent, Action: "ADD", ParentID: "urn:epc:id:sscc:9506000.0000000017", EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"childEPCs"}},
		{"aggregation observe", Event{Type: AggregationEvent, Action: "OBSERVE", ChildEPCs: epc, EventTime: t0, EventTimeZoneOffset: "+00:00"}, nil},
		{"transaction needs list", Event{Type: TransactionEvent, Action: "ADD", EPCList: epc, EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"bizTransactionList"}},
		{"transformation with action and nothing", Event{Type: TransformationEvent, Action: "ADD", EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"action", "inputEPCList"}},
		{"transformation by id", Event{Type: TransformationEvent, TransformationID: "urn:x:t:1", EventTime: t0, EventTimeZoneOffset: "+00:00"}, nil},
		{"association needs parent", Event{Type: AssociationEvent, Action: "ADD", EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"parentID"}},
		{"missing type", Event{EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"type"}},
		{"unknown type", Event{Type: "TeleportEvent", EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"type"}},
		{"cbv vocabularies", Event{Type: TransactionEvent, Action: "ADD", EventTime: t0, EventTimeZoneOffset: "+00:00",
			Disposition:        "levitating",
			BizTransactionList: []BizTransaction{{Type: "napkin", BizTransaction: ""}},
			SourceList:         []Source{{Type: "friend", Source: "urn:x:1"}},
			DestinationList:    []Destination{{Type: "enemy", Destination: "urn:x:2"}}},
			[]string{"disposition", "bizTransactionList", "bizTransactionList", "sourceList", "destinationList"}},
		{"custom vocabularies are allowed", Event{Type: ObjectEvent, Action: "ADD", EPCList: epc, EventTime: t0, EventTimeZoneOffset: "+00:00",
			BizStep: "https://example.org/step/x", Disposition: "https://example.org/disp/y"}, nil},
		{"bad ids and quantities", Event{Type: TransformationEvent, EventTime: t0, EventTimeZoneOffset: "+00:00",
			InputEPCList: []string{"nope"}, OutputEPCList: []string{"urn:epc:id:sscc:bad"}, ParentID: "bad parent",
			InputQuantityList: []QuantityElement{{EPCClass: "", Quantity: f64(-1)}}},
			[]string{"inputEPCList", "outputEPCList", "parentID", "quantityList", "quantityList"}},
		{"bad child", Event{Type: AggregationEvent, Action: "ADD", ParentID: "urn:epc:id:sscc:9506000.0000000017", ChildEPCs: []string{"x"}, EventTime: t0, EventTimeZoneOffset: "+00:00"}, []string{"childEPCs"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := Validate(3, &tc.e)
			var got []string
			for _, e := range errs {
				if e.Index != 3 {
					t.Errorf("index %d", e.Index)
				}
				got = append(got, e.Field)
			}
			if strings.Join(got, ",") != strings.Join(tc.fields, ",") {
				t.Fatalf("fields %v, want %v (%v)", got, tc.fields, errs)
			}
		})
	}
	fe := FieldError{Index: 2, Field: "action", Message: "required"}
	if fe.Error() != "event 2: action: required" {
		t.Fatalf("FieldError.Error: %q", fe.Error())
	}
}

func TestNormalize(t *testing.T) {
	e := Event{BizStep: "urn:epcglobal:cbv:bizstep:shipping", Disposition: "cbv:Disp-in_transit",
		EventTime:          time.Date(2024, 1, 1, 10, 0, 0, 0, time.FixedZone("x", 2*3600)),
		BizTransactionList: []BizTransaction{{Type: "https://ref.gs1.org/cbv/BTT-po", BizTransaction: "urn:x"}, {BizTransaction: "urn:y"}},
		SourceList:         []Source{{Type: "urn:epcglobal:cbv:sdt:owning_party"}},
		DestinationList:    []Destination{{Type: "cbv:SDT-location"}},
		ErrorDeclaration:   &ErrorDeclaration{Reason: "urn:epcglobal:cbv:er:incorrect_data"}}
	Normalize(&e)
	if e.BizStep != "shipping" || e.Disposition != "in_transit" || e.BizTransactionList[0].Type != "po" || e.BizTransactionList[1].Type != "" ||
		e.SourceList[0].Type != "owning_party" || e.DestinationList[0].Type != "location" || e.ErrorDeclaration.Reason != "incorrect_data" ||
		e.EventTimeZoneOffset != "+02:00" {
		t.Fatalf("normalize: %+v", e)
	}
	if URN("bizstep", "") != "" || URN("disp", "urn:x:y") != "urn:x:y" || URN("btt", "po") != "urn:epcglobal:cbv:btt:po" {
		t.Fatal("URN")
	}
}

func TestParseJSONVariants(t *testing.T) {
	if _, err := ParseJSON([]byte("{")); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("bad json: %v", err)
	}
	if _, err := ParseJSON([]byte(`{"type":"Something"}`)); err == nil || !strings.Contains(err.Error(), "EPCISDocument") {
		t.Fatalf("bad type: %v", err)
	}
	if _, err := ParseJSON([]byte(`{"type":"EPCISDocument","epcisBody":{"eventList":[{"type":"ObjectEvent","eventTime":"yesterday"}]}}`)); err == nil || !strings.Contains(err.Error(), "event 0") {
		t.Fatalf("bad event: %v", err)
	}
	if _, err := ParseJSON([]byte(`{"type":"EPCISDocument","epcisBody":[]}`)); err == nil {
		t.Fatal("bad body accepted")
	}
	if _, err := ParseJSON([]byte(`{"type":"ObjectEvent","epcList":"x"}`)); err == nil {
		t.Fatal("bad bare event accepted")
	}
	if _, err := ParseJSON([]byte(`{"type":"EPCISQueryDocument","epcisBody":5}`)); err == nil {
		t.Fatal("bad query doc accepted")
	}
	q := `{"type":"EPCISQueryDocument","schemaVersion":"2.0","epcisBody":{"queryResults":{"queryName":"SimpleEventQuery","resultsBody":{"eventList":[
		{"type":"ObjectEvent","action":"OBSERVE","eventTime":"2024-01-01T00:00:00Z","eventTimeZoneOffset":"+00:00","epcList":["urn:x:1"]}]}}}}`
	d, format, err := Parse("application/json", []byte(q))
	if err != nil || format != "json" || len(d.Body.EventList) != 1 || d.Body.QueryResults.QueryName != "SimpleEventQuery" {
		t.Fatalf("query doc: %v %s %+v", err, format, d)
	}
	ev := `{"type":"AggregationEvent","action":"ADD","eventTime":"2024-01-01T00:00:00Z","parentID":"urn:x:p","childEPCs":["urn:x:c"]}`
	d, err = ParseJSON([]byte(ev))
	if err != nil || len(d.Body.EventList) != 1 || d.Type != "EPCISDocument" || d.Body.EventList[0].ParentID != "urn:x:p" {
		t.Fatalf("bare event: %v %+v", err, d)
	}
}

func TestWriteJSONDefaults(t *testing.T) {
	d := &Document{Type: "EPCISDocument", Body: Body{EventList: []Event{{Type: ObjectEvent, Action: "OBSERVE", EPCList: []string{"urn:x:1"}, Extensions: map[string]any{"type": "shadowed", "ex:k": 1.5}}}}}
	b, err := WriteJSON(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["schemaVersion"] != "2.0" || fmt.Sprint(m["@context"]) != "["+DefaultContext+"]" {
		t.Fatalf("defaults: %s", b)
	}
	ev := m["epcisBody"].(map[string]any)["eventList"].([]any)[0].(map[string]any)
	if ev["type"] != ObjectEvent || ev["ex:k"] != 1.5 {
		t.Fatalf("extensions must not override standard fields: %v", ev)
	}
	nd := NewDocument(nil)
	if nd.Type != "EPCISDocument" || nd.SchemaVersion != "2.0" || nd.CreationDate.IsZero() {
		t.Fatalf("NewDocument: %+v", nd)
	}
	var e Event
	if err := e.UnmarshalJSON([]byte(`[1]`)); err == nil {
		t.Fatal("array event accepted")
	}
}

const xml2 = `<?xml version="1.0"?>
<epcis:EPCISDocument xmlns:epcis="urn:epcglobal:epcis:xsd:2" schemaVersion="2.0" creationDate="2024-05-01T00:00:00Z">
<EPCISBody><EventList>
<TransactionEvent>
  <eventTime>2024-05-01T10:00:00</eventTime>
  <recordTime>2024-05-01T10:00:05Z</recordTime>
  <eventTimeZoneOffset>+00:00</eventTimeZoneOffset>
  <eventID>urn:uuid:1</eventID>
  <errorDeclaration><declarationTime>2024-05-02T00:00:00Z</declarationTime><reason>urn:epcglobal:cbv:er:incorrect_data</reason>
    <correctiveEventIDs><correctiveEventID>urn:uuid:2</correctiveEventID></correctiveEventIDs></errorDeclaration>
  <epcList><epc>urn:epc:id:sgtin:0614141.107346.1</epc></epcList>
  <action>ADD</action>
  <bizStep>urn:epcglobal:cbv:bizstep:shipping</bizStep>
  <disposition>urn:epcglobal:cbv:disp:in_transit</disposition>
  <bizTransactionList><bizTransaction type="urn:epcglobal:cbv:btt:po">urn:epcglobal:cbv:bt:0614141000005:PO1</bizTransaction></bizTransactionList>
  <sourceList><source type="urn:epcglobal:cbv:sdt:owning_party">urn:epc:id:sgln:9506000.13435.0</source></sourceList>
  <destinationList><destination type="urn:epcglobal:cbv:sdt:location">urn:epc:id:sgln:9506000.13435.0</destination></destinationList>
  <quantityList><quantityElement><epcClass>urn:epc:class:lgtin:0614141.107346.L1</epcClass><quantity>2.5</quantity><uom>KGM</uom></quantityElement>
    <quantityElement><epcClass>urn:epc:class:lgtin:0614141.107346.L2</epcClass><quantity>many</quantity></quantityElement></quantityList>
  <ilmd><lotNumber>L1</lotNumber><nested><a>1</a><b>2</b></nested></ilmd>
</TransactionEvent>
<AssociationEvent><eventTime>2024-05-01T10:00:00+0200</eventTime><eventTimeZoneOffset>+02:00</eventTimeZoneOffset>
  <parentID>urn:epc:id:grai:0614141.12345.1</parentID><childEPCs><epc>urn:epc:id:giai:0614141.1</epc></childEPCs><action>ADD</action>
  <bizLocation><id>urn:epc:id:sgln:9506000.13435.0</id></bizLocation></AssociationEvent>
</EventList></EPCISBody></epcis:EPCISDocument>`

func TestParseXML20AllFields(t *testing.T) {
	d, format, err := Parse("text/xml", []byte(xml2))
	if err != nil || format != "xml" {
		t.Fatal(err, format)
	}
	if d.SchemaVersion != "2.0" || d.CreationDate.IsZero() || len(d.Body.EventList) != 2 {
		t.Fatalf("doc: %+v", d)
	}
	e := d.Body.EventList[0]
	if e.Type != TransactionEvent || e.EventID != "urn:uuid:1" || e.RecordTime == nil || e.ErrorDeclaration == nil ||
		e.ErrorDeclaration.CorrectiveEventIDs[0] != "urn:uuid:2" || len(e.BizTransactionList) != 1 || e.BizTransactionList[0].Type != "urn:epcglobal:cbv:btt:po" ||
		len(e.SourceList) != 1 || len(e.DestinationList) != 1 || len(e.QuantityList) != 2 || *e.QuantityList[0].Quantity != 2.5 ||
		e.QuantityList[0].UOM != "KGM" || e.QuantityList[1].Quantity != nil {
		t.Fatalf("event 0: %+v", e)
	}
	if e.ILMD["lotNumber"] != "L1" || fmt.Sprint(e.ILMD["nested"]) != "map[a:1 b:2]" {
		t.Fatalf("ilmd: %+v", e.ILMD)
	}
	if errs := ValidateDocument(d); len(errs) != 0 {
		t.Fatalf("valid document reported errors: %v", errs)
	}
	if d.Body.EventList[0].ErrorDeclaration.Reason != "incorrect_data" {
		t.Fatal("reason not normalized")
	}
	a := d.Body.EventList[1]
	if a.Type != AssociationEvent || a.BizLocation == nil || a.EventTime.Hour() != 10 {
		t.Fatalf("association: %+v", a)
	}

	// Round trip through our XML writer keeps the content (and hence the hash).
	x, _ := WriteXML(d)
	for _, frag := range []string{"<errorDeclaration>", `type="urn:epcglobal:cbv:btt:po"`, "<source type=", "<destination type=", "<quantity>2.5</quantity>",
		"<lotNumber>L1</lotNumber>", "<recordTime>", "<bizLocation><id>"} {
		if !strings.Contains(string(x), frag) {
			t.Errorf("written XML lacks %s", frag)
		}
	}
	if strings.Contains(string(x), "<nested>") {
		t.Error("non-string ILMD must be skipped in XML output")
	}
	d2, err := ParseXML(strings.NewReader(string(x)))
	if err != nil {
		t.Fatal(err)
	}
	ValidateDocument(d2)
	for i := range d.Body.EventList {
		if HashID(&d.Body.EventList[i]) != HashID(&d2.Body.EventList[i]) {
			t.Fatalf("event %d hash changed in round trip", i)
		}
	}
}

func TestParseXMLErrors(t *testing.T) {
	cases := map[string]string{
		"":                 "empty XML",
		"<a><b></a>":       "invalid XML",
		"<Foo/>":           "root element",
		"<EPCISDocument/>": "missing EPCISBody",
		"<EPCISDocument><EPCISBody><EventList><Bogus/></EventList></EPCISBody></EPCISDocument>":                                               "unknown event element",
		"<EPCISDocument><EPCISBody><EventList><ObjectEvent><eventTime>soon</eventTime></ObjectEvent></EventList></EPCISBody></EPCISDocument>": "invalid time",
	}
	for in, frag := range cases {
		if _, err := ParseXML(strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("ParseXML(%q) = %v, want %q", in, err, frag)
		}
	}
	// No EventList is an empty document, not an error.
	d, err := ParseXML(strings.NewReader(`<EPCISDocument><EPCISBody/></EPCISDocument>`))
	if err != nil || len(d.Body.EventList) != 0 {
		t.Fatalf("empty body: %v %+v", err, d)
	}
	// Query documents carry events under QueryResults/resultsBody.
	q := `<epcisq:EPCISQueryDocument xmlns:epcisq="urn:epcglobal:epcis-query:xsd:2"><EPCISBody><epcisq:QueryResults><queryName>q</queryName><resultsBody><EventList>
	<ObjectEvent><eventTime>2024-01-01T00:00:00Z</eventTime><eventTimeZoneOffset>+00:00</eventTimeZoneOffset><epcList><epc>urn:x:1</epc></epcList><action>OBSERVE</action></ObjectEvent>
	</EventList></resultsBody></epcisq:QueryResults></EPCISBody></epcisq:EPCISQueryDocument>`
	d, _, err = Parse("", []byte("  \n"+q))
	if err != nil || len(d.Body.EventList) != 1 {
		t.Fatalf("query xml: %v %+v", err, d)
	}
}

func TestIsXMLName(t *testing.T) {
	for s, want := range map[string]bool{"": false, "lot": true, "lot_2": true, "a-b.c": true, "1abc": false, "-x": false, "a b": false, "x:y": false} {
		if isXMLName(s) != want {
			t.Errorf("isXMLName(%q) != %v", s, want)
		}
	}
}

func TestParallelFor(t *testing.T) {
	var sum atomic.Int64
	if err := ParallelFor(10000, func(i int) error { sum.Add(int64(i)); return nil }); err != nil {
		t.Fatal(err)
	}
	if sum.Load() != 10000*9999/2 {
		t.Fatalf("sum %d", sum.Load())
	}
	boom := errors.New("boom")
	if err := ParallelFor(5000, func(i int) error {
		if i == 4321 {
			return boom
		}
		return nil
	}); !errors.Is(err, boom) {
		t.Fatalf("parallel err: %v", err)
	}
	if err := ParallelFor(3, func(i int) error {
		if i == 1 {
			return boom
		}
		return nil
	}); !errors.Is(err, boom) {
		t.Fatalf("serial err: %v", err)
	}
	if err := ParallelFor(0, func(int) error { return boom }); err != nil {
		t.Fatal("n=0 must not call fn")
	}
}

func TestLargeDocumentParsesInParallel(t *testing.T) {
	events := make([]Event, 2000)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range events {
		events[i] = Event{Type: ObjectEvent, Action: "OBSERVE", EventTime: t0.Add(time.Duration(i) * time.Second), EventTimeZoneOffset: "+00:00",
			EPCList: []string{fmt.Sprintf("urn:epc:id:sgtin:0614141.107346.%d", i)}}
	}
	b, _ := WriteJSON(NewDocument(events))
	d, err := ParseJSON(b)
	if err != nil || len(d.Body.EventList) != 2000 || d.Body.EventList[1999].EPCList[0] != "urn:epc:id:sgtin:0614141.107346.1999" {
		t.Fatalf("parallel parse: %v", err)
	}
	if errs := ValidateDocument(d); len(errs) != 0 {
		t.Fatalf("errors: %v", errs[:1])
	}
	// One bad event among many is reported with its index.
	bad := strings.Replace(string(b), `"eventTime":"2024-01-01T00:16:40Z"`, `"eventTime":"bad"`, 1)
	if _, err := ParseJSON([]byte(bad)); err == nil || !strings.Contains(err.Error(), "event 1000") {
		t.Fatalf("bad event in big doc: %v", err)
	}
}

func TestHashIDStableAndSensitive(t *testing.T) {
	base := Event{Type: ObjectEvent, Action: "ADD", EventTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), EventTimeZoneOffset: "+00:00",
		EPCList: []string{"urn:x:b", "urn:x:a"}, QuantityList: []QuantityElement{{EPCClass: "urn:x:c", Quantity: f64(2), UOM: "KGM"}, {EPCClass: "urn:x:d"}},
		ReadPoint: &Location{ID: "urn:x:rp"}, BizLocation: &Location{ID: "urn:x:bl"},
		BizTransactionList: []BizTransaction{{Type: "po", BizTransaction: "urn:x:po"}},
		SourceList:         []Source{{Type: "owning_party", Source: "urn:x:s"}}, DestinationList: []Destination{{Type: "location", Destination: "urn:x:d"}}}
	h := HashID(&base)
	if !strings.HasPrefix(h, "ni:///sha-256;") || !strings.HasSuffix(h, "?ver=openspoor1") {
		t.Fatalf("format: %s", h)
	}
	reordered := base
	reordered.EPCList = []string{"urn:x:a", "urn:x:b"}
	if HashID(&reordered) != h {
		t.Fatal("hash depends on list order")
	}
	// Same instant in another zone is the same event time.
	shifted := base
	shifted.EventTime = base.EventTime.In(time.FixedZone("p", 3600))
	if HashID(&shifted) != h {
		t.Fatal("hash depends on time zone representation")
	}
	for name, mut := range map[string]func(e *Event){
		"bizLocation": func(e *Event) { e.BizLocation = &Location{ID: "urn:x:other"} },
		"quantity": func(e *Event) {
			e.QuantityList = []QuantityElement{{EPCClass: "urn:x:c", Quantity: f64(3), UOM: "KGM"}}
		},
		"source":      func(e *Event) { e.SourceList = nil },
		"destination": func(e *Event) { e.DestinationList[0].Destination = "urn:x:e" },
		"transform":   func(e *Event) { e.TransformationID = "urn:x:t" },
	} {
		c := base
		c.DestinationList = append([]Destination(nil), base.DestinationList...)
		mut(&c)
		if HashID(&c) == h {
			t.Errorf("%s change did not change hash", name)
		}
	}
}
