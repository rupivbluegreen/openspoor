package edi

import (
	"strings"
	"testing"
)

func TestParseSegmentsX12(t *testing.T) {
	d := sampleOrder()
	out := X12(d, Envelope{Control: 9})
	ic, err := ParseSegments(out)
	if err != nil {
		t.Fatal(err)
	}
	if ic.Standard != "x12" || len(ic.Messages) != 1 || ic.Messages[0].Type != "850" {
		t.Fatalf("%+v", ic)
	}
	var env []string
	for _, s := range ic.Envelope {
		env = append(env, s.ID)
	}
	if strings.Join(env, ",") != "ISA,GS,GE,IEA" {
		t.Errorf("envelope %v", env)
	}
	m := ic.Messages[0]
	if m.Segments[0].ID != "ST" || m.Segments[len(m.Segments)-1].ID != "SE" {
		t.Errorf("segments %+v", m.Segments)
	}
	var beg Segment
	for _, s := range m.Segments {
		if s.ID == "BEG" {
			beg = s
		}
	}
	if beg.Element(3) != d.Number || beg.Element(9) != "" || beg.Component(3, 1) != d.Number || beg.Component(3, 2) != "" {
		t.Errorf("BEG %+v", beg)
	}
}

func sampleOrder() Document {
	return Document{Type: "order", Number: "PO-1", Date: midnight(), Buyer: Party{Name: "Globex", GLN: "0614141000005"}, Seller: Party{Name: "Acme", GLN: "9506000000008"},
		Currency: "USD", Lines: []Line{{Line: 1, GTIN: "09506000134352", Quantity: 2, UOM: "EA", UnitPrice: 1.5}, {Line: 2, GTIN: "09506000134369", Quantity: 1, UOM: "EA", UnitPrice: 2}}}
}

func TestParseSegmentsX12CustomSeparators(t *testing.T) {
	// "|" as element separator, "^" as sub-element separator and "!" as terminator, with line breaks between segments.
	in := "ISA|00|          |00|          |ZZ|SENDER         |ZZ|RECEIVER       |260701|0000|U|00401|000000001|0|P|^!\n" +
		"GS|PO|S|R|20260701|0000|1|X|004010!\nST|850|0001!\nBEG|00|SA|PO-9||20260701!\nPO1|1|5|EA|2.5||UK|09506000134352^X!\nSE|4|0001!\nGE|1|1!\nIEA|1|000000001!\n"
	ic, err := ParseSegments(in)
	if err != nil {
		t.Fatal(err)
	}
	m := ic.Messages[0]
	if m.Type != "850" || len(m.Segments) != 4 {
		t.Fatalf("%+v", m)
	}
	po1 := m.Segments[2]
	if po1.ID != "PO1" || po1.Element(7) != "09506000134352" || po1.Component(7, 2) != "X" || po1.Element(99) != "" || po1.Component(7, 5) != "" || po1.Component(0, 1) != "" {
		t.Errorf("PO1 %+v", po1)
	}
	if ic.Envelope[0].Element(6) != "SENDER         " {
		t.Errorf("ISA06 %q", ic.Envelope[0].Element(6))
	}
}

func TestParseSegmentsEDIFACT(t *testing.T) {
	d := sampleOrder()
	ic, err := ParseSegments(EDIFACT(d, Envelope{Control: 3}))
	if err != nil {
		t.Fatal(err)
	}
	if ic.Standard != "edifact" || len(ic.Messages) != 1 || ic.Messages[0].Type != "ORDERS" {
		t.Fatalf("%+v", ic)
	}
	var ids []string
	for _, s := range ic.Envelope {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "UNB,UNZ" {
		t.Errorf("envelope %v", ids)
	}
	var lin Segment
	for _, s := range ic.Messages[0].Segments {
		if s.ID == "LIN" {
			lin = s
			break
		}
	}
	if lin.Component(3, 1) != "09506000134352" || lin.Component(3, 2) != "SRV" {
		t.Errorf("LIN %+v", lin)
	}
}

func TestParseSegmentsEDIFACTReleaseAndServiceString(t *testing.T) {
	// UNA with custom separators: component "*", data "+", decimal ".", release "!", terminator ";".
	in := "UNA*+.! ;UNB+UNOC:3+A+B+260701:0000+1;UNH+1+ORDERS*D*96A;BGM+220+PO!+1!;2;FTX+AAI+++a!+b!!c;UNT+4+1;UNZ+1+1;"
	ic, err := ParseSegments(in)
	if err != nil {
		t.Fatal(err)
	}
	m := ic.Messages[0]
	if m.Type != "ORDERS" || len(m.Segments) != 4 {
		t.Fatalf("%+v", m)
	}
	if got := m.Segments[1].Element(2); got != "PO+1;2" {
		t.Errorf("escaped value %q", got)
	}
	if got := m.Segments[2].Element(4); got != "a+b!c" {
		t.Errorf("escaped value %q", got)
	}
	// Without UNA the default separators apply, and UNH may open the file.
	ic, err = ParseSegments("UNH+1+INVOIC:D:96A:UN'BGM+380+INV-1+9'UNT+3+1'")
	if err != nil || ic.Messages[0].Type != "INVOIC" || len(ic.Messages[0].Segments) != 3 {
		t.Fatalf("%v %+v", err, ic)
	}
}

func TestParseSegmentsErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":     "",
		"neither":   "<xml/>",
		"short ISA": "ISA",
		"truncated": "ISA*00*          *00*          *ZZ*A",
		"no ST":     "ISA*00*          *00*          *ZZ*A              *ZZ*B              *260701*0000*U*00401*000000001*0*P*>~GS*PO*A*B*20260701*0000*1*X*004010~",
		"short UNA": "UNA:+",
		"no UNH":    "UNA:+.? 'UNB+UNOC:3+A+B+260701:0000+1'UNZ+0+1'",
	} {
		if _, err := ParseSegments(in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
