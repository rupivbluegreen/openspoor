package food

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
)

// EUDR readiness: the tenant's role for due diligence statements, plot geolocation validation,
// the statement completeness checklist and the application-date reference data.

// Statement roles a tenant can be configured with.
//
// An operator is the party that first places goods on the EU market (or exports them) and
// creates the due diligence statement. A downstream trader holds a reference to the upstream
// statement instead of creating a new one.
//
// TODO(spec): this split follows the structure of Regulation (EU) 2023/1115 as secondary sources
// describe it, including the amended regime under which downstream traders are said to be
// exempt from making their own statements. None of that is asserted here as law; the role is a
// per-tenant setting so each organisation can follow the advice it gets, and the legal detail
// (who must keep what, for how long, what a reference must carry) must be confirmed against the
// Official Journal. See docs/OPEN_QUESTIONS.md.
const (
	RoleOperator   = "operator"
	RoleDownstream = "downstream_trader"
)

// Statement kinds stored in eudr_dds.kind.
const (
	KindStatement         = "statement"
	KindUpstreamReference = "upstream_reference"
)

// EUDRSettings is the tenant's EUDR configuration.
type EUDRSettings struct {
	DDSRole   string    `json:"ddsRole"`
	Roles     []string  `json:"roles"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
	Notes     []string  `json:"notes"`
}

var eudrSettingNotes = []string{
	"The role decides whether this tenant creates due diligence statements (operator) or records a reference to the upstream statement (downstream_trader).",
	"TODO(spec): the legal split of duties between operators and downstream traders is not asserted by this software; confirm it against the Official Journal.",
}

func eudrSettings(ctx context.Context, tx pgx.Tx) (EUDRSettings, error) {
	s := EUDRSettings{DDSRole: RoleOperator, Roles: []string{RoleOperator, RoleDownstream}, Notes: eudrSettingNotes}
	err := tx.QueryRow(ctx, `select dds_role, updated_at from food_eudr_setting`).Scan(&s.DDSRole, &s.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	return s, nil
}

func (m *Module) getEUDRSettings(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := eudrSettings(r.Context(), tx)
		if err == nil {
			httpx.JSON(w, http.StatusOK, s)
		}
		return err
	})
}

func (m *Module) putEUDRSettings(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		DDSRole string `json:"ddsRole"`
		Reason  string `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.DDSRole != RoleOperator && in.DDSRole != RoleDownstream {
		return httpx.BadRequest("ddsRole must be operator or downstream_trader")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := eudrSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into food_eudr_setting (tenant_id, dds_role) values (app_tenant(), $1)
			on conflict (tenant_id) do update set dds_role=excluded.dds_role, updated_at=now()`, in.DDSRole); err != nil {
			return err
		}
		after, err := eudrSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "food.eudr.settings", Entity: "food_eudr_setting", EntityID: auth.MustFrom(r.Context()).TenantID.String(),
			Before: map[string]string{"ddsRole": before.DDSRole}, After: map[string]string{"ddsRole": after.DDSRole}, Reason: in.Reason}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, after)
		return nil
	})
}

// --- application dates (reference data) ---

// UnverifiedLabel is shown next to every application date until a person confirms it.
const UnverifiedLabel = "unverified, check the Official Journal"

// RefDate is an application date of the EUDR, kept as editable reference data.
type RefDate struct {
	Key       string     `json:"key"`
	Label     string     `json:"label"`
	Date      string     `json:"date"`
	Source    string     `json:"source"`
	Verified  bool       `json:"verified"`
	Status    string     `json:"status"`
	Note      string     `json:"note"`
	IsDefault bool       `json:"isDefault"`
	DaysUntil int        `json:"daysUntil"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// defaultEUDRDates come from secondary sources, not from the legal text.
// TODO(spec): confirm both dates (and who falls in which group) against the Official Journal.
var defaultEUDRDates = []RefDate{
	{Key: "large_medium_operators", Label: "Large and medium operators and traders", Date: "2026-12-30", Source: "secondary sources"},
	{Key: "micro_small_operators", Label: "Micro and small operators", Date: "2027-06-30", Source: "secondary sources"},
}

func eudrDates(ctx context.Context, tx pgx.Tx, now time.Time) ([]RefDate, error) {
	out := make([]RefDate, len(defaultEUDRDates))
	copy(out, defaultEUDRDates)
	for i := range out {
		out[i].IsDefault = true
	}
	rows, err := tx.Query(ctx, `select key, to_char(date,'YYYY-MM-DD'), note, verified, updated_at from food_eudr_reference_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, d, note string
		var v bool
		var at time.Time
		if err := rows.Scan(&k, &d, &note, &v, &at); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].Key == k {
				out[i].Date, out[i].Note, out[i].Verified, out[i].IsDefault, out[i].UpdatedAt = d, note, v, false, &at
				out[i].Source = "set by this tenant"
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	today := now.UTC().Truncate(24 * time.Hour)
	for i := range out {
		out[i].Status = UnverifiedLabel
		if out[i].Verified {
			out[i].Status = "confirmed by this tenant against the Official Journal"
		}
		if t, err := time.Parse("2006-01-02", out[i].Date); err == nil {
			out[i].DaysUntil = int(t.Sub(today).Hours() / 24)
		}
	}
	return out, nil
}

func (m *Module) listEUDRDates(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := eudrDates(r.Context(), tx, time.Now())
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[RefDate]{Items: items, Total: len(items)})
		}
		return err
	})
}

func knownDateKey(k string) bool {
	for _, d := range defaultEUDRDates {
		if d.Key == k {
			return true
		}
	}
	return false
}

func (m *Module) putEUDRDate(w http.ResponseWriter, r *http.Request) error {
	key := chi.URLParam(r, "key")
	if !knownDateKey(key) {
		return httpx.NotFound("unknown reference date")
	}
	var in struct {
		Date     string `json:"date"`
		Note     string `json:"note"`
		Verified bool   `json:"verified"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if _, err := time.Parse("2006-01-02", in.Date); err != nil {
		return httpx.BadRequest("date must be YYYY-MM-DD")
	}
	if len(in.Note) > 500 {
		return httpx.BadRequest("note is limited to 500 characters")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `insert into food_eudr_reference_date (tenant_id, key, date, note, verified) values (app_tenant(), $1, $2::date, $3, $4)
			on conflict (tenant_id, key) do update set date=excluded.date, note=excluded.note, verified=excluded.verified, updated_at=now()`, key, in.Date, in.Note, in.Verified); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "food.eudr.date.set", Entity: "food_eudr_reference_date", EntityID: key, After: in}); err != nil {
			return err
		}
		items, err := eudrDates(r.Context(), tx, time.Now())
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[RefDate]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) resetEUDRDate(w http.ResponseWriter, r *http.Request) error {
	key := chi.URLParam(r, "key")
	if !knownDateKey(key) {
		return httpx.NotFound("unknown reference date")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `delete from food_eudr_reference_date where key=$1`, key); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "food.eudr.date.reset", Entity: "food_eudr_reference_date", EntityID: key}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// --- plot validation ---

// plotReports validates every plot of the tenant and detects overlaps between them.
func plotReports(ctx context.Context, tx pgx.Tx) ([]PlotReport, error) {
	plots, err := db.List[Plot](ctx, tx, `select `+plotCols+` from farm_plot order by name, id`)
	if err != nil {
		return nil, err
	}
	reports := make([]PlotReport, len(plots))
	geoms := map[uuid.UUID]json.RawMessage{}
	for i, p := range plots {
		rep := ValidateGeometry(p.GeoJSON, p.AreaHa, p.Commodity, DefaultGeoRules)
		rep.PlotID, rep.Name, rep.Commodity = p.ID, p.Name, p.Commodity
		reports[i] = rep
		geoms[p.ID] = p.GeoJSON
	}
	DetectOverlaps(reports, geoms, DefaultGeoRules.MaxPositions)
	return reports, nil
}

var plotValidationNotes = []string{
	"Machine-readable reasons carry a stable code and a severity (error, warning, info); the status is invalid when any error is present.",
	fmt.Sprintf("Working defaults: at least %d coordinate decimals, a point for plots up to %.0f ha. TODO(spec): both thresholds must be confirmed against the Official Journal and the Commission's guidance.", DefaultGeoRules.MinDecimals, DefaultGeoRules.PointMaxHa),
	"Overlap detection finds boundary crossings and containment between the outer rings of this tenant's plots; it does not compute the overlapping area.",
}

func (m *Module) validatePlots(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		reps, err := plotReports(r.Context(), tx)
		if err != nil {
			return err
		}
		summary := map[string]int{"valid": 0, "warnings": 0, "invalid": 0}
		for _, p := range reps {
			summary[p.Status]++
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": reps, "total": len(reps), "summary": summary, "reasonCodes": ReasonCodes, "notes": plotValidationNotes})
		return nil
	})
}

func (m *Module) validatePlot(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		reps, err := plotReports(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, p := range reps {
			if p.PlotID == id {
				httpx.JSON(w, http.StatusOK, map[string]any{"report": p, "notes": plotValidationNotes})
				return nil
			}
		}
		return httpx.NotFound("plot not found")
	})
}

// --- due diligence statements ---

// createDDS builds a statement from registered plots (geolocation per Art. 9(1)(d)) when the
// tenant is an operator, or records the reference to the upstream statement when the tenant is
// configured as a downstream trader.
// TODO(spec): map the payload to the EU Information System (TRACES) DDS schema when submitting.
func (m *Module) createDDS(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Reference           string      `json:"reference"`
		OperatorRole        string      `json:"operatorRole"`
		Commodity           string      `json:"commodity"`
		HSCode              string      `json:"hsCode"`
		Description         string      `json:"description"`
		Quantity            float64     `json:"quantity"`
		UOM                 string      `json:"uom"`
		CountryOfProduction string      `json:"countryOfProduction"`
		PlotIDs             []uuid.UUID `json:"plotIds"`
		UpstreamReference   string      `json:"upstreamReference"`
		UpstreamOperator    string      `json:"upstreamOperator"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Commodity = strings.ToLower(in.Commodity)
	if !eudrCommodities[in.Commodity] {
		return httpx.BadRequest("commodity must be one of cattle, cocoa, coffee, oil_palm, rubber, soya, wood (Regulation 2023/1115 Annex I)")
	}
	if in.HSCode == "" || in.Description == "" || in.Quantity <= 0 || len(in.CountryOfProduction) != 2 {
		return httpx.BadRequest("hsCode, description, quantity and ISO countryOfProduction are required")
	}
	if in.UOM == "" {
		in.UOM = "KGM"
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		st, err := eudrSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		if in.OperatorRole != "" && in.OperatorRole != st.DDSRole {
			return httpx.BadRequest("operatorRole must match this tenant's configured role (" + st.DDSRole + "); change it in the EUDR settings")
		}
		var d DDS
		if st.DDSRole == RoleDownstream {
			d, err = m.createUpstreamReference(r.Context(), tx, in.Reference, in.Commodity, in.HSCode, in.Description, in.UOM, in.CountryOfProduction, in.UpstreamReference, in.UpstreamOperator, in.Quantity, len(in.PlotIDs))
		} else {
			d, err = m.createOperatorStatement(r.Context(), tx, in.Reference, in.Commodity, in.HSCode, in.Description, in.UOM, in.CountryOfProduction, in.Quantity, in.PlotIDs)
		}
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "eudr.dds.create", Entity: "eudr_dds", EntityID: d.ID.String(), After: d}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, d)
		return nil
	})
}

func (m *Module) createUpstreamReference(ctx context.Context, tx pgx.Tx, ref, commodity, hs, desc, uom, country, upRef, upOp string, qty float64, nPlots int) (DDS, error) {
	upRef, upOp = strings.TrimSpace(upRef), strings.TrimSpace(upOp)
	if upRef == "" || len(upRef) > 200 || len(upOp) > 200 {
		return DDS{}, httpx.BadRequest("upstreamReference (the reference of the upstream statement, max 200 characters) is required for a downstream trader")
	}
	if nPlots > 0 {
		return DDS{}, httpx.BadRequest("a downstream trader records the upstream reference, not plots; leave plotIds empty")
	}
	if ref == "" {
		ref = "REF-" + shortHash(upRef, hs, time.Now().UnixNano())
	}
	payload := map[string]any{"operatorRole": RoleDownstream, "commodity": commodity, "hsCode": hs, "description": desc,
		"quantity": map[string]any{"value": qty, "uom": uom}, "countryOfProduction": strings.ToUpper(country),
		"upstreamStatement": map[string]any{"reference": upRef, "operator": upOp},
		// TODO(spec): which data a downstream trader must keep alongside the reference is not asserted here.
		"note": "Reference to the upstream due diligence statement; no new statement is created by this tenant."}
	return db.One[DDS](ctx, tx, `insert into eudr_dds (id, tenant_id, reference, operator_role, commodity, hs_code, description, quantity, uom, country_of_production, plot_ids, status, payload, kind, upstream_reference, upstream_operator)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,upper($9),'{}','referenced',$10,'upstream_reference',$11,$12) returning `+ddsCols,
		ids.New(), ref, RoleDownstream, commodity, hs, desc, qty, uom, country, payload, upRef, upOp)
}

func shortHash(parts ...any) string {
	h := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	return strings.ToUpper(hex.EncodeToString(h[:5]))
}

func (m *Module) createOperatorStatement(ctx context.Context, tx pgx.Tx, ref, commodity, hs, desc, uom, country string, qty float64, plotIDs []uuid.UUID) (DDS, error) {
	if len(plotIDs) == 0 {
		return DDS{}, httpx.BadRequest("geolocation of at least one plot is required (Art. 9(1)(d))")
	}
	plots, err := db.List[Plot](ctx, tx, `select `+plotCols+` from farm_plot where id = any($1)`, plotIDs)
	if err != nil {
		return DDS{}, err
	}
	if len(plots) != len(plotIDs) {
		return DDS{}, httpx.BadRequest("unknown plot id")
	}
	var features []map[string]any
	var area float64
	for _, p := range plots {
		if p.Commodity != commodity {
			return DDS{}, httpx.BadRequest(fmt.Sprintf("plot %s produces %s, not %s", p.Name, p.Commodity, commodity))
		}
		var g any
		_ = json.Unmarshal(p.GeoJSON, &g)
		if f, ok := g.(map[string]any); ok && f["type"] == "Feature" {
			g = f["geometry"]
		}
		features = append(features, map[string]any{"type": "Feature", "geometry": g, "properties": map[string]any{"name": p.Name, "producer": p.ProducerName, "areaHa": p.AreaHa, "country": p.Country}})
		area += p.AreaHa
	}
	if ref == "" {
		ref = "DDS-" + shortHash(plotIDs, hs, time.Now().UnixNano())
	}
	payload := map[string]any{"operatorRole": RoleOperator, "commodity": commodity, "hsCode": hs, "description": desc,
		"quantity": map[string]any{"value": qty, "uom": uom}, "countryOfProduction": strings.ToUpper(country),
		"geolocation": map[string]any{"type": "FeatureCollection", "features": features}, "totalAreaHa": area,
		"declaration": "Due diligence exercised; negligible risk that the products are non-compliant (Art. 4(2))"}
	return db.One[DDS](ctx, tx, `insert into eudr_dds (id, tenant_id, reference, operator_role, commodity, hs_code, description, quantity, uom, country_of_production, plot_ids, status, payload)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,upper($9),$10,'ready',$11) returning `+ddsCols,
		ids.New(), ref, RoleOperator, commodity, hs, desc, qty, uom, country, plotIDs, payload)
}

// --- completeness checklist ---

// Checklist item states.
const (
	CheckOK            = "ok"
	CheckMissing       = "missing"
	CheckWarning       = "warning"
	CheckNotApplicable = "not_applicable"
)

// CheckItem is one line of the statement completeness checklist.
type CheckItem struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

// Checklist is the completeness checklist of a statement.
type Checklist struct {
	DDSID    uuid.UUID   `json:"ddsId"`
	Kind     string      `json:"kind"`
	Complete bool        `json:"complete"`
	Items    []CheckItem `json:"items"`
	Notes    []string    `json:"notes"`
}

var checklistNotes = []string{
	"The checklist covers the fields this software holds and the plot validation; it is not a legal assessment.",
	"TODO(spec): the full list of information elements a statement must contain (Regulation (EU) 2023/1115, Art. 9 and Annex II) is to be confirmed against the Official Journal; items are added once confirmed.",
}

// BuildChecklist evaluates a statement against the validation reports of its plots.
func BuildChecklist(d DDS, plots []Plot, reports map[uuid.UUID]PlotReport) Checklist {
	c := Checklist{DDSID: d.ID, Kind: d.Kind, Items: []CheckItem{}, Notes: checklistNotes}
	add := func(key, label, status string, reasons ...string) {
		if reasons == nil {
			reasons = []string{}
		}
		c.Items = append(c.Items, CheckItem{Key: key, Label: label, Status: status, Reasons: reasons})
	}
	cond := func(ok bool, missing string) (string, []string) {
		if ok {
			return CheckOK, nil
		}
		return CheckMissing, []string{missing}
	}
	st, rs := cond(eudrCommodities[d.Commodity], "commodity is not one of the Annex I commodities this software lists")
	add("commodity", "Commodity (Annex I)", st, rs...)
	st, rs = cond(d.HSCode != "", "hsCode is empty")
	add("hs_code", "HS code", st, rs...)
	st, rs = cond(d.Description != "", "description is empty")
	add("description", "Product description", st, rs...)
	st, rs = cond(d.Quantity > 0, "quantity must be above zero")
	add("quantity", "Quantity", st, rs...)
	st, rs = cond(len(d.CountryOfProduction) == 2, "country of production must be an ISO code")
	add("country", "Country of production", st, rs...)

	if d.Kind == KindUpstreamReference {
		st, rs = cond(d.UpstreamReference != "", "no upstream statement reference recorded")
		add("upstream_reference", "Reference to the upstream statement", st, rs...)
		if d.UpstreamOperator == "" {
			add("upstream_operator", "Upstream operator named", CheckWarning, "the operator that made the upstream statement is not recorded")
		} else {
			add("upstream_operator", "Upstream operator named", CheckOK)
		}
		for _, k := range [][2]string{{"geolocation", "Geolocation of plots"}, {"plots_valid", "Plot geolocation valid"}, {"deforestation", "Deforestation-free evidence"}, {"declaration", "Operator declaration"}} {
			add(k[0], k[1], CheckNotApplicable, "held by the upstream operator's statement")
		}
		c.Complete = complete(c.Items)
		return c
	}

	st, rs = cond(len(d.PlotIDs) > 0 && len(plots) == len(d.PlotIDs), "no registered plot is linked, or a linked plot no longer exists")
	add("geolocation", "Geolocation of plots (Art. 9(1)(d))", st, rs...)
	var invalid, warn, overlap, mism, countries, unknown, notFree []string
	for _, p := range plots {
		rep, ok := reports[p.ID]
		if !ok {
			continue
		}
		switch rep.Status {
		case "invalid":
			invalid = append(invalid, p.Name)
		case "warnings":
			warn = append(warn, p.Name)
		}
		if len(rep.Overlaps) > 0 {
			overlap = append(overlap, p.Name)
		}
		if p.Commodity != d.Commodity {
			mism = append(mism, p.Name)
		}
		if !strings.EqualFold(p.Country, d.CountryOfProduction) {
			countries = append(countries, p.Name)
		}
		switch {
		case p.DeforestationFree == nil:
			unknown = append(unknown, p.Name)
		case !*p.DeforestationFree:
			notFree = append(notFree, p.Name)
		}
	}
	listed := func(prefix string, names []string) string { return prefix + strings.Join(names, ", ") }
	if len(invalid) > 0 {
		add("plots_valid", "Plot geolocation valid", CheckMissing, listed("plots with validation errors: ", invalid))
	} else if len(warn) > 0 {
		add("plots_valid", "Plot geolocation valid", CheckWarning, listed("plots with validation warnings: ", warn))
	} else {
		add("plots_valid", "Plot geolocation valid", CheckOK)
	}
	if len(overlap) > 0 {
		add("plots_no_overlap", "Plots do not overlap other plots", CheckWarning, listed("overlapping plots: ", overlap))
	} else {
		add("plots_no_overlap", "Plots do not overlap other plots", CheckOK)
	}
	if len(mism) > 0 {
		add("plots_commodity", "Plots produce the statement's commodity", CheckMissing, listed("plots of another commodity: ", mism))
	} else {
		add("plots_commodity", "Plots produce the statement's commodity", CheckOK)
	}
	if len(countries) > 0 {
		add("plots_country", "Plots lie in the country of production", CheckWarning, listed("plots registered in another country: ", countries))
	} else {
		add("plots_country", "Plots lie in the country of production", CheckOK)
	}
	switch {
	case len(notFree) > 0:
		add("deforestation", "Deforestation-free evidence recorded", CheckMissing, listed("plots recorded as not deforestation-free: ", notFree))
	case len(unknown) > 0:
		add("deforestation", "Deforestation-free evidence recorded", CheckWarning, listed("no assessment recorded for plots: ", unknown))
	default:
		add("deforestation", "Deforestation-free evidence recorded", CheckOK)
	}
	decl, _ := d.Payload["declaration"].(string)
	st, rs = cond(decl != "", "the declaration text is missing from the payload")
	add("declaration", "Operator declaration", st, rs...)
	c.Complete = complete(c.Items)
	return c
}

func complete(items []CheckItem) bool {
	for _, it := range items {
		if it.Status == CheckMissing {
			return false
		}
	}
	return true
}

func (m *Module) ddsChecklist(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := db.One[DDS](r.Context(), tx, `select `+ddsCols+` from eudr_dds where id=$1`, id)
		if err != nil {
			return err
		}
		plots, err := db.List[Plot](r.Context(), tx, `select `+plotCols+` from farm_plot where id = any($1)`, d.PlotIDs)
		if err != nil {
			return err
		}
		reps, err := plotReports(r.Context(), tx)
		if err != nil {
			return err
		}
		byID := map[uuid.UUID]PlotReport{}
		for _, p := range reps {
			byID[p.PlotID] = p
		}
		httpx.JSON(w, http.StatusOK, BuildChecklist(d, plots, byID))
		return nil
	})
}

// eudrRoutes registers the EUDR readiness endpoints under /food. Reads are open to Viewer (the
// group's auth.Require lets GET through); writes need Admin, Ops or Compliance.
func (m *Module) eudrRoutes(r chi.Router) {
	write := auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance)
	r.Get("/eudr/settings", httpx.Handler(m.getEUDRSettings))
	r.With(write).Put("/eudr/settings", httpx.Handler(m.putEUDRSettings))
	r.Get("/eudr/dates", httpx.Handler(m.listEUDRDates))
	r.With(write).Put("/eudr/dates/{key}", httpx.Handler(m.putEUDRDate))
	r.With(write).Delete("/eudr/dates/{key}", httpx.Handler(m.resetEUDRDate))
	r.Get("/eudr/plots/validation", httpx.Handler(m.validatePlots))
	r.Get("/eudr/plots/{id}/validation", httpx.Handler(m.validatePlot))
	r.Get("/eudr/dds/{id}/checklist", httpx.Handler(m.ddsChecklist))
}
