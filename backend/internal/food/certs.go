package food

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Certificate is a certificate (halal, origin, organic, ...) held in the supplier compliance
// hub. Certification body rules are not modelled: the fields are the same for every kind.
// TODO(spec): confirm which certification bodies or accreditation checks (e.g. for GCC halal)
// a market requires before validating issuers.
type Certificate struct {
	ID          uuid.UUID  `json:"id"`
	Kind        string     `json:"kind"`
	Number      string     `json:"number"`
	Issuer      string     `json:"issuer"`
	Scope       string     `json:"scope"`
	ValidFrom   *time.Time `json:"validFrom,omitempty"`
	ValidTo     *time.Time `json:"validTo,omitempty"`
	DocumentID  *uuid.UUID `json:"documentId,omitempty"`
	PartnerID   *uuid.UUID `json:"partnerId,omitempty"`
	Supplier    string     `json:"supplier"`
	SupplierGLN string     `json:"supplierGln,omitempty"`
}

// CertificateInput creates a certificate in the hub.
type CertificateInput struct {
	PartnerID  *uuid.UUID
	Kind       string
	Number     string
	Issuer     string
	Scope      string
	ValidFrom  *time.Time
	ValidTo    *time.Time
	DocumentID *uuid.UUID
	Notes      string
}

// CertificateStore is the certificate registry food links to. The supplier compliance hub
// implements it; internal/app wires it, so food does not depend on that module.
type CertificateStore interface {
	// CreateCertificate stores a certificate inside tx (tenant scoped) and returns it.
	CreateCertificate(ctx context.Context, tx pgx.Tx, in CertificateInput) (Certificate, error)
	// Certificates returns the certificates with the given ids, plus the certificates held for
	// suppliers whose GLN is in supplierGLNs (those carry SupplierGLN).
	Certificates(ctx context.Context, tx pgx.Tx, ids []uuid.UUID, supplierGLNs []string) ([]Certificate, error)
}

// Certificate validity states.
const (
	CertValid       = "valid"
	CertExpiring    = "expiring"
	CertExpired     = "expired"
	CertNotYetValid = "not_yet_valid"
)

// expiringWithin matches the supplier compliance hub's expiry window.
const expiringWithin = 30

// CertStatus is the validity of c on day (UTC date).
func CertStatus(c Certificate, day time.Time) string {
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case c.ValidFrom != nil && d.Before(*c.ValidFrom):
		return CertNotYetValid
	case c.ValidTo == nil:
		return CertValid
	case c.ValidTo.Before(d):
		return CertExpired
	case c.ValidTo.Before(d.AddDate(0, 0, expiringWithin)):
		return CertExpiring
	}
	return CertValid
}

// Ref is a short certificate reference for exports: kind:number (issuer).
func (c Certificate) Ref() string {
	s := c.Kind
	if c.Number != "" {
		s += ":" + c.Number
	}
	if c.Issuer != "" {
		s += " (" + c.Issuer + ")"
	}
	return s
}

// CertLink attaches a certificate to a lot, product or site.
type CertLink struct {
	ID         uuid.UUID `json:"id" db:"id"`
	CertID     uuid.UUID `json:"certificateId" db:"certificate_id"`
	TargetType string    `json:"targetType" db:"target_type"`
	Target     string    `json:"target" db:"target"`
	CreatedAt  time.Time `json:"createdAt" db:"created_at"`
}

const linkCols = `id, certificate_id, target_type, target, created_at`

// LinkedCertificate is a certificate with its validity and links.
type LinkedCertificate struct {
	Certificate
	Status string     `json:"status"`
	Links  []CertLink `json:"links"`
}

// Coverage is a certificate covering a lot and how: directly ("lot"), via its product
// ("product"), via a linked site ("site") or as a supplier certificate in the hub held for one
// of the lot's supplier sites ("supplier").
type Coverage struct {
	Certificate
	Status string     `json:"status"`
	Via    string     `json:"via"`
	ViaRef string     `json:"viaRef"`
	LinkID *uuid.UUID `json:"linkId,omitempty"`
}

func (m *Module) certs() (CertificateStore, error) {
	if m.Certs == nil {
		return nil, httpx.BadRequest("the certificate registry is not configured")
	}
	return m.Certs, nil
}

// linkTarget validates and normalises a link target inside tx.
func linkTarget(ctx context.Context, tx pgx.Tx, typ, target string) (string, error) {
	target = strings.TrimSpace(target)
	switch typ {
	case "lot":
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from food_lot where tlc=$1`, target).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return "", httpx.BadRequest("unknown traceability lot code " + target)
		}
	case "product":
		id, err := uuid.Parse(target)
		if err != nil {
			// A GTIN identifies the product as well.
			if e := tx.QueryRow(ctx, `select id from product where gtin=$1`, gs1.NormalizeGTIN(target)).Scan(&id); e != nil {
				return "", httpx.BadRequest("unknown product " + target)
			}
		} else {
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from product where id=$1`, id).Scan(&n); err != nil {
				return "", err
			}
			if n == 0 {
				return "", httpx.BadRequest("unknown product " + target)
			}
		}
		target = id.String()
	case "site":
		if !gs1.ValidGLN(target) {
			return "", httpx.BadRequest("site links take a valid GLN")
		}
	default:
		return "", httpx.BadRequest("targetType must be lot, product or site")
	}
	return target, nil
}

type linkInput struct {
	TargetType string `json:"targetType"`
	Target     string `json:"target"`
}

func (m *Module) insertLink(ctx context.Context, tx pgx.Tx, cert uuid.UUID, in linkInput) (CertLink, error) {
	target, err := linkTarget(ctx, tx, in.TargetType, in.Target)
	if err != nil {
		return CertLink{}, err
	}
	l, err := db.One[CertLink](ctx, tx, `insert into food_certificate_link (id, tenant_id, certificate_id, target_type, target) values ($1, app_tenant(), $2,$3,$4)
		on conflict (tenant_id, certificate_id, target_type, target) do nothing returning `+linkCols, ids.New(), cert, in.TargetType, target)
	if errors.Is(err, db.ErrNotFound) {
		return l, httpx.Conflict("the certificate is already linked to this " + in.TargetType)
	}
	if err != nil {
		return l, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "food.certificate.link", Entity: "food_certificate_link", EntityID: l.ID.String(), After: l})
	return l, err
}

func certDate(s string) (*time.Time, error) {
	if len(s) > 10 {
		s = s[:10]
	}
	return parseDate(s)
}

// createCertificate stores a certificate in the hub and links it to lots, products or sites.
func (m *Module) createCertificate(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PartnerID  *uuid.UUID  `json:"partnerId"`
		Kind       string      `json:"kind"`
		Number     string      `json:"number"`
		Issuer     string      `json:"issuer"`
		Scope      string      `json:"scope"`
		ValidFrom  string      `json:"validFrom"`
		ValidTo    string      `json:"validTo"`
		DocumentID *uuid.UUID  `json:"documentId"`
		Notes      string      `json:"notes"`
		Links      []linkInput `json:"links"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind == "" || len(in.Kind) > 64 {
		return httpx.BadRequest("kind is required (e.g. halal, origin, organic; max 64 characters)")
	}
	if len(in.Links) == 0 {
		return httpx.BadRequest("at least one link (lot, product or site) is required")
	}
	vf, err := certDate(in.ValidFrom)
	if err != nil {
		return err
	}
	vt, err := certDate(in.ValidTo)
	if err != nil {
		return err
	}
	if vf != nil && vt != nil && vt.Before(*vf) {
		return httpx.BadRequest("validTo must not be before validFrom")
	}
	store, err := m.certs()
	if err != nil {
		return err
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		c, err := store.CreateCertificate(ctx, tx, CertificateInput{PartnerID: in.PartnerID, Kind: in.Kind, Number: strings.TrimSpace(in.Number), Issuer: strings.TrimSpace(in.Issuer),
			Scope: strings.TrimSpace(in.Scope), ValidFrom: vf, ValidTo: vt, DocumentID: in.DocumentID, Notes: in.Notes})
		if err != nil {
			return err
		}
		out := LinkedCertificate{Certificate: c, Status: CertStatus(c, time.Now()), Links: []CertLink{}}
		for _, li := range in.Links {
			l, err := m.insertLink(ctx, tx, c.ID, li)
			if err != nil {
				return err
			}
			out.Links = append(out.Links, l)
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "food.certificate.create", Entity: "supplier_record", EntityID: c.ID.String(), After: out}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, out)
		return nil
	})
}

// addCertificateLink links an existing hub certificate to a lot, product or site.
func (m *Module) addCertificateLink(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in linkInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	store, err := m.certs()
	if err != nil {
		return err
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		cs, err := store.Certificates(ctx, tx, []uuid.UUID{id}, nil)
		if err != nil {
			return err
		}
		if len(cs) == 0 {
			return httpx.NotFound("certificate not found")
		}
		l, err := m.insertLink(ctx, tx, id, in)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, l)
		}
		return err
	})
}

// deleteCertificateLink removes a link; the certificate stays in the hub.
func (m *Module) deleteCertificateLink(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "linkId")
	if err != nil {
		return err
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		l, err := db.One[CertLink](ctx, tx, `delete from food_certificate_link where id=$1 returning `+linkCols, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "food.certificate.unlink", Entity: "food_certificate_link", EntityID: id.String(), Before: l}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// listCertificates lists linked certificates, optionally for one target or kind/status.
func (m *Module) listCertificates(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	store, err := m.certs()
	if err != nil {
		return err
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		links, err := db.List[CertLink](ctx, tx, `select `+linkCols+` from food_certificate_link where ($1='' or target_type=$1) and ($2='' or target=$2) order by created_at`,
			q.Get("targetType"), q.Get("target"))
		if err != nil {
			return err
		}
		byCert := map[uuid.UUID][]CertLink{}
		var certIDs []uuid.UUID
		for _, l := range links {
			if _, ok := byCert[l.CertID]; !ok {
				certIDs = append(certIDs, l.CertID)
			}
			byCert[l.CertID] = append(byCert[l.CertID], l)
		}
		cs, err := store.Certificates(ctx, tx, certIDs, nil)
		if err != nil {
			return err
		}
		items := []LinkedCertificate{}
		now := time.Now()
		for _, c := range cs {
			lc := LinkedCertificate{Certificate: c, Status: CertStatus(c, now), Links: byCert[c.ID]}
			if (q.Get("kind") != "" && !strings.EqualFold(c.Kind, q.Get("kind"))) || (q.Get("status") != "" && lc.Status != q.Get("status")) {
				continue
			}
			items = append(items, lc)
		}
		sort.Slice(items, func(i, j int) bool { return certLess(items[i].Certificate, items[j].Certificate) })
		httpx.JSON(w, http.StatusOK, httpx.Page[LinkedCertificate]{Items: items, Total: len(items)})
		return nil
	})
}

// certLess orders certificates by expiry (soonest first, open-ended last), then kind and number.
func certLess(a, b Certificate) bool {
	switch {
	case a.ValidTo != nil && b.ValidTo != nil && !a.ValidTo.Equal(*b.ValidTo):
		return a.ValidTo.Before(*b.ValidTo)
	case (a.ValidTo == nil) != (b.ValidTo == nil):
		return a.ValidTo != nil
	case a.Kind != b.Kind:
		return a.Kind < b.Kind
	}
	return a.Number < b.Number
}

// lotCertificates lists the certificates covering a lot.
func (m *Module) lotCertificates(w http.ResponseWriter, r *http.Request) error {
	tlc := chi.URLParam(r, "tlc")
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from food_lot where tlc=$1`, tlc).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return httpx.NotFound("lot not found")
		}
		cov, err := m.coverage(ctx, tx, []string{tlc})
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"tlc": tlc, "certificates": cov[tlc]})
		return nil
	})
}

// lotKeys are what a lot can be covered through: its product and its sites (TLC source,
// location and the ship-from of its receiving events).
type lotKeys struct {
	product string
	sites   []string
}

func lotKeysOf(ctx context.Context, tx pgx.Tx, tlcs []string) (map[string]lotKeys, error) {
	rows, err := tx.Query(ctx, `select l.tlc, coalesce(l.product_id::text, ''),
			array(select distinct s from unnest(array[l.tlc_source, coalesce(l.location_gln, '')] ||
				coalesce((select array_agg(c.ship_from) from food_cte c where c.tlc = l.tlc and c.type in ('receiving','first_land_based_receiving')), '{}')) s where s <> '')
		from food_lot l where l.tlc = any($1)`, tlcs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]lotKeys{}
	for rows.Next() {
		var tlc string
		var k lotKeys
		if err := rows.Scan(&tlc, &k.product, &k.sites); err != nil {
			return nil, err
		}
		out[tlc] = k
	}
	return out, rows.Err()
}

// coverage returns, per lot, the certificates covering it. Lots without certificates map to an empty list.
func (m *Module) coverage(ctx context.Context, tx pgx.Tx, tlcs []string) (map[string][]Coverage, error) {
	out := map[string][]Coverage{}
	for _, t := range tlcs {
		out[t] = []Coverage{}
	}
	if m.Certs == nil || len(tlcs) == 0 {
		return out, nil
	}
	keys, err := lotKeysOf(ctx, tx, tlcs)
	if err != nil {
		return nil, err
	}
	products, sites := []string{}, []string{}
	for _, k := range keys {
		if k.product != "" {
			products = append(products, k.product)
		}
		sites = append(sites, k.sites...)
	}
	links, err := db.List[CertLink](ctx, tx, `select `+linkCols+` from food_certificate_link
		where (target_type='lot' and target = any($1)) or (target_type='product' and target = any($2)) or (target_type='site' and target = any($3))`, tlcs, products, sites)
	if err != nil {
		return nil, err
	}
	certIDs := make([]uuid.UUID, 0, len(links))
	for _, l := range links {
		certIDs = append(certIDs, l.CertID)
	}
	cs, err := m.Certs.Certificates(ctx, tx, certIDs, sites)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]Certificate{}
	var held []Certificate
	for _, c := range cs {
		byID[c.ID] = c
		if c.SupplierGLN != "" {
			held = append(held, c)
		}
	}
	now := time.Now()
	for _, tlc := range tlcs {
		k, ok := keys[tlc]
		if !ok {
			continue
		}
		seen := map[string]bool{}
		linked := map[uuid.UUID]bool{}
		add := func(c Certificate, via, ref string, link *uuid.UUID) {
			key := c.ID.String() + via + ref
			if seen[key] || (via == "supplier" && linked[c.ID]) {
				return
			}
			seen[key], linked[c.ID] = true, true
			out[tlc] = append(out[tlc], Coverage{Certificate: c, Status: CertStatus(c, now), Via: via, ViaRef: ref, LinkID: link})
		}
		for _, l := range links {
			c, ok := byID[l.CertID]
			if !ok {
				continue
			}
			id := l.ID
			switch {
			case l.TargetType == "lot" && l.Target == tlc,
				l.TargetType == "product" && l.Target == k.product,
				l.TargetType == "site" && contains(k.sites, l.Target):
				add(c, l.TargetType, l.Target, &id)
			}
		}
		for _, c := range held {
			if contains(k.sites, c.SupplierGLN) {
				add(c, "supplier", c.SupplierGLN, nil)
			}
		}
		sort.SliceStable(out[tlc], func(i, j int) bool { return certLess(out[tlc][i].Certificate, out[tlc][j].Certificate) })
	}
	return out, nil
}

// --- scheduled expiry check ---

// Schedules implements kernel.Scheduled.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: "food.cert_expiry", Every: 6 * time.Hour, Run: m.CertificateExpiryAlerts}}
}

// CertificateExpiryAlerts raises an alert for each certificate linked to food lots, products or
// sites that has expired or expires within 30 days, listing the active lots it covers. Alerts
// are deduplicated per certificate, expiry date and state.
func (m *Module) CertificateExpiryAlerts(ctx context.Context) error {
	if m.Certs == nil {
		return nil
	}
	var tenants []uuid.UUID
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct tenant_id from food_certificate_link`)
		if err != nil {
			return err
		}
		tenants, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	})
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if err := m.DB.Tx(ctx, t, func(tx pgx.Tx) error { return m.expiryAlertsTenant(ctx, tx, t) }); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) expiryAlertsTenant(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	links, err := db.List[CertLink](ctx, tx, `select `+linkCols+` from food_certificate_link order by created_at`)
	if err != nil {
		return err
	}
	byCert := map[uuid.UUID][]CertLink{}
	var certIDs []uuid.UUID
	for _, l := range links {
		if _, ok := byCert[l.CertID]; !ok {
			certIDs = append(certIDs, l.CertID)
		}
		byCert[l.CertID] = append(byCert[l.CertID], l)
	}
	cs, err := m.Certs.Certificates(ctx, tx, certIDs, nil)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, c := range cs {
		st := CertStatus(c, now)
		if st != CertExpired && st != CertExpiring {
			continue
		}
		var lots, products, sites []string
		for _, l := range byCert[c.ID] {
			switch l.TargetType {
			case "lot":
				lots = append(lots, l.Target)
			case "product":
				products = append(products, l.Target)
			case "site":
				sites = append(sites, l.Target)
			}
		}
		rows, err := tx.Query(ctx, `select l.tlc from food_lot l where l.status in ('active','shipped') and (l.tlc = any($1) or l.product_id::text = any($2)
				or l.tlc_source = any($3) or l.location_gln = any($3)
				or exists (select 1 from food_cte c where c.tlc = l.tlc and c.type in ('receiving','first_land_based_receiving') and c.ship_from = any($3)))
			order by l.tlc`, lots, products, sites)
		if err != nil {
			return err
		}
		covered, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if covered == nil {
			covered = []string{}
		}
		severity, verb := "warning", "expires"
		if st == CertExpired {
			severity, verb = "error", "expired"
		}
		title := fmt.Sprintf("%s certificate %s %s on %s (%d active lots)", c.Kind, c.Number, verb, c.ValidTo.Format("2006-01-02"), len(covered))
		detail := map[string]any{"certificateId": c.ID, "kind": c.Kind, "number": c.Number, "issuer": c.Issuer, "validTo": c.ValidTo.Format("2006-01-02"),
			"status": st, "lots": covered, "links": byCert[c.ID]}
		ct, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			values ($1, app_tenant(), 'food.certificate_expiring', $2, $3, $4, 'supplier_record', $5, $6) on conflict do nothing`,
			ids.New(), severity, title, detail, c.ID.String(), "food-cert:"+c.ID.String()+":"+c.ValidTo.Format("2006-01-02")+":"+st)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 1 {
			if _, err := audit.WriteAs(ctx, tx, tenant, "system", audit.Record{Action: "food.certificate.expiry_alert", Entity: "supplier_record", EntityID: c.ID.String(), After: detail}); err != nil {
				return err
			}
		}
	}
	return nil
}

// certificateRefs maps each lot with events in the window to the references of the
// certificates covering it.
func (m *Module) certificateRefs(ctx context.Context, tx pgx.Tx, from, to time.Time) (map[string][]string, error) {
	tlcs, err := listStrings(ctx, tx, `select distinct tlc from food_cte where event_time >= $1 and event_time < $2`, from, to)
	if err != nil {
		return nil, err
	}
	cov, err := m.coverage(ctx, tx, tlcs)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for tlc, cs := range cov {
		for _, c := range cs {
			out[tlc] = append(out[tlc], c.Ref())
		}
		out[tlc] = dedupe(out[tlc])
	}
	return out, nil
}
