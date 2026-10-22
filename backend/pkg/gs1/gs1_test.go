package gs1

import "testing"

func TestCheckDigits(t *testing.T) {
	for _, g := range []string{"09506000134352", "00614141107346", "10614141073464"} {
		if !ValidGTIN(g) {
			t.Errorf("%s should be valid", g)
		}
	}
	if ValidGTIN("09506000134353") {
		t.Error("bad check digit accepted")
	}
	if !ValidGLN("0614141073467") {
		t.Error("GLN should be valid")
	}
	if !ValidSSCC("106141411234567897") {
		t.Error("SSCC should be valid")
	}
}

func TestElementStringRoundTrip(t *testing.T) {
	p := Pack{GTIN: "09506000134352", Serial: "ABC123", Lot: "L42", Expiry: "270131"}
	es := ElementString(p.AIs())
	ais, err := ParseElementString("]d2" + es)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PackFromAIs(ais)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("round trip: got %+v want %+v", got, p)
	}
	h, err := ParseElementString("(01)09506000134352(17)270131(10)L42(21)ABC123")
	if err != nil || len(h) != 4 {
		t.Fatalf("hri parse: %v %v", h, err)
	}
}

func TestSGTINAndDigitalLink(t *testing.T) {
	urn, err := SGTINURN("10614141073464", "2017", 7)
	if err != nil || urn != "urn:epc:id:sgtin:0614141.107346.2017" {
		t.Fatalf("got %s %v", urn, err)
	}
	g, s, cp, err := ParseSGTINURN(urn)
	if err != nil || g != "10614141073464" || s != "2017" || cp != 7 {
		t.Fatalf("parse: %s %s %d %v", g, s, cp, err)
	}
	dl, err := URNToDigitalLink(urn, "")
	if err != nil || dl != "https://id.gs1.org/01/10614141073464/21/2017" {
		t.Fatalf("dl: %s %v", dl, err)
	}
	back, err := DigitalLinkToURN(dl, 7)
	if err != nil || back != urn {
		t.Fatalf("back: %s %v", back, err)
	}
	ais, err := ParseDigitalLink("https://example.com/01/09506000134352/10/L1/21/X%2F1?17=270131")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := PackFromAIs(ais)
	if p.Serial != "X/1" || p.Expiry != "270131" || p.Lot != "L1" {
		t.Fatalf("pack %+v", p)
	}
	ss, err := SSCCURN("106141411234567897", 7)
	if err != nil || ss != "urn:epc:id:sscc:0614141.1123456789" {
		t.Fatalf("sscc %s %v", ss, err)
	}
	if v, _ := ParseSSCCURN(ss); v != "106141411234567897" {
		t.Fatalf("sscc back %s", v)
	}
	gl, _ := SGLNURN("0614141073467", 7, "")
	if v, _ := ParseSGLNURN(gl); v != "0614141073467" {
		t.Fatalf("sgln back %s from %s", v, gl)
	}
}

func TestExpiryDate(t *testing.T) {
	d, err := ExpiryDate("270200")
	if err != nil || d.Format("2006-01-02") != "2027-02-28" {
		t.Fatalf("%v %v", d, err)
	}
}
