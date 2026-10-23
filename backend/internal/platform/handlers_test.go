package platform_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

// problem asserts an RFC 9457 error response with the given status whose title contains want.
func problem(t *testing.T, r testkit.Response, status int, want string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Body)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content type %q: %s", ct, r.Body)
	}
	var p struct {
		Title  string
		Status int
	}
	if err := r.JSON(&p); err != nil || p.Status != status || !strings.Contains(p.Title, want) {
		t.Fatalf("problem %+v (want title containing %q): %s", p, want, r.Body)
	}
}

// login creates a user with the given roles in the default tenant and returns its token.
func login(t *testing.T, e *testkit.Env, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

// superExec runs SQL as the database superuser (bypassing RLS) against the test database.
func superExec(t *testing.T, e *testkit.Env, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

type auditPage struct {
	Total int
	Items []struct {
		Actor, Action, Entity, EntityID, Reason string
		Before, After                           map[string]any
	}
}

func TestModuleName(t *testing.T) {
	if n := platform.New(kernel.Deps{}).Name(); n != "platform" {
		t.Fatalf("name %q", n)
	}
}

func TestOrganizationsAndSites(t *testing.T) {
	e := testkit.New(t)
	var org struct{ ID, Name, Kind string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "Acme Pharma", "kind": "manufacturer"}), &org)

	problem(t, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "  ", "kind": "manufacturer"}), 400, "name is required")
	problem(t, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "X", "kind": "pirate"}), 400, "unknown organization kind")
	problem(t, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "X", "kind": "cmo", "bogus": 1}), 400, "invalid JSON body")

	// Update records the before and after state in the audit log.
	var upd struct{ ID, Name, Kind string }
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/orgs/"+org.ID, map[string]any{"name": "Acme Pharma BV", "kind": "cmo", "country": "NL"}), &upd)
	if upd.ID != org.ID || upd.Name != "Acme Pharma BV" || upd.Kind != "cmo" {
		t.Fatalf("update: %+v", upd)
	}
	var aud auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=org.update&entityId="+org.ID, nil), &aud)
	if aud.Total != 1 || aud.Items[0].Before["name"] != "Acme Pharma" || aud.Items[0].After["name"] != "Acme Pharma BV" || aud.Items[0].Actor != "user:"+adminID(t, e) {
		t.Fatalf("org.update audit: %+v", aud)
	}
	problem(t, e.Do("PUT", "/api/v1/orgs/00000000-0000-0000-0000-000000000001", map[string]any{"name": "Y", "kind": "cmo"}), 404, "not found")
	problem(t, e.Do("PUT", "/api/v1/orgs/not-a-uuid", map[string]any{"name": "Y", "kind": "cmo"}), 400, "invalid id")
	problem(t, e.Do("PUT", "/api/v1/orgs/"+org.ID, map[string]any{"name": "Y", "kind": "nope"}), 400, "unknown organization kind")
	problem(t, e.Do("PUT", "/api/v1/orgs/"+org.ID, "{"), 400, "invalid JSON body")

	// Sites: defaults, validation, update.
	var site struct {
		ID, GLN, Name, Kind string
		Address             map[string]any
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "Plant 1"}), &site)
	if site.Kind != "plant" || site.Address == nil {
		t.Fatalf("site defaults: %+v", site)
	}
	problem(t, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": "0614141073467"}), 400, "name is required")
	problem(t, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": "123", "name": "Short"}), 400, "gln must be 13 digits")
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/sites/"+site.ID, map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "Plant One", "kind": "warehouse", "address": map[string]any{"city": "Delft"}}), &site)
	if site.Name != "Plant One" || site.Kind != "warehouse" || site.Address["city"] != "Delft" {
		t.Fatalf("site update: %+v", site)
	}
	problem(t, e.Do("PUT", "/api/v1/sites/"+site.ID, map[string]any{"orgId": org.ID, "gln": "0614141073468", "name": "Bad"}), 400, "gln")
	problem(t, e.Do("PUT", "/api/v1/sites/00000000-0000-0000-0000-000000000001", map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "Ghost"}), 404, "not found")
	problem(t, e.Do("PUT", "/api/v1/sites/xyz", map[string]any{}), 400, "invalid id")
	problem(t, e.Do("PUT", "/api/v1/sites/"+site.ID, "nope"), 400, "invalid JSON body")
	var sites struct {
		Total int
		Items []struct{ Name string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/sites", nil), &sites)
	if sites.Total != 1 || sites.Items[0].Name != "Plant One" {
		t.Fatalf("sites: %+v", sites)
	}

	// Viewer reads, cannot write; Ops has no access to sites at all.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/sites", nil), nil)
	problem(t, e.Call(viewer, "PUT", "/api/v1/sites/"+site.ID, map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "V"}), 403, "missing role")
	problem(t, e.Call(viewer, "PUT", "/api/v1/orgs/"+org.ID, map[string]any{"name": "V", "kind": "cmo"}), 403, "missing role")
	problem(t, e.Call(ops, "GET", "/api/v1/sites", nil), 403, "missing role")
	problem(t, e.Call(ops, "POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "O"}), 403, "missing role")

	// Another tenant can neither see nor change these records.
	_, other := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(other, "PUT", "/api/v1/orgs/"+org.ID, map[string]any{"name": "Hijack", "kind": "cmo"}), 404, "not found")
	problem(t, e.Call(other, "PUT", "/api/v1/sites/"+site.ID, map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "Hijack"}), 404, "not found")
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/sites", nil), &sites)
	if sites.Total != 0 {
		t.Fatalf("cross-tenant sites: %+v", sites)
	}
}

func TestLicenses(t *testing.T) {
	e := testkit.New(t)
	var org struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "Acme", "kind": "wholesaler"}), &org)
	compliance := login(t, e, "qa@acme.test", "Compliance")
	ops := login(t, e, "ops@acme.test", "Ops")
	viewer := login(t, e, "viewer@acme.test", "Viewer")

	later := time.Date(time.Now().Year()+1, 6, 30, 0, 0, 0, 0, time.UTC) // validity is a calendar date
	var lic struct {
		ID, Kind, Number, Authority string
		ValidTo                     *time.Time
	}
	e.Must(http.StatusCreated, e.Call(compliance, "POST", "/api/v1/licenses", map[string]any{"orgId": org.ID, "kind": "WDA", "number": "WDA-1", "authority": "IGJ", "validTo": later}), &lic)
	if lic.Kind != "WDA" || lic.ValidTo == nil || !lic.ValidTo.Equal(later) {
		t.Fatalf("license: %+v", lic)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/licenses", map[string]any{"orgId": org.ID, "kind": "GDP", "number": "GDP-1", "authority": "IGJ"}), nil)
	problem(t, e.Do("POST", "/api/v1/licenses", map[string]any{"orgId": org.ID, "kind": "WDA", "number": "", "authority": "IGJ"}), 400, "kind, number and authority are required")
	problem(t, e.Do("POST", "/api/v1/licenses", "[]"), 400, "invalid JSON body")
	problem(t, e.Call(ops, "POST", "/api/v1/licenses", map[string]any{"orgId": org.ID, "kind": "WDA", "number": "X", "authority": "IGJ"}), 403, "missing role")
	problem(t, e.Call(viewer, "POST", "/api/v1/licenses", map[string]any{"orgId": org.ID, "kind": "WDA", "number": "X", "authority": "IGJ"}), 403, "missing role")

	var page struct {
		Total int
		Items []struct{ Number string }
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/licenses", nil), &page)
	// Licenses with a validity end come first (nulls last).
	if page.Total != 2 || page.Items[0].Number != "WDA-1" || page.Items[1].Number != "GDP-1" {
		t.Fatalf("licenses: %+v", page)
	}
	var aud auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=license.create", nil), &aud)
	if aud.Total != 2 {
		t.Fatalf("license audit: %+v", aud)
	}
}

func TestUsersAndRoles(t *testing.T) {
	e := testkit.New(t)
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "nope", "roles": []string{"Ops"}}), 400, "valid email required")
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "a@acme.test"}), 400, "at least one role")
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "a@acme.test", "roles": []string{"Wizard"}}), 400, `unknown role "Wizard"`)
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "a@acme.test", "roles": []string{"Ops"}, "password": "short"}), 400, "at least 10 characters")
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "a@acme.test", "roles": []string{"Ops"}, "extra": true}), 400, "invalid JSON body")

	// Emails are stored lower-case; locale defaults to en; duplicates conflict.
	var u struct {
		ID, Email, Locale, Status string
		Roles                     []string
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "Ops@Acme.test", "name": "Olga", "roles": []string{"Ops"}, "password": testkit.Password}), &u)
	if u.Email != "ops@acme.test" || u.Locale != "en" || u.Status != "active" {
		t.Fatalf("created user: %+v", u)
	}
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "ops@acme.test", "roles": []string{"Ops"}}), 409, "conflict")
	// A user without a password (SSO only) cannot log in with a password.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "sso@acme.test", "roles": []string{"Viewer"}, "locale": "nl"}), nil)
	problem(t, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "sso@acme.test", "password": testkit.Password}), 401, "invalid credentials")

	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "OPS@acme.test", "password": testkit.Password}), &tok)
	problem(t, e.Call(tok.Token, "GET", "/api/v1/users", nil), 403, "missing role")
	problem(t, e.Call(tok.Token, "PATCH", "/api/v1/users/"+u.ID, map[string]any{"roles": []string{"Admin"}}), 403, "missing role")

	var list struct {
		Total int
		Items []struct{ Email string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/users", nil), &list)
	if list.Total != 3 || list.Items[0].Email != "admin@acme.test" {
		t.Fatalf("users: %+v", list)
	}

	// PATCH validation.
	problem(t, e.Do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"roles": []string{}}), 400, "at least one role")
	problem(t, e.Do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"roles": []string{"Root"}}), 400, "unknown role")
	problem(t, e.Do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"status": "deleted"}), 400, "status must be active or disabled")
	problem(t, e.Do("PATCH", "/api/v1/users/00000000-0000-0000-0000-000000000009", map[string]any{"name": "Ghost"}), 404, "not found")
	problem(t, e.Do("PATCH", "/api/v1/users/bad", map[string]any{"name": "Ghost"}), 400, "invalid id")
	problem(t, e.Do("PATCH", "/api/v1/users/"+u.ID, "x"), 400, "invalid JSON body")

	// Partial update keeps untouched fields; the reason lands in the audit log.
	e.Must(http.StatusOK, e.Do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"roles": []string{"Ops", "Compliance"}, "locale": "de", "reason": "promotion"}), &u)
	if len(u.Roles) != 2 || u.Locale != "de" || u.Status != "active" {
		t.Fatalf("patched user: %+v", u)
	}
	var aud auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=user.update&entityId="+u.ID, nil), &aud)
	if aud.Total != 1 || aud.Items[0].Reason != "promotion" || len(aud.Items[0].Before["roles"].([]any)) != 1 {
		t.Fatalf("user.update audit: %+v", aud)
	}

	// Disabling a user blocks login, and ends the sessions they already hold (the token is revoked).
	e.Must(http.StatusOK, e.Do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"status": "disabled", "reason": "left"}), &u)
	if u.Status != "disabled" {
		t.Fatalf("disable: %+v", u)
	}
	problem(t, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "ops@acme.test", "password": testkit.Password}), 401, "invalid credentials")
	problem(t, e.Call(tok.Token, "POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Approved", "reason": "r", "entity": "x", "entityId": "1"}), 401, "authentication required")
}

func TestAPIKeys(t *testing.T) {
	e := testkit.New(t)
	integ := login(t, e, "int@acme.test", "Integration")
	viewer := login(t, e, "viewer@acme.test", "Viewer")

	problem(t, e.Do("POST", "/api/v1/api-keys", map[string]any{"roles": []string{"Integration"}}), 400, "name is required")
	problem(t, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "roles": []string{}}), 400, "at least one role")
	problem(t, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "roles": []string{"God"}}), 400, "unknown role")
	problem(t, e.Do("POST", "/api/v1/api-keys", "{bad"), 400, "invalid JSON body")
	// Privilege escalation: an Integration user cannot mint an Admin key.
	problem(t, e.Call(integ, "POST", "/api/v1/api-keys", map[string]any{"name": "esc", "roles": []string{"Admin"}}), 403, "cannot grant a role you do not hold: Admin")
	problem(t, e.Call(viewer, "POST", "/api/v1/api-keys", map[string]any{"name": "v", "roles": []string{"Viewer"}}), 403, "missing role")

	var key struct {
		ID, Secret, Prefix, CreatedBy string
		Roles                         []string
	}
	e.Must(http.StatusCreated, e.Call(integ, "POST", "/api/v1/api-keys", map[string]any{"name": "L3 line", "roles": []string{"Integration"}}), &key)
	if !strings.HasPrefix(key.Secret, "osk_") || key.Prefix != key.Secret[:10] || key.CreatedBy != "user:int@acme.test" {
		t.Fatalf("key: %+v", key)
	}

	// The list never reveals secrets; the viewer may read it.
	r := e.Call(viewer, "GET", "/api/v1/api-keys", nil)
	if r.Status != http.StatusOK || strings.Contains(string(r.Body), key.Secret) || strings.Contains(string(r.Body), `"secret"`) {
		t.Fatalf("key list leaks secret: %d %s", r.Status, r.Body)
	}
	var keys struct {
		Total int
		Items []struct {
			ID         string
			LastUsedAt *time.Time
		}
	}
	// API key principals: /me works and records last use; user-only endpoints refuse.
	var me struct{ Kind, KeyID string }
	e.Must(http.StatusOK, e.Call(key.Secret, "GET", "/api/v1/me", nil), &me)
	if me.Kind != "apikey" || me.KeyID != key.ID {
		t.Fatalf("me as key: %+v", me)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/api-keys", nil), &keys)
	if keys.Total != 1 || keys.Items[0].LastUsedAt == nil {
		t.Fatalf("keys: %+v", keys)
	}
	problem(t, e.Call(key.Secret, "GET", "/api/v1/me/tenants", nil), 403, "users only")
	problem(t, e.Call(key.Secret, "POST", "/api/v1/auth/switch", map[string]any{"tenantId": e.Tenant.ID}), 403, "users only")
	problem(t, e.Call(key.Secret, "POST", "/api/v1/esignatures", map[string]any{"password": "x", "meaning": "m", "reason": "r", "entity": "e", "entityId": "1"}), 403, "only users can sign")

	// Expired keys are refused.
	var old struct{ ID, Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "old", "roles": []string{"Viewer"}, "expiresAt": time.Now().Add(-time.Hour)}), &old)
	problem(t, e.Call(old.Secret, "GET", "/api/v1/me", nil), 401, "authentication required")

	// Revocation: once only, other tenants cannot revoke.
	_, other := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(other, "DELETE", "/api/v1/api-keys/"+key.ID, nil), 404, "not found")
	e.Must(http.StatusOK, e.Call(key.Secret, "GET", "/api/v1/me", nil), nil)
	problem(t, e.Call(viewer, "DELETE", "/api/v1/api-keys/"+key.ID, nil), 403, "missing role")
	e.Must(http.StatusNoContent, e.Call(integ, "DELETE", "/api/v1/api-keys/"+key.ID, nil), nil)
	problem(t, e.Do("DELETE", "/api/v1/api-keys/"+key.ID, nil), 404, "not found")
	problem(t, e.Do("DELETE", "/api/v1/api-keys/zzz", nil), 400, "invalid id")
	var aud auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?entity=api_key&entityId="+key.ID, nil), &aud)
	if aud.Total != 2 || aud.Items[0].Action != "apikey.revoke" || aud.Items[1].Action != "apikey.create" {
		t.Fatalf("key audit: %+v", aud)
	}
}

func TestLoginTenantsAndSwitching(t *testing.T) {
	e := testkit.New(t)
	problem(t, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "nope", "email": "admin@acme.test", "password": testkit.Password}), 401, "invalid credentials")
	problem(t, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "admin@acme.test", "password": "wrong-password"}), 401, "invalid credentials")
	problem(t, e.Call("", "POST", "/api/v1/auth/login", `{"tenant":`), 400, "invalid JSON body")

	// The tenant can come from the request host (subdomain).
	var tok struct {
		Token     string
		Principal struct{ TenantID, Email string }
	}
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"email": "admin@acme.test", "password": testkit.Password}, "Host", "acme.localhost"), &tok)
	if tok.Principal.TenantID != e.Tenant.ID.String() {
		t.Fatalf("host login: %+v", tok)
	}

	var ten struct{ ID, Slug, Plan, Region, Status string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/tenant", nil), &ten)
	if ten.Slug != "acme" || ten.Plan != "community" || ten.Region != "eu" || ten.Status != "active" {
		t.Fatalf("tenant: %+v", ten)
	}

	// The same person in two workspaces sees both and may switch; not into a third.
	globex, _ := e.NewTenant("globex", "admin@acme.test")
	initech, _ := e.NewTenant("initech", "boss@initech.test")
	var mine []struct{ ID, Slug string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/me/tenants", nil), &mine)
	if len(mine) != 2 || mine[0].Slug != "acme" || mine[1].Slug != "globex" {
		t.Fatalf("memberships: %+v", mine)
	}
	var sw struct {
		Token     string
		Principal struct {
			TenantID string
			Roles    []string
			Kind     string
		}
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/auth/switch", map[string]any{"tenantId": globex.ID}), &sw)
	if sw.Principal.TenantID != globex.ID.String() || sw.Principal.Kind != "user" || len(sw.Principal.Roles) != 1 || sw.Principal.Roles[0] != "Admin" {
		t.Fatalf("switch: %+v", sw)
	}
	e.Must(http.StatusOK, e.Call(sw.Token, "GET", "/api/v1/tenant", nil), &ten)
	if ten.Slug != "globex" {
		t.Fatalf("after switch: %+v", ten)
	}
	problem(t, e.Do("POST", "/api/v1/auth/switch", map[string]any{"tenantId": initech.ID}), 403, "no membership in that tenant")
	problem(t, e.Do("POST", "/api/v1/auth/switch", map[string]any{"tenantId": "x"}), 400, "invalid JSON body")

	// A disabled membership is not offered and cannot be switched into.
	var gu struct {
		Items []struct{ ID, Email string }
	}
	e.Must(http.StatusOK, e.Call(sw.Token, "GET", "/api/v1/users", nil), &gu)
	e.Must(http.StatusOK, e.Call(sw.Token, "PATCH", "/api/v1/users/"+gu.Items[0].ID, map[string]any{"status": "disabled"}), nil)
	problem(t, e.Do("POST", "/api/v1/auth/switch", map[string]any{"tenantId": globex.ID}), 403, "no membership")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/me/tenants", nil), &mine)
	if len(mine) != 1 || mine[0].Slug != "acme" {
		t.Fatalf("memberships after disable: %+v", mine)
	}

	// A suspended tenant refuses logins.
	superExec(t, e, `update tenant set status='suspended' where slug='initech'`)
	problem(t, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "initech", "email": "boss@initech.test", "password": testkit.Password}), 403, "tenant is suspended")
}

func TestCreateTenantValidation(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	status := func(err error) int {
		var he *httpx.Error
		if errors.As(err, &he) {
			return he.Status
		}
		return 0
	}
	if _, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "x1", AdminEmail: "a@b.c", AdminPassword: testkit.Password}); status(err) != 400 {
		t.Fatalf("missing name: %v", err)
	}
	if _, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "x1", Name: "X", AdminEmail: "a@b.c", AdminPassword: "short"}); status(err) != 400 {
		t.Fatalf("short password: %v", err)
	}
	if _, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: " ACME ", Name: "Dup", AdminEmail: "a@b.c", AdminPassword: testkit.Password}); status(err) != 409 {
		t.Fatalf("duplicate slug: %v", err)
	}
	if _, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "us-co", Name: "US Co", AdminEmail: "a@b.c", AdminPassword: testkit.Password, Region: "us"}); status(err) != 400 {
		t.Fatalf("tenant for another region on an eu installation: %v", err)
	}
	tn, u, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: " Food-Co ", Name: "Food Co", AdminEmail: "Chef@Food.test", AdminPassword: testkit.Password, Plan: "pro", Verticals: []string{"food"}})
	if err != nil || tn.Slug != "food-co" || tn.Plan != "pro" || tn.Region != "eu" || tn.Verticals[0] != "food" || u.Email != "chef@food.test" || u.Roles[0] != "Admin" {
		t.Fatalf("create: %+v %+v %v", tn, u, err)
	}
	got, err := e.App.Platform.TenantBySlug(ctx, "FOOD-CO")
	if err != nil || got.ID != tn.ID {
		t.Fatalf("by slug: %+v %v", got, err)
	}
	if _, err := e.App.Platform.Login(ctx, "food-co", "chef@food.test", testkit.Password); err != nil {
		t.Fatalf("login: %v", err)
	}
	// The provisioning is audited inside the new tenant as the system actor.
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "food-co", "email": "chef@food.test", "password": testkit.Password}), &tok)
	var aud auditPage
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/audit?action=tenant.create", nil), &aud)
	if aud.Total != 1 || aud.Items[0].Actor != "system" || aud.Items[0].EntityID != tn.ID.String() {
		t.Fatalf("tenant.create audit: %+v", aud)
	}
	// Membership lookups and principals for the tenant switcher.
	ts, err := e.App.Platform.MembershipsFor(ctx, "CHEF@food.test")
	if err != nil || len(ts) != 1 || ts[0].ID != tn.ID {
		t.Fatalf("memberships: %+v %v", ts, err)
	}
	p, err := e.App.Platform.PrincipalFor(ctx, tn.ID, "chef@food.test")
	if err != nil || p.UserID != u.ID || p.TenantID != tn.ID || p.Kind != "user" {
		t.Fatalf("principal: %+v %v", p, err)
	}
	if _, err := e.App.Platform.PrincipalFor(ctx, e.Tenant.ID, "chef@food.test"); status(err) != 403 {
		t.Fatalf("principal in foreign tenant: %v", err)
	}
}

func TestSignaturesAuditAndAlerts(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")

	problem(t, e.Do("POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Approved"}), 400, "meaning, reason, entity and entityId are required")
	problem(t, e.Do("POST", "/api/v1/esignatures", "{"), 400, "invalid JSON body")
	var sig struct {
		ID, Email, Meaning, Entity, EntityID string
		AuditSeq                             int64
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Approved", "reason": "release", "entity": "batch", "entityId": "B-1"}), &sig)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Reviewed", "reason": "review", "entity": "batch", "entityId": "B-2"}), nil)
	if sig.Email != "admin@acme.test" || sig.AuditSeq == 0 {
		t.Fatalf("signature: %+v", sig)
	}
	// The signature is bound to the audit entry it created.
	var aud auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=esignature.sign&entity=batch&entityId=B-1", nil), &aud)
	if aud.Total != 1 || aud.Items[0].Reason != "release" || aud.Items[0].After["meaning"] != "Approved" {
		t.Fatalf("signature audit: %+v", aud)
	}
	var sigs struct {
		Total int
		Items []struct{ EntityID string }
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/esignatures?entity=batch&entityId=B-2", nil), &sigs)
	if sigs.Total != 1 || sigs.Items[0].EntityID != "B-2" {
		t.Fatalf("signatures: %+v", sigs)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/esignatures", nil), &sigs)
	if sigs.Total != 2 {
		t.Fatalf("all signatures: %+v", sigs)
	}

	// Audit paging.
	var page auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=1&offset=1", nil), &page)
	if len(page.Items) != 1 || page.Total < 5 {
		t.Fatalf("audit paging: total=%d items=%d", page.Total, len(page.Items))
	}

	// Alerts inbox: filter by status and kind, resolve with audit; Viewer may read, not resolve.
	a1, a2 := "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	superExec(t, e, `insert into alert (id, tenant_id, kind, severity, title) values ($1,$3,'verify.unknown','error','Unknown serial'), ($2,$3,'supplier.certificate_expiring','warning','GDP expiring')`, a1, a2, e.Tenant.ID)
	var alerts struct {
		Total int
		Items []struct{ ID, Kind, Status string }
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/alerts", nil), &alerts)
	if alerts.Total != 2 {
		t.Fatalf("open alerts: %+v", alerts)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=verify.", nil), &alerts)
	if alerts.Total != 1 || alerts.Items[0].ID != a1 {
		t.Fatalf("kind filter: %+v", alerts)
	}
	problem(t, e.Call(viewer, "POST", "/api/v1/alerts/"+a1+"/resolve", nil), 403, "missing role")
	_, other := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(other, "POST", "/api/v1/alerts/"+a1+"/resolve", nil), 404, "not found")
	var res struct {
		Status     string
		ResolvedAt *time.Time
	}
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/alerts/"+a1+"/resolve", nil), &res)
	if res.Status != "resolved" || res.ResolvedAt == nil {
		t.Fatalf("resolve: %+v", res)
	}
	problem(t, e.Do("POST", "/api/v1/alerts/nope/resolve", nil), 400, "invalid id")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts", nil), &alerts)
	if alerts.Total != 1 || alerts.Items[0].ID != a2 {
		t.Fatalf("open after resolve: %+v", alerts)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=resolved", nil), &alerts)
	if alerts.Total != 1 || alerts.Items[0].ID != a1 {
		t.Fatalf("resolved alerts: %+v", alerts)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=all", nil), &alerts)
	if alerts.Total != 2 {
		t.Fatalf("all alerts: %+v", alerts)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=alert.resolve&entityId="+a1, nil), &aud)
	if aud.Total != 1 || strings.Contains(aud.Items[0].Actor, "@") || !strings.HasPrefix(aud.Items[0].Actor, "user:") {
		t.Fatalf("alert.resolve audit: %+v", aud)
	}
	// Other tenants see none of it.
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/alerts?status=all", nil), &alerts)
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/esignatures", nil), &sigs)
	if alerts.Total != 0 || sigs.Total != 0 {
		t.Fatalf("cross-tenant alerts/signatures: %d %d", alerts.Total, sigs.Total)
	}
}

// adminID is the user id of the default test tenant's admin, as audit entries record it.
func adminID(t *testing.T, e *testkit.Env) string {
	t.Helper()
	var me struct{ UserID string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/me", nil), &me)
	return me.UserID
}

func TestAuditHoldsNoPersonalData(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "jane.doe@acme.test", "name": "Jane Doe", "roles": []string{"Ops"}, "password": testkit.Password}), nil)
	raw := e.Do("GET", "/api/v1/audit?limit=200", nil)
	if raw.Status != http.StatusOK {
		t.Fatalf("audit: %d", raw.Status)
	}
	body := string(raw.Body)
	for _, leak := range []string{"jane.doe@acme.test", "Jane Doe", "admin@acme.test"} {
		if strings.Contains(body, leak) {
			t.Fatalf("audit trail contains %q: %s", leak, body)
		}
	}
	var page struct {
		Items []struct {
			Action string
			Actor  string
			After  map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=user.create", nil), &page)
	if len(page.Items) < 1 {
		t.Fatal("no user.create entry")
	}
	if em, _ := page.Items[0].After["email"].(string); !strings.HasPrefix(em, "pseudo:") || !strings.HasPrefix(page.Items[0].Actor, "user:") {
		t.Fatalf("user.create entry: %+v", page.Items[0])
	}
	// The hash chain still verifies over the pseudonymised entries.
	var v struct {
		Intact   bool
		BrokenAt int64
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit/verify", nil), &v)
	if !v.Intact {
		t.Fatalf("chain broken at %d", v.BrokenAt)
	}
}
