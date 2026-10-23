package dscsa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Investigation kinds.
const (
	KindTracingRequest = "tracing_request"
	KindSuspect        = "suspect"
	KindIllegitimate   = "illegitimate"
)

// Investigation statuses.
const (
	StatusOpen                  = "open"
	StatusQuarantined           = "quarantined"
	StatusInvestigating         = "investigating"
	StatusCleared               = "cleared"
	StatusConfirmedIllegitimate = "confirmed_illegitimate"
	StatusClosed                = "closed"
)

// Quarantine is recorded in the EPCIS repository as a holding ObjectEvent; the trace module
// projects the disposition into serial_state.status. Release on clearance records the product
// as sellable again.
const (
	quarantineBizStep     = "holding"
	quarantineDisposition = "non_sellable_other"
	releaseBizStep        = "inspecting"
	releaseDisposition    = "sellable_accessible"
	// maxScope bounds how many identifiers one investigation resolves (lots can be large).
	maxScope = 10000
	// maxAnswerItems bounds the per-identifier detail in a tracing answer.
	maxAnswerItems = 500
)

var investigationKinds = []string{KindTracingRequest, KindSuspect, KindIllegitimate}

// transitions is the investigation state machine. Closing is handled by close (signature rules).
var transitions = map[string][]string{
	StatusOpen:                  {StatusQuarantined, StatusInvestigating},
	StatusQuarantined:           {StatusInvestigating},
	StatusInvestigating:         {StatusCleared, StatusConfirmedIllegitimate},
	StatusCleared:               {StatusClosed},
	StatusConfirmedIllegitimate: {StatusClosed},
}

// allowed reports whether kind may move from one status to another.
func allowed(kind, from, to string) bool {
	if kind == KindTracingRequest {
		if to == StatusQuarantined {
			return false
		}
		// A tracing request is answered, not adjudicated: it may close once investigated.
		if from == StatusInvestigating && to == StatusClosed {
			return true
		}
	}
	return slices.Contains(transitions[from], to)
}

// Investigation is a DSCSA tracing request or suspect / illegitimate product investigation.
type Investigation struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Kind               string     `json:"kind" db:"kind"`
	Status             string     `json:"status" db:"status"`
	Title              string     `json:"title" db:"title"`
	GTIN               string     `json:"gtin" db:"gtin"`
	Serials            []string   `json:"serials" db:"serials"`
	Lots               []string   `json:"lots" db:"lots"`
	EPCs               []string   `json:"epcs" db:"epcs"`
	NotFound           []string   `json:"notFound" db:"not_found"`
	ShipmentID         *uuid.UUID `json:"shipmentId,omitempty" db:"shipment_id"`
	RequesterType      string     `json:"requesterType" db:"requester_type"`
	RequesterPartnerID *uuid.UUID `json:"requesterPartnerId,omitempty" db:"requester_partner_id"`
	RequesterName      string     `json:"requesterName" db:"requester_name"`
	RequestRef         string     `json:"requestRef" db:"request_ref"`
	SourceAlertID      *uuid.UUID `json:"sourceAlertId,omitempty" db:"source_alert_id"`
	DueAt              *time.Time `json:"dueAt,omitempty" db:"due_at"`
	Overdue            bool       `json:"overdue" db:"overdue"`
	Held               bool       `json:"held" db:"held"`
	Findings           []Finding  `json:"findings" db:"findings"`
	Decision           string     `json:"decision" db:"decision"`
	AnsweredAt         *time.Time `json:"answeredAt,omitempty" db:"answered_at"`
	SignatureID        *uuid.UUID `json:"signatureId,omitempty" db:"signature_id"`
	CreatedBy          string     `json:"createdBy" db:"created_by"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt          time.Time  `json:"updatedAt" db:"updated_at"`
	ClosedAt           *time.Time `json:"closedAt,omitempty" db:"closed_at"`
}

const invCols = `id, kind, status, title, gtin, serials, lots, epcs, not_found, shipment_id, requester_type, requester_partner_id, requester_name, request_ref,
	source_alert_id, due_at, (due_at is not null and due_at < now() and status <> 'closed') as overdue, held, findings, decision, answered_at, signature_id,
	created_by, created_at, updated_at, closed_at`

// Finding is one recorded investigation finding (evidence, notification, consultation…).
type Finding struct {
	ID      uuid.UUID      `json:"id"`
	Kind    string         `json:"kind"`
	Summary string         `json:"summary"`
	Detail  map[string]any `json:"detail,omitempty"`
	By      string         `json:"by"`
	At      time.Time      `json:"at"`
}

// findingKinds are the finding categories. Notification and consultation findings record that
// the step happened (who, when, reference); the forms and channels used are tenant procedure.
// TODO(spec): confirm the notification form, channel and required content for FDA and trading
// partner notifications of illegitimate product, and for terminating a notification.
var findingKinds = []string{"note", "evidence", "verification", "partner_notification", "authority_notification", "consultation", "notification_terminated"}

// InvestigationEvent is one timeline entry.
type InvestigationEvent struct {
	ID         uuid.UUID      `json:"id" db:"id"`
	Kind       string         `json:"kind" db:"kind"`
	FromStatus string         `json:"fromStatus" db:"from_status"`
	ToStatus   string         `json:"toStatus" db:"to_status"`
	Actor      string         `json:"actor" db:"actor"`
	Note       string         `json:"note" db:"note"`
	Data       map[string]any `json:"data" db:"data"`
	CreatedAt  time.Time      `json:"createdAt" db:"created_at"`
}

type investigationPolicy struct {
	Kind      string `json:"kind" db:"kind"`
	DueHours  int    `json:"dueHours" db:"due_hours"`
	Basis     string `json:"basis" db:"basis"`
	IsDefault bool   `json:"isDefault" db:"-"`
}

// defaultInvestigationPolicies apply until a tenant configures its own windows.
// TODO(spec): the defaults below are placeholders, not verified statutory deadlines. Confirm the
// FD&C Act 582 response window for tracing requests (and whether it counts business days), the
// notification window after an illegitimate-product determination, and whether any timeline
// applies to suspect-product investigations, then set each tenant's values accordingly.
var defaultInvestigationPolicies = []investigationPolicy{
	{Kind: KindTracingRequest, DueHours: 48, Basis: "Default, unverified (TODO(spec)): response window for tracing requests; confirm against FD&C Act 582. Business days are not computed.", IsDefault: true},
	{Kind: KindSuspect, DueHours: 0, Basis: "Default (TODO(spec)): no due date; confirm whether a timeline applies to suspect-product investigations.", IsDefault: true},
	{Kind: KindIllegitimate, DueHours: 24, Basis: "Default, unverified (TODO(spec)): notification window after an illegitimate-product determination; confirm against FD&C Act 582.", IsDefault: true},
}

func (m *Module) investigationRoutes(r chi.Router) {
	r.Use(auth.Require(auth.RoleAdmin, auth.RoleCompliance))
	r.Get("/", httpx.Handler(m.listInvestigations))
	r.Post("/", httpx.Handler(m.createInvestigation))
	r.Post("/from-alert", httpx.Handler(m.investigationFromAlert))
	r.Get("/policies", httpx.Handler(m.listInvestigationPolicies))
	r.Put("/policies/{kind}", httpx.Handler(m.putInvestigationPolicy))
	r.Get("/{id}", httpx.Handler(m.getInvestigation))
	r.Post("/{id}/transition", httpx.Handler(m.transitionInvestigation))
	r.Post("/{id}/findings", httpx.Handler(m.addFinding))
	r.Post("/{id}/tracing-answer", httpx.Handler(m.assembleTracingAnswer))
	r.Get("/{id}/tracing-answer", httpx.Handler(m.getTracingAnswer))
	r.Post("/{id}/close", httpx.Handler(m.closeInvestigation))
}

// --- policies ---

func policies(ctx context.Context, tx pgx.Tx) (map[string]investigationPolicy, error) {
	items, err := db.List[investigationPolicy](ctx, tx, `select kind, due_hours, basis from investigation_policy`)
	if err != nil {
		return nil, err
	}
	out := map[string]investigationPolicy{}
	for _, d := range defaultInvestigationPolicies {
		out[d.Kind] = d
	}
	for _, it := range items {
		out[it.Kind] = it
	}
	return out, nil
}

func dueFor(ctx context.Context, tx pgx.Tx, kind string, from time.Time) (*time.Time, error) {
	ps, err := policies(ctx, tx)
	if err != nil {
		return nil, err
	}
	if h := ps[kind].DueHours; h > 0 {
		t := from.Add(time.Duration(h) * time.Hour)
		return &t, nil
	}
	return nil, nil
}

func (m *Module) listInvestigationPolicies(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ps, err := policies(r.Context(), tx)
		if err != nil {
			return err
		}
		items := make([]investigationPolicy, 0, len(investigationKinds))
		for _, k := range investigationKinds {
			items = append(items, ps[k])
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[investigationPolicy]{Items: items, Total: len(items)})
		return nil
	})
}

func (m *Module) putInvestigationPolicy(w http.ResponseWriter, r *http.Request) error {
	kind := chi.URLParam(r, "kind")
	if !slices.Contains(investigationKinds, kind) {
		return httpx.BadRequest("kind must be one of " + strings.Join(investigationKinds, ", "))
	}
	var in struct {
		DueHours int    `json:"dueHours"`
		Basis    string `json:"basis"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.DueHours < 0 || in.DueHours > 8760 {
		return httpx.BadRequest("dueHours must be between 0 and 8760")
	}
	if strings.TrimSpace(in.Basis) == "" {
		return httpx.BadRequest("basis is required: state the procedure or regulation the window comes from")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[investigationPolicy](r.Context(), tx, `insert into investigation_policy (tenant_id, kind, due_hours, basis) values (app_tenant(), $1,$2,$3)
			on conflict (tenant_id, kind) do update set due_hours=excluded.due_hours, basis=excluded.basis returning kind, due_hours, basis`, kind, in.DueHours, in.Basis)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "investigation.policy.put", Entity: "investigation_policy", EntityID: kind, After: p}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

// --- create / list / get ---

// InvestigationInput opens an investigation.
type InvestigationInput struct {
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	GTIN       string     `json:"gtin"`
	Serials    []string   `json:"serials"`
	Lots       []string   `json:"lots"`
	EPCs       []string   `json:"epcs"`
	ShipmentID *uuid.UUID `json:"shipmentId"`
	Requester  *struct {
		Type      string     `json:"type"`
		PartnerID *uuid.UUID `json:"partnerId"`
		Name      string     `json:"name"`
	} `json:"requester"`
	RequestRef string     `json:"requestRef"`
	DueAt      *time.Time `json:"dueAt"`
	Note       string     `json:"note"`
	alertID    *uuid.UUID
}

func trimAll(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func (in *InvestigationInput) validate() error {
	if !slices.Contains(investigationKinds, in.Kind) {
		return httpx.BadRequest("kind must be one of " + strings.Join(investigationKinds, ", "))
	}
	in.Serials, in.Lots, in.EPCs = trimAll(in.Serials), trimAll(in.Lots), trimAll(in.EPCs)
	if in.GTIN = strings.TrimSpace(in.GTIN); in.GTIN != "" {
		in.GTIN = gs1.NormalizeGTIN(in.GTIN)
	}
	if (len(in.Serials) > 0 || len(in.Lots) > 0) && !gs1.ValidGTIN(in.GTIN) {
		return httpx.BadRequest("a valid gtin is required with serials or lots")
	}
	if in.GTIN != "" && !gs1.ValidGTIN(in.GTIN) {
		return httpx.BadRequest("gtin is not a valid GTIN")
	}
	if len(in.Serials)+len(in.Lots)+len(in.EPCs) == 0 && in.ShipmentID == nil {
		return httpx.BadRequest("name the product in scope: serials, lots, epcs or shipmentId")
	}
	if len(in.Serials)+len(in.EPCs) > maxScope {
		return httpx.BadRequest(fmt.Sprintf("at most %d identifiers per investigation", maxScope))
	}
	if in.Kind == KindTracingRequest {
		if in.Requester == nil || (in.Requester.Type != "partner" && in.Requester.Type != "authority") {
			return httpx.BadRequest("a tracing request needs requester.type partner or authority")
		}
		if in.Requester.Type == "partner" && in.Requester.PartnerID == nil {
			return httpx.BadRequest("requester.partnerId is required for a partner request")
		}
		if in.Requester.Type == "authority" && strings.TrimSpace(in.Requester.Name) == "" {
			return httpx.BadRequest("requester.name is required for an authority request")
		}
	} else if in.Requester != nil && in.Requester.Type != "" && in.Requester.Type != "partner" && in.Requester.Type != "authority" {
		return httpx.BadRequest("requester.type must be partner or authority")
	}
	return nil
}

// resolveScope maps the requested product to identifiers known in this tenant's repository,
// including everything aggregated below them.
func resolveScope(ctx context.Context, tx pgx.Tx, in InvestigationInput, shipmentEPCs []string) (known, notFound []string, err error) {
	notFound = []string{}
	top := make([]string, 0, len(in.Serials)+len(in.EPCs)+len(shipmentEPCs))
	for _, s := range in.Serials {
		top = append(top, gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: in.GTIN, Serial: s}))
	}
	top = lib.CanonicalAll(append(append(top, in.EPCs...), shipmentEPCs...))
	if len(in.Lots) > 0 {
		rows, err := tx.Query(ctx, `select epc from serial_state where tenant_id = app_tenant() and gtin=$1 and lot = any($2) order by epc limit $3`, in.GTIN, in.Lots, maxScope)
		if err != nil {
			return nil, nil, err
		}
		lotEPCs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, nil, err
		}
		if len(lotEPCs) == 0 {
			notFound = append(notFound, in.Lots...)
		}
		top = lib.CanonicalAll(append(top, lotEPCs...))
	}
	_, states, err := tree(ctx, tx, top)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range top {
		if _, ok := states[t]; !ok {
			notFound = append(notFound, t)
		}
	}
	known = make([]string, 0, len(states))
	for e := range states {
		known = append(known, e)
	}
	sort.Strings(known)
	if len(known) > maxScope {
		return nil, nil, httpx.Unprocessable(fmt.Sprintf("scope resolves to more than %d identifiers; narrow it", maxScope), nil)
	}
	return known, notFound, nil
}

func addEvent(ctx context.Context, tx pgx.Tx, inv uuid.UUID, kind, from, to, note string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	_, err := tx.Exec(ctx, `insert into investigation_event (id, tenant_id, investigation_id, kind, from_status, to_status, actor, note, data)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8)`, ids.New(), inv, kind, from, to, auth.MustFrom(ctx).Actor(), note, data)
	return err
}

// CreateInvestigation opens an investigation inside tx.
func (m *Module) CreateInvestigation(ctx context.Context, tx pgx.Tx, in InvestigationInput) (Investigation, error) {
	if err := in.validate(); err != nil {
		return Investigation{}, err
	}
	var shipEPCs []string
	if in.ShipmentID != nil {
		s, err := db.One[Shipment](ctx, tx, `select `+shipCols+` from shipment where id=$1`, *in.ShipmentID)
		if err != nil {
			return Investigation{}, httpx.BadRequest("unknown shipmentId")
		}
		shipEPCs = s.EPCs
	}
	var reqType, reqName string
	var reqPartner *uuid.UUID
	if in.Requester != nil {
		reqType, reqName, reqPartner = in.Requester.Type, strings.TrimSpace(in.Requester.Name), in.Requester.PartnerID
		if reqPartner != nil {
			p, err := partners.Get(ctx, tx, *reqPartner)
			if err != nil {
				return Investigation{}, httpx.BadRequest("unknown requester.partnerId")
			}
			if reqName == "" {
				reqName = p.Name
			}
		}
	}
	known, notFound, err := resolveScope(ctx, tx, in, shipEPCs)
	if err != nil {
		return Investigation{}, err
	}
	due := in.DueAt
	if due == nil {
		if due, err = dueFor(ctx, tx, in.Kind, time.Now()); err != nil {
			return Investigation{}, err
		}
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = strings.ReplaceAll(in.Kind, "_", " ")
		if in.GTIN != "" {
			title += " " + in.GTIN
		}
	}
	inv, err := db.One[Investigation](ctx, tx, `insert into investigation (id, tenant_id, kind, title, gtin, serials, lots, epcs, not_found, shipment_id,
			requester_type, requester_partner_id, requester_name, request_ref, source_alert_id, due_at, created_by)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) returning `+invCols,
		ids.New(), in.Kind, title, in.GTIN, in.Serials, in.Lots, known, notFound, in.ShipmentID, reqType, reqPartner, reqName, strings.TrimSpace(in.RequestRef), in.alertID, due,
		auth.MustFrom(ctx).Actor())
	if err != nil {
		return Investigation{}, err
	}
	if err := addEvent(ctx, tx, inv.ID, "created", "", StatusOpen, in.Note, map[string]any{"identifiers": len(known), "notFound": len(notFound), "sourceAlertId": in.alertID}); err != nil {
		return Investigation{}, err
	}
	if _, err := audit.Write(ctx, tx, audit.Record{Action: "investigation.create", Entity: "investigation", EntityID: inv.ID.String(), After: inv}); err != nil {
		return Investigation{}, err
	}
	return inv, nil
}

func (m *Module) createInvestigation(w http.ResponseWriter, r *http.Request) error {
	var in InvestigationInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		inv, err := m.CreateInvestigation(r.Context(), tx, in)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, inv)
		}
		return err
	})
}

func (m *Module) listInvestigations(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Investigation](r.Context(), tx, `select `+invCols+` from investigation
			where ($1='' or kind=$1)
			  and ($2='' or status=$2 or ($2='active' and status <> 'closed'))
			  and ($3='' or gtin=$3)
			  and ($4 <> 'true' or (due_at is not null and due_at < now() and status <> 'closed'))
			  and ($5='' or title ilike '%'||$5||'%' or request_ref ilike '%'||$5||'%' or requester_name ilike '%'||$5||'%' or $5 = any(serials) or $5 = any(lots))
			order by created_at desc limit 500`,
			q.Get("kind"), q.Get("status"), normGTIN(q.Get("gtin")), q.Get("overdue"), strings.TrimSpace(q.Get("q")))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Investigation]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getInvestigation(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		inv, err := db.One[Investigation](r.Context(), tx, `select `+invCols+` from investigation where id=$1`, id)
		if err != nil {
			return err
		}
		tl, err := db.List[InvestigationEvent](r.Context(), tx, `select id, kind, from_status, to_status, actor, note, data, created_at
			from investigation_event where investigation_id=$1 order by created_at, id`, id)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"investigation": inv, "timeline": tl, "allowedTransitions": nextStatuses(inv)})
		return nil
	})
}

func normGTIN(g string) string {
	if g == "" {
		return ""
	}
	return gs1.NormalizeGTIN(g)
}

// nextStatuses lists the transitions available from the current status.
func nextStatuses(inv Investigation) []string {
	out := []string{}
	for _, s := range []string{StatusQuarantined, StatusInvestigating, StatusCleared, StatusConfirmedIllegitimate, StatusClosed} {
		if allowed(inv.Kind, inv.Status, s) {
			out = append(out, s)
		}
	}
	return out
}

// --- transitions ---

type transitionInput struct {
	To          string     `json:"to"`
	Note        string     `json:"note"`
	Decision    string     `json:"decision"`
	SignatureID *uuid.UUID `json:"signatureId"`
}

func (m *Module) transitionInvestigation(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in transitionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	inv, err := m.Transition(r.Context(), id, in)
	if err == nil {
		httpx.JSON(w, http.StatusOK, inv)
	}
	return err
}

func (m *Module) closeInvestigation(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in transitionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.To = StatusClosed
	inv, err := m.Transition(r.Context(), id, in)
	if err == nil {
		httpx.JSON(w, http.StatusOK, inv)
	}
	return err
}

// signatureRequired reports whether closing needs an electronic signature.
func signatureRequired(inv Investigation) bool {
	return inv.Status == StatusConfirmedIllegitimate || inv.Kind == KindIllegitimate
}

// Transition moves an investigation through its state machine. Quarantine and release capture
// EPCIS events through the epcis module; closing a confirmed illegitimate product investigation
// requires the caller's electronic signature on the investigation.
func (m *Module) Transition(ctx context.Context, id uuid.UUID, in transitionInput) (Investigation, error) {
	p := auth.MustFrom(ctx)
	var cur Investigation
	err := m.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		cur, err = db.One[Investigation](ctx, tx, `select `+invCols+` from investigation where id=$1`, id)
		return err
	})
	if err != nil {
		return Investigation{}, err
	}
	if !allowed(cur.Kind, cur.Status, in.To) {
		return Investigation{}, httpx.Conflict(fmt.Sprintf("cannot move a %s investigation from %s to %s", cur.Kind, cur.Status, in.To))
	}
	if in.To == StatusClosed {
		in.Decision = strings.TrimSpace(in.Decision)
		if in.Decision == "" {
			return Investigation{}, httpx.BadRequest("decision is required to close an investigation")
		}
		if cur.Kind == KindTracingRequest && cur.AnsweredAt == nil {
			return Investigation{}, httpx.Conflict("assemble the tracing answer before closing the request")
		}
		if signatureRequired(cur) && in.SignatureID == nil {
			return Investigation{}, &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "closing an illegitimate product investigation requires your electronic signature (POST /esignatures with entity investigation)"}
		}
	}

	// EPCIS side effects run before the state change; capture is idempotent per event.
	data := map[string]any{}
	held := cur.Held
	switch {
	case in.To == StatusQuarantined && len(cur.EPCs) > 0:
		cid, err := m.captureHold(ctx, cur.EPCs, quarantineBizStep, quarantineDisposition)
		if err != nil {
			return Investigation{}, err
		}
		data["captureId"], data["identifiers"], data["disposition"] = cid, len(cur.EPCs), quarantineDisposition
		held = true
	case in.To == StatusQuarantined:
		data["identifiers"] = 0
		data["physicalOnly"] = true
	case in.To == StatusCleared && cur.Held:
		// Identifiers still held by another investigation stay quarantined.
		var release, kept []string
		err := m.Tx(ctx, func(tx pgx.Tx) error {
			var err error
			kept, err = heldIdentifiers(ctx, tx, cur.EPCs, id)
			return err
		})
		if err != nil {
			return Investigation{}, err
		}
		for _, e := range cur.EPCs {
			if !slices.Contains(kept, e) {
				release = append(release, e)
			}
		}
		if len(release) > 0 {
			cid, err := m.captureHold(ctx, release, releaseBizStep, releaseDisposition)
			if err != nil {
				return Investigation{}, err
			}
			data["captureId"], data["disposition"] = cid, releaseDisposition
		}
		data["identifiers"], data["stillHeld"] = len(release), len(kept)
		held = false
	}

	var out Investigation
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		var sigID *uuid.UUID
		if in.To == StatusClosed && in.SignatureID != nil {
			var meaning string
			// The signature must be the caller's, on this investigation, and given after its last change.
			if err := tx.QueryRow(ctx, `select meaning from esignature where id=$1 and entity='investigation' and entity_id=$2 and user_id=$3 and signed_at >= $4`,
				*in.SignatureID, id.String(), p.UserID, cur.UpdatedAt).Scan(&meaning); err != nil {
				return &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "signature not found: sign this investigation (POST /esignatures with entity investigation) and pass its id"}
			}
			sigID = in.SignatureID
			data["signatureId"], data["signatureMeaning"] = sigID, meaning
		}
		kind := cur.Kind
		due := cur.DueAt
		if in.To == StatusConfirmedIllegitimate {
			kind = KindIllegitimate
			d, err := dueFor(ctx, tx, KindIllegitimate, time.Now())
			if err != nil {
				return err
			}
			if d != nil {
				due = d
			}
		}
		if in.To == StatusClosed && in.Decision != "" {
			data["decision"] = in.Decision
		}
		var err error
		out, err = db.One[Investigation](ctx, tx, `update investigation set status=$3, kind=$4, held=$5, due_at=$6,
				decision=case when $7 <> '' then $7 else decision end, signature_id=coalesce($8, signature_id),
				closed_at=case when $3='closed' then now() else closed_at end, updated_at=now()
			where id=$1 and status=$2 returning `+invCols,
			id, cur.Status, in.To, kind, held, due, in.Decision, sigID)
		if errors.Is(err, db.ErrNotFound) {
			return httpx.Conflict("investigation changed concurrently; reload and retry")
		}
		if err != nil {
			return err
		}
		if err := addEvent(ctx, tx, id, "transition", cur.Status, in.To, in.Note, data); err != nil {
			return err
		}
		_, err = audit.Write(ctx, tx, audit.Record{Action: "investigation." + in.To, Entity: "investigation", EntityID: id.String(),
			Before: map[string]any{"status": cur.Status, "kind": cur.Kind, "held": cur.Held}, After: out, Reason: in.Note})
		return err
	})
	return out, err
}

// captureHold records a holding (or release) ObjectEvent for the identifiers in the EPCIS repository.
func (m *Module) captureHold(ctx context.Context, epcs []string, bizStep, disposition string) (uuid.UUID, error) {
	ev := lib.Event{Type: lib.ObjectEvent, Action: lib.ActionObserve, BizStep: bizStep, Disposition: disposition,
		EventTime: time.Now().UTC().Truncate(time.Millisecond), EventTimeZoneOffset: "+00:00", EPCList: append([]string(nil), epcs...)}
	b, err := lib.WriteJSON(lib.NewDocument([]lib.Event{ev}))
	if err != nil {
		return uuid.Nil, err
	}
	c, err := m.EPCIS.Capture(ctx, "application/ld+json", b, "dscsa-investigation")
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

// heldIdentifiers returns the identifiers among epcs held by an investigation (other than except).
func heldIdentifiers(ctx context.Context, tx pgx.Tx, epcs []string, except uuid.UUID) ([]string, error) {
	if len(epcs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `select distinct x from investigation i, unnest(i.epcs) x where i.held and i.id <> $2 and i.epcs && $1 and x = any($1) order by x`, epcs, except)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// --- findings ---

func (m *Module) addFinding(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Kind    string         `json:"kind"`
		Summary string         `json:"summary"`
		Detail  map[string]any `json:"detail"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = "note"
	}
	if !slices.Contains(findingKinds, in.Kind) {
		return httpx.BadRequest("kind must be one of " + strings.Join(findingKinds, ", "))
	}
	if strings.TrimSpace(in.Summary) == "" {
		return httpx.BadRequest("summary is required")
	}
	ctx := r.Context()
	f := Finding{ID: ids.New(), Kind: in.Kind, Summary: strings.TrimSpace(in.Summary), Detail: in.Detail, By: auth.MustFrom(ctx).Actor(), At: time.Now().UTC()}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		fj, _ := json.Marshal([]Finding{f})
		inv, err := db.One[Investigation](ctx, tx, `update investigation set findings = findings || $2::jsonb, updated_at=now() where id=$1 and status <> 'closed' returning `+invCols, id, fj)
		if errors.Is(err, db.ErrNotFound) {
			var n int
			if tx.QueryRow(ctx, `select count(*) from investigation where id=$1`, id).Scan(&n) == nil && n > 0 {
				return httpx.Conflict("investigation is closed")
			}
			return err
		}
		if err != nil {
			return err
		}
		if err := addEvent(ctx, tx, id, "finding", inv.Status, inv.Status, f.Summary, map[string]any{"findingId": f.ID, "kind": f.Kind, "detail": f.Detail}); err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "investigation.finding", Entity: "investigation", EntityID: id.String(), After: f}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, inv)
		return nil
	})
}

// --- tracing answer ---

// TracingAnswer is the assembled transaction history for the identifiers in scope.
// TODO(spec): confirm the content and format a tracing response must take for each requester
// (FDA or other federal/state official, trading partner); the answer carries the stored TI/TS
// per transaction and the event history per identifier, and a person reviews it before sending.
type TracingAnswer struct {
	InvestigationID uuid.UUID            `json:"investigationId"`
	Kind            string               `json:"kind"`
	Requester       map[string]any       `json:"requester"`
	RequestRef      string               `json:"requestRef"`
	GeneratedAt     time.Time            `json:"generatedAt"`
	GeneratedBy     string               `json:"generatedBy"`
	Scope           map[string]any       `json:"scope"`
	Products        []map[string]any     `json:"products"`
	Items           []TracingItem        `json:"items"`
	Transactions    []TracingTransaction `json:"transactions"`
	NotFound        []string             `json:"notFound"`
	Truncated       bool                 `json:"truncated"`
}

// TracingItem is the state and history of one identifier.
type TracingItem struct {
	ID        string      `json:"id"`
	State     trace.State `json:"state"`
	Ancestors []string    `json:"ancestors"`
	History   []lib.Event `json:"history"`
	Shipments []uuid.UUID `json:"shipments"`
}

// TracingTransaction is one change of ownership (shipment) with its TI/TS.
type TracingTransaction struct {
	ShipmentID             uuid.UUID  `json:"shipmentId"`
	Direction              string     `json:"direction"`
	Partner                *party     `json:"partner,omitempty"`
	PONumber               string     `json:"poNumber"`
	ASNNumber              string     `json:"asnNumber"`
	ShipDate               *time.Time `json:"shipDate,omitempty"`
	SentAt                 *time.Time `json:"sentAt,omitempty"`
	ReceivedAt             *time.Time `json:"receivedAt,omitempty"`
	Status                 string     `json:"status"`
	TITSSource             string     `json:"titsSource"` // stored | rebuilt | unavailable
	TransactionInformation any        `json:"transactionInformation,omitempty"`
	TransactionStatement   any        `json:"transactionStatement,omitempty"`
	EventCount             int        `json:"eventCount"`
	Note                   string     `json:"note,omitempty"`
}

// BuildTracingAnswer assembles the answer for an investigation inside tx.
func (m *Module) BuildTracingAnswer(ctx context.Context, tx pgx.Tx, inv Investigation) (TracingAnswer, error) {
	ans := TracingAnswer{InvestigationID: inv.ID, Kind: inv.Kind, RequestRef: inv.RequestRef, GeneratedAt: time.Now().UTC(), GeneratedBy: auth.MustFrom(ctx).Actor(),
		Requester: map[string]any{"type": inv.RequesterType, "name": inv.RequesterName, "partnerId": inv.RequesterPartnerID},
		Scope:     map[string]any{"gtin": inv.GTIN, "serials": inv.Serials, "lots": inv.Lots, "shipmentId": inv.ShipmentID},
		Products:  []map[string]any{}, Items: []TracingItem{}, Transactions: []TracingTransaction{}, NotFound: append([]string{}, inv.NotFound...)}
	epcs := inv.EPCs
	if len(epcs) > maxAnswerItems {
		epcs, ans.Truncated = epcs[:maxAnswerItems], true
	}
	related := map[string]bool{}
	gtins := map[string]bool{}
	for _, e := range epcs {
		st, err := trace.Lookup(ctx, tx, e)
		if err != nil {
			ans.NotFound = append(ans.NotFound, e)
			continue
		}
		anc, err := trace.Ancestors(ctx, tx, st)
		if err != nil {
			return ans, err
		}
		hist, err := trace.History(ctx, tx, e, 200)
		if err != nil {
			return ans, err
		}
		it := TracingItem{ID: st.EPC, State: st, Ancestors: []string{}, History: hist, Shipments: []uuid.UUID{}}
		related[st.EPC] = true
		for _, a := range anc {
			it.Ancestors = append(it.Ancestors, a.EPC)
			related[a.EPC] = true
		}
		if st.GTIN != nil {
			gtins[*st.GTIN] = true
		}
		ans.Items = append(ans.Items, it)
	}
	if inv.GTIN != "" {
		gtins[inv.GTIN] = true
	}
	// Products.
	gl := make([]string, 0, len(gtins))
	for g := range gtins {
		gl = append(gl, g)
	}
	sort.Strings(gl)
	products, err := masterdata.KnownGTINs(ctx, tx, gl)
	if err != nil {
		return ans, err
	}
	for _, g := range gl {
		if p, ok := products[g]; ok {
			ans.Products = append(ans.Products, map[string]any{"gtin": g, "name": p.Name, "ndc": p.NDC, "dosageForm": p.DosageForm, "strength": p.Strength, "packSize": p.PackSize})
		}
	}
	// Transactions: every shipment carrying an identifier in scope or one of its containers.
	relIDs := make([]string, 0, len(related))
	for e := range related {
		relIDs = append(relIDs, e)
	}
	ships, err := db.List[Shipment](ctx, tx, `select `+shipCols+` from shipment where epcs && $1 order by coalesce(sent_at, received_at, created_at), id`, relIDs)
	if err != nil {
		return ans, err
	}
	for _, s := range ships {
		ans.Transactions = append(ans.Transactions, m.tracingTransaction(ctx, tx, s))
		for i := range ans.Items {
			it := &ans.Items[i]
			for _, e := range s.EPCs {
				if e == it.ID || slices.Contains(it.Ancestors, e) {
					it.Shipments = append(it.Shipments, s.ID)
					break
				}
			}
		}
	}
	return ans, nil
}

func (m *Module) tracingTransaction(ctx context.Context, tx pgx.Tx, s Shipment) TracingTransaction {
	t := TracingTransaction{ShipmentID: s.ID, Direction: s.Direction, PONumber: s.PONumber, ASNNumber: s.ASNNumber, ShipDate: s.ShipDate, SentAt: s.SentAt,
		ReceivedAt: s.ReceivedAt, Status: s.Status, TITSSource: "unavailable"}
	if s.PartnerID != nil {
		if p, err := partners.Get(ctx, tx, *s.PartnerID); err == nil {
			t.Partner = &party{Name: p.Name}
			if p.GLN != nil {
				t.Partner.GLN = *p.GLN
			}
		}
	}
	doc := []byte(s.TITS)
	if len(doc) > 0 {
		t.TITSSource = "stored"
	} else if s.Direction == "outbound" && t.Partner != nil {
		d, err := m.BuildTITS(ctx, tx, s, sellerParty(ctx, tx), *t.Partner)
		if err != nil {
			t.Note = "TI/TS was never built for this shipment and cannot be rebuilt: " + err.Error()
			return t
		}
		doc, _ = lib.WriteJSON(d)
		t.TITSSource, t.Note = "rebuilt", "TI/TS rebuilt from current repository data; the shipment had no stored TI/TS"
	} else {
		t.Note = "no TI/TS on record for this shipment"
		return t
	}
	parsed, _, err := lib.Parse("application/ld+json", doc)
	if err != nil {
		t.TITSSource, t.Note = "unavailable", "stored TI/TS cannot be parsed: "+err.Error()
		return t
	}
	t.EventCount = len(parsed.Body.EventList)
	var h map[string]json.RawMessage
	if len(parsed.Header) > 0 && json.Unmarshal(parsed.Header, &h) == nil {
		if v, ok := h["dscsa:transactionInformation"]; ok {
			t.TransactionInformation = v
		}
		if v, ok := h["dscsa:transactionStatement"]; ok {
			t.TransactionStatement = v
		}
	}
	return t
}

func (m *Module) assembleTracingAnswer(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		b, _, err := m.AssembleTracingAnswer(ctx, tx, id)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b) //nolint:gosec // G705: server-built JSON with nosniff, not HTML
		return nil
	})
}

func (m *Module) getTracingAnswer(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var b []byte
		if err := tx.QueryRow(r.Context(), `select tracing_answer from investigation where id=$1`, id).Scan(&b); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.ErrNotFound
			}
			return err
		}
		if len(b) == 0 {
			return httpx.NotFound("no tracing answer assembled yet")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="tracing-answer-%s.json"`, id))
		_, _ = w.Write(b) //nolint:gosec // G705: stored server-built JSON with nosniff, not HTML
		return nil
	})
}

// --- hook-ins ---

// investigationFromAlert opens a suspect-product investigation from a public-scan alert
// (verify.*) or a failed VRS verification (dscsa.vrs_not_verified) and links the alert.
func (m *Module) investigationFromAlert(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		AlertID uuid.UUID `json:"alertId"`
		Kind    string    `json:"kind"`
		Title   string    `json:"title"`
		Note    string    `json:"note"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = KindSuspect
	}
	if in.Kind != KindSuspect && in.Kind != KindIllegitimate {
		return httpx.BadRequest("kind must be suspect or illegitimate")
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		var kind, title, status string
		var detail map[string]any
		if err := tx.QueryRow(ctx, `select kind, title, status, detail from alert where id=$1 for update`, in.AlertID).Scan(&kind, &title, &status, &detail); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("alert not found")
			}
			return err
		}
		if !strings.HasPrefix(kind, "verify.") && kind != "dscsa.vrs_not_verified" {
			return httpx.BadRequest("only suspect scan (verify.*) and VRS verification alerts open investigations")
		}
		if status != "open" {
			return httpx.Conflict("alert is already " + status)
		}
		g, s, lot := alertPack(detail)
		if g == "" || s == "" {
			return httpx.Unprocessable("alert does not identify a pack (gtin and serial)", nil)
		}
		aid := in.AlertID
		ii := InvestigationInput{Kind: in.Kind, Title: in.Title, GTIN: g, Serials: []string{s}, Note: in.Note, alertID: &aid}
		if ii.Title == "" {
			ii.Title = title
		}
		if lot != "" {
			ii.Note = strings.TrimSpace(ii.Note + " (lot on pack: " + lot + ")")
		}
		inv, err := m.CreateInvestigation(ctx, tx, ii)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update alert set status='resolved', resolved_at=now(), detail = detail || jsonb_build_object('investigationId', $2::text) where id=$1`,
			in.AlertID, inv.ID.String()); err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "alert.resolve", Entity: "alert", EntityID: in.AlertID.String(),
			After: map[string]any{"status": "resolved", "investigationId": inv.ID}, Reason: "opened as investigation"}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, inv)
		return nil
	})
}

// alertPack extracts the pack identity from a verify scan result or a VRS alert detail.
func alertPack(d map[string]any) (gtin, serial, lot string) {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	if p, ok := d["pack"].(map[string]any); ok {
		return str(p, "gtin"), str(p, "serial"), str(p, "lot")
	}
	return str(d, "gtin"), str(d, "serial"), str(d, "lot")
}

// raiseOverdue opens one alert per investigation past its due date (deduplicated while open).
func (m *Module) raiseOverdue(ctx context.Context) error {
	return m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			select gen_random_uuid(), i.tenant_id, 'dscsa.investigation_overdue', 'error', 'Investigation overdue: ' || i.title,
			       jsonb_build_object('investigationId', i.id, 'kind', i.kind, 'status', i.status, 'dueAt', i.due_at), 'investigation', i.id::text,
			       'investigation-overdue:' || i.id
			from investigation i where i.due_at < now() and i.status <> 'closed'
			on conflict do nothing`)
		return err
	})
}
