package platform_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/testkit"
)

func TestPlatformFlow(t *testing.T) {
	e := testkit.New(t)

	var org struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "Acme Pharma", "kind": "manufacturer", "companyPrefix": "0614141"}), &org)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": "0614141073467", "name": "Plant 1"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": "0614141073468", "name": "Bad GLN"}), nil)

	// Users and roles.
	var u struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "viewer@acme.test", "roles": []string{"Viewer"}, "password": testkit.Password}), &u)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "viewer@acme.test", "password": testkit.Password}), &tok)
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/orgs", nil), nil)
	e.Must(http.StatusForbidden, e.Call(tok.Token, "POST", "/api/v1/orgs", map[string]any{"name": "X", "kind": "manufacturer"}), nil)
	e.Must(http.StatusUnauthorized, e.Call("", "GET", "/api/v1/orgs", nil), nil)

	// API keys.
	var key struct{ ID, Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "L3", "roles": []string{"Integration"}}), &key)
	e.Must(http.StatusOK, e.Call(key.Secret, "GET", "/api/v1/me", nil), nil)
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/api-keys/"+key.ID, nil), nil)
	e.Must(http.StatusUnauthorized, e.Call(key.Secret, "GET", "/api/v1/me", nil), nil)

	// E-signature requires re-authentication.
	e.Must(http.StatusUnauthorized, e.Do("POST", "/api/v1/esignatures", map[string]any{"password": "wrong", "meaning": "Approved", "reason": "release", "entity": "organization", "entityId": org.ID}), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Approved", "reason": "release", "entity": "organization", "entityId": org.ID}), nil)

	// Audit chain is intact.
	var v struct {
		Checked int
		Intact  bool
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit/verify", nil), &v)
	if !v.Intact || v.Checked < 6 {
		t.Fatalf("audit chain: %+v", v)
	}
}

func TestTenantIsolation(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "Acme Secret Org", "kind": "manufacturer"}), nil)
	_, other := e.NewTenant("globex", "admin@globex.test")
	var page struct{ Items []map[string]any }
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/orgs", nil), &page)
	if len(page.Items) != 0 {
		t.Fatalf("cross-tenant read succeeded: %v", page.Items)
	}
	var audit struct{ Total int }
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/audit?entity=organization", nil), &audit)
	if audit.Total != 0 {
		t.Fatalf("cross-tenant audit visible")
	}
	// Direct SQL without a tenant set sees nothing (RLS forced, app role is not superuser).
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.DBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var n int
	if err := c.QueryRow(ctx, "select count(*) from organization").Scan(&n); err != nil || n != 0 {
		t.Fatalf("RLS not enforced without tenant: n=%d err=%v", n, err)
	}
	// Writing a row into another tenant is rejected by the policy's WITH CHECK.
	tx, _ := c.Begin(ctx)
	_, _ = tx.Exec(ctx, "select set_config('app.tenant_id', $1, true)", e.Tenant.ID.String())
	_, err = tx.Exec(ctx, "insert into organization (id, tenant_id, name) values (gen_random_uuid(), (select id from tenant where slug='globex'), 'x')")
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("cross-tenant insert allowed")
	}
}

func TestAuditImmutable(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.DBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	tx, _ := c.Begin(ctx)
	_, _ = tx.Exec(ctx, "select set_config('app.tenant_id', $1, true)", e.Tenant.ID.String())
	_, err = tx.Exec(ctx, "update audit_entry set actor='mallory'")
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("audit update allowed")
	}
}
