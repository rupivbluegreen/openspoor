// Package verify serves the public, unauthenticated surfaces: pharmacy/consumer
// "scan and verify" and the EPCIS Playground (validate, convert, visualize). Both are
// rate-limited per client IP.
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/time/rate"

	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Module is the public verification and playground surface.
type Module struct {
	kernel.Deps
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// New creates the module.
func New(d kernel.Deps) *Module { return &Module{Deps: d, limiters: map[string]*rate.Limiter{}} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "verify" }

// Routes implements kernel.Module (no authenticated routes).
func (m *Module) Routes(chi.Router) {}

// Public implements kernel.PublicRoutes.
func (m *Module) Public(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(m.limit)
		r.Post("/verify/scan", httpx.Handler(m.scan))
		r.Post("/playground/validate", httpx.Handler(m.validate))
		r.Post("/playground/convert", httpx.Handler(m.convert))
		r.Post("/playground/visualize", httpx.Handler(m.visualize))
	})
}

func (m *Module) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		m.mu.Lock()
		l, ok := m.limiters[host]
		if !ok {
			if len(m.limiters) > 100_000 {
				m.limiters = map[string]*rate.Limiter{}
			}
			l = rate.NewLimiter(rate.Limit(5), 30) // 5 req/s, burst 30
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

// ScanResult is shown to a pharmacist or consumer.
type ScanResult struct {
	Verified bool           `json:"verified"`
	Status   string         `json:"status"` // verified | not_found | unknown_product | recalled | expired | decommissioned | suspect
	Message  string         `json:"message"`
	Product  map[string]any `json:"product,omitempty"`
	Pack     gs1.Pack       `json:"pack"`
	Checked  time.Time      `json:"checkedAt"`
}

func (m *Module) scan(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	pack, err := serialization.ParseCode(in.Code)
	if err != nil {
		return httpx.BadRequest("could not read the code: " + err.Error())
	}
	res, err := m.Scan(r.Context(), pack)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// Scan verifies one pack against the manufacturer's repository on this node.
func (m *Module) Scan(ctx context.Context, pack gs1.Pack) (ScanResult, error) {
	res := ScanResult{Pack: pack, Checked: time.Now().UTC()}
	// Find the brand owner: the tenant that serializes this GTIN (has a pool), else any with the product.
	var tenant uuid.UUID
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select p.tenant_id from product p left join serial_pool s on s.tenant_id=p.tenant_id and s.gtin=p.gtin
			where p.gtin=$1 order by (s.id is null), p.created_at limit 1`, pack.GTIN).Scan(&tenant)
	})
	if err != nil {
		res.Status, res.Message = "unknown_product", "This product is not registered on this network. Do not dispense; check with the supplier."
		return res, nil
	}
	err = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		p, err := masterdata.ByGTIN(ctx, tx, pack.GTIN)
		if err != nil {
			return err
		}
		res.Product = map[string]any{"name": p.Name, "gtin": p.GTIN, "strength": p.Strength, "dosageForm": p.DosageForm}
		if pack.Serial == "" {
			res.Status, res.Message = "suspect", "The code has no serial number."
			return nil
		}
		st, err := trace.Lookup(ctx, tx, gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: pack.GTIN, Serial: pack.Serial}))
		if err != nil {
			res.Status, res.Message = "not_found", "This serial number was never issued by the manufacturer. Quarantine the pack and report it."
			return raise(ctx, tx, res)
		}
		switch {
		case pack.Lot != "" && st.Lot != nil && *st.Lot != pack.Lot:
			res.Status, res.Message = "suspect", "Batch on the pack does not match the manufacturer's record."
		case pack.Expiry != "" && st.Expiry != nil && !sameExpiry(pack.Expiry, *st.Expiry):
			res.Status, res.Message = "suspect", "Expiry on the pack does not match the manufacturer's record."
		case st.Status == "recalled":
			res.Status, res.Message = "recalled", "This batch is recalled. Do not dispense."
		case st.Status == "decommissioned" || st.Status == "destroyed" || st.Status == "dispensed" || st.Status == "stolen":
			res.Status, res.Message = "decommissioned", "This pack was already "+st.Status+". It may be a duplicate; quarantine it."
		case st.Expiry != nil && st.Expiry.Before(time.Now()):
			res.Status, res.Message = "expired", "This pack is expired."
		default:
			res.Verified, res.Status, res.Message = true, "verified", "Genuine pack, verified with the manufacturer's record."
		}
		if !res.Verified {
			return raise(ctx, tx, res)
		}
		return nil
	})
	return res, err
}

func sameExpiry(yymmdd string, d time.Time) bool {
	t, err := gs1.ExpiryDate(yymmdd)
	return err == nil && t.Year() == d.Year() && t.YearDay() == d.YearDay()
}

// raise records a suspect-product alert for the brand owner (deduplicated per pack).
func raise(ctx context.Context, tx pgx.Tx, res ScanResult) error {
	_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
		values ($1, app_tenant(), 'verify.'||$2, 'error', $3, $4, 'serial', $5, $6) on conflict do nothing`,
		ids.New(), res.Status, "Public scan flagged "+res.Status+" pack "+res.Pack.Serial, res, res.Pack.GTIN+"/"+res.Pack.Serial, "scan:"+res.Status+":"+res.Pack.GTIN+"/"+res.Pack.Serial)
	return err
}

// --- Playground ---

type docInput struct {
	Document string `json:"document"`
	To       string `json:"to"`
}

const playgroundMax = 5 << 20

func parseInput(r *http.Request) (*lib.Document, string, docInput, error) {
	var in docInput
	if err := httpx.Decode(r, &in); err != nil {
		return nil, "", in, err
	}
	if len(in.Document) > playgroundMax {
		return nil, "", in, httpx.BadRequest("the Playground accepts documents up to 5 MiB")
	}
	d, f, err := lib.Parse("", []byte(in.Document))
	if err != nil {
		return nil, f, in, httpx.Unprocessable("could not parse document", []lib.FieldError{{Index: -1, Field: "document", Message: err.Error()}})
	}
	return d, f, in, nil
}

func (m *Module) validate(w http.ResponseWriter, r *http.Request) error {
	d, f, _, err := parseInput(r)
	if err != nil {
		var he *httpx.Error
		if errorsAs(err, &he) && he.Status == http.StatusUnprocessableEntity {
			httpx.JSON(w, http.StatusOK, map[string]any{"valid": false, "format": f, "eventCount": 0, "errors": he.Extra})
			return nil
		}
		return err
	}
	errs := lib.ValidateDocument(d)
	if errs == nil {
		errs = []lib.FieldError{}
	}
	counts := map[string]int{}
	for _, e := range d.Body.EventList {
		counts[e.Type]++
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": len(errs) == 0, "format": f, "schemaVersion": d.SchemaVersion, "eventCount": len(d.Body.EventList),
		"eventTypes": counts, "errors": errs})
	return nil
}

func (m *Module) convert(w http.ResponseWriter, r *http.Request) error {
	d, _, in, err := parseInput(r)
	if err != nil {
		return err
	}
	lib.ValidateDocument(d) // normalizes CBV values
	var out []byte
	switch strings.ToLower(in.To) {
	case "xml":
		out, err = lib.WriteXML(d)
	case "json", "jsonld", "":
		d.Context = nil
		out, err = lib.WriteJSON(d)
		if err == nil {
			var v any
			_ = json.Unmarshal(out, &v)
			out, _ = json.MarshalIndent(v, "", "  ")
		}
	default:
		return httpx.BadRequest("to must be json or xml")
	}
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"document": string(out), "format": strings.ToLower(in.To)})
	return nil
}

type gnode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // item | container | location | lot
}

type gedge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"` // contains | at | transformed
	Event int    `json:"event"`
}

func label(id string) (string, string) {
	c := lib.Canonical(id)
	g, s, l := lib.Key(c)
	switch {
	case g != "" && s != "":
		return "GTIN " + g + " · " + s, "item"
	case g != "" && l != "":
		return "GTIN " + g + " · lot " + l, "lot"
	case strings.Contains(c, "/00/"):
		return "SSCC " + c[strings.LastIndex(c, "/")+1:], "container"
	case strings.Contains(c, "/414/"):
		return "GLN " + strings.TrimPrefix(c[strings.Index(c, "/414/")+5:], "/"), "location"
	}
	return id, "item"
}

func (m *Module) visualize(w http.ResponseWriter, r *http.Request) error {
	d, _, _, err := parseInput(r)
	if err != nil {
		return err
	}
	lib.ValidateDocument(d)
	nodes := map[string]gnode{}
	var edges []gedge
	add := func(id string) string {
		c := lib.Canonical(id)
		if _, ok := nodes[c]; !ok {
			l, k := label(id)
			nodes[c] = gnode{ID: c, Label: l, Kind: k}
		}
		return c
	}
	type tl struct {
		Index       int       `json:"index"`
		Type        string    `json:"type"`
		Time        time.Time `json:"time"`
		BizStep     string    `json:"bizStep,omitempty"`
		Disposition string    `json:"disposition,omitempty"`
		Action      string    `json:"action,omitempty"`
		Objects     int       `json:"objects"`
		Location    string    `json:"location,omitempty"`
	}
	var timeline []tl
	for i, e := range d.Body.EventList {
		t := tl{Index: i, Type: e.Type, Time: e.EventTime, BizStep: e.BizStep, Disposition: e.Disposition, Action: e.Action, Objects: len(e.AllEPCs())}
		where := ""
		if e.BizLocation != nil {
			where = add(e.BizLocation.ID)
			t.Location = nodes[where].Label
		} else if e.ReadPoint != nil {
			t.Location = e.ReadPoint.ID
		}
		timeline = append(timeline, t)
		switch e.Type {
		case lib.AggregationEvent, lib.AssociationEvent:
			if e.ParentID != "" {
				p := add(e.ParentID)
				for _, c := range e.ChildEPCs {
					edges = append(edges, gedge{From: p, To: add(c), Kind: "contains", Event: i})
				}
			}
		case lib.TransformationEvent:
			var ins, outs []string
			for _, x := range e.InputEPCList {
				ins = append(ins, add(x))
			}
			for _, q := range e.InputQuantityList {
				ins = append(ins, add(q.EPCClass))
			}
			for _, x := range e.OutputEPCList {
				outs = append(outs, add(x))
			}
			for _, q := range e.OutputQuantityList {
				outs = append(outs, add(q.EPCClass))
			}
			for _, a := range ins {
				for _, b := range outs {
					edges = append(edges, gedge{From: a, To: b, Kind: "transformed", Event: i})
				}
			}
		default:
			for _, x := range e.EPCList {
				id := add(x)
				if where != "" {
					edges = append(edges, gedge{From: id, To: where, Kind: "at", Event: i})
				}
			}
			for _, q := range e.QuantityList {
				add(q.EPCClass)
			}
		}
		if len(nodes) > 2000 {
			break
		}
	}
	list := make([]gnode, 0, len(nodes))
	for _, n := range nodes {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	sort.SliceStable(timeline, func(i, j int) bool { return timeline[i].Time.Before(timeline[j].Time) })
	if edges == nil {
		edges = []gedge{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"nodes": list, "edges": edges, "timeline": timeline})
	return nil
}

func errorsAs(err error, target **httpx.Error) bool { return errors.As(err, target) }
