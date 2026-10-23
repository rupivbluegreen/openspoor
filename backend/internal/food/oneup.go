package food

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// --- EU General Food Law one-up/one-down report (Regulation (EC) No 178/2002, Art. 18) ---

// ReportNotes accompany every one-up/one-down export. The Regulation sets the obligation, not
// a layout, so the notes say which parts are this product's own choice.
var ReportNotes = []string{
	"Regulation (EC) No 178/2002, Article 18(2)-(3): food business operators must be able to identify any person from whom they have been supplied (one step back) " +
		"and the other businesses to which their products have been supplied (one step forward), and make this information available to the competent authorities on demand.",
	"The Regulation does not prescribe a file format or columns. The layout of this report (columns, CSV and JSON shapes) is Open Spoor's own.",
	"Rows marked transformation_input / transformation_output are internal: they show how a lot's suppliers and customers are reached through this operator's own processing.",
	"Certificates list certificates recorded for the exchanged lot (directly, via its product, via a site or held for the supplier); Article 18 does not require them.",
	"TODO(spec): the Commission's guidance on Articles 11, 12, 14, 17, 18, 19 and 20 of Regulation (EC) No 178/2002 recommends further information, such as supplier and customer addresses; " +
		"only fields captured on the tracking events are included. Confirm against the current guidance and, for food of animal origin, Implementing Regulation (EU) No 931/2011.",
}

// ReportRow is one supplier (one-up) or customer (one-down) link of a lot.
type ReportRow struct {
	Lot                string    `json:"lot"`
	Direction          string    `json:"direction"` // one_up | one_down
	Relation           string    `json:"relation"`  // received_from | transformation_input | shipped_to | transformation_output
	ViaLot             string    `json:"viaLot,omitempty"`
	CounterpartyID     string    `json:"counterpartyId"`
	CounterpartyName   string    `json:"counterpartyName"`
	EventID            uuid.UUID `json:"eventId"`
	EventType          string    `json:"eventType"`
	EventTime          time.Time `json:"eventTime"`
	LotCode            string    `json:"lotCode"`
	ProductDescription string    `json:"productDescription"`
	Quantity           float64   `json:"quantity"`
	UOM                string    `json:"uom"`
	RefDocType         string    `json:"refDocType"`
	RefDocNumber       string    `json:"refDocNumber"`
	Certificates       []string  `json:"certificates"`
}

// ReportLot is a lot the report is about.
type ReportLot struct {
	TLC          string     `json:"tlc"`
	Description  string     `json:"description"`
	GTIN         string     `json:"gtin,omitempty"`
	Status       string     `json:"status"`
	Certificates []Coverage `json:"certificates"`
}

// ReportScope is what the report was asked for.
type ReportScope struct {
	TLCs      []string `json:"tlcs,omitempty"`
	ProductID string   `json:"productId,omitempty"`
	GTIN      string   `json:"gtin,omitempty"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
}

// Report is the one-up/one-down report.
type Report struct {
	GeneratedAt time.Time   `json:"generatedAt"`
	Operator    string      `json:"operator"`
	Scope       ReportScope `json:"scope"`
	Notes       []string    `json:"notes"`
	Lots        []ReportLot `json:"lots"`
	Rows        []ReportRow `json:"rows"`
	Truncated   bool        `json:"truncated"`
}

const maxReportLots = 500

// reportColumns are the CSV columns (Open Spoor's own layout; see ReportNotes).
var reportColumns = []string{"Lot", "Direction", "Relation", "Via Lot", "Counterparty Identifier", "Counterparty Name", "Event Type", "Event Date", "Event Time (UTC)",
	"Lot Code", "Product Description", "Quantity", "Unit of Measure", "Reference Document Type", "Reference Document Number", "Certificates"}

type window struct{ from, to time.Time }

func parseWindow(from, to string) (window, error) {
	w := window{from: time.Unix(0, 0).UTC(), to: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)}
	if from != "" {
		t, err := time.Parse("2006-01-02", from)
		if err != nil {
			return w, httpx.BadRequest("from must be YYYY-MM-DD")
		}
		w.from = t
	}
	if to != "" {
		t, err := time.Parse("2006-01-02", to)
		if err != nil {
			return w, httpx.BadRequest("to must be YYYY-MM-DD")
		}
		w.to = t.AddDate(0, 0, 1)
	}
	if !w.to.After(w.from) {
		return w, httpx.BadRequest("to must not be before from")
	}
	return w, nil
}

// BuildReport assembles the one-up/one-down report inside tx. Explicit lots must exist; without
// lots the report covers the product's lots, or every lot with a receipt, shipment or
// transformation in the period (at most 500 lots).
func (m *Module) BuildReport(ctx context.Context, tx pgx.Tx, scope ReportScope) (Report, error) {
	win, err := parseWindow(scope.From, scope.To)
	if err != nil {
		return Report{}, err
	}
	rep := Report{GeneratedAt: time.Now().UTC().Truncate(time.Second), Scope: scope, Notes: ReportNotes, Lots: []ReportLot{}, Rows: []ReportRow{}}
	_ = tx.QueryRow(ctx, `select name from tenant where id=app_tenant()`).Scan(&rep.Operator)
	if scope.GTIN != "" {
		scope.GTIN = gs1.NormalizeGTIN(scope.GTIN)
		rep.Scope.GTIN = scope.GTIN
	}
	var tlcs []string
	switch {
	case len(scope.TLCs) > 0:
		tlcs = dedupe(scope.TLCs)
		known, err := listStrings(ctx, tx, `select tlc from food_lot where tlc = any($1)`, tlcs)
		if err != nil {
			return rep, err
		}
		for _, t := range tlcs {
			if !contains(known, t) {
				return rep, httpx.BadRequest("unknown traceability lot code " + t)
			}
		}
	case scope.ProductID != "" || scope.GTIN != "":
		tlcs, err = listStrings(ctx, tx, `select tlc from food_lot where ($1 <> '' and product_id::text = $1) or ($2 <> '' and gtin = $2) order by tlc limit $3`,
			scope.ProductID, scope.GTIN, maxReportLots+1)
	default:
		tlcs, err = listStrings(ctx, tx, `select tlc from (
				select c.tlc from food_cte c where c.type in ('receiving','first_land_based_receiving','shipping','transformation') and c.event_time >= $1 and c.event_time < $2
				union select f.input_tlc from food_lineage f join food_cte c on c.id = f.cte_id where c.event_time >= $1 and c.event_time < $2) t
			order by tlc limit $3`, win.from, win.to, maxReportLots+1)
	}
	if err != nil {
		return rep, err
	}
	if len(tlcs) > maxReportLots {
		tlcs, rep.Truncated = tlcs[:maxReportLots], true
	}
	if len(tlcs) == 0 {
		return rep, nil
	}
	lots, err := db.List[struct {
		TLC         string  `db:"tlc"`
		Description string  `db:"description"`
		GTIN        *string `db:"gtin"`
		Status      string  `db:"status"`
	}](ctx, tx, `select tlc, description, gtin, status from food_lot where tlc = any($1) order by tlc`, tlcs)
	if err != nil {
		return rep, err
	}
	for _, l := range lots {
		rows, err := lotLinks(ctx, tx, l.TLC, win)
		if err != nil {
			return rep, err
		}
		rep.Lots = append(rep.Lots, ReportLot{TLC: l.TLC, Description: l.Description, GTIN: deref(l.GTIN), Status: l.Status})
		rep.Rows = append(rep.Rows, rows...)
	}
	// Certificates of the subject lots and of every exchanged lot.
	all := append([]string{}, tlcs...)
	for _, r := range rep.Rows {
		all = append(all, r.LotCode)
	}
	cov, err := m.coverage(ctx, tx, dedupe(all))
	if err != nil {
		return rep, err
	}
	for i := range rep.Lots {
		rep.Lots[i].Certificates = cov[rep.Lots[i].TLC]
	}
	for i := range rep.Rows {
		refs := []string{}
		for _, c := range cov[rep.Rows[i].LotCode] {
			refs = append(refs, c.Ref())
		}
		rep.Rows[i].Certificates = dedupe(refs)
	}
	return rep, nil
}

// lotLinks returns the one-up and one-down rows of one lot in the window. Transformations are
// followed inside this operator: receipts of upstream lots are the lot's suppliers, shipments of
// downstream lots its customers.
func lotLinks(ctx context.Context, tx pgx.Tx, tlc string, win window) ([]ReportRow, error) {
	back, err := lineage(ctx, tx, []string{tlc}, false)
	if err != nil {
		return nil, err
	}
	fwd, err := lineage(ctx, tx, []string{tlc}, true)
	if err != nil {
		return nil, err
	}
	via := func(lot string) string {
		if lot == tlc {
			return ""
		}
		return lot
	}
	var out []ReportRow
	// Transformation edges: inputs of the lot and of its upstream lots (one-up); outputs made
	// from the lot and from its downstream lots (one-down).
	for _, dir := range []struct {
		direction, relation, match string
		set                        []string
	}{{"one_up", "transformation_input", "f.output_tlc", back}, {"one_down", "transformation_output", "f.input_tlc", fwd}} {
		rows, err := tx.Query(ctx, `select f.input_tlc, f.output_tlc, f.quantity, c.quantity, c.id, c.event_time, c.location_gln, c.location_name, c.uom,
				coalesce(il.description,''), coalesce(il.uom,''), coalesce(ol.description,'')
			from food_lineage f join food_cte c on c.id = f.cte_id
			left join food_lot il on il.tlc = f.input_tlc left join food_lot ol on ol.tlc = f.output_tlc
			where `+dir.match+` = any($1) and c.event_time >= $2 and c.event_time < $3`, dir.set, win.from, win.to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var in, outTLC, gln, lname, outUOM, inDesc, inUOM, outDesc string
			var q, outQ float64
			var id uuid.UUID
			var at time.Time
			if err := rows.Scan(&in, &outTLC, &q, &outQ, &id, &at, &gln, &lname, &outUOM, &inDesc, &inUOM, &outDesc); err != nil {
				rows.Close()
				return nil, err
			}
			r := ReportRow{Lot: tlc, Direction: dir.direction, Relation: dir.relation, CounterpartyID: gln, CounterpartyName: lname, EventID: id, EventType: "transformation", EventTime: at}
			if dir.direction == "one_up" {
				r.ViaLot, r.LotCode, r.ProductDescription, r.Quantity, r.UOM = via(outTLC), in, inDesc, q, inUOM
			} else {
				// The output quantity is on the transformation event, not on the lineage edge.
				r.ViaLot, r.LotCode, r.ProductDescription, r.Quantity, r.UOM = via(in), outTLC, outDesc, outQ, outUOM
			}
			out = append(out, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	// Receipts (one-up) and shipments (one-down) of the lot and the lots linked by transformations.
	for _, dir := range []struct {
		direction, relation, types, party, name string
		set                                     []string
	}{
		{"one_up", "received_from", "'receiving','first_land_based_receiving'", "c.ship_from", "''", back},
		{"one_down", "shipped_to", "'shipping'", "c.ship_to", "c.ship_to_name", fwd},
	} {
		rows, err := tx.Query(ctx, `select c.id, c.tlc, c.type, c.event_time, c.quantity, c.uom, `+dir.party+`,
				coalesce(nullif(`+dir.name+`, ''), (select p.name from partner p where p.gln = `+dir.party+` and `+dir.party+` <> '' limit 1), ''),
				c.ref_doc_type, c.ref_doc_number, coalesce(l.description,'')
			from food_cte c left join food_lot l on l.tlc = c.tlc
			where c.tlc = any($1) and c.type in (`+dir.types+`) and c.event_time >= $2 and c.event_time < $3`, dir.set, win.from, win.to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r := ReportRow{Lot: tlc, Direction: dir.direction, Relation: dir.relation}
			if err := rows.Scan(&r.EventID, &r.LotCode, &r.EventType, &r.EventTime, &r.Quantity, &r.UOM, &r.CounterpartyID, &r.CounterpartyName,
				&r.RefDocType, &r.RefDocNumber, &r.ProductDescription); err != nil {
				rows.Close()
				return nil, err
			}
			r.ViaLot = via(r.LotCode)
			out = append(out, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	order := map[string]int{"one_up": 0, "one_down": 1}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Direction != b.Direction {
			return order[a.Direction] < order[b.Direction]
		}
		if !a.EventTime.Equal(b.EventTime) {
			return a.EventTime.Before(b.EventTime)
		}
		return a.LotCode < b.LotCode
	})
	return out, nil
}

func listStrings(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// csvText neutralises spreadsheet formulas in free-text cells.
func csvText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// WriteReportCSV writes the report as CSV: "#" comment lines with the notes and scope, then a
// header row and one row per link.
func WriteReportCSV(out io.Writer, rep Report) error {
	w := csv.NewWriter(out)
	comment := func(s string) {
		_, _ = io.WriteString(out, "# "+strings.NewReplacer("\r", " ", "\n", " ").Replace(s)+"\n")
	}
	comment("One-up/one-down traceability report")
	comment(fmt.Sprintf("Operator: %s; generated %s; lots: %s; period: %s to %s", rep.Operator, rep.GeneratedAt.Format(time.RFC3339),
		strings.Join(rep.Scope.TLCs, " "), orDash(rep.Scope.From), orDash(rep.Scope.To)))
	for _, n := range rep.Notes {
		comment(n)
	}
	if rep.Truncated {
		comment(fmt.Sprintf("Truncated to the first %d lots.", maxReportLots))
	}
	_ = w.Write(reportColumns)
	for _, r := range rep.Rows {
		_ = w.Write([]string{csvText(r.Lot), r.Direction, r.Relation, csvText(r.ViaLot), csvText(r.CounterpartyID), csvText(r.CounterpartyName), r.EventType,
			r.EventTime.UTC().Format("2006-01-02"), r.EventTime.UTC().Format("15:04:05"), csvText(r.LotCode), csvText(r.ProductDescription), trimFloat(r.Quantity),
			csvText(r.UOM), csvText(r.RefDocType), csvText(r.RefDocNumber), csvText(strings.Join(r.Certificates, "; "))})
	}
	w.Flush()
	return w.Error()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func reportScope(r *http.Request) ReportScope {
	q := r.URL.Query()
	var tlcs []string
	for _, v := range q["tlc"] {
		tlcs = append(tlcs, strings.Split(v, ",")...)
	}
	return ReportScope{TLCs: dedupe(tlcs), ProductID: q.Get("productId"), GTIN: q.Get("gtin"), From: q.Get("from"), To: q.Get("to")}
}

func attachment(w http.ResponseWriter, name, format string) {
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+"."+format+`"`)
}

func writeReport(w http.ResponseWriter, format string, v any, rep Report) error {
	if format == "csv" {
		return WriteReportCSV(w, rep)
	}
	return json.NewEncoder(w).Encode(v)
}

// oneUpOneDown serves the report. Without format it is a JSON view; format=csv or json returns
// a download and is audited.
func (m *Module) oneUpOneDown(w http.ResponseWriter, r *http.Request) error {
	format := r.URL.Query().Get("format")
	if format != "" && format != "csv" && format != "json" {
		return httpx.BadRequest("format must be csv or json")
	}
	ctx := r.Context()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		rep, err := m.BuildReport(ctx, tx, reportScope(r))
		if err != nil {
			return err
		}
		if format == "" {
			httpx.JSON(w, http.StatusOK, rep)
			return nil
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "food.oneupdown.export", Entity: "food_lot", EntityID: strings.Join(rep.Scope.TLCs, ","),
			After: map[string]any{"format": format, "scope": rep.Scope, "lots": len(rep.Lots), "rows": len(rep.Rows)}}); err != nil {
			return err
		}
		attachment(w, "one-up-one-down-"+time.Now().UTC().Format("20060102-1504"), format)
		return writeReport(w, format, rep, rep)
	})
}

// --- authority requests ---

// AuthorityRequest is a report assembled for a competent authority, kept as generated.
type AuthorityRequest struct {
	ID         uuid.UUID        `json:"id" db:"id"`
	Authority  string           `json:"authority" db:"authority"`
	Reference  string           `json:"reference" db:"reference"`
	Reason     string           `json:"reason" db:"reason"`
	TLCs       []string         `json:"tlcs" db:"tlcs"`
	PeriodFrom *time.Time       `json:"periodFrom,omitempty" db:"period_from"`
	PeriodTo   *time.Time       `json:"periodTo,omitempty" db:"period_to"`
	Rows       int              `json:"rows" db:"rows"`
	SHA256     string           `json:"sha256" db:"sha256"`
	CreatedBy  string           `json:"createdBy" db:"created_by"`
	Exports    []map[string]any `json:"exports" db:"exports"`
	CreatedAt  time.Time        `json:"createdAt" db:"created_at"`
	Report     *Report          `json:"report,omitempty" db:"-"`
}

const authCols = `id, authority, reference, reason, tlcs, period_from, period_to, rows, sha256, created_by, exports, created_at`

func (m *Module) createAuthorityRequest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Authority string   `json:"authority"`
		Reference string   `json:"reference"`
		Reason    string   `json:"reason"`
		TLCs      []string `json:"tlcs"`
		From      string   `json:"from"`
		To        string   `json:"to"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Authority = strings.TrimSpace(in.Authority)
	in.TLCs = dedupe(in.TLCs)
	if in.Authority == "" {
		return httpx.BadRequest("authority (the requesting competent authority) is required")
	}
	if len(in.TLCs) == 0 || len(in.TLCs) > maxReportLots {
		return httpx.BadRequest(fmt.Sprintf("between 1 and %d lots (tlcs) are required", maxReportLots))
	}
	ctx := r.Context()
	actor := auth.MustFrom(ctx).Actor()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		rep, err := m.BuildReport(ctx, tx, ReportScope{TLCs: in.TLCs, From: in.From, To: in.To})
		if err != nil {
			return err
		}
		b, err := json.Marshal(rep)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		pf, _ := parseDate(in.From)
		pt, _ := parseDate(in.To)
		ar, err := db.One[AuthorityRequest](ctx, tx, `insert into food_authority_request (id, tenant_id, authority, reference, reason, tlcs, period_from, period_to, rows, sha256, report, created_by)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11) returning `+authCols,
			ids.New(), in.Authority, strings.TrimSpace(in.Reference), in.Reason, rep.Scope.TLCs, pf, pt, len(rep.Rows), hex.EncodeToString(sum[:]), b, actor)
		if err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "food.oneupdown.authority_request.create", Entity: "food_authority_request", EntityID: ar.ID.String(),
			After: ar, Reason: in.Reason}); err != nil {
			return err
		}
		ar.Report = &rep
		httpx.JSON(w, http.StatusCreated, ar)
		return nil
	})
}

func (m *Module) listAuthorityRequests(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[AuthorityRequest](r.Context(), tx, `select `+authCols+` from food_authority_request order by created_at desc limit 500`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[AuthorityRequest]{Items: items, Total: len(items)})
		}
		return err
	})
}

func loadAuthorityRequest(ctx context.Context, tx pgx.Tx, id uuid.UUID) (AuthorityRequest, error) {
	ar, err := db.One[AuthorityRequest](ctx, tx, `select `+authCols+` from food_authority_request where id=$1`, id)
	if err != nil {
		return ar, err
	}
	var rep Report
	if err := tx.QueryRow(ctx, `select report from food_authority_request where id=$1`, id).Scan(&rep); err != nil {
		return ar, err
	}
	ar.Report = &rep
	return ar, nil
}

func (m *Module) getAuthorityRequest(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ar, err := loadAuthorityRequest(r.Context(), tx, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, ar)
		}
		return err
	})
}

// exportAuthorityRequest downloads the report as generated and records who exported it.
func (m *Module) exportAuthorityRequest(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "json" {
		return httpx.BadRequest("format must be csv or json")
	}
	ctx := r.Context()
	actor := auth.MustFrom(ctx).Actor()
	return m.Tx(ctx, func(tx pgx.Tx) error {
		ar, err := loadAuthorityRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		entry := map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "actor": actor, "format": format}
		if _, err := tx.Exec(ctx, `update food_authority_request set exports = exports || jsonb_build_array($2::jsonb) where id=$1`, id, entry); err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "food.oneupdown.authority_request.export", Entity: "food_authority_request", EntityID: id.String(),
			After: map[string]any{"format": format, "authority": ar.Authority, "reference": ar.Reference, "sha256": ar.SHA256}}); err != nil {
			return err
		}
		name := ar.Reference
		if name == "" {
			name = ar.ID.String()
		}
		name = strings.Map(func(c rune) rune {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
				return c
			}
			return '-'
		}, name)
		attachment(w, "one-up-one-down-"+name, format)
		rep := *ar.Report
		ar.Report = nil
		return writeReport(w, format, map[string]any{"request": ar, "report": rep}, rep)
	})
}
