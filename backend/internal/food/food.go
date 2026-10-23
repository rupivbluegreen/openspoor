// Package food is the food vertical: lot-level traceability with traceability lot codes
// (TLCs), FSMA 204 critical tracking events (CTEs) and key data elements (KDEs),
// transformations (many input lots to one output lot), a farm/plot registry with GeoJSON,
// the FSMA 204 24-hour sortable spreadsheet, recalls (affected lots and customers,
// notifications) and EUDR due diligence statements. CTEs are mirrored into the EPCIS
// repository as class-level events, so food and pharma share Trace/Link/Act/Comply.
package food

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
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
	"github.com/openspoor/openspoor/internal/epcis"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Module is the food bounded context.
type Module struct {
	kernel.Deps
	EPCIS *epcis.Module
	// Certs is the certificate registry (the supplier compliance hub), wired by internal/app.
	Certs CertificateStore
}

// New creates the module.
func New(d kernel.Deps, ep *epcis.Module) *Module { return &Module{Deps: d, EPCIS: ep} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "food" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Route("/food", func(r chi.Router) {
		r.Use(m.requireFood, auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance, auth.RoleIntegration))
		r.Get("/lots", httpx.Handler(m.listLots))
		r.Post("/lots", httpx.Handler(m.createLot))
		r.Get("/lots/{tlc}/trace", httpx.Handler(m.traceLot))
		r.Get("/cte", httpx.Handler(m.listCTEs))
		r.Post("/cte", httpx.Handler(m.createCTE))
		r.Post("/transformations", httpx.Handler(m.transform))
		r.Get("/plots", httpx.Handler(m.listPlots))
		r.Post("/plots", httpx.Handler(m.createPlot))
		r.Get("/fsma204/export", httpx.Handler(m.fsmaExport))
		r.Get("/recalls", httpx.Handler(m.listRecalls))
		r.Post("/recalls", httpx.Handler(m.createRecall))
		r.Get("/recalls/{id}", httpx.Handler(m.getRecall))
		r.Post("/recalls/{id}/close", httpx.Handler(m.closeRecall))
		r.Get("/eudr/dds", httpx.Handler(m.listDDS))
		r.Post("/eudr/dds", httpx.Handler(m.createDDS))
		r.Get("/eudr/dds/{id}", httpx.Handler(m.getDDS))
		r.Post("/eudr/dds/{id}/submit", httpx.Handler(m.submitDDS))
		r.Get("/lots/{tlc}/certificates", httpx.Handler(m.lotCertificates))
		r.Get("/certificates", httpx.Handler(m.listCertificates))
		r.Post("/certificates", httpx.Handler(m.createCertificate))
		r.Post("/certificates/{id}/links", httpx.Handler(m.addCertificateLink))
		r.Delete("/certificates/links/{linkId}", httpx.Handler(m.deleteCertificateLink))
		r.Get("/traceability-plan", httpx.Handler(m.getPlan))
		r.Get("/traceability-plan/versions", httpx.Handler(m.listPlanVersions))
		r.Get("/traceability-plan/export", httpx.Handler(m.exportPlan))
		r.Get("/traceability-plan/settings", httpx.Handler(m.getPlanSettings))
		r.With(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance)).Put("/traceability-plan", httpx.Handler(m.putPlan))
		r.With(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance)).Put("/traceability-plan/settings", httpx.Handler(m.putPlanSettings))
		r.Get("/one-up-one-down", httpx.Handler(m.oneUpOneDown))
		r.Get("/one-up-one-down/authority-requests", httpx.Handler(m.listAuthorityRequests))
		r.Post("/one-up-one-down/authority-requests", httpx.Handler(m.createAuthorityRequest))
		r.Get("/one-up-one-down/authority-requests/{id}", httpx.Handler(m.getAuthorityRequest))
		r.Get("/one-up-one-down/authority-requests/{id}/export", httpx.Handler(m.exportAuthorityRequest))
	})
}

// requireFood allows the routes only for tenants with the food vertical enabled.
func (m *Module) requireFood(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var verticals []string
		err := m.Tx(r.Context(), func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(), `select verticals from tenant where id=app_tenant()`).Scan(&verticals)
		})
		if err != nil || !contains(verticals, "food") {
			httpx.Fail(w, r, httpx.Forbidden("the food vertical is not enabled for this tenant"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Lot is a traceability lot.
type Lot struct {
	ID          uuid.UUID      `json:"id" db:"id"`
	TLC         string         `json:"tlc" db:"tlc"`
	TLCSource   string         `json:"tlcSource" db:"tlc_source"`
	ProductID   *uuid.UUID     `json:"productId,omitempty" db:"product_id"`
	GTIN        *string        `json:"gtin,omitempty" db:"gtin"`
	Description string         `json:"description" db:"description"`
	Quantity    float64        `json:"quantity" db:"quantity"`
	UOM         string         `json:"uom" db:"uom"`
	LocationGLN *string        `json:"locationGln,omitempty" db:"location_gln"`
	Origin      string         `json:"origin" db:"origin"`
	HarvestDate *time.Time     `json:"harvestDate,omitempty" db:"harvest_date"`
	PackDate    *time.Time     `json:"packDate,omitempty" db:"pack_date"`
	PlotIDs     []uuid.UUID    `json:"plotIds" db:"plot_ids"`
	Status      string         `json:"status" db:"status"`
	KDEs        map[string]any `json:"kdes" db:"kdes"`
	CreatedAt   time.Time      `json:"createdAt" db:"created_at"`
}

const lotCols = `id, tlc, tlc_source, product_id, gtin, description, quantity, uom, location_gln, origin, harvest_date, pack_date, plot_ids, status, kdes, created_at`

func (m *Module) listLots(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Lot](r.Context(), tx, `select `+lotCols+` from food_lot where ($1='' or status=$1) order by created_at desc limit 1000`, r.URL.Query().Get("status"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Lot]{Items: items, Total: len(items)})
		}
		return err
	})
}

type lotInput struct {
	TLC         string         `json:"tlc"`
	TLCSource   string         `json:"tlcSource"`
	ProductID   *uuid.UUID     `json:"productId"`
	Description string         `json:"description"`
	Quantity    float64        `json:"quantity"`
	UOM         string         `json:"uom"`
	SiteID      *uuid.UUID     `json:"siteId"`
	LocationGLN string         `json:"locationGln"`
	Origin      string         `json:"origin"`
	HarvestDate string         `json:"harvestDate"`
	PackDate    string         `json:"packDate"`
	PlotIDs     []uuid.UUID    `json:"plotIds"`
	KDEs        map[string]any `json:"kdes"`
}

func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, httpx.BadRequest("dates must be YYYY-MM-DD")
	}
	return &t, nil
}

// CreateLot assigns a traceability lot code (21 CFR 1.1320) inside tx.
func (m *Module) CreateLot(ctx context.Context, tx pgx.Tx, in lotInput) (Lot, error) {
	in.TLC = strings.TrimSpace(in.TLC)
	if in.TLC == "" || len(in.TLC) > 64 {
		return Lot{}, httpx.BadRequest("tlc is required (max 64 characters)")
	}
	if in.Quantity < 0 {
		return Lot{}, httpx.BadRequest("quantity must be ≥ 0")
	}
	if in.UOM == "" {
		in.UOM = "EA"
	}
	if in.Origin == "" {
		in.Origin = "initially_packed"
	}
	switch in.Origin {
	case "harvested", "initially_packed", "first_land_based_receiving", "transformed", "received":
	default:
		return Lot{}, httpx.BadRequest("origin must be harvested, initially_packed, first_land_based_receiving, transformed or received")
	}
	hd, err := parseDate(in.HarvestDate)
	if err != nil {
		return Lot{}, err
	}
	pd, err := parseDate(in.PackDate)
	if err != nil {
		return Lot{}, err
	}
	var gtin *string
	if in.ProductID != nil {
		var g, name string
		if err := tx.QueryRow(ctx, `select gtin, name from product where id=$1`, *in.ProductID).Scan(&g, &name); err != nil {
			return Lot{}, httpx.BadRequest("unknown productId")
		}
		gtin = &g
		if in.Description == "" {
			in.Description = name
		}
	}
	if in.Description == "" {
		return Lot{}, httpx.BadRequest("description (or productId) is required")
	}
	if in.SiteID != nil && in.LocationGLN == "" {
		_ = tx.QueryRow(ctx, `select gln from site where id=$1`, *in.SiteID).Scan(&in.LocationGLN)
	}
	if in.LocationGLN != "" && !gs1.ValidGLN(in.LocationGLN) {
		return Lot{}, httpx.BadRequest("invalid location GLN")
	}
	if in.TLCSource == "" {
		in.TLCSource = in.LocationGLN
	}
	if in.PlotIDs == nil {
		in.PlotIDs = []uuid.UUID{}
	}
	if in.KDEs == nil {
		in.KDEs = map[string]any{}
	}
	l, err := db.One[Lot](ctx, tx, `insert into food_lot (id, tenant_id, tlc, tlc_source, product_id, gtin, description, quantity, uom, location_gln, origin, harvest_date, pack_date, plot_ids, kdes)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,nullif($9,''),$10,$11,$12,$13,$14) returning `+lotCols,
		ids.New(), in.TLC, in.TLCSource, in.ProductID, gtin, in.Description, in.Quantity, in.UOM, in.LocationGLN, in.Origin, hd, pd, in.PlotIDs, in.KDEs)
	if errors.Is(err, db.ErrConflict) {
		return l, httpx.Conflict("a lot with this TLC already exists")
	}
	if err != nil {
		return l, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "food.lot.create", Entity: "food_lot", EntityID: l.TLC, After: l})
	return l, err
}

func (m *Module) createLot(w http.ResponseWriter, r *http.Request) error {
	var in lotInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	var l Lot
	var c CTE
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		l, err = m.CreateLot(r.Context(), tx, in)
		if err != nil {
			return err
		}
		// The CTE that assigns the TLC (harvest or initial packing).
		cteType := map[string]string{"harvested": "harvesting", "initially_packed": "initial_packing", "first_land_based_receiving": "first_land_based_receiving"}[l.Origin]
		if cteType != "" {
			c, err = m.insertCTE(r.Context(), tx, cteInput{Type: cteType, TLC: l.TLC, Quantity: l.Quantity, UOM: l.UOM, LocationGLN: deref(l.LocationGLN), KDEs: l.KDEs})
		}
		return err
	})
	if err != nil {
		return err
	}
	if c.ID != uuid.Nil {
		m.mirror(r.Context(), c, deref(l.GTIN), nil)
	}
	httpx.JSON(w, http.StatusCreated, l)
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CTE is a critical tracking event with its KDEs.
type CTE struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	Type         string         `json:"type" db:"type"`
	TLC          string         `json:"tlc" db:"tlc"`
	Quantity     float64        `json:"quantity" db:"quantity"`
	UOM          string         `json:"uom" db:"uom"`
	EventTime    time.Time      `json:"eventTime" db:"event_time"`
	LocationGLN  string         `json:"locationGln" db:"location_gln"`
	LocationName string         `json:"locationName" db:"location_name"`
	ShipFrom     string         `json:"shipFrom" db:"ship_from"`
	ShipTo       string         `json:"shipTo" db:"ship_to"`
	ShipToName   string         `json:"shipToName" db:"ship_to_name"`
	RefDocType   string         `json:"refDocType" db:"ref_doc_type"`
	RefDocNumber string         `json:"refDocNumber" db:"ref_doc_number"`
	KDEs         map[string]any `json:"kdes" db:"kdes"`
	CreatedAt    time.Time      `json:"createdAt" db:"created_at"`
}

const cteCols = `id, type, tlc, quantity, uom, event_time, location_gln, location_name, ship_from, ship_to, ship_to_name, ref_doc_type, ref_doc_number, kdes, created_at`

type cteInput struct {
	Type         string         `json:"type"`
	TLC          string         `json:"tlc"`
	Quantity     float64        `json:"quantity"`
	UOM          string         `json:"uom"`
	EventTime    *time.Time     `json:"eventTime"`
	LocationGLN  string         `json:"locationGln"`
	LocationName string         `json:"locationName"`
	ShipFrom     string         `json:"shipFrom"`
	ShipTo       string         `json:"shipTo"`
	ShipToName   string         `json:"shipToName"`
	RefDocType   string         `json:"refDocType"`
	RefDocNumber string         `json:"refDocNumber"`
	KDEs         map[string]any `json:"kdes"`
}

// requiredKDEs lists FSMA 204 KDEs that must be present per CTE type (21 CFR 1.1325–1.1350).
// Location, date, TLC, quantity and UOM are columns; these are the additional ones.
var requiredKDEs = map[string][]string{
	"shipping":  {"refDocNumber", "shipTo"},
	"receiving": {"refDocNumber", "shipFrom"},
}

func (m *Module) insertCTE(ctx context.Context, tx pgx.Tx, in cteInput) (CTE, error) {
	if in.EventTime == nil {
		now := time.Now().UTC()
		in.EventTime = &now
	}
	if in.UOM == "" {
		in.UOM = "EA"
	}
	if in.KDEs == nil {
		in.KDEs = map[string]any{}
	}
	for _, k := range requiredKDEs[in.Type] {
		if (k == "refDocNumber" && in.RefDocNumber == "") || (k == "shipTo" && in.ShipTo == "" && in.ShipToName == "") || (k == "shipFrom" && in.ShipFrom == "") {
			return CTE{}, httpx.BadRequest(fmt.Sprintf("%s CTE requires %s (FSMA 204 KDE)", in.Type, k))
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from food_lot where tlc=$1`, in.TLC).Scan(&n); err != nil {
		return CTE{}, err
	}
	if n == 0 && in.Type != "receiving" {
		return CTE{}, httpx.BadRequest("unknown traceability lot code " + in.TLC)
	}
	c, err := db.One[CTE](ctx, tx, `insert into food_cte (id, tenant_id, type, tlc, quantity, uom, event_time, location_gln, location_name, ship_from, ship_to, ship_to_name, ref_doc_type, ref_doc_number, kdes)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) returning `+cteCols,
		ids.New(), in.Type, in.TLC, in.Quantity, in.UOM, *in.EventTime, in.LocationGLN, in.LocationName, in.ShipFrom, in.ShipTo, in.ShipToName, in.RefDocType, in.RefDocNumber, in.KDEs)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return c, err
		}
		return c, httpx.BadRequest("invalid CTE: " + err.Error())
	}
	if in.Type == "shipping" {
		if _, err := tx.Exec(ctx, `update food_lot set status='shipped' where tlc=$1 and status='active' and quantity <= (select coalesce(sum(quantity),0) from food_cte where tlc=$1 and type='shipping')`, in.TLC); err != nil {
			return c, err
		}
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "food.cte." + in.Type, Entity: "food_lot", EntityID: in.TLC, After: c})
	return c, err
}

// mirror records the CTE as a class-level EPCIS event (best effort; FSMA records are the source of truth).
func (m *Module) mirror(ctx context.Context, c CTE, gtin string, inputs []string) {
	step := map[string]string{"harvesting": "commissioning", "initial_packing": "commissioning", "first_land_based_receiving": "receiving",
		"cooling": "storing", "shipping": "shipping", "receiving": "receiving"}[c.Type]
	q := c.Quantity
	class := func(tlc string) string {
		if gtin != "" {
			return gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: gtin, Lot: tlc})
		}
		return "urn:openspoor:tlc:" + tlc
	}
	e := lib.Event{EventTime: c.EventTime, EventTimeZoneOffset: "+00:00", Extensions: map[string]any{"fsma:cte": c.Type}}
	if c.Type == "transformation" {
		e.Type = lib.TransformationEvent
		for _, in := range inputs {
			e.InputQuantityList = append(e.InputQuantityList, lib.QuantityElement{EPCClass: class(in)})
		}
		e.OutputQuantityList = []lib.QuantityElement{{EPCClass: class(c.TLC), Quantity: &q, UOM: c.UOM}}
	} else {
		e.Type, e.Action, e.BizStep = lib.ObjectEvent, "OBSERVE", step
		if step == "commissioning" {
			e.Action = "ADD"
		}
		e.QuantityList = []lib.QuantityElement{{EPCClass: class(c.TLC), Quantity: &q, UOM: c.UOM}}
	}
	if c.LocationGLN != "" {
		e.BizLocation = &lib.Location{ID: gs1.DefaultResolver + "/414/" + c.LocationGLN}
	}
	if c.ShipTo != "" && gs1.ValidGLN(c.ShipTo) {
		e.DestinationList = []lib.Destination{{Type: "location", Destination: gs1.DefaultResolver + "/414/" + c.ShipTo}}
	}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{e}))
	if _, err := m.EPCIS.Capture(ctx, "application/ld+json", b, "food-cte"); err != nil {
		var ce *epcis.CaptureError
		if errors.As(err, &ce) {
			slog.WarnContext(ctx, "food CTE not mirrored to EPCIS", "tlc", c.TLC, "type", c.Type, "errors", ce.Capture.Errors)
		} else {
			slog.WarnContext(ctx, "food CTE not mirrored to EPCIS", "tlc", c.TLC, "type", c.Type, "err", err)
		}
	}
}

func (m *Module) gtinOf(ctx context.Context, tx pgx.Tx, tlc string) string {
	var g *string
	_ = tx.QueryRow(ctx, `select gtin from food_lot where tlc=$1`, tlc).Scan(&g)
	return deref(g)
}

func (m *Module) createCTE(w http.ResponseWriter, r *http.Request) error {
	var in cteInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Type == "transformation" {
		return httpx.BadRequest("use POST /food/transformations for transformation CTEs")
	}
	var c CTE
	var gtin string
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		if in.Type == "receiving" {
			var n int
			_ = tx.QueryRow(r.Context(), `select count(*) from food_lot where tlc=$1`, in.TLC).Scan(&n)
			if n == 0 {
				desc, _ := in.KDEs["productDescription"].(string)
				if desc == "" {
					desc = "Received lot " + in.TLC
				}
				if _, err := m.CreateLot(r.Context(), tx, lotInput{TLC: in.TLC, TLCSource: in.ShipFrom, Description: desc, Quantity: in.Quantity, UOM: in.UOM,
					LocationGLN: in.LocationGLN, Origin: "received", KDEs: in.KDEs}); err != nil {
					return err
				}
			}
		}
		var err error
		c, err = m.insertCTE(r.Context(), tx, in)
		gtin = m.gtinOf(r.Context(), tx, in.TLC)
		return err
	})
	if err != nil {
		return err
	}
	m.mirror(r.Context(), c, gtin, nil)
	httpx.JSON(w, http.StatusCreated, c)
	return nil
}

func (m *Module) listCTEs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[CTE](r.Context(), tx, `select `+cteCols+` from food_cte where ($1='' or tlc=$1) and ($2='' or type=$2) order by event_time desc limit 2000`, q.Get("tlc"), q.Get("type"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[CTE]{Items: items, Total: len(items)})
		}
		return err
	})
}

// transform records a transformation CTE: input lots are consumed (fully or partly) into a new output lot.
func (m *Module) transform(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Inputs []struct {
			TLC      string  `json:"tlc"`
			Quantity float64 `json:"quantity"`
		} `json:"inputs"`
		Output struct {
			TLC         string     `json:"tlc"`
			ProductID   *uuid.UUID `json:"productId"`
			Description string     `json:"description"`
			Quantity    float64    `json:"quantity"`
			UOM         string     `json:"uom"`
			LocationGLN string     `json:"locationGln"`
		} `json:"output"`
		EventTime *time.Time     `json:"eventTime"`
		KDEs      map[string]any `json:"kdes"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Inputs) == 0 || in.Output.TLC == "" {
		return httpx.BadRequest("at least one input lot and an output TLC are required")
	}
	var c CTE
	var gtin string
	var inputs []string
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		for _, x := range in.Inputs {
			var avail float64
			if err := tx.QueryRow(r.Context(), `select quantity from food_lot where tlc=$1 and status in ('active','shipped')`, x.TLC).Scan(&avail); err != nil {
				return httpx.BadRequest("unknown or unavailable input lot " + x.TLC)
			}
			if x.Quantity > avail {
				return httpx.BadRequest(fmt.Sprintf("input %s: quantity %.3f exceeds lot quantity %.3f", x.TLC, x.Quantity, avail))
			}
			inputs = append(inputs, x.TLC)
		}
		if _, err := m.CreateLot(r.Context(), tx, lotInput{TLC: in.Output.TLC, ProductID: in.Output.ProductID, Description: in.Output.Description, Quantity: in.Output.Quantity,
			UOM: in.Output.UOM, LocationGLN: in.Output.LocationGLN, Origin: "transformed", KDEs: in.KDEs}); err != nil {
			return err
		}
		kdes := map[string]any{"inputs": in.Inputs}
		for k, v := range in.KDEs {
			kdes[k] = v
		}
		var err error
		c, err = m.insertCTE(r.Context(), tx, cteInput{Type: "transformation", TLC: in.Output.TLC, Quantity: in.Output.Quantity, UOM: in.Output.UOM,
			EventTime: in.EventTime, LocationGLN: in.Output.LocationGLN, KDEs: kdes})
		if err != nil {
			return err
		}
		for _, x := range in.Inputs {
			if _, err := tx.Exec(r.Context(), `insert into food_lineage (tenant_id, input_tlc, output_tlc, quantity, cte_id) values (app_tenant(), $1,$2,$3,$4)`, x.TLC, in.Output.TLC, x.Quantity, c.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `update food_lot set quantity = greatest(quantity - $2, 0), status = case when quantity - $2 <= 0 then 'consumed' else status end where tlc=$1`, x.TLC, x.Quantity); err != nil {
				return err
			}
		}
		gtin = m.gtinOf(r.Context(), tx, in.Output.TLC)
		return nil
	})
	if err != nil {
		return err
	}
	m.mirror(r.Context(), c, gtin, inputs)
	httpx.JSON(w, http.StatusCreated, c)
	return nil
}

// lineage walks transformations: direction "forward" (inputs → outputs) or "back".
func lineage(ctx context.Context, tx pgx.Tx, start []string, forward bool) ([]string, error) {
	q := `with recursive l(tlc, d) as (select unnest($1::text[]), 0 union select f.output_tlc, l.d+1 from food_lineage f join l on f.input_tlc = l.tlc where l.d < 20) select distinct tlc from l`
	if !forward {
		q = `with recursive l(tlc, d) as (select unnest($1::text[]), 0 union select f.input_tlc, l.d+1 from food_lineage f join l on f.output_tlc = l.tlc where l.d < 20) select distinct tlc from l`
	}
	rows, err := tx.Query(ctx, q, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out, rows.Err()
}

func (m *Module) traceLot(w http.ResponseWriter, r *http.Request) error {
	tlc := chi.URLParam(r, "tlc")
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		back, err := lineage(r.Context(), tx, []string{tlc}, false)
		if err != nil {
			return err
		}
		fwd, err := lineage(r.Context(), tx, []string{tlc}, true)
		if err != nil {
			return err
		}
		ctes, err := db.List[CTE](r.Context(), tx, `select `+cteCols+` from food_cte where tlc = any($1) order by event_time`, append(back, fwd...))
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"tlc": tlc, "sources": back, "derived": fwd, "events": ctes})
		return nil
	})
}

// --- FSMA 204 sortable spreadsheet ---

// fsmaColumns follow the KDEs of 21 CFR 1.1325–1.1350.
// TODO(spec): align header names and order with FDA's published sortable spreadsheet template. The
// template is not available in this repository, so nothing here is claimed to match it; the open
// points are listed in docs/OPEN_QUESTIONS.md ("FSMA 204 sortable spreadsheet").
var fsmaColumns = []string{"Traceability Lot Code", "Traceability Lot Code Source", "Product Description", "Quantity", "Unit of Measure",
	"Critical Tracking Event", "Event Date", "Event Time (UTC)", "Location GLN", "Location Description", "Ship From", "Ship To", "Ship To Name",
	"Reference Document Type", "Reference Document Number", "Input Traceability Lot Codes", "Harvest Date", "Pack Date", "Additional KDEs"}

// FSMACSV writes the spreadsheet rows for CTEs in the window (optionally one lot and its lineage).
func FSMACSV(ctx context.Context, tx pgx.Tx, w *csv.Writer, from, to time.Time, tlc string) (int, error) {
	return fsmaCSV(ctx, tx, w, from, to, tlc, nil)
}

// fsmaCSV writes the spreadsheet; certs (when non-nil) maps a TLC to certificate references,
// added to the "Additional KDEs" JSON under "certificates". Certificates are not FSMA 204 KDEs,
// so they are only included on request.
func fsmaCSV(ctx context.Context, tx pgx.Tx, w *csv.Writer, from, to time.Time, tlc string, certs map[string][]string) (int, error) {
	tlcs := []string{}
	if tlc != "" {
		back, err := lineage(ctx, tx, []string{tlc}, false)
		if err != nil {
			return 0, err
		}
		fwd, err := lineage(ctx, tx, []string{tlc}, true)
		if err != nil {
			return 0, err
		}
		tlcs = append(back, fwd...)
	}
	rows, err := tx.Query(ctx, `select c.tlc, coalesce(l.tlc_source,''), coalesce(l.description,''), c.quantity, c.uom, c.type, c.event_time, c.location_gln, c.location_name,
			c.ship_from, c.ship_to, c.ship_to_name, c.ref_doc_type, c.ref_doc_number, c.kdes,
			coalesce(to_char(l.harvest_date,'YYYY-MM-DD'),''), coalesce(to_char(l.pack_date,'YYYY-MM-DD'),'')
		from food_cte c left join food_lot l on l.tenant_id=c.tenant_id and l.tlc=c.tlc
		where c.event_time >= $1 and c.event_time < $2 and (cardinality($3::text[]) = 0 or c.tlc = any($3))
		order by c.event_time, c.tlc`, from, to, tlcs)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	_ = w.Write(fsmaColumns)
	n := 0
	for rows.Next() {
		var t, src, desc, uom, typ, gln, lname, from, to, toName, rdt, rdn, hd, pd string
		var q float64
		var at time.Time
		var kdes map[string]any
		if err := rows.Scan(&t, &src, &desc, &q, &uom, &typ, &at, &gln, &lname, &from, &to, &toName, &rdt, &rdn, &kdes, &hd, &pd); err != nil {
			return n, err
		}
		inputs := ""
		if in, ok := kdes["inputs"].([]any); ok {
			var parts []string
			for _, x := range in {
				if mm, ok := x.(map[string]any); ok {
					parts = append(parts, fmt.Sprint(mm["tlc"]))
				}
			}
			inputs = strings.Join(parts, ";")
			delete(kdes, "inputs")
		}
		if refs := certs[t]; len(refs) > 0 {
			kdes["certificates"] = refs
		}
		extra := ""
		if len(kdes) > 0 {
			b, _ := json.Marshal(kdes)
			extra = string(b)
		}
		_ = w.Write([]string{t, src, desc, trimFloat(q), uom, typ, at.UTC().Format("2006-01-02"), at.UTC().Format("15:04:05"), gln, lname, from, to, toName, rdt, rdn, inputs, hd, pd, extra})
		n++
	}
	w.Flush()
	return n, rows.Err()
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.6f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

func (m *Module) fsmaExport(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	from, to := time.Unix(0, 0), time.Now().Add(24*time.Hour)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return httpx.BadRequest("from must be YYYY-MM-DD")
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return httpx.BadRequest("to must be YYYY-MM-DD")
		}
		to = t.Add(24 * time.Hour)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="fsma204-`+time.Now().UTC().Format("20060102-1504")+`.csv"`)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var certs map[string][]string
		if q.Get("certificates") == "true" {
			var err error
			if certs, err = m.certificateRefs(r.Context(), tx, from, to); err != nil {
				return err
			}
		}
		n, err := fsmaCSV(r.Context(), tx, csv.NewWriter(w), from, to, q.Get("tlc"), certs)
		if err != nil {
			return err
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "food.fsma204.export", Entity: "food_cte", EntityID: q.Get("tlc"), After: map[string]any{"rows": n}})
		return err
	})
}

// --- plots ---

// Plot is a production plot of land.
type Plot struct {
	ID                uuid.UUID       `json:"id" db:"id"`
	Name              string          `json:"name" db:"name"`
	ProducerName      string          `json:"producerName" db:"producer_name"`
	Country           string          `json:"country" db:"country"`
	Commodity         string          `json:"commodity" db:"commodity"`
	AreaHa            float64         `json:"areaHa" db:"area_ha"`
	GeoJSON           json.RawMessage `json:"geojson" db:"geojson"`
	DeforestationFree *bool           `json:"deforestationFree,omitempty" db:"deforestation_free"`
	CreatedAt         time.Time       `json:"createdAt" db:"created_at"`
}

const plotCols = `id, name, producer_name, country, commodity, area_ha, geojson, deforestation_free, created_at`

// EUDR commodities (Regulation 2023/1115, Annex I).
var eudrCommodities = map[string]bool{"cattle": true, "cocoa": true, "coffee": true, "oil_palm": true, "rubber": true, "soya": true, "wood": true}

type geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// ParseGeometry accepts a GeoJSON Geometry or Feature (Point or Polygon, WGS84) and returns its area in hectares.
func ParseGeometry(raw json.RawMessage) (string, float64, error) {
	var g geometry
	if err := json.Unmarshal(raw, &g); err != nil {
		return "", 0, errors.New("invalid GeoJSON")
	}
	if g.Type == "Feature" {
		var f struct {
			Geometry json.RawMessage `json:"geometry"`
		}
		_ = json.Unmarshal(raw, &f)
		return ParseGeometry(f.Geometry)
	}
	switch g.Type {
	case "Point":
		var p []float64
		if err := json.Unmarshal(g.Coordinates, &p); err != nil || len(p) < 2 || !validLonLat(p[0], p[1]) {
			return "", 0, errors.New("point must be [longitude, latitude] in WGS84")
		}
		return "Point", 0, nil
	case "Polygon":
		var rings [][][]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil || len(rings) == 0 || len(rings[0]) < 4 {
			return "", 0, errors.New("polygon needs a closed outer ring of at least 4 positions")
		}
		ring := rings[0]
		first, last := ring[0], ring[len(ring)-1]
		if len(first) < 2 || len(last) < 2 || first[0] != last[0] || first[1] != last[1] {
			return "", 0, errors.New("polygon ring must be closed (first position = last position)")
		}
		for _, p := range ring {
			if len(p) < 2 || !validLonLat(p[0], p[1]) {
				return "", 0, errors.New("positions must be [longitude, latitude] in WGS84")
			}
		}
		return "Polygon", ringAreaHa(ring), nil
	}
	return "", 0, errors.New("geometry must be a Point or Polygon")
}

func validLonLat(lon, lat float64) bool { return lon >= -180 && lon <= 180 && lat >= -90 && lat <= 90 }

// ringAreaHa computes a geodesic polygon area on a spherical Earth (Chamberlain & Duquette).
func ringAreaHa(ring [][]float64) float64 {
	const r = 6378137.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	var sum float64
	for i := 0; i < len(ring)-1; i++ {
		p1, p2 := ring[i], ring[i+1]
		sum += rad(p2[0]-p1[0]) * (2 + math.Sin(rad(p1[1])) + math.Sin(rad(p2[1])))
	}
	return math.Abs(sum*r*r/2) / 10000
}

func (m *Module) listPlots(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Plot](r.Context(), tx, `select `+plotCols+` from farm_plot order by name`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Plot]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createPlot(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name              string          `json:"name"`
		ProducerName      string          `json:"producerName"`
		Country           string          `json:"country"`
		Commodity         string          `json:"commodity"`
		GeoJSON           json.RawMessage `json:"geojson"`
		AreaHa            float64         `json:"areaHa"`
		DeforestationFree *bool           `json:"deforestationFree"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Name == "" || len(in.Country) != 2 || in.Commodity == "" {
		return httpx.BadRequest("name, ISO country code and commodity are required")
	}
	kind, area, err := ParseGeometry(in.GeoJSON)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if kind == "Point" {
		area = in.AreaHa
		// Regulation 2023/1115 Art. 2(28): plots over 4 ha (other than cattle) need a polygon.
		if area > 4 && in.Commodity != "cattle" {
			return httpx.BadRequest("plots larger than 4 hectares must be described with a polygon (EUDR Art. 2(28))")
		}
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[Plot](r.Context(), tx, `insert into farm_plot (id, tenant_id, name, producer_name, country, commodity, area_ha, geojson, deforestation_free)
			values ($1, app_tenant(), $2,$3,upper($4),$5,$6,$7,$8) returning `+plotCols,
			ids.New(), in.Name, in.ProducerName, in.Country, strings.ToLower(in.Commodity), math.Round(area*10000)/10000, in.GeoJSON, in.DeforestationFree)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "food.plot.create", Entity: "farm_plot", EntityID: p.ID.String(), After: p}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, p)
		return nil
	})
}
