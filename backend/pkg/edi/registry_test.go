package edi

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	keyRE     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	x12RE     = regexp.MustCompile(`^[0-9]{3}$`)
	edifactRE = regexp.MustCompile(`^[A-Z]{6}$`)
	bgmRE     = regexp.MustCompile(`^[0-9]{1,3}$`)
	gsRE      = regexp.MustCompile(`^[A-Z]{2}$`)
	segIDRE   = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,2}$`)
)

func validRole(role string) bool {
	kind, arg, _ := strings.Cut(role, ":")
	switch kind {
	case "", "number", "status", "gsid":
		return arg == ""
	case "const":
		return arg != ""
	case "date":
		if arg == "" {
			return true
		}
		_, ok := codeByKey(DateKeys, arg)
		return ok
	case "ref":
		_, ok := codeByKey(ReferenceKeys, arg)
		return ok
	case "amount":
		_, ok := codeByKey(AmountKeys, arg)
		return ok
	}
	return false
}

// TestRegistryConsistency proves every registry entry is internally consistent.
func TestRegistryConsistency(t *testing.T) {
	if len(Types) < 40 {
		t.Fatalf("registry has %d types, want at least 40", len(Types))
	}
	keys, x12s := map[string]bool{}, map[string]bool{}
	cats := map[Category]int{}
	for _, c := range Categories {
		cats[c] = 0
	}
	for _, ty := range Types {
		t.Run(ty.Key, func(t *testing.T) {
			if !keyRE.MatchString(ty.Key) || keys[ty.Key] {
				t.Errorf("key %q malformed or duplicate", ty.Key)
			}
			keys[ty.Key] = true
			if ty.Name == "" || ty.Description == "" {
				t.Error("name and description are required")
			}
			if _, ok := cats[ty.Category]; !ok {
				t.Errorf("unknown category %q", ty.Category)
			}
			cats[ty.Category]++
			switch ty.Flow {
			case FlowBuyerToSeller, FlowSellerToBuyer, FlowEither:
			default:
				t.Errorf("unknown flow %q", ty.Flow)
			}
			if ty.X12 == "" && ty.EDIFACT == "" {
				t.Error("a type needs a standard identifier (X12 set or EDIFACT message)")
			}
			if ty.X12 != "" {
				if !x12RE.MatchString(ty.X12) || x12s[ty.X12] {
					t.Errorf("X12 code %q malformed or used twice", ty.X12)
				}
				x12s[ty.X12] = true
			}
			if ty.EDIFACT != "" && !edifactRE.MatchString(ty.EDIFACT) {
				t.Errorf("EDIFACT message %q malformed", ty.EDIFACT)
			}
			if ty.EDIFACTBGM != "" && !bgmRE.MatchString(ty.EDIFACTBGM) {
				t.Errorf("BGM code %q malformed", ty.EDIFACTBGM)
			}
			if ty.EDIFACTWrite && ty.EDIFACT == "" {
				t.Error("EDIFACTWrite without an EDIFACT message")
			}
			if (ty.x12 != nil || ty.legacy) && ty.X12 == "" {
				t.Error("X12 writer without an X12 code")
			}
			if ty.IDocBasic != "" != (ty.IDocMessage != "") {
				t.Error("IDoc basic and message type go together")
			}
			if ty.Lines != "" && ty.Lines != LinesItem && ty.Lines != LinesReference {
				t.Errorf("unknown line kind %q", ty.Lines)
			}
			if ty.TODO != "" && !strings.HasPrefix(ty.TODO, "TODO(spec)") {
				t.Errorf("TODO must start with TODO(spec): %q", ty.TODO)
			}
			if ty.EDIFACTBGM == "" && ty.EDIFACT != "" && ty.EDIFACTWrite && !strings.Contains(ty.TODO, "BGM") && !ty.legacy {
				t.Error("an EDIFACT writer without a BGM code must say so in TODO")
			}
			seenReq := map[string]bool{}
			for _, f := range ty.Required {
				if !fieldKnown(f) || seenReq[f] {
					t.Errorf("required field %q unknown or repeated", f)
				}
				seenReq[f] = true
				if k, ok := strings.CutPrefix(f, "dates."); ok && !hasKey(ty.Dates, k) {
					t.Errorf("%s is required but the date is not declared", f)
				}
				if k, ok := strings.CutPrefix(f, "lines.dates."); ok && !hasKey(ty.LineDates, k) {
					t.Errorf("%s is required but the line date is not declared", f)
				}
				if k, ok := strings.CutPrefix(f, "parties."); ok && !hasKey(ty.Parties, k) {
					t.Errorf("%s is required but the party is not declared", f)
				}
				if k, ok := strings.CutPrefix(f, "amounts."); ok && !hasKey(ty.Amounts, k) {
					t.Errorf("%s is required but the amount is not declared", f)
				}
				if f == "status" && len(ty.Statuses) == 0 {
					t.Error("status is required but no statuses are declared")
				}
				if f == "lines.status" && len(ty.LineStatuses) == 0 {
					t.Error("lines.status is required but no line statuses are declared")
				}
				if ty.Lines == LinesReference && (f == "lines.gtin" || f == "lines.unitPrice") {
					t.Errorf("reference lines cannot require %s", f)
				}
				if ty.Lines == LinesItem && (f == "lines.ref" || f == "lines.refType" || f == "lines.amount") {
					t.Errorf("item lines cannot require %s", f)
				}
			}
			for _, set := range []struct {
				name string
				got  []string
				voc  []Code
			}{{"dates", ty.Dates, DateKeys}, {"lineDates", ty.LineDates, DateKeys}, {"parties", ty.Parties, PartyKeys}, {"amounts", ty.Amounts, AmountKeys}} {
				seen := map[string]bool{}
				for _, k := range set.got {
					if _, ok := codeByKey(set.voc, k); !ok || seen[k] {
						t.Errorf("%s key %q unknown or repeated", set.name, k)
					}
					seen[k] = true
				}
			}
			for _, group := range [][]string{ty.Statuses, ty.LineStatuses} {
				seen := map[string]bool{}
				for _, s := range group {
					if !keyRE.MatchString(s) || seen[s] {
						t.Errorf("status %q malformed or repeated", s)
					}
					seen[s] = true
				}
			}
			for _, r := range ty.Rules {
				if !RuleKnown(r.ID) || r.Description == "" {
					t.Errorf("rule %q unknown or undescribed", r.ID)
				}
				if r.ID == "date_not_after" && (len(r.Args) != 2 || !hasKey(ty.Dates, r.Args[0]) || !hasKey(ty.Dates, r.Args[1])) {
					t.Errorf("date_not_after args %v must be declared dates", r.Args)
				}
				if r.ID == "one_of_refs" {
					for _, a := range r.Args {
						if _, ok := codeByKey(ReferenceKeys, a); !ok {
							t.Errorf("one_of_refs arg %q is not a reference key", a)
						}
					}
				}
				if r.ID == "amount_sum" && (len(r.Args) != 1 || !hasKey(ty.Amounts, r.Args[0])) {
					t.Errorf("amount_sum args %v must be a declared amount", r.Args)
				}
			}
			if tm := ty.x12; tm != nil {
				if !gsRE.MatchString(tm.GS) {
					t.Errorf("GS code %q", tm.GS)
				}
				if _, ok := x12Profiles[tm.Lines]; !ok {
					t.Errorf("unknown line profile %q", tm.Lines)
				}
				if len(tm.Header) == 0 {
					t.Error("template without header segment")
				}
				for _, h := range tm.Header {
					if !segIDRE.MatchString(h.ID) {
						t.Errorf("segment id %q", h.ID)
					}
					for _, role := range h.Elems {
						if !validRole(role) {
							t.Errorf("%s: bad role %q", h.ID, role)
						}
						if k, ok := strings.CutPrefix(role, "date:"); ok && !hasKey(ty.Dates, k) {
							t.Errorf("%s: role %q uses an undeclared date", h.ID, role)
						}
						if k, ok := strings.CutPrefix(role, "amount:"); ok && !hasKey(ty.Amounts, k) {
							t.Errorf("%s: role %q uses an undeclared amount", h.ID, role)
						}
						if role == "status" && len(tm.Status) == 0 {
							t.Errorf("%s: status role without status table", h.ID)
						}
					}
				}
				for s := range tm.Status {
					if !hasKey(ty.Statuses, s) {
						t.Errorf("status table has undeclared status %q", s)
					}
				}
				for s := range tm.LineStatus {
					if !hasKey(ty.LineStatuses, s) {
						t.Errorf("line status table has undeclared status %q", s)
					}
				}
				for _, s := range ty.LineStatuses {
					if _, ok := tm.LineStatus[s]; !ok {
						t.Errorf("line status %q has no X12 code", s)
					}
				}
				codes := map[string]bool{}
				for _, c := range tm.LineStatus {
					if codes[c] {
						t.Errorf("line status code %q used twice", c)
					}
					codes[c] = true
				}
				if ty.Lines == LinesReference != (tm.Lines == "rmt" || tm.Lines == "oti" || tm.Lines == "ak2") {
					t.Errorf("line kind %s does not suit profile %s", ty.Lines, tm.Lines)
				}
			}
			info := ty.Info()
			if info.Key != ty.Key || len(info.Fields) == 0 || !contains(info.Fields, "number") {
				t.Errorf("info: %+v", info)
			}
			for _, f := range info.Fields {
				if f == "" || strings.Contains(f, " ") {
					t.Errorf("target %q", f)
				}
			}
		})
	}
	for c, n := range cats {
		if n == 0 {
			t.Errorf("category %s has no types", c)
		}
	}
	// The vocabularies themselves.
	for name, set := range map[string][]Code{"dates": DateKeys, "parties": PartyKeys, "references": ReferenceKeys, "amounts": AmountKeys, "statuses": StatusKeys} {
		seenKey, seenX, seenE := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, c := range set {
			if c.Key == "" || seenKey[c.Key] || (c.X12 != "" && seenX[c.X12]) || (c.EDIFACT != "" && seenE[c.EDIFACT]) {
				t.Errorf("%s: code %+v empty or duplicate", name, c)
			}
			seenKey[c.Key], seenX[c.X12], seenE[c.EDIFACT] = true, true, true
		}
	}
	if !reflect.DeepEqual(codeKeys(DateKeys)[:2], []string{"delivery_requested", "ship"}) {
		t.Error("codeKeys")
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestLegacyMapsFilledFromRegistry(t *testing.T) {
	want := map[string]string{"order": "850", "asn": "856", "invoice": "810", "inventory": "846", "return": "180", "chargeback": "844"}
	if !reflect.DeepEqual(x12Sets, want) || len(edifactTypes) != 6 || len(idocTypes) != 6 {
		t.Fatalf("legacy maps: %v %v %v", x12Sets, edifactTypes, idocTypes)
	}
	if X12Set("forecast") != "830" || EDIFACTType("forecast") != "DELFOR" || X12Set("nope") != "" || EDIFACTType("nope") != "" {
		t.Error("lookups")
	}
	if b, m := IDocType("order"); b != "ORDERS05" || m != "ORDERS" {
		t.Error("IDocType")
	}
	if b, _ := IDocType("forecast"); b != "" {
		t.Error("forecast has no IDoc type")
	}
	if b, _ := IDocType("nope"); b != "" {
		t.Error("unknown")
	}
}

func TestLookups(t *testing.T) {
	if ty, ok := LookupX12("855"); !ok || ty.Key != "order_ack" {
		t.Error("LookupX12")
	}
	if _, ok := LookupX12("000"); ok {
		t.Error("LookupX12 unknown")
	}
	if ty, ok := LookupEDIFACT("INVOIC", "381"); !ok || ty.Key != "credit_note" {
		t.Errorf("INVOIC/381: %v", ty.Key)
	}
	if ty, ok := LookupEDIFACT("ORDERS", "999"); !ok || ty.Key != "order" {
		t.Errorf("unknown BGM falls back to the first type of the message: %v", ty.Key)
	}
	if ty, ok := LookupEDIFACT("SLSRPT", ""); !ok || ty.Key != "product_activity" {
		t.Errorf("SLSRPT: %v", ty.Key)
	}
	if _, ok := LookupEDIFACT("XXXXXX", ""); ok {
		t.Error("unknown message")
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup unknown")
	}
	if len(Keys()) != len(Types) || len(Infos()) != len(Types) {
		t.Error("Keys/Infos")
	}
}

// --- fixtures: a valid document for every type ---

func midnight() time.Time { return time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) }

// fixture builds a valid document of the type. When rich is false only fields every writer
// carries are filled (the X12 profiles carry fewer line fields than EDIFACT).
func fixture(ty Type, rich bool) Document {
	d := Document{Type: ty.Key, Number: "DOC-" + strings.ToUpper(ty.Key[:2]) + "1", Date: midnight(),
		Buyer: Party{Name: "Globex Wholesale", GLN: "0614141000005"}, Seller: Party{Name: "Acme Pharma", GLN: "9506000000008"},
		ShipTo: Party{Name: "Globex DC", GLN: "0614141000012"}}
	if (!rich && ty.x12 != nil && ty.x12.Currency) || (rich && withCurrency[ty.EDIFACT]) {
		d.Currency = "EUR"
	}
	if len(ty.Statuses) > 0 {
		d.Status = ty.Statuses[0]
	}
	if len(ty.Dates) > 0 {
		d.Dates = map[string]string{}
		for i, k := range ty.Dates {
			d.Dates[k] = fmt.Sprintf("2026-07-%02d", 10+i)
		}
		if hasKey(ty.Dates, "period_start") {
			d.Dates["period_start"], d.Dates["period_end"] = "2026-07-01", "2026-07-31"
		}
	}
	if len(ty.Parties) > 0 {
		d.Parties = map[string]Party{}
		for i, k := range ty.Parties {
			d.Parties[k] = Party{Name: "Party " + k, GLN: fmt.Sprintf("40123450000%02d", i)}
		}
	}
	d.References = map[string]string{}
	for _, c := range ReferenceKeys {
		if (rich && c.EDIFACT != "") || (!rich && c.X12 != "") {
			d.References[c.Key] = strings.ToUpper(c.Key) + "-1"
		}
	}
	n := 2
	if ty.Lines == LinesReference {
		for i := 0; i < n; i++ {
			l := Line{RefType: "invoice", Ref: fmt.Sprintf("INV-%d", i+1)}
			if hasKey(ty.Required, "lines.amount") {
				l.Amount = float64(10 * (i + 1))
			}
			if len(ty.LineStatuses) > 0 {
				l.Status = ty.LineStatuses[i%len(ty.LineStatuses)]
			}
			for _, r := range ty.Rules {
				if r.ID == "x12_set_refs" {
					l.RefType = []string{"850", "856"}[i]
				}
			}
			d.Lines = append(d.Lines, l)
		}
		for _, k := range ty.Amounts {
			if d.Amounts == nil {
				d.Amounts = map[string]float64{}
			}
			d.Amounts[k] = 30
		}
	} else {
		for i := 0; i < n; i++ {
			l := Line{GTIN: []string{"09506000134352", "09506000134369"}[i], Description: "Item " + strconv.Itoa(i+1), Quantity: float64(12 * (i + 1)), UOM: "EA", UnitPrice: 3.25}
			if len(ty.LineStatuses) > 0 {
				l.Status = ty.LineStatuses[i%len(ty.LineStatuses)]
			}
			if len(ty.LineDates) > 0 {
				l.Dates = map[string]string{ty.LineDates[0]: fmt.Sprintf("2026-07-%02d", 15+i)}
			}
			if ty.x12 != nil && (ty.x12.Lines == "fst") {
				l.UnitPrice = 0
			}
			if rich {
				l.Lot, l.Expiry, l.Serials, l.Reason = "LOT"+strconv.Itoa(i), "2029-06-30", []string{"S1", "S2"}, "damaged"
			} else if hasKey(ty.Required, "lines.lot") {
				l.Lot = "LOT1"
			}
			if hasKey(ty.Required, "lines.reason") {
				l.Reason = "damaged"
			}
			if hasKey(ty.Required, "lines.lot") {
				l.Lot = "LOT" + strconv.Itoa(i)
			}
			d.Lines = append(d.Lines, l)
		}
		if rich {
			d.SSCCs = []string{"095060000000000176"}
			d.Note = "handle with care"
		}
		for _, k := range ty.Amounts {
			if d.Amounts == nil {
				d.Amounts = map[string]float64{}
			}
			d.Amounts[k] = 30
		}
		if hasKey(ty.Amounts, "total") {
			d.Amounts["total"] = d.Total()
		}
	}
	return d
}

// norm validates (applies defaults) and drops fields a format does not carry.
func norm(t *testing.T, d Document) Document {
	t.Helper()
	if err := d.Validate(); err != nil {
		t.Fatalf("%s: %v", d.Type, err)
	}
	return d
}

func TestFixturesAreValid(t *testing.T) {
	for _, ty := range Types {
		for _, rich := range []bool{false, true} {
			d := fixture(ty, rich)
			if err := d.Validate(); err != nil {
				t.Errorf("%s (rich=%v): %v", ty.Key, rich, err)
			}
		}
	}
}

func TestX12RoundTripEveryTemplate(t *testing.T) {
	n := 0
	for _, ty := range Types {
		if ty.x12 == nil {
			continue
		}
		n++
		t.Run(ty.Key, func(t *testing.T) {
			d := norm(t, fixture(ty, false))
			out := X12(d, Envelope{Control: 12})
			if out == "" {
				t.Fatal("empty output")
			}
			segs := splitSegments(out, '~')
			var st, se int
			for i, s := range segs {
				if strings.HasPrefix(s, "ST*") {
					st = i
					if !strings.HasPrefix(s, "ST*"+ty.X12+"*") {
						t.Fatalf("ST: %s", s)
					}
				}
				if strings.HasPrefix(s, "SE*") {
					se = i
					if c, _ := strconv.Atoi(strings.Split(s, "*")[1]); c != se-st+1 {
						t.Fatalf("SE count %d, actual %d", c, se-st+1)
					}
				}
				if strings.HasPrefix(s, "GS*") && strings.Split(s, "*")[1] != ty.x12.GS {
					t.Fatalf("GS: %s", s)
				}
			}
			back, err := ParseX12(out)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(d, back) {
				t.Fatalf("round trip differs\nwant %+v\n got %+v\n%s", d, back, out)
			}
		})
	}
	if n < 8 {
		t.Fatalf("only %d X12 templates", n)
	}
}

func TestEDIFACTRoundTripEveryWriter(t *testing.T) {
	n := 0
	for _, ty := range Types {
		if !ty.CanWriteEDIFACT() {
			continue
		}
		n++
		t.Run(ty.Key, func(t *testing.T) {
			d := norm(t, fixture(ty, true))
			out := EDIFACT(d, Envelope{Control: 5})
			segs := splitSegments(out, '\'')
			var unh, unt int
			for i, s := range segs {
				if strings.HasPrefix(s, "UNH+") {
					unh = i
					if !strings.Contains(s, "+"+ty.EDIFACT+":D:96A") {
						t.Fatalf("UNH: %s", s)
					}
				}
				if strings.HasPrefix(s, "UNT+") {
					unt = i
					if c, _ := strconv.Atoi(strings.Split(s, "+")[1]); c != unt-unh+1 {
						t.Fatalf("UNT count %d, actual %d", c, unt-unh+1)
					}
				}
			}
			back, err := ParseEDIFACTAs(out, ty.Key)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(d, back) {
				t.Fatalf("round trip differs\nwant %+v\n got %+v\n%s", d, back, out)
			}
			// Without a hint the type follows from message and BGM code, unless another type shares both.
			auto, err := ParseEDIFACT(out)
			if err != nil {
				t.Fatal(err)
			}
			if auto.Type != ty.Key {
				t.Errorf("auto-detected %s, want %s", auto.Type, ty.Key)
			}
		})
	}
	if n < 20 {
		t.Fatalf("only %d EDIFACT writers", n)
	}
}

// One representative document per category, written by hand so the expected segments are visible.
func TestRepresentativeSamples(t *testing.T) {
	forecast := Document{Type: "forecast", Number: "FC-1", Date: midnight(), Buyer: Party{Name: "Globex", GLN: "0614141000005"}, Seller: Party{Name: "Acme", GLN: "9506000000008"},
		Dates: map[string]string{"period_start": "2026-08-01", "period_end": "2026-08-31"},
		Lines: []Line{{GTIN: "09506000134352", Quantity: 100, UOM: "EA", Status: "firm", Dates: map[string]string{"delivery_requested": "2026-08-10"}},
			{GTIN: "09506000134352", Quantity: 80, UOM: "EA", Status: "planning", Dates: map[string]string{"delivery_requested": "2026-08-24"}}}}
	ack := Document{Type: "order_ack", Number: "ACK-1", Date: midnight(), Currency: "EUR", Status: "changed", References: map[string]string{"po": "PO-4711"},
		Buyer: Party{Name: "Globex", GLN: "0614141000005"}, Seller: Party{Name: "Acme", GLN: "9506000000008"},
		Lines: []Line{{GTIN: "09506000134352", Quantity: 100, UOM: "EA", UnitPrice: 3.25, Status: "changed"}, {GTIN: "09506000134369", Quantity: 5, UOM: "EA", UnitPrice: 2, Status: "backordered"}}}
	remit := Document{Type: "payment_remittance", Number: "RA-1", Date: midnight(), Currency: "EUR", Amounts: map[string]float64{"payment": 125.5},
		Dates: map[string]string{"effective": "2026-07-15"}, Parties: map[string]Party{"payer": {Name: "Globex", GLN: "0614141000005"}, "payee": {Name: "Acme", GLN: "9506000000008"}},
		Lines: []Line{{RefType: "invoice", Ref: "INV-1", Amount: 100}, {RefType: "invoice", Ref: "INV-2", Amount: 25.5}}}
	fack := Document{Type: "functional_ack", Number: "17", Date: midnight(),
		Lines: []Line{{RefType: "850", Ref: "0001", Status: "accepted"}, {RefType: "856", Ref: "0002", Status: "rejected"}}}
	advice := Document{Type: "application_advice", Number: "AA-1", Date: midnight(),
		Lines: []Line{{RefType: "po", Ref: "PO-4711", Status: "accepted"}, {RefType: "invoice", Ref: "INV-9", Status: "rejected"}}}
	credit := Document{Type: "credit_note", Number: "CN-1", Date: midnight(), Currency: "EUR", References: map[string]string{"invoice": "INV-1"},
		Lines: []Line{{GTIN: "09506000134352", Quantity: 10, UOM: "EA", UnitPrice: 3.25}}, Amounts: map[string]float64{"total": 32.5}}
	recadv := Document{Type: "receiving_advice", Number: "RC-1", Date: midnight(), References: map[string]string{"asn": "ASN-1"}, Dates: map[string]string{"received": "2026-07-03"},
		Lines: []Line{{GTIN: "09506000134352", Quantity: 118, UOM: "EA", Status: "accepted"}, {GTIN: "09506000134369", Quantity: 2, UOM: "EA", Status: "damaged", Reason: "crushed"}}}
	for _, d := range []Document{forecast, ack, remit, fack, advice, credit, recadv} {
		d := d
		t.Run(d.Type, func(t *testing.T) {
			want := norm(t, d)
			ty, _ := Lookup(d.Type)
			if ty.CanWriteX12() && ty.CanReadX12() {
				back, err := ParseX12(X12(want, Envelope{}))
				if err != nil || !reflect.DeepEqual(want, back) {
					t.Fatalf("X12: %v\nwant %+v\n got %+v", err, want, back)
				}
			}
			if ty.CanWriteEDIFACT() {
				back, err := ParseEDIFACTAs(EDIFACT(want, Envelope{}), d.Type)
				if err != nil {
					t.Fatal(err)
				}
				// EDIFACT carries the currency only for messages with a CUX segment.
				if !withCurrency[ty.EDIFACT] {
					back.Currency = want.Currency
				}
				if !reflect.DeepEqual(want, back) {
					t.Fatalf("EDIFACT:\nwant %+v\n got %+v", want, back)
				}
			}
		})
	}
	out := X12(norm(t, remit), Envelope{})
	for _, s := range []string{"ST*820*0001~", "BPR*I*125.5*C*NON************20260715~", "TRN*1*RA-1~", "RMT*IV*INV-1*100~", "N1*PR*Globex*UL*0614141000005~"} {
		if !strings.Contains(out, s) {
			t.Errorf("820 lacks %q:\n%s", s, out)
		}
	}
	out = X12(norm(t, fack), Envelope{})
	for _, s := range []string{"AK1*PO*17~", "AK2*850*0001~", "AK5*A~", "AK2*856*0002~", "AK5*R~", "AK9*P*2*2*1~"} {
		if !strings.Contains(out, s) {
			t.Errorf("997 lacks %q:\n%s", s, out)
		}
	}
	out = X12(norm(t, forecast), Envelope{})
	for _, s := range []string{"BFR*00*FC-1**DL*A*20260801*20260831*20260701~", "FST*100*C*D*20260810~", "UIT*EA~"} {
		if !strings.Contains(out, s) {
			t.Errorf("830 lacks %q:\n%s", s, out)
		}
	}
}

func TestOrderDatesRoundTripOnHandWrittenMappers(t *testing.T) {
	d := sample("order")
	d.Dates = map[string]string{"delivery_requested": "2026-07-20", "ship": "2026-07-15"}
	back, err := ParseX12(X12(d, Envelope{}))
	if err != nil || !reflect.DeepEqual(back.Dates, d.Dates) {
		t.Fatalf("X12 850 dates: %v %+v", err, back.Dates)
	}
	back, err = ParseEDIFACT(EDIFACT(d, Envelope{}))
	if err != nil || !reflect.DeepEqual(back.Dates, d.Dates) || back.Lines[0].Lot != "M1" || back.Lines[0].Expiry != "2029-06-30" || len(back.Lines[0].Serials) != 2 || back.Lines[0].Reason != "damaged" || len(back.SSCCs) != 1 {
		t.Fatalf("EDIFACT ORDERS: %v %+v", err, back)
	}
	// Every hand-written type still renders and parses back through EDIFACT.
	for typ := range edifactTypes {
		d := sample(typ)
		back, err := ParseEDIFACTAs(EDIFACT(d, Envelope{}), typ)
		if err != nil || back.Number != d.Number || len(back.Lines) != 2 {
			t.Errorf("%s: %v %+v", typ, err, back)
		}
	}
}

func TestWritersRefuseTypesWithoutThem(t *testing.T) {
	ghost := Document{Type: "customs_declaration", Number: "C-1", Lines: []Line{{GTIN: "09506000134352", Quantity: 1}}}
	if X12(ghost, Envelope{}) != "" || EDIFACT(ghost, Envelope{}) != "" {
		t.Error("catalogued-only types must not render")
	}
	if _, err := IDoc(ghost, Envelope{}); err == nil {
		t.Error("IDoc must refuse a type without mapping")
	}
	if X12(Document{Type: "nope"}, Envelope{}) != "" || EDIFACT(Document{Type: "nope"}, Envelope{}) != "" {
		t.Error("unknown type")
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"not x12":      "hello",
		"no ST":        "ISA*00*          *00*          *ZZ*A              *ZZ*B              *260701*0000*U*00401*000000001*0*P*>~GS*PO*A*B*20260701*0000*1*X*004010~",
		"unsupported":  "ISA*00*          *00*          *ZZ*A              *ZZ*B              *260701*0000*U*00401*000000001*0*P*>~ST*875*0001~SE*2*0001~",
		"unknown set":  "ISA*00*          *00*          *ZZ*A              *ZZ*B              *260701*0000*U*00401*000000001*0*P*>~ST*000*0001~SE*2*0001~",
		"not a header": "ISA*",
	} {
		if _, err := ParseX12(in); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// A template type with a header but invalid content fails validation.
	if _, err := ParseX12(X12(norm(t, fixture(mustType("order_ack"), false)), Envelope{}) + ""); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(X12(norm(t, fixture(mustType("order_ack"), false)), Envelope{}), "BAK*00*AD*", "BAK*00*ZZ*", 1)
	if _, err := ParseX12(bad); err == nil || !strings.Contains(err.Error(), "status is required") {
		t.Errorf("an unknown status code reads as empty, which fails validation: %v", err)
	}
	if _, err := ParseEDIFACT("UNB+UNOC:3'"); err == nil {
		t.Error("no UNH")
	}
	if _, err := ParseEDIFACT("UNH+1+ZZZZZZ:D:96A:UN'BGM+220+X+9'"); err == nil {
		t.Error("unknown message")
	}
	if _, err := ParseEDIFACTAs("UNH+1+ORDERS:D:96A:UN'BGM+220+X+9'", "nope"); err == nil {
		t.Error("unknown hint")
	}
	if _, err := ParseEDIFACTAs("UNH+1+ORDERS:D:96A:UN'BGM+220+X+9'", "asn"); err == nil {
		t.Error("hint of another message")
	}
	if _, err := ParseEDIFACT("UNH+1+ORDERS:D:96A:UN'BGM+220+X+9'"); err == nil {
		t.Error("an order needs lines")
	}
}

func mustType(k string) Type {
	t, ok := Lookup(k)
	if !ok {
		panic(k)
	}
	return t
}

// --- validation ---

func TestValidateMessagesAndRules(t *testing.T) {
	good := func() Document { return norm(t, fixture(mustType("forecast"), false)) }
	cases := map[string]struct {
		mut  func(d *Document)
		want string
	}{
		"unknown type":     {func(d *Document) { d.Type = "nope" }, "unknown document type"},
		"number":           {func(d *Document) { d.Number = "" }, "number is required"},
		"no lines":         {func(d *Document) { d.Lines = nil }, "at least one line is required"},
		"gtin":             {func(d *Document) { d.Lines[1].GTIN = "" }, "line 2: gtin is required"},
		"line status":      {func(d *Document) { d.Lines[0].Status = "" }, "line 1: status is required"},
		"bad line status":  {func(d *Document) { d.Lines[0].Status = "maybe" }, `status "maybe" is not allowed`},
		"line date":        {func(d *Document) { d.Lines[0].Dates = nil }, "line 1: dates.delivery_requested is required"},
		"line date format": {func(d *Document) { d.Lines[0].Dates["delivery_requested"] = "10/08/2026" }, "not YYYY-MM-DD"},
		"line date key":    {func(d *Document) { d.Lines[0].Dates["expiry"] = "2026-08-01" }, `date "expiry" is not allowed`},
		"missing period":   {func(d *Document) { delete(d.Dates, "period_end") }, "dates.period_end is required"},
		"date key":         {func(d *Document) { d.Dates["ship"] = "2026-08-01" }, `date "ship" is not allowed`},
		"date format":      {func(d *Document) { d.Dates["period_start"] = "x" }, "not YYYY-MM-DD"},
		"date order":       {func(d *Document) { d.Dates["period_start"] = "2026-09-01" }, "must not be after"},
		"quantity":         {func(d *Document) { d.Lines[0].Quantity = 0 }, "quantity must be greater than zero"},
		"gtin digits":      {func(d *Document) { d.Lines[0].GTIN = "ABC" }, "must be 8, 12, 13 or 14 digits"},
		"status not used":  {func(d *Document) { d.Status = "accepted" }, "is not allowed for forecast"},
		"party":            {func(d *Document) { d.Parties = map[string]Party{"carrier": {Name: "x"}} }, `party "carrier"`},
		"amount":           {func(d *Document) { d.Amounts = map[string]float64{"total": 1} }, `amount "total"`},
	}
	for name, c := range cases {
		d := good()
		c.mut(&d)
		if err := d.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
	if d := good(); d.Validate() != nil {
		t.Error("good fixture")
	}

	// Rules that need other types.
	rule := func(key string, mut func(d *Document), want string) {
		t.Helper()
		d := norm(t, fixture(mustType(key), false))
		mut(&d)
		if err := d.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", key, err, want)
		}
	}
	rule("payment_remittance", func(d *Document) { d.Amounts["payment"] = 99 }, "must equal the sum of the line amounts")
	rule("payment_remittance", func(d *Document) { d.Lines[0].RefType = "weird" }, "not a known reference key")
	rule("payment_remittance", func(d *Document) { d.Currency = "eur" }, "three-letter upper-case")
	rule("payment_remittance", func(d *Document) { delete(d.Amounts, "payment") }, "amounts.payment is required")
	rule("functional_ack", func(d *Document) { d.Lines[0].RefType = "85" }, "three-digit X12 transaction set")
	rule("receiving_advice", func(d *Document) { d.References = nil }, "one of the references po, asn, deliveryNote is required")
	rule("receiving_advice", func(d *Document) { d.Lines[0].Quantity = -1 }, "must not be negative")
	rule("quote", func(d *Document) { d.Lines[0].UnitPrice = -1 }, "unit price must not be negative")
	rule("quote", func(d *Document) { d.Lines[0].UnitPrice = 0 }, "unitPrice is required")
	rule("order_ack", func(d *Document) { d.Parties = map[string]Party{"x": {Name: "y"}} }, `party "x"`)
	rule("order_ack", func(d *Document) { d.References = nil }, "references.po is required")
	rule("order_ack", func(d *Document) { d.Status = "" }, "status is required")
}

func TestValidateAppliesDefaultsAndTotals(t *testing.T) {
	d := Document{Type: "credit_note", Number: "CN-7", References: map[string]string{"invoice": "I-1"},
		Lines: []Line{{GTIN: "09506000134352", Quantity: 2, UnitPrice: 5}}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Currency != "USD" || d.Lines[0].Line != 1 || d.Lines[0].UOM != "EA" || d.Date.IsZero() || d.Amounts["total"] != 10 {
		t.Fatalf("defaults: %+v", d)
	}
	r := Document{Type: "payment_remittance", Number: "R", Amounts: map[string]float64{"payment": 5}, Lines: []Line{{Ref: "I", RefType: "invoice", Amount: 5}}}
	if err := r.Validate(); err != nil || r.Lines[0].UOM != "" {
		t.Fatalf("reference lines get no unit of measure: %v %+v", err, r.Lines[0])
	}
	if r.Total() != 5 {
		t.Errorf("Total counts line amounts: %v", r.Total())
	}
	// A status not declared by a hand-written type is rejected.
	o := sample("order")
	o.Status = "accepted"
	if err := o.Validate(); err == nil {
		t.Error("order has no statuses")
	}
}

func TestTargetsFollowTheType(t *testing.T) {
	order, _ := Lookup("order")
	if tg := order.Targets(); !contains(tg, "lines.lot") || !contains(tg, "dates.delivery_requested") || contains(tg, "status") {
		t.Errorf("order targets: %v", tg)
	}
	remit, _ := Lookup("payment_remittance")
	tg := remit.Targets()
	for _, want := range []string{"lines.ref", "lines.refType", "lines.amount", "amounts.payment", "parties.payer.gln", "dates.effective"} {
		if !contains(tg, want) {
			t.Errorf("payment_remittance targets lack %s: %v", want, tg)
		}
	}
	if contains(tg, "lines.gtin") {
		t.Error("reference lines have no GTIN")
	}
	fc, _ := Lookup("forecast")
	tg = fc.Targets()
	sort.Strings(tg)
	for _, want := range []string{"lines.status", "lines.dates.delivery_requested", "dates.period_start"} {
		if i := sort.SearchStrings(tg, want); i >= len(tg) || tg[i] != want {
			t.Errorf("forecast targets lack %s", want)
		}
	}
	for _, ty := range Types {
		seen := map[string]bool{}
		for _, f := range ty.Targets() {
			if seen[f] {
				t.Errorf("%s: duplicate target %s", ty.Key, f)
			}
			seen[f] = true
		}
	}
}
