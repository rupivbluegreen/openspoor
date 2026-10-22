package edi

import (
	"encoding/xml"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sample(t string) Document {
	d := Document{Type: t, Number: "PO-4711", Date: time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC), Currency: "EUR",
		Buyer: Party{Name: "Globex Wholesale", GLN: "0614141000005"}, Seller: Party{Name: "Acme Pharma", GLN: "9506000000008"},
		ShipTo: Party{Name: "Globex DC", GLN: "0614141000005"}, References: map[string]string{"po": "PO-4711"},
		SSCCs: []string{"095060000000000176"},
		Lines: []Line{{GTIN: "09506000134352", Description: "Metformin 850mg x30", Quantity: 120, UnitPrice: 3.25, Lot: "M1", Expiry: "2029-06-30", Serials: []string{"A1", "A2"}, Reason: "damaged"},
			{GTIN: "09506000134369", Description: "Metformin 500mg x30", Quantity: 60, UnitPrice: 2.1}}}
	if err := d.Validate(); err != nil {
		panic(err)
	}
	return d
}

func TestX12AllSets(t *testing.T) {
	for typ, set := range x12Sets {
		out := X12(sample(typ), Envelope{Control: 7})
		segs := splitSegments(out, '~')
		isa := strings.Split(segs[0], "*")
		if len(isa) != 17 || len(segs[0]) != 105 {
			t.Fatalf("%s: ISA must have 16 elements and be 106 chars incl. terminator: %d %q", typ, len(segs[0]), segs[0])
		}
		var st, se int
		for i, s := range segs {
			if strings.HasPrefix(s, "ST*") {
				st = i
				if !strings.HasPrefix(s, "ST*"+set) {
					t.Fatalf("%s: wrong set %s", typ, s)
				}
			}
			if strings.HasPrefix(s, "SE*") {
				se = i
				n, _ := strconv.Atoi(strings.Split(s, "*")[1])
				if n != se-st+1 {
					t.Fatalf("%s: SE count %d, actual %d", typ, n, se-st+1)
				}
			}
		}
		if !strings.HasSuffix(strings.TrimSpace(out), "IEA*1*000000007~") {
			t.Fatalf("%s: bad trailer", typ)
		}
	}
}

func TestX12OrderRoundTrip(t *testing.T) {
	d := sample("order")
	back, err := ParseX12(X12(d, Envelope{}))
	if err != nil {
		t.Fatal(err)
	}
	if back.Number != d.Number || len(back.Lines) != 2 || back.Lines[0].GTIN != d.Lines[0].GTIN || back.Lines[0].Quantity != 120 ||
		back.Lines[0].UnitPrice != 3.25 || back.Buyer.GLN != d.Buyer.GLN || back.Lines[0].Description != d.Lines[0].Description || back.Currency != "EUR" {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestEDIFACT(t *testing.T) {
	for typ := range edifactTypes {
		out := EDIFACT(sample(typ), Envelope{Control: 3})
		segs := splitSegments(out, '\'')
		var unh, unt int
		for i, s := range segs {
			if strings.HasPrefix(s, "UNH+") {
				unh = i
			}
			if strings.HasPrefix(s, "UNT+") {
				unt = i
				n, _ := strconv.Atoi(strings.Split(s, "+")[1])
				if n != unt-unh+1 {
					t.Fatalf("%s: UNT count %d, actual %d", typ, n, unt-unh+1)
				}
			}
		}
	}
	d := sample("order")
	back, err := ParseEDIFACT(EDIFACT(d, Envelope{}))
	if err != nil {
		t.Fatal(err)
	}
	if back.Number != "PO-4711" || len(back.Lines) != 2 || back.Lines[1].Quantity != 60 || back.Lines[0].UnitPrice != 3.25 || back.References["po"] != "PO-4711" {
		t.Fatalf("edifact round trip: %+v", back)
	}
}

func TestIDocIsXML(t *testing.T) {
	for typ := range idocTypes {
		out, err := IDoc(sample(typ), Envelope{Control: 42})
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := xml.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("%s: invalid XML: %v", typ, err)
		}
		if !strings.Contains(out, "<E1EDP19") || !strings.Contains(out, "<IDOCTYP>") {
			t.Fatalf("%s: missing segments", typ)
		}
	}
}
