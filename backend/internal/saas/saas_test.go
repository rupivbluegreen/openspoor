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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/saas"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

func TestSaaSLifecycle(t *testing.T) {
	var mu sync.Mutex
	var lagoEvents []map[string]any
	lago := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		mu.Lock()
		lagoEvents = append(lagoEvents, v)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer lago.Close()
	e := testkit.New(t, func(c *config.Config) {
		c.Mode, c.BaseDomain, c.BillingDriver, c.LagoURL, c.LagoAPIKey = config.ModeSaaS, "openspoor.test", "lago", lago.URL, "k"
	})

	// Self-service sign-up.
	e.Must(http.StatusBadRequest, e.Call("", "POST", "/api/v1/saas/signup", map[string]any{"companyName": "X", "slug": "admin", "email": "a@x.test", "password": testkit.Password}), nil)
	var su struct {
		Token  string
		URL    string
		Tenant struct{ ID, Slug, Plan string }
	}
	e.Must(http.StatusCreated, e.Call("", "POST", "/api/v1/saas/signup", map[string]any{"companyName": "Nordic Pharma", "slug": "nordic", "email": "ceo@nordic.test",
		"password": testkit.Password, "verticals": []string{"pharma", "food"}}), &su)
	if su.Tenant.Plan != "trial" || su.URL != "https://nordic.openspoor.test" {
		t.Fatalf("signup: %+v", su)
	}

	// Login by subdomain (no tenant field) and public branding by host.
	var lg struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"email": "ceo@nordic.test", "password": testkit.Password}, "Host", "nordic.openspoor.test"), &lg)
	e.Must(http.StatusOK, e.Call(su.Token, "PUT", "/api/v1/tenant/branding", map[string]any{"primaryColor": "#0F766E", "displayName": "Nordic"}), nil)
	var br struct {
		Name  string
		Brand map[string]any
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/saas/branding?host=nordic.openspoor.test", nil), &br)
	if br.Brand["primaryColor"] != "#0F766E" {
		t.Fatalf("branding: %+v", br)
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/saas/tls-ask?domain=nordic.openspoor.test", nil), nil)
	e.Must(http.StatusNotFound, e.Call("", "GET", "/api/v1/saas/tls-ask?domain=evil.example.com", nil), nil)

	// Feature flags come from the plan via OpenFeature.
	var ff map[string]bool
	e.Must(http.StatusOK, e.Call(su.Token, "GET", "/api/v1/tenant/features", nil), &ff)
	if !ff["assistant"] || ff["custom_domain"] {
		t.Fatalf("trial flags: %+v", ff)
	}
	e.Must(http.StatusForbidden, e.Call(su.Token, "POST", "/api/v1/tenant/domain", map[string]any{"domain": "trace.nordic.example"}), nil)

	// Operator console: an operator user in a platform tenant.
	ctx := context.Background()
	_, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "platform", Name: "Platform", AdminEmail: "ops@openspoor.test", AdminPassword: testkit.Password, Operator: true})
	if err != nil {
		t.Fatal(err)
	}
	var op struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "platform", "email": "ops@openspoor.test", "password": testkit.Password}), &op)
	e.Must(http.StatusForbidden, e.Call(su.Token, "GET", "/api/v1/operator/tenants", nil), nil)
	var ts struct {
		Items []struct{ ID, Slug, Plan string }
	}
	e.Must(http.StatusOK, e.Call(op.Token, "GET", "/api/v1/operator/tenants", nil), &ts)
	if len(ts.Items) != 3 {
		t.Fatalf("operator tenants: %+v", ts.Items)
	}

	// Quotas: operator caps events at 2 per month.
	e.Must(http.StatusOK, e.Call(op.Token, "PATCH", "/api/v1/operator/tenants/"+su.Tenant.ID, map[string]any{"quotas": map[string]int{"eventsPerMonth": 2}, "reason": "test quota"}), nil)
	evs := []lib.Event{}
	for i := 0; i < 2; i++ {
		evs = append(evs, lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "receiving", EventTime: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC), EventTimeZoneOffset: "+00:00",
			EPCList: []string{"urn:epc:id:sscc:9506000.000000001" + string(rune('0'+i))}})
	}
	b, _ := lib.WriteJSON(lib.NewDocument(evs))
	e.Must(http.StatusAccepted, e.Call(su.Token, "POST", "/api/v1/epcis/capture", b), nil)
	b, _ = lib.WriteJSON(lib.NewDocument(evs[:1]))
	e.Must(http.StatusTooManyRequests, e.Call(su.Token, "POST", "/api/v1/epcis/capture", b), nil)
	var u struct {
		Events int
		Quota  map[string]int
	}
	e.Must(http.StatusOK, e.Call(su.Token, "GET", "/api/v1/tenant/usage", nil), &u)
	if u.Events != 2 || u.Quota["eventsPerMonth"] != 2 {
		t.Fatalf("usage: %+v", u)
	}

	// Billing push to Lago.
	for _, m := range e.App.Modules {
		if s, ok := m.(*saas.Module); ok {
			if err := s.Schedules()[0].Run(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	mu.Lock()
	if len(lagoEvents) != 1 || lagoEvents[0]["event"].(map[string]any)["external_subscription_id"] != "nordic" {
		t.Fatalf("lago: %+v", lagoEvents)
	}
	mu.Unlock()

	// Consented support access.
	e.Must(http.StatusForbidden, e.Call(op.Token, "POST", "/api/v1/operator/tenants/"+su.Tenant.ID+"/impersonate", map[string]any{"reason": "customer ticket 1234"}), nil)
	e.Must(http.StatusOK, e.Call(su.Token, "POST", "/api/v1/tenant/support-access", map[string]any{"hours": 2, "level": "read"}), nil)
	var imp struct{ Token string }
	e.Must(http.StatusOK, e.Call(op.Token, "POST", "/api/v1/operator/tenants/"+su.Tenant.ID+"/impersonate", map[string]any{"reason": "customer ticket 1234"}), &imp)
	e.Must(http.StatusOK, e.Call(imp.Token, "GET", "/api/v1/orgs", nil), nil)
	e.Must(http.StatusForbidden, e.Call(imp.Token, "POST", "/api/v1/orgs", map[string]any{"name": "x", "kind": "manufacturer"}), nil)
	var aud struct{ Items []struct{ Action, Actor, Reason string } }
	e.Must(http.StatusOK, e.Call(su.Token, "GET", "/api/v1/audit?action=operator.", nil), &aud)
	if len(aud.Items) != 2 || aud.Items[0].Reason == "" {
		t.Fatalf("operator actions not audited in tenant: %+v", aud.Items)
	}

	// GDPR export then delete with crypto-shredding.
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Call(su.Token, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Export me"}), &p)
	e.Must(http.StatusCreated, e.Call(su.Token, "POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "SE"}), nil)
	ex := e.Call(su.Token, "GET", "/api/v1/tenant/export", nil)
	var dump map[string]json.RawMessage
	if ex.Status != 200 || json.Unmarshal(ex.Body, &dump) != nil || !strings.Contains(string(dump["products"]), "Export me") || len(dump["epcisEvents"]) < 10 {
		t.Fatalf("export: %d %.300s", ex.Status, ex.Body)
	}
	if strings.Contains(string(ex.Body), "password_hash") || strings.Contains(string(ex.Body), "key_material") {
		t.Fatal("export leaked secrets")
	}
	e.Must(http.StatusBadRequest, e.Call(su.Token, "DELETE", "/api/v1/tenant", map[string]any{"password": testkit.Password, "confirmSlug": "acme", "reason": "leaving"}), nil)
	e.Must(http.StatusOK, e.Call(su.Token, "DELETE", "/api/v1/tenant", map[string]any{"password": testkit.Password, "confirmSlug": "nordic", "reason": "leaving"}), nil)
	e.Must(http.StatusUnauthorized, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "nordic", "email": "ceo@nordic.test", "password": testkit.Password}), nil)
	c, _ := pgx.Connect(ctx, e.DBURL)
	defer c.Close(ctx)
	var keys, events int
	tx, _ := c.Begin(ctx)
	_, _ = tx.Exec(ctx, "select set_config('app.operator','on',true)")
	_ = tx.QueryRow(ctx, "select (select count(*) from tenant_key where tenant_id=$1), (select count(*) from epcis_event where tenant_id=$1)", su.Tenant.ID).Scan(&keys, &events)
	_ = tx.Rollback(ctx)
	if keys != 0 || events != 0 {
		t.Fatalf("tenant data remains: keys=%d events=%d", keys, events)
	}
}
