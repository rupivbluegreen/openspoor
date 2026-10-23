package trace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Settings are a tenant's reconciliation and checkpoint settings.
type Settings struct {
	// ToleranceUnits and TolerancePercent: a lot closes when its discrepancy is at most the
	// larger of the two allowances (percent of the packs that entered custody, rounded up).
	ToleranceUnits   int     `json:"toleranceUnits" db:"tolerance_units"`
	TolerancePercent float64 `json:"tolerancePercent" db:"tolerance_percent"`
	// ProposeCase adds a ready-made investigation request to the alert; nothing is opened.
	ProposeCase bool `json:"proposeCase" db:"propose_case"`
	// BalanceAuto lets the worker re-run the reconciliation for lots whose serials changed.
	BalanceAuto     bool       `json:"balanceAuto" db:"balance_auto"`
	CheckpointHours int        `json:"checkpointHours" db:"checkpoint_hours"`
	UpdatedBy       string     `json:"updatedBy" db:"updated_by"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty" db:"updated_at"`
	// IsDefault is true while the tenant has not saved settings.
	IsDefault bool `json:"isDefault" db:"-"`
}

const settingCols = `tolerance_units, tolerance_percent::float8 as tolerance_percent, propose_case, balance_auto, checkpoint_hours, updated_by, updated_at`

func loadSettings(ctx context.Context, tx pgx.Tx) (Settings, error) {
	s, err := db.One[Settings](ctx, tx, `select `+settingCols+` from trace_setting where tenant_id = app_tenant()`)
	if errors.Is(err, db.ErrNotFound) {
		return Settings{CheckpointHours: 24, IsDefault: true}, nil
	}
	return s, err
}

// Anomaly is one serial whose accounting does not add up.
type Anomaly struct {
	EPC   string `json:"epc"`
	In    int    `json:"in"`
	Out   int    `json:"out"`
	Issue string `json:"issue"`
	Units int    `json:"units"`
}

// LotBalance is the reconciliation of one lot.
type LotBalance struct {
	GTIN string `json:"gtin"`
	Lot  string `json:"lot"`
	// Data: distinct serials with at least one event of the kind (from stored EPCIS events).
	Serials        int `json:"serials"`
	Commissioned   int `json:"commissioned"`
	Shipped        int `json:"shipped"`
	Received       int `json:"received"`
	Decommissioned int `json:"decommissioned"`
	Destroyed      int `json:"destroyed"`
	// Data: event occurrences by kind, after removing repeated copies of the same event.
	Occurrences map[string]int `json:"occurrences"`
	// ViaAggregation counts events attributed to a serial through the pallet or case it was in.
	ViaAggregation int `json:"viaAggregation"`
	// Data: serial_state statuses of the lot.
	ByStatus map[string]int `json:"byStatus"`
	// Helper: derived from the data above.
	Inflow         int       `json:"inflow"`
	Outflow        int       `json:"outflow"`
	ExpectedOnHand int       `json:"expectedOnHand"`
	Discrepancy    int       `json:"discrepancy"`
	Allowed        int       `json:"allowed"`
	Closed         bool      `json:"closed"`
	Anomalies      []Anomaly `json:"anomalies"`
	AnomaliesMore  int       `json:"anomaliesNotListed,omitempty"`
}

const maxAnomalies = 50

// Reconcile computes the balance of one lot from the per-serial events.
//
//	inflow  = commissioning + receiving events        outflow = shipping + decommissioning + destroying events
//
// A serial that is in custody once has inflow minus outflow of 0 (gone) or 1 (present). Every
// unit outside that range is a discrepancy: outflow without a matching inflow (shipped or
// destroyed twice, shipped without being received) or inflow without a matching outflow
// (commissioned or received twice).
func Reconcile(gtin, lot string, scope []scopeSerial, events map[string][]evRow, s Settings) LotBalance {
	lb := LotBalance{GTIN: gtin, Lot: lot, Occurrences: map[string]int{}, ByStatus: map[string]int{}, Anomalies: []Anomaly{}}
	for _, sc := range scope {
		lb.Serials++
		lb.ByStatus[sc.Status]++
		var in, out int
		seenCat := map[string]bool{}
		for _, e := range events[sc.EPC] {
			cat := classify(e)
			if cat == "" {
				continue
			}
			if e.Via {
				lb.ViaAggregation++
			}
			lb.Occurrences[cat]++
			seenCat[cat] = true
			switch cat {
			case CatCommissioning, CatReceiving:
				in++
			default:
				out++
			}
		}
		for cat, p := range map[string]*int{CatCommissioning: &lb.Commissioned, CatShipping: &lb.Shipped, CatReceiving: &lb.Received, CatDecommissioning: &lb.Decommissioned, CatDestroying: &lb.Destroyed} {
			if seenCat[cat] {
				*p++
			}
		}
		lb.Inflow += in
		lb.Outflow += out
		net := in - out
		if net > 0 {
			lb.ExpectedOnHand += net
		}
		var units int
		var issue string
		switch {
		case net < 0:
			units, issue = -net, "outflow without a matching inflow (shipped, decommissioned or destroyed more often than commissioned or received)"
		case net > 1:
			units, issue = net-1, "inflow without a matching outflow (commissioned or received more than once)"
		}
		if units > 0 {
			lb.Discrepancy += units
			if len(lb.Anomalies) < maxAnomalies {
				lb.Anomalies = append(lb.Anomalies, Anomaly{EPC: sc.EPC, In: in, Out: out, Issue: issue, Units: units})
			} else {
				lb.AnomaliesMore++
			}
		}
	}
	entered := lb.Commissioned + lb.Received
	lb.Allowed = max(s.ToleranceUnits, int(math.Ceil(float64(entered)*s.TolerancePercent/100)))
	lb.Closed = lb.Discrepancy <= lb.Allowed
	return lb
}

// groupByLot splits scope serials by lot (sorted lot order; "" = serials without a lot).
func groupByLot(scope []scopeSerial) ([]string, map[string][]scopeSerial) {
	by := map[string][]scopeSerial{}
	for _, s := range scope {
		by[s.Lot] = append(by[s.Lot], s)
	}
	lots := make([]string, 0, len(by))
	for l := range by {
		lots = append(lots, l)
	}
	sort.Strings(lots)
	return lots, by
}

// Gap is a serial seen at a location without a recorded movement into it.
type Gap struct {
	EPC      string    `json:"epc"`
	Lot      string    `json:"lot"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"` // low | medium | high
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	At       time.Time `json:"at"`
	EventID  string    `json:"eventId"`
	Detail   string    `json:"detail"`
}

// Gap kinds.
const (
	GapNoLink            = "no_link"
	GapNotReceived       = "shipped_not_received"
	GapNoShipment        = "received_without_shipment"
	GapDestinationMiss   = "destination_mismatch"
	GapAfterEndOfLife    = "seen_after_end_of_life"
	severityLow, sevMed  = "low", "medium"
	severityHigh         = "high"
	maxGapsListed        = 500
	departing, arriving  = "departing", "arriving"
	reasonNoLinkTemplate = "seen at %s with no shipping or receiving event after it was last seen at %s"
)

func sevRank(s string) int {
	switch s {
	case severityHigh:
		return 3
	case sevMed:
		return 2
	case severityLow:
		return 1
	}
	return 0
}

// DetectGaps walks one serial's events in time order and reports every move between two
// locations that no shipping or receiving event explains, and events after the serial left the
// supply chain (decommissioned or destroyed).
//
// Severity: high = seen at B with neither a shipping event out of A nor a receiving event at B;
// medium = shipped from A but seen at B by a non-receiving event, or received at a location other
// than the shipment's destination, or seen after decommissioning/destruction; low = received at B with
// no shipping event from A on record (normal when A belongs to a partner that shares no events).
func DetectGaps(sc scopeSerial, evs []evRow) []Gap {
	var gaps []Gap
	loc, shipped, ended := "", false, ""
	var shipDest []string
	endedReported := false
	for _, e := range evs {
		cat := classify(e)
		if ended != "" && !endedReported && cat != CatDecommissioning && cat != CatDestroying {
			gaps = append(gaps, Gap{EPC: sc.EPC, Lot: sc.Lot, Kind: GapAfterEndOfLife, Severity: sevMed, At: e.Time, EventID: e.ID,
				Detail: fmt.Sprintf("event %q after the serial was %s", e.BizStep, ended)})
			endedReported = true
		}
		if cat == CatDecommissioning || cat == CatDestroying {
			ended = cat
		}
		if e.Location == "" {
			if cat == CatShipping || e.BizStep == departing {
				shipped, shipDest = true, e.Dest
			}
			continue
		}
		if loc != "" && e.Location != loc {
			receiving := cat == CatReceiving || e.BizStep == arriving
			g := Gap{EPC: sc.EPC, Lot: sc.Lot, From: loc, To: e.Location, At: e.Time, EventID: e.ID}
			switch {
			case shipped && receiving:
				if want := destGLNs(shipDest); len(want) > 0 {
					if got := glnOf(e.Location); got != "" && !want[got] {
						g.Kind, g.Severity = GapDestinationMiss, sevMed
						g.Detail = fmt.Sprintf("shipped to a different destination than the location %s where it was received", e.Location)
					}
				}
			case shipped:
				g.Kind, g.Severity = GapNotReceived, sevMed
				g.Detail = fmt.Sprintf("shipped from %s, then seen at %s without a receiving event", loc, e.Location)
			case receiving:
				g.Kind, g.Severity = GapNoShipment, severityLow
				g.Detail = fmt.Sprintf("received at %s; no shipping event from %s is on record", e.Location, loc)
			default:
				g.Kind, g.Severity = GapNoLink, severityHigh
				g.Detail = fmt.Sprintf(reasonNoLinkTemplate, e.Location, loc)
			}
			if g.Kind != "" {
				gaps = append(gaps, g)
			}
			shipped, shipDest = false, nil
		}
		loc = e.Location
		if cat == CatShipping || e.BizStep == departing {
			shipped, shipDest = true, e.Dest
		}
	}
	return gaps
}

func destGLNs(dest []string) map[string]bool {
	m := map[string]bool{}
	for _, d := range dest {
		if g := glnOf(d); g != "" {
			m[g] = true
		}
	}
	return m
}

// GapSummary counts gaps by severity.
type GapSummary struct {
	High   int `json:"high"`
	Medium int `json:"medium"`
	Low    int `json:"low"`
}

func summarize(gaps []Gap) (GapSummary, string) {
	var s GapSummary
	top := ""
	for _, g := range gaps {
		switch g.Severity {
		case severityHigh:
			s.High++
		case sevMed:
			s.Medium++
		default:
			s.Low++
		}
		if sevRank(g.Severity) > sevRank(top) {
			top = g.Severity
		}
	}
	return s, top
}

// LotGaps is the custody gap result for one lot.
type LotGaps struct {
	GTIN      string     `json:"gtin"`
	Lot       string     `json:"lot"`
	Serials   int        `json:"serials"`
	Summary   GapSummary `json:"summary"`
	Gaps      []Gap      `json:"gaps"`
	NotListed int        `json:"notListed,omitempty"`
}

func lotGaps(gtin, lot string, serials []scopeSerial, events map[string][]evRow, minSev string) LotGaps {
	lg := LotGaps{GTIN: gtin, Lot: lot, Serials: len(serials), Gaps: []Gap{}}
	var all []Gap
	for _, sc := range serials {
		for _, g := range DetectGaps(sc, events[sc.EPC]) {
			if sevRank(g.Severity) >= sevRank(minSev) {
				all = append(all, g)
			}
		}
	}
	lg.Summary, _ = summarize(all)
	sort.SliceStable(all, func(i, j int) bool {
		if r := sevRank(all[i].Severity) - sevRank(all[j].Severity); r != 0 {
			return r > 0
		}
		return all[i].At.Before(all[j].At)
	})
	if len(all) > maxGapsListed {
		lg.NotListed = len(all) - maxGapsListed
		all = all[:maxGapsListed]
	}
	lg.Gaps = append(lg.Gaps, all...)
	return lg
}

// Analysis is the combined result of a reconciliation run.
type Analysis struct {
	GTIN      string       `json:"gtin"`
	Settings  Settings     `json:"settings"`
	Lots      []LotBalance `json:"lots"`
	Gaps      []LotGaps    `json:"gaps,omitempty"`
	Truncated bool         `json:"truncated"`
	Notes     []string     `json:"notes"`
}

// analysisNotes says what is data and what is helper in a reconciliation.
var analysisNotes = []string{
	"Data: counts of serials and events come from stored EPCIS events and the serial projection of this tenant only. Events held by partners that were not captured here are not visible.",
	"Helper: the balance (inflow minus outflow per serial), the tolerance and the closed / not closed judgement, and the gap severities are computed here; none of it is a regulatory test.",
	"Helper: events on a pallet or case are attributed to the serials that were aggregated into it at the time of the event; wrong or missing aggregation events distort the counts.",
	"TODO(spec): no regulation is applied here for an acceptable loss or a reporting deadline; the tolerance is the tenant's own setting.",
}

func analyze(ctx context.Context, tx pgx.Tx, gtin, lot, minSev string) (Analysis, error) {
	s, err := loadSettings(ctx, tx)
	if err != nil {
		return Analysis{}, err
	}
	scope, events, truncated, err := scopeEvents(ctx, tx, gtin, lot)
	if err != nil {
		return Analysis{}, err
	}
	a := Analysis{GTIN: gtin, Settings: s, Truncated: truncated, Lots: []LotBalance{}, Gaps: []LotGaps{}, Notes: analysisNotes}
	lots, by := groupByLot(scope)
	for _, l := range lots {
		a.Lots = append(a.Lots, Reconcile(gtin, l, by[l], events, s))
		a.Gaps = append(a.Gaps, lotGaps(gtin, l, by[l], events, minSev))
	}
	return a, nil
}

func normalizeGTIN(g string) (string, error) {
	g = gs1.NormalizeGTIN(strings.TrimSpace(g))
	if !gs1.ValidGTIN(g) {
		return "", httpx.BadRequest("gtin is not a valid GTIN")
	}
	return g, nil
}

func validSeverity(s string) (string, error) {
	switch s {
	case "":
		return "", nil
	case severityLow, sevMed, severityHigh:
		return s, nil
	}
	return "", httpx.BadRequest("minSeverity must be low, medium or high")
}

// --- alerts and case proposals ---

type alertOut struct {
	ID       uuid.UUID `json:"id"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Created  bool      `json:"created"`
}

func raiseAlert(ctx context.Context, tx pgx.Tx, kind, severity, title string, detail map[string]any, entityID, dedupe string) (alertOut, error) {
	id := ids.New()
	ct, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
		values ($1, app_tenant(), $2, $3, $4, $5, 'lot', $6, $7) on conflict do nothing`, id, kind, severity, title, detail, entityID, dedupe)
	if err != nil {
		return alertOut{}, err
	}
	if ct.RowsAffected() == 0 {
		err = tx.QueryRow(ctx, `select id from alert where dedupe_key=$1 and status='open' and tenant_id = app_tenant()`, dedupe).Scan(&id)
		return alertOut{ID: id, Kind: kind, Severity: severity}, err
	}
	return alertOut{ID: id, Kind: kind, Severity: severity, Created: true}, nil
}

func resolveAlerts(ctx context.Context, tx pgx.Tx, dedupe string) (int64, error) {
	ct, err := tx.Exec(ctx, `update alert set status='resolved', resolved_at=now() where dedupe_key=$1 and status='open' and tenant_id = app_tenant()`, dedupe)
	return ct.RowsAffected(), err
}

// caseProposal is a ready-made body for POST /api/v1/investigations. It is a proposal:
// opening the case stays a person's decision.
func caseProposal(gtin, lot, why string) map[string]any {
	body := map[string]any{"kind": "suspect", "title": "Mass balance does not close: " + gtin + " lot " + lot, "gtin": gtin, "note": why}
	if lot != "" {
		body["lots"] = []string{lot}
	}
	return map[string]any{"endpoint": "POST /api/v1/investigations", "body": body,
		"note": "Proposal only: nothing was opened. A person with the Compliance role decides whether to open the case."}
}

// RunResult is what POST /trace/balance/run returns.
type RunResult struct {
	Reports   []RunReport `json:"reports"`
	Alerts    []alertOut  `json:"alerts"`
	Notes     []string    `json:"notes"`
	Truncated bool        `json:"truncated"`
}

// RunReport is a stored reconciliation of one lot.
type RunReport struct {
	ID      uuid.UUID  `json:"id"`
	Status  string     `json:"status"`
	Balance LotBalance `json:"balance"`
	Gaps    LotGaps    `json:"custodyGaps"`
}

// runBalance reconciles, stores the reports, raises or resolves alerts and writes the audit
// trail. actor and tenant come from the caller (a request or the worker).
func runBalance(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, actor, gtin, lot string, maxLots int) (RunResult, error) {
	a, err := analyze(ctx, tx, gtin, lot, "")
	if err != nil {
		return RunResult{}, err
	}
	res := RunResult{Reports: []RunReport{}, Alerts: []alertOut{}, Notes: a.Notes, Truncated: a.Truncated}
	for i, lb := range a.Lots {
		if i >= maxLots {
			res.Notes = append(res.Notes, fmt.Sprintf("Only the first %d lots were run; pass lot to run the others.", maxLots))
			break
		}
		gp := a.Gaps[i]
		status := "balanced"
		if !lb.Closed {
			status = "discrepancy"
		}
		rep := RunReport{ID: ids.New(), Status: status, Balance: lb, Gaps: gp}
		var alertID *uuid.UUID
		bkey := "balance:" + gtin + "/" + lb.Lot
		if !lb.Closed {
			why := fmt.Sprintf("Discrepancy %d units exceeds the allowed %d (inflow %d, outflow %d).", lb.Discrepancy, lb.Allowed, lb.Inflow, lb.Outflow)
			detail := map[string]any{"gtin": gtin, "lot": lb.Lot, "discrepancy": lb.Discrepancy, "allowed": lb.Allowed, "inflow": lb.Inflow, "outflow": lb.Outflow,
				"reportId": rep.ID, "anomalies": lb.Anomalies}
			if a.Settings.ProposeCase {
				detail["caseProposal"] = caseProposal(gtin, lb.Lot, why)
			}
			al, err := raiseAlert(ctx, tx, "trace.balance_discrepancy", "error", "Mass balance does not close: "+gtin+" lot "+lb.Lot, detail, gtin+"/"+lb.Lot, bkey)
			if err != nil {
				return res, err
			}
			alertID = &al.ID
			res.Alerts = append(res.Alerts, al)
		} else if n, err := resolveAlerts(ctx, tx, bkey); err != nil {
			return res, err
		} else if n > 0 {
			if _, err := audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "alert.resolve", Entity: "alert", EntityID: bkey, After: map[string]any{"status": "resolved"}, Reason: "balance closes again"}); err != nil {
				return res, err
			}
		}
		gkey := "custody:" + gtin + "/" + lb.Lot
		if sum, top := summarize(gp.Gaps); sevRank(top) >= sevRank(sevMed) {
			sev := "warning"
			if top == severityHigh {
				sev = "error"
			}
			detail := map[string]any{"gtin": gtin, "lot": lb.Lot, "summary": sum, "reportId": rep.ID, "examples": firstGaps(gp.Gaps, 5)}
			if a.Settings.ProposeCase && top == severityHigh {
				detail["caseProposal"] = caseProposal(gtin, lb.Lot, fmt.Sprintf("%d serials were seen at a location without a shipping or receiving link.", sum.High))
			}
			al, err := raiseAlert(ctx, tx, "trace.custody_gap", sev, "Chain-of-custody gap: "+gtin+" lot "+lb.Lot, detail, gtin+"/"+lb.Lot, gkey)
			if err != nil {
				return res, err
			}
			res.Alerts = append(res.Alerts, al)
		} else if _, err := resolveAlerts(ctx, tx, gkey); err != nil {
			return res, err
		}
		blob, _ := json.Marshal(map[string]any{"balance": lb, "custodyGaps": gp})
		if _, err := tx.Exec(ctx, `insert into balance_report (id, tenant_id, gtin, lot, status, discrepancy, allowed, result, alert_id, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, rep.ID, tenant, gtin, lb.Lot, status, lb.Discrepancy, lb.Allowed, blob, alertID, actor); err != nil {
			return res, err
		}
		if _, err := audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "trace.balance.run", Entity: "balance_report", EntityID: rep.ID.String(),
			After: map[string]any{"gtin": gtin, "lot": lb.Lot, "status": status, "discrepancy": lb.Discrepancy, "allowed": lb.Allowed, "gaps": gp.Summary}}); err != nil {
			return res, err
		}
		res.Reports = append(res.Reports, rep)
	}
	return res, nil
}

func firstGaps(g []Gap, n int) []Gap {
	if len(g) > n {
		return g[:n]
	}
	return g
}

// --- HTTP ---

func (m *Module) balanceRoutes(r chi.Router) {
	r.Get("/balance", httpx.Handler(m.getBalance))
	r.Get("/balance/gaps", httpx.Handler(m.getGaps))
	r.Get("/balance/reports", httpx.Handler(m.listReports))
	r.Get("/balance/settings", httpx.Handler(m.getSettings))
	w := r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance))
	w.Put("/balance/settings", httpx.Handler(m.putSettings))
	w.Post("/balance/run", httpx.Handler(m.runBalanceHandler))
}

func (m *Module) getBalance(w http.ResponseWriter, r *http.Request) error {
	gtin, err := normalizeGTIN(r.URL.Query().Get("gtin"))
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		a, err := analyze(r.Context(), tx, gtin, r.URL.Query().Get("lot"), "")
		if err != nil {
			return err
		}
		a.Gaps = nil // the gap list has its own endpoint
		httpx.JSON(w, http.StatusOK, a)
		return nil
	})
}

func (m *Module) getGaps(w http.ResponseWriter, r *http.Request) error {
	gtin, err := normalizeGTIN(r.URL.Query().Get("gtin"))
	if err != nil {
		return err
	}
	sev, err := validSeverity(r.URL.Query().Get("minSeverity"))
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		a, err := analyze(r.Context(), tx, gtin, r.URL.Query().Get("lot"), sev)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"gtin": gtin, "lots": a.Gaps, "truncated": a.Truncated, "notes": a.Notes})
		return nil
	})
}

func (m *Module) runBalanceHandler(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		GTIN string `json:"gtin"`
		Lot  string `json:"lot"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	gtin, err := normalizeGTIN(in.GTIN)
	if err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	var res RunResult
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		res, err = runBalance(r.Context(), tx, p.TenantID, p.AuditActor(), gtin, in.Lot, 100)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// ReportRow is a stored run, without the full result.
type ReportRow struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	GTIN        string     `json:"gtin" db:"gtin"`
	Lot         string     `json:"lot" db:"lot"`
	Status      string     `json:"status" db:"status"`
	Discrepancy int        `json:"discrepancy" db:"discrepancy"`
	Allowed     int        `json:"allowed" db:"allowed"`
	AlertID     *uuid.UUID `json:"alertId,omitempty" db:"alert_id"`
	CreatedBy   string     `json:"createdBy" db:"created_by"`
	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
}

func (m *Module) listReports(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := httpx.IntQuery(r, "limit", 50, 1, 500)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[ReportRow](r.Context(), tx, `select id, gtin, lot, status, discrepancy, allowed, alert_id, created_by, created_at from balance_report
			where tenant_id = app_tenant() and ($1='' or gtin=$1) and ($2='' or lot=$2) and ($3='' or status=$3) order by created_at desc limit $4`,
			q.Get("gtin"), q.Get("lot"), q.Get("status"), limit)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[ReportRow]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getSettings(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := loadSettings(r.Context(), tx)
		if err == nil {
			httpx.JSON(w, http.StatusOK, s)
		}
		return err
	})
}

func (m *Module) putSettings(w http.ResponseWriter, r *http.Request) error {
	var in Settings
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.ToleranceUnits < 0 || in.ToleranceUnits > 1000000 || in.TolerancePercent < 0 || in.TolerancePercent > 100 || in.CheckpointHours < 0 || in.CheckpointHours > 8760 {
		return httpx.BadRequest("toleranceUnits must be 0..1000000, tolerancePercent 0..100 and checkpointHours 0..8760")
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := loadSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		s, err := db.One[Settings](r.Context(), tx, `insert into trace_setting (tenant_id, tolerance_units, tolerance_percent, propose_case, balance_auto, checkpoint_hours, updated_by, updated_at)
			values (app_tenant(), $1, $2, $3, $4, $5, $6, now())
			on conflict (tenant_id) do update set tolerance_units=$1, tolerance_percent=$2, propose_case=$3, balance_auto=$4, checkpoint_hours=$5, updated_by=$6, updated_at=now()
			returning `+settingCols, in.ToleranceUnits, in.TolerancePercent, in.ProposeCase, in.BalanceAuto, in.CheckpointHours, p.AuditActor())
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "trace.settings.update", Entity: "trace_setting", EntityID: p.TenantID.String(), Before: before, After: s}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

// autoBalance re-runs the reconciliation, for tenants that turned it on, for lots whose serials
// changed since the lot's last stored run (at most 50 lots per tenant and pass).
func (m *Module) autoBalance(ctx context.Context) error {
	var tenants []uuid.UUID
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select tenant_id from trace_setting where balance_auto`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t uuid.UUID
			if err := rows.Scan(&t); err != nil {
				return err
			}
			tenants = append(tenants, t)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if err := m.autoBalanceTenant(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) autoBalanceTenant(ctx context.Context, tenant uuid.UUID) error {
	type key struct{ gtin, lot string }
	var todo []key
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select g.gtin, g.lot from (
				select gtin, coalesce(lot,'') as lot, max(updated_at) as changed from serial_state
				where tenant_id = app_tenant() and gtin is not null and serial is not null group by 1, 2) g
			where g.changed > coalesce((select max(r.created_at) from balance_report r where r.tenant_id = app_tenant() and r.gtin = g.gtin and r.lot = g.lot), 'epoch'::timestamptz)
			order by 1, 2 limit 50`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.gtin, &k.lot); err != nil {
				return err
			}
			todo = append(todo, k)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, k := range todo {
		err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
			_, err := runBalance(ctx, tx, tenant, "system", k.gtin, k.lot, 1)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
