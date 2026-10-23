package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
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
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// Holder notification statuses.
const (
	HolderPending      = "pending"
	HolderSent         = "sent"
	HolderAcknowledged = "acknowledged"
)

// Scope is the snapshot of where a recalled lot was when the recall was scoped (or refreshed).
// Every number is a count of serials from the projection and the stored events.
type Scope struct {
	AsOf time.Time `json:"asOf"`
	// Packs is the number of serials of the lot that the repository knows.
	Packs int `json:"packs"`
	// OwnStock is held by this tenant: the last custody event is commissioning or receiving at one of
	// its own sites, or at a location that is not recorded.
	OwnStock int `json:"ownStock"`
	// AtHolders is shipped to a named destination and not recorded as having left it.
	AtHolders int `json:"atHolders"`
	// NoDestination is shipped, but the shipping event names no destination.
	NoDestination int `json:"noDestination"`
	// NoCustodyRecord has no commissioning, receiving or shipping event.
	NoCustodyRecord int `json:"noCustodyRecord"`
	// Ended counts packs recorded as decommissioned, destroyed or dispensed.
	Ended map[string]int `json:"ended"`
}

// RecallHolder is one party that holds recalled packs, with the notification status.
type RecallHolder struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	HolderID       string     `json:"holderId" db:"holder_id"`
	GLN            string     `json:"gln" db:"gln"`
	Name           string     `json:"name" db:"name"`
	PartnerID      *uuid.UUID `json:"partnerId,omitempty" db:"partner_id"`
	OnNetwork      bool       `json:"partnerOnNetwork" db:"-"`
	LinkedTenant   *uuid.UUID `json:"-" db:"linked_tenant"`
	Packs          int        `json:"packs" db:"packs"`
	SampleSerials  []string   `json:"sampleSerials" db:"sample_serials"`
	Channel        string     `json:"channel" db:"channel"`
	Status         string     `json:"status" db:"status"`
	Note           string     `json:"note" db:"note"`
	SentAt         *time.Time `json:"sentAt,omitempty" db:"sent_at"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty" db:"acknowledged_at"`
	UpdatedBy      string     `json:"updatedBy" db:"updated_by"`
}

const holderCols = `id, holder_id, gln, name, partner_id, linked_tenant, packs, sample_serials, channel, status, note, sent_at, acknowledged_at, updated_by`

// LotRecall is a pharma lot recall.
type LotRecall struct {
	ID             uuid.UUID      `json:"id" db:"id"`
	GTIN           string         `json:"gtin" db:"gtin"`
	Lot            string         `json:"lot" db:"lot"`
	Title          string         `json:"title" db:"title"`
	Reason         string         `json:"reason" db:"reason"`
	Classification string         `json:"classification" db:"classification"`
	Status         string         `json:"status" db:"status"`
	Scope          Scope          `json:"scope" db:"scope"`
	CreatedBy      string         `json:"createdBy" db:"created_by"`
	CreatedAt      time.Time      `json:"createdAt" db:"created_at"`
	ClosedAt       *time.Time     `json:"closedAt,omitempty" db:"closed_at"`
	Holders        []RecallHolder `json:"holders" db:"-"`
}

const recallCols = `id, gtin, lot, title, reason, classification, status, scope, created_by, created_at, closed_at`

type holderAgg struct {
	id      string
	packs   int
	serials []string
}

// Position is the result of tracing a lot forward.
type Position struct {
	Scope   Scope
	Holders []*holderAgg
}

func isDispensed(e evRow) bool {
	switch e.BizStep {
	case "dispensing", "retail_selling":
		return true
	}
	return e.Disposition == "dispensed" || e.Disposition == "retail_sold"
}

// Forward traces a lot forward from the events: the last custody event of each serial decides
// who holds it. Commissioning and receiving put it at the event's business location, a shipping
// event moves it to the event's first destination; decommissioning, destruction and dispensing
// end it, and events after an end are ignored. A holder whose GLN is in ownGLN (the tenant's own
// sites), or a location that is not recorded, counts as own stock.
func Forward(scope []scopeSerial, events map[string][]evRow, ownGLN map[string]bool, now time.Time) Position {
	pos := Position{Scope: Scope{AsOf: now, Ended: map[string]int{}}}
	by := map[string]*holderAgg{}
	for _, sc := range scope {
		pos.Scope.Packs++
		state, holder, ended := "none", "", ""
		for _, e := range events[sc.EPC] {
			if ended != "" {
				break
			}
			switch cat := classify(e); {
			case cat == CatCommissioning || cat == CatReceiving:
				state, holder = "held", ""
				if e.Location != "" {
					holder = lib.Canonical(e.Location)
				}
			case cat == CatShipping:
				state, holder = "shipped", ""
				if len(e.Dest) > 0 {
					holder = lib.Canonical(e.Dest[0])
				}
			case cat == CatDecommissioning:
				ended = "decommissioned"
			case cat == CatDestroying:
				ended = "destroyed"
			case isDispensed(e):
				ended = "dispensed"
			}
		}
		switch {
		case ended != "":
			pos.Scope.Ended[ended]++
		case state == "none":
			pos.Scope.NoCustodyRecord++
		case state == "shipped" && holder == "":
			pos.Scope.NoDestination++
		case holder == "" || ownGLN[glnOf(holder)]:
			pos.Scope.OwnStock++
		default:
			pos.Scope.AtHolders++
			h := by[holder]
			if h == nil {
				h = &holderAgg{id: holder}
				by[holder] = h
			}
			h.packs++
			if len(h.serials) < 5 {
				_, serial, _ := lib.Key(sc.EPC)
				h.serials = append(h.serials, serial)
			}
		}
	}
	for _, h := range by {
		pos.Holders = append(pos.Holders, h)
	}
	sort.Slice(pos.Holders, func(i, j int) bool {
		if pos.Holders[i].packs != pos.Holders[j].packs {
			return pos.Holders[i].packs > pos.Holders[j].packs
		}
		return pos.Holders[i].id < pos.Holders[j].id
	})
	return pos
}

type directoryEntry struct {
	name         string
	partnerID    *uuid.UUID
	linkedTenant *uuid.UUID
}

// resolveHolders maps GLNs to names through the tenant's partners, then the published directory.
func (m *Module) resolveHolders(ctx context.Context, tx pgx.Tx, glns []string) (map[string]directoryEntry, error) {
	out := map[string]directoryEntry{}
	if len(glns) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select gln, name, id, linked_tenant from partner where tenant_id = app_tenant() and gln = any($1)`, glns)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var gln, name string
		var id uuid.UUID
		var lt *uuid.UUID
		if err := rows.Scan(&gln, &name, &id, &lt); err != nil {
			rows.Close()
			return nil, err
		}
		pid := id
		out[gln] = directoryEntry{name: name, partnerID: &pid, linkedTenant: lt}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var rest []string
	for _, g := range glns {
		if _, ok := out[g]; !ok {
			rest = append(rest, g)
		}
	}
	if len(rest) == 0 {
		return out, nil
	}
	err = m.DB.OperatorTx(ctx, func(otx pgx.Tx) error {
		rows, err := otx.Query(ctx, `select gln, name from directory_listing where published and gln = any($1)`, rest)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var gln, name string
			if err := rows.Scan(&gln, &name); err != nil {
				return err
			}
			if _, ok := out[gln]; !ok {
				out[gln] = directoryEntry{name: name}
			}
		}
		return rows.Err()
	})
	return out, err
}

func (m *Module) scopeHolders(ctx context.Context, tx pgx.Tx, gtin, lot string) (Position, map[string]directoryEntry, error) {
	scope, events, _, err := scopeEvents(ctx, tx, gtin, lot)
	if err != nil {
		return Position{}, nil, err
	}
	own := map[string]bool{}
	srows, err := tx.Query(ctx, `select gln from site where tenant_id = app_tenant()`)
	if err != nil {
		return Position{}, nil, err
	}
	for srows.Next() {
		var g string
		if err := srows.Scan(&g); err != nil {
			srows.Close()
			return Position{}, nil, err
		}
		own[g] = true
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return Position{}, nil, err
	}
	pos := Forward(scope, events, own, time.Now().UTC())
	var glns []string
	for _, h := range pos.Holders {
		if g := glnOf(h.id); g != "" {
			glns = append(glns, g)
		}
	}
	dir, err := m.resolveHolders(ctx, tx, glns)
	return pos, dir, err
}

func (m *Module) recallRoutes(r chi.Router) {
	r.Get("/recalls", httpx.Handler(m.listRecalls))
	r.Get("/recalls/{id}", httpx.Handler(m.getRecall))
	r.Get("/recalls/{id}/report", httpx.Handler(m.recallReport))
	w := r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance))
	w.Post("/recalls", httpx.Handler(m.createRecall))
	w.Post("/recalls/{id}/refresh", httpx.Handler(m.refreshRecall))
	w.Post("/recalls/{id}/notify", httpx.Handler(m.notifyRecall))
	w.Post("/recalls/{id}/holders/{holder}/sent", httpx.Handler(m.holderSent))
	w.Post("/recalls/{id}/holders/{holder}/acknowledge", httpx.Handler(m.holderAck))
	w.Post("/recalls/{id}/close", httpx.Handler(m.closeRecall))
}

func (m *Module) createRecall(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		GTIN           string `json:"gtin"`
		Lot            string `json:"lot"`
		Title          string `json:"title"`
		Reason         string `json:"reason"`
		Classification string `json:"classification"`
		// MarkPacks sets the projected status of the lot's packs still in custody to recalled, so a
		// public scan answers "recalled" (default true).
		MarkPacks *bool `json:"markPacks"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	gtin, err := normalizeGTIN(in.GTIN)
	if err != nil {
		return err
	}
	in.Lot, in.Title, in.Reason = strings.TrimSpace(in.Lot), strings.TrimSpace(in.Title), strings.TrimSpace(in.Reason)
	if in.Lot == "" || in.Reason == "" {
		return httpx.BadRequest("lot and reason are required")
	}
	if in.Classification != "" && in.Classification != "I" && in.Classification != "II" && in.Classification != "III" {
		return httpx.BadRequest("classification must be I, II or III, or empty")
	}
	if in.Title == "" {
		in.Title = "Recall of " + gtin + " lot " + in.Lot
	}
	mark := in.MarkPacks == nil || *in.MarkPacks
	ctx := r.Context()
	p := auth.MustFrom(ctx)
	var rc LotRecall
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		pos, dir, err := m.scopeHolders(ctx, tx, gtin, in.Lot)
		if err != nil {
			return err
		}
		if pos.Scope.Packs == 0 {
			return httpx.NotFound("no serials of this lot are known in this repository")
		}
		if n, err := db.Count(ctx, tx, `select count(*) from lot_recall where tenant_id = app_tenant() and gtin=$1 and lot=$2 and status='open'`, gtin, in.Lot); err != nil {
			return err
		} else if n > 0 {
			return httpx.Conflict("an open recall for this lot exists; refresh or close it")
		}
		rc, err = db.One[LotRecall](ctx, tx, `insert into lot_recall (id, tenant_id, gtin, lot, title, reason, classification, scope, created_by)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8) returning `+recallCols, ids.New(), gtin, in.Lot, in.Title, in.Reason, in.Classification, pos.Scope, p.AuditActor())
		if err != nil {
			return err
		}
		for _, h := range pos.Holders {
			if err := insertHolder(ctx, tx, rc.ID, h, dir); err != nil {
				return err
			}
		}
		var marked int64
		if mark {
			ct, err := tx.Exec(ctx, `update serial_state set status='recalled', updated_at=now()
				where tenant_id = app_tenant() and gtin=$1 and lot=$2 and status not in ('decommissioned','destroyed','dispensed','recalled')`, gtin, in.Lot)
			if err != nil {
				return err
			}
			marked = ct.RowsAffected()
		}
		if _, err := raiseAlert(ctx, tx, "trace.lot_recall", "error", "Recall: "+in.Title,
			map[string]any{"recallId": rc.ID, "gtin": gtin, "lot": in.Lot, "reason": in.Reason, "scope": pos.Scope}, rc.ID.String(), "lotrecall:"+rc.ID.String()); err != nil {
			return err
		}
		if rc.Holders, err = db.List[RecallHolder](ctx, tx, `select `+holderCols+` from lot_recall_holder where tenant_id = app_tenant() and recall_id=$1 order by packs desc, holder_id`, rc.ID); err != nil {
			return err
		}
		_, err = audit.Write(ctx, tx, audit.Record{Action: "trace.recall.create", Entity: "lot_recall", EntityID: rc.ID.String(),
			After: map[string]any{"gtin": gtin, "lot": in.Lot, "classification": in.Classification, "scope": pos.Scope, "holders": len(pos.Holders), "packsMarkedRecalled": marked}, Reason: in.Reason})
		return err
	})
	if err != nil {
		return err
	}
	if err := m.sendNetwork(ctx, rc.ID); err != nil {
		return err
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		out, err := loadRecall(ctx, tx, rc.ID)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, out)
		}
		return err
	})
}

func insertHolder(ctx context.Context, tx pgx.Tx, recall uuid.UUID, h *holderAgg, dir map[string]directoryEntry) error {
	gln := glnOf(h.id)
	d := dir[gln]
	name := d.name
	if name == "" {
		name = h.id
	}
	_, err := tx.Exec(ctx, `insert into lot_recall_holder (id, tenant_id, recall_id, holder_id, gln, name, partner_id, linked_tenant, packs, sample_serials)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9)
		on conflict (recall_id, holder_id) do update set packs=excluded.packs, sample_serials=excluded.sample_serials, name=excluded.name`,
		ids.New(), recall, h.id, gln, name, d.partnerID, d.linkedTenant, h.packs, h.serials)
	return err
}

func loadRecall(ctx context.Context, tx pgx.Tx, id uuid.UUID) (LotRecall, error) {
	rc, err := db.One[LotRecall](ctx, tx, `select `+recallCols+` from lot_recall where tenant_id = app_tenant() and id=$1`, id)
	if err != nil {
		return rc, err
	}
	rc.Holders, err = db.List[RecallHolder](ctx, tx, `select `+holderCols+` from lot_recall_holder where tenant_id = app_tenant() and recall_id=$1 order by packs desc, holder_id`, id)
	for i := range rc.Holders {
		rc.Holders[i].OnNetwork = rc.Holders[i].LinkedTenant != nil
	}
	return rc, err
}

// sendNetwork puts the recall into the alert inbox of partners that run on this node and marks
// them as notified. Holders that are not on the network stay pending until a person sends the
// notice by other means and records it.
func (m *Module) sendNetwork(ctx context.Context, id uuid.UUID) error {
	p := auth.MustFrom(ctx)
	var rc LotRecall
	var from string
	if err := m.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		if rc, err = loadRecall(ctx, tx, id); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select name from tenant where id=app_tenant()`).Scan(&from)
	}); err != nil {
		return err
	}
	if rc.Status != "open" {
		return nil
	}
	for _, h := range rc.Holders {
		if h.Status != HolderPending || h.LinkedTenant == nil {
			continue
		}
		err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
				values ($1,$2,'recall.received','error',$3,$4,'recall',$5,$6) on conflict do nothing`,
				ids.New(), *h.LinkedTenant, fmt.Sprintf("Recall from %s: %s", from, rc.Title),
				map[string]any{"from": from, "gtin": rc.GTIN, "lot": rc.Lot, "reason": rc.Reason, "classification": rc.Classification, "packs": h.Packs},
				rc.ID.String(), "lotrecall-in:"+rc.ID.String()+":"+h.LinkedTenant.String())
			return err
		})
		if err != nil {
			return err
		}
		err = m.Tx(ctx, func(tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, `update lot_recall_holder set status='sent', channel='network', sent_at=now(), updated_by=$3
				where tenant_id = app_tenant() and recall_id=$1 and id=$2 and status='pending'`, rc.ID, h.ID, p.AuditActor())
			if err != nil || ct.RowsAffected() == 0 {
				return err
			}
			_, err = audit.Write(ctx, tx, audit.Record{Action: "trace.recall.notify", Entity: "lot_recall", EntityID: rc.ID.String(),
				After: map[string]any{"holder": h.HolderID, "name": h.Name, "channel": "network"}})
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) listRecalls(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[LotRecall](r.Context(), tx, `select `+recallCols+` from lot_recall where tenant_id = app_tenant()
			and ($1='' or gtin=$1) and ($2='' or lot=$2) and ($3='' or status=$3) order by created_at desc limit 200`, q.Get("gtin"), q.Get("lot"), q.Get("status"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[LotRecall]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getRecall(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rc, err := loadRecall(r.Context(), tx, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, rc)
		}
		return err
	})
}

func (m *Module) refreshRecall(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		rc, err := loadRecall(ctx, tx, id)
		if err != nil {
			return err
		}
		if rc.Status != "open" {
			return httpx.Conflict("recall is closed")
		}
		pos, dir, err := m.scopeHolders(ctx, tx, rc.GTIN, rc.Lot)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update lot_recall_holder set packs=0, sample_serials='{}' where tenant_id = app_tenant() and recall_id=$1`, id); err != nil {
			return err
		}
		for _, h := range pos.Holders {
			if err := insertHolder(ctx, tx, id, h, dir); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update lot_recall set scope=$2 where tenant_id = app_tenant() and id=$1`, id, pos.Scope); err != nil {
			return err
		}
		_, err = audit.Write(ctx, tx, audit.Record{Action: "trace.recall.refresh", Entity: "lot_recall", EntityID: id.String(), Before: rc.Scope, After: pos.Scope})
		return err
	})
	if err != nil {
		return err
	}
	if err := m.sendNetwork(ctx, id); err != nil {
		return err
	}
	return m.getRecall(w, r)
}

func (m *Module) notifyRecall(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	if err := m.sendNetwork(r.Context(), id); err != nil {
		return err
	}
	return m.getRecall(w, r)
}

func (m *Module) holderTransition(w http.ResponseWriter, r *http.Request, to string) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	hid, err := ids.Param(r, "holder")
	if err != nil {
		return err
	}
	var in struct {
		Channel string `json:"channel"`
		Note    string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	if len(in.Note) > 1000 || len(in.Channel) > 50 {
		return httpx.BadRequest("note is limited to 1000 characters and channel to 50")
	}
	ctx := r.Context()
	p := auth.MustFrom(ctx)
	return m.Tx(ctx, func(tx pgx.Tx) error {
		rc, err := db.One[LotRecall](ctx, tx, `select `+recallCols+` from lot_recall where tenant_id = app_tenant() and id=$1`, id)
		if err != nil {
			return err
		}
		if rc.Status != "open" {
			return httpx.Conflict("recall is closed")
		}
		h, err := db.One[RecallHolder](ctx, tx, `select `+holderCols+` from lot_recall_holder where tenant_id = app_tenant() and recall_id=$1 and id=$2 for update`, id, hid)
		if err != nil {
			return err
		}
		from := h.Status
		switch {
		case to == HolderSent && from != HolderPending:
			return httpx.Conflict("only a pending notice can be marked sent")
		case to == HolderAcknowledged && from == HolderAcknowledged:
			return httpx.Conflict("already acknowledged")
		}
		channel := h.Channel
		if in.Channel != "" {
			channel = in.Channel
		} else if channel == "" {
			channel = "manual"
		}
		if _, err := tx.Exec(ctx, `update lot_recall_holder set status=$3, channel=$4, note=case when $5='' then note else $5 end, updated_by=$6,
			sent_at = case when $3='sent' then now() else sent_at end, acknowledged_at = case when $3='acknowledged' then now() else acknowledged_at end
			where tenant_id = app_tenant() and recall_id=$1 and id=$2`, id, hid, to, channel, in.Note, p.AuditActor()); err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "trace.recall.holder." + to, Entity: "lot_recall", EntityID: id.String(),
			Before: map[string]any{"holder": h.HolderID, "status": from}, After: map[string]any{"holder": h.HolderID, "status": to, "channel": channel}, Reason: in.Note}); err != nil {
			return err
		}
		out, err := loadRecall(ctx, tx, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, out)
		}
		return err
	})
}

func (m *Module) holderSent(w http.ResponseWriter, r *http.Request) error {
	return m.holderTransition(w, r, HolderSent)
}

func (m *Module) holderAck(w http.ResponseWriter, r *http.Request) error {
	return m.holderTransition(w, r, HolderAcknowledged)
}

func (m *Module) closeRecall(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `update lot_recall set status='closed', closed_at=now() where tenant_id = app_tenant() and id=$1 and status='open'`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.Conflict("recall is closed or does not exist")
		}
		if _, err := resolveAlerts(ctx, tx, "lotrecall:"+id.String()); err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "trace.recall.close", Entity: "lot_recall", EntityID: id.String(), Reason: in.Reason}); err != nil {
			return err
		}
		out, err := loadRecall(ctx, tx, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, out)
		}
		return err
	})
}

// --- report ---

// RecallReport is the exportable recall report. "data" is what the repository holds and what
// people entered; "helper" is what the software derived from it.
type RecallReport struct {
	ReportType   string    `json:"reportType"`
	Version      int       `json:"version"`
	GeneratedAt  time.Time `json:"generatedAt"`
	Organization string    `json:"organization"`
	Data         struct {
		Recall  recallFacts    `json:"recall"`
		Scope   Scope          `json:"scope"`
		Holders []RecallHolder `json:"holders"`
		Sources []string       `json:"sources"`
	} `json:"data"`
	Helper struct {
		Derived      derivedFacts `json:"derived"`
		Explanations []string     `json:"explanations"`
	} `json:"helper"`
	ResponseTime responseTime `json:"responseTime"`
	Limitations  []string     `json:"limitations"`
}

type recallFacts struct {
	ID             uuid.UUID  `json:"id"`
	GTIN           string     `json:"gtin"`
	Lot            string     `json:"lot"`
	Title          string     `json:"title"`
	Reason         string     `json:"reason"`
	Classification string     `json:"classification"`
	Status         string     `json:"status"`
	CreatedBy      string     `json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	ClosedAt       *time.Time `json:"closedAt,omitempty"`
}

type derivedFacts struct {
	HoldersTotal              int      `json:"holdersTotal"`
	Pending                   int      `json:"pending"`
	Sent                      int      `json:"sent"`
	Acknowledged              int      `json:"acknowledged"`
	PacksAtHolders            int      `json:"packsAtHolders"`
	PacksAcknowledged         int      `json:"packsAcknowledged"`
	HoursSinceRecall          float64  `json:"hoursSinceRecall"`
	OldestUnacknowledgedHours *float64 `json:"oldestUnacknowledgedHours,omitempty"`
}

type responseTime struct {
	Configured bool   `json:"configured"`
	Note       string `json:"note"`
}

const responseTimeNote = "TODO(spec): response-time rules for recalls (when holders must be notified or must respond, per class and market) are not implemented; no deadline is computed or checked. Elapsed times above are informational."

// BuildRecallReport assembles the report for a recall at time now.
func BuildRecallReport(rc LotRecall, org string, now time.Time) RecallReport {
	var rep RecallReport
	rep.ReportType, rep.Version, rep.GeneratedAt, rep.Organization = "openspoor.lot_recall_report", 1, now.UTC(), org
	rep.Data.Recall = recallFacts{ID: rc.ID, GTIN: rc.GTIN, Lot: rc.Lot, Title: rc.Title, Reason: rc.Reason, Classification: rc.Classification,
		Status: rc.Status, CreatedBy: rc.CreatedBy, CreatedAt: rc.CreatedAt, ClosedAt: rc.ClosedAt}
	rep.Data.Scope = rc.Scope
	rep.Data.Holders = rc.Holders
	if rep.Data.Holders == nil {
		rep.Data.Holders = []RecallHolder{}
	}
	rep.Data.Sources = []string{"EPCIS events stored in this repository", "serial projection (serial_state)", "partner records and published directory for holder names", "notification status entered or recorded by users of this system"}
	d := derivedFacts{HoldersTotal: len(rc.Holders), HoursSinceRecall: round1(now.Sub(rc.CreatedAt).Hours())}
	for _, h := range rc.Holders {
		d.PacksAtHolders += h.Packs
		switch h.Status {
		case HolderPending:
			d.Pending++
		case HolderSent:
			d.Sent++
		case HolderAcknowledged:
			d.Acknowledged++
			d.PacksAcknowledged += h.Packs
		}
		if h.Status != HolderAcknowledged && h.Packs > 0 {
			hrs := round1(now.Sub(rc.CreatedAt).Hours())
			if h.SentAt != nil {
				hrs = round1(now.Sub(*h.SentAt).Hours())
			}
			if d.OldestUnacknowledgedHours == nil || hrs > *d.OldestUnacknowledgedHours {
				v := hrs
				d.OldestUnacknowledgedHours = &v
			}
		}
	}
	rep.Helper.Derived = d
	rep.Helper.Explanations = []string{
		"The holders are derived: a pack counts as held by the destination named in its last shipping event, or by the location of a later receiving event, unless an end event (decommissioning, destruction, dispensing) is stored. Aggregation (pallets, cases) is followed through stored aggregation events.",
		"Packs without a named destination, or without any custody event, are listed in the scope and are not attributed to a holder. A pack received at one of the tenant's own sites counts as own stock.",
		"Holder names come from this tenant's partner records or the published partner directory; where neither matches, the identifier from the event is shown.",
		"Elapsed times are measured from the recall creation or the recorded send time to the time of this report.",
	}
	rep.ResponseTime = responseTime{Configured: false, Note: responseTimeNote}
	rep.Limitations = []string{
		"The report covers the events captured in this repository. Packs that left through parties that did not share events are not traced.",
		"Acknowledgements are recorded by users of this system; the holder did not necessarily confirm electronically.",
		"The classification is entered by the user; the software does not decide it.",
		"TODO(spec): the content and format a competent authority requires for a recall report is not specified here; this report is an evidence summary, not a regulatory submission.",
	}
	return rep
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

var reportTmpl = template.Must(template.New("recall").Funcs(template.FuncMap{
	"ts": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	},
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Lot recall report</title>
<style>body{font:14px/1.5 sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem}table{border-collapse:collapse;width:100%}th,td{border:1px solid #bbb;padding:.3rem .5rem;text-align:left}h2{margin-top:2rem}.tag{font-size:12px;border:1px solid #888;padding:0 .4rem;border-radius:3px}</style></head><body>
<h1>Lot recall report</h1>
<p>{{.Organization}} &middot; generated {{.GeneratedAt.Format "2006-01-02T15:04:05Z07:00"}}</p>
<h2>Data <span class="tag">stored or entered</span></h2>
<p>Everything in this section is held in the repository or was entered by a user.</p>
<table><tr><th>GTIN</th><td>{{.Data.Recall.GTIN}}</td></tr><tr><th>Lot</th><td>{{.Data.Recall.Lot}}</td></tr><tr><th>Title</th><td>{{.Data.Recall.Title}}</td></tr>
<tr><th>Reason</th><td>{{.Data.Recall.Reason}}</td></tr><tr><th>Classification (entered)</th><td>{{.Data.Recall.Classification}}</td></tr>
<tr><th>Status</th><td>{{.Data.Recall.Status}}</td></tr><tr><th>Created</th><td>{{.Data.Recall.CreatedAt.Format "2006-01-02T15:04:05Z07:00"}} by {{.Data.Recall.CreatedBy}}</td></tr>
<tr><th>Closed</th><td>{{ts .Data.Recall.ClosedAt}}</td></tr></table>
<h3>Scope as of {{.Data.Scope.AsOf.Format "2006-01-02T15:04:05Z07:00"}}</h3>
<table><tr><th>Packs known</th><th>Own stock</th><th>At holders</th><th>Shipped, no destination</th><th>No custody record</th><th>Ended</th></tr>
<tr><td>{{.Data.Scope.Packs}}</td><td>{{.Data.Scope.OwnStock}}</td><td>{{.Data.Scope.AtHolders}}</td><td>{{.Data.Scope.NoDestination}}</td><td>{{.Data.Scope.NoCustodyRecord}}</td><td>{{range $k, $v := .Data.Scope.Ended}}{{$k}}: {{$v}} {{end}}</td></tr></table>
<h3>Holders and notification status</h3>
<table><tr><th>Holder</th><th>GLN</th><th>Packs</th><th>Status</th><th>Channel</th><th>Sent</th><th>Acknowledged</th><th>Note</th></tr>
{{range .Data.Holders}}<tr><td>{{.Name}}</td><td>{{.GLN}}</td><td>{{.Packs}}</td><td>{{.Status}}</td><td>{{.Channel}}</td><td>{{ts .SentAt}}</td><td>{{ts .AcknowledgedAt}}</td><td>{{.Note}}</td></tr>{{else}}<tr><td colspan="8">No holders.</td></tr>{{end}}</table>
<p>Sources: {{range $i, $s := .Data.Sources}}{{if $i}}; {{end}}{{$s}}{{end}}.</p>
<h2>Helper <span class="tag">derived by the software</span></h2>
<p>Holders {{.Helper.Derived.HoldersTotal}} (pending {{.Helper.Derived.Pending}}, sent {{.Helper.Derived.Sent}}, acknowledged {{.Helper.Derived.Acknowledged}}); packs at holders {{.Helper.Derived.PacksAtHolders}}, of which acknowledged {{.Helper.Derived.PacksAcknowledged}}; hours since recall {{.Helper.Derived.HoursSinceRecall}}.</p>
<ul>{{range .Helper.Explanations}}<li>{{.}}</li>{{end}}</ul>
<h2>Response time</h2><p>{{.ResponseTime.Note}}</p>
<h2>Limitations</h2><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>
</body></html>`))

func (m *Module) recallReport(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "html" {
		return httpx.BadRequest("format must be json or html")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rc, err := loadRecall(r.Context(), tx, id)
		if err != nil {
			return err
		}
		var org string
		if err := tx.QueryRow(r.Context(), `select name from tenant where id=app_tenant()`).Scan(&org); err != nil {
			return err
		}
		rep := BuildRecallReport(rc, org, time.Now())
		name := "recall-" + rc.GTIN + "-" + sanitizeFile(rc.Lot)
		if format == "html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.html"`)
			return reportTmpl.Execute(w, rep)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
		b, _ := json.MarshalIndent(rep, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return nil
	})
}

func sanitizeFile(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}
