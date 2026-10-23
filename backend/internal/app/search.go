package app

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// SearchHit is one global-search result; Path is the console route to open.
type SearchHit struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Path     string `json:"path"`
}

// searchSources are queried in order; each returns at most searchPerKind rows for the
// pattern $1. Every query carries the explicit tenant predicate so it can use the
// (tenant_id, ...) indexes under RLS.
var searchSources = []struct {
	kind, sql, path string
}{
	{"product", `select id::text, name, gtin from product where tenant_id = app_tenant() and (name ilike $1 or gtin like $1) order by name limit $2`, "/settings/products/"},
	{"shipment", `select id::text, coalesce(nullif(po_number, ''), asn_number, id::text), direction || ' · ' || status from shipment
		where tenant_id = app_tenant() and (po_number ilike $1 or asn_number ilike $1) order by created_at desc limit $2`, "/link/shipments?id="},
	{"partner", `select id::text, name, coalesce(gln, '') from partner where tenant_id = app_tenant() and (name ilike $1 or gln like $1) order by name limit $2`, "/link/partners?id="},
	{"lot", `select id::text, tlc, description from food_lot where tenant_id = app_tenant() and (tlc ilike $1 or description ilike $1) order by created_at desc limit $2`, "/food/lots?id="},
	{"case", `select id::text, title, kind || ' · ' || status from work_case where tenant_id = app_tenant() and title ilike $1 order by created_at desc limit $2`, "/act/cases?id="},
}

const searchPerKind = 5

// search answers the console's global search box: a scanned or typed GS1 code opens serial
// lookup; any other text matches products, shipments, partners, lots and cases by name or number.
func (a *App) search(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	hits := []SearchHit{}
	if len([]rune(q)) < 2 {
		httpx.JSON(w, http.StatusOK, map[string]any{"items": hits})
		return nil
	}
	if p, err := serialization.ParseCode(q); err == nil && p.Serial != "" {
		link := gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: p.GTIN, Serial: p.Serial})
		hits = append(hits, SearchHit{Kind: "serial", ID: link, Title: p.Serial, Subtitle: "GTIN " + p.GTIN,
			Path: "/trace/lookup?epc=" + url.QueryEscape(link)})
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
	err := a.Deps.DB.Tx(r.Context(), auth.MustFrom(r.Context()).TenantID, func(tx pgx.Tx) error {
		for _, s := range searchSources {
			rows, err := tx.Query(r.Context(), s.sql, pattern, searchPerKind)
			if err != nil {
				return err
			}
			for rows.Next() {
				var h SearchHit
				if err := rows.Scan(&h.ID, &h.Title, &h.Subtitle); err != nil {
					rows.Close()
					return err
				}
				h.Kind, h.Path = s.kind, s.path+h.ID
				hits = append(hits, h)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": hits})
	return nil
}
