package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/openspoor/openspoor/internal/core/auth"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTest(plans map[string]PlanLimits, plan string) (*Limiter, *clock, *atomic.Int32) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	var lookups atomic.Int32
	l := New(plans, func(context.Context, uuid.UUID) (string, error) { lookups.Add(1); return plan, nil })
	l.now = c.now
	return l, c, &lookups
}

func user(tenant uuid.UUID) auth.Principal {
	return auth.Principal{TenantID: tenant, UserID: uuid.New(), Kind: "user", Roles: []string{auth.RoleAdmin}}
}

func key(tenant, id uuid.UUID) auth.Principal {
	return auth.Principal{TenantID: tenant, KeyID: id, Kind: "apikey", Roles: []string{auth.RoleIntegration}}
}

func TestTenantBucketBurstRefillAndIsolation(t *testing.T) {
	l, c, _ := newTest(map[string]PlanLimits{"p": {Tenant: Limit{2, 3}}}, "p")
	a, b := uuid.New(), uuid.New()
	for i := 0; i < 3; i++ {
		if !l.Allow(context.Background(), user(a)).Allowed {
			t.Fatalf("request %d refused inside the burst", i)
		}
	}
	d := l.Allow(context.Background(), user(a))
	if d.Allowed || d.Scope != "tenant" || d.RetryAfter <= 0 || d.RetryAfter > 500*time.Millisecond+time.Millisecond {
		t.Fatalf("fourth request: %+v", d)
	}
	if !l.Allow(context.Background(), user(b)).Allowed {
		t.Fatal("another tenant shares the bucket")
	}
	c.advance(500 * time.Millisecond) // one token at 2/s
	if !l.Allow(context.Background(), user(a)).Allowed {
		t.Fatal("no refill")
	}
	if l.Allow(context.Background(), user(a)).Allowed {
		t.Fatal("refill is too generous")
	}
	c.advance(time.Hour) // refill is capped at the burst
	n := 0
	for l.Allow(context.Background(), user(a)).Allowed {
		n++
	}
	if n != 3 {
		t.Fatalf("burst after a long pause = %d", n)
	}
}

func TestAPIKeyBucketIsCheckedFirstAndSeparate(t *testing.T) {
	l, _, _ := newTest(map[string]PlanLimits{"p": {Tenant: Limit{100, 100}, Key: Limit{1, 2}}}, "p")
	tn, k1, k2 := uuid.New(), uuid.New(), uuid.New()
	for i := 0; i < 2; i++ {
		if !l.Allow(context.Background(), key(tn, k1)).Allowed {
			t.Fatal("key refused inside its burst")
		}
	}
	d := l.Allow(context.Background(), key(tn, k1))
	if d.Allowed || d.Scope != "apikey" {
		t.Fatalf("%+v", d)
	}
	if !l.Allow(context.Background(), key(tn, k2)).Allowed {
		t.Fatal("a second key of the tenant is affected")
	}
	if !l.Allow(context.Background(), user(tn)).Allowed {
		t.Fatal("a user of the tenant is affected by a key's limit")
	}
	// A refused key request does not draw on the tenant bucket.
	l2, _, _ := newTest(map[string]PlanLimits{"p": {Tenant: Limit{1, 3}, Key: Limit{1, 1}}}, "p")
	l2.Allow(context.Background(), key(tn, k1))
	for i := 0; i < 5; i++ {
		l2.Allow(context.Background(), key(tn, k1)) // refused
	}
	for i := 0; i < 2; i++ { // 3 tenant tokens, one spent by the allowed key request
		if !l2.Allow(context.Background(), user(tn)).Allowed {
			t.Fatal("refused key requests consumed tenant tokens")
		}
	}
}

func TestTenantLimitAppliesToAllKeysTogether(t *testing.T) {
	l, _, _ := newTest(map[string]PlanLimits{"p": {Tenant: Limit{1, 2}, Key: Limit{10, 10}}}, "p")
	tn := uuid.New()
	ok := 0
	for i := 0; i < 6; i++ {
		if l.Allow(context.Background(), key(tn, uuid.New())).Allowed {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("%d requests passed the tenant bucket", ok)
	}
}

func TestUnlimitedPlanAndUnknownPlanFallback(t *testing.T) {
	plans := DefaultPlans()
	l, _, _ := newTest(plans, "community")
	tn := uuid.New()
	for i := 0; i < 5000; i++ {
		if !l.Allow(context.Background(), user(tn)).Allowed {
			t.Fatal("community is limited")
		}
	}
	l, _, _ = newTest(plans, "no-such-plan")
	n := 0
	for l.Allow(context.Background(), user(tn)).Allowed {
		n++
	}
	if n != plans[FallbackPlan].Tenant.Burst {
		t.Fatalf("unknown plan allowed %d, want the %s burst %d", n, FallbackPlan, plans[FallbackPlan].Tenant.Burst)
	}
}

func TestPlanLookupCachedAndPlanChangeFollows(t *testing.T) {
	plan := "a"
	var lookups int
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := New(map[string]PlanLimits{"a": {Tenant: Limit{1, 1}}, "b": {Tenant: Limit{1, 5}}, FallbackPlan: {Tenant: Limit{1, 1}}},
		func(context.Context, uuid.UUID) (string, error) { lookups++; return plan, nil })
	l.now = c.now
	tn := uuid.New()
	l.Allow(context.Background(), user(tn))
	l.Allow(context.Background(), user(tn))
	if lookups != 1 {
		t.Fatalf("plan looked up %d times for two requests", lookups)
	}
	plan = "b"
	c.advance(planTTL + time.Second)
	if !l.Allow(context.Background(), user(tn)).Allowed || lookups != 2 {
		t.Fatalf("plan change not followed (lookups %d)", lookups)
	}
}

func TestPlanLookupErrorFallsBackWithoutCaching(t *testing.T) {
	calls := 0
	l := New(DefaultPlans(), func(context.Context, uuid.UUID) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("db down")
		}
		return "community", nil
	})
	tn := uuid.New()
	if !l.Allow(context.Background(), user(tn)).Allowed {
		t.Fatal("first request refused")
	}
	if d := l.Allow(context.Background(), user(tn)); !d.Allowed || d.Plan != "community" {
		t.Fatalf("error was cached: %+v", d)
	}
}

func TestSweepDropsIdleBuckets(t *testing.T) {
	l, c, _ := newTest(map[string]PlanLimits{"p": {Tenant: Limit{1, 1}}}, "p")
	for i := 0; i < 20; i++ {
		l.Allow(context.Background(), user(uuid.New()))
	}
	c.advance(time.Hour)
	l.Allow(context.Background(), user(uuid.New()))
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d buckets after the sweep", n)
	}
}

func TestParsePlans(t *testing.T) {
	got, err := ParsePlans("trial=2/4, starter=10/20/3/6, growth=unlimited", DefaultPlans())
	if err != nil {
		t.Fatal(err)
	}
	if got["trial"].Tenant != (Limit{2, 4}) || got["trial"].Key != (Limit{1, 2}) {
		t.Fatalf("trial %+v", got["trial"])
	}
	if got["starter"].Key != (Limit{3, 6}) || !got["growth"].Tenant.Unlimited() || got["enterprise"].Tenant.Rate != 1000 {
		t.Fatalf("%+v", got)
	}
	for _, bad := range []string{"trial", "trial=5", "trial=a/b", "trial=-1/2", "=1/2", "trial=1/2/3", "trial=0/5"} {
		if _, err := ParsePlans(bad, DefaultPlans()); err == nil && bad != "trial=0/5" {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParsePlans("trial=0/5", DefaultPlans()); err == nil {
		t.Error("zero rate with a burst accepted")
	}
	if d := Describe(DefaultPlans()); len(d) != 5 || d[0] != "community=unlimited" {
		t.Fatalf("%v", d)
	}
}

func serve(l *Limiter, p auth.Principal, hits *int) *httptest.ResponseRecorder {
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { *hits++ }))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/products", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r.WithContext(auth.With(r.Context(), p)))
	return rec
}

func TestMiddlewareAnswers429WithRetryAfterAndExemptsOperators(t *testing.T) {
	l, _, _ := newTest(map[string]PlanLimits{"p": {Tenant: Limit{0.5, 1}}}, "p")
	tn := uuid.New()
	hits := 0
	if rec := serve(l, user(tn), &hits); rec.Code != http.StatusOK {
		t.Fatalf("first: %d", rec.Code)
	}
	rec := serve(l, user(tn), &hits)
	if rec.Code != http.StatusTooManyRequests || hits != 1 {
		t.Fatalf("second: %d, handler hits %d", rec.Code, hits)
	}
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || ra != 2 { // 1 token at 0.5/s = 2 s
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	op := user(tn)
	op.Operator = true
	sys := auth.System(tn)
	for i := 0; i < 50; i++ {
		if rec := serve(l, op, &hits); rec.Code != http.StatusOK {
			t.Fatalf("operator limited at %d", i)
		}
		if rec := serve(l, sys, &hits); rec.Code != http.StatusOK {
			t.Fatalf("system limited at %d", i)
		}
	}
	// No principal (a route outside authentication) passes through untouched.
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("anonymous: %d", rec.Code)
	}
}

func TestMetrics(t *testing.T) {
	l, _, _ := newTest(map[string]PlanLimits{"p": {Tenant: Limit{1, 1}}}, "p")
	tn := uuid.New()
	hits := 0
	before := counter(t, "p", "tenant", "limited")
	serve(l, user(tn), &hits)
	serve(l, user(tn), &hits)
	if got := counter(t, "p", "tenant", "limited") - before; got != 1 {
		t.Fatalf("limited counter moved by %v", got)
	}
}

// counter reads openspoor_ratelimit_decisions_total from the default registry.
func counter(t *testing.T, plan, scope, decision string) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != "openspoor_ratelimit_decisions_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			lv := map[string]string{}
			for _, l := range m.GetLabel() {
				lv[l.GetName()] = l.GetValue()
			}
			if lv["plan"] == plan && lv["scope"] == scope && lv["decision"] == decision {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}
