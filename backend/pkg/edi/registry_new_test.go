package edi

import (
	"reflect"
	"strings"
	"testing"
)

func party(name, gln string) Party { return Party{Name: name, GLN: gln} }

func mustContain(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, s := range parts {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

// The item profile carries lot, expiry, reason, description and a text status.
func TestX12ItemProfileCarriesLotExpiryAndReason(t *testing.T) {
	d := norm(t, Document{Type: "receiving_advice", Number: "RC-9", Date: midnight(), References: map[string]string{"po": "PO-9"},
		Buyer: party("Globex", "0614141000005"), Seller: party("Acme", "9506000000008"), Dates: map[string]string{"received": "2026-07-03"},
		Lines: []Line{{GTIN: "09506000134352", Description: "Tablets", Quantity: 4, UOM: "EA", Status: "damaged", Lot: "L7", Expiry: "2029-06-30", Reason: "crushed"}}})
	out := X12(d, Envelope{Control: 3})
	mustContain(t, out, "ST*861*0001~", "BRA*RC-9*20260701*00~", "RCD*1*4*EA~", "REF*LT*L7~", "DTM*036*20290630~", "NTE*ADD*crushed~", "MSG*damaged~", "GS*RC*")
	back, err := ParseX12(out)
	if err != nil || !reflect.DeepEqual(d, back) {
		t.Fatalf("round trip: %v\nwant %+v\n got %+v", err, d, back)
	}
}

func TestX12ReportAndAcknowledgementSets(t *testing.T) {
	period := map[string]string{"period_start": "2026-07-01", "period_end": "2026-07-31"}
	activity := norm(t, Document{Type: "product_activity", Number: "PA-1", Date: midnight(), Dates: period,
		Buyer: party("Globex", "0614141000005"), Seller: party("Acme", "9506000000008"),
		Lines: []Line{{GTIN: "09506000134352", Quantity: 40, UOM: "EA"}, {GTIN: "09506000134369", Quantity: 0, UOM: "EA"}}})
	resale := norm(t, Document{Type: "resale_report", Number: "RS-1", Date: midnight(), Dates: period,
		Buyer: party("Globex", "0614141000005"), Seller: party("Acme", "9506000000008"),
		Lines: []Line{{GTIN: "09506000134352", Quantity: 7, UOM: "EA"}}})
	response := norm(t, Document{Type: "load_tender_response", Number: "LT-1", Date: midnight(), Status: "rejected", References: map[string]string{"shipment": "SH-1"}})
	impl := norm(t, Document{Type: "implementation_ack", Number: "21", Date: midnight(),
		Lines: []Line{{RefType: "850", Ref: "0001", Status: "accepted"}, {RefType: "856", Ref: "0002", Status: "accepted_with_errors"}}})
	for _, tc := range []struct {
		d     Document
		parts []string
	}{
		{activity, []string{"ST*852*0001~", "XQ*H*20260701*20260731~", "REF*ZZ*PA-1~", "ZA*QS*40*EA~", "ZA*QS*0*EA~", "GS*PD*"}},
		{resale, []string{"ST*867*0001~", "BPT*00*RS-1*20260701~", "DTM*150*20260701~", "DTM*151*20260731~", "SN1**7*EA~", "GS*PT*"}},
		{response, []string{"ST*990*0001~", "B1**LT-1*20260701*D~", "REF*SI*SH-1~", "GS*GF*"}},
		{impl, []string{"ST*999*0001~", "AK1*PO*21~", "AK2*850*0001~", "IK5*A~", "AK2*856*0002~", "IK5*E~", "AK9*E*2*2*2~"}},
	} {
		t.Run(tc.d.Type, func(t *testing.T) {
			out := X12(tc.d, Envelope{Control: 2})
			mustContain(t, out, tc.parts...)
			back, err := ParseX12(out)
			if err != nil || !reflect.DeepEqual(tc.d, back) {
				t.Fatalf("round trip: %v\nwant %+v\n got %+v\n%s", err, tc.d, back, out)
			}
		})
	}
	if strings.Contains(X12(impl, Envelope{}), "AK5") {
		t.Error("the 999 uses IK5, not AK5")
	}
	// A 990 carries no lines: they are not written.
	response.Lines = []Line{{RefType: "invoice", Ref: "INV-1"}}
	if out := X12(response, Envelope{}); strings.Contains(out, "INV-1") {
		t.Errorf("990 wrote a line:\n%s", out)
	}
}

// A receiving advice that names only an ASN cannot be read back from X12 (no verified REF
// qualifier for it): the reader reports the missing reference rather than guessing.
func TestX12ReceivingAdviceNeedsAPurchaseOrderReference(t *testing.T) {
	d := norm(t, Document{Type: "receiving_advice", Number: "RC-2", Date: midnight(), References: map[string]string{"asn": "ASN-1"},
		Lines: []Line{{GTIN: "09506000134352", Quantity: 1, UOM: "EA", Status: "accepted"}}})
	if _, err := ParseX12(X12(d, Envelope{})); err == nil || !strings.Contains(err.Error(), "references") {
		t.Fatalf("want a missing-reference error, got %v", err)
	}
}

func TestEDIFACTTransportAndMasterDataMessages(t *testing.T) {
	status := norm(t, Document{Type: "shipment_status", Number: "ST-1", Date: midnight(), Status: "in_transit",
		References: map[string]string{"shipment": "SH-1"}, Dates: map[string]string{"ship": "2026-07-02", "delivery_estimated": "2026-07-05"},
		Parties: map[string]Party{"carrier": party("Fast Freight", "4012345000009")}, Seller: party("Acme", "9506000000008"), Buyer: party("Globex", "0614141000005")})
	tender := norm(t, Document{Type: "load_tender", Number: "LD-1", Date: midnight(), Dates: map[string]string{"ship": "2026-07-02"},
		Parties: map[string]Party{"carrier": party("Fast Freight", "4012345000009"), "shipFrom": party("Acme DC", "9506000000015")},
		Lines:   []Line{{GTIN: "09506000134352", Quantity: 3, UOM: "EA"}}})
	pay := norm(t, Document{Type: "payment_order", Number: "PO-77", Date: midnight(), Currency: "EUR", Amounts: map[string]float64{"payment": 125.5},
		Dates: map[string]string{"effective": "2026-07-15"}, Parties: map[string]Party{"payer": party("Globex", "0614141000005"), "payee": party("Acme", "9506000000008")}})
	party1 := norm(t, Document{Type: "party_data", Number: "PD-1", Date: midnight(), Seller: party("Acme Pharma", "9506000000008")})
	customs := norm(t, Document{Type: "customs_response", Number: "CR-1", Date: midnight(), Status: "accepted"})
	for _, tc := range []struct {
		d     Document
		parts []string
	}{
		{status, []string{"UNH+1+IFTSTA:D:96A:UN:EAN008'", "BGM++ST-1+9'", "FTX+ZZZ+++in_transit'", "RFF+SI:SH-1'", "NAD+CA+4012345000009::9++Fast Freight'"}},
		{tender, []string{"UNH+1+IFTMIN:D:96A", "NAD+CA+4012345000009::9++Fast Freight'", "NAD+SF+9506000000015::9++Acme DC'", "DTM+11:20260702:102'"}},
		{pay, []string{"UNH+1+PAYORD:D:96A", "CUX+2:EUR:9'", "MOA+9:125.5'", "NAD+PR+0614141000005::9++Globex'", "NAD+PE+"}},
		{party1, []string{"UNH+1+PARTIN:D:96A", "NAD+SU+9506000000008::9++Acme Pharma'", "CNT+2:0'"}},
		{customs, []string{"UNH+1+CUSRES:D:96A", "BGM++CR-1+29'"}},
	} {
		t.Run(tc.d.Type, func(t *testing.T) {
			out := EDIFACT(tc.d, Envelope{Control: 4})
			mustContain(t, out, tc.parts...)
			back, err := ParseEDIFACT(out)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, out)
			}
			if !withCurrency[EDIFACTType(tc.d.Type)] {
				back.Currency = tc.d.Currency
			}
			if !reflect.DeepEqual(tc.d, back) {
				t.Fatalf("round trip\nwant %+v\n got %+v\n%s", tc.d, back, out)
			}
		})
	}
}

// The writer and reader counts are asserted so the README and OPEN_QUESTIONS stay honest.
func TestFormatCounts(t *testing.T) {
	var x12w, x12r, edw, edr, idoc int
	for _, ty := range Types {
		if ty.CanWriteX12() {
			x12w++
		}
		if ty.CanReadX12() {
			x12r++
		}
		if ty.CanWriteEDIFACT() {
			edw++
		}
		if ty.CanReadEDIFACT() {
			edr++
		}
		if ty.CanWriteIDoc() {
			idoc++
		}
	}
	got := [5]int{x12w, x12r, edw, edr, idoc}
	if want := [5]int{19, 14, 35, 35, 6}; got != want {
		t.Errorf("writers/readers x12w x12r edw edr idoc = %v, want %v (update README.md and docs/OPEN_QUESTIONS.md)", got, want)
	}
	if len(Types) != 60 {
		t.Errorf("%d types, README says 60", len(Types))
	}
}
