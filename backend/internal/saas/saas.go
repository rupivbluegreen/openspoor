// Package saas makes the same binary run as a self-hostable SaaS: self-service sign-up and
// tenant provisioning, subdomain and custom-domain routing (with an on-demand TLS "ask"
// endpoint), per-tenant branding, plans, quotas and OpenFeature feature flags, usage
// metering to a billing adapter (Lago; off when self-hosted), the operator console APIs
// with consented and audited support access, region pinning, tenant export and deletion
// (GDPR) with crypto-shredding of the tenant's data key.
package saas

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	of "github.com/open-feature/go-sdk/openfeature"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/platform"
)

// Plan defines quotas and features. A quota of 0 means unlimited.
type Plan struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	EventsPerMonth int64           `json:"eventsPerMonth"`
	ActivePartners int64           `json:"activePartners"`
	Users          int64           `json:"users"`
	Features       map[string]bool `json:"features"`
}

// Plans available on a SaaS installation. Self-hosted installs use "community" (no limits, every feature).
var Plans = map[string]Plan{
	"community":  {ID: "community", Name: "Community (self-hosted)", Features: allFeatures()},
	"trial":      {ID: "trial", Name: "Trial", EventsPerMonth: 100_000, ActivePartners: 5, Users: 10, Features: map[string]bool{"assistant": true, "agents": true, "food": true}},
	"starter":    {ID: "starter", Name: "Starter", EventsPerMonth: 1_000_000, ActivePartners: 25, Users: 25, Features: map[string]bool{"food": true, "custom_domain": true}},
	"growth":     {ID: "growth", Name: "Growth", EventsPerMonth: 20_000_000, ActivePartners: 250, Users: 250, Features: map[string]bool{"assistant": true, "agents": true, "food": true, "custom_domain": true, "federation": true, "sso": true}},
	"enterprise": {ID: "enterprise", Name: "Enterprise", Features: allFeatures()},
}

func allFeatures() map[string]bool {
	return map[string]bool{"assistant": true, "agents": true, "food": true, "custom_domain": true, "federation": true, "sso": true}
}

// Module is the SaaS bounded context.
type Module struct {
	kernel.Deps
	Platform *platform.Module
	Billing  Billing
	flags    *of.Client
}

// New creates the module and installs the feature-flag provider.
func New(d kernel.Deps, p *platform.Module) *Module {
	m := &Module{Deps: d, Platform: p, Billing: NewBilling(d.Cfg)}
	var prov of.FeatureProvider = &PlanProvider{m: m}
	if up, err := unleashProvider(d.Cfg); err == nil && up != nil {
		prov = up
	}
	_ = of.SetProviderAndWait(prov)
	m.flags = of.NewClient("openspoor")
	return m
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "saas" }

// Public implements kernel.PublicRoutes.
func (m *Module) Public(r chi.Router) {
	r.Post("/saas/signup", httpx.Handler(m.signup))
	r.Get("/saas/branding", httpx.Handler(m.branding))
	r.Get("/saas/plans", httpx.Handler(m.plans))
	r.Get("/saas/tls-ask", m.tlsAsk)
}

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Get("/tenant/usage", httpx.Handler(m.usage))
	r.Get("/tenant/features", httpx.Handler(m.features))
	r.Get("/tenant/branding", httpx.Handler(m.getBranding))
	r.Group(func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin))
		r.Put("/tenant/branding", httpx.Handler(m.putBranding))
		r.Post("/tenant/domain", httpx.Handler(m.setDomain))
		r.Post("/tenant/domain/verify", httpx.Handler(m.verifyDomain))
		r.Post("/tenant/support-access", httpx.Handler(m.grantSupport))
		// The export is every record of the tenant: Admin only, also for GET.
		r.With(auth.RequireStrict(auth.RoleAdmin)).Get("/tenant/export", httpx.Handler(m.export))
		r.Post("/tenant/export", httpx.Handler(m.export))
		r.Delete("/tenant", httpx.Handler(m.deleteTenant))
	})
	r.Route("/operator", func(r chi.Router) {
		r.Use(auth.RequireOperator)
		r.Get("/tenants", httpx.Handler(m.opTenants))
		r.Patch("/tenants/{id}", httpx.Handler(m.opUpdate))
		r.Post("/tenants/{id}/impersonate", httpx.Handler(m.opImpersonate))
		r.Get("/hub/analytics", httpx.Handler(m.hubAnalytics))
	})
}

// Schedules implements kernel.Scheduled.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: "saas.billing", Every: time.Hour, Run: m.pushUsage}}
}

// Wire subscribes to metering events and installs the capture quota gate.
func (m *Module) Wire(setGate func(func(ctx context.Context, tenant uuid.UUID, events int) error)) {
	m.Bus.Subscribe("epcis.accepted", func(ctx context.Context, e busEvent) {
		var d struct {
			Events int `json:"events"`
		}
		if json.Unmarshal(e.Data, &d) == nil && d.Events > 0 {
			_ = m.Meter(ctx, e.TenantID, "epcis_events", int64(d.Events))
		}
	})
	setGate(m.CheckEvents)
}

func period(t time.Time) string { return t.UTC().Format("2006-01") }

// Meter adds to a tenant's usage counter for the current month.
func (m *Module) Meter(ctx context.Context, tenant uuid.UUID, metric string, n int64) error {
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into usage_counter (tenant_id, period, metric, value) values (app_tenant(), $1, $2, $3)
			on conflict (tenant_id, period, metric) do update set value = usage_counter.value + excluded.value`, period(time.Now()), metric, n)
		return err
	})
}

type tenantPlan struct {
	Plan   string           `db:"plan"`
	Quotas map[string]int64 `db:"quotas"`
}

func (m *Module) planOf(ctx context.Context, tx pgx.Tx) (Plan, map[string]int64, error) {
	tp, err := db.One[tenantPlan](ctx, tx, `select plan, quotas from tenant where id=app_tenant()`)
	if err != nil {
		return Plan{}, nil, err
	}
	p, ok := Plans[tp.Plan]
	if !ok {
		p = Plans["trial"]
	}
	q := map[string]int64{"eventsPerMonth": p.EventsPerMonth, "activePartners": p.ActivePartners, "users": p.Users}
	for k, v := range tp.Quotas {
		q[k] = v
	}
	return p, q, nil
}

// CheckEvents rejects a capture that would exceed the monthly event quota (SaaS mode only).
func (m *Module) CheckEvents(ctx context.Context, tenant uuid.UUID, events int) error {
	if m.Cfg.Mode != config.ModeSaaS {
		return nil
	}
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		_, q, err := m.planOf(ctx, tx)
		if err != nil {
			return err
		}
		limit := q["eventsPerMonth"]
		if limit == 0 {
			return nil
		}
		var used int64
		_ = tx.QueryRow(ctx, `select coalesce(sum(value),0) from usage_counter where period=$1 and metric='epcis_events'`, period(time.Now())).Scan(&used)
		if used+int64(events) > limit {
			return httpx.TooMany(fmt.Sprintf("monthly EPCIS event quota reached (%d of %d); upgrade the plan or wait for the next period", used, limit))
		}
		return nil
	})
}

// Usage is the tenant's metered usage.
type Usage struct {
	Period         string           `json:"period"`
	Plan           Plan             `json:"plan"`
	Events         int64            `json:"events"`
	Reports        int64            `json:"reports"`
	ActivePartners int64            `json:"activePartners"`
	Users          int64            `json:"users"`
	Quota          map[string]int64 `json:"quota"`
	BillingDriver  string           `json:"billingDriver"`
}

// UsageFor computes usage inside a tenant transaction.
func (m *Module) UsageFor(ctx context.Context, tx pgx.Tx) (Usage, error) {
	p, q, err := m.planOf(ctx, tx)
	if err != nil {
		return Usage{}, err
	}
	u := Usage{Period: period(time.Now()), Plan: p, Quota: q, BillingDriver: m.Billing.Name()}
	start := time.Now().UTC().Format("2006-01") + "-01"
	err = tx.QueryRow(ctx, `select
		coalesce((select sum(value) from usage_counter where period=$1 and metric='epcis_events'),0),
		(select count(*) from regulatory_report where created_at >= $2::date),
		(select count(*) from partner where status='active'),
		(select count(*) from app_user where status='active')`, u.Period, start).Scan(&u.Events, &u.Reports, &u.ActivePartners, &u.Users)
	return u, err
}

func (m *Module) usage(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		u, err := m.UsageFor(r.Context(), tx)
		if err == nil {
			httpx.JSON(w, http.StatusOK, u)
		}
		return err
	})
}

func (m *Module) plans(w http.ResponseWriter, _ *http.Request) error {
	out := []Plan{}
	for _, id := range []string{"trial", "starter", "growth", "enterprise", "community"} {
		out = append(out, Plans[id])
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Plan]{Items: out, Total: len(out)})
	return nil
}

// FlagKeys are the evaluated feature flags.
var FlagKeys = []string{"assistant", "agents", "food", "custom_domain", "federation", "sso"}

// Enabled evaluates a feature flag for the caller's tenant through OpenFeature.
func (m *Module) Enabled(ctx context.Context, tenant uuid.UUID, flag string) bool {
	v, _ := m.flags.BooleanValue(ctx, flag, false, of.NewEvaluationContext(tenant.String(), map[string]any{"mode": string(m.Cfg.Mode)}))
	return v
}

func (m *Module) features(w http.ResponseWriter, r *http.Request) error {
	t := kernel.TenantOf(r.Context())
	out := map[string]bool{}
	for _, k := range FlagKeys {
		out[k] = m.Enabled(r.Context(), t, k)
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

// --- sign-up and routing ---

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

var reservedSlugs = map[string]bool{"www": true, "api": true, "app": true, "admin": true, "operator": true, "verify": true, "status": true, "mail": true}

func (m *Module) signup(w http.ResponseWriter, r *http.Request) error {
	if m.Cfg.Mode != config.ModeSaaS {
		return httpx.NotFound("self-service sign-up is only available in SaaS mode")
	}
	var in struct {
		CompanyName string   `json:"companyName"`
		Slug        string   `json:"slug"`
		Email       string   `json:"email"`
		Name        string   `json:"name"`
		Password    string   `json:"password"`
		Verticals   []string `json:"verticals"`
		Region      string   `json:"region"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !slugRe.MatchString(in.Slug) || reservedSlugs[in.Slug] {
		return httpx.BadRequest("slug must be 2-41 lowercase letters, digits or dashes and not reserved")
	}
	for _, v := range in.Verticals {
		if v != "pharma" && v != "food" {
			return httpx.BadRequest("verticals must be pharma and/or food")
		}
	}
	t, _, err := m.Platform.CreateTenant(r.Context(), platform.NewTenant{Slug: in.Slug, Name: in.CompanyName, Plan: "trial", Region: in.Region,
		Verticals: in.Verticals, AdminEmail: in.Email, AdminName: in.Name, AdminPassword: in.Password})
	if err != nil {
		return err
	}
	p, err := m.Platform.Login(r.Context(), t.Slug, in.Email, in.Password)
	if err != nil {
		return err
	}
	tok, exp, err := m.Auth.Issue(p)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"tenant": t, "token": tok, "expiresAt": exp, "url": m.TenantURL(t.Slug)})
	return nil
}

// TenantURL is the tenant's console URL.
func (m *Module) TenantURL(slug string) string {
	if m.Cfg.Mode != config.ModeSaaS {
		return m.Cfg.PublicURL
	}
	return "https://" + slug + "." + m.Cfg.BaseDomain
}

// SlugFromHost maps "<slug>.<base domain>" or a verified custom domain to a tenant slug.
func (m *Module) SlugFromHost(ctx context.Context, host string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if s, ok := strings.CutSuffix(host, "."+m.Cfg.BaseDomain); ok && !strings.Contains(s, ".") && !reservedSlugs[s] {
		return s, true
	}
	var slug string
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select slug from tenant where custom_domain=$1 and domain_verified`, host).Scan(&slug)
	})
	return slug, err == nil
}

// branding is public so the login page can show the tenant's name and colours.
func (m *Module) branding(w http.ResponseWriter, r *http.Request) error {
	host := r.URL.Query().Get("host")
	if host == "" {
		host = r.Host
	}
	slug, ok := m.SlugFromHost(r.Context(), host)
	if !ok {
		slug = r.URL.Query().Get("slug")
	}
	t, err := m.Platform.TenantBySlug(r.Context(), slug)
	if err != nil {
		httpx.JSON(w, http.StatusOK, map[string]any{"name": "Open Spoor", "brand": map[string]any{}})
		return nil
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"name": t.Name, "slug": t.Slug, "brand": t.Brand})
	return nil
}

// tlsAsk answers a reverse proxy's on-demand TLS check (e.g. Caddy "ask"): 200 only for
// domains that belong to a tenant, so certificates are never issued for arbitrary hosts.
func (m *Module) tlsAsk(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.SlugFromHost(r.Context(), r.URL.Query().Get("domain")); ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (m *Module) getBranding(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var b map[string]any
		var domain *string
		var verified bool
		if err := tx.QueryRow(r.Context(), `select brand, custom_domain, domain_verified from tenant where id=app_tenant()`).Scan(&b, &domain, &verified); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"brand": b, "customDomain": domain, "domainVerified": verified})
		return nil
	})
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (m *Module) putBranding(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PrimaryColor string `json:"primaryColor"`
		LogoURL      string `json:"logoUrl"`
		DisplayName  string `json:"displayName"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.PrimaryColor != "" && !colorRe.MatchString(in.PrimaryColor) {
		return httpx.BadRequest("primaryColor must be #RRGGBB")
	}
	if in.LogoURL != "" && !strings.HasPrefix(in.LogoURL, "https://") && !strings.HasPrefix(in.LogoURL, "data:image/") {
		return httpx.BadRequest("logoUrl must be https or a data:image URI")
	}
	brand := map[string]any{"primaryColor": in.PrimaryColor, "logoUrl": in.LogoURL, "displayName": in.DisplayName}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `update tenant set brand=$1 where id=app_tenant()`, brand); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "tenant.branding", Entity: "tenant", EntityID: kernel.TenantOf(r.Context()).String(), After: brand}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"brand": brand})
		return nil
	})
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

func (m *Module) setDomain(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Domain string `json:"domain"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	d := strings.ToLower(strings.TrimSpace(in.Domain))
	if !domainRe.MatchString(d) || strings.HasSuffix(d, "."+m.Cfg.BaseDomain) {
		return httpx.BadRequest("invalid custom domain")
	}
	if !m.Enabled(r.Context(), kernel.TenantOf(r.Context()), "custom_domain") {
		return httpx.Forbidden("custom domains are not included in this plan")
	}
	token := "openspoor-verify=" + ids.Secret(16)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `update tenant set custom_domain=$1, domain_token=$2, domain_verified=false where id=app_tenant()`, d, token); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "tenant.domain", Entity: "tenant", EntityID: kernel.TenantOf(r.Context()).String(), After: map[string]string{"domain": d}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"domain": d, "txtRecord": "_openspoor." + d, "txtValue": token,
			"instructions": "Create the TXT record, point a CNAME to your Open Spoor host, then call /tenant/domain/verify. TLS is issued automatically on first request."})
		return nil
	})
}

// LookupTXT is replaceable in tests.
var LookupTXT = net.LookupTXT

func (m *Module) verifyDomain(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var d, tok *string
		if err := tx.QueryRow(r.Context(), `select custom_domain, domain_token from tenant where id=app_tenant()`).Scan(&d, &tok); err != nil {
			return err
		}
		if d == nil || tok == nil {
			return httpx.Conflict("no custom domain set")
		}
		recs, _ := LookupTXT("_openspoor." + *d)
		ok := false
		for _, rec := range recs {
			if strings.TrimSpace(rec) == *tok {
				ok = true
			}
		}
		if !ok {
			return httpx.Conflict("TXT record not found yet; DNS can take a while to propagate")
		}
		if _, err := tx.Exec(r.Context(), `update tenant set domain_verified=true where id=app_tenant()`); err != nil {
			return err
		}
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "tenant.domain_verified", Entity: "tenant", EntityID: kernel.TenantOf(r.Context()).String(), After: map[string]string{"domain": *d}})
		if err == nil {
			httpx.JSON(w, http.StatusOK, map[string]any{"domain": *d, "verified": true})
		}
		return err
	})
}

// grantSupport lets a tenant admin consent to time-boxed operator support access.
func (m *Module) grantSupport(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Hours int    `json:"hours"`
		Level string `json:"level"` // read | admin | none
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Level != "read" && in.Level != "admin" && in.Level != "none" {
		return httpx.BadRequest("level must be read, admin or none")
	}
	if in.Hours < 0 || in.Hours > 72 {
		return httpx.BadRequest("hours must be 0-72")
	}
	until := time.Now().Add(time.Duration(in.Hours) * time.Hour)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `update tenant set support_until=$1, support_level=$2 where id=app_tenant()`, until, in.Level); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "tenant.support_consent", Entity: "tenant", EntityID: kernel.TenantOf(r.Context()).String(),
			After: map[string]any{"level": in.Level, "until": until}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"level": in.Level, "until": until})
		return nil
	})
}

// --- operator console ---

type opTenant struct {
	ID            uuid.UUID        `json:"id" db:"id"`
	Slug          string           `json:"slug" db:"slug"`
	Name          string           `json:"name" db:"name"`
	Plan          string           `json:"plan" db:"plan"`
	Status        string           `json:"status" db:"status"`
	Region        string           `json:"region" db:"region"`
	Verticals     []string         `json:"verticals" db:"verticals"`
	Quotas        map[string]int64 `json:"quotas" db:"quotas"`
	CustomDomain  *string          `json:"customDomain,omitempty" db:"custom_domain"`
	SupportUntil  *time.Time       `json:"supportUntil,omitempty" db:"support_until"`
	SupportLevel  string           `json:"supportLevel" db:"support_level"`
	CreatedAt     time.Time        `json:"createdAt" db:"created_at"`
	EventsMonth   int64            `json:"eventsThisMonth" db:"events_month"`
	Users         int64            `json:"users" db:"users"`
	OpenAlerts    int64            `json:"openAlerts" db:"open_alerts"`
	LastCaptureAt *time.Time       `json:"lastCaptureAt,omitempty" db:"last_capture"`
	FailedJobs24h int64            `json:"failedJobs24h" db:"failed_jobs"`
}

func (m *Module) opTenants(w http.ResponseWriter, r *http.Request) error {
	var items []opTenant
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		items, err = db.List[opTenant](r.Context(), tx, `select t.id, t.slug, t.name, t.plan, t.status, t.region, t.verticals, t.quotas, t.custom_domain, t.support_until, t.support_level, t.created_at,
			coalesce((select sum(value) from usage_counter u where u.tenant_id=t.id and u.period=$1 and u.metric='epcis_events'),0) as events_month,
			(select count(*) from app_user u where u.tenant_id=t.id and u.status='active') as users,
			(select count(*) from alert a where a.tenant_id=t.id and a.status='open') as open_alerts,
			(select max(created_at) from epcis_capture c where c.tenant_id=t.id) as last_capture,
			(select count(*) from job j where j.tenant_id=t.id and j.status='failed' and j.updated_at > now() - interval '24 hours') as failed_jobs
			from tenant t order by t.created_at desc limit 1000`, period(time.Now()))
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[opTenant]{Items: items, Total: len(items)})
	return nil
}

func (m *Module) opUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Plan   *string          `json:"plan"`
		Status *string          `json:"status"`
		Region *string          `json:"region"`
		Quotas map[string]int64 `json:"quotas"`
		Reason string           `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Plan != nil {
		if _, ok := Plans[*in.Plan]; !ok {
			return httpx.BadRequest("unknown plan")
		}
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "suspended" {
		return httpx.BadRequest("status must be active or suspended")
	}
	// Region pinning: relabelling would claim a residency the data does not have. Moving a
	// tenant is an export and import into an installation in the other region.
	region := m.Cfg.Region
	if region == "" {
		region = "eu"
	}
	if in.Region != nil && *in.Region != region {
		return httpx.BadRequest("a tenant cannot change region on this installation; export it and import it into an installation in " + *in.Region)
	}
	if in.Reason == "" {
		return httpx.BadRequest("reason is required for operator changes")
	}
	op := auth.MustFrom(r.Context())
	var t opTenant
	err = m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[tenantPlan](r.Context(), tx, `select plan, quotas from tenant where id=$1`, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `update tenant set plan=coalesce($2,plan), status=coalesce($3,status), region=coalesce($4,region),
			quotas=case when $5::jsonb is null then quotas else $5::jsonb end where id=$1`, id, in.Plan, in.Status, in.Region, in.Quotas); err != nil {
			return err
		}
		t, err = db.One[opTenant](r.Context(), tx, `select id, slug, name, plan, status, region, verticals, quotas, custom_domain, support_until, support_level, created_at,
			0::bigint as events_month, 0::bigint as users, 0::bigint as open_alerts, null::timestamptz as last_capture, 0::bigint as failed_jobs from tenant where id=$1`, id)
		if err != nil {
			return err
		}
		_, err = audit.WriteAs(r.Context(), tx, id, "operator:"+op.UserID.String(), audit.Record{Action: "operator.tenant.update", Entity: "tenant", EntityID: id.String(), Before: before, After: in, Reason: in.Reason})
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, t)
	return nil
}

// opImpersonate issues a short-lived support token, only while the tenant has consented.
func (m *Module) opImpersonate(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(strings.TrimSpace(in.Reason)) < 10 {
		return httpx.BadRequest("a reason of at least 10 characters is required")
	}
	op := auth.MustFrom(r.Context())
	var level string
	var until *time.Time
	err = m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `select support_level, support_until from tenant where id=$1`, id).Scan(&level, &until); err != nil {
			return err
		}
		if level == "none" || until == nil || until.Before(time.Now()) {
			return httpx.Forbidden("the tenant has not granted support access")
		}
		_, err := audit.WriteAs(r.Context(), tx, id, "operator:"+op.UserID.String(), audit.Record{Action: "operator.impersonate", Entity: "tenant", EntityID: id.String(),
			After: map[string]any{"level": level, "until": until}, Reason: in.Reason})
		return err
	})
	if err != nil {
		return err
	}
	roles := []string{auth.RoleViewer}
	if level == "admin" {
		roles = []string{auth.RoleAdmin}
	}
	p := auth.Principal{TenantID: id, UserID: op.UserID, Email: "support+" + op.Email, Name: "Support (" + op.Email + ")", Roles: roles, Kind: "user"}
	tok, exp, err := m.Auth.Issue(p)
	if err != nil {
		return err
	}
	if exp.After(*until) {
		exp = *until
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"token": tok, "expiresAt": exp, "principal": p, "level": level})
	return nil
}

// --- billing push ---

func (m *Module) pushUsage(ctx context.Context) error {
	if m.Billing.Name() == "none" {
		return nil
	}
	type row struct {
		Tenant uuid.UUID
		Slug   string
		Period string
		Metric string
		Delta  int64
	}
	var rows []row
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rs, err := tx.Query(ctx, `select u.tenant_id, t.slug, u.period, u.metric, u.value - u.billed from usage_counter u join tenant t on t.id=u.tenant_id where u.value > u.billed`)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var x row
			if err := rs.Scan(&x.Tenant, &x.Slug, &x.Period, &x.Metric, &x.Delta); err != nil {
				return err
			}
			rows = append(rows, x)
		}
		return rs.Err()
	})
	if err != nil {
		return err
	}
	var firstErr error
	for _, x := range rows {
		if err := m.Billing.ReportUsage(ctx, x.Slug, x.Metric, x.Period, x.Delta); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_ = m.DB.Tx(ctx, x.Tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update usage_counter set billed = billed + $3 where period=$1 and metric=$2`, x.Period, x.Metric, x.Delta)
			return err
		})
	}
	return firstErr
}
