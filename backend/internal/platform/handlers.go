package platform

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Organization is a legal entity in a tenant.
type Organization struct {
	ID            uuid.UUID `json:"id" db:"id"`
	Name          string    `json:"name" db:"name"`
	Kind          string    `json:"kind" db:"kind"`
	CompanyPrefix *string   `json:"companyPrefix,omitempty" db:"company_prefix"`
	Country       *string   `json:"country,omitempty" db:"country"`
	CreatedAt     time.Time `json:"createdAt" db:"created_at"`
}

// Site is a physical location identified by a GLN.
type Site struct {
	ID        uuid.UUID      `json:"id" db:"id"`
	OrgID     uuid.UUID      `json:"orgId" db:"org_id"`
	GLN       string         `json:"gln" db:"gln"`
	Name      string         `json:"name" db:"name"`
	Kind      string         `json:"kind" db:"kind"`
	Country   *string        `json:"country,omitempty" db:"country"`
	Address   map[string]any `json:"address" db:"address"`
	CreatedAt time.Time      `json:"createdAt" db:"created_at"`
}

// License is a regulatory license held by an organization.
type License struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	OrgID     uuid.UUID  `json:"orgId" db:"org_id"`
	SiteID    *uuid.UUID `json:"siteId,omitempty" db:"site_id"`
	Kind      string     `json:"kind" db:"kind"`
	Number    string     `json:"number" db:"number"`
	Authority string     `json:"authority" db:"authority"`
	Country   *string    `json:"country,omitempty" db:"country"`
	ValidFrom *time.Time `json:"validFrom,omitempty" db:"valid_from"`
	ValidTo   *time.Time `json:"validTo,omitempty" db:"valid_to"`
	CreatedAt time.Time  `json:"createdAt" db:"created_at"`
}

// APIKey is a machine credential (secret shown once).
type APIKey struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Name       string     `json:"name" db:"name"`
	Prefix     string     `json:"prefix" db:"prefix"`
	Roles      []string   `json:"roles" db:"roles"`
	CreatedBy  string     `json:"createdBy" db:"created_by"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty" db:"expires_at"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty" db:"revoked_at"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty" db:"last_used_at"`
	CreatedAt  time.Time  `json:"createdAt" db:"created_at"`
	Secret     string     `json:"secret,omitempty" db:"-"`
}

// Signature is an electronic signature record.
type Signature struct {
	ID       uuid.UUID `json:"id" db:"id"`
	UserID   uuid.UUID `json:"userId" db:"user_id"`
	Email    string    `json:"email" db:"email"`
	Meaning  string    `json:"meaning" db:"meaning"`
	Reason   string    `json:"reason" db:"reason"`
	Entity   string    `json:"entity" db:"entity"`
	EntityID string    `json:"entityId" db:"entity_id"`
	AuditSeq int64     `json:"auditSeq" db:"audit_seq"`
	SignedAt time.Time `json:"signedAt" db:"signed_at"`
}

type userRow struct {
	ID        uuid.UUID `db:"id"`
	Email     string    `db:"email"`
	Name      string    `db:"name"`
	Roles     []string  `db:"roles"`
	Status    string    `db:"status"`
	Locale    string    `db:"locale"`
	CreatedAt time.Time `db:"created_at"`
}

func (u userRow) user() User {
	return User(u)
}

// Public mounts unauthenticated routes.
func (m *Module) Public(r chi.Router) {
	r.Post("/auth/login", httpx.Handler(m.login))
	r.Post("/auth/password/forgot", httpx.Handler(m.forgotPassword))
	r.Post("/auth/password/reset", httpx.Handler(m.resetPassword))
}

// Routes mounts tenant routes.
func (m *Module) Routes(r chi.Router) {
	r.Get("/me", httpx.Handler(m.me))
	r.Get("/me/tenants", httpx.Handler(m.myTenants))
	r.Post("/auth/switch", httpx.Handler(m.switchTenant))
	r.Get("/tenant", httpx.Handler(m.getTenant))
	m.accountRoutes(r)

	r.Route("/orgs", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin))
		r.Get("/", httpx.Handler(m.listOrgs))
		r.Post("/", httpx.Handler(m.createOrg))
		r.Put("/{id}", httpx.Handler(m.updateOrg))
	})
	r.Route("/sites", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin))
		r.Get("/", httpx.Handler(m.listSites))
		r.Post("/", httpx.Handler(m.createSite))
		r.Put("/{id}", httpx.Handler(m.updateSite))
	})
	r.Route("/licenses", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleCompliance))
		r.Get("/", httpx.Handler(m.listLicenses))
		r.Post("/", httpx.Handler(m.createLicense))
	})
	r.Route("/users", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin))
		r.Get("/", httpx.Handler(m.listUsers))
		r.Post("/", httpx.Handler(m.createUser))
		r.Patch("/{id}", httpx.Handler(m.updateUser))
		r.Post("/{id}/mfa/reset", httpx.Handler(m.resetUserMFA))
	})
	r.Route("/api-keys", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleIntegration))
		r.Get("/", httpx.Handler(m.listKeys))
		r.Post("/", httpx.Handler(m.createKey))
		r.Delete("/{id}", httpx.Handler(m.revokeKey))
	})
	r.Get("/audit", httpx.Handler(m.listAudit))
	r.Get("/audit/verify", httpx.Handler(m.verifyAudit))
	r.Post("/esignatures", httpx.Handler(m.sign))
	r.Get("/esignatures", httpx.Handler(m.listSignatures))
	r.Get("/alerts", httpx.Handler(m.listAlerts))
	// Any role except Viewer (read-only) may resolve alerts.
	r.With(auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleCompliance, auth.RoleOps, auth.RoleIntegration)).
		Post("/alerts/{id}/resolve", httpx.Handler(m.resolveAlert))
}

type tokenResponse struct {
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Principal auth.Principal `json:"principal"`
}

func (m *Module) issue(w http.ResponseWriter, p auth.Principal) error {
	tok, exp, err := m.Auth.Issue(p)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, tokenResponse{Token: tok, ExpiresAt: exp, Principal: p})
	return nil
}

func (m *Module) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Tenant   string `json:"tenant"`
		Email    string `json:"email"`
		Password string `json:"password"`
		MFACode  string `json:"mfaCode"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Tenant == "" && m.HostTenant != nil {
		if slug, ok := m.HostTenant(r.Context(), r.Host); ok {
			in.Tenant = slug
		}
	}
	p, err := m.LoginMFA(WithClientIP(r.Context(), ClientIP(r, m.Cfg.TrustedProxies)), in.Tenant, in.Email, in.Password, in.MFACode)
	if err != nil {
		if secs, ok := RetryAfter(err); ok {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		return err
	}
	return m.issue(w, p)
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, http.StatusOK, auth.MustFrom(r.Context()))
	return nil
}

func (m *Module) myTenants(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFrom(r.Context())
	if p.Kind != "user" {
		return httpx.Forbidden("users only")
	}
	ts, err := m.MembershipsFor(r.Context(), p.Email)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, ts)
	return nil
}

func (m *Module) switchTenant(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		TenantID uuid.UUID `json:"tenantId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	cur := auth.MustFrom(r.Context())
	if cur.Kind != "user" {
		return httpx.Forbidden("users only")
	}
	p, err := m.PrincipalForSession(r.Context(), in.TenantID, cur.Email, cur.MFA)
	if err != nil {
		return err
	}
	return m.issue(w, p)
}

func (m *Module) getTenant(w http.ResponseWriter, r *http.Request) error {
	var t Tenant
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `select id, slug, name, plan, status, region, verticals, brand, custom_domain, created_at from tenant where id=app_tenant()`).
			Scan(&t.ID, &t.Slug, &t.Name, &t.Plan, &t.Status, &t.Region, &t.Verticals, &t.Brand, &t.CustomDomain, &t.CreatedAt)
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, t)
	return nil
}

// --- organizations ---

func (m *Module) listOrgs(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Organization](r.Context(), tx, `select id, name, kind, company_prefix, country, created_at from organization order by name`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Organization]{Items: items, Total: len(items)})
		}
		return err
	})
}

type orgInput struct {
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	CompanyPrefix *string `json:"companyPrefix"`
	Country       *string `json:"country"`
}

var orgKinds = map[string]bool{"manufacturer": true, "cmo": true, "3pl": true, "wholesaler": true, "pharmacy": true, "hospital": true, "regulator": true,
	"grower": true, "processor": true, "distributor": true, "retailer": true, "restaurant": true, "importer": true}

func (in orgInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("name is required")
	}
	if !orgKinds[in.Kind] {
		return httpx.BadRequest("unknown organization kind")
	}
	return nil
}

func (m *Module) createOrg(w http.ResponseWriter, r *http.Request) error {
	var in orgInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		o, err := db.One[Organization](r.Context(), tx, `insert into organization (id, tenant_id, name, kind, company_prefix, country) values ($1, app_tenant(), $2,$3,$4,$5)
			returning id, name, kind, company_prefix, country, created_at`, ids.New(), in.Name, in.Kind, in.CompanyPrefix, in.Country)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "org.create", Entity: "organization", EntityID: o.ID.String(), After: o}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, o)
		return nil
	})
}

func (m *Module) updateOrg(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in orgInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[Organization](r.Context(), tx, `select id, name, kind, company_prefix, country, created_at from organization where id=$1`, id)
		if err != nil {
			return err
		}
		o, err := db.One[Organization](r.Context(), tx, `update organization set name=$2, kind=$3, company_prefix=$4, country=$5 where id=$1
			returning id, name, kind, company_prefix, country, created_at`, id, in.Name, in.Kind, in.CompanyPrefix, in.Country)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "org.update", Entity: "organization", EntityID: id.String(), Before: before, After: o}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, o)
		return nil
	})
}

// --- sites ---

const siteCols = `id, org_id, gln, name, kind, country, address, created_at`

type siteInput struct {
	OrgID   uuid.UUID      `json:"orgId"`
	GLN     string         `json:"gln"`
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Country *string        `json:"country"`
	Address map[string]any `json:"address"`
}

func (in *siteInput) validate() error {
	if !gs1.ValidGLN(in.GLN) {
		return httpx.BadRequest("gln must be 13 digits with a valid check digit")
	}
	if in.Name == "" {
		return httpx.BadRequest("name is required")
	}
	if in.Kind == "" {
		in.Kind = "plant"
	}
	if in.Address == nil {
		in.Address = map[string]any{}
	}
	return nil
}

func (m *Module) listSites(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Site](r.Context(), tx, `select `+siteCols+` from site order by name`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Site]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createSite(w http.ResponseWriter, r *http.Request) error {
	var in siteInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := db.One[Site](r.Context(), tx, `insert into site (id, tenant_id, org_id, gln, name, kind, country, address) values ($1, app_tenant(), $2,$3,$4,$5,$6,$7)
			returning `+siteCols, ids.New(), in.OrgID, in.GLN, in.Name, in.Kind, in.Country, in.Address)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "site.create", Entity: "site", EntityID: s.ID.String(), After: s}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, s)
		return nil
	})
}

func (m *Module) updateSite(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in siteInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[Site](r.Context(), tx, `select `+siteCols+` from site where id=$1`, id)
		if err != nil {
			return err
		}
		s, err := db.One[Site](r.Context(), tx, `update site set org_id=$2, gln=$3, name=$4, kind=$5, country=$6, address=$7 where id=$1 returning `+siteCols,
			id, in.OrgID, in.GLN, in.Name, in.Kind, in.Country, in.Address)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "site.update", Entity: "site", EntityID: id.String(), Before: before, After: s}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

// --- licenses ---

const licenseCols = `id, org_id, site_id, kind, number, authority, country, valid_from, valid_to, created_at`

func (m *Module) listLicenses(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[License](r.Context(), tx, `select `+licenseCols+` from license order by valid_to nulls last`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[License]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createLicense(w http.ResponseWriter, r *http.Request) error {
	var in License
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Kind == "" || in.Number == "" || in.Authority == "" {
		return httpx.BadRequest("kind, number and authority are required")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		l, err := db.One[License](r.Context(), tx, `insert into license (id, tenant_id, org_id, site_id, kind, number, authority, country, valid_from, valid_to)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9) returning `+licenseCols,
			ids.New(), in.OrgID, in.SiteID, in.Kind, in.Number, in.Authority, in.Country, in.ValidFrom, in.ValidTo)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "license.create", Entity: "license", EntityID: l.ID.String(), After: l}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, l)
		return nil
	})
}

// --- users ---

const userCols = `id, email, name, roles, status, locale, created_at`

func (m *Module) listUsers(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := db.List[userRow](r.Context(), tx, `select `+userCols+` from app_user order by email`)
		if err != nil {
			return err
		}
		items := make([]User, len(rows))
		for i, u := range rows {
			items[i] = u.user()
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[User]{Items: items, Total: len(items)})
		return nil
	})
}

func (m *Module) createUser(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string   `json:"email"`
		Name     string   `json:"name"`
		Roles    []string `json:"roles"`
		Password string   `json:"password"`
		Locale   string   `json:"locale"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !strings.Contains(in.Email, "@") {
		return httpx.BadRequest("valid email required")
	}
	if err := validRoles(in.Roles); err != nil {
		return err
	}
	var hash *string
	if in.Password != "" {
		if err := ValidatePassword(in.Password, in.Email); err != nil {
			return err
		}
		b, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		s := string(b)
		hash = &s
	}
	if in.Locale == "" {
		in.Locale = "en"
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		u, err := db.One[userRow](r.Context(), tx, `insert into app_user (id, tenant_id, email, name, roles, password_hash, locale) values ($1, app_tenant(), $2,$3,$4,$5,$6)
			returning `+userCols, ids.New(), strings.ToLower(in.Email), in.Name, in.Roles, hash, in.Locale)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "user.create", Entity: "user", EntityID: u.ID.String(), After: u.user()}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, u.user())
		return nil
	})
}

func (m *Module) updateUser(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Name   *string  `json:"name"`
		Roles  []string `json:"roles"`
		Status *string  `json:"status"`
		Locale *string  `json:"locale"`
		Reason string   `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Roles != nil {
		if err := validRoles(in.Roles); err != nil {
			return err
		}
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "disabled" {
		return httpx.BadRequest("status must be active or disabled")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[userRow](r.Context(), tx, `select `+userCols+` from app_user where id=$1`, id)
		if err != nil {
			return err
		}
		// A change of roles or status ends the user's existing sessions: tokens carry the roles they were issued with.
		bump := 0
		if (in.Roles != nil && !slices.Equal(in.Roles, before.Roles)) || (in.Status != nil && *in.Status != before.Status) {
			bump = 1
		}
		u, err := db.One[userRow](r.Context(), tx, `update app_user set name=coalesce($2,name), roles=coalesce($3,roles), status=coalesce($4,status), locale=coalesce($5,locale), token_version=token_version+$6
			where id=$1 returning `+userCols, id, in.Name, in.Roles, in.Status, in.Locale, bump)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "user.update", Entity: "user", EntityID: id.String(), Before: before.user(), After: u.user(), Reason: in.Reason}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, u.user())
		return nil
	})
}

// --- API keys ---

const keyCols = `id, name, prefix, roles, created_by, expires_at, revoked_at, last_used_at, created_at`

func (m *Module) listKeys(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[APIKey](r.Context(), tx, `select `+keyCols+` from api_key order by created_at desc`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[APIKey]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createKey(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name      string     `json:"name"`
		Roles     []string   `json:"roles"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Name == "" {
		return httpx.BadRequest("name is required")
	}
	if err := validRoles(in.Roles); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	for _, role := range in.Roles {
		if !p.Has(role) {
			return httpx.Forbidden("cannot grant a role you do not hold: " + role)
		}
	}
	secret := "osk_" + ids.Secret(32)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		k, err := db.One[APIKey](r.Context(), tx, `insert into api_key (id, tenant_id, name, prefix, hash, roles, created_by, expires_at) values ($1, app_tenant(), $2,$3,$4,$5,$6,$7)
			returning `+keyCols, ids.New(), in.Name, secret[:10], auth.HashKey(secret), in.Roles, p.Actor(), in.ExpiresAt)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "apikey.create", Entity: "api_key", EntityID: k.ID.String(), After: k}); err != nil {
			return err
		}
		k.Secret = secret
		httpx.JSON(w, http.StatusCreated, k)
		return nil
	})
}

func (m *Module) revokeKey(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		k, err := db.One[APIKey](r.Context(), tx, `update api_key set revoked_at=now() where id=$1 and revoked_at is null returning `+keyCols, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "apikey.revoke", Entity: "api_key", EntityID: id.String(), After: k}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// --- audit + e-signatures ---

type auditRow struct {
	ID       int64     `json:"id" db:"id"`
	Seq      int64     `json:"seq" db:"seq"`
	At       time.Time `json:"at" db:"at"`
	Actor    string    `json:"actor" db:"actor"`
	Action   string    `json:"action" db:"action"`
	Entity   string    `json:"entity" db:"entity"`
	EntityID string    `json:"entityId" db:"entity_id"`
	Before   any       `json:"before,omitempty" db:"before"`
	After    any       `json:"after,omitempty" db:"after"`
	Reason   string    `json:"reason,omitempty" db:"reason"`
	Hash     string    `json:"hash" db:"hash"`
}

func (m *Module) listAudit(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := httpx.IntQuery(r, "limit", 50, 1, 500)
	offset := httpx.IntQuery(r, "offset", 0, 0, 1<<30)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		where := `($1 = '' or entity = $1) and ($2 = '' or entity_id = $2) and ($3 = '' or action like $3 || '%')`
		total, err := db.Count(r.Context(), tx, `select count(*) from audit_entry where `+where, q.Get("entity"), q.Get("entityId"), q.Get("action"))
		if err != nil {
			return err
		}
		items, err := db.List[auditRow](r.Context(), tx, `select id, seq, at, actor, action, entity, entity_id, before, after, reason, hash from audit_entry where `+where+`
			order by seq desc limit $4 offset $5`, q.Get("entity"), q.Get("entityId"), q.Get("action"), limit, offset)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[auditRow]{Items: items, Total: total})
		}
		return err
	})
}

func (m *Module) verifyAudit(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		n, broken, err := audit.Verify(r.Context(), tx, auth.MustFrom(r.Context()).TenantID)
		if err == nil {
			httpx.JSON(w, http.StatusOK, map[string]any{"checked": n, "intact": broken == 0, "brokenAtSeq": broken})
		}
		return err
	})
}

// sign records a 21 CFR Part 11-style signature: the user re-authenticates with
// their password and states a meaning and reason; the signature is bound to the audit chain.
func (m *Module) sign(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
		Meaning  string `json:"meaning"`
		Reason   string `json:"reason"`
		Entity   string `json:"entity"`
		EntityID string `json:"entityId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Meaning == "" || in.Reason == "" || in.Entity == "" || in.EntityID == "" {
		return httpx.BadRequest("meaning, reason, entity and entityId are required")
	}
	p := auth.MustFrom(r.Context())
	if p.Kind != "user" {
		return httpx.Forbidden("only users can sign")
	}
	if _, err := m.checkPassword(r.Context(), p.TenantID, p.Email, in.Password); err != nil {
		return httpx.Unauthorized("re-authentication failed")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s := Signature{ID: ids.New(), UserID: p.UserID, Email: p.Email, Meaning: in.Meaning, Reason: in.Reason, Entity: in.Entity, EntityID: in.EntityID}
		e, err := audit.Write(r.Context(), tx, audit.Record{Action: "esignature.sign", Entity: in.Entity, EntityID: in.EntityID, After: map[string]string{"meaning": in.Meaning, "signer": p.Email}, Reason: in.Reason})
		if err != nil {
			return err
		}
		s.AuditSeq = e.Seq
		s, err = db.One[Signature](r.Context(), tx, `insert into esignature (id, tenant_id, user_id, email, meaning, reason, entity, entity_id, audit_seq)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8) returning id, user_id, email, meaning, reason, entity, entity_id, audit_seq, signed_at`,
			s.ID, s.UserID, s.Email, s.Meaning, s.Reason, s.Entity, s.EntityID, s.AuditSeq)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, s)
		}
		return err
	})
}

func (m *Module) listSignatures(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Signature](r.Context(), tx, `select id, user_id, email, meaning, reason, entity, entity_id, audit_seq, signed_at from esignature
			where ($1='' or entity=$1) and ($2='' or entity_id=$2) order by signed_at desc limit 200`, q.Get("entity"), q.Get("entityId"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Signature]{Items: items, Total: len(items)})
		}
		return err
	})
}

// --- alerts (shared inbox for serialization, regulatory and agent alerts) ---

type alertRow struct {
	ID         uuid.UUID      `json:"id" db:"id"`
	Kind       string         `json:"kind" db:"kind"`
	Severity   string         `json:"severity" db:"severity"`
	Title      string         `json:"title" db:"title"`
	Detail     map[string]any `json:"detail" db:"detail"`
	Status     string         `json:"status" db:"status"`
	Entity     string         `json:"entity" db:"entity"`
	EntityID   string         `json:"entityId" db:"entity_id"`
	CreatedAt  time.Time      `json:"createdAt" db:"created_at"`
	ResolvedAt *time.Time     `json:"resolvedAt,omitempty" db:"resolved_at"`
}

func (m *Module) listAlerts(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	status := q.Get("status")
	if status == "" {
		status = "open"
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[alertRow](r.Context(), tx, `select id, kind, severity, title, detail, status, entity, entity_id, created_at, resolved_at from alert
			where ($1='all' or status=$1) and ($2='' or kind like $2 || '%') order by created_at desc limit 500`, status, q.Get("kind"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[alertRow]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) resolveAlert(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		a, err := db.One[alertRow](r.Context(), tx, `update alert set status='resolved', resolved_at=now() where id=$1
			returning id, kind, severity, title, detail, status, entity, entity_id, created_at, resolved_at`, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "alert.resolve", Entity: "alert", EntityID: id.String(), After: a}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, a)
		return nil
	})
}
