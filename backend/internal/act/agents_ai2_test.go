package act_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// foodTenantToken creates a tenant with the food and pharma verticals and returns its admin token.
func foodTenantToken(t *testing.T, e *testkit.Env) string {
	t.Helper()
	if _, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "greenfarm", Name: "Green Farm", AdminEmail: "a@green.test",
		AdminPassword: testkit.Password, Verticals: []string{"food", "pharma"}}); err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "greenfarm", "email": "a@green.test", "password": testkit.Password}), &tok)
	return tok.Token
}

func greenUser(t *testing.T, e *testkit.Env, admin, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Call(admin, "POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "greenfarm", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

type pkgResp struct {
	ID, Kind, Status, Title, ContentType, SHA256 string
	Size                                         int
	Summary                                      map[string]any
	Checklist                                    []struct{ Key, Kind, Status, ConfirmedBy string }
	SignatureID, ReleasedBy                      string
}

func TestInspectionAgentFSMA204(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenantToken(t, e)
	qa := greenUser(t, e, tok, "qa@green.test", "Compliance")
	other := greenUser(t, e, tok, "qa2@green.test", "Compliance")
	viewer := greenUser(t, e, tok, "viewer@green.test", "Viewer")
	ops := greenUser(t, e, tok, "ops@green.test", "Ops")
	farm := gs1.WithCheckDigit("950600001000")
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/lots", map[string]any{"tlc": "LET-001", "description": "Romaine lettuce", "quantity": 500, "uom": "KGM",
		"locationGln": farm, "origin": "harvested", "harvestDate": "2026-06-01"}), nil)
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/food/cte", map[string]any{"type": "cooling", "tlc": "LET-001", "quantity": 500, "uom": "KGM", "locationGln": farm,
		"eventTime": time.Date(2026, 6, 1, 7, 0, 0, 0, time.UTC)}), nil)

	assemble := func(token string, body map[string]any) testkit.Response {
		return e.Call(token, "POST", "/api/v1/agents/inspection/assemble", body)
	}
	e.Must(http.StatusForbidden, assemble(viewer, map[string]any{"kind": "fsma204"}), nil)
	e.Must(http.StatusBadRequest, assemble(tok, map[string]any{"kind": "audit"}), nil)
	e.Must(http.StatusBadRequest, assemble(tok, map[string]any{"kind": "fsma204", "scope": map[string]any{"from": "June"}}), nil)
	e.Must(http.StatusBadRequest, assemble(tok, map[string]any{"kind": "fsma204", "scope": map[string]any{"to": "June"}}), nil)
	e.Must(http.StatusBadRequest, assemble(tok, map[string]any{"kind": "tracing"}), nil)
	e.Must(http.StatusBadRequest, assemble(tok, map[string]any{"kind": "tracing", "scope": map[string]any{"investigationId": uuid.NewString()}}), nil)
	e.Must(http.StatusBadRequest, e.Call(tok, "POST", "/api/v1/agents/inspection/assemble", "{"), nil)

	var pkg pkgResp
	e.Must(http.StatusCreated, assemble(ops, map[string]any{"kind": "fsma204", "scope": map[string]any{"from": "2026-01-01", "to": "2026-12-31"}}), &pkg)
	if pkg.Status != "draft" || pkg.Kind != "fsma204" || pkg.SHA256 == "" || pkg.Size == 0 || pkg.Summary["rows"].(float64) < 1 || !strings.HasPrefix(pkg.ContentType, "text/csv") {
		t.Fatalf("package: %+v", pkg)
	}
	keys := map[string]string{}
	for _, c := range pkg.Checklist {
		keys[c.Key] = c.Status
	}
	if keys["rows"] != "ok" || keys["scope"] != "todo" || keys["layout"] != "todo" || keys["reviewed"] != "todo" || keys["sender"] != "todo" {
		t.Fatalf("checklist: %+v", pkg.Checklist)
	}
	// The request is recorded as an agent action decided by the requester, with the tool's audit trail.
	var ac struct{ Items []fullAction }
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/agents/actions?status=executed", nil), &ac)
	if len(ac.Items) != 1 || ac.Items[0].Agent != "inspection-agent" || ac.Items[0].Kind != "inspection.assemble" || ac.Items[0].DecidedBy == nil || !strings.Contains(*ac.Items[0].DecidedBy, "ops@green.test") {
		t.Fatalf("action: %+v", ac.Items)
	}
	r := e.Call(viewer, "GET", "/api/v1/agents/inspection/packages/"+pkg.ID+"/content", nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), "Traceability Lot Code") || !strings.Contains(string(r.Body), "LET-001") || !strings.Contains(r.Header.Get("Content-Disposition"), "fsma204-draft") {
		t.Fatalf("content: %d %s", r.Status, r.Body)
	}

	// Release rules.
	rel := func(token string, sig any) testkit.Response {
		return e.Call(token, "POST", "/api/v1/agents/inspection/packages/"+pkg.ID+"/release", map[string]any{"signatureId": sig})
	}
	confirm := func(token, key string, v bool) testkit.Response {
		return e.Call(token, "POST", "/api/v1/agents/inspection/packages/"+pkg.ID+"/checklist", map[string]any{"key": key, "confirmed": v})
	}
	e.Must(http.StatusForbidden, rel(ops, nil), nil)
	e.Must(http.StatusForbidden, rel(viewer, nil), nil)
	problem(t, rel(qa, nil), 409, "checklist items are not satisfied")
	e.Must(http.StatusForbidden, confirm(viewer, "scope", true), nil)
	problem(t, confirm(qa, "rows", true), 409, "needs no confirmation")
	problem(t, confirm(qa, "nope", true), 400, "unknown checklist item")
	for _, k := range []string{"scope", "layout", "reviewed", "sender"} {
		e.Must(http.StatusOK, confirm(ops, k, true), nil)
	}
	e.Must(http.StatusOK, confirm(ops, "sender", false), nil)
	problem(t, rel(qa, nil), 409, "sender")
	e.Must(http.StatusOK, confirm(ops, "sender", true), &pkg)
	problem(t, rel(qa, nil), 428, "electronic signature")
	problem(t, rel(qa, uuid.NewString()), 428, "signature not found")
	foreign := signID(t, e, other, "inspection_package", pkg.ID)
	problem(t, rel(qa, foreign), 428, "signature not found") // somebody else's signature
	wrongEntity := signID(t, e, qa, "investigation", pkg.ID)
	problem(t, rel(qa, wrongEntity), 428, "signature not found")
	stale := signID(t, e, qa, "inspection_package", pkg.ID)
	time.Sleep(20 * time.Millisecond)
	e.Must(http.StatusOK, confirm(ops, "sender", false), nil)
	e.Must(http.StatusOK, confirm(ops, "sender", true), nil) // the package changed after the signature
	problem(t, rel(qa, stale), 428, "signature not found")
	good := signID(t, e, qa, "inspection_package", pkg.ID)
	e.Must(http.StatusOK, rel(qa, good), &pkg)
	if pkg.Status != "released" || pkg.SignatureID != good || !strings.Contains(pkg.ReleasedBy, "qa@green.test") {
		t.Fatalf("released: %+v", pkg)
	}
	problem(t, rel(qa, good), 409, "only a draft")
	problem(t, confirm(qa, "scope", false), 409, "only a draft")
	if auditCountAs(t, e, tok, "inspection.package.release") != 1 || auditCountAs(t, e, tok, "inspection.package.download") != 1 || auditCountAs(t, e, tok, "inspection.package.checklist") < 8 {
		t.Fatal("release, download and checklist changes must be audited")
	}
	// A released package cannot be reverted away; it can be voided with a reason.
	e.Must(http.StatusConflict, e.Call(tok, "POST", "/api/v1/agents/actions/"+ac.Items[0].ID+"/revert", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", "/api/v1/agents/inspection/packages/"+pkg.ID+"/void", map[string]any{}), nil)
	e.Must(http.StatusOK, e.Call(qa, "POST", "/api/v1/agents/inspection/packages/"+pkg.ID+"/void", map[string]any{"reason": "wrong lot"}), &pkg)
	e.Must(http.StatusConflict, e.Call(qa, "POST", "/api/v1/agents/inspection/packages/"+pkg.ID+"/void", map[string]any{"reason": "again"}), nil)

	// An empty window warns; a draft can be reverted (deleted).
	var empty pkgResp
	e.Must(http.StatusCreated, assemble(tok, map[string]any{"kind": "fsma204", "scope": map[string]any{"from": "2000-01-01", "to": "2000-01-02", "tlc": ""}}), &empty)
	if empty.Checklist[0].Key != "rows" || empty.Checklist[0].Status != "warn" {
		t.Fatalf("empty window: %+v", empty.Checklist)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/inspection/packages?status=draft", nil), nil)
	var list struct{ Items []pkgResp }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/inspection/packages", nil), &list)
	if len(list.Items) != 2 {
		t.Fatalf("packages: %+v", list.Items)
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/agents/actions?status=executed", nil), &ac)
	for _, a := range ac.Items {
		if a.EntityID == empty.ID {
			e.Must(http.StatusOK, e.Call(tok, "POST", "/api/v1/agents/actions/"+a.ID+"/revert", nil), nil)
		}
	}
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/agents/inspection/packages/"+empty.ID, nil), nil)
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/agents/inspection/packages/"+empty.ID+"/content", nil), nil)

	// Suggest-only blocks assembling.
	e.Must(http.StatusOK, e.Call(tok, "PUT", "/api/v1/agents/tools/inspection.assemble", map[string]any{"mode": "suggest"}), nil)
	problem(t, assemble(tok, map[string]any{"kind": "fsma204"}), 409, "suggest only")
	// The agent has no scheduled work: running it proposes nothing.
	e.Must(http.StatusOK, e.Call(tok, "POST", "/api/v1/agents/inspection-agent/run", nil), nil)
}

func auditCountAs(t *testing.T, e *testkit.Env, tok, prefix string) int {
	t.Helper()
	var p struct{ Total int }
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/audit?action="+prefix, nil), &p)
	return p.Total
}

func TestInspectionAgentTracing(t *testing.T) {
	e := testkit.New(t)
	tok := foodTenantToken(t, e)
	gtin := "09506000134352"
	serials := supply(t, e, tok, gtin)
	var inv struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/investigations", map[string]any{"kind": "tracing_request", "gtin": gtin, "serials": append(serials, "UNKNOWN9"),
		"title": "Board request", "requester": map[string]any{"type": "authority", "name": "State board"}}), &inv)
	var pkg pkgResp
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/agents/inspection/assemble", map[string]any{"kind": "tracing", "scope": map[string]any{"investigationId": inv.ID}}), &pkg)
	if pkg.Kind != "tracing" || pkg.ContentType != "application/json" || pkg.Summary["items"].(float64) != 2 || pkg.Summary["investigationId"] != inv.ID || !strings.HasPrefix(pkg.Title, "Tracing answer: Board request") {
		t.Fatalf("package: %+v", pkg)
	}
	keys := map[string]bool{}
	for _, c := range pkg.Checklist {
		keys[c.Key] = true
	}
	if !keys["items"] || !keys["not_found"] || !keys["format"] {
		t.Fatalf("checklist: %+v", pkg.Checklist)
	}
	r := e.Call(tok, "GET", "/api/v1/agents/inspection/packages/"+pkg.ID+"/content", nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), inv.ID) || !strings.Contains(r.Header.Get("Content-Disposition"), ".json") {
		t.Fatalf("content: %d %s", r.Status, r.Body)
	}
	// Assembling a package never stores the answer on the investigation, closes or signs it.
	var d struct {
		Investigation struct {
			Status     string
			AnsweredAt *time.Time
		}
	}
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/investigations/"+inv.ID, nil), &d)
	if d.Investigation.AnsweredAt != nil || d.Investigation.Status != "open" {
		t.Fatalf("investigation changed: %+v", d)
	}
}

func TestRegWatch(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	supply(t, e, e.Token, "09506000134352") // a product targeting the US market

	var (
		mu     sync.Mutex
		body   = `<html><body><nav>Home <span>v1</span></nav><div id="content"><h1>Guidance</h1><p>Old rule text</p></div><script>var t=1</script></body></html>`
		status = http.StatusOK
		hits   atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 3<<20)))
		case "/private":
			http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}))
	defer srv.Close()
	set := func(b string, st int) { mu.Lock(); body, status = b, st; mu.Unlock() }
	age := func() {
		superExec(t, e, `update watch_source set last_checked_at = now() - interval '7 hours' where last_checked_at is not null`)
	}
	create := func(tok string, in map[string]any) testkit.Response {
		return e.Call(tok, "POST", "/api/v1/watch-sources", in)
	}

	// Production mode: only public https.
	for _, u := range []string{srv.URL + "/page", "https://127.0.0.1/x", "https://10.0.0.1/", "https://user:pw@www.fda.gov/", "https://www.fda.gov:8443/", "ftp://www.fda.gov/", ""} {
		e.Must(http.StatusBadRequest, create(e.Token, map[string]any{"name": "Bad", "url": u}), nil)
	}
	actOf(e).InsecureWatch = true
	in := map[string]any{"name": "FDA guidance", "url": srv.URL + "/page", "selector": "#content", "market": "us"}
	e.Must(http.StatusForbidden, create(viewer, in), nil)
	e.Must(http.StatusBadRequest, create(e.Token, map[string]any{"name": "", "url": srv.URL}), nil)
	e.Must(http.StatusBadRequest, create(e.Token, map[string]any{"name": "S", "url": srv.URL, "selector": "a b"}), nil)
	e.Must(http.StatusBadRequest, create(e.Token, map[string]any{"name": "S", "url": srv.URL, "market": "United States"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/watch-sources", "{"), nil)
	var src struct{ ID, Market string }
	e.Must(http.StatusCreated, create(e.Token, in), &src)
	if src.Market != "US" {
		t.Fatalf("market not normalised: %+v", src)
	}
	e.Must(http.StatusConflict, create(e.Token, in), nil)

	// First fetch: baseline only.
	if n := runAgent(t, e, "reg-watch"); n != 0 {
		t.Fatalf("baseline proposed %d", n)
	}
	var list struct {
		Items []struct {
			ID, Name, LastHash, LastPreview, LastError string
			LastCheckedAt, LastChangedAt               *time.Time
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/watch-sources", nil), &list)
	if len(list.Items) != 1 || list.Items[0].LastHash == "" || list.Items[0].LastCheckedAt == nil || list.Items[0].LastChangedAt != nil || !strings.Contains(list.Items[0].LastPreview, "Old rule text") {
		t.Fatalf("after baseline: %+v", list.Items)
	}
	// Sources are fetched at most every few hours.
	before := hits.Load()
	runAgent(t, e, "reg-watch")
	if hits.Load() != before {
		t.Fatal("fetched again within the minimum interval")
	}
	// Changes outside the selector, scripts and whitespace do not count.
	age()
	set(strings.ReplaceAll(strings.ReplaceAll(body, "v1", "v2"), "var t=1", "var t=2"), 200)
	if n := runAgent(t, e, "reg-watch"); n != 0 {
		t.Fatalf("irrelevant change proposed %d", n)
	}
	// A real change proposes a case with a diff and the affected products.
	age()
	set(strings.ReplaceAll(body, "Old rule text", "New rule text<br>Second paragraph"), 200)
	if n := runAgent(t, e, "reg-watch"); n != 1 {
		t.Fatalf("change proposed %d", n)
	}
	a := agentActions(t, e, "reg-watch", "pending")[0]
	r := strings.Join(a.Reasoning, "\n")
	if a.Kind != "case.open" || a.Title != "Regulatory source changed: FDA guidance" || !strings.Contains(r, "Amoxicillin 500mg (09506000134352)") || !strings.Contains(r, "1 products target market US") {
		t.Fatalf("proposal: %+v", a)
	}
	var det struct{ After map[string]any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions/"+a.ID, nil), &det)
	diff, _ := det.After["diff"].(string)
	if !strings.Contains(diff, "-Old rule text") || !strings.Contains(diff, "+New rule text") || !strings.Contains(diff, "+Second paragraph") {
		t.Fatalf("diff:\n%s\n%+v", diff, det.After)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), nil)
	var cs struct {
		Items []struct {
			Title, Kind string
			Notes       []map[string]string
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/cases", nil), &cs)
	if len(cs.Items) != 1 || cs.Items[0].Kind != "regulatory.source_changed" || len(cs.Items[0].Notes) != 3 || !strings.Contains(cs.Items[0].Notes[2]["text"], "Diff excerpt") {
		t.Fatalf("case: %+v", cs.Items)
	}
	if auditCount(t, e, "watch.source.changed") != 1 {
		t.Fatal("a detected change must be audited")
	}
	age()
	if n := runAgent(t, e, "reg-watch"); n != 0 {
		t.Fatalf("unchanged content proposed %d", n)
	}

	// Failures are recorded, not proposed.
	check := func(want string) {
		t.Helper()
		age()
		runAgent(t, e, "reg-watch")
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/watch-sources", nil), &list)
		if !strings.Contains(list.Items[0].LastError, want) {
			t.Fatalf("last error %q, want %q", list.Items[0].LastError, want)
		}
	}
	set(body, 503)
	check("HTTP 503")
	set(strings.ReplaceAll(body, `id="content"`, `id="other"`), 200)
	check("matched nothing")
	set(body, 200)
	var upd map[string]any
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/watch-sources", nil), &upd)
	for path, want := range map[string]string{"/big": "larger than", "/private": "not public"} {
		e.Must(http.StatusOK, e.Do("PUT", "/api/v1/watch-sources/"+src.ID, map[string]any{"name": "FDA guidance", "url": srv.URL + path, "market": "EU"}), nil)
		check(want)
	}
	// Changing the page starts a new baseline; a market without products says so; disabled sources are skipped.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/watch-sources/"+src.ID, map[string]any{"name": "FDA guidance", "url": srv.URL + "/page", "market": "EU"}), nil)
	runAgent(t, e, "reg-watch")
	age()
	set(body+"<p>Extra</p>", 200)
	// "#content" is gone, so the whole page is compared.
	if n := runAgent(t, e, "reg-watch"); n != 1 {
		t.Fatalf("change after new baseline proposed %d", n)
	}
	var fresh fullAction
	for _, x := range agentActions(t, e, "reg-watch", "pending") {
		fresh = x
	}
	if !strings.Contains(strings.Join(fresh.Reasoning, "\n"), "No product in this workspace targets market EU") {
		t.Fatalf("reasoning: %v", fresh.Reasoning)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/watch-sources/"+src.ID, map[string]any{"name": "FDA guidance", "url": srv.URL + "/page", "enabled": false}), nil)
	age()
	before = hits.Load()
	runAgent(t, e, "reg-watch")
	if hits.Load() != before {
		t.Fatal("a disabled source was fetched")
	}
	e.Must(http.StatusNotFound, e.Do("PUT", "/api/v1/watch-sources/"+uuid.NewString(), map[string]any{"name": "x", "url": srv.URL}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/watch-sources/"+src.ID, map[string]any{"name": "x", "url": "https://10.0.0.1/"}), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "DELETE", "/api/v1/watch-sources/"+src.ID, nil), nil)
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/watch-sources/"+src.ID, nil), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/watch-sources/"+src.ID, nil), nil)

	// At most 50 sources.
	for i := 0; i < 50; i++ {
		e.Must(http.StatusCreated, create(e.Token, map[string]any{"name": fmt.Sprintf("S%02d", i), "url": srv.URL}), nil)
	}
	e.Must(http.StatusConflict, create(e.Token, map[string]any{"name": "one too many", "url": srv.URL}), nil)
}
