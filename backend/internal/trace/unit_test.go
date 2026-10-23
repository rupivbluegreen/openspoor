package trace

import (
	"errors"
	"strings"
	"testing"
	"time"

	lib "github.com/openspoor/openspoor/pkg/epcis"
)

var t0 = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

func at(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }

func ev(id string, h int, typ, step, loc string, epcs ...string) evRow {
	return evRow{ID: id, Type: typ, BizStep: step, Location: loc, Time: at(h), Recorded: at(h), EPCs: epcs}
}

const (
	ga = "https://id.gs1.org/414/9506000000015"
	gb = "https://id.gs1.org/414/0614141000029"
	gc = "https://id.gs1.org/414/0614141000036"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		e    evRow
		want string
	}{
		{evRow{Type: lib.ObjectEvent, BizStep: "commissioning"}, CatCommissioning},
		{evRow{Type: lib.ObjectEvent, BizStep: "shipping"}, CatShipping},
		{evRow{Type: lib.ObjectEvent, BizStep: "receiving"}, CatReceiving},
		{evRow{Type: lib.ObjectEvent, BizStep: "decommissioning"}, CatDecommissioning},
		{evRow{Type: lib.ObjectEvent, BizStep: "destroying"}, CatDestroying},
		{evRow{Type: lib.ObjectEvent, Disposition: "destroyed"}, CatDestroying},
		{evRow{Type: lib.ObjectEvent, Action: "DELETE", BizStep: "dispensing"}, CatDecommissioning},
		{evRow{Type: lib.AggregationEvent, Action: "DELETE"}, ""},
		{evRow{Type: lib.TransformationEvent, BizStep: "shipping"}, ""},
		{evRow{Type: lib.ObjectEvent, BizStep: "storing"}, ""},
	}
	for i, c := range cases {
		if got := classify(c.e); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

func TestGLNOf(t *testing.T) {
	for in, want := range map[string]string{
		"urn:epc:id:sgln:0614141.00002.0":      "0614141000029",
		"urn:epc:id:pgln:0614141.00002":        "0614141000029",
		gb:                                     "0614141000029",
		"0614141000029":                        "0614141000029",
		"urn:epc:id:sgln:0614141":              "",
		"urn:epc:id:sgln:06.00002.0":           "",
		"https://id.gs1.org/01/09506000134352": "",
		"nonsense":                             "",
	} {
		if got := glnOf(in); got != want {
			t.Errorf("glnOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReconcile(t *testing.T) {
	scope := []scopeSerial{{EPC: "a", Lot: "L", Status: "active"}, {EPC: "b", Lot: "L", Status: "in_transit"}, {EPC: "c", Lot: "L", Status: "decommissioned"},
		{EPC: "d", Lot: "L", Status: "destroyed"}, {EPC: "e", Lot: "L", Status: "active"}, {EPC: "f", Lot: "L", Status: "active"}}
	events := map[string][]evRow{
		"a": {ev("1", 0, lib.ObjectEvent, "commissioning", ga)},
		"b": {ev("2", 0, lib.ObjectEvent, "commissioning", ga), ev("3", 1, lib.ObjectEvent, "shipping", ga)},
		"c": {ev("4", 0, lib.ObjectEvent, "commissioning", ga), {ID: "5", Type: lib.ObjectEvent, Action: "DELETE", Time: at(1)}},
		"d": {ev("6", 0, lib.ObjectEvent, "commissioning", ga), ev("7", 1, lib.ObjectEvent, "destroying", ga)},
		// e was shipped without ever being commissioned or received; f was received twice
		"e": {{ID: "8", Type: lib.ObjectEvent, BizStep: "shipping", Time: at(1), Via: true}},
		"f": {ev("9", 0, lib.ObjectEvent, "receiving", gb), ev("10", 1, lib.ObjectEvent, "receiving", gb), ev("11", 2, lib.ObjectEvent, "storing", gb)},
	}
	lb := Reconcile("g", "L", scope, events, Settings{})
	if lb.Commissioned != 4 || lb.Shipped != 2 || lb.Received != 1 || lb.Decommissioned != 1 || lb.Destroyed != 1 || lb.Serials != 6 {
		t.Fatalf("counts: %+v", lb)
	}
	if lb.Inflow != 6 || lb.Outflow != 4 || lb.ExpectedOnHand != 3 || lb.Discrepancy != 2 || lb.Closed || len(lb.Anomalies) != 2 || lb.ViaAggregation != 1 {
		t.Fatalf("balance: %+v", lb)
	}
	if lb.Occurrences[CatReceiving] != 2 || lb.ByStatus["active"] != 3 {
		t.Fatalf("occurrences: %+v", lb)
	}
	if lb = Reconcile("g", "L", scope, events, Settings{ToleranceUnits: 2}); !lb.Closed || lb.Allowed != 2 {
		t.Fatalf("units tolerance: %+v", lb)
	}
	// 5 percent of the 5 serials that entered custody rounds up to one unit.
	if lb = Reconcile("g", "L", scope, events, Settings{TolerancePercent: 50}); lb.Allowed != 3 || !lb.Closed {
		t.Fatalf("percent tolerance: %+v", lb)
	}
	// More anomalies than are listed are counted.
	var many []scopeSerial
	ev2 := map[string][]evRow{}
	for i := 0; i < maxAnomalies+5; i++ {
		id := string(rune('A'+i%26)) + string(rune('a'+i/26))
		many = append(many, scopeSerial{EPC: id})
		ev2[id] = []evRow{ev(id, 1, lib.ObjectEvent, "shipping", "")}
	}
	if lb = Reconcile("g", "", many, ev2, Settings{}); len(lb.Anomalies) != maxAnomalies || lb.AnomaliesMore != 5 {
		t.Fatalf("anomaly cap: %d %d", len(lb.Anomalies), lb.AnomaliesMore)
	}
}

func TestGroupByLot(t *testing.T) {
	lots, by := groupByLot([]scopeSerial{{EPC: "1", Lot: "B"}, {EPC: "2", Lot: ""}, {EPC: "3", Lot: "B"}, {EPC: "4", Lot: "A"}})
	if strings.Join(lots, ",") != ",A,B" || len(by["B"]) != 2 {
		t.Fatalf("%v %v", lots, by)
	}
}

func TestDetectGaps(t *testing.T) {
	sc := scopeSerial{EPC: "s", Lot: "L"}
	kinds := func(evs ...evRow) map[string]string {
		m := map[string]string{}
		for _, g := range DetectGaps(sc, evs) {
			m[g.Kind] = g.Severity
		}
		return m
	}
	ship := func(id string, h int, loc string, dest ...string) evRow {
		e := ev(id, h, lib.ObjectEvent, "shipping", loc)
		e.Dest = dest
		return e
	}
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), ship("2", 1, ga, gb), ev("3", 2, lib.ObjectEvent, "receiving", gb)); len(m) != 0 {
		t.Fatalf("clean transfer reported: %v", m)
	}
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), ev("2", 1, lib.ObjectEvent, "storing", gc)); m[GapNoLink] != "high" {
		t.Fatalf("teleport: %v", m)
	}
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), ship("2", 1, ga, gb), ev("3", 2, lib.ObjectEvent, "storing", gb)); m[GapNotReceived] != "medium" {
		t.Fatalf("missing receipt: %v", m)
	}
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), ev("2", 2, lib.ObjectEvent, "receiving", gb)); m[GapNoShipment] != "low" {
		t.Fatalf("missing shipment: %v", m)
	}
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), ship("2", 1, "", gb), ev("3", 2, lib.ObjectEvent, "receiving", gc)); m[GapDestinationMiss] != "medium" {
		t.Fatalf("destination mismatch: %v", m)
	}
	// A receiving event at the stated destination, in URN form, is fine.
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), ship("2", 1, "", "urn:epc:id:sgln:0614141.00002.0"), ev("3", 2, lib.ObjectEvent, "receiving", gb)); len(m) != 0 {
		t.Fatalf("matching destination reported: %v", m)
	}
	dep := ev("2", 1, lib.ObjectEvent, "departing", ga)
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), dep, ev("3", 2, lib.ObjectEvent, "arriving", gb)); len(m) != 0 {
		t.Fatalf("departing/arriving: %v", m)
	}
	des := ev("2", 1, lib.ObjectEvent, "destroying", ga)
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), des, ev("3", 2, lib.ObjectEvent, "storing", ga), ev("4", 3, lib.ObjectEvent, "storing", ga)); m[GapAfterEndOfLife] != "medium" {
		t.Fatalf("after end of life: %v", m)
	}
	gaps := DetectGaps(sc, []evRow{ev("1", 0, lib.ObjectEvent, "commissioning", ga), des, ev("3", 2, lib.ObjectEvent, "storing", ga), ev("4", 3, lib.ObjectEvent, "storing", ga)})
	if len(gaps) != 1 {
		t.Fatalf("end-of-life reported once: %+v", gaps)
	}
	// Events without a location do not move the serial.
	if m := kinds(ev("1", 0, lib.ObjectEvent, "commissioning", ga), ev("2", 1, lib.ObjectEvent, "inspecting", ""), ev("3", 2, lib.ObjectEvent, "storing", ga)); len(m) != 0 {
		t.Fatalf("locationless event: %v", m)
	}
}

func TestLotGapsAndSummary(t *testing.T) {
	scope := []scopeSerial{{EPC: "x", Lot: "L"}, {EPC: "y", Lot: "L"}}
	events := map[string][]evRow{
		"x": {ev("1", 0, lib.ObjectEvent, "commissioning", ga), ev("2", 1, lib.ObjectEvent, "storing", gc)},
		"y": {ev("3", 0, lib.ObjectEvent, "commissioning", ga), ev("4", 1, lib.ObjectEvent, "receiving", gb)},
	}
	lg := lotGaps("g", "L", scope, events, "")
	if lg.Summary.High != 1 || lg.Summary.Low != 1 || lg.Gaps[0].Severity != "high" {
		t.Fatalf("%+v", lg)
	}
	if lg = lotGaps("g", "L", scope, events, "medium"); len(lg.Gaps) != 1 || lg.Summary.Low != 0 {
		t.Fatalf("min severity: %+v", lg)
	}
	sum, top := summarize(lg.Gaps)
	if sum.High != 1 || top != "high" {
		t.Fatalf("%+v %q", sum, top)
	}
	// The listing is capped; the count of the rest is kept.
	big := map[string][]evRow{}
	var many []scopeSerial
	for i := 0; i < maxGapsListed+3; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+i/676))
		many = append(many, scopeSerial{EPC: id})
		big[id] = []evRow{ev(id+"1", 0, lib.ObjectEvent, "commissioning", ga), ev(id+"2", 1, lib.ObjectEvent, "storing", gc)}
	}
	if lg = lotGaps("g", "", many, big, ""); len(lg.Gaps) != maxGapsListed || lg.NotListed != 3 {
		t.Fatalf("cap: %d %d", len(lg.Gaps), lg.NotListed)
	}
	if sevRank("") != 0 || sevRank("low") != 1 {
		t.Fatal("sevRank")
	}
	if _, err := validSeverity("x"); err == nil {
		t.Fatal("severity")
	}
	if s, err := validSeverity(""); err != nil || s != "" {
		t.Fatal("empty severity")
	}
	if _, err := normalizeGTIN("12345678901234"); err == nil {
		t.Fatal("gtin")
	}
}

func TestAttributeEventsThroughAggregation(t *testing.T) {
	// Case C1 holds serial s from hour 1 to hour 5; pallet P holds C1 from hour 2 onward.
	rows := []evRow{
		ev("comm", 0, lib.ObjectEvent, "commissioning", ga, "s"),
		{ID: "pack", Type: lib.AggregationEvent, Action: "ADD", Parent: "C1", Time: at(1), Recorded: at(1), EPCs: []string{"C1", "s"}},
		{ID: "palletize", Type: lib.AggregationEvent, Action: "ADD", Parent: "P", Time: at(2), Recorded: at(2), EPCs: []string{"P", "C1"}},
		ev("ship", 3, lib.ObjectEvent, "shipping", ga, "P"),
		{ID: "unpack", Type: lib.AggregationEvent, Action: "DELETE", Parent: "C1", Time: at(5), Recorded: at(5), EPCs: []string{"C1", "s"}},
		ev("late", 6, lib.ObjectEvent, "shipping", ga, "C1"), // after s left the case
		ev("pre", 0, lib.ObjectEvent, "storing", ga, "P"),    // before s was in the pallet
	}
	calls := 0
	got, err := attributeEvents([]string{"s"}, func(epcs []string) ([]evRow, error) {
		calls++
		var out []evRow
		for _, r := range rows {
			for _, e := range r.EPCs {
				for _, w := range epcs {
					if e == w {
						out = append(out, r)
						goto next
					}
				}
			}
		next:
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got["s"] {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "comm,pack,palletize,ship,unpack" {
		t.Fatalf("attributed events: %v", ids)
	}
	if !got["s"][2].Via || got["s"][0].Via || calls < 3 {
		t.Fatalf("via flags / rounds: %+v calls=%d", got["s"], calls)
	}
	// Unpacking a parent without listing children closes every child.
	rows2 := []evRow{
		{ID: "pack", Type: lib.AggregationEvent, Action: "ADD", Parent: "C1", Time: at(1), Recorded: at(1), EPCs: []string{"C1", "s"}},
		{ID: "unpack", Type: lib.AggregationEvent, Action: "DELETE", Parent: "C1", Time: at(2), Recorded: at(2), EPCs: []string{"C1"}},
		ev("ship1", 1, lib.ObjectEvent, "shipping", "", "C1"),
		ev("ship3", 3, lib.ObjectEvent, "shipping", "", "C1"),
	}
	fetch := func(epcs []string) ([]evRow, error) {
		var out []evRow
		for _, r := range rows2 {
			for _, e := range r.EPCs {
				for _, w := range epcs {
					if e == w {
						out = append(out, r)
					}
				}
			}
		}
		return out, nil
	}
	got, err = attributeEvents([]string{"s"}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, e := range got["s"] {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "pack,ship1,unpack" {
		t.Fatalf("unpack all: %v", ids)
	}
	if _, err := attributeEvents([]string{"s"}, func([]string) ([]evRow, error) { return nil, errors.New("boom") }); err == nil {
		t.Fatal("fetch errors must surface")
	}
}

func TestForward(t *testing.T) {
	own := map[string]bool{"9506000000015": true}
	scope := []scopeSerial{{EPC: "https://id.gs1.org/01/09506000134352/21/A"}, {EPC: "https://id.gs1.org/01/09506000134352/21/B"}, {EPC: "https://id.gs1.org/01/09506000134352/21/C"},
		{EPC: "https://id.gs1.org/01/09506000134352/21/D"}, {EPC: "https://id.gs1.org/01/09506000134352/21/E"}, {EPC: "https://id.gs1.org/01/09506000134352/21/F"},
		{EPC: "https://id.gs1.org/01/09506000134352/21/G"}, {EPC: "https://id.gs1.org/01/09506000134352/21/H"}}
	k := func(s string) string { return "https://id.gs1.org/01/09506000134352/21/" + s }
	shipTo := func(id string, h int, dest string) evRow {
		e := ev(id, h, lib.ObjectEvent, "shipping", "")
		if dest != "" {
			e.Dest = []string{dest}
		}
		return e
	}
	events := map[string][]evRow{
		k("A"): {ev("1", 0, lib.ObjectEvent, "commissioning", ga)},                                                                                         // own stock
		k("B"): {ev("2", 0, lib.ObjectEvent, "commissioning", ga), shipTo("3", 1, "urn:epc:id:sgln:0614141.00002.0")},                                      // at B
		k("C"): {ev("4", 0, lib.ObjectEvent, "commissioning", ga), shipTo("5", 1, gb), ev("6", 2, lib.ObjectEvent, "receiving", gb)},                       // received at B
		k("D"): {ev("7", 0, lib.ObjectEvent, "commissioning", ga), shipTo("8", 1, gb), ev("9", 2, lib.ObjectEvent, "receiving", ga)},                       // returned home
		k("E"): {ev("10", 0, lib.ObjectEvent, "commissioning", ga), shipTo("11", 1, "")},                                                                   // no destination
		k("F"): {ev("12", 0, lib.ObjectEvent, "commissioning", ga), {ID: "13", Type: lib.ObjectEvent, Action: "DELETE", Time: at(1)}, shipTo("14", 2, gc)}, // ended
		k("G"): {ev("15", 0, lib.ObjectEvent, "commissioning", ga), {ID: "16", Type: lib.ObjectEvent, BizStep: "dispensing", Time: at(1)}},                 // dispensed
		// H has no custody event
		k("H"): {ev("17", 0, lib.ObjectEvent, "storing", gc)},
	}
	p := Forward(scope, events, own, t0)
	s := p.Scope
	if s.Packs != 8 || s.OwnStock != 2 || s.AtHolders != 2 || s.NoDestination != 1 || s.NoCustodyRecord != 1 || s.Ended["decommissioned"] != 1 || s.Ended["dispensed"] != 1 {
		t.Fatalf("scope: %+v", s)
	}
	if len(p.Holders) != 1 || p.Holders[0].packs != 2 || p.Holders[0].id != gb || len(p.Holders[0].serials) != 2 {
		t.Fatalf("holders: %+v", p.Holders)
	}
	// Holders with equal packs are ordered by identifier.
	scope2 := []scopeSerial{{EPC: k("A")}, {EPC: k("B")}}
	ev2 := map[string][]evRow{k("A"): {shipTo("1", 1, gc)}, k("B"): {shipTo("2", 1, gb)}}
	if p = Forward(scope2, ev2, own, t0); len(p.Holders) != 2 || p.Holders[0].id != gb {
		t.Fatalf("order: %+v", p.Holders)
	}
}

func TestRecallReport(t *testing.T) {
	sent := t0.Add(time.Hour)
	rc := LotRecall{GTIN: "09506000134352", Lot: "L1", Title: "T", Reason: "R", Status: "open", CreatedAt: t0, CreatedBy: "user:1", Scope: Scope{AsOf: t0, Packs: 3},
		Holders: []RecallHolder{
			{Name: "B", Packs: 2, Status: HolderSent, SentAt: &sent},
			{Name: "C", Packs: 1, Status: HolderPending},
			{Name: "D", Packs: 4, Status: HolderAcknowledged},
			{Name: "E", Packs: 0, Status: HolderPending},
		}}
	rep := BuildRecallReport(rc, "ACME", t0.Add(5*time.Hour))
	d := rep.Helper.Derived
	if d.HoldersTotal != 4 || d.Pending != 2 || d.Sent != 1 || d.Acknowledged != 1 || d.PacksAtHolders != 7 || d.PacksAcknowledged != 4 || d.HoursSinceRecall != 5 ||
		d.OldestUnacknowledgedHours == nil || *d.OldestUnacknowledgedHours != 5 {
		t.Fatalf("derived: %+v", d)
	}
	if rep.ResponseTime.Configured || !strings.Contains(rep.ResponseTime.Note, "TODO(spec)") || rep.Data.Holders == nil {
		t.Fatalf("report: %+v", rep.ResponseTime)
	}
	if got := BuildRecallReport(LotRecall{CreatedAt: t0}, "x", t0); got.Data.Holders == nil || len(got.Data.Holders) != 0 {
		t.Fatal("empty holders must be a list")
	}
	if sanitizeFile("A/b c-1") != "A_b_c-1" {
		t.Fatal("sanitizeFile")
	}
	if round1(1.26) != 1.3 {
		t.Fatal("round1")
	}
}

func TestIsDispensedAndDid(t *testing.T) {
	if !isDispensed(evRow{BizStep: "retail_selling"}) || !isDispensed(evRow{Disposition: "dispensed"}) || isDispensed(evRow{BizStep: "storing"}) {
		t.Fatal("isDispensed")
	}
	for in, want := range map[string]string{"https://node.example.org:8443/x/y": "did:web:node.example.org%3A8443:x:y", "https://node.example.org": "did:web:node.example.org", "::bad": "did:web:localhost"} {
		if got := didFor(in); got != want {
			t.Errorf("didFor(%q) = %q, want %q", in, got, want)
		}
	}
	if hexOf([]byte{0, 0xab, 0xff}) != "00abff" {
		t.Fatal("hexOf")
	}
}

func TestFoldAndFinalStates(t *testing.T) {
	events := []lib.Event{
		{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: t0, EPCList: []string{"urn:epc:id:sgtin:9506000.013435.A1"},
			BizLocation: &lib.Location{ID: "urn:epc:id:sgln:9506000.00001.0"}, ILMD: map[string]any{"lotNumber": "L1", "itemExpirationDate": "2030-01-01"}},
		{Type: lib.AggregationEvent, Action: "ADD", EventTime: t0.Add(time.Hour), ParentID: "urn:epc:id:sscc:9506000.1234567890", ChildEPCs: []string{"urn:epc:id:sgtin:9506000.013435.A1"}},
		{Type: lib.TransactionEvent, Action: "ADD", EventTime: t0.Add(2 * time.Hour), ParentID: "urn:epc:id:sscc:9506000.1234567890", EPCList: []string{"urn:epc:id:sgtin:9506000.013435.A2"}, Disposition: "in_transit"},
		{Type: lib.TransformationEvent, EventTime: t0.Add(3 * time.Hour), InputEPCList: []string{"urn:epc:id:sgtin:9506000.013435.A2"}, OutputEPCList: []string{"urn:epc:id:sgtin:9506000.013435.A3"}},
		{Type: lib.AssociationEvent, Action: "ADD", EventTime: t0.Add(4 * time.Hour), ParentID: "urn:epc:id:sscc:9506000.1234567890", ChildEPCs: []string{"urn:epc:id:sgtin:9506000.013435.A4"}},
		{Type: lib.AssociationEvent, Action: "DELETE", EventTime: t0.Add(5 * time.Hour), ChildEPCs: []string{"urn:epc:id:sgtin:9506000.013435.A4"}},
		{Type: lib.ObjectEvent, Action: "DELETE", EventTime: t0.Add(6 * time.Hour), EPCList: []string{"urn:epc:id:sgtin:9506000.013435.A1"}},
		{Type: lib.AggregationEvent, Action: "DELETE", EventTime: t0.Add(7 * time.Hour), ParentID: "urn:epc:id:sscc:9506000.1234567890", ChildEPCs: []string{"urn:epc:id:sgtin:9506000.013435.A1"}},
	}
	fs := FinalStates(events)
	a1 := fs[lib.Canonical("urn:epc:id:sgtin:9506000.013435.A1")]
	if a1.Status != "decommissioned" || !a1.ParentClear {
		t.Fatalf("A1: %+v", a1)
	}
	if fs[lib.Canonical("urn:epc:id:sgtin:9506000.013435.A2")].Status != "transformed" || fs[lib.Canonical("urn:epc:id:sgtin:9506000.013435.A3")].Status != "active" {
		t.Fatalf("transformation: %+v", fs)
	}
	if a4 := fs[lib.Canonical("urn:epc:id:sgtin:9506000.013435.A4")]; !a4.ParentClear {
		t.Fatalf("association: %+v", a4)
	}
	if sp("") != nil || *sp("x") != "x" {
		t.Fatal("sp")
	}
}
