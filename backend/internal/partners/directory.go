package partners

import (
	"errors"
	"net"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strings"
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
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Network directory: each tenant may publish a profile (names, GLNs, company prefixes,
// licenses, a contact for connection requests). Operators set verification marks. Published
// profiles are searchable by every tenant and, without integration endpoints, publicly.

// ProfileLicense is a license shown on a public profile, with its verification mark.
type ProfileLicense struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Number     string     `json:"number"`
	Authority  string     `json:"authority"`
	Country    string     `json:"country,omitempty"`
	ValidTo    string     `json:"validTo,omitempty"` // YYYY-MM-DD
	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
}

func (l ProfileLicense) sameFacts(o ProfileLicense) bool {
	return l.Kind == o.Kind && l.Number == o.Number && l.Authority == o.Authority && l.Country == o.Country && l.ValidTo == o.ValidTo
}

// PublicProfile is what anyone may read about a published tenant.
type PublicProfile struct {
	TenantID        uuid.UUID        `json:"tenantId" db:"tenant_id"`
	Name            string           `json:"name" db:"name"`
	GLN             *string          `json:"gln,omitempty" db:"gln"`
	GLNs            []string         `json:"glns" db:"glns"`
	CompanyPrefixes []string         `json:"companyPrefixes" db:"company_prefixes"`
	Country         *string          `json:"country,omitempty" db:"country"`
	Kind            string           `json:"kind" db:"kind"`
	Description     string           `json:"description" db:"description"`
	Website         string           `json:"website" db:"website"`
	Contact         string           `json:"contact" db:"contact"`
	Licenses        []ProfileLicense `json:"licenses" db:"licenses"`
	Verified        bool             `json:"verified" db:"verified"`
	VerifiedAt      *time.Time       `json:"verifiedAt,omitempty" db:"verified_at"`
	UpdatedAt       time.Time        `json:"updatedAt" db:"updated_at"`
}

const publicCols = `tenant_id, name, gln, glns, company_prefixes, country, kind, description, website, contact, licenses, verified, verified_at, updated_at`

var prefixRe = regexp.MustCompile(`^[0-9]{4,12}$`)

type listingInput struct {
	Name            string           `json:"name"`
	GLN             *string          `json:"gln"`
	GLNs            []string         `json:"glns"`
	CompanyPrefixes []string         `json:"companyPrefixes"`
	Country         *string          `json:"country"`
	Kind            string           `json:"kind"`
	Description     string           `json:"description"`
	Website         string           `json:"website"`
	Contact         string           `json:"contact"`
	Licenses        []ProfileLicense `json:"licenses"`
	Endpoints       Endpoints        `json:"endpoints"`
	Published       bool             `json:"published"`
}

func (in *listingInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.Kind == "" {
		return httpx.BadRequest("name and kind are required")
	}
	if in.GLN != nil && *in.GLN != "" && !gs1.ValidGLN(*in.GLN) {
		return httpx.BadRequest("invalid GLN")
	}
	if in.GLNs == nil {
		in.GLNs = []string{}
	}
	for _, g := range in.GLNs {
		if !gs1.ValidGLN(g) {
			return httpx.BadRequest("invalid GLN " + g)
		}
	}
	if in.CompanyPrefixes == nil {
		in.CompanyPrefixes = []string{}
	}
	for _, p := range in.CompanyPrefixes {
		if !prefixRe.MatchString(p) {
			return httpx.BadRequest("company prefixes are 4 to 12 digits")
		}
	}
	if in.Website != "" && !strings.HasPrefix(in.Website, "https://") && !strings.HasPrefix(in.Website, "http://") {
		return httpx.BadRequest("website must be an http(s) URL")
	}
	if c := in.Contact; c != "" {
		if _, err := mail.ParseAddress(c); err != nil && !strings.HasPrefix(c, "https://") {
			return httpx.BadRequest("contact must be an e-mail address or an https URL")
		}
	}
	if in.Licenses == nil {
		in.Licenses = []ProfileLicense{}
	}
	if len(in.Licenses) > 50 {
		return httpx.BadRequest("at most 50 licenses")
	}
	for i := range in.Licenses {
		l := &in.Licenses[i]
		if l.Kind == "" || l.Number == "" || l.Authority == "" {
			return httpx.BadRequest("each license needs kind, number and authority")
		}
		if l.ValidTo != "" {
			if _, err := time.Parse("2006-01-02", l.ValidTo); err != nil {
				return httpx.BadRequest("license validTo must be YYYY-MM-DD")
			}
		}
		if l.ID == "" {
			l.ID = ids.New().String()
		}
		// Verification marks are set by operators only.
		l.Verified, l.VerifiedAt = false, nil
	}
	return nil
}

func (m *Module) putListing(w http.ResponseWriter, r *http.Request) error {
	var in listingInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[Listing](r.Context(), tx, `select `+listingCols+` from directory_listing`)
		hadBefore := err == nil
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
		var prev any
		if hadBefore {
			prev = before
		}
		// Keep a license's verification mark while its facts are unchanged.
		for i, l := range in.Licenses {
			for _, b := range before.Licenses {
				if b.ID == l.ID && b.sameFacts(l) {
					in.Licenses[i].Verified, in.Licenses[i].VerifiedAt = b.Verified, b.VerifiedAt
				}
			}
		}
		// The entity mark covers identity: name, GLNs and company prefixes.
		keepMark := hadBefore && before.Name == in.Name && ptrEq(before.GLN, in.GLN) && slices.Equal(before.GLNs, in.GLNs) && slices.Equal(before.CompanyPrefixes, in.CompanyPrefixes)
		l, err := db.One[Listing](r.Context(), tx, `insert into directory_listing (tenant_id, name, gln, glns, company_prefixes, country, kind, description, website, contact, licenses, endpoints, published)
			values (app_tenant(), $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			on conflict (tenant_id) do update set name=excluded.name, gln=excluded.gln, glns=excluded.glns, company_prefixes=excluded.company_prefixes,
				country=excluded.country, kind=excluded.kind, description=excluded.description, website=excluded.website, contact=excluded.contact,
				licenses=excluded.licenses, endpoints=excluded.endpoints, published=excluded.published, updated_at=now(),
				verified = directory_listing.verified and $13, verified_at = case when $13 then directory_listing.verified_at end,
				verified_by = case when $13 then directory_listing.verified_by else '' end
			returning `+listingCols,
			in.Name, in.GLN, in.GLNs, in.CompanyPrefixes, in.Country, in.Kind, in.Description, in.Website, in.Contact, in.Licenses, in.Endpoints, in.Published, keepMark)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "directory.publish", Entity: "directory_listing", EntityID: l.TenantID.String(), Before: prev, After: l}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, l)
		return nil
	})
}

func ptrEq(a, b *string) bool {
	return (a == nil || *a == "") && (b == nil || *b == "") || a != nil && b != nil && *a == *b
}

// --- public directory (rate-limited, published profiles only) ---

func (m *Module) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		m.mu.Lock()
		if m.limiters == nil || len(m.limiters) > 100_000 {
			m.limiters = map[string]*rate.Limiter{}
		}
		l, ok := m.limiters[host]
		if !ok {
			l = rate.NewLimiter(rate.Limit(5), 30) // 5 req/s, burst 30 (as the verify endpoints)
			m.limiters[host] = l
		}
		m.mu.Unlock()
		if !l.Allow() {
			httpx.Fail(w, r, httpx.TooMany("rate limit exceeded"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Module) publicSearch(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	term := strings.TrimSpace(q.Get("q"))
	var items []PublicProfile
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		items, err = db.List[PublicProfile](r.Context(), tx, `select `+publicCols+` from directory_listing
			where published and ($1 = '' or name ilike '%'||$1||'%' or gln = $1 or $1 = any(glns) or $1 = any(company_prefixes))
			  and ($2 = '' or country = upper($2)) and ($3 = '' or kind = $3) and (not $4 or verified)
			order by verified desc, name limit 100`, term, q.Get("country"), q.Get("kind"), q.Get("verified") == "true")
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[PublicProfile]{Items: items, Total: len(items)})
	return nil
}

func (m *Module) publicProfile(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "tenantId")
	if err != nil {
		return err
	}
	return m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[PublicProfile](r.Context(), tx, `select `+publicCols+` from directory_listing where tenant_id = $1 and published`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, p)
		}
		return err
	})
}

// verifyProfile sets verification marks on a profile (operators only).
func (m *Module) verifyProfile(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "tenantId")
	if err != nil {
		return err
	}
	var in struct {
		Verified bool            `json:"verified"`
		Note     string          `json:"note"`
		Licenses map[string]bool `json:"licenses"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	me := auth.MustFrom(r.Context())
	return m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		l, err := db.One[Listing](r.Context(), tx, `select `+listingCols+` from directory_listing where tenant_id = $1 for update`, id)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for i, lic := range l.Licenses {
			v, ok := in.Licenses[lic.ID]
			if !ok {
				continue
			}
			l.Licenses[i].Verified, l.Licenses[i].VerifiedAt = v, nil
			if v {
				l.Licenses[i].VerifiedAt = &now
			}
			delete(in.Licenses, lic.ID)
		}
		if len(in.Licenses) > 0 {
			return httpx.BadRequest("unknown license id on this profile")
		}
		after, err := db.One[Listing](r.Context(), tx, `update directory_listing set verified = $2, verified_at = case when $2 then now() end,
			verified_by = case when $2 then $3 else '' end, verification_note = $4, licenses = $5 where tenant_id = $1 returning `+listingCols,
			id, in.Verified, me.Actor(), in.Note, l.Licenses)
		if err != nil {
			return err
		}
		rec := audit.Record{Action: "directory.verify", Entity: "directory_listing", EntityID: id.String(), Before: l, After: after}
		if _, err := audit.WriteAs(r.Context(), tx, id, me.AuditActor(), rec); err != nil {
			return err
		}
		if _, err := audit.WriteAs(r.Context(), tx, me.TenantID, me.AuditActor(), rec); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, after)
		return nil
	})
}

// --- connection requests ---

type connectionRequest struct {
	ID         uuid.UUID `json:"id" db:"id"`
	FromTenant uuid.UUID `json:"fromTenant" db:"from_tenant"`
	FromName   string    `json:"fromName" db:"from_name"`
	Message    string    `json:"message" db:"message"`
	Status     string    `json:"status" db:"status"`
	CreatedAt  time.Time `json:"createdAt" db:"created_at"`
}

// requestConnection asks a published tenant to connect; it lands in their alert inbox.
func (m *Module) requestConnection(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "tenantId")
	if err != nil {
		return err
	}
	var in struct {
		Message string `json:"message"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Message) > 2000 {
		return httpx.BadRequest("message is limited to 2000 characters")
	}
	me := auth.MustFrom(r.Context())
	if id == me.TenantID {
		return httpx.BadRequest("cannot request a connection with yourself")
	}
	var req connectionRequest
	err = m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var published bool
		if err := tx.QueryRow(r.Context(), `select published from directory_listing where tenant_id = $1`, id).Scan(&published); err != nil || !published {
			return httpx.NotFound("no published profile")
		}
		var from string
		if err := tx.QueryRow(r.Context(), `select coalesce((select name from directory_listing where tenant_id = $1), (select name from tenant where id = $1))`, me.TenantID).Scan(&from); err != nil {
			return err
		}
		var err error
		req, err = db.One[connectionRequest](r.Context(), tx, `insert into directory_connection_request (id, tenant_id, from_tenant, from_name, message)
			values ($1,$2,$3,$4,$5) returning id, from_tenant, from_name, message, status, created_at`, ids.New(), id, me.TenantID, from, in.Message)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id)
			values ($1, $2, 'directory.connection_request', 'info', $3, $4, 'directory_connection_request', $5)`,
			ids.New(), id, from+" asks to connect", map[string]any{"fromTenant": me.TenantID, "message": in.Message}, req.ID.String()); err != nil {
			return err
		}
		rec := audit.Record{Action: "directory.connection_request", Entity: "directory_connection_request", EntityID: req.ID.String(), After: req}
		if _, err := audit.WriteAs(r.Context(), tx, id, me.AuditActor(), rec); err != nil {
			return err
		}
		_, err = audit.WriteAs(r.Context(), tx, me.TenantID, me.AuditActor(), rec)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, req)
	return nil
}

func (m *Module) listConnectionRequests(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[connectionRequest](r.Context(), tx, `select id, from_tenant, from_name, message, status, created_at from directory_connection_request order by created_at desc limit 200`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[connectionRequest]{Items: items, Total: len(items)})
		}
		return err
	})
}

// Public implements kernel.PublicRoutes: the network directory.
func (m *Module) Public(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(m.limit)
		r.Get("/directory", httpx.Handler(m.publicSearch))
		r.Get("/directory/{tenantId}", httpx.Handler(m.publicProfile))
	})
}
