// Package platform owns tenancy, organizations, sites (GLN), licenses, users, roles,
// API keys, the audit log and 21 CFR Part 11-style electronic signatures.
package platform

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
)

// Module is the platform bounded context.
type Module struct {
	kernel.Deps
	// HostTenant resolves a tenant slug from the request host (SaaS subdomains, custom domains).
	HostTenant func(ctx context.Context, host string) (string, bool)
}

// New creates the module.
func New(d kernel.Deps) *Module { return &Module{Deps: d} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "platform" }

// Tenant is a customer workspace.
type Tenant struct {
	ID           uuid.UUID      `json:"id"`
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	Plan         string         `json:"plan"`
	Status       string         `json:"status"`
	Region       string         `json:"region"`
	Verticals    []string       `json:"verticals"`
	Brand        map[string]any `json:"brand"`
	CustomDomain *string        `json:"customDomain,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
}

// User is a tenant user.
type User struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Roles     []string  `json:"roles"`
	Status    string    `json:"status"`
	Locale    string    `json:"locale"`
	CreatedAt time.Time `json:"createdAt"`
}

// NewTenant is input for CreateTenant.
type NewTenant struct {
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	Plan          string   `json:"plan"`
	Region        string   `json:"region"`
	Verticals     []string `json:"verticals"`
	AdminEmail    string   `json:"adminEmail"`
	AdminName     string   `json:"adminName"`
	AdminPassword string   `json:"adminPassword"`
	Operator      bool     `json:"-"`
}

// CreateTenant provisions a tenant with its first admin user. It is used by
// self-hosted bootstrap, SaaS sign-up and the demo seed.
func (m *Module) CreateTenant(ctx context.Context, in NewTenant) (Tenant, User, error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if in.Name == "" || in.Slug == "" || in.AdminEmail == "" {
		return Tenant{}, User{}, httpx.BadRequest("name, slug and adminEmail are required")
	}
	if len(in.AdminPassword) < 10 {
		return Tenant{}, User{}, httpx.BadRequest("admin password must be at least 10 characters")
	}
	if in.Plan == "" {
		in.Plan = "community"
	}
	if in.Region == "" {
		in.Region = "eu"
	}
	if len(in.Verticals) == 0 {
		in.Verticals = []string{"pharma"}
	}
	t := Tenant{ID: ids.New(), Slug: in.Slug, Name: in.Name, Plan: in.Plan, Status: "active", Region: in.Region, Verticals: in.Verticals, Brand: map[string]any{}}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return t, User{}, err
	}
	u := User{ID: ids.New(), Email: strings.ToLower(in.AdminEmail), Name: in.AdminName, Roles: []string{auth.RoleAdmin}, Status: "active", Locale: "en"}
	err = m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `insert into tenant (id, slug, name, plan, region, verticals) values ($1,$2,$3,$4,$5,$6) returning created_at`,
			t.ID, t.Slug, t.Name, t.Plan, t.Region, t.Verticals).Scan(&t.CreatedAt); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into app_user (id, tenant_id, email, name, password_hash, roles, operator) values ($1,$2,$3,$4,$5,$6,$7) returning created_at`,
			u.ID, t.ID, u.Email, u.Name, string(hash), u.Roles, in.Operator).Scan(&u.CreatedAt); err != nil {
			return err
		}
		_, err := audit.WriteAs(ctx, tx, t.ID, "system", audit.Record{Action: "tenant.create", Entity: "tenant", EntityID: t.ID.String(), After: t})
		return err
	})
	if errors.Is(err, db.ErrConflict) {
		return t, u, httpx.Conflict("tenant slug already taken")
	}
	return t, u, err
}

// TenantBySlug finds a tenant by slug or custom domain.
func (m *Module) TenantBySlug(ctx context.Context, slugOrDomain string) (Tenant, error) {
	var t Tenant
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, slug, name, plan, status, region, verticals, brand, custom_domain, created_at from tenant
			where slug=$1 or custom_domain=$1`, strings.ToLower(slugOrDomain)).
			Scan(&t.ID, &t.Slug, &t.Name, &t.Plan, &t.Status, &t.Region, &t.Verticals, &t.Brand, &t.CustomDomain, &t.CreatedAt)
	})
	return t, err
}

// Login checks a local password and returns a principal.
func (m *Module) Login(ctx context.Context, tenantSlug, email, password string) (auth.Principal, error) {
	t, err := m.TenantBySlug(ctx, tenantSlug)
	if err != nil {
		return auth.Principal{}, httpx.Unauthorized("invalid credentials")
	}
	if t.Status != "active" {
		return auth.Principal{}, httpx.Forbidden("tenant is " + t.Status)
	}
	return m.checkPassword(ctx, t.ID, email, password)
}

func (m *Module) checkPassword(ctx context.Context, tenant uuid.UUID, email, password string) (auth.Principal, error) {
	var p auth.Principal
	var hash *string
	var status string
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, email, name, roles, password_hash, status, operator from app_user where email=$1`, strings.ToLower(email)).
			Scan(&p.UserID, &p.Email, &p.Name, &p.Roles, &hash, &status, &p.Operator)
	})
	if err != nil || hash == nil || status != "active" || bcrypt.CompareHashAndPassword([]byte(*hash), []byte(password)) != nil {
		return auth.Principal{}, httpx.Unauthorized("invalid credentials")
	}
	p.TenantID, p.Kind = tenant, "user"
	return p, nil
}

// ResolveAPIKey implements auth.APIKeyResolver.
func (m *Module) ResolveAPIKey(ctx context.Context, hash string) (auth.Principal, error) {
	var p auth.Principal
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update api_key set last_used_at=now() where hash=$1 and revoked_at is null and (expires_at is null or expires_at > now())
			returning id, tenant_id, roles`, hash).Scan(&p.KeyID, &p.TenantID, &p.Roles)
		return err
	})
	if err != nil {
		return p, errors.New("invalid api key")
	}
	p.Kind = "apikey"
	return p, nil
}

// MembershipsFor lists tenants where the email has an active user (for the tenant switcher).
func (m *Module) MembershipsFor(ctx context.Context, email string) ([]Tenant, error) {
	var out []Tenant
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select t.id, t.slug, t.name, t.plan, t.status, t.region, t.verticals, t.brand, t.custom_domain, t.created_at
			from tenant t join app_user u on u.tenant_id=t.id where u.email=$1 and u.status='active' and t.status='active' order by t.name`, strings.ToLower(email))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Tenant
			if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Plan, &t.Status, &t.Region, &t.Verticals, &t.Brand, &t.CustomDomain, &t.CreatedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// PrincipalFor builds a user principal in another tenant (tenant switch).
func (m *Module) PrincipalFor(ctx context.Context, tenant uuid.UUID, email string) (auth.Principal, error) {
	p := auth.Principal{TenantID: tenant, Kind: "user"}
	var status string
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, email, name, roles, status, operator from app_user where email=$1`, strings.ToLower(email)).
			Scan(&p.UserID, &p.Email, &p.Name, &p.Roles, &status, &p.Operator)
	})
	if err != nil || status != "active" {
		return p, httpx.Forbidden("no membership in that tenant")
	}
	return p, nil
}

func validRoles(roles []string) error {
	if len(roles) == 0 {
		return httpx.BadRequest("at least one role is required")
	}
	for _, r := range roles {
		if !slices.Contains(auth.AllRoles, r) {
			return httpx.BadRequest(fmt.Sprintf("unknown role %q", r))
		}
	}
	return nil
}
