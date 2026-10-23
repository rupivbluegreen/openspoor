package food_test

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/food"
	"github.com/openspoor/openspoor/internal/testkit"
)

type planResp struct {
	Plan    *food.PlanVersion
	Context struct {
		FTLCategories []string
		TLCSources    []struct {
			Source  string
			Lots    int
			Example string
		}
		PlotCount int
	}
	Review            struct{ Enabled, Due bool }
	CompletenessHints []string
	Notes             []string
}

func TestTraceabilityPlanVersioningAndExport(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "viewer@green.test", "Viewer")
	integ := foodUser(t, e, tok, "int@green.test", "Integration")
	ops := foodUser(t, e, tok, "ops@green.test", "Ops")
	base := "/api/v1/food/traceability-plan"

	var st planResp
	e.Must(http.StatusOK, e.Call(viewer, "GET", base, nil), &st)
	if st.Plan != nil || len(st.Notes) == 0 || len(st.Context.FTLCategories) == 0 {
		t.Fatalf("empty state: %+v", st)
	}
	e.Must(http.StatusNotFound, e.Call(tok, "GET", base+"/export", nil), nil)

	var prod struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Baby spinach"}), &prod)
	var plot struct{ ID string }
	poly := map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{-75.6, 4.5}, {-75.5, 4.5}, {-75.5, 4.6}, {-75.6, 4.6}, {-75.6, 4.5}}}}
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/plots", map[string]any{"name": "North <b>field</b>", "country": "CO", "commodity": "coffee", "geojson": poly}), &plot)
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/plots", map[string]any{"name": "Pin", "country": "CO", "commodity": "coffee",
		"geojson": map[string]any{"type": "Feature", "geometry": map[string]any{"type": "Point", "coordinates": []float64{-75.55, 4.55}}}, "areaHa": 1}), nil)
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/lots", map[string]any{"tlc": "SP-001", "description": "Spinach", "tlcSource": "GLN 9506000010009", "quantity": 1, "locationGln": farmGLN, "origin": "harvested"}), nil)

	content := map[string]any{
		"recordProcedures": map[string]any{"text": "Records are kept in Open Spoor <script>"},
		"foodsHandled":     map[string]any{"text": "Leafy greens", "items": []map[string]any{{"productId": prod.ID, "ftlCategory": "Leafy greens (fresh)"}}},
		"tlcAssignment":    map[string]any{"text": "Assigned at harvest", "pattern": "SP-NNN", "example": "SP-001"},
		"pointOfContact":   map[string]any{"text": "Ask quality", "name": "Q. Manager", "email": "q@green.test"},
		"farmMap":          map[string]any{"applicable": true, "text": "See map", "plotIds": []string{plot.ID}},
	}
	put := func(token string, status int, body map[string]any) food.PlanVersion {
		t.Helper()
		var v food.PlanVersion
		e.Must(status, e.Call(token, "PUT", base, body), &v)
		return v
	}
	// Permissions and validation.
	put(viewer, http.StatusForbidden, map[string]any{"reason": "x", "content": content})
	put(integ, http.StatusForbidden, map[string]any{"reason": "x", "content": content})
	put(tok, http.StatusBadRequest, map[string]any{"content": content})
	put(tok, http.StatusBadRequest, map[string]any{"reason": "x", "siteId": "nope", "content": content})
	put(tok, http.StatusBadRequest, map[string]any{"reason": "x", "siteId": "00000000-0000-0000-0000-000000000001", "content": content})
	for name, mut := range map[string]func(c map[string]any){
		"product": func(c map[string]any) {
			c["foodsHandled"] = map[string]any{"items": []map[string]any{{"productId": "00000000-0000-0000-0000-000000000001", "ftlCategory": "x"}}}
		},
		"nocat": func(c map[string]any) {
			c["foodsHandled"] = map[string]any{"items": []map[string]any{{"name": "Kale"}}}
		},
		"noname": func(c map[string]any) {
			c["foodsHandled"] = map[string]any{"items": []map[string]any{{"ftlCategory": "Herbs (fresh)"}}}
		},
		"dupe": func(c map[string]any) {
			c["foodsHandled"] = map[string]any{"items": []map[string]any{{"productId": prod.ID, "ftlCategory": "a"}, {"productId": prod.ID, "ftlCategory": "a"}}}
		},
		"email": func(c map[string]any) { c["pointOfContact"] = map[string]any{"email": "nope"} },
		"plot": func(c map[string]any) {
			c["farmMap"] = map[string]any{"applicable": true, "plotIds": []string{"00000000-0000-0000-0000-000000000001"}}
		},
		"toolong": func(c map[string]any) { c["recordProcedures"] = map[string]any{"text": strings.Repeat("x", 20001)} },
		"longpat": func(c map[string]any) { c["tlcAssignment"] = map[string]any{"pattern": strings.Repeat("x", 501)} },
	} {
		c := map[string]any{}
		for k, v := range content {
			c[k] = v
		}
		mut(c)
		if r := e.Call(tok, "PUT", base, map[string]any{"reason": "x", "content": c}); r.Status != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusBadRequest, e.Call(tok, "PUT", base, "{"), nil)

	v1 := put(ops, http.StatusOK, map[string]any{"reason": "Initial plan", "content": content})
	if v1.Version != 1 || !v1.Current || v1.Reason != "Initial plan" || v1.CreatedBy == "" ||
		v1.Content.FoodsHandled.Items[0].GTIN != "09506000134352" || v1.Content.FoodsHandled.Items[0].Name != "Baby spinach" {
		t.Fatalf("v1: %+v", v1)
	}
	content["tlcAssignment"] = map[string]any{"text": "Assigned at harvest and repack", "pattern": "SP-NNN"}
	v2 := put(tok, http.StatusOK, map[string]any{"reason": "Added repack", "content": content})
	if v2.Version != 2 || !v2.Current {
		t.Fatalf("v2: %+v", v2)
	}

	e.Must(http.StatusOK, e.Call(viewer, "GET", base, nil), &st)
	if st.Plan == nil || st.Plan.Version != 2 || len(st.CompletenessHints) != 0 || st.Context.PlotCount != 2 ||
		len(st.Context.TLCSources) != 1 || st.Context.TLCSources[0].Example != "SP-001" || st.Review.Enabled {
		t.Fatalf("current: %+v", st)
	}
	var vs struct{ Items []food.PlanVersion }
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/versions", nil), &vs)
	if len(vs.Items) != 2 || vs.Items[0].Version != 2 || !vs.Items[0].Current || vs.Items[1].Current || vs.Items[1].Reason != "Initial plan" {
		t.Fatalf("versions: %+v", vs.Items)
	}

	// Exports (viewer may download; each is audited).
	var ex food.PlanExport
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/export?format=json", nil), &ex)
	if ex.Version.Version != 2 || ex.Tenant != "Green Farm" || len(ex.Plots) != 1 {
		t.Fatalf("json export: %+v", ex)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/export?version=1&format=json", nil), &ex)
	if ex.Version.Version != 1 {
		t.Fatalf("old version export: %+v", ex.Version)
	}
	h := e.Call(viewer, "GET", base+"/export?format=html", nil)
	body := string(h.Body)
	if h.Status != 200 || !strings.HasPrefix(h.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, "1.1315(a)(5)") ||
		!strings.Contains(body, "<svg") || strings.Contains(body, "<script>") || strings.Contains(body, "<b>field</b>") || !strings.Contains(body, "&lt;b&gt;field") {
		t.Fatalf("html export: %d %s", h.Status, body)
	}
	md := e.Call(viewer, "GET", base+"/export?format=md", nil)
	if md.Status != 200 || !strings.Contains(string(md.Body), "## 3. How traceability lot codes are assigned") || !strings.Contains(string(md.Body), "Pattern: SP-NNN") {
		t.Fatalf("md export: %s", md.Body)
	}
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/export?format=pdf", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(viewer, "GET", base+"/export?version=x", nil), nil)
	e.Must(http.StatusNotFound, e.Call(viewer, "GET", base+"/export?version=9", nil), nil)

	// Audit of writes and exports.
	var au struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?limit=200", nil), &au)
	n := map[string]int{}
	for _, a := range au.Items {
		n[a.Action]++
	}
	if n["food.traceability_plan.save"] != 2 || n["food.traceability_plan.export"] != 4 {
		t.Fatalf("audit: %v", n)
	}
}

func TestTraceabilityPlanSiteScopeAndReview(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenant(t, e)
	viewer := foodUser(t, e, tok, "viewer@green.test", "Viewer")
	base := "/api/v1/food/traceability-plan"
	var org, site struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/orgs", map[string]any{"name": "Green", "kind": "manufacturer"}), &org)
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": farmGLN, "name": "Packhouse"}), &site)

	e.Must(http.StatusOK, e.Call(tok, "PUT", base, map[string]any{"reason": "tenant plan", "content": map[string]any{"recordProcedures": map[string]any{"text": "a"}}}), nil)
	var sv food.PlanVersion
	e.Must(http.StatusOK, e.Call(tok, "PUT", base, map[string]any{"siteId": site.ID, "reason": "site plan", "content": map[string]any{"farmMap": map[string]any{"applicable": false}}}), &sv)
	if sv.Version != 1 || sv.SiteID == nil {
		t.Fatalf("site plan numbers separately: %+v", sv)
	}
	var st planResp
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"?siteId="+site.ID, nil), &st)
	if st.Plan == nil || st.Plan.Reason != "site plan" || len(st.CompletenessHints) != 4 {
		t.Fatalf("site current: %+v hints=%v", st.Plan, st.CompletenessHints)
	}
	var ex food.PlanExport
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/export?siteId="+site.ID, nil), &ex)
	if !strings.Contains(ex.Site, "Packhouse") {
		t.Fatalf("site export: %+v", ex)
	}
	if r := e.Call(viewer, "GET", base+"/export?format=html&siteId="+site.ID, nil); !strings.Contains(string(r.Body), "Not applicable") {
		t.Fatalf("not a farm: %s", r.Body)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/versions?siteId="+site.ID, nil), nil)

	// Review reminder: off by default, per-tenant setting, never above helper UX.
	var s food.PlanSettings
	e.Must(http.StatusOK, e.Call(viewer, "GET", base+"/settings", nil), &s)
	if s.ReviewReminder || s.ReviewMonths != 12 {
		t.Fatalf("defaults: %+v", s)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", base+"/settings", map[string]any{"reviewReminder": true}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "PUT", base+"/settings", map[string]any{"reviewReminder": true, "reviewMonths": 99}), nil)
	e.Must(http.StatusOK, e.Call(tok, "PUT", base+"/settings", map[string]any{"reviewReminder": true}), &s)
	if !s.ReviewReminder || s.ReviewMonths != 12 {
		t.Fatalf("settings: %+v", s)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", base, nil), &st)
	if !st.Review.Enabled || st.Review.Due {
		t.Fatalf("fresh plan is not due: %+v", st.Review)
	}
	// Age the plan 13 months.
	c, err := pgx.Connect(t.Context(), e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(t.Context()) }()
	if _, err := c.Exec(t.Context(), `update food_traceability_plan set created_at = $1`, time.Now().AddDate(0, -13, 0)); err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", base, nil), &st)
	if !st.Review.Due {
		t.Fatalf("old plan is due: %+v", st.Review)
	}
}

func TestPlotOverviewSVG(t *testing.T) {
	if s := food.PlotOverviewSVG(nil); !strings.Contains(s, "No plots") {
		t.Fatal(s)
	}
	bad := []food.Plot{{Name: "bad", GeoJSON: []byte(`{`)}, {Name: "line", GeoJSON: []byte(`{"type":"LineString"}`)}, {Name: "pt", GeoJSON: []byte(`{"type":"Point","coordinates":[1]}`)}}
	if s := food.PlotOverviewSVG(bad); !strings.Contains(s, "No plots") {
		t.Fatal(s)
	}
	one := []food.Plot{{Name: "A&B", GeoJSON: []byte(`{"type":"Point","coordinates":[5,52]}`)}}
	if s := food.PlotOverviewSVG(one); !strings.Contains(s, "A&amp;B") || !strings.Contains(s, "<circle") {
		t.Fatal(s)
	}
}
