package food

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// PharmaLot identifies a pharmaceutical batch to recall.
type PharmaLot struct {
	GTIN string `json:"gtin"`
	Lot  string `json:"lot"`
}

// Customer is a downstream recipient of recalled product.
type Customer struct {
	Name     string   `json:"name"`
	GLN      string   `json:"gln,omitempty"`
	TLCs     []string `json:"tlcs"`
	Quantity float64  `json:"quantity"`
	Partner  bool     `json:"partnerOnNetwork"`
}

// Recall is a recall case.
type Recall struct {
	ID                uuid.UUID      `json:"id" db:"id"`
	Title             string         `json:"title" db:"title"`
	Reason            string         `json:"reason" db:"reason"`
	Classification    string         `json:"classification" db:"classification"`
	Status            string         `json:"status" db:"status"`
	TLCs              []string       `json:"tlcs" db:"tlcs"`
	PharmaLots        []PharmaLot    `json:"pharmaLots" db:"pharma_lots"`
	Affected          map[string]any `json:"affected" db:"affected"`
	CreatedAt         time.Time      `json:"createdAt" db:"created_at"`
	ClosedAt          *time.Time     `json:"closedAt,omitempty" db:"closed_at"`
	AffectedLots      []string       `json:"affectedLots" db:"-"`
	AffectedCustomers []Customer     `json:"affectedCustomers" db:"-"`
	Notifications     []notification `json:"notifications,omitempty" db:"-"`
}

type notification struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	CustomerName   string     `json:"customerName" db:"customer_name"`
	CustomerGLN    string     `json:"customerGln" db:"customer_gln"`
	Channel        string     `json:"channel" db:"channel"`
	Status         string     `json:"status" db:"status"`
	SentAt         time.Time  `json:"sentAt" db:"sent_at"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty" db:"acknowledged_at"`
}

const recallCols = `id, title, reason, classification, status, tlcs, pharma_lots, affected, created_at, closed_at`

func (rc *Recall) expand() {
	if v, ok := rc.Affected["lots"].([]any); ok {
		for _, x := range v {
			rc.AffectedLots = append(rc.AffectedLots, fmt.Sprint(x))
		}
	}
	if v, ok := rc.Affected["customers"]; ok {
		b, _ := json.Marshal(v)
		_ = json.Unmarshal(b, &rc.AffectedCustomers)
	}
	if rc.AffectedLots == nil {
		rc.AffectedLots = []string{}
	}
	if rc.AffectedCustomers == nil {
		rc.AffectedCustomers = []Customer{}
	}
}

// createRecall scopes a recall: forward lineage of the lots (everything made from them),
// every customer they were shipped to, and pharma serials in the recalled batches. Lots are
// marked recalled, customers are notified (partners on this node get an alert in their inbox).
func (m *Module) createRecall(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Title          string      `json:"title"`
		Reason         string      `json:"reason"`
		Classification string      `json:"classification"`
		TLCs           []string    `json:"tlcs"`
		PharmaLots     []PharmaLot `json:"pharmaLots"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Title == "" || in.Reason == "" || len(in.TLCs)+len(in.PharmaLots) == 0 {
		return httpx.BadRequest("title, reason and at least one lot are required")
	}
	if in.Classification == "" {
		in.Classification = "II"
	}
	if in.Classification != "I" && in.Classification != "II" && in.Classification != "III" {
		return httpx.BadRequest("classification must be I, II or III")
	}
	if in.PharmaLots == nil {
		in.PharmaLots = []PharmaLot{}
	}
	if in.TLCs == nil {
		in.TLCs = []string{}
	}
	ctx := r.Context()
	var rc Recall
	var notify []Customer
	err := m.Tx(ctx, func(tx pgx.Tx) error {
		lots, err := lineage(ctx, tx, in.TLCs, true)
		if err != nil {
			return err
		}
		if len(in.TLCs) == 0 {
			lots = []string{}
		}
		customers, err := shippedTo(ctx, tx, lots)
		if err != nil {
			return err
		}
		var packs int64
		for _, pl := range in.PharmaLots {
			ct, err := tx.Exec(ctx, `update serial_state set status='recalled', updated_at=now() where gtin=$1 and lot=$2`, gs1.NormalizeGTIN(pl.GTIN), pl.Lot)
			if err != nil {
				return err
			}
			packs += ct.RowsAffected()
		}
		if len(lots) > 0 {
			if _, err := tx.Exec(ctx, `update food_lot set status='recalled' where tlc = any($1)`, lots); err != nil {
				return err
			}
		}
		affected := map[string]any{"lots": lots, "customers": customers, "pharmaPacks": packs}
		rc, err = db.One[Recall](ctx, tx, `insert into recall (id, tenant_id, title, reason, classification, status, tlcs, pharma_lots, affected) values ($1, app_tenant(), $2,$3,$4,'notifying',$5,$6,$7)
			returning `+recallCols, ids.New(), in.Title, in.Reason, in.Classification, in.TLCs, in.PharmaLots, affected)
		if err != nil {
			return err
		}
		for _, c := range customers {
			channel := "email"
			if c.Partner {
				channel = "network"
			}
			if _, err := tx.Exec(ctx, `insert into recall_notification (id, tenant_id, recall_id, customer_name, customer_gln, channel) values ($1, app_tenant(), $2,$3,$4,$5)`,
				ids.New(), rc.ID, c.Name, c.GLN, channel); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id) values ($1, app_tenant(), 'recall.open', 'error', $2, $3, 'recall', $4)`,
			ids.New(), "Recall: "+in.Title, affected, rc.ID.String()); err != nil {
			return err
		}
		notify = customers
		_, err = audit.Write(ctx, tx, audit.Record{Action: "recall.create", Entity: "recall", EntityID: rc.ID.String(), After: rc, Reason: in.Reason})
		return err
	})
	if err != nil {
		return err
	}
	m.notifyPartners(ctx, rc, notify)
	rc.expand()
	httpx.JSON(w, http.StatusCreated, rc)
	return nil
}

// shippedTo aggregates shipping CTEs of the lots by recipient.
func shippedTo(ctx context.Context, tx pgx.Tx, lots []string) ([]Customer, error) {
	if len(lots) == 0 {
		return []Customer{}, nil
	}
	rows, err := tx.Query(ctx, `select c.ship_to, max(c.ship_to_name), array_agg(distinct c.tlc), sum(c.quantity),
			exists(select 1 from partner p where p.gln = c.ship_to and p.linked_tenant is not null)
		from food_cte c where c.type='shipping' and c.tlc = any($1) group by c.ship_to order by 2`, lots)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Customer{}
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.GLN, &c.Name, &c.TLCs, &c.Quantity, &c.Partner); err != nil {
			return nil, err
		}
		if c.Name == "" {
			c.Name = c.GLN
		}
		sort.Strings(c.TLCs)
		out = append(out, c)
	}
	return out, rows.Err()
}

// notifyPartners puts the recall into the alert inbox of partner tenants on this node.
func (m *Module) notifyPartners(ctx context.Context, rc Recall, customers []Customer) {
	from := ""
	_ = m.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select name from tenant where id=app_tenant()`).Scan(&from)
	})
	for _, c := range customers {
		if !c.Partner {
			continue
		}
		_ = m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
			var tenant uuid.UUID
			if err := tx.QueryRow(ctx, `select linked_tenant from partner where gln=$1 and linked_tenant is not null limit 1`, c.GLN).Scan(&tenant); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id) values ($1,$2,'recall.received','error',$3,$4,'recall',$5)`,
				ids.New(), tenant, fmt.Sprintf("Recall from %s: %s", from, rc.Title), map[string]any{"from": from, "reason": rc.Reason, "classification": rc.Classification, "lots": c.TLCs, "quantity": c.Quantity}, rc.ID.String())
			return err
		})
	}
}

func (m *Module) listRecalls(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Recall](r.Context(), tx, `select `+recallCols+` from recall order by created_at desc`)
		for i := range items {
			items[i].expand()
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Recall]{Items: items, Total: len(items)})
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
		rc, err := db.One[Recall](r.Context(), tx, `select `+recallCols+` from recall where id=$1`, id)
		if err != nil {
			return err
		}
		rc.expand()
		rc.Notifications, err = db.List[notification](r.Context(), tx, `select id, customer_name, customer_gln, channel, status, sent_at, acknowledged_at from recall_notification where recall_id=$1 order by customer_name`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, rc)
		}
		return err
	})
}

func (m *Module) closeRecall(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = httpx.Decode(r, &in)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rc, err := db.One[Recall](r.Context(), tx, `update recall set status='closed', closed_at=now() where id=$1 returning `+recallCols, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `update alert set status='resolved', resolved_at=now() where entity='recall' and entity_id=$1 and status='open'`, id.String()); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "recall.close", Entity: "recall", EntityID: id.String(), Reason: in.Reason}); err != nil {
			return err
		}
		rc.expand()
		httpx.JSON(w, http.StatusOK, rc)
		return nil
	})
}

// --- EUDR due diligence statements ---

// DDS is an EUDR due diligence statement draft.
type DDS struct {
	ID                  uuid.UUID      `json:"id" db:"id"`
	Reference           string         `json:"reference" db:"reference"`
	OperatorRole        string         `json:"operatorRole" db:"operator_role"`
	Commodity           string         `json:"commodity" db:"commodity"`
	HSCode              string         `json:"hsCode" db:"hs_code"`
	Description         string         `json:"description" db:"description"`
	Quantity            float64        `json:"quantity" db:"quantity"`
	UOM                 string         `json:"uom" db:"uom"`
	CountryOfProduction string         `json:"countryOfProduction" db:"country_of_production"`
	PlotIDs             []uuid.UUID    `json:"plotIds" db:"plot_ids"`
	Status              string         `json:"status" db:"status"`
	Payload             map[string]any `json:"payload" db:"payload"`
	SubmissionRef       string         `json:"submissionRef" db:"submission_ref"`
	// Kind is "statement" (made by the operator) or "upstream_reference" (a downstream trader's
	// record of the upstream statement, see eudr.go).
	Kind              string    `json:"kind" db:"kind"`
	UpstreamReference string    `json:"upstreamReference" db:"upstream_reference"`
	UpstreamOperator  string    `json:"upstreamOperator" db:"upstream_operator"`
	CreatedAt         time.Time `json:"createdAt" db:"created_at"`
}

const ddsCols = `id, reference, operator_role, commodity, hs_code, description, quantity, uom, country_of_production, plot_ids, status, payload, submission_ref, kind, upstream_reference, upstream_operator, created_at`

func (m *Module) listDDS(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[DDS](r.Context(), tx, `select `+ddsCols+` from eudr_dds order by created_at desc`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[DDS]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getDDS(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := db.One[DDS](r.Context(), tx, `select `+ddsCols+` from eudr_dds where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, d)
		}
		return err
	})
}

// submitDDS is the stubbed EU Information System connector: the statement is marked
// submitted with a local reference until a real TRACES client is configured.
func (m *Module) submitDDS(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ref := "LOCAL-" + strings.ToUpper(ids.Secret(6))
		d, err := db.One[DDS](r.Context(), tx, `update eudr_dds set status='submitted_stub', submission_ref=$2 where id=$1 and status='ready' returning `+ddsCols, id, ref)
		if err != nil {
			return httpx.Conflict("only ready statements can be submitted")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "eudr.dds.submit", Entity: "eudr_dds", EntityID: id.String(), After: map[string]string{"ref": ref, "mode": "stub"}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"dds": d, "note": "TODO(spec): EU Information System submission is stubbed; reference is local"})
		return nil
	})
}
