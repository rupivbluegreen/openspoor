package food

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// GeoRules are the thresholds of the plot geolocation checks. They are engineering defaults
// of this software, not statements of law; every one that stands in for a legal figure is
// marked TODO(spec) where it is declared.
type GeoRules struct {
	// MinDecimals is the fewest decimal digits a coordinate should carry.
	// TODO(spec): confirm the required precision against Regulation (EU) 2023/1115 (Art. 2 and
	// Art. 9) and the Commission's guidance in the Official Journal; 6 is a working default.
	MinDecimals int
	// PointMaxHa is the largest plot that may be described by a single point; bigger plots
	// need a polygon. TODO(spec): confirm the threshold and any commodity exception (cattle is
	// treated as exempt here, as the plot registry did before). 4 ha is a working default.
	PointMaxHa float64
	// MaxAreaHa is a sanity ceiling for one plot, to catch swapped or mistyped coordinates.
	// It is not a legal limit.
	MaxAreaHa float64
	// AreaTolerancePct is how far a reported area may differ from the computed one.
	AreaTolerancePct float64
	// MaxPositions caps the quadratic self-intersection and overlap checks.
	MaxPositions int
}

// DefaultGeoRules are the values used by the plot validation report.
var DefaultGeoRules = GeoRules{MinDecimals: 6, PointMaxHa: 4, MaxAreaHa: 100000, AreaTolerancePct: 10, MaxPositions: 5000}

// Severities of a validation reason.
const (
	SevError   = "error"
	SevWarning = "warning"
	SevInfo    = "info"
)

// Reason is one machine-readable finding of a plot validation. Codes are stable; messages are
// for people.
type Reason struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Message  string         `json:"message"`
	Detail   map[string]any `json:"detail,omitempty"`
}

// Overlap says another plot of the same tenant occupies part of this one.
type Overlap struct {
	PlotID uuid.UUID `json:"plotId"`
	Name   string    `json:"name"`
	// Kind is "boundary_crossing", "contained" or "contains" (this plot inside / around the other).
	Kind string `json:"kind"`
}

// PlotReport is the validation report of one plot.
type PlotReport struct {
	PlotID         uuid.UUID `json:"plotId"`
	Name           string    `json:"name"`
	Commodity      string    `json:"commodity"`
	GeometryType   string    `json:"geometryType"`
	Status         string    `json:"status"` // valid | warnings | invalid
	AreaHa         float64   `json:"areaHa"`
	ReportedAreaHa float64   `json:"reportedAreaHa"`
	Decimals       int       `json:"decimals"`
	Positions      int       `json:"positions"`
	Reasons        []Reason  `json:"reasons"`
	Overlaps       []Overlap `json:"overlaps"`
}

// ReasonCodes lists every code the validation can emit, for clients and documentation.
var ReasonCodes = []string{"invalid_geojson", "unsupported_geometry", "invalid_coordinates", "coordinate_out_of_range", "ring_too_short", "ring_not_closed",
	"duplicate_positions", "winding_outer_clockwise", "winding_hole_counterclockwise", "low_precision", "self_intersection", "antimeridian_crossing",
	"zero_area", "area_too_large", "area_mismatch", "point_needs_polygon", "point_area_unknown", "check_skipped_too_many_positions", "holes_ignored", "overlap"}

type pos struct{ x, y float64 }

// rawGeom reads coordinates as json.Number so the decimals of what was written can be counted.
type rawGeom struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
	Geometry    json.RawMessage `json:"geometry"`
}

// decimalsOf counts the digits after the decimal point of a JSON number as written.
func decimalsOf(n json.Number) int {
	s := strings.ToLower(n.String())
	if i := strings.IndexAny(s, "e"); i >= 0 {
		// 1.5e-7 -> mantissa decimals minus exponent
		mant, exp := s[:i], 0
		_, _ = fmt.Sscanf(s[i+1:], "%d", &exp)
		d := 0
		if j := strings.Index(mant, "."); j >= 0 {
			d = len(mant) - j - 1
		}
		d -= exp
		if d < 0 {
			d = 0
		}
		return d
	}
	if j := strings.Index(s, "."); j >= 0 {
		return len(s) - j - 1
	}
	return 0
}

func decodeNumbers(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

// ValidateGeometry checks one GeoJSON geometry (or a Feature holding one) against the rules and
// returns the reasons plus the computed area. reportedHa is the area stored for the plot (used
// for the mismatch check and, for a point, the point-versus-polygon rule).
func ValidateGeometry(raw json.RawMessage, reportedHa float64, commodity string, rules GeoRules) (rep PlotReport) {
	rep.Reasons = []Reason{}
	rep.Overlaps = []Overlap{}
	rep.ReportedAreaHa = reportedHa
	add := func(code, sev, msg string, detail map[string]any) {
		rep.Reasons = append(rep.Reasons, Reason{Code: code, Severity: sev, Message: msg, Detail: detail})
	}
	defer func() { rep.Status = statusOf(rep.Reasons) }()

	var g rawGeom
	if err := json.Unmarshal(raw, &g); err != nil {
		add("invalid_geojson", SevError, "the geolocation is not valid JSON", nil)
		return
	}
	if g.Type == "Feature" {
		if len(g.Geometry) == 0 {
			add("invalid_geojson", SevError, "the Feature has no geometry", nil)
			return
		}
		if err := json.Unmarshal(g.Geometry, &g); err != nil {
			add("invalid_geojson", SevError, "the Feature geometry is not valid JSON", nil)
			return
		}
	}
	rep.GeometryType = g.Type
	switch g.Type {
	case "Point":
		var c []json.Number
		if err := decodeNumbers(g.Coordinates, &c); err != nil || len(c) < 2 {
			add("invalid_coordinates", SevError, "a point is [longitude, latitude]", nil)
			return
		}
		p, ok := toPos(c[0], c[1])
		if !ok {
			add("invalid_coordinates", SevError, "coordinates must be numbers", nil)
			return
		}
		rep.Positions = 1
		checkRange(&rep, []pos{p}, add)
		rep.Decimals = minDecimals([]json.Number{c[0], c[1]})
		checkPrecision(&rep, rules, add)
		switch {
		case reportedHa <= 0:
			add("point_area_unknown", SevWarning, "the plot is a point without an area, so the point-versus-polygon rule cannot be checked", nil)
		case reportedHa > rules.PointMaxHa && commodity != "cattle":
			// TODO(spec): the size threshold and the cattle exception are working defaults.
			add("point_needs_polygon", SevError, fmt.Sprintf("a plot above %.4g ha needs a polygon, not a point", rules.PointMaxHa),
				map[string]any{"thresholdHa": rules.PointMaxHa, "areaHa": reportedHa})
		}
	case "Polygon":
		var rings [][][]json.Number
		if err := decodeNumbers(g.Coordinates, &rings); err != nil || len(rings) == 0 {
			add("invalid_coordinates", SevError, "a polygon needs at least an outer ring of [longitude, latitude] positions", nil)
			return
		}
		validatePolygon(&rep, rings, reportedHa, rules, add)
	default:
		add("unsupported_geometry", SevError, "the geometry must be a Point or a Polygon", map[string]any{"type": g.Type})
	}
	return
}

func statusOf(rs []Reason) string {
	st := "valid"
	for _, r := range rs {
		if r.Severity == SevError {
			return "invalid"
		}
		if r.Severity == SevWarning {
			st = "warnings"
		}
	}
	return st
}

func toPos(a, b json.Number) (pos, bool) {
	x, err1 := a.Float64()
	y, err2 := b.Float64()
	if err1 != nil || err2 != nil || math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return pos{}, false
	}
	return pos{x, y}, true
}

func minDecimals(ns []json.Number) int {
	m := -1
	for _, n := range ns {
		if d := decimalsOf(n); m < 0 || d < m {
			m = d
		}
	}
	if m < 0 {
		return 0
	}
	return m
}

func checkRange(rep *PlotReport, ps []pos, add func(string, string, string, map[string]any)) bool {
	for i, p := range ps {
		if !validLonLat(p.x, p.y) {
			add("coordinate_out_of_range", SevError, "longitude must be within ±180 and latitude within ±90 (WGS84, [longitude, latitude] order)", map[string]any{"index": i, "longitude": p.x, "latitude": p.y})
			return false
		}
	}
	return true
}

func checkPrecision(rep *PlotReport, rules GeoRules, add func(string, string, string, map[string]any)) {
	if rep.Decimals < rules.MinDecimals {
		add("low_precision", SevWarning, fmt.Sprintf("coordinates carry %d decimal digits, fewer than the %d expected", rep.Decimals, rules.MinDecimals),
			map[string]any{"decimals": rep.Decimals, "expected": rules.MinDecimals})
	}
}

func validatePolygon(rep *PlotReport, rings [][][]json.Number, reportedHa float64, rules GeoRules, add func(string, string, string, map[string]any)) {
	var all []json.Number
	conv := make([][]pos, len(rings))
	for ri, ring := range rings {
		for _, c := range ring {
			if len(c) < 2 {
				add("invalid_coordinates", SevError, "every position is [longitude, latitude]", map[string]any{"ring": ri})
				return
			}
			p, ok := toPos(c[0], c[1])
			if !ok {
				add("invalid_coordinates", SevError, "coordinates must be numbers", map[string]any{"ring": ri})
				return
			}
			conv[ri] = append(conv[ri], p)
			all = append(all, c[0], c[1])
		}
		rep.Positions += len(ring)
	}
	if len(rings) > 1 {
		add("holes_ignored", SevInfo, "inner rings (holes) are checked for closure and winding only; area and overlap use the outer ring", map[string]any{"holes": len(rings) - 1})
	}
	rep.Decimals = minDecimals(all)
	outer := conv[0]
	ok := true
	for ri, ring := range conv {
		if len(ring) < 4 {
			add("ring_too_short", SevError, "a ring needs at least 4 positions (3 distinct plus the closing one)", map[string]any{"ring": ri, "positions": len(ring)})
			ok = false
			continue
		}
		if ring[0] != ring[len(ring)-1] {
			add("ring_not_closed", SevError, "a ring must be closed: the last position must equal the first", map[string]any{"ring": ri})
			ok = false
		}
		if !checkRange(rep, ring, add) {
			ok = false
		}
	}
	checkPrecision(rep, rules, add)
	if !ok {
		return
	}
	for ri, ring := range conv {
		dups := 0
		for i := 1; i < len(ring); i++ {
			if ring[i] == ring[i-1] {
				dups++
			}
		}
		if dups > 0 {
			add("duplicate_positions", SevWarning, "consecutive positions repeat", map[string]any{"ring": ri, "count": dups})
		}
		for i := 1; i < len(ring); i++ {
			if math.Abs(ring[i].x-ring[i-1].x) > 180 {
				add("antimeridian_crossing", SevWarning, "an edge spans more than 180 degrees of longitude; polygons crossing the antimeridian are not handled", map[string]any{"ring": ri})
				break
			}
		}
		// RFC 7946 section 3.1.6: exterior rings counterclockwise, holes clockwise.
		a := signedArea(ring)
		switch {
		case ri == 0 && a < 0:
			add("winding_outer_clockwise", SevWarning, "the outer ring is clockwise; RFC 7946 expects counterclockwise", nil)
		case ri > 0 && a > 0:
			add("winding_hole_counterclockwise", SevWarning, "a hole is counterclockwise; RFC 7946 expects clockwise", map[string]any{"ring": ri})
		}
	}
	if len(outer) > rules.MaxPositions {
		add("check_skipped_too_many_positions", SevWarning, "the self-intersection check was skipped: too many positions", map[string]any{"positions": len(outer), "max": rules.MaxPositions})
	} else if i, j, found := selfIntersection(outer); found {
		add("self_intersection", SevError, "the outer ring crosses itself", map[string]any{"edgeA": i, "edgeB": j})
	}
	area := ringAreaHa(toRing(outer))
	rep.AreaHa = math.Round(area*10000) / 10000
	switch {
	case area <= 0:
		add("zero_area", SevError, "the polygon encloses no area", nil)
	case area > rules.MaxAreaHa:
		add("area_too_large", SevWarning, fmt.Sprintf("the area exceeds %.0f ha; check for swapped or mistyped coordinates", rules.MaxAreaHa), map[string]any{"areaHa": rep.AreaHa, "maxHa": rules.MaxAreaHa})
	}
	if reportedHa > 0 && area > 0 && math.Abs(reportedHa-area)/area*100 > rules.AreaTolerancePct {
		add("area_mismatch", SevWarning, "the stored area differs from the area computed from the polygon", map[string]any{"computedHa": rep.AreaHa, "reportedHa": reportedHa, "tolerancePct": rules.AreaTolerancePct})
	}
}

func toRing(ps []pos) [][]float64 {
	out := make([][]float64, len(ps))
	for i, p := range ps {
		out[i] = []float64{p.x, p.y}
	}
	return out
}

// signedArea is the planar shoelace area in degrees squared: positive for counterclockwise.
func signedArea(r []pos) float64 {
	var s float64
	for i := 0; i < len(r)-1; i++ {
		s += r[i].x*r[i+1].y - r[i+1].x*r[i].y
	}
	return s / 2
}

func cross(o, a, b pos) float64 { return (a.x-o.x)*(b.y-o.y) - (a.y-o.y)*(b.x-o.x) }

func onSeg(a, b, p pos) bool {
	return math.Min(a.x, b.x) <= p.x && p.x <= math.Max(a.x, b.x) && math.Min(a.y, b.y) <= p.y && p.y <= math.Max(a.y, b.y)
}

// segmentsIntersect reports whether the closed segments ab and cd share a point.
func segmentsIntersect(a, b, c, d pos) bool {
	d1, d2, d3, d4 := cross(c, d, a), cross(c, d, b), cross(a, b, c), cross(a, b, d)
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	return (d1 == 0 && onSeg(c, d, a)) || (d2 == 0 && onSeg(c, d, b)) || (d3 == 0 && onSeg(a, b, c)) || (d4 == 0 && onSeg(a, b, d))
}

// selfIntersection finds two non-adjacent edges of a closed ring that touch or cross.
func selfIntersection(r []pos) (int, int, bool) {
	n := len(r) - 1 // edges
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if j == i+1 || (i == 0 && j == n-1) {
				continue // adjacent edges share an endpoint by construction
			}
			if segmentsIntersect(r[i], r[i+1], r[j], r[j+1]) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

// pointInRing is the even-odd rule; points on the boundary count as inside.
func pointInRing(p pos, r []pos) bool {
	in := false
	for i := 0; i < len(r)-1; i++ {
		a, b := r[i], r[i+1]
		if cross(a, b, p) == 0 && onSeg(a, b, p) {
			return true
		}
		if (a.y > p.y) != (b.y > p.y) && p.x < (b.x-a.x)*(p.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

// footprint is the outer ring of a plot (or its single point) for overlap tests.
type footprint struct {
	id    uuid.UUID
	name  string
	point *pos
	ring  []pos
}

// outerFootprint extracts the outer ring or point of a stored geometry; ok is false when the
// geometry is not usable (it then has its own validation errors and takes no part in overlap).
func outerFootprint(raw json.RawMessage) (fp footprint, ok bool) {
	var g rawGeom
	if json.Unmarshal(raw, &g) != nil {
		return fp, false
	}
	if g.Type == "Feature" && json.Unmarshal(g.Geometry, &g) != nil {
		return fp, false
	}
	switch g.Type {
	case "Point":
		var c []float64
		if json.Unmarshal(g.Coordinates, &c) != nil || len(c) < 2 || !validLonLat(c[0], c[1]) {
			return fp, false
		}
		p := pos{c[0], c[1]}
		fp.point = &p
		return fp, true
	case "Polygon":
		var rings [][][]float64
		if json.Unmarshal(g.Coordinates, &rings) != nil || len(rings) == 0 || len(rings[0]) < 4 {
			return fp, false
		}
		for _, c := range rings[0] {
			if len(c) < 2 || !validLonLat(c[0], c[1]) {
				return fp, false
			}
			fp.ring = append(fp.ring, pos{c[0], c[1]})
		}
		if fp.ring[0] != fp.ring[len(fp.ring)-1] {
			return fp, false
		}
		return fp, true
	}
	return fp, false
}

// overlapKind says how a occupies space of b ("" when they are apart). Touching boundaries count
// as boundary_crossing. Two points overlap only when identical. Overlap area is not computed.
func overlapKind(a, b footprint, maxPositions int) string {
	switch {
	case a.point != nil && b.point != nil:
		if *a.point == *b.point {
			return "contained"
		}
		return ""
	case a.point != nil:
		if pointInRing(*a.point, b.ring) {
			return "contained"
		}
		return ""
	case b.point != nil:
		if pointInRing(*b.point, a.ring) {
			return "contains"
		}
		return ""
	}
	if len(a.ring) > maxPositions || len(b.ring) > maxPositions {
		return ""
	}
	for i := 0; i < len(a.ring)-1; i++ {
		for j := 0; j < len(b.ring)-1; j++ {
			if segmentsIntersect(a.ring[i], a.ring[i+1], b.ring[j], b.ring[j+1]) {
				return "boundary_crossing"
			}
		}
	}
	if pointInRing(a.ring[0], b.ring) {
		return "contained"
	}
	if pointInRing(b.ring[0], a.ring) {
		return "contains"
	}
	return ""
}

// DetectOverlaps fills Overlaps (and an "overlap" warning) on each report from the footprints of
// the same tenant's plots. reports and prints are parallel to plots by plot id.
func DetectOverlaps(reports []PlotReport, geoms map[uuid.UUID]json.RawMessage, maxPositions int) {
	fps := make([]footprint, 0, len(reports))
	idx := map[uuid.UUID]int{}
	for _, r := range reports {
		if fp, ok := outerFootprint(geoms[r.PlotID]); ok {
			fp.id, fp.name = r.PlotID, r.Name
			idx[r.PlotID] = len(fps)
			fps = append(fps, fp)
		}
	}
	for ri := range reports {
		ai, ok := idx[reports[ri].PlotID]
		if !ok {
			continue
		}
		for bi := range fps {
			if bi == ai {
				continue
			}
			if k := overlapKind(fps[ai], fps[bi], maxPositions); k != "" {
				reports[ri].Overlaps = append(reports[ri].Overlaps, Overlap{PlotID: fps[bi].id, Name: fps[bi].name, Kind: k})
			}
		}
		sort.Slice(reports[ri].Overlaps, func(i, j int) bool { return reports[ri].Overlaps[i].Name < reports[ri].Overlaps[j].Name })
		if n := len(reports[ri].Overlaps); n > 0 {
			ids := make([]string, n)
			for i, o := range reports[ri].Overlaps {
				ids[i] = o.PlotID.String()
			}
			reports[ri].Reasons = append(reports[ri].Reasons, Reason{Code: "overlap", Severity: SevWarning,
				Message: fmt.Sprintf("the plot overlaps %d other plot(s) of this tenant", n), Detail: map[string]any{"plotIds": ids}})
			reports[ri].Status = statusOf(reports[ri].Reasons)
		}
	}
}
