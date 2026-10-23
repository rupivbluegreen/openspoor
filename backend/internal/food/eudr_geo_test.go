package food

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func codes(r PlotReport) map[string]string {
	m := map[string]string{}
	for _, x := range r.Reasons {
		m[x.Code] = x.Severity
	}
	return m
}

func check(t *testing.T, raw string, ha float64, commodity string, want map[string]string, status string) PlotReport {
	t.Helper()
	rep := ValidateGeometry(json.RawMessage(raw), ha, commodity, DefaultGeoRules)
	got := codes(rep)
	for c, sev := range want {
		if got[c] != sev {
			t.Errorf("%s: want %s=%s, got %v", raw, c, sev, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%s: reasons %v, want %v", raw, got, want)
	}
	if rep.Status != status {
		t.Errorf("%s: status %s, want %s", raw, rep.Status, status)
	}
	return rep
}

func TestValidateGeometry(t *testing.T) {
	ccw := `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.000001],[10.001001,50.001001],[10.000001,50.001001],[10.000001,50.000001]]]}`
	rep := check(t, ccw, 0, "coffee", map[string]string{}, "valid")
	if rep.AreaHa < 0.7 || rep.AreaHa > 0.9 || rep.Decimals != 6 || rep.Positions != 5 || rep.GeometryType != "Polygon" {
		t.Errorf("ccw report %+v", rep)
	}
	check(t, `{"type":"Feature","geometry":`+ccw+`}`, rep.AreaHa, "coffee", map[string]string{}, "valid")

	cw := `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.000001,50.001001],[10.001001,50.001001],[10.001001,50.000001],[10.000001,50.000001]]]}`
	check(t, cw, 0, "coffee", map[string]string{"winding_outer_clockwise": SevWarning}, "warnings")

	check(t, `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.000001],[10.001001,50.001001],[10.000001,50.001001]]]}`, 0, "coffee",
		map[string]string{"ring_not_closed": SevError}, "invalid")
	check(t, `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.000001],[10.000001,50.000001]]]}`, 0, "coffee",
		map[string]string{"ring_too_short": SevError}, "invalid")
	check(t, `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.000001],[10.001001,95.001001],[10.000001,50.000001]]]}`, 0, "coffee",
		map[string]string{"coordinate_out_of_range": SevError}, "invalid")
	// Low precision: 2 decimals.
	check(t, `{"type":"Polygon","coordinates":[[[10.01,50.01],[10.11,50.01],[10.11,50.11],[10.01,50.11],[10.01,50.01]]]}`, 0, "coffee",
		map[string]string{"low_precision": SevWarning}, "warnings")
	check(t, `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.500001,50.000001],[10.500001,50.500001],[10.000001,50.500001],[10.000001,50.000001]]]}`, 0, "coffee",
		map[string]string{"area_too_large": SevWarning}, "warnings")
	// Bow-tie self-intersection.
	check(t, `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.001001],[10.001001,50.000001],[10.000001,50.001001],[10.000001,50.000001]]]}`, 0, "coffee",
		map[string]string{"self_intersection": SevError, "zero_area": SevError}, "invalid")
	// Zero area (collinear) and a repeated position.
	check(t, `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.000001],[10.001001,50.000001],[10.000001,50.000001]]]}`, 0, "coffee",
		map[string]string{"zero_area": SevError, "duplicate_positions": SevWarning}, "invalid")
	// Stored area far from the computed one.
	check(t, ccw, 100, "coffee", map[string]string{"area_mismatch": SevWarning}, "warnings")
	// Hole winding (hole counterclockwise) is reported, holes are noted.
	hole := `{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.000001],[10.001001,50.001001],[10.000001,50.001001],[10.000001,50.000001]],
		[[10.000201,50.000201],[10.000401,50.000201],[10.000401,50.000401],[10.000201,50.000401],[10.000201,50.000201]]]}`
	check(t, hole, 0, "coffee", map[string]string{"holes_ignored": SevInfo, "winding_hole_counterclockwise": SevWarning}, "warnings")
	// Antimeridian-spanning edge.
	check(t, `{"type":"Polygon","coordinates":[[[179.000001,10.000001],[-179.000001,10.000001],[-179.000001,11.000001],[179.000001,11.000001],[179.000001,10.000001]]]}`, 0, "coffee",
		map[string]string{"antimeridian_crossing": SevWarning, "winding_outer_clockwise": SevWarning, "area_too_large": SevWarning}, "warnings")

	// Points: size rule and precision.
	pt := `{"type":"Point","coordinates":[10.000001,50.000001]}`
	check(t, pt, 2, "coffee", map[string]string{}, "valid")
	check(t, pt, 0, "coffee", map[string]string{"point_area_unknown": SevWarning}, "warnings")
	check(t, pt, 9, "coffee", map[string]string{"point_needs_polygon": SevError}, "invalid")
	check(t, pt, 9, "cattle", map[string]string{}, "valid")
	check(t, `{"type":"Point","coordinates":[10.5,50.5]}`, 1, "coffee", map[string]string{"low_precision": SevWarning}, "warnings")
	check(t, `{"type":"Point","coordinates":[1.5e-7,50.0000001]}`, 1, "coffee", map[string]string{}, "valid")
	check(t, `{"type":"Point","coordinates":[1.5e-3,50.0000001]}`, 1, "coffee", map[string]string{"low_precision": SevWarning}, "warnings")
	check(t, `{"type":"Point","coordinates":[200.000001,50.000001]}`, 1, "coffee", map[string]string{"coordinate_out_of_range": SevError}, "invalid")
	check(t, `{"type":"Point","coordinates":[1]}`, 1, "coffee", map[string]string{"invalid_coordinates": SevError}, "invalid")

	// Malformed input.
	check(t, `{`, 0, "coffee", map[string]string{"invalid_geojson": SevError}, "invalid")
	check(t, `{"type":"Feature"}`, 0, "coffee", map[string]string{"invalid_geojson": SevError}, "invalid")
	check(t, `{"type":"Feature","geometry":3}`, 0, "coffee", map[string]string{"invalid_geojson": SevError}, "invalid")
	check(t, `{"type":"MultiPolygon","coordinates":[]}`, 0, "coffee", map[string]string{"unsupported_geometry": SevError}, "invalid")
	check(t, `{"type":"Polygon","coordinates":[]}`, 0, "coffee", map[string]string{"invalid_coordinates": SevError}, "invalid")
	check(t, `{"type":"Polygon","coordinates":[[[1]]]}`, 0, "coffee", map[string]string{"invalid_coordinates": SevError}, "invalid")
	check(t, `{"type":"Polygon","coordinates":[[["a","b"],[1,2],[3,4],["a","b"]]]}`, 0, "coffee", map[string]string{"invalid_coordinates": SevError}, "invalid")
	check(t, `{"type":"Point","coordinates":["a","b"]}`, 0, "coffee", map[string]string{"invalid_coordinates": SevError}, "invalid")

	// The quadratic check is skipped above the position cap.
	rules := DefaultGeoRules
	rules.MaxPositions = 4
	big := ValidateGeometry(json.RawMessage(`{"type":"Polygon","coordinates":[[[10.000001,50.000001],[10.001001,50.000001],[10.001001,50.001001],[10.000501,50.002001],[10.000001,50.001001],[10.000001,50.000001]]]}`), 0, "coffee", rules)
	if codes(big)["check_skipped_too_many_positions"] != SevWarning {
		t.Errorf("cap: %+v", big.Reasons)
	}
}

func TestDecimalsOf(t *testing.T) {
	for in, want := range map[string]int{"1": 0, "1.25": 2, "-0.000001": 6, "1.5e-7": 8, "2e3": 0, "1.25E+1": 1, "5e-1": 1} {
		if got := decimalsOf(json.Number(in)); got != want {
			t.Errorf("decimalsOf(%s) = %d, want %d", in, got, want)
		}
	}
}

func TestReasonCodesCovered(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range ReasonCodes {
		seen[c] = true
	}
	for _, c := range []string{"overlap", "self_intersection", "low_precision", "point_needs_polygon"} {
		if !seen[c] {
			t.Errorf("missing code %s", c)
		}
	}
}

func fp(raw string) footprint {
	f, ok := outerFootprint(json.RawMessage(raw))
	if !ok {
		panic("bad footprint " + raw)
	}
	return f
}

func square(x, y, s float64) string {
	b, _ := json.Marshal(map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{x, y}, {x + s, y}, {x + s, y + s}, {x, y + s}, {x, y}}}})
	return string(b)
}

func TestOverlapKinds(t *testing.T) {
	a := fp(square(0, 0, 10))
	for _, c := range []struct {
		name string
		b    footprint
		want string
	}{
		{"apart", fp(square(20, 20, 5)), ""},
		{"crossing", fp(square(5, 5, 10)), "boundary_crossing"},
		{"touching", fp(square(10, 0, 5)), "boundary_crossing"},
		{"inside", fp(square(2, 2, 3)), "contains"},
		{"around", fp(square(-5, -5, 30)), "contained"},
		{"point inside", fp(`{"type":"Point","coordinates":[5,5]}`), "contains"},
		{"point outside", fp(`{"type":"Point","coordinates":[50,5]}`), ""},
	} {
		if got := overlapKind(a, c.b, 100); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	p := fp(`{"type":"Point","coordinates":[5,5]}`)
	if overlapKind(p, a, 100) != "contained" || overlapKind(p, fp(square(50, 50, 1)), 100) != "" {
		t.Error("point against polygon")
	}
	if overlapKind(p, fp(`{"type":"Point","coordinates":[5,5]}`), 100) != "contained" || overlapKind(p, fp(`{"type":"Point","coordinates":[6,5]}`), 100) != "" {
		t.Error("point against point")
	}
	if overlapKind(a, fp(square(5, 5, 10)), 2) != "" {
		t.Error("cap skips")
	}
	for _, raw := range []string{`{`, `{"type":"Line"}`, `{"type":"Point","coordinates":[500,1]}`, `{"type":"Point","coordinates":[1]}`, `{"type":"Polygon","coordinates":[]}`,
		`{"type":"Polygon","coordinates":[[[1,1],[2,2],[3,3]]]}`, `{"type":"Polygon","coordinates":[[[1,1],[2],[3,3],[1,1]]]}`, `{"type":"Polygon","coordinates":[[[1,1],[2,2],[3,3],[4,4]]]}`,
		`{"type":"Polygon","coordinates":[[[1,1],[2,2],[3,3],[1,200]]]}`, `{"type":"Feature","geometry":3}`} {
		if _, ok := outerFootprint(json.RawMessage(raw)); ok {
			t.Errorf("footprint accepted %s", raw)
		}
	}
}

func TestDetectOverlaps(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	geoms := map[uuid.UUID]json.RawMessage{ids[0]: json.RawMessage(square(0, 0, 10)), ids[1]: json.RawMessage(square(5, 5, 10)), ids[2]: json.RawMessage(square(50, 50, 1)), ids[3]: json.RawMessage(`{`)}
	reps := []PlotReport{}
	for i, id := range ids {
		reps = append(reps, PlotReport{PlotID: id, Name: string(rune('A' + i)), Reasons: []Reason{}, Overlaps: []Overlap{}, Status: "valid"})
	}
	DetectOverlaps(reps, geoms, 100)
	if len(reps[0].Overlaps) != 1 || reps[0].Overlaps[0].PlotID != ids[1] || reps[0].Status != "warnings" || codes(reps[0])["overlap"] != SevWarning {
		t.Errorf("A: %+v", reps[0])
	}
	if len(reps[1].Overlaps) != 1 || len(reps[2].Overlaps) != 0 || len(reps[3].Overlaps) != 0 || reps[2].Status != "valid" {
		t.Errorf("others: %+v", reps)
	}
}

func TestMeanKineticTemperature(t *testing.T) {
	c, err := MeanKineticTemperature([]float64{5, 5, 5}, DefaultActivationEnergyKJ)
	if err != nil || math.Abs(c-5) > 1e-9 {
		t.Fatalf("constant: %v %v", c, err)
	}
	// Independent evaluation of the formula for 20 and 30 C at 83.144 kJ/mol.
	k := 83.144 / 0.0083144598
	want := k/-math.Log((math.Exp(-k/293.15)+math.Exp(-k/303.15))/2) - 273.15
	got, err := MeanKineticTemperature([]float64{20, 30}, 83.144)
	if err != nil || math.Abs(got-want) > 1e-9 || got <= 25 || got >= 30 {
		t.Fatalf("mkt %v want %v (above the 25 C mean, below the 30 C maximum) err %v", got, want, err)
	}
	// A higher activation energy weights warm readings more.
	hi, _ := MeanKineticTemperature([]float64{20, 30}, 150)
	if hi <= got {
		t.Errorf("higher dH should raise MKT: %v <= %v", hi, got)
	}
	for _, bad := range []struct {
		t  []float64
		ea float64
	}{{nil, 83}, {[]float64{5}, 0}, {[]float64{5}, 2000}, {[]float64{-300}, 83}, {[]float64{math.NaN()}, 83}, {[]float64{5}, math.NaN()}, {[]float64{-272.9}, 1000}} {
		if _, err := MeanKineticTemperature(bad.t, bad.ea); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestStats(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var rs []Reading
	for i, c := range []float64{4, 5, 9, 6} {
		rs = append(rs, Reading{At: t0.Add(time.Duration(i) * 15 * time.Minute), TempC: c})
	}
	st, err := Stats(rs, 0)
	if err != nil || st.Count != 4 || st.MinC != 4 || st.MaxC != 9 || st.MeanC != 6 || !st.EvenSampling || st.ActivationKJ != DefaultActivationEnergyKJ || st.MKTC < st.MeanC || st.MKTC > 9 {
		t.Fatalf("%+v %v", st, err)
	}
	rs[3].At = t0.Add(10 * time.Hour)
	if st, _ = Stats(rs, 80); st.EvenSampling || st.ActivationKJ != 80 {
		t.Errorf("uneven: %+v", st)
	}
	if _, err := Stats(nil, 0); err == nil {
		t.Error("empty readings")
	}
}

func TestBuildChecklist(t *testing.T) {
	yes, no := true, false
	pid, p2 := uuid.New(), uuid.New()
	d := DDS{ID: uuid.New(), Kind: KindStatement, Commodity: "coffee", HSCode: "0901", Description: "beans", Quantity: 5, CountryOfProduction: "CO", PlotIDs: []uuid.UUID{pid},
		Payload: map[string]any{"declaration": "x"}}
	plots := []Plot{{ID: pid, Name: "A", Commodity: "coffee", Country: "CO", DeforestationFree: &yes}}
	reps := map[uuid.UUID]PlotReport{pid: {PlotID: pid, Status: "valid"}}
	status := func(c Checklist, key string) string {
		for _, it := range c.Items {
			if it.Key == key {
				return it.Status
			}
		}
		return "absent"
	}
	c := BuildChecklist(d, plots, reps)
	if !c.Complete || len(c.Notes) == 0 {
		t.Fatalf("complete: %+v", c)
	}
	// Problems.
	d2 := d
	d2.Commodity, d2.HSCode, d2.Description, d2.Quantity, d2.CountryOfProduction, d2.Payload = "tea", "", "", 0, "COL", map[string]any{}
	plots2 := []Plot{{ID: pid, Name: "A", Commodity: "coffee", Country: "BR", DeforestationFree: &no}, {ID: p2, Name: "B", Commodity: "coffee", Country: "CO"}}
	d2.PlotIDs = []uuid.UUID{pid, p2, uuid.New()}
	reps2 := map[uuid.UUID]PlotReport{pid: {Status: "invalid", Overlaps: []Overlap{{Name: "B"}}}, p2: {Status: "warnings"}}
	c = BuildChecklist(d2, plots2, reps2)
	for _, k := range []string{"commodity", "hs_code", "description", "quantity", "country", "geolocation", "plots_valid", "plots_commodity", "deforestation", "declaration"} {
		if status(c, k) != CheckMissing {
			t.Errorf("%s = %s", k, status(c, k))
		}
	}
	if status(c, "plots_no_overlap") != CheckWarning || status(c, "plots_country") != CheckWarning || c.Complete {
		t.Errorf("warnings: %+v", c)
	}
	// Warnings only: unknown deforestation assessment, plot warnings.
	plots3 := []Plot{{ID: pid, Name: "A", Commodity: "coffee", Country: "CO"}}
	c = BuildChecklist(d, plots3, map[uuid.UUID]PlotReport{pid: {Status: "warnings"}})
	if status(c, "plots_valid") != CheckWarning || status(c, "deforestation") != CheckWarning || !c.Complete {
		t.Errorf("warn-only: %+v", c)
	}
	// Downstream reference.
	ref := DDS{ID: uuid.New(), Kind: KindUpstreamReference, Commodity: "coffee", HSCode: "0901", Description: "beans", Quantity: 1, CountryOfProduction: "CO"}
	c = BuildChecklist(ref, nil, nil)
	if c.Complete || status(c, "upstream_reference") != CheckMissing || status(c, "geolocation") != CheckNotApplicable {
		t.Errorf("ref missing: %+v", c)
	}
	ref.UpstreamReference = "R-1"
	c = BuildChecklist(ref, nil, nil)
	if !c.Complete || status(c, "upstream_operator") != CheckWarning {
		t.Errorf("ref: %+v", c)
	}
	ref.UpstreamOperator = "Op"
	if c = BuildChecklist(ref, nil, nil); status(c, "upstream_operator") != CheckOK {
		t.Errorf("ref op: %+v", c)
	}
}

func TestDispositionAllowed(t *testing.T) {
	for _, c := range []struct {
		from, to string
		ok       bool
	}{{DispOpen, DispQuarantine, true}, {DispOpen, DispRelease, true}, {DispOpen, DispDestroy, true}, {DispQuarantine, DispRelease, true}, {DispQuarantine, DispDestroy, true},
		{DispQuarantine, DispQuarantine, false}, {DispRelease, DispDestroy, false}, {DispDestroy, DispRelease, false}} {
		if dispositionAllowed(c.from, c.to) != c.ok {
			t.Errorf("%s -> %s", c.from, c.to)
		}
	}
	if got := uniqueTrimmed([]string{" a", "a", "", "b"}); len(got) != 2 {
		t.Errorf("dedupe %v", got)
	}
}
