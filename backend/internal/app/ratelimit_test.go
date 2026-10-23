package app_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/testkit"
)

// Slow refill keeps the tests independent of timing: tenant bucket 8, API key bucket 2.
func limited(c *config.Config) {
	c.RateLimit = true
	c.RateLimitPlans = "community=0.01/8/0.01/2,trial=0.01/8/0.01/2"
}

const probe = "/api/v1/products"

func TestRateLimitPerTenantAnswers429WithRetryAfter(t *testing.T) {
	e := testkit.New(t, limited)
	_, other := e.NewTenant("globex", "admin@globex.test")
	for i := 0; i < 8; i++ {
		e.Must(http.StatusOK, e.Do("GET", probe, nil), nil)
	}
	r := e.Do("GET", probe, nil)
	if r.Status != http.StatusTooManyRequests {
		t.Fatalf("ninth request: %d %s", r.Status, r.Body)
	}
	if ra, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After %q", r.Header.Get("Retry-After"))
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "problem+json") || !strings.Contains(string(r.Body), "rate limit") {
		t.Fatalf("body: %s", r.Body)
	}
	// Writes are limited the same way; another tenant has its own bucket; public routes are not limited.
	e.Must(http.StatusTooManyRequests, e.Do("POST", "/api/v1/products", map[string]any{}), nil)
	e.Must(http.StatusOK, e.Call(other, "GET", probe, nil), nil)
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/meta", nil), nil)

	res, err := http.Get(e.Server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), `openspoor_ratelimit_decisions_total{decision="limited"`) {
		t.Fatal("limited decisions are not exported as a metric")
	}
}

func TestRateLimitPerAPIKeyIsSeparateFromTheTenant(t *testing.T) {
	e := testkit.New(t, limited)
	var k1, k2 struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "one", "roles": []string{"Viewer"}}), &k1)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "two", "roles": []string{"Viewer"}}), &k2)
	for i := 0; i < 2; i++ {
		e.Must(http.StatusOK, e.Call(k1.Secret, "GET", probe, nil), nil)
	}
	r := e.Call(k1.Secret, "GET", probe, nil)
	if r.Status != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" || !strings.Contains(string(r.Body), "apikey") {
		t.Fatalf("third request with key one: %d %s", r.Status, r.Body)
	}
	// The other key and the users of the tenant still have budget (6 of the 8 tenant tokens are used by now).
	e.Must(http.StatusOK, e.Call(k2.Secret, "GET", probe, nil), nil)
	// A Viewer key stays read-only whatever the limit.
	e.Must(http.StatusForbidden, e.Call(k2.Secret, "POST", "/api/v1/products", map[string]any{}), nil)
}

func TestRateLimitExemptsOperators(t *testing.T) {
	e := testkit.New(t, limited)
	tok, _, err := e.App.Deps.Auth.Issue(auth.Principal{TenantID: e.Tenant.ID, UserID: uuid.New(), Roles: []string{auth.RoleAdmin}, Operator: true, Unbound: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		e.Must(http.StatusOK, e.Call(tok, "GET", probe, nil), nil)
	}
	// The operator's traffic does not draw on the tenant's bucket either.
	e.Must(http.StatusOK, e.Do("GET", probe, nil), nil)
}

func TestRateLimitCanBeSwitchedOff(t *testing.T) {
	e := testkit.New(t)
	if e.App.Limiter != nil {
		t.Fatal("the test default has no limiter")
	}
	for i := 0; i < 40; i++ {
		e.Must(http.StatusOK, e.Do("GET", probe, nil), nil)
	}
}
