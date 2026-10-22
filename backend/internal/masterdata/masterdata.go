// Package masterdata owns products (GTIN/NDC/market codes), packaging hierarchy templates
// and CSV import. Other modules read products through the Lookup interface.
package masterdata

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Module is the master data bounded context.
type Module struct{ kernel.Deps }

// New creates the module.
func New(d kernel.Deps) *Module { return &Module{Deps: d} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "masterdata" }

// Product is a trade item.
type Product struct {
	ID                  uuid.UUID         `json:"id" db:"id"`
	GTIN                string            `json:"gtin" db:"gtin"`
	Name                string            `json:"name" db:"name"`
	Description         string            `json:"description" db:"description"`
	CompanyPrefixLength int               `json:"companyPrefixLength" db:"company_prefix_length"`
	NDC                 *string           `json:"ndc,omitempty" db:"ndc"`
	MarketCodes         map[string]string `json:"marketCodes" db:"market_codes"`
	TargetMarkets       []string          `json:"targetMarkets" db:"target_markets"`
	DosageForm          string            `json:"dosageForm" db:"dosage_form"`
	Strength            string            `json:"strength" db:"strength"`
	PackSize            string            `json:"packSize" db:"pack_size"`
	ManufacturerOrgID   *uuid.UUID        `json:"manufacturerOrgId,omitempty" db:"manufacturer_org_id"`
	Vertical            string            `json:"vertical" db:"vertical"`
	Status              string            `json:"status" db:"status"`
	Attributes          map[string]any    `json:"attributes" db:"attributes"`
	CreatedAt           time.Time         `json:"createdAt" db:"created_at"`
	UpdatedAt           time.Time         `json:"updatedAt" db:"updated_at"`
}

const productCols = `id, gtin, name, description, company_prefix_length, ndc, market_codes, target_markets, dosage_form, strength, pack_size,
	manufacturer_org_id, vertical, status, attributes, created_at, updated_at`

// Level is one packaging level.
type Level struct {
	Level    string `json:"level"`
	GTIN     string `json:"gtin,omitempty"`
	Quantity int    `json:"quantity"`
}

// PackagingTemplate is the hierarchy for one product and market.
type PackagingTemplate struct {
	ID        uuid.UUID `json:"id" db:"id"`
	ProductID uuid.UUID `json:"productId" db:"product_id"`
	Market    string    `json:"market" db:"market"`
	Levels    []Level   `json:"levels" db:"levels"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

var levelOrder = map[string]int{"unit": 0, "bundle": 1, "case": 2, "pallet": 3, "shipment": 4}

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Route("/products", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleCompliance))
		r.Get("/", httpx.Handler(m.list))
		r.Post("/", httpx.Handler(m.create))
		r.Post("/import", httpx.Handler(m.importCSV))
		r.Get("/{id}", httpx.Handler(m.get))
		r.Put("/{id}", httpx.Handler(m.update))
		r.Get("/{id}/packaging", httpx.Handler(m.listPackaging))
		r.Get("/{id}/packaging/{market}", httpx.Handler(m.getPackaging))
		r.Put("/{id}/packaging/{market}", httpx.Handler(m.putPackaging))
	})
}

// ProductInput is the create/update payload.
type ProductInput struct {
	GTIN                string            `json:"gtin"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	CompanyPrefixLength int               `json:"companyPrefixLength"`
	NDC                 *string           `json:"ndc"`
	MarketCodes         map[string]string `json:"marketCodes"`
	TargetMarkets       []string          `json:"targetMarkets"`
	DosageForm          string            `json:"dosageForm"`
	Strength            string            `json:"strength"`
	PackSize            string            `json:"packSize"`
	ManufacturerOrgID   *uuid.UUID        `json:"manufacturerOrgId"`
	Vertical            string            `json:"vertical"`
	Status              string            `json:"status"`
	Attributes          map[string]any    `json:"attributes"`
}

// Validate normalizes and checks the input.
func (in *ProductInput) Validate() error {
	in.GTIN = gs1.NormalizeGTIN(strings.TrimSpace(in.GTIN))
	if !gs1.ValidGTIN(in.GTIN) {
		return httpx.BadRequest("gtin must be a valid GTIN-8/12/13/14 with check digit")
	}
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("name is required")
	}
	if in.CompanyPrefixLength == 0 {
		in.CompanyPrefixLength = 7
	}
	if in.CompanyPrefixLength < 6 || in.CompanyPrefixLength > 12 {
		return httpx.BadRequest("companyPrefixLength must be 6-12")
	}
	if in.MarketCodes == nil {
		in.MarketCodes = map[string]string{}
	}
	if in.TargetMarkets == nil {
		in.TargetMarkets = []string{}
	}
	for i, mk := range in.TargetMarkets {
		in.TargetMarkets[i] = strings.ToUpper(strings.TrimSpace(mk))
	}
	if in.Vertical == "" {
		in.Vertical = "pharma"
	}
	if in.Vertical != "pharma" && in.Vertical != "food" && in.Vertical != "device" {
		return httpx.BadRequest("vertical must be pharma, food or device")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.Attributes == nil {
		in.Attributes = map[string]any{}
	}
	return nil
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query().Get("q")
	limit := httpx.IntQuery(r, "limit", 100, 1, 1000)
	offset := httpx.IntQuery(r, "offset", 0, 0, 1<<30)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		where := `($1 = '' or gtin like $1 || '%' or name ilike '%' || $1 || '%' or ndc = $1)`
		total, err := db.Count(r.Context(), tx, `select count(*) from product where `+where, q)
		if err != nil {
			return err
		}
		items, err := db.List[Product](r.Context(), tx, `select `+productCols+` from product where `+where+` order by name limit $2 offset $3`, q, limit, offset)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Product]{Items: items, Total: total})
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
		p, err := db.One[Product](r.Context(), tx, `select `+productCols+` from product where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, p)
		}
		return err
	})
}

// Upsert creates or updates by GTIN inside tx; returns the product and whether it was created.
func (m *Module) Upsert(ctx context.Context, tx pgx.Tx, in ProductInput) (Product, bool, error) {
	before, err := db.One[Product](ctx, tx, `select `+productCols+` from product where gtin=$1`, in.GTIN)
	exists := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return Product{}, false, err
	}
	p, err := db.One[Product](ctx, tx, `insert into product (id, tenant_id, gtin, name, description, company_prefix_length, ndc, market_codes, target_markets,
			dosage_form, strength, pack_size, manufacturer_org_id, vertical, status, attributes)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		on conflict (tenant_id, gtin) do update set name=excluded.name, description=excluded.description, company_prefix_length=excluded.company_prefix_length,
			ndc=excluded.ndc, market_codes=excluded.market_codes, target_markets=excluded.target_markets, dosage_form=excluded.dosage_form,
			strength=excluded.strength, pack_size=excluded.pack_size, manufacturer_org_id=excluded.manufacturer_org_id, vertical=excluded.vertical,
			status=excluded.status, attributes=excluded.attributes, updated_at=now()
		returning `+productCols,
		ids.New(), in.GTIN, in.Name, in.Description, in.CompanyPrefixLength, in.NDC, in.MarketCodes, in.TargetMarkets, in.DosageForm, in.Strength,
		in.PackSize, in.ManufacturerOrgID, in.Vertical, in.Status, in.Attributes)
	if err != nil {
		return p, false, err
	}
	rec := audit.Record{Action: "product.create", Entity: "product", EntityID: p.ID.String(), After: p}
	if exists {
		rec.Action, rec.Before = "product.update", before
	}
	_, err = audit.Write(ctx, tx, rec)
	return p, !exists, err
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	var in ProductInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		n, err := db.Count(r.Context(), tx, `select count(*) from product where gtin=$1`, in.GTIN)
		if err != nil {
			return err
		}
		if n > 0 {
			return httpx.Conflict("a product with this GTIN already exists")
		}
		p, _, err := m.Upsert(r.Context(), tx, in)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, p)
		}
		return err
	})
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in ProductInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		cur, err := db.One[Product](r.Context(), tx, `select `+productCols+` from product where id=$1`, id)
		if err != nil {
			return err
		}
		if cur.GTIN != in.GTIN {
			return httpx.BadRequest("gtin cannot be changed; create a new product")
		}
		p, _, err := m.Upsert(r.Context(), tx, in)
		if err == nil {
			httpx.JSON(w, http.StatusOK, p)
		}
		return err
	})
}

// ImportResult summarizes a CSV import.
type ImportResult struct {
	Created int              `json:"created"`
	Updated int              `json:"updated"`
	Errors  []ImportRowError `json:"errors"`
}

// ImportRowError is a rejected CSV line.
type ImportRowError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// importCSV accepts header: gtin,name,ndc,company_prefix_length,target_markets,dosage_form,strength,pack_size,description,vertical.
// Valid rows are applied; invalid rows are reported with their line numbers.
func (m *Module) importCSV(w http.ResponseWriter, r *http.Request) error {
	res, err := m.ImportCSV(r.Context(), io.LimitReader(r.Body, 50<<20))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// ImportCSV parses and upserts products.
func (m *Module) ImportCSV(ctx context.Context, rd io.Reader) (ImportResult, error) {
	cr := csv.NewReader(rd)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return ImportResult{}, httpx.BadRequest("empty or invalid CSV")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	if _, ok := col["gtin"]; !ok {
		return ImportResult{}, httpx.BadRequest("CSV header must include gtin and name")
	}
	if _, ok := col["name"]; !ok {
		return ImportResult{}, httpx.BadRequest("CSV header must include gtin and name")
	}
	get := func(rec []string, k string) string {
		if i, ok := col[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	res := ImportResult{Errors: []ImportRowError{}}
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		line := 1
		for {
			rec, err := cr.Read()
			line++
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				res.Errors = append(res.Errors, ImportRowError{line, err.Error()})
				continue
			}
			in := ProductInput{GTIN: get(rec, "gtin"), Name: get(rec, "name"), DosageForm: get(rec, "dosage_form"), Strength: get(rec, "strength"),
				PackSize: get(rec, "pack_size"), Description: get(rec, "description"), Vertical: get(rec, "vertical")}
			if v := get(rec, "ndc"); v != "" {
				in.NDC = &v
			}
			if v := get(rec, "company_prefix_length"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil {
					res.Errors = append(res.Errors, ImportRowError{line, "company_prefix_length must be a number"})
					continue
				}
				in.CompanyPrefixLength = n
			}
			if v := get(rec, "target_markets"); v != "" {
				in.TargetMarkets = strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == '|' || r == ' ' })
			}
			if err := in.Validate(); err != nil {
				res.Errors = append(res.Errors, ImportRowError{line, err.Error()})
				continue
			}
			_, created, err := m.Upsert(ctx, tx, in)
			if err != nil {
				return fmt.Errorf("line %d: %w", line, err)
			}
			if created {
				res.Created++
			} else {
				res.Updated++
			}
		}
	})
	return res, err
}

func validateLevels(levels []Level) error {
	if len(levels) == 0 {
		return httpx.BadRequest("at least one level is required")
	}
	prev := -1
	for i, l := range levels {
		o, ok := levelOrder[l.Level]
		if !ok {
			return httpx.BadRequest("level must be unit, bundle, case, pallet or shipment")
		}
		if o <= prev {
			return httpx.BadRequest("levels must be ordered from unit up and not repeat")
		}
		prev = o
		if i > 0 && l.Quantity < 1 {
			return httpx.BadRequest(fmt.Sprintf("%s quantity (children per %s) must be ≥ 1", l.Level, l.Level))
		}
		if l.GTIN != "" && !gs1.ValidGTIN(gs1.NormalizeGTIN(l.GTIN)) {
			return httpx.BadRequest("invalid GTIN on level " + l.Level)
		}
	}
	return nil
}

func (m *Module) listPackaging(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[PackagingTemplate](r.Context(), tx, `select id, product_id, market, levels, updated_at from packaging_template where product_id=$1 order by market`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[PackagingTemplate]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getPackaging(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		t, err := db.One[PackagingTemplate](r.Context(), tx, `select id, product_id, market, levels, updated_at from packaging_template where product_id=$1 and market=$2`,
			id, strings.ToUpper(chi.URLParam(r, "market")))
		if err == nil {
			httpx.JSON(w, http.StatusOK, t)
		}
		return err
	})
}

func (m *Module) putPackaging(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Levels []Level `json:"levels"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := validateLevels(in.Levels); err != nil {
		return err
	}
	market := strings.ToUpper(chi.URLParam(r, "market"))
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		t, err := db.One[PackagingTemplate](r.Context(), tx, `insert into packaging_template (id, tenant_id, product_id, market, levels) values ($1, app_tenant(), $2, $3, $4)
			on conflict (tenant_id, product_id, market) do update set levels=excluded.levels, updated_at=now()
			returning id, product_id, market, levels, updated_at`, ids.New(), id, market, in.Levels)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "packaging.put", Entity: "product", EntityID: id.String(), After: t}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, t)
		return nil
	})
}

// --- Lookup used by other modules (read-only, inside the caller's tenant tx) ---

// ByGTIN returns a product by GTIN inside tx.
func ByGTIN(ctx context.Context, tx pgx.Tx, gtin string) (Product, error) {
	return db.One[Product](ctx, tx, `select `+productCols+` from product where gtin=$1`, gtin)
}

// ByID returns a product by id inside tx.
func ByID(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Product, error) {
	return db.One[Product](ctx, tx, `select `+productCols+` from product where id=$1`, id)
}

// KnownGTINs returns which of the GTINs exist (for EPCIS validation).
func KnownGTINs(ctx context.Context, tx pgx.Tx, gtins []string) (map[string]Product, error) {
	out := map[string]Product{}
	if len(gtins) == 0 {
		return out, nil
	}
	ps, err := db.List[Product](ctx, tx, `select `+productCols+` from product where gtin = any($1)`, gtins)
	for _, p := range ps {
		out[p.GTIN] = p
	}
	return out, err
}
