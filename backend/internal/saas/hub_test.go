package saas_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

func operatorToken(t *testing.T, e *testkit.Env) string {
	t.Helper()
	if _, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "platform", Name: "Platform",
		AdminEmail: "ops@openspoor.test", AdminPassword: testkit.Password, Operator: true}); err != nil {
		t.Fatal(err)
	}
	var op struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "platform", "email": "ops@openspoor.test", "password": testkit.Password}), &op)
	return op.Token
}

func TestHubAnalytics(t *testing.T) {
	e := testkit.New(t, func(c *config.Config) { c.Mode = config.ModeHub })
	op := operatorToken(t, e)
	_, silentToken := e.NewTenant("silent-pharma", "admin@silent.example")
	_ = silentToken

	// acme reports one capture; silent-pharma reports nothing.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Amoxicillin 500mg"}), nil)
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "receiving", Disposition: "in_progress",
		EventTime: mustTime(t, "2026-01-01T10:00:00Z"), EventTimeZoneOffset: "+00:00", EPCList: []string{"urn:epc:id:sscc:0614141.1234567890"}}}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()

	var a struct {
		Days          int
		Organisations []struct {
			Name         string
			Events       int64
			DaysSilent   *int
			LastReportAt *string
		}
		SerialStates []struct {
			Key   string
			Count int64
		}
		Products []struct{ GTIN, Owner string }
	}
	e.Must(http.StatusOK, e.Call(op, "GET", "/api/v1/operator/hub/analytics", nil), &a)
	if a.Days != 30 || len(a.Organisations) != 2 {
		t.Fatalf("organisations (operator tenant excluded): %+v", a.Organisations)
	}
	acme, silent := a.Organisations[0], a.Organisations[1]
	if acme.Name != e.Tenant.Name {
		acme, silent = silent, acme
	}
	if acme.Events != 1 || acme.LastReportAt == nil || silent.Events != 0 || silent.LastReportAt != nil {
		t.Fatalf("reporting per organisation: acme=%+v silent=%+v", acme, silent)
	}
	if len(a.Products) != 1 || a.Products[0].GTIN != "09506000134352" || a.Products[0].Owner != e.Tenant.Name {
		t.Fatalf("products: %+v", a.Products)
	}
	if len(a.SerialStates) == 0 {
		t.Fatal("serial states missing")
	}
	// Tenants never see the market, and the endpoint is hub-only.
	e.Must(http.StatusForbidden, e.Do("GET", "/api/v1/operator/hub/analytics", nil), nil)
}

func TestHubAnalyticsOnlyInHubMode(t *testing.T) {
	e := testkit.New(t, func(c *config.Config) { c.Mode = config.ModeSaaS })
	e.Must(http.StatusNotFound, e.Call(operatorToken(t, e), "GET", "/api/v1/operator/hub/analytics", nil), nil)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
