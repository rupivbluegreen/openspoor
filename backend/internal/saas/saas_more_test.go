package saas_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	of "github.com/open-feature/go-sdk/openfeature"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/saas"
	"github.com/openspoor/openspoor/internal/testkit"
)

func saasModule(t *testing.T, e *testkit.Env) *saas.Module {
	t.Helper()
	for _, m := range e.App.Modules {
		if s, ok := m.(*saas.Module); ok {
			return s
		}
	}
	t.Fatal("saas module missing")
	return nil
}

// operator creates the platform tenant with an operator user and returns its token.
func operator(t *testing.T, e *testkit.Env) string {
	t.Helper()
	if _, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "platform", Name: "Platform", AdminEmail: "ops@openspoor.test",
		AdminPassword: testkit.Password, Operator: true}); err != nil {
		t.Fatal(err)
	}
	var op struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "platform", "email": "ops@openspoor.test", "password": testkit.Password}), &op)
	return op.Token
}

func saasEnv(t *testing.T, opts ...func(*config.Config)) *testkit.Env {
	return testkit.New(t, append([]func(*config.Config){func(c *config.Config) { c.Mode, c.BaseDomain = config.ModeSaaS, "openspoor.test" }}, opts...)...)
}

type signupResp struct {
	Token  string
	URL    string
	Tenant struct{ ID, Slug, Plan string }
}

func signup(t *testing.T, e *testkit.Env, slug string) signupResp {
	t.Helper()
	var su signupResp
	e.Must(http.StatusCreated, e.Call("", "POST", "/api/v1/saas/signup", map[string]any{"companyName": strings.ToUpper(slug), "slug": slug, "email": "admin@" + slug + ".test",
		"password": testkit.Password}), &su)
	return su
}

func TestSignupValidationPlansAndBranding(t *testing.T) {
	e := saasEnv(t)
	var plans struct {
		Items []saas.Plan
		Total int
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/saas/plans", nil), &plans)
	if plans.Total != 5 || plans.Items[0].ID != "trial" || plans.Items[0].EventsPerMonth != 100_000 || !plans.Items[3].Features["sso"] {
		t.Fatalf("plans: %+v", plans.Items)
	}

	for name, body := range map[string]map[string]any{
		"reserved":     {"companyName": "X", "slug": "www", "email": "a@x.test", "password": testkit.Password},
		"too short":    {"companyName": "X", "slug": "a", "email": "a@x.test", "password": testkit.Password},
		"uppercase ok": {"companyName": "X", "slug": "-bad", "email": "a@x.test", "password": testkit.Password},
		"vertical":     {"companyName": "X", "slug": "okslug", "email": "a@x.test", "password": testkit.Password, "verticals": []string{"cosmetics"}},
	} {
		r := e.Call("", "POST", "/api/v1/saas/signup", body)
		if r.Status != http.StatusBadRequest || !strings.Contains(r.Header.Get("Content-Type"), "problem+json") {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusBadRequest, e.Call("", "POST", "/api/v1/saas/signup", "{"), nil)
	var su signupResp
	e.Must(http.StatusCreated, e.Call("", "POST", "/api/v1/saas/signup", map[string]any{"companyName": "Baltic", "slug": "  Baltic ", "email": "admin@baltic.test",
		"password": testkit.Password, "verticals": []string{"food"}}), &su)
	if su.Tenant.Slug != "baltic" || su.URL != "https://baltic.openspoor.test" {
		t.Fatalf("signup: %+v", su)
	}
	r := e.Call("", "POST", "/api/v1/saas/signup", map[string]any{"companyName": "Again", "slug": "baltic", "email": "b@x.test", "password": testkit.Password})
	if r.Status < 400 || r.Status >= 500 {
		t.Fatalf("duplicate slug: %d %s", r.Status, r.Body)
	}

	// Branding: validation, admin-only writes, public read by host (with port) or slug.
	viewerEmail := "viewer@baltic.test"
	e.Must(http.StatusCreated, e.Call(su.Token, "POST", "/api/v1/users", map[string]any{"email": viewerEmail, "roles": []string{"Viewer"}, "password": testkit.Password}), nil)
	var vt struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"email": viewerEmail, "password": testkit.Password}, "Host", "baltic.openspoor.test:443"), &vt)
	e.Must(http.StatusForbidden, e.Call(vt.Token, "PUT", "/api/v1/tenant/branding", map[string]any{"primaryColor": "#000000"}), nil)
	e.Must(http.StatusForbidden, e.Call(vt.Token, "POST", "/api/v1/tenant/export", nil), nil)
	e.Must(http.StatusForbidden, e.Call(vt.Token, "POST", "/api/v1/tenant/support-access", map[string]any{"hours": 1, "level": "admin"}), nil)
	e.Must(http.StatusBadRequest, e.Call(su.Token, "PUT", "/api/v1/tenant/branding", map[string]any{"primaryColor": "red"}), nil)
	e.Must(http.StatusBadRequest, e.Call(su.Token, "PUT", "/api/v1/tenant/branding", map[string]any{"logoUrl": "http://insecure.example/logo.png"}), nil)
	e.Must(http.StatusOK, e.Call(su.Token, "PUT", "/api/v1/tenant/branding", map[string]any{"primaryColor": "#112233", "logoUrl": "data:image/png;base64,AAAA", "displayName": "Baltic"}), nil)
	var gb struct {
		Brand          map[string]any
		CustomDomain   *string
		DomainVerified bool
	}
	e.Must(http.StatusOK, e.Call(vt.Token, "GET", "/api/v1/tenant/branding", nil), &gb)
	if gb.Brand["displayName"] != "Baltic" || gb.CustomDomain != nil || gb.DomainVerified {
		t.Fatalf("get branding: %+v", gb)
	}
	var br struct {
		Name, Slug string
		Brand      map[string]any
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/saas/branding?host=BALTIC.openspoor.test:8443", nil), &br)
	if br.Slug != "baltic" || br.Brand["primaryColor"] != "#112233" {
		t.Fatalf("branding by host: %+v", br)
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/saas/branding?host=unknown.example&slug=baltic", nil), &br)
	if br.Slug != "baltic" {
		t.Fatalf("branding by slug: %+v", br)
	}
	var def struct {
		Name, Slug string
		Brand      map[string]any
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/saas/branding?host=nobody.example", nil), &def)
	if def.Name != "Open Spoor" || def.Slug != "" || len(def.Brand) != 0 {
		t.Fatalf("default branding: %+v", def)
	}
	// Reserved and nested subdomains are not tenants.
	e.Must(http.StatusNotFound, e.Call("", "GET", "/api/v1/saas/tls-ask?domain=admin.openspoor.test", nil), nil)
	e.Must(http.StatusNotFound, e.Call("", "GET", "/api/v1/saas/tls-ask?domain=a.b.openspoor.test", nil), nil)

	var aud struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Call(su.Token, "GET", "/api/v1/audit?action=tenant.branding", nil), &aud)
	if len(aud.Items) != 1 {
		t.Fatalf("branding audit: %+v", aud.Items)
	}
}

func TestCustomDomainPlanUpgradeAndSupportAccess(t *testing.T) {
	e := saasEnv(t)
	su := signup(t, e, "orbit")
	other := signup(t, e, "nebula")
	op := operator(t, e)

	// Trial plan: no custom domains.
	e.Must(http.StatusBadRequest, e.Call(su.Token, "POST", "/api/v1/tenant/domain", map[string]any{"domain": "not a domain"}), nil)
	e.Must(http.StatusBadRequest, e.Call(su.Token, "POST", "/api/v1/tenant/domain", map[string]any{"domain": "x.openspoor.test"}), nil)
	e.Must(http.StatusForbidden, e.Call(su.Token, "POST", "/api/v1/tenant/domain", map[string]any{"domain": "trace.orbit.example"}), nil)

	// Operator changes: validation, audit in the tenant, plan upgrade enables the flag.
	path := "/api/v1/operator/tenants/" + su.Tenant.ID
	e.Must(http.StatusBadRequest, e.Call(op, "PATCH", path, map[string]any{"plan": "platinum", "reason": "r"}), nil)
	e.Must(http.StatusBadRequest, e.Call(op, "PATCH", path, map[string]any{"status": "deleted", "reason": "r"}), nil)
	e.Must(http.StatusBadRequest, e.Call(op, "PATCH", path, map[string]any{"plan": "starter"}), nil)
	e.Must(http.StatusBadRequest, e.Call(op, "PATCH", "/api/v1/operator/tenants/nope", map[string]any{"plan": "starter", "reason": "r"}), nil)
	e.Must(http.StatusNotFound, e.Call(op, "PATCH", "/api/v1/operator/tenants/"+uuid.NewString(), map[string]any{"plan": "starter", "reason": "r"}), nil)
	e.Must(http.StatusForbidden, e.Call(su.Token, "PATCH", path, map[string]any{"plan": "enterprise", "reason": "self-upgrade"}), nil)
	var upd struct{ Plan, Region, Status string }
	e.Must(http.StatusOK, e.Call(op, "PATCH", path, map[string]any{"plan": "starter", "region": "eu-north", "reason": "upgrade order 42"}), &upd)
	if upd.Plan != "starter" || upd.Region != "eu-north" || upd.Status != "active" {
		t.Fatalf("op update: %+v", upd)
	}
	var ff map[string]bool
	e.Must(http.StatusOK, e.Call(su.Token, "GET", "/api/v1/tenant/features", nil), &ff)
	if !ff["custom_domain"] || ff["assistant"] || !ff["food"] || ff["sso"] {
		t.Fatalf("starter flags: %+v", ff)
	}
	var u saas.Usage
	e.Must(http.StatusOK, e.Call(su.Token, "GET", "/api/v1/tenant/usage", nil), &u)
	if u.Plan.ID != "starter" || u.Quota["eventsPerMonth"] != 1_000_000 || u.Users != 1 || u.BillingDriver != "none" {
		t.Fatalf("usage: %+v", u)
	}

	// Verification without a domain is a conflict.
	e.Must(http.StatusConflict, e.Call(other.Token, "POST", "/api/v1/tenant/domain/verify", nil), nil)

	var dom struct{ Domain, TxtRecord, TxtValue string }
	e.Must(http.StatusOK, e.Call(su.Token, "POST", "/api/v1/tenant/domain", map[string]any{"domain": " Trace.Orbit.Example "}), &dom)
	if dom.Domain != "trace.orbit.example" || dom.TxtRecord != "_openspoor.trace.orbit.example" || !strings.HasPrefix(dom.TxtValue, "openspoor-verify=") {
		t.Fatalf("domain: %+v", dom)
	}
	// Not yet verified: TLS and branding do not resolve the domain.
	e.Must(http.StatusNotFound, e.Call("", "GET", "/api/v1/saas/tls-ask?domain=trace.orbit.example", nil), nil)
	orig := saas.LookupTXT
	defer func() { saas.LookupTXT = orig }()
	var asked []string
	saas.LookupTXT = func(name string) ([]string, error) { asked = append(asked, name); return []string{"wrong"}, nil }
	e.Must(http.StatusConflict, e.Call(su.Token, "POST", "/api/v1/tenant/domain/verify", nil), nil)
	saas.LookupTXT = func(name string) ([]string, error) {
		asked = append(asked, name)
		return []string{"unrelated", " " + dom.TxtValue + " "}, nil
	}
	var ver struct {
		Domain   string
		Verified bool
	}
	e.Must(http.StatusOK, e.Call(su.Token, "POST", "/api/v1/tenant/domain/verify", nil), &ver)
	if !ver.Verified || len(asked) != 2 || asked[1] != "_openspoor.trace.orbit.example" {
		t.Fatalf("verify: %+v %v", ver, asked)
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/saas/tls-ask?domain=trace.orbit.example", nil), nil)
	var lg struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"email": "admin@orbit.test", "password": testkit.Password}, "Host", "trace.orbit.example"), &lg)

	// Support access: validation, "none" blocks, admin level grants an admin token.
	e.Must(http.StatusBadRequest, e.Call(su.Token, "POST", "/api/v1/tenant/support-access", map[string]any{"hours": 2, "level": "root"}), nil)
	e.Must(http.StatusBadRequest, e.Call(su.Token, "POST", "/api/v1/tenant/support-access", map[string]any{"hours": 100, "level": "read"}), nil)
	imp := path + "/impersonate"
	e.Must(http.StatusBadRequest, e.Call(op, "POST", imp, map[string]any{"reason": "short"}), nil)
	e.Must(http.StatusBadRequest, e.Call(op, "POST", "/api/v1/operator/tenants/nope/impersonate", map[string]any{"reason": "a long enough reason"}), nil)
	e.Must(http.StatusNotFound, e.Call(op, "POST", "/api/v1/operator/tenants/"+uuid.NewString()+"/impersonate", map[string]any{"reason": "a long enough reason"}), nil)
	e.Must(http.StatusOK, e.Call(su.Token, "POST", "/api/v1/tenant/support-access", map[string]any{"hours": 4, "level": "none"}), nil)
	e.Must(http.StatusForbidden, e.Call(op, "POST", imp, map[string]any{"reason": "ticket 777 investigation"}), nil)
	e.Must(http.StatusOK, e.Call(su.Token, "POST", "/api/v1/tenant/support-access", map[string]any{"hours": 0, "level": "admin"}), nil)
	e.Must(http.StatusForbidden, e.Call(op, "POST", imp, map[string]any{"reason": "ticket 777 investigation"}), nil) // consent already expired
	e.Must(http.StatusOK, e.Call(su.Token, "POST", "/api/v1/tenant/support-access", map[string]any{"hours": 1, "level": "admin"}), nil)
	var tok struct {
		Token, Level string
		Principal    struct{ Roles []string }
	}
	e.Must(http.StatusOK, e.Call(op, "POST", imp, map[string]any{"reason": "ticket 777 investigation"}), &tok)
	if tok.Level != "admin" || len(tok.Principal.Roles) != 1 || tok.Principal.Roles[0] != "Admin" {
		t.Fatalf("impersonate: %+v", tok)
	}
	e.Must(http.StatusCreated, e.Call(tok.Token, "POST", "/api/v1/orgs", map[string]any{"name": "Support-made", "kind": "manufacturer"}), nil)

	// Suspension by the operator is audited with the operator as actor.
	e.Must(http.StatusOK, e.Call(op, "PATCH", path, map[string]any{"status": "suspended", "quotas": map[string]int{"users": 3}, "reason": "unpaid invoice"}), &upd)
	if upd.Status != "suspended" {
		t.Fatalf("suspend: %+v", upd)
	}
	var ts struct {
		Items []struct {
			Slug, Status string
			Quotas       map[string]int64
			Users        int64
		}
	}
	e.Must(http.StatusOK, e.Call(op, "GET", "/api/v1/operator/tenants", nil), &ts)
	found := false
	for _, x := range ts.Items {
		if x.Slug == "orbit" {
			found = x.Status == "suspended" && x.Quotas["users"] == 3 && x.Users == 1
		}
	}
	if !found {
		t.Fatalf("operator list: %+v", ts.Items)
	}
}

func TestBillingPushAndQuotaInSingleMode(t *testing.T) {
	var mu sync.Mutex
	fail := true
	var events []map[string]any
	lago := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer key" || r.URL.Path != "/api/v1/events" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		events = append(events, v)
	}))
	defer lago.Close()
	e := testkit.New(t, func(c *config.Config) {
		c.Mode, c.BillingDriver, c.LagoURL, c.LagoAPIKey = config.ModeSingle, "lago", lago.URL+"/", "key"
	})
	m := saasModule(t, e)
	ctx := context.Background()
	if m.Name() != "saas" || m.TenantURL("acme") != "http://localhost" {
		t.Fatalf("single mode url: %s", m.TenantURL("acme"))
	}
	// Self-hosted: every feature, no quota, no sign-up.
	if err := m.CheckEvents(ctx, e.Tenant.ID, 1<<30); err != nil {
		t.Fatalf("quota in single mode: %v", err)
	}
	if !m.Enabled(ctx, e.Tenant.ID, "sso") {
		t.Fatal("single mode must enable every feature")
	}
	e.Must(http.StatusNotFound, e.Call("", "POST", "/api/v1/saas/signup", map[string]any{"slug": "x"}), nil)

	if err := m.Meter(ctx, e.Tenant.ID, "epcis_events", 7); err != nil {
		t.Fatal(err)
	}
	if err := m.Meter(ctx, e.Tenant.ID, "epcis_events", 3); err != nil {
		t.Fatal(err)
	}
	var u saas.Usage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/tenant/usage", nil), &u)
	if u.Events != 10 || u.BillingDriver != "lago" {
		t.Fatalf("usage: %+v", u)
	}
	run := m.Schedules()[0].Run
	if err := run(ctx); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("lago failure must surface: %v", err)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if err := run(ctx); err != nil {
		t.Fatal(err)
	}
	// Nothing left to bill: a second push sends nothing.
	if err := run(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("lago events: %+v", events)
	}
	ev := events[0]["event"].(map[string]any)
	if ev["external_subscription_id"] != "acme" || ev["code"] != "epcis_events" || ev["properties"].(map[string]any)["count"] != float64(10) {
		t.Fatalf("lago event: %+v", ev)
	}
}

func TestExportAndDeleteGuards(t *testing.T) {
	e := testkit.New(t)
	var key struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "admin-key", "roles": []string{"Admin"}}), &key)
	e.Must(http.StatusForbidden, e.Call(key.Secret, "DELETE", "/api/v1/tenant", map[string]any{"password": testkit.Password, "confirmSlug": "acme", "reason": "x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("DELETE", "/api/v1/tenant", map[string]any{"password": testkit.Password, "confirmSlug": "nobody", "reason": "x"}), nil)
	e.Must(http.StatusUnauthorized, e.Do("DELETE", "/api/v1/tenant", map[string]any{"password": "wrong-password", "confirmSlug": "acme", "reason": "x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("DELETE", "/api/v1/tenant", map[string]any{"password": testkit.Password, "confirmSlug": "acme"}), nil)

	// Another tenant's data never appears in an export.
	_, other := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusCreated, e.Call(other, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Globex secret product"}), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Acme product"}), nil)
	r := e.Do("POST", "/api/v1/tenant/export", nil)
	var dump map[string]json.RawMessage
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &dump) != nil {
		t.Fatalf("export: %d %.200s", r.Status, r.Body)
	}
	if !strings.Contains(string(dump["products"]), "Acme product") || strings.Contains(string(r.Body), "Globex secret product") {
		t.Fatalf("export scope: %s", dump["products"])
	}
	if !strings.Contains(r.Header.Get("Content-Disposition"), "openspoor-export-") || len(dump) != 16 {
		t.Fatalf("export shape: %s keys=%d", r.Header.Get("Content-Disposition"), len(dump))
	}
	var aud struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=tenant.export", nil), &aud)
	if len(aud.Items) != 1 || aud.Items[0].Action != "tenant.export" {
		t.Fatalf("audit: %+v", aud.Items)
	}
	// The tenant still exists after the refused deletions.
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "acme", "email": "admin@acme.test", "password": testkit.Password}), nil)
}

func TestBillingAdapters(t *testing.T) {
	ctx := context.Background()
	if b := saas.NewBilling(config.Config{}); b.Name() != "none" || b.ReportUsage(ctx, "c", "m", "p", 1) != nil {
		t.Fatal("none adapter")
	}
	if b := saas.NewBilling(config.Config{BillingDriver: "stripe"}); b.Name() != "stripe" || b.ReportUsage(ctx, "c", "m", "p", 1) == nil {
		t.Fatal("stripe stub must refuse")
	}
	l := saas.NewBilling(config.Config{BillingDriver: "lago", LagoURL: "http://[::1"})
	if l.Name() != "lago" || l.ReportUsage(ctx, "c", "m", "p", 1) == nil {
		t.Fatal("malformed lago URL must fail")
	}
	if err := saas.NewBilling(config.Config{BillingDriver: "lago", LagoURL: "http://127.0.0.1:1"}).ReportUsage(ctx, "c", "m", "p", 1); err == nil {
		t.Fatal("unreachable lago must fail")
	}
}

func TestPlanProviderDefaults(t *testing.T) {
	e := saasEnv(t)
	m := saasModule(t, e)
	ctx := context.Background()
	p := &saas.PlanProvider{}
	if p.Metadata().Name != "openspoor-plan" || p.Hooks() != nil {
		t.Fatal("metadata")
	}
	fc := of.FlattenedContext{}
	if p.StringEvaluation(ctx, "f", "d", fc).Value != "d" || p.FloatEvaluation(ctx, "f", 1.5, fc).Value != 1.5 ||
		p.IntEvaluation(ctx, "f", 3, fc).Value != 3 || p.ObjectEvaluation(ctx, "f", "o", fc).Value != "o" {
		t.Fatal("non-boolean flags must return defaults")
	}
	// Unknown tenant id / unknown plan fall back to the caller's default.
	if m.Enabled(ctx, uuid.New(), "food") {
		t.Fatal("unknown tenant must get the default")
	}
	if !m.Enabled(ctx, e.Tenant.ID, "sso") {
		t.Fatal("community plan of a provisioned tenant enables everything")
	}
	if v, _ := of.NewClient("t").BooleanValue(ctx, "food", true, of.NewEvaluationContext("not-a-uuid", nil)); !v {
		t.Fatal("non-uuid targeting key must return the default")
	}
}
