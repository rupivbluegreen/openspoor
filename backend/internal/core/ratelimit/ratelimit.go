// Package ratelimit limits the request rate of the authenticated API per tenant and per API key.
//
// Each tenant has one token bucket and each API key has another, both sized by the tenant's plan.
// A request must find a token in its key's bucket (when it authenticates with a key) and in its
// tenant's bucket; otherwise it is answered 429 with a Retry-After header and never reaches the
// handler. A key is checked first, so a runaway integration exhausts its own bucket before the
// tenant's. Platform operators and system principals are exempt.
//
// State is in memory, per node: with n nodes behind a load balancer a tenant can use up to n times
// its limit. The limit is a protective ceiling against runaway clients, not a billing meter.
package ratelimit

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
)

// Limit is a token bucket: Rate tokens per second up to Burst. Rate 0 means unlimited.
type Limit struct {
	Rate  float64
	Burst int
}

// Unlimited reports whether the limit is off.
func (l Limit) Unlimited() bool { return l.Rate <= 0 || l.Burst <= 0 }

// PlanLimits are the buckets for one plan: one per tenant and one per API key.
type PlanLimits struct{ Tenant, Key Limit }

// FallbackPlan applies to a tenant whose plan is unknown or cannot be looked up.
const FallbackPlan = "trial"

// DefaultPlans are the documented limits (requests per second / burst) of the plans in package saas.
// Self-hosted "community" installations are unlimited.
func DefaultPlans() map[string]PlanLimits {
	return map[string]PlanLimits{
		"community":  {},
		"trial":      {Tenant: Limit{10, 20}, Key: Limit{5, 10}},
		"starter":    {Tenant: Limit{50, 100}, Key: Limit{25, 50}},
		"growth":     {Tenant: Limit{200, 400}, Key: Limit{100, 200}},
		"enterprise": {Tenant: Limit{1000, 2000}, Key: Limit{500, 1000}},
	}
}

// ParsePlans applies overrides to base. The spec is a comma-separated list of
// plan=tenantRPS/tenantBurst[/keyRPS/keyBurst] entries; "plan=unlimited" turns the plan's limits off.
// Without key values the API key limit is half the tenant limit.
func ParsePlans(spec string, base map[string]PlanLimits) (map[string]PlanLimits, error) {
	out := make(map[string]PlanLimits, len(base))
	for k, v := range base {
		out[k] = v
	}
	for _, ent := range strings.Split(spec, ",") {
		ent = strings.TrimSpace(ent)
		if ent == "" {
			continue
		}
		name, val, ok := strings.Cut(ent, "=")
		name, val = strings.TrimSpace(name), strings.TrimSpace(val)
		if !ok || name == "" {
			return nil, fmt.Errorf("rate limit %q: want plan=rps/burst[/keyRps/keyBurst]", ent)
		}
		if val == "unlimited" || val == "0" {
			out[name] = PlanLimits{}
			continue
		}
		parts := strings.Split(val, "/")
		if len(parts) != 2 && len(parts) != 4 {
			return nil, fmt.Errorf("rate limit %q: want plan=rps/burst[/keyRps/keyBurst]", ent)
		}
		nums := make([]float64, len(parts))
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) || f > 1e9 {
				return nil, fmt.Errorf("rate limit %q: %q is not a positive number", ent, p)
			}
			nums[i] = f
		}
		pl := PlanLimits{Tenant: Limit{nums[0], int(math.Ceil(nums[1]))}}
		if len(nums) == 4 {
			pl.Key = Limit{nums[2], int(math.Ceil(nums[3]))}
		} else {
			pl.Key = Limit{math.Max(nums[0]/2, 0.001), max(int(math.Ceil(nums[1]/2)), 1)}
		}
		out[name] = pl
	}
	return out, nil
}

var decisions = promauto.NewCounterVec(prometheus.CounterOpts{Name: "openspoor_ratelimit_decisions_total",
	Help: "API requests by rate-limit decision (allowed, limited, exempt), scope (tenant, apikey) and plan"}, []string{"plan", "scope", "decision"})

var bucketsGauge = promauto.NewGauge(prometheus.GaugeOpts{Name: "openspoor_ratelimit_buckets",
	Help: "Token buckets currently held in memory on this node"})

type scope uint8

const (
	scopeTenant scope = iota
	scopeKey
)

func (s scope) String() string {
	if s == scopeKey {
		return "apikey"
	}
	return "tenant"
}

type bucketKey struct {
	scope scope
	id    uuid.UUID
}

type bucket struct {
	tokens float64
	last   time.Time
	refill time.Duration // time from empty to full
}

type planEntry struct {
	plan    string
	expires time.Time
}

// PlanFunc returns the plan id of a tenant.
type PlanFunc func(ctx context.Context, tenant uuid.UUID) (string, error)

// Limiter holds the buckets of one node.
type Limiter struct {
	plans  map[string]PlanLimits
	planOf PlanFunc
	now    func() time.Time

	mu        sync.Mutex
	buckets   map[bucketKey]*bucket
	plansSeen map[uuid.UUID]planEntry
	lastSweep time.Time
}

const (
	planTTL       = time.Minute
	sweepInterval = time.Minute
)

// New creates a limiter. planOf resolves a tenant's plan; its answers are cached for a minute.
func New(plans map[string]PlanLimits, planOf PlanFunc) *Limiter {
	return &Limiter{plans: plans, planOf: planOf, now: time.Now, buckets: map[bucketKey]*bucket{}, plansSeen: map[uuid.UUID]planEntry{}}
}

// take removes one token from the bucket and reports the wait until the next one when it is empty.
// The caller holds l.mu.
func (l *Limiter) take(k bucketKey, lim Limit, now time.Time) (bool, time.Duration) {
	b := l.buckets[k]
	if b == nil {
		b = &bucket{tokens: float64(lim.Burst), last: now}
		l.buckets[k] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = math.Min(float64(lim.Burst), b.tokens+el*lim.Rate)
		b.last = now
	}
	b.tokens = math.Min(b.tokens, float64(lim.Burst)) // a plan change may have lowered the burst
	b.refill = time.Duration(float64(lim.Burst) / lim.Rate * float64(time.Second))
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / lim.Rate * float64(time.Second))
}

// sweep drops buckets that have been idle long enough to be full again (equivalent to a fresh one),
// and expired plan cache entries. The caller holds l.mu.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		// A bucket idle for longer than it takes to refill is the same as a fresh one.
		if now.Sub(b.last) > max(b.refill, 10*time.Minute) {
			delete(l.buckets, k)
		}
	}
	for t, e := range l.plansSeen {
		if now.After(e.expires) {
			delete(l.plansSeen, t)
		}
	}
	bucketsGauge.Set(float64(len(l.buckets)))
}

// Decision is the outcome of Allow.
type Decision struct {
	Allowed    bool
	Plan       string
	Scope      string // which bucket refused: "tenant" or "apikey"
	RetryAfter time.Duration
}

func (l *Limiter) planFor(ctx context.Context, tenant uuid.UUID, now time.Time) string {
	l.mu.Lock()
	e, ok := l.plansSeen[tenant]
	l.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.plan
	}
	plan, err := l.planOf(ctx, tenant)
	if err != nil {
		return FallbackPlan // not cached: the next request asks again
	}
	l.mu.Lock()
	l.plansSeen[tenant] = planEntry{plan: plan, expires: now.Add(planTTL)}
	l.mu.Unlock()
	return plan
}

// Allow charges one request of the principal to its buckets.
func (l *Limiter) Allow(ctx context.Context, p auth.Principal) Decision {
	now := l.now()
	plan := l.planFor(ctx, p.TenantID, now)
	pl, ok := l.plans[plan]
	if !ok {
		plan = FallbackPlan
		pl = l.plans[FallbackPlan]
	}
	d := Decision{Allowed: true, Plan: plan}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	if p.Kind == "apikey" && p.KeyID != uuid.Nil && !pl.Key.Unlimited() {
		if ok, wait := l.take(bucketKey{scopeKey, p.KeyID}, pl.Key, now); !ok {
			return Decision{Plan: plan, Scope: scopeKey.String(), RetryAfter: wait}
		}
	}
	if !pl.Tenant.Unlimited() {
		if ok, wait := l.take(bucketKey{scopeTenant, p.TenantID}, pl.Tenant, now); !ok {
			return Decision{Plan: plan, Scope: scopeTenant.String(), RetryAfter: wait}
		}
	}
	return d
}

// Exempt reports whether the principal is outside the limits: platform operators and system work.
func Exempt(p auth.Principal) bool { return p.Operator || p.Kind == "system" }

func planLabel(l *Limiter, plan string) string {
	if _, ok := l.plans[plan]; ok {
		return plan
	}
	return "other"
}

// Middleware enforces the limits. It must run after authentication.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.From(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if Exempt(p) {
			decisions.WithLabelValues("exempt", "tenant", "exempt").Inc()
			next.ServeHTTP(w, r)
			return
		}
		d := l.Allow(r.Context(), p)
		if d.Allowed {
			decisions.WithLabelValues(planLabel(l, d.Plan), "tenant", "allowed").Inc()
			next.ServeHTTP(w, r)
			return
		}
		decisions.WithLabelValues(planLabel(l, d.Plan), d.Scope, "limited").Inc()
		secs := int(math.Ceil(d.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		httpx.Fail(w, r, httpx.TooMany(fmt.Sprintf("rate limit exceeded for this %s; retry after %d s", d.Scope, secs)))
	})
}

// Describe lists the effective limits, for logs and documentation.
func Describe(plans map[string]PlanLimits) []string {
	names := make([]string, 0, len(plans))
	for n := range plans {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		pl := plans[n]
		if pl.Tenant.Unlimited() {
			out = append(out, n+"=unlimited")
			continue
		}
		out = append(out, fmt.Sprintf("%s=%g/%d/%g/%d", n, pl.Tenant.Rate, pl.Tenant.Burst, pl.Key.Rate, pl.Key.Burst))
	}
	return out
}
