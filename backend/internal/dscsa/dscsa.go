// Package dscsa implements US DSCSA interoperability: Transaction Information and
// Transaction Statement (TI/TS) EPCIS documents built from shipping data, partner exchange
// over HTTPS (AS2 behind an interface, ADR-007), reconciliation of inbound TI/TS with the ASN,
// Verification Router Service (VRS) requester and responder (GS1 Lightweight Messaging),
// a VRS lookup directory and the six-year retention policy.
package dscsa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// RetentionYears is the DSCSA record retention period (FD&C Act 582: six years).
const RetentionYears = 6

// Module is the DSCSA bounded context.
type Module struct {
	kernel.Deps
	EPCIS      *epcis.Module
	Transports map[string]Transport
	Client     *http.Client
}

// New creates the module.
func New(d kernel.Deps, ep *epcis.Module) *Module {
	m := &Module{Deps: d, EPCIS: ep, Client: &http.Client{Timeout: 10 * time.Second}}
	m.Transports = map[string]Transport{"https": &HTTPSTransport{Client: m.Client}, "as2": AS2Transport{}, "local": &localTransport{m: m}}
	return m
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "dscsa" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Route("/shipments", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance, auth.RoleIntegration))
		r.Get("/", httpx.Handler(m.listShipments))
		r.Post("/", httpx.Handler(m.createShipment))
		r.Post("/inbound", httpx.Handler(m.receiveInbound))
		r.Get("/{id}", httpx.Handler(m.getShipment))
		r.Post("/{id}/tits", httpx.Handler(m.buildTITS))
		r.Post("/{id}/send", httpx.Handler(m.send))
		r.Post("/{id}/reconcile", httpx.Handler(m.reconcile))
	})
	r.Route("/vrs", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance, auth.RoleIntegration))
		r.Get("/requests", httpx.Handler(m.listVRS))
		r.Post("/requests", httpx.Handler(m.vrsRequest))
		r.Get("/directory", httpx.Handler(m.listDirectory))
		r.Post("/directory", httpx.Handler(m.putDirectory))
		r.Delete("/directory/{id}", httpx.Handler(m.deleteDirectory))
	})
	r.Get("/retention", httpx.Handler(m.listRetention))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance)).Put("/retention/{class}", httpx.Handler(m.putRetention))
}

// Public mounts the VRS responder (API-key authenticated inside the handler).
func (m *Module) Public(r chi.Router) {
	r.Post("/vrs/verify", httpx.Handler(m.vrsRespond))
}

// Schedules implements kernel.Scheduled.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: "dscsa.retention", Every: 24 * time.Hour, Run: m.enforceRetention}}
}

// Shipment is an outbound or inbound shipment.
type Shipment struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	Direction      string          `json:"direction" db:"direction"`
	PartnerID      *uuid.UUID      `json:"partnerId,omitempty" db:"partner_id"`
	PONumber       string          `json:"poNumber" db:"po_number"`
	ASNNumber      string          `json:"asnNumber" db:"asn_number"`
	ShipDate       *time.Time      `json:"shipDate,omitempty" db:"ship_date"`
	Status         string          `json:"status" db:"status"`
	EPCs           []string        `json:"epcs" db:"epcs"`
	ASN            ASN             `json:"asn" db:"asn"`
	TITS           json.RawMessage `json:"tits,omitempty" db:"tits"`
	Transport      string          `json:"transport" db:"transport"`
	Response       map[string]any  `json:"response,omitempty" db:"response"`
	Reconciliation *Reconciliation `json:"reconciliation,omitempty" db:"reconciliation"`
	CaptureID      *uuid.UUID      `json:"captureId,omitempty" db:"capture_id"`
	SentAt         *time.Time      `json:"sentAt,omitempty" db:"sent_at"`
	ReceivedAt     *time.Time      `json:"receivedAt,omitempty" db:"received_at"`
	CreatedAt      time.Time       `json:"createdAt" db:"created_at"`
}

const shipCols = `id, direction, partner_id, po_number, asn_number, ship_date, status, epcs, asn, tits, transport, response, reconciliation, capture_id, sent_at, received_at, created_at`

// ASN is an advance ship notice summary.
type ASN struct {
	Number string    `json:"number,omitempty"`
	Items  []ASNItem `json:"items,omitempty"`
}

// ASNItem is one ASN line.
type ASNItem struct {
	GTIN     string   `json:"gtin"`
	Lot      string   `json:"lot,omitempty"`
	Quantity int      `json:"quantity"`
	EPCs     []string `json:"epcs,omitempty"`
}

// Reconciliation compares TI/TS with the ASN.
type Reconciliation struct {
	Matched    bool             `json:"matched"`
	Missing    []string         `json:"missing"`
	Unexpected []string         `json:"unexpected"`
	Quantities []QuantityResult `json:"quantities"`
	CheckedAt  time.Time        `json:"checkedAt"`
}

// QuantityResult compares per-GTIN counts.
type QuantityResult struct {
	GTIN     string `json:"gtin"`
	Expected int    `json:"expected"`
	Received int    `json:"received"`
}

func (m *Module) listShipments(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Shipment](r.Context(), tx, `select `+shipCols+` from shipment where ($1='' or direction=$1) order by created_at desc limit 500`, r.URL.Query().Get("direction"))
		for i := range items {
			items[i].TITS = nil // keep lists light
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Shipment]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getShipment(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := db.One[Shipment](r.Context(), tx, `select `+shipCols+` from shipment where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, s)
		}
		return err
	})
}

func (m *Module) createShipment(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PartnerID uuid.UUID `json:"partnerId"`
		PONumber  string    `json:"poNumber"`
		ASNNumber string    `json:"asnNumber"`
		ShipDate  *string   `json:"shipDate"`
		EPCs      []string  `json:"epcs"`
		Transport string    `json:"transport"`
		ASN       ASN       `json:"asn"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.EPCs) == 0 || in.PONumber == "" {
		return httpx.BadRequest("poNumber and at least one EPC (SSCC or SGTIN) are required")
	}
	if in.Transport == "" {
		in.Transport = "https"
	}
	if _, ok := m.Transports[in.Transport]; !ok {
		return httpx.BadRequest("transport must be https or as2")
	}
	var shipDate *time.Time
	if in.ShipDate != nil && *in.ShipDate != "" {
		t, err := time.Parse("2006-01-02", *in.ShipDate)
		if err != nil {
			return httpx.BadRequest("shipDate must be YYYY-MM-DD")
		}
		shipDate = &t
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := partners.Get(r.Context(), tx, in.PartnerID); err != nil {
			return httpx.BadRequest("unknown partnerId")
		}
		in.ASN.Number = in.ASNNumber
		s, err := db.One[Shipment](r.Context(), tx, `insert into shipment (id, tenant_id, direction, partner_id, po_number, asn_number, ship_date, epcs, transport, asn)
			values ($1, app_tenant(), 'outbound', $2,$3,$4,$5,$6,$7,$8) returning `+shipCols,
			ids.New(), in.PartnerID, in.PONumber, in.ASNNumber, shipDate, lib.CanonicalAll(in.EPCs), in.Transport, in.ASN)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "shipment.create", Entity: "shipment", EntityID: s.ID.String(), After: s}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, s)
		return nil
	})
}

// --- TI/TS ---

// tree returns every descendant of the top-level identifiers with its parent.
func tree(ctx context.Context, tx pgx.Tx, top []string) (map[string][]string, map[string]trace.State, error) {
	children := map[string][]string{}
	states := map[string]trace.State{}
	rows, err := tx.Query(ctx, `with recursive t(epc, depth) as (
			select epc, 0 from serial_state where tenant_id = app_tenant() and epc = any($1)
			union all select s.epc, t.depth+1 from serial_state s join t on s.tenant_id = app_tenant() and s.parent_epc = t.epc where t.depth < 6)
		select s.epc, s.gtin, s.serial, s.lot, s.expiry, s.status, s.disposition, s.biz_location, s.parent_epc, s.last_event_time, s.last_event_id
		from t join serial_state s on s.epc = t.epc`, top)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s trace.State
		if err := rows.Scan(&s.EPC, &s.GTIN, &s.Serial, &s.Lot, &s.Expiry, &s.Status, &s.Disposition, &s.BizLocation, &s.ParentEPC, &s.LastEventTime, &s.LastEventID); err != nil {
			return nil, nil, err
		}
		states[s.EPC] = s
		if s.ParentEPC != nil {
			children[*s.ParentEPC] = append(children[*s.ParentEPC], s.EPC)
		}
	}
	return children, states, rows.Err()
}

type party struct {
	Name string
	GLN  string
}

// BuildTITS assembles the TI/TS EPCIS document for an outbound shipment.
//
// Structure follows the GS1 US Healthcare DSCSA implementation approach: aggregation
// events describe the hierarchy, one shipping ObjectEvent carries purchase order and
// despatch advice references, source/destination owning parties and locations, and the
// EPCIS header carries product and location master data plus the transaction statement.
// TODO(spec): align header vocabulary attribute URIs exactly with the current GS1 US
// Healthcare "Applying GS1 Standards for DSCSA" guideline before production use.
func (m *Module) BuildTITS(ctx context.Context, tx pgx.Tx, s Shipment, seller, buyer party) (*lib.Document, error) {
	children, states, err := tree(ctx, tx, s.EPCs)
	if err != nil {
		return nil, err
	}
	for _, e := range s.EPCs {
		if _, ok := states[e]; !ok {
			return nil, httpx.Unprocessable("unknown identifier in shipment", []string{e})
		}
		if st := states[e]; st.Status == "decommissioned" || st.Status == "destroyed" || st.Status == "recalled" {
			return nil, httpx.Unprocessable("cannot ship "+st.Status+" product", []string{e})
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	sellerLoc, _ := sgln(seller.GLN)
	buyerLoc, _ := sgln(buyer.GLN)
	var events []lib.Event
	parents := make([]string, 0, len(children))
	for p := range children {
		parents = append(parents, p)
	}
	sort.Strings(parents)
	for _, p := range parents {
		kids := append([]string(nil), children[p]...)
		sort.Strings(kids)
		events = append(events, lib.Event{Type: lib.AggregationEvent, Action: lib.ActionObserve, BizStep: "shipping", Disposition: "in_transit",
			EventTime: now, EventTimeZoneOffset: "+00:00", ParentID: p, ChildEPCs: kids, BizLocation: loc(sellerLoc)})
	}
	ship := lib.Event{Type: lib.ObjectEvent, Action: lib.ActionObserve, BizStep: "shipping", Disposition: "in_transit", EventTime: now, EventTimeZoneOffset: "+00:00",
		EPCList: append([]string(nil), s.EPCs...), ReadPoint: loc(sellerLoc),
		BizTransactionList: []lib.BizTransaction{{Type: "po", BizTransaction: "urn:epcglobal:cbv:bt:" + seller.GLN + ":" + s.PONumber}}}
	if s.ASNNumber != "" {
		ship.BizTransactionList = append(ship.BizTransactionList, lib.BizTransaction{Type: "desadv", BizTransaction: "urn:epcglobal:cbv:bt:" + seller.GLN + ":" + s.ASNNumber})
	}
	if seller.GLN != "" {
		ship.SourceList = []lib.Source{{Type: "owning_party", Source: sellerLoc}, {Type: "location", Source: sellerLoc}}
	}
	if buyer.GLN != "" {
		ship.DestinationList = []lib.Destination{{Type: "owning_party", Destination: buyerLoc}, {Type: "location", Destination: buyerLoc}}
	}
	events = append(events, ship)

	// Master data: one entry per product (GTIN) and lot.
	gtinSet := map[string]bool{}
	lots := map[string]map[string]string{}
	for _, st := range states {
		if st.GTIN == nil {
			continue
		}
		gtinSet[*st.GTIN] = true
		if st.Lot != nil {
			if lots[*st.GTIN] == nil {
				lots[*st.GTIN] = map[string]string{}
			}
			exp := ""
			if st.Expiry != nil {
				exp = st.Expiry.Format("2006-01-02")
			}
			lots[*st.GTIN][*st.Lot] = exp
		}
	}
	gtins := make([]string, 0, len(gtinSet))
	for g := range gtinSet {
		gtins = append(gtins, g)
	}
	sort.Strings(gtins)
	products, err := masterdata.KnownGTINs(ctx, tx, gtins)
	if err != nil {
		return nil, err
	}
	var vocabProducts []map[string]any
	for _, g := range gtins {
		p := products[g]
		attrs := map[string]any{"gtin": g, "regulatedProductName": p.Name, "dosageFormType": p.DosageForm, "strengthDescription": p.Strength, "netContentDescription": p.PackSize}
		if p.NDC != nil {
			attrs["additionalTradeItemIdentification"] = map[string]string{"type": "FDA_NDC_11", "value": *p.NDC}
		}
		var ls []map[string]string
		for l, exp := range lots[g] {
			ls = append(ls, map[string]string{"lotNumber": l, "itemExpirationDate": exp})
		}
		attrs["lots"] = ls
		vocabProducts = append(vocabProducts, attrs)
	}
	header, _ := json.Marshal(map[string]any{
		"epcisMasterData": map[string]any{
			"products":  vocabProducts,
			"locations": []map[string]string{{"id": sellerLoc, "name": seller.Name, "role": "seller"}, {"id": buyerLoc, "name": buyer.Name, "role": "buyer"}},
		},
		"dscsa:transactionInformation": map[string]any{"transactionDate": now.Format("2006-01-02"), "shipmentDate": shipDate(s, now), "poNumber": s.PONumber,
			"seller": seller, "buyer": buyer, "numberOfContainers": len(s.EPCs)},
		// The transaction statement: the seller attests to the DSCSA requirements of section 582.
		"dscsa:transactionStatement": map[string]any{"affirmTransactionStatement": true, "statement": transactionStatement},
	})
	doc := lib.NewDocument(events)
	doc.Header = header
	return doc, nil
}

// transactionStatement paraphrases the statutory elements; the binding text is FD&C Act 581(27).
const transactionStatement = "Seller has complied with each applicable subsection of FDCA Sec. 582; is authorized as required under DSCSA; " +
	"received the product from a person authorized as required under DSCSA; received transaction information and a transaction statement from the prior owner; " +
	"did not knowingly ship a suspect or illegitimate product; had systems and processes in place to comply with verification requirements; " +
	"did not knowingly provide false transaction information; and did not knowingly alter the transaction history."

func shipDate(s Shipment, now time.Time) string {
	if s.ShipDate != nil {
		return s.ShipDate.Format("2006-01-02")
	}
	return now.Format("2006-01-02")
}

func sgln(gln string) (string, error) {
	if gln == "" {
		return "", errors.New("no gln")
	}
	return gs1.DefaultResolver + "/414/" + gln, nil
}

func loc(id string) *lib.Location {
	if id == "" {
		return nil
	}
	return &lib.Location{ID: id}
}

// sellerParty is this tenant's identity (directory listing, else first site).
func sellerParty(ctx context.Context, tx pgx.Tx) party {
	var p party
	_ = tx.QueryRow(ctx, `select coalesce(l.name, t.name), coalesce(l.gln, (select gln from site order by created_at limit 1), '')
		from tenant t left join directory_listing l on l.tenant_id = t.id where t.id = app_tenant()`).Scan(&p.Name, &p.GLN)
	return p
}

func (m *Module) buildTITS(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := db.One[Shipment](r.Context(), tx, `select `+shipCols+` from shipment where id=$1 and direction='outbound'`, id)
		if err != nil {
			return err
		}
		p, err := partners.Get(r.Context(), tx, *s.PartnerID)
		if err != nil {
			return err
		}
		buyer := party{Name: p.Name}
		if p.GLN != nil {
			buyer.GLN = *p.GLN
		}
		doc, err := m.BuildTITS(r.Context(), tx, s, sellerParty(r.Context(), tx), buyer)
		if err != nil {
			return err
		}
		b, _ := lib.WriteJSON(doc)
		if _, err := tx.Exec(r.Context(), `update shipment set tits=$2, status=case when status='draft' then 'ready' else status end where id=$1`, id, b); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "shipment.tits", Entity: "shipment", EntityID: id.String(), After: map[string]any{"events": len(doc.Body.EventList)}}); err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/ld+json")
		_, _ = w.Write(b) //nolint:gosec // G705: server-built JSON-LD with nosniff, not HTML
		return nil
	})
}

// --- transports ---

// Transport delivers a TI/TS document to a partner.
type Transport interface {
	Send(ctx context.Context, from uuid.UUID, p partners.Partner, s Shipment, doc []byte) (map[string]any, error)
}

// HTTPSTransport posts the EPCIS document to the partner's EPCIS capture endpoint.
type HTTPSTransport struct{ Client *http.Client }

// Send implements Transport.
func (t *HTTPSTransport) Send(ctx context.Context, _ uuid.UUID, p partners.Partner, s Shipment, doc []byte) (map[string]any, error) {
	if p.Endpoints.EPCISCapture == "" {
		return nil, httpx.Conflict("partner has no EPCIS capture endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoints.EPCISCapture, bytes.NewReader(doc))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ld+json")
	req.Header.Set("X-Source", "openspoor-dscsa")
	req.Header.Set("X-OpenSpoor-Shipment", s.ID.String())
	res, err := t.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	out := map[string]any{"status": res.StatusCode, "body": string(body)}
	if res.StatusCode >= 300 {
		return out, fmt.Errorf("partner returned %s", res.Status)
	}
	return out, nil
}

// AS2Transport is the AS2 (RFC 4130) channel. Go AS2 libraries are immature (ADR-007), so the
// transport is an interface boundary: production installs plug in an AS2 gateway.
type AS2Transport struct{}

// Send implements Transport.
func (AS2Transport) Send(context.Context, uuid.UUID, partners.Partner, Shipment, []byte) (map[string]any, error) {
	return nil, httpx.Conflict("AS2 transport is not configured on this installation; use https or plug in an AS2 gateway (see ADR-007)")
}

// localTransport delivers to a partner tenant on the same Open Spoor node.
type localTransport struct{ m *Module }

// Send implements Transport.
func (t *localTransport) Send(ctx context.Context, from uuid.UUID, p partners.Partner, s Shipment, doc []byte) (map[string]any, error) {
	if p.LinkedTenant == nil {
		return nil, errors.New("partner is not on this node")
	}
	asn := s.ASN
	asn.Number = s.ASNNumber
	in, err := t.m.Receive(auth.With(ctx, auth.System(*p.LinkedTenant)), InboundInput{Document: doc, PONumber: s.PONumber, ASN: asn, FromTenant: &from})
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": 202, "delivered": "local", "inboundShipmentId": in.ID, "captureId": in.CaptureID}, nil
}

func (m *Module) send(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	me := auth.MustFrom(ctx)
	var s Shipment
	var p partners.Partner
	var doc []byte
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		s, err = db.One[Shipment](ctx, tx, `select `+shipCols+` from shipment where id=$1 and direction='outbound'`, id)
		if err != nil {
			return err
		}
		p, err = partners.Get(ctx, tx, *s.PartnerID)
		if err != nil {
			return err
		}
		if p.Status != "active" {
			return httpx.Conflict("partner connection is " + p.Status)
		}
		if len(s.TITS) == 0 {
			buyer := party{Name: p.Name}
			if p.GLN != nil {
				buyer.GLN = *p.GLN
			}
			d, err := m.BuildTITS(ctx, tx, s, sellerParty(ctx, tx), buyer)
			if err != nil {
				return err
			}
			s.TITS, _ = lib.WriteJSON(d)
		}
		doc = s.TITS
		return nil
	})
	if err != nil {
		return err
	}
	transport := s.Transport
	if strings.HasPrefix(p.Endpoints.EPCISCapture, "local:") && transport == "https" {
		transport = "local"
	}
	resp, sendErr := m.Transports[transport].Send(ctx, me.TenantID, p, s, doc)
	status := "sent"
	if sendErr != nil {
		status = "send_failed"
		if resp == nil {
			resp = map[string]any{}
		}
		resp["error"] = sendErr.Error()
	}
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update shipment set status=$2, tits=$3, response=$4, sent_at=case when $2='sent' then now() else sent_at end where id=$1`, id, status, doc, resp); err != nil {
			return err
		}
		if sendErr != nil {
			if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id) values ($1, app_tenant(), 'dscsa.send_failed', 'error', $2, $3, 'shipment', $4)`,
				ids.New(), "TI/TS delivery to "+p.Name+" failed", map[string]any{"error": sendErr.Error(), "shipmentId": id}, id.String()); err != nil {
				return err
			}
		}
		_, err := audit.Write(ctx, tx, audit.Record{Action: "shipment.send", Entity: "shipment", EntityID: id.String(), After: map[string]any{"status": status, "transport": transport, "response": resp}})
		return err
	})
	if err != nil {
		return err
	}
	if sendErr == nil {
		// Record our own shipping events (in_transit) in the repository.
		var d lib.Document
		if json.Unmarshal(doc, &d) == nil {
			b, _ := lib.WriteJSON(&d)
			_, _ = m.EPCIS.Capture(ctx, "application/ld+json", b, "dscsa-outbound")
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": status, "response": resp})
	return nil
}

// --- inbound ---

// InboundInput is a received TI/TS with its ASN.
type InboundInput struct {
	Document   json.RawMessage `json:"document"`
	PONumber   string          `json:"poNumber"`
	ASN        ASN             `json:"asn"`
	PartnerID  *uuid.UUID      `json:"partnerId"`
	FromTenant *uuid.UUID      `json:"-"`
}

// Receive captures an inbound TI/TS, stores the shipment and reconciles it with the ASN.
func (m *Module) Receive(ctx context.Context, in InboundInput) (Shipment, error) {
	if len(in.Document) == 0 {
		return Shipment{}, httpx.BadRequest("document is required")
	}
	raw := []byte(in.Document)
	var str string
	if json.Unmarshal(raw, &str) == nil { // XML passed as a JSON string
		raw = []byte(str)
	}
	c, err := m.EPCIS.Capture(ctx, "", raw, "dscsa-inbound")
	if err != nil {
		return Shipment{}, err
	}
	doc, _, err := lib.Parse("", raw)
	if err != nil {
		return Shipment{}, httpx.BadRequest(err.Error())
	}
	ValidateNormalize(doc)
	var top []string
	for _, e := range doc.Body.EventList {
		if e.Type == lib.ObjectEvent && e.BizStep == "shipping" {
			top = append(top, lib.CanonicalAll(e.EPCList)...)
		}
	}
	rec := ReconcileDoc(doc, in.ASN)
	var s Shipment
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		pid := in.PartnerID
		if pid == nil && in.FromTenant != nil {
			var id uuid.UUID
			if tx.QueryRow(ctx, `select id from partner where linked_tenant=$1`, *in.FromTenant).Scan(&id) == nil {
				pid = &id
			}
		}
		status := "received"
		if !rec.Matched {
			status = "exception"
		}
		var err error
		s, err = db.One[Shipment](ctx, tx, `insert into shipment (id, tenant_id, direction, partner_id, po_number, asn_number, status, epcs, asn, tits, reconciliation, capture_id, received_at)
			values ($1, app_tenant(), 'inbound', $2,$3,$4,$5,$6,$7,$8,$9,$10, now()) returning `+shipCols,
			ids.New(), pid, in.PONumber, in.ASN.Number, status, top, in.ASN, raw, rec, c.ID)
		if err != nil {
			return err
		}
		if !rec.Matched {
			if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id) values ($1, app_tenant(), 'dscsa.reconciliation', 'error', $2, $3, 'shipment', $4)`,
				ids.New(), fmt.Sprintf("Inbound PO %s does not match its ASN (%d missing, %d unexpected)", in.PONumber, len(rec.Missing), len(rec.Unexpected)), rec, s.ID.String()); err != nil {
				return err
			}
		}
		_, err = audit.Write(ctx, tx, audit.Record{Action: "shipment.receive", Entity: "shipment", EntityID: s.ID.String(), After: map[string]any{"po": in.PONumber, "matched": rec.Matched}})
		return err
	})
	return s, err
}

// ValidateNormalize normalizes CBV values on a parsed document.
func ValidateNormalize(d *lib.Document) {
	for i := range d.Body.EventList {
		lib.Normalize(&d.Body.EventList[i])
	}
}

// ReconcileDoc compares the identifiers and per-GTIN counts in a TI/TS with an ASN.
func ReconcileDoc(doc *lib.Document, asn ASN) Reconciliation {
	inDoc := map[string]bool{}
	for _, e := range doc.Body.EventList {
		for _, id := range e.AllEPCs() {
			inDoc[lib.Canonical(id)] = true
		}
	}
	rec := Reconciliation{Missing: []string{}, Unexpected: []string{}, CheckedAt: time.Now().UTC()}
	inASN := map[string]bool{}
	expected := map[string]int{}
	listed := false
	for _, it := range asn.Items {
		expected[gs1.NormalizeGTIN(it.GTIN)] += it.Quantity
		for _, e := range it.EPCs {
			listed = true
			c := lib.Canonical(e)
			inASN[c] = true
			if !inDoc[c] {
				rec.Missing = append(rec.Missing, e)
			}
		}
	}
	received := map[string]int{}
	for id := range inDoc {
		g, s, _ := lib.Key(id)
		if g != "" && s != "" {
			received[g]++
		}
		if listed && !inASN[id] && s != "" {
			rec.Unexpected = append(rec.Unexpected, id)
		}
	}
	sort.Strings(rec.Unexpected)
	ok := len(rec.Missing) == 0 && len(rec.Unexpected) == 0
	keys := make([]string, 0, len(expected))
	for g := range expected {
		keys = append(keys, g)
	}
	sort.Strings(keys)
	for _, g := range keys {
		q := QuantityResult{GTIN: g, Expected: expected[g], Received: received[g]}
		rec.Quantities = append(rec.Quantities, q)
		if q.Expected != q.Received {
			ok = false
		}
	}
	rec.Matched = ok
	return rec
}

func (m *Module) receiveInbound(w http.ResponseWriter, r *http.Request) error {
	var in InboundInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	s, err := m.Receive(r.Context(), in)
	var ce *epcis.CaptureError
	if errors.As(err, &ce) {
		return httpx.Unprocessable("TI/TS document rejected", ce.Capture.Errors)
	}
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, s)
	return nil
}

func (m *Module) reconcile(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		ASN *ASN `json:"asn"`
	}
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := db.One[Shipment](r.Context(), tx, `select `+shipCols+` from shipment where id=$1`, id)
		if err != nil {
			return err
		}
		if len(s.TITS) == 0 {
			return httpx.Conflict("shipment has no TI/TS document")
		}
		doc, _, err := lib.Parse("", s.TITS)
		if err != nil {
			return err
		}
		ValidateNormalize(doc)
		asn := s.ASN
		if in.ASN != nil {
			asn = *in.ASN
		}
		rec := ReconcileDoc(doc, asn)
		status := "reconciled"
		if !rec.Matched {
			status = "exception"
		}
		if _, err := tx.Exec(r.Context(), `update shipment set reconciliation=$2, asn=$3, status=$4 where id=$1`, id, rec, asn, status); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "shipment.reconcile", Entity: "shipment", EntityID: id.String(), After: rec}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, rec)
		return nil
	})
}
