package partners_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/testkit"
)

type partner struct {
	ID, Name, Kind, Status string
	GLN                    *string
	LinkedTenant           *string
	ApprovedAt             *time.Time
	Endpoints              map[string]string
	MasterData             map[string]any
	Health                 map[string]any
}

func problem(t *testing.T, r testkit.Response, status int, want string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Body)
	}
	var p struct {
		Title  string
		Status int
	}
	if err := r.JSON(&p); err != nil || p.Status != status || !strings.Contains(p.Title, want) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("problem %+v (want %q): %s", p, want, r.Body)
	}
}

func login(t *testing.T, e *testkit.Env, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

func superExec(t *testing.T, e *testkit.Env, sql string, args ...any) {
	t.Helper()
	u, _ := url.Parse(os.Getenv("OPENSPOOR_TEST_ADMIN_URL"))
	app, _ := url.Parse(e.DBURL)
	u.Path = app.Path
	ctx := context.Background()
	c, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func module(e *testkit.Env) *partners.Module {
	for _, m := range e.App.Modules {
		if p, ok := m.(*partners.Module); ok {
			return p
		}
	}
	e.T.Fatal("partners module missing")
	return nil
}

func TestModuleMetadata(t *testing.T) {
	m := partners.New(kernel.Deps{})
	if m.Name() != "partners" || m.Client == nil || m.Client.Timeout != 5*time.Second {
		t.Fatalf("module: %+v", m)
	}
	s := m.Schedules()
	if len(s) != 1 || s[0].Name != "partners.health" || s[0].Every != 5*time.Minute || s[0].Run == nil {
		t.Fatalf("schedules: %+v", s)
	}
}

func TestPartnerCRUDAndRoles(t *testing.T) {
	e := testkit.New(t)
	problem(t, e.Do("POST", "/api/v1/partners", map[string]any{"name": " "}), 400, "name is required")
	problem(t, e.Do("POST", "/api/v1/partners", map[string]any{"name": "X", "gln": "0614141073468"}), 400, "invalid GLN")
	problem(t, e.Do("POST", "/api/v1/partners", map[string]any{"name": "X", "endpoints": map[string]string{"vrs": "ftp://x"}}), 400, "endpoints must be http(s) URLs")
	problem(t, e.Do("POST", "/api/v1/partners", map[string]any{"name": "X", "endpoints": map[string]string{"health": "javascript:alert(1)"}}), 400, "endpoints must be http(s) URLs")
	problem(t, e.Do("POST", "/api/v1/partners", map[string]any{"name": "X", "unknown": 1}), 400, "invalid JSON body")

	gln := "0614141073467"
	var p partner
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Wholesale BV", "gln": gln, "country": "NL",
		"endpoints": map[string]string{"epcisCapture": "https://wh.example/epcis", "federation": "anything"}}), &p)
	if p.Kind != "wholesaler" || p.Status != "active" || p.ApprovedAt == nil || p.Endpoints["epcisCapture"] != "https://wh.example/epcis" {
		t.Fatalf("created: %+v", p)
	}
	// Empty GLN is allowed (not every partner has one); duplicate GLN conflicts.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Clinic", "gln": "", "kind": "hospital"}), nil)
	problem(t, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Copy", "gln": gln}), 409, "conflict")

	var got partner
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+p.ID, nil), &got)
	if got.Name != "Wholesale BV" || got.GLN == nil || *got.GLN != gln {
		t.Fatalf("get: %+v", got)
	}
	problem(t, e.Do("GET", "/api/v1/partners/00000000-0000-0000-0000-000000000001", nil), 404, "not found")
	problem(t, e.Do("GET", "/api/v1/partners/not-a-uuid", nil), 400, "invalid id")

	// Update is audited with before and after.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+p.ID, map[string]any{"name": "Wholesale Europe BV", "gln": gln, "kind": "distributor"}), &got)
	if got.Name != "Wholesale Europe BV" || got.Kind != "distributor" || got.Status != "active" {
		t.Fatalf("update: %+v", got)
	}
	var aud struct {
		Total int
		Items []struct {
			Action        string
			Before, After map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=partner.update&entityId="+p.ID, nil), &aud)
	if aud.Total != 1 || aud.Items[0].Before["name"] != "Wholesale BV" || aud.Items[0].After["name"] != "Wholesale Europe BV" {
		t.Fatalf("partner.update audit: %+v", aud)
	}
	problem(t, e.Do("PUT", "/api/v1/partners/"+p.ID, map[string]any{"name": ""}), 400, "name is required")
	problem(t, e.Do("PUT", "/api/v1/partners/00000000-0000-0000-0000-000000000001", map[string]any{"name": "Ghost"}), 404, "not found")
	problem(t, e.Do("PUT", "/api/v1/partners/zz", map[string]any{"name": "Ghost"}), 400, "invalid id")
	problem(t, e.Do("PUT", "/api/v1/partners/"+p.ID, "{"), 400, "invalid JSON body")

	// Suspend and re-approve; status filter on the list.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+p.ID+"/suspend", nil), &got)
	if got.Status != "suspended" {
		t.Fatalf("suspend: %+v", got)
	}
	var list struct {
		Total int
		Items []partner
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners?status=suspended", nil), &list)
	if list.Total != 1 || list.Items[0].ID != p.ID {
		t.Fatalf("suspended list: %+v", list)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners", nil), &list)
	if list.Total != 2 {
		t.Fatalf("all partners: %+v", list)
	}
	before := *got.ApprovedAt
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+p.ID+"/approve", nil), &got)
	if got.Status != "active" || !got.ApprovedAt.After(before) {
		t.Fatalf("re-approve: %+v", got)
	}
	problem(t, e.Do("POST", "/api/v1/partners/00000000-0000-0000-0000-000000000001/approve", nil), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/partners/bad/suspend", nil), 400, "invalid id")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?entity=partner&entityId="+p.ID, nil), &aud)
	actions := []string{}
	for _, it := range aud.Items {
		actions = append(actions, it.Action)
	}
	if strings.Join(actions, ",") != "partner.approve,partner.suspend,partner.update,partner.create" {
		t.Fatalf("partner audit trail: %v", actions)
	}

	// Roles: Viewer reads only; Serialization has no access; Ops may manage.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ser := login(t, e, "ser@acme.test", "Serialization")
	ops := login(t, e, "ops@acme.test", "Ops")
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/partners/"+p.ID, nil), nil)
	problem(t, e.Call(viewer, "POST", "/api/v1/partners/"+p.ID+"/suspend", nil), 403, "missing role")
	problem(t, e.Call(viewer, "POST", "/api/v1/partners", map[string]any{"name": "V"}), 403, "missing role")
	problem(t, e.Call(viewer, "PUT", "/api/v1/partners/listing", map[string]any{"name": "V", "kind": "cmo"}), 403, "missing role")
	problem(t, e.Call(ser, "GET", "/api/v1/partners", nil), 403, "missing role")
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/partners/"+p.ID+"/suspend", nil), nil)

	// Lookups used by other modules run inside the caller's tenant transaction.
	other1, other := e.NewTenant("globex", "admin@globex.test")
	ctx := context.Background()
	err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		byGLN, err := partners.ByGLN(ctx, tx, gln)
		if err != nil || byGLN.ID.String() != p.ID {
			t.Fatalf("ByGLN: %+v %v", byGLN, err)
		}
		if _, err := partners.ByGLN(ctx, tx, "0000000000000"); err == nil {
			t.Fatal("ByGLN found an unknown GLN")
		}
		got, err := partners.Get(ctx, tx, byGLN.ID)
		if err != nil || got.Name != "Wholesale Europe BV" {
			t.Fatalf("Get: %+v %v", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.App.Deps.DB.Tx(ctx, other1.ID, func(tx pgx.Tx) error {
		if _, err := partners.ByGLN(ctx, tx, gln); err == nil {
			t.Fatal("ByGLN crossed tenants")
		}
		return nil
	})

	// Tenant isolation.
	problem(t, e.Call(other, "GET", "/api/v1/partners/"+p.ID, nil), 404, "not found")
	problem(t, e.Call(other, "PUT", "/api/v1/partners/"+p.ID, map[string]any{"name": "Hijack"}), 404, "not found")
	problem(t, e.Call(other, "POST", "/api/v1/partners/"+p.ID+"/approve", nil), 404, "not found")
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/partners", nil), &list)
	if list.Total != 0 {
		t.Fatalf("cross-tenant partners: %+v", list)
	}
	// The same GLN is fine in another tenant.
	e.Must(http.StatusCreated, e.Call(other, "POST", "/api/v1/partners", map[string]any{"name": "Same GLN", "gln": gln}), nil)
}

func TestDirectoryListing(t *testing.T) {
	e := testkit.New(t)
	var l struct {
		TenantID, Name, Kind string
		Published            bool
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/listing", nil), &l)
	if l.TenantID != e.Tenant.ID.String() || l.Name != "" || l.Published {
		t.Fatalf("empty listing: %+v", l)
	}
	problem(t, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme"}), 400, "name and kind are required")
	problem(t, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme", "kind": "manufacturer", "gln": "123"}), 400, "invalid GLN")
	problem(t, e.Do("PUT", "/api/v1/partners/listing", "[1]"), 400, "invalid JSON body")

	// Unpublished listings are not searchable.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme Pharma", "kind": "manufacturer", "gln": "0614141073467", "country": "NL", "description": "Generics"}), &l)
	if l.Published || l.Name != "Acme Pharma" {
		t.Fatalf("draft listing: %+v", l)
	}
	_, other := e.NewTenant("globex", "admin@globex.test")
	var dir struct {
		Total int
		Items []struct{ TenantID, Name string }
	}
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/partners/directory?q=acme", nil), &dir)
	if dir.Total != 0 {
		t.Fatalf("unpublished listing visible: %+v", dir)
	}

	// Publishing upserts the listing; search by name, GLN or country from another tenant.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme Pharma", "kind": "manufacturer", "gln": "0614141073467", "country": "NL", "published": true}), &l)
	e.Must(http.StatusOK, e.Call(other, "PUT", "/api/v1/partners/listing", map[string]any{"name": "Globex Distribution", "kind": "wholesaler", "country": "DE", "published": true}), nil)
	for q, want := range map[string]int{"acme": 1, "0614141073467": 1, "nl": 1, "de": 1, "": 2, "nomatch": 0} {
		e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/partners/directory?q="+q, nil), &dir)
		if dir.Total != want {
			t.Fatalf("directory q=%q: %+v", q, dir)
		}
	}
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/partners/directory?q=acme", nil), &dir)
	if dir.Items[0].TenantID != e.Tenant.ID.String() {
		t.Fatalf("directory entry: %+v", dir)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/listing", nil), &l)
	if !l.Published || l.Kind != "manufacturer" {
		t.Fatalf("stored listing: %+v", l)
	}
	var aud struct{ Total int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=directory.publish", nil), &aud)
	if aud.Total != 2 {
		t.Fatalf("publish audit: %+v", aud)
	}
}

func TestInvitationsAndAcceptance(t *testing.T) {
	e := testkit.New(t)
	// Inviter master data: one product and one site GLN; a published listing with endpoints.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Paracetamol 500"}), nil)
	var org struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "Acme", "kind": "manufacturer"}), &org)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "Plant"}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme Pharma", "kind": "manufacturer", "gln": "0614141073467",
		"endpoints": map[string]string{"epcisCapture": "https://acme.example/epcis"}}), nil)

	problem(t, e.Do("POST", "/api/v1/partners/invitations", map[string]any{"email": "nobody", "name": "G"}), 400, "email and name are required")
	problem(t, e.Do("POST", "/api/v1/partners/invitations", map[string]any{"email": "a@b.c"}), 400, "email and name are required")
	problem(t, e.Do("POST", "/api/v1/partners/invitations", "{"), 400, "invalid JSON body")
	var inv struct{ ID, Code, Email, Status string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners/invitations", map[string]any{"email": "Admin@Globex.test", "name": "Globex"}), &inv)
	if !strings.HasPrefix(inv.Code, "OSP-") || inv.Email != "admin@globex.test" || inv.Status != "open" {
		t.Fatalf("invitation: %+v", inv)
	}
	var invs struct {
		Total int
		Items []struct{ Code string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/invitations", nil), &invs)
	if invs.Total != 1 || invs.Items[0].Code != inv.Code {
		t.Fatalf("invitations: %+v", invs)
	}

	// Own invitations, unknown codes and expired codes are refused.
	problem(t, e.Do("POST", "/api/v1/partners/invitations/accept", map[string]any{"code": inv.Code}), 400, "cannot accept your own invitation")
	_, gtok := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(gtok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": "OSP-NOPE"}), 404, "invitation not found")
	problem(t, e.Call(gtok, "POST", "/api/v1/partners/invitations/accept", "{"), 400, "invalid JSON body")
	var stale struct{ Code string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners/invitations", map[string]any{"email": "x@globex.test", "name": "Old"}), &stale)
	superExec(t, e, `update partner_invitation set expires_at = now() - interval '1 day' where code=$1`, stale.Code)
	problem(t, e.Call(gtok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": stale.Code}), 409, "no longer valid")

	// Acceptance: the invitee gets an active partner with the inviter's listing data and master data.
	var mine partner
	e.Must(http.StatusCreated, e.Call(gtok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": "  " + inv.Code + " "}), &mine)
	if mine.Status != "active" || mine.Name != "Acme Pharma" || mine.Kind != "manufacturer" || mine.LinkedTenant == nil || *mine.LinkedTenant != e.Tenant.ID.String() ||
		mine.Endpoints["epcisCapture"] != "https://acme.example/epcis" {
		t.Fatalf("invitee partner: %+v", mine)
	}
	prods, _ := mine.MasterData["products"].([]any)
	glns, _ := mine.MasterData["glns"].([]any)
	if len(prods) != 1 || prods[0].(map[string]any)["gtin"] != "09506000134352" || len(glns) != 1 || glns[0] != "0614141073467" {
		t.Fatalf("exchanged master data: %+v", mine.MasterData)
	}
	problem(t, e.Call(gtok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": inv.Code}), 409, "no longer valid")

	// The inviter sees a pending partner (no listing → tenant name and local endpoints) and must approve.
	var list struct {
		Total int
		Items []partner
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners?status=pending", nil), &list)
	if list.Total != 1 {
		t.Fatalf("pending: %+v", list)
	}
	theirs := list.Items[0]
	if theirs.Name != "GLOBEX" || theirs.Kind != "wholesaler" || theirs.ApprovedAt != nil || !strings.HasPrefix(theirs.Endpoints["epcisCapture"], "local:") {
		t.Fatalf("inviter view: %+v", theirs)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+theirs.ID+"/approve", nil), &theirs)
	if theirs.Status != "active" || theirs.ApprovedAt == nil {
		t.Fatalf("approve: %+v", theirs)
	}
	var aud struct {
		Items []struct{ Action, Actor string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=partner.invitation_accepted", nil), &aud)
	if len(aud.Items) != 1 || aud.Items[0].Actor != "user:admin@globex.test" {
		t.Fatalf("inviter audit: %+v", aud)
	}
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/audit?action=partner.accept", nil), &aud)
	if len(aud.Items) != 1 {
		t.Fatalf("invitee audit: %+v", aud)
	}
	// Invitations stay private to the inviter.
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/partners/invitations", nil), &invs)
	if invs.Total != 0 {
		t.Fatalf("cross-tenant invitations: %+v", invs)
	}
}

func TestHealthProbes(t *testing.T) {
	e := testkit.New(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer up.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer broken.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()

	mk := func(name string, ep map[string]string) string {
		var p partner
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": name, "endpoints": ep}), &p)
		return p.ID
	}
	cases := []struct {
		id, status, detail string
	}{
		{mk("No endpoint", nil), "unknown", "no endpoint configured"},
		{mk("Local", map[string]string{"epcisCapture": "local:" + e.Tenant.ID.String()}), "up", ""},
		{mk("Healthy", map[string]string{"health": up.URL, "epcisCapture": broken.URL}), "up", ""}, // the health URL wins
		{mk("Broken", map[string]string{"epcisCapture": broken.URL}), "down", "503 Service Unavailable"},
		{mk("Gone", map[string]string{"health": goneURL}), "down", "connect"},
	}
	for _, c := range cases {
		var h map[string]any
		e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+c.id+"/health", nil), &h)
		if h["status"] != c.status || h["checkedAt"] == nil || !strings.Contains(stringOf(h["detail"]), c.detail) {
			t.Fatalf("probe %s: %+v", c.id, h)
		}
		var p partner
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+c.id, nil), &p)
		if p.Health["status"] != c.status {
			t.Fatalf("stored health %s: %+v", c.id, p.Health)
		}
	}
	problem(t, e.Do("POST", "/api/v1/partners/00000000-0000-0000-0000-000000000001/health", nil), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/partners/x/health", nil), 400, "invalid id")

	// The scheduled probe updates active partners in every tenant and skips suspended ones.
	_, gtok := e.NewTenant("globex", "admin@globex.test")
	var gp partner
	e.Must(http.StatusCreated, e.Call(gtok, "POST", "/api/v1/partners", map[string]any{"name": "G", "endpoints": map[string]string{"health": broken.URL}}), &gp)
	suspended := mk("Suspended", map[string]string{"health": broken.URL})
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+suspended+"/suspend", nil), nil)
	if err := module(e).Schedules()[0].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/partners/"+gp.ID, nil), &gp)
	if gp.Health["status"] != "down" {
		t.Fatalf("scheduled probe (other tenant): %+v", gp.Health)
	}
	var sp partner
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+suspended, nil), &sp)
	if len(sp.Health) != 0 {
		t.Fatalf("suspended partner probed: %+v", sp.Health)
	}
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
