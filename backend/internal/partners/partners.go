// Package partners owns the partner directory, invitations, connection approval,
// exchange of endpoints and master data, and partner health.
package partners

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/time/rate"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/pkg/as2"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Module is the partners bounded context.
type Module struct {
	kernel.Deps
	Client   *http.Client
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// New creates the module.
func New(d kernel.Deps) *Module {
	return &Module{Deps: d, Client: &http.Client{Timeout: 5 * time.Second}}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "partners" }

// Endpoints are a partner's integration endpoints.
type Endpoints struct {
	EPCISCapture string `json:"epcisCapture,omitempty"`
	VRS          string `json:"vrs,omitempty"`
	AS2URL       string `json:"as2Url,omitempty"`
	AS2ID        string `json:"as2Id,omitempty"`
	Health       string `json:"health,omitempty"`
	Federation   string `json:"federation,omitempty"`
}

// Partner is a trading partner connection.
type Partner struct {
	ID            uuid.UUID      `json:"id" db:"id"`
	Name          string         `json:"name" db:"name"`
	GLN           *string        `json:"gln,omitempty" db:"gln"`
	CompanyPrefix *string        `json:"companyPrefix,omitempty" db:"company_prefix"`
	Country       *string        `json:"country,omitempty" db:"country"`
	Kind          string         `json:"kind" db:"kind"`
	Status        string         `json:"status" db:"status"`
	LinkedTenant  *uuid.UUID     `json:"linkedTenant,omitempty" db:"linked_tenant"`
	Endpoints     Endpoints      `json:"endpoints" db:"endpoints"`
	MasterData    map[string]any `json:"masterData" db:"master_data"`
	Health        map[string]any `json:"health" db:"health"`
	CreatedAt     time.Time      `json:"createdAt" db:"created_at"`
	ApprovedAt    *time.Time     `json:"approvedAt,omitempty" db:"approved_at"`
}

const partnerCols = `id, name, gln, company_prefix, country, kind, status, linked_tenant, endpoints, master_data, health, created_at, approved_at`

// Listing is a directory entry: the tenant's opt-in public profile.
type Listing struct {
	TenantID         uuid.UUID        `json:"tenantId" db:"tenant_id"`
	Name             string           `json:"name" db:"name"`
	GLN              *string          `json:"gln,omitempty" db:"gln"`
	GLNs             []string         `json:"glns" db:"glns"`
	CompanyPrefixes  []string         `json:"companyPrefixes" db:"company_prefixes"`
	Country          *string          `json:"country,omitempty" db:"country"`
	Kind             string           `json:"kind" db:"kind"`
	Description      string           `json:"description" db:"description"`
	Website          string           `json:"website" db:"website"`
	Contact          string           `json:"contact" db:"contact"`
	Licenses         []ProfileLicense `json:"licenses" db:"licenses"`
	Endpoints        Endpoints        `json:"endpoints" db:"endpoints"`
	Published        bool             `json:"published" db:"published"`
	Verified         bool             `json:"verified" db:"verified"`
	VerifiedAt       *time.Time       `json:"verifiedAt,omitempty" db:"verified_at"`
	VerifiedBy       string           `json:"verifiedBy,omitempty" db:"verified_by"`
	VerificationNote string           `json:"verificationNote,omitempty" db:"verification_note"`
	UpdatedAt        time.Time        `json:"updatedAt" db:"updated_at"`
}

const listingCols = `tenant_id, name, gln, glns, company_prefixes, country, kind, description, website, contact, licenses, endpoints, published,
	verified, verified_at, verified_by, verification_note, updated_at`

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Route("/partners", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleIntegration, auth.RoleCompliance))
		r.Get("/", httpx.Handler(m.list))
		r.Post("/", httpx.Handler(m.create))
		r.Get("/directory", httpx.Handler(m.searchDirectory))
		r.Get("/directory/requests", httpx.Handler(m.listConnectionRequests))
		r.Post("/directory/{tenantId}/connect", httpx.Handler(m.requestConnection))
		r.Get("/onboarding/templates", httpx.Handler(m.listTemplates))
		r.Get("/listing", httpx.Handler(m.getListing))
		r.Put("/listing", httpx.Handler(m.putListing))
		r.Post("/invitations", httpx.Handler(m.invite))
		r.Get("/invitations", httpx.Handler(m.listInvitations))
		r.Post("/invitations/accept", httpx.Handler(m.accept))
		r.Get("/{id}", httpx.Handler(m.get))
		r.Put("/{id}", httpx.Handler(m.update))
		r.Post("/{id}/approve", httpx.Handler(m.approve))
		r.Post("/{id}/suspend", httpx.Handler(m.suspend))
		r.Post("/{id}/health", httpx.Handler(m.checkHealthNow))
		r.Get("/{id}/as2", httpx.Handler(m.getAS2))
		r.With(auth.Require(auth.RoleAdmin, auth.RoleIntegration)).Put("/{id}/as2", httpx.Handler(m.putAS2))
		r.With(auth.Require(auth.RoleAdmin, auth.RoleIntegration)).Delete("/{id}/as2", httpx.Handler(m.deleteAS2))
		r.Get("/{id}/onboarding", httpx.Handler(m.getOnboarding))
		r.Post("/{id}/onboarding", httpx.Handler(m.startOnboarding))
		r.Patch("/{id}/onboarding/items/{itemId}", httpx.Handler(m.updateItem))
	})
	r.With(auth.RequireOperator).Post("/directory/{tenantId}/verification", httpx.Handler(m.verifyProfile))
}

// Schedules implements kernel.Scheduled: partner health probes every 5 minutes.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: "partners.health", Every: 5 * time.Minute, Run: m.probeAll}}
}

type partnerInput struct {
	Name          string    `json:"name"`
	GLN           *string   `json:"gln"`
	CompanyPrefix *string   `json:"companyPrefix"`
	Country       *string   `json:"country"`
	Kind          string    `json:"kind"`
	Endpoints     Endpoints `json:"endpoints"`
}

func (in *partnerInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("name is required")
	}
	if in.GLN != nil && *in.GLN != "" && !gs1.ValidGLN(*in.GLN) {
		return httpx.BadRequest("invalid GLN")
	}
	if in.Kind == "" {
		in.Kind = "wholesaler"
	}
	if in.Endpoints.AS2ID != "" && !as2.ValidID(in.Endpoints.AS2ID) {
		return httpx.BadRequest("as2Id must be 1-128 characters of letters, digits and . _ : @ -")
	}
	for _, u := range []string{in.Endpoints.EPCISCapture, in.Endpoints.VRS, in.Endpoints.AS2URL, in.Endpoints.Health} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "local:") {
			return httpx.BadRequest("endpoints must be http(s) URLs")
		}
	}
	return nil
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Partner](r.Context(), tx, `select `+partnerCols+` from partner where ($1='' or status=$1) order by name`, r.URL.Query().Get("status"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Partner]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[Partner](r.Context(), tx, `select `+partnerCols+` from partner where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, p)
		}
		return err
	})
}

// Get returns a partner inside tx (used by DSCSA and Link).
func Get(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Partner, error) {
	return db.One[Partner](ctx, tx, `select `+partnerCols+` from partner where id=$1`, id)
}

// ByGLN returns a partner by GLN inside tx.
func ByGLN(ctx context.Context, tx pgx.Tx, gln string) (Partner, error) {
	return db.One[Partner](ctx, tx, `select `+partnerCols+` from partner where gln=$1`, gln)
}

// create adds a manually configured partner (e.g. one not on Open Spoor). It starts active.
func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	var in partnerInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[Partner](r.Context(), tx, `insert into partner (id, tenant_id, name, gln, company_prefix, country, kind, status, endpoints, approved_at)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,'active',$7, now()) returning `+partnerCols, ids.New(), in.Name, in.GLN, in.CompanyPrefix, in.Country, in.Kind, in.Endpoints)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.create", Entity: "partner", EntityID: p.ID.String(), After: p}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, p)
		return nil
	})
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in partnerInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := Get(r.Context(), tx, id)
		if err != nil {
			return err
		}
		p, err := db.One[Partner](r.Context(), tx, `update partner set name=$2, gln=$3, company_prefix=$4, country=$5, kind=$6, endpoints=$7 where id=$1 returning `+partnerCols,
			id, in.Name, in.GLN, in.CompanyPrefix, in.Country, in.Kind, in.Endpoints)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.update", Entity: "partner", EntityID: id.String(), Before: before, After: p}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

func (m *Module) setStatus(w http.ResponseWriter, r *http.Request, status, action string) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[Partner](r.Context(), tx, `update partner set status=$2, approved_at=case when $2='active' then now() else approved_at end where id=$1 returning `+partnerCols, id, status)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: action, Entity: "partner", EntityID: id.String(), After: p}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

func (m *Module) approve(w http.ResponseWriter, r *http.Request) error {
	return m.setStatus(w, r, "active", "partner.approve")
}

func (m *Module) suspend(w http.ResponseWriter, r *http.Request) error {
	return m.setStatus(w, r, "suspended", "partner.suspend")
}

// --- directory ---

func (m *Module) searchDirectory(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var items []Listing
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		items, err = db.List[Listing](r.Context(), tx, `select `+listingCols+` from directory_listing
			where published and ($1 = '' or name ilike '%'||$1||'%' or gln = $1 or $1 = any(glns) or $1 = any(company_prefixes) or country = upper($1)) order by name limit 100`, q)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Listing]{Items: items, Total: len(items)})
	return nil
}

func (m *Module) getListing(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		l, err := db.One[Listing](r.Context(), tx, `select `+listingCols+` from directory_listing`)
		if errors.Is(err, db.ErrNotFound) {
			httpx.JSON(w, http.StatusOK, Listing{TenantID: kernel.TenantOf(r.Context()), GLNs: []string{}, CompanyPrefixes: []string{}, Licenses: []ProfileLicense{}})
			return nil
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, l)
		}
		return err
	})
}

// --- invitations: tenant A invites; tenant B accepts the code; A approves; endpoints are exchanged ---

type invitation struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Code      string    `json:"code" db:"code"`
	Email     string    `json:"email" db:"email"`
	Name      string    `json:"name" db:"name"`
	Status    string    `json:"status" db:"status"`
	ExpiresAt time.Time `json:"expiresAt" db:"expires_at"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

func (m *Module) invite(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !strings.Contains(in.Email, "@") || in.Name == "" {
		return httpx.BadRequest("email and name are required")
	}
	code := "OSP-" + strings.ToUpper(ids.Secret(9))
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		inv, err := db.One[invitation](r.Context(), tx, `insert into partner_invitation (id, tenant_id, code, email, name, expires_at) values ($1, app_tenant(), $2,$3,$4, now() + interval '14 days')
			returning id, code, email, name, status, expires_at, created_at`, ids.New(), code, strings.ToLower(in.Email), in.Name)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.invite", Entity: "partner_invitation", EntityID: inv.ID.String(), After: map[string]any{"email": inv.Email, "name": inv.Name}}); err != nil {
			return err
		}
		// TODO: e-mail delivery is an adapter (SMTP) configured per install; the code is shown to the inviter.
		httpx.JSON(w, http.StatusCreated, inv)
		return nil
	})
}

func (m *Module) listInvitations(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[invitation](r.Context(), tx, `select id, code, email, name, status, expires_at, created_at from partner_invitation order by created_at desc`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[invitation]{Items: items, Total: len(items)})
		}
		return err
	})
}

type tenantInfo struct {
	ID       uuid.UUID
	Name     string
	Listing  *Listing
	Products []map[string]any
	GLNs     []string
}

func loadInfo(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (tenantInfo, error) {
	ti := tenantInfo{ID: tenant}
	if err := tx.QueryRow(ctx, `select name from tenant where id=$1`, tenant).Scan(&ti.Name); err != nil {
		return ti, err
	}
	if l, err := db.One[Listing](ctx, tx, `select `+listingCols+` from directory_listing where tenant_id=$1`, tenant); err == nil {
		ti.Listing = &l
	}
	rows, err := tx.Query(ctx, `select gtin, name, coalesce(ndc,'') from product where tenant_id=$1 and status='active' order by gtin limit 500`, tenant)
	if err != nil {
		return ti, err
	}
	for rows.Next() {
		var g, n, ndc string
		if err := rows.Scan(&g, &n, &ndc); err != nil {
			rows.Close()
			return ti, err
		}
		ti.Products = append(ti.Products, map[string]any{"gtin": g, "name": n, "ndc": ndc})
	}
	rows.Close()
	grows, err := tx.Query(ctx, `select gln from site where tenant_id=$1 order by gln`, tenant)
	if err != nil {
		return ti, err
	}
	for grows.Next() {
		var g string
		if err := grows.Scan(&g); err != nil {
			grows.Close()
			return ti, err
		}
		ti.GLNs = append(ti.GLNs, g)
	}
	grows.Close()
	return ti, nil
}

func (ti tenantInfo) partnerFields() (name string, gln, country *string, kind string, ep Endpoints, md map[string]any) {
	name, kind = ti.Name, "wholesaler"
	ep = Endpoints{EPCISCapture: "local:" + ti.ID.String(), VRS: "local:" + ti.ID.String()}
	if ti.Listing != nil {
		name, gln, country, kind = ti.Listing.Name, ti.Listing.GLN, ti.Listing.Country, ti.Listing.Kind
		if ti.Listing.Endpoints.EPCISCapture != "" {
			ep = ti.Listing.Endpoints
		}
	}
	md = map[string]any{"products": ti.Products, "glns": ti.GLNs}
	return
}

// accept is called by the invited tenant. Both sides get a partner record; the inviter must approve.
func (m *Module) accept(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	me := auth.MustFrom(r.Context())
	var mine Partner
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var invID, inviter uuid.UUID
		var status string
		var exp time.Time
		err := tx.QueryRow(r.Context(), `select id, tenant_id, status, expires_at from partner_invitation where code=$1 for update`, strings.TrimSpace(in.Code)).Scan(&invID, &inviter, &status, &exp)
		if err != nil {
			return httpx.NotFound("invitation not found")
		}
		if status != "open" || time.Now().After(exp) {
			return httpx.Conflict("invitation is no longer valid")
		}
		if inviter == me.TenantID {
			return httpx.BadRequest("cannot accept your own invitation")
		}
		a, err := loadInfo(r.Context(), tx, inviter)
		if err != nil {
			return err
		}
		b, err := loadInfo(r.Context(), tx, me.TenantID)
		if err != nil {
			return err
		}
		// Invitee's view of the inviter: active (they chose to accept).
		n, gln, ctry, kind, ep, md := a.partnerFields()
		mine, err = db.One[Partner](r.Context(), tx, `insert into partner (id, tenant_id, name, gln, country, kind, status, linked_tenant, endpoints, master_data, approved_at)
			values ($1,$2,$3,$4,$5,$6,'active',$7,$8,$9, now()) returning `+partnerCols, ids.New(), me.TenantID, n, gln, ctry, kind, inviter, ep, md)
		if err != nil {
			return err
		}
		// Inviter's view of the invitee: pending approval.
		n, gln, ctry, kind, ep, md = b.partnerFields()
		theirs := ids.New()
		if _, err := tx.Exec(r.Context(), `insert into partner (id, tenant_id, name, gln, country, kind, status, linked_tenant, endpoints, master_data)
			values ($1,$2,$3,$4,$5,$6,'pending',$7,$8,$9)`, theirs, inviter, n, gln, ctry, kind, me.TenantID, ep, md); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `update partner_invitation set status='accepted', accepted_by=$2 where id=$1`, invID, me.TenantID); err != nil {
			return err
		}
		if _, err := audit.WriteAs(r.Context(), tx, me.TenantID, me.AuditActor(), audit.Record{Action: "partner.accept", Entity: "partner", EntityID: mine.ID.String(), After: mine}); err != nil {
			return err
		}
		_, err = audit.WriteAs(r.Context(), tx, inviter, me.AuditActor(), audit.Record{Action: "partner.invitation_accepted", Entity: "partner", EntityID: theirs.String(),
			After: map[string]any{"by": b.Name}})
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, mine)
	return nil
}

// --- health ---

func (m *Module) checkHealthNow(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var p Partner
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err = Get(r.Context(), tx, id)
		return err
	})
	if err != nil {
		return err
	}
	h := m.probe(r.Context(), p)
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `update partner set health=$2 where id=$1`, id, h)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, h)
	return nil
}

func (m *Module) probe(ctx context.Context, p Partner) map[string]any {
	h := map[string]any{"checkedAt": time.Now().UTC()}
	target := p.Endpoints.Health
	if target == "" {
		target = p.Endpoints.EPCISCapture
	}
	switch {
	case target == "":
		h["status"], h["detail"] = "unknown", "no endpoint configured"
	case strings.HasPrefix(target, "local:"):
		h["status"], h["latencyMs"] = "up", 0
	default:
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		res, err := m.Client.Do(req)
		h["latencyMs"] = time.Since(start).Milliseconds()
		if err != nil {
			h["status"], h["detail"] = "down", err.Error()
		} else {
			_ = res.Body.Close()
			if res.StatusCode >= 500 {
				h["status"], h["detail"] = "down", res.Status
			} else {
				h["status"] = "up"
			}
		}
	}
	return h
}

func (m *Module) probeAll(ctx context.Context) error {
	type row struct {
		Tenant uuid.UUID
		P      Partner
	}
	var rows []row
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `select tenant_id, `+partnerCols+` from partner where status='active'`)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var x row
			p := &x.P
			if err := r.Scan(&x.Tenant, &p.ID, &p.Name, &p.GLN, &p.CompanyPrefix, &p.Country, &p.Kind, &p.Status, &p.LinkedTenant, &p.Endpoints, &p.MasterData, &p.Health, &p.CreatedAt, &p.ApprovedAt); err != nil {
				return err
			}
			rows = append(rows, x)
		}
		return r.Err()
	})
	if err != nil {
		return err
	}
	for _, x := range rows {
		h := m.probe(ctx, x.P)
		_ = m.DB.Tx(ctx, x.Tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update partner set health=$2 where id=$1`, x.P.ID, h)
			return err
		})
	}
	return nil
}
