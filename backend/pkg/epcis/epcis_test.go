package epcis

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "epcis", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGS1Examples(t *testing.T) {
	for _, f := range []string{"Example_9.6.1-ObjectEvent.jsonld", "Example_9.6.4-TransformationEvent.jsonld"} {
		t.Run(f, func(t *testing.T) {
			d, _, err := Parse("application/ld+json", load(t, f))
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Body.EventList) == 0 {
				t.Fatal("no events")
			}
			if errs := ValidateDocument(d); len(errs) > 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			// JSON -> XML -> JSON keeps the core fields.
			x, err := WriteXML(d)
			if err != nil {
				t.Fatal(err)
			}
			d2, err := ParseXML(bytesReader(x))
			if err != nil {
				t.Fatalf("reparse xml: %v\n%s", err, x)
			}
			ValidateDocument(d2)
			for i := range d.Body.EventList {
				a, b := d.Body.EventList[i], d2.Body.EventList[i]
				if a.Type != b.Type || a.BizStep != b.BizStep || len(a.AllEPCs()) != len(b.AllEPCs()) || !a.EventTime.Equal(b.EventTime) {
					t.Fatalf("event %d differs after XML round trip:\n%+v\n%+v", i, a, b)
				}
				if HashID(&a) != HashID(&b) {
					t.Fatalf("hash differs after round trip")
				}
			}
		})
	}
}

func TestEPCIS12XML(t *testing.T) {
	d, format, err := Parse("", load(t, "epcis12-object-aggregation.xml"))
	if err != nil || format != "xml" {
		t.Fatal(err, format)
	}
	if n := len(d.Body.EventList); n != 3 {
		t.Fatalf("want 3 events (incl. extension-wrapped TransformationEvent), got %d", n)
	}
	if errs := ValidateDocument(d); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	e := d.Body.EventList[0]
	if e.BizStep != "commissioning" || e.Disposition != "active" || e.ILMD["lotNumber"] != "LOT42" {
		t.Fatalf("normalization failed: %+v", e)
	}
	if d.Body.EventList[2].Type != TransformationEvent || *d.Body.EventList[2].InputQuantityList[0].Quantity != 200 {
		t.Fatalf("transformation not parsed: %+v", d.Body.EventList[2])
	}
}

func TestValidationErrors(t *testing.T) {
	d, _, err := Parse("application/json", load(t, "invalid-events.jsonld"))
	if err != nil {
		t.Fatal(err)
	}
	errs := ValidateDocument(d)
	fields := map[string]int{}
	for _, e := range errs {
		fields[e.Field] = e.Index
	}
	if fields["action"] != 1 || fields["bizStep"] != 1 {
		t.Fatalf("expected action and bizStep errors on event 1: %v", errs)
	}
	if fields["parentID"] != 2 {
		t.Fatalf("expected parentID error on event 2: %v", errs)
	}
	for _, e := range errs {
		if e.Index == 0 {
			t.Fatalf("event 0 is valid: %v", e)
		}
	}
}

func TestExtensionsPreserved(t *testing.T) {
	src := `{"type":"ObjectEvent","action":"OBSERVE","eventTime":"2024-01-01T00:00:00Z","eventTimeZoneOffset":"+00:00","epcList":["urn:epc:id:sgtin:0614141.107346.1"],"example:temp":{"@value":"5"}}`
	d, err := ParseJSON([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := WriteJSON(d)
	d2, _ := ParseJSON(b)
	if d2.Body.EventList[0].Extensions["example:temp"] == nil {
		t.Fatalf("extension lost: %s", b)
	}
}

func TestCBVForms(t *testing.T) {
	for _, v := range []string{"shipping", "urn:epcglobal:cbv:bizstep:shipping", "https://ref.gs1.org/cbv/BizStep-shipping"} {
		if s, cbv := Short("bizstep", v); s != "shipping" || !cbv {
			t.Errorf("%s -> %s %v", v, s, cbv)
		}
	}
	if _, cbv := Short("bizstep", "https://example.com/steps/custom"); cbv {
		t.Error("custom URI must not be CBV")
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
