package food

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// Traceability plan (21 CFR 1.1315). A covered person keeps a plan. Reading the public rule
// text, 1.1315(a) lists five elements, and those are the only sections this module asserts:
//
//	(a)(1) procedures used to maintain the required records and make them available
//	(a)(2) procedures to identify foods on the Food Traceability List that are handled
//	(a)(3) how traceability lot codes are assigned
//	(a)(4) the point of contact for questions about the plan and records
//	(a)(5) for a farm, a farm map showing the areas where listed foods are grown
//
// Everything else here (structured helpers, the review reminder, completeness hints, the SVG
// overview, Markdown export) is helper UX and is labelled as such in the output.
//
// TODO(spec): confirm the element wording and numbering against the current eCFR text, and
// whether updates to the plan must be kept for any period (versions are kept indefinitely).

const maxPlanText = 20000

// PlanSection is the free text of one plan element.
type PlanSection struct {
	Text string `json:"text"`
}

// PlanFood is one food on the Food Traceability List that the covered person handles
// (1.1315(a)(2)). The product link is a helper; name and GTIN are snapshots.
type PlanFood struct {
	ProductID   *uuid.UUID `json:"productId,omitempty"`
	GTIN        string     `json:"gtin,omitempty"`
	Name        string     `json:"name"`
	FTLCategory string     `json:"ftlCategory"`
	Note        string     `json:"note,omitempty"`
}

// PlanFoods is element (a)(2).
type PlanFoods struct {
	Text  string     `json:"text"`
	Items []PlanFood `json:"items"`
}

// PlanTLC is element (a)(3); pattern, source and example are helper fields.
type PlanTLC struct {
	Text    string `json:"text"`
	Pattern string `json:"pattern,omitempty"`
	Source  string `json:"source,omitempty"`
	Example string `json:"example,omitempty"`
}

// PlanContact is element (a)(4); the structured fields are helpers.
type PlanContact struct {
	Text  string `json:"text"`
	Name  string `json:"name,omitempty"`
	Role  string `json:"role,omitempty"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// PlanFarmMap is element (a)(5). Applicable is false when the covered person is not a farm.
// PlotIDs selects registered plots; empty means all plots in the registry.
type PlanFarmMap struct {
	Applicable bool        `json:"applicable"`
	Text       string      `json:"text"`
	PlotIDs    []uuid.UUID `json:"plotIds"`
}

// PlanContent is the editable plan document.
type PlanContent struct {
	RecordProcedures PlanSection `json:"recordProcedures"` // (a)(1)
	FoodsHandled     PlanFoods   `json:"foodsHandled"`     // (a)(2)
	TLCAssignment    PlanTLC     `json:"tlcAssignment"`    // (a)(3)
	PointOfContact   PlanContact `json:"pointOfContact"`   // (a)(4)
	FarmMap          PlanFarmMap `json:"farmMap"`          // (a)(5)
}

// PlanVersion is one stored version.
type PlanVersion struct {
	ID        uuid.UUID   `json:"id" db:"id"`
	SiteID    *uuid.UUID  `json:"siteId,omitempty" db:"site_id"`
	Version   int         `json:"version" db:"version"`
	Current   bool        `json:"current" db:"is_current"`
	Content   PlanContent `json:"content" db:"content"`
	Reason    string      `json:"reason" db:"reason"`
	CreatedBy string      `json:"createdBy" db:"created_by"`
	CreatedAt time.Time   `json:"createdAt" db:"created_at"`
}

// PlanSettings are the per-tenant helper settings.
type PlanSettings struct {
	ReviewReminder bool `json:"reviewReminder"`
	ReviewMonths   int  `json:"reviewMonths"`
}

// planNotes label what is rule text and what is helper UX; they appear in every export.
var planNotes = []string{
	"Sections 1 to 5 correspond to the elements listed in 21 CFR 1.1315(a)(1) to (a)(5).",
	"Structured fields (product picks, lot-code pattern, contact fields, plot selection), completeness hints, the farm overview drawing and any review reminder are helpers of this software, not requirements of the rule.",
	"The overview drawing is schematic (no base map) and is not a substitute for a farm map that meets the rule.",
}

const planCols = `id, site_id, version, is_current, content, reason, created_by, created_at`

// FTLCategories lists the food categories of the Food Traceability List as a picking aid.
// TODO(spec): the list is maintained by FDA and changes; confirm it against the current
// Food Traceability List before relying on it. Free text is accepted as well.
var FTLCategories = []string{
	"Cheeses (fresh soft or soft unripened; soft ripened or semi-soft)",
	"Shell eggs",
	"Nut butters",
	"Cucumbers (fresh)",
	"Herbs (fresh)",
	"Leafy greens (fresh)",
	"Leafy greens (fresh-cut)",
	"Melons (fresh)",
	"Peppers (fresh)",
	"Sprouts (fresh)",
	"Tomatoes (fresh)",
	"Tropical tree fruits (fresh)",
	"Fruits and vegetables (fresh-cut)",
	"Finfish (fresh and frozen)",
	"Smoked finfish",
	"Crustaceans",
	"Molluscan shellfish (bivalves)",
	"Ready-to-eat deli salads",
}

func (m *Module) planSite(ctx context.Context, tx pgx.Tx, raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, httpx.BadRequest("siteId must be a UUID")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from site where id=$1)`, id).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, httpx.BadRequest("unknown siteId")
	}
	return &id, nil
}

func planVersions(ctx context.Context, tx pgx.Tx, site *uuid.UUID) ([]PlanVersion, error) {
	return db.List[PlanVersion](ctx, tx, `select `+planCols+` from food_traceability_plan
		where site_id is not distinct from $1 order by version desc limit 500`, site)
}

func planSettings(ctx context.Context, tx pgx.Tx) (PlanSettings, error) {
	s := PlanSettings{ReviewMonths: 12}
	err := tx.QueryRow(ctx, `select review_reminder, review_months from food_traceability_plan_setting`).Scan(&s.ReviewReminder, &s.ReviewMonths)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	return s, err
}

// planStatus is the response of GET /traceability-plan.
type planStatus struct {
	Plan *PlanVersion `json:"plan"`
	// Context holds the helper data the editor offers; none of it is rule text.
	Context  planContext  `json:"context"`
	Review   planReview   `json:"review"`
	Hints    []string     `json:"completenessHints"`
	Notes    []string     `json:"notes"`
	Settings PlanSettings `json:"settings"`
}

type planContext struct {
	FTLCategories []string    `json:"ftlCategories"`
	TLCSources    []tlcSource `json:"tlcSources"`
	PlotCount     int         `json:"plotCount"`
}

type tlcSource struct {
	Source  string `json:"source" db:"source"`
	Lots    int    `json:"lots" db:"lots"`
	Example string `json:"example" db:"example"`
}

type planReview struct {
	// Enabled mirrors the per-tenant setting (off by default). TODO(spec): no review interval
	// is asserted; the 12-month default is a convention of this software.
	Enabled bool       `json:"enabled"`
	Due     bool       `json:"due"`
	DueAt   *time.Time `json:"dueAt,omitempty"`
}

// completenessHints names the elements without any content (helper UX).
func completenessHints(c PlanContent) []string {
	var out []string
	if strings.TrimSpace(c.RecordProcedures.Text) == "" {
		out = append(out, "recordProcedures")
	}
	if strings.TrimSpace(c.FoodsHandled.Text) == "" && len(c.FoodsHandled.Items) == 0 {
		out = append(out, "foodsHandled")
	}
	if strings.TrimSpace(c.TLCAssignment.Text) == "" && c.TLCAssignment.Pattern == "" {
		out = append(out, "tlcAssignment")
	}
	pc := c.PointOfContact
	if strings.TrimSpace(pc.Text) == "" && pc.Name == "" && pc.Email == "" && pc.Phone == "" {
		out = append(out, "pointOfContact")
	}
	if c.FarmMap.Applicable && strings.TrimSpace(c.FarmMap.Text) == "" && len(c.FarmMap.PlotIDs) == 0 {
		out = append(out, "farmMap")
	}
	return out
}

func (m *Module) getPlan(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		site, err := m.planSite(r.Context(), tx, r.URL.Query().Get("siteId"))
		if err != nil {
			return err
		}
		st := planStatus{Notes: planNotes, Hints: []string{}}
		if st.Settings, err = planSettings(r.Context(), tx); err != nil {
			return err
		}
		st.Review.Enabled = st.Settings.ReviewReminder
		cur, err := db.List[PlanVersion](r.Context(), tx, `select `+planCols+` from food_traceability_plan where site_id is not distinct from $1 and is_current`, site)
		if err != nil {
			return err
		}
		if len(cur) == 1 {
			st.Plan = &cur[0]
			st.Hints = completenessHints(cur[0].Content)
			if st.Review.Enabled {
				due := cur[0].CreatedAt.AddDate(0, st.Settings.ReviewMonths, 0)
				st.Review.DueAt = &due
				st.Review.Due = time.Now().After(due)
			}
		}
		st.Context.FTLCategories = FTLCategories
		if st.Context.TLCSources, err = db.List[tlcSource](r.Context(), tx, `select tlc_source as source, count(*)::int as lots, max(tlc) as example
			from food_lot where tlc_source <> '' group by tlc_source order by lots desc, tlc_source limit 20`); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `select count(*)::int from farm_plot`).Scan(&st.Context.PlotCount); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, st)
		return nil
	})
}

func (m *Module) listPlanVersions(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		site, err := m.planSite(r.Context(), tx, r.URL.Query().Get("siteId"))
		if err != nil {
			return err
		}
		items, err := planVersions(r.Context(), tx, site)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[PlanVersion]{Items: items, Total: len(items)})
		return nil
	})
}

func checkText(label, s string) error {
	if len(s) > maxPlanText {
		return httpx.BadRequest(label + " is too long (max " + strconv.Itoa(maxPlanText) + " characters)")
	}
	return nil
}

// normalize validates the content and resolves product snapshots and plot ids inside tx.
func (m *Module) normalizePlan(ctx context.Context, tx pgx.Tx, c *PlanContent) error {
	for label, s := range map[string]string{"recordProcedures.text": c.RecordProcedures.Text, "foodsHandled.text": c.FoodsHandled.Text,
		"tlcAssignment.text": c.TLCAssignment.Text, "pointOfContact.text": c.PointOfContact.Text, "farmMap.text": c.FarmMap.Text} {
		if err := checkText(label, s); err != nil {
			return err
		}
	}
	if len(c.FoodsHandled.Items) > 500 {
		return httpx.BadRequest("at most 500 foods per plan")
	}
	if e := strings.TrimSpace(c.PointOfContact.Email); e != "" {
		if _, err := mail.ParseAddress(e); err != nil {
			return httpx.BadRequest("pointOfContact.email is not a valid address")
		}
	}
	for _, s := range []string{c.TLCAssignment.Pattern, c.TLCAssignment.Source, c.TLCAssignment.Example, c.PointOfContact.Name, c.PointOfContact.Role, c.PointOfContact.Phone} {
		if len(s) > 500 {
			return httpx.BadRequest("structured fields are limited to 500 characters")
		}
	}
	seen := map[uuid.UUID]bool{}
	for i := range c.FoodsHandled.Items {
		it := &c.FoodsHandled.Items[i]
		it.FTLCategory = strings.TrimSpace(it.FTLCategory)
		it.Name = strings.TrimSpace(it.Name)
		if len(it.Note) > 1000 || len(it.FTLCategory) > 200 || len(it.Name) > 300 {
			return httpx.BadRequest("food entries are too long")
		}
		if it.ProductID != nil {
			if seen[*it.ProductID] {
				return httpx.BadRequest("a product can be listed once")
			}
			seen[*it.ProductID] = true
			var gtin, name string
			if err := tx.QueryRow(ctx, `select gtin, name from product where id=$1`, *it.ProductID).Scan(&gtin, &name); err != nil {
				return httpx.BadRequest("unknown productId " + it.ProductID.String())
			}
			it.GTIN = gtin
			if it.Name == "" {
				it.Name = name
			}
		}
		if it.Name == "" {
			return httpx.BadRequest("each food needs a product or a name")
		}
		if it.FTLCategory == "" {
			return httpx.BadRequest("each food needs a Food Traceability List category")
		}
	}
	if c.FarmMap.PlotIDs == nil {
		c.FarmMap.PlotIDs = []uuid.UUID{}
	}
	if len(c.FarmMap.PlotIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from farm_plot where id = any($1)`, c.FarmMap.PlotIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(uniqueIDs(c.FarmMap.PlotIDs)) {
			return httpx.BadRequest("unknown plotIds")
		}
	}
	if c.FoodsHandled.Items == nil {
		c.FoodsHandled.Items = []PlanFood{}
	}
	return nil
}

func uniqueIDs(in []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, id := range in {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (m *Module) putPlan(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		SiteID  string      `json:"siteId"`
		Reason  string      `json:"reason"`
		Content PlanContent `json:"content"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len(in.Reason) > 1000 {
		return httpx.BadRequest("reason is required (max 1000 characters)")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ctx := r.Context()
		site, err := m.planSite(ctx, tx, in.SiteID)
		if err != nil {
			return err
		}
		if err := m.normalizePlan(ctx, tx, &in.Content); err != nil {
			return err
		}
		// Serialize saves per scope so version numbers have no gaps or forks.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended(app_tenant()::text || coalesce($1::text, ''), 20))`, site); err != nil {
			return err
		}
		var prev *PlanVersion
		if cur, err := db.List[PlanVersion](ctx, tx, `select `+planCols+` from food_traceability_plan where site_id is not distinct from $1 and is_current`, site); err != nil {
			return err
		} else if len(cur) == 1 {
			prev = &cur[0]
		}
		next := 1
		if prev != nil {
			next = prev.Version + 1
			if _, err := tx.Exec(ctx, `update food_traceability_plan set is_current=false where id=$1`, prev.ID); err != nil {
				return err
			}
		}
		p := auth.MustFrom(ctx)
		v, err := db.One[PlanVersion](ctx, tx, `insert into food_traceability_plan (id, tenant_id, site_id, version, is_current, content, reason, created_by)
			values ($1, app_tenant(), $2, $3, true, $4, $5, $6) returning `+planCols, ids.New(), site, next, in.Content, in.Reason, p.Actor())
		if err != nil {
			return db.MapErr(err)
		}
		rec := audit.Record{Action: "food.traceability_plan.save", Entity: "food_traceability_plan", EntityID: v.ID.String(), After: v, Reason: in.Reason}
		if prev != nil {
			rec.Before = map[string]any{"id": prev.ID, "version": prev.Version}
		}
		if _, err := audit.Write(ctx, tx, rec); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, v)
		return nil
	})
}

func (m *Module) getPlanSettings(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := planSettings(r.Context(), tx)
		if err == nil {
			httpx.JSON(w, http.StatusOK, s)
		}
		return err
	})
}

func (m *Module) putPlanSettings(w http.ResponseWriter, r *http.Request) error {
	var in PlanSettings
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.ReviewMonths == 0 {
		in.ReviewMonths = 12
	}
	if in.ReviewMonths < 1 || in.ReviewMonths > 60 {
		return httpx.BadRequest("reviewMonths must be between 1 and 60")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := planSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into food_traceability_plan_setting (tenant_id, review_reminder, review_months) values (app_tenant(), $1, $2)
			on conflict (tenant_id) do update set review_reminder=excluded.review_reminder, review_months=excluded.review_months, updated_at=now()`, in.ReviewReminder, in.ReviewMonths); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "food.traceability_plan.settings", Entity: "food_traceability_plan_setting", EntityID: auth.MustFrom(r.Context()).TenantID.String(), Before: before, After: in}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, in)
		return nil
	})
}

// PlanExport is the JSON export.
type PlanExport struct {
	Tenant     string      `json:"tenant"`
	Site       string      `json:"site,omitempty"`
	ExportedAt time.Time   `json:"exportedAt"`
	Version    PlanVersion `json:"plan"`
	Plots      []Plot      `json:"plots,omitempty"`
	Notes      []string    `json:"notes"`
	SHA256     string      `json:"-"`
}

func (m *Module) exportPlan(w http.ResponseWriter, r *http.Request) error {
	format := r.URL.Query().Get("format")
	switch format {
	case "":
		format = "json"
	case "json", "html", "md":
	default:
		return httpx.BadRequest("format must be json, html or md")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ctx := r.Context()
		site, err := m.planSite(ctx, tx, r.URL.Query().Get("siteId"))
		if err != nil {
			return err
		}
		q := `select ` + planCols + ` from food_traceability_plan where site_id is not distinct from $1 and is_current`
		args := []any{site}
		if vs := r.URL.Query().Get("version"); vs != "" {
			n, err := strconv.Atoi(vs)
			if err != nil || n < 1 {
				return httpx.BadRequest("version must be a positive integer")
			}
			q, args = `select `+planCols+` from food_traceability_plan where site_id is not distinct from $1 and version=$2`, []any{site, n}
		}
		vs, err := db.List[PlanVersion](ctx, tx, q, args...)
		if err != nil {
			return err
		}
		if len(vs) == 0 {
			return httpx.NotFound("no traceability plan has been saved yet")
		}
		ex := PlanExport{ExportedAt: time.Now().UTC(), Version: vs[0], Notes: planNotes}
		if err := tx.QueryRow(ctx, `select name from tenant where id=app_tenant()`).Scan(&ex.Tenant); err != nil {
			return err
		}
		if site != nil {
			var name, gln string
			if err := tx.QueryRow(ctx, `select name, gln from site where id=$1`, *site).Scan(&name, &gln); err != nil {
				return err
			}
			ex.Site = name + " (GLN " + gln + ")"
		}
		if ex.Version.Content.FarmMap.Applicable {
			if ex.Plots, err = db.List[Plot](ctx, tx, `select `+plotCols+` from farm_plot where (cardinality($1::uuid[]) = 0 or id = any($1)) order by name`, ex.Version.Content.FarmMap.PlotIDs); err != nil {
				return err
			}
		}
		var body []byte
		ctype, ext := "application/json", "json"
		switch format {
		case "json":
			body, _ = json.MarshalIndent(ex, "", "  ")
		case "html":
			body, ctype, ext = []byte(renderPlanHTML(ex)), "text/html; charset=utf-8", "html"
		case "md":
			body, ctype, ext = []byte(renderPlanMarkdown(ex)), "text/markdown; charset=utf-8", "md"
		}
		sum := sha256.Sum256(body)
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "food.traceability_plan.export", Entity: "food_traceability_plan", EntityID: ex.Version.ID.String(),
			After: map[string]any{"format": format, "version": ex.Version.Version, "sha256": hex.EncodeToString(sum[:])}}); err != nil {
			return err
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="traceability-plan-v%d.%s"`, ex.Version.Version, ext))
		_, _ = w.Write(body)
		return nil
	})
}

type planSectionView struct {
	Ref, Title string
	Text       string
	Lines      []string
}

func planSections(ex PlanExport) []planSectionView {
	c := ex.Version.Content
	foods := []string{}
	for _, f := range c.FoodsHandled.Items {
		s := f.Name + " - " + f.FTLCategory
		if f.GTIN != "" {
			s += " (GTIN " + f.GTIN + ")"
		}
		if f.Note != "" {
			s += ": " + f.Note
		}
		foods = append(foods, s)
	}
	tlc := []string{}
	for _, kv := range [][2]string{{"Pattern", c.TLCAssignment.Pattern}, {"Source", c.TLCAssignment.Source}, {"Example", c.TLCAssignment.Example}} {
		if kv[1] != "" {
			tlc = append(tlc, kv[0]+": "+kv[1])
		}
	}
	contact := []string{}
	for _, kv := range [][2]string{{"Name", c.PointOfContact.Name}, {"Role", c.PointOfContact.Role}, {"E-mail", c.PointOfContact.Email}, {"Phone", c.PointOfContact.Phone}} {
		if kv[1] != "" {
			contact = append(contact, kv[0]+": "+kv[1])
		}
	}
	farm := []string{}
	if c.FarmMap.Applicable {
		for _, p := range ex.Plots {
			farm = append(farm, fmt.Sprintf("%s - %s, %s, %.2f ha", p.Name, p.Commodity, p.Country, p.AreaHa))
		}
	}
	return []planSectionView{
		{"1.1315(a)(1)", "Procedures to maintain and make available the required records", c.RecordProcedures.Text, nil},
		{"1.1315(a)(2)", "Procedures to identify foods on the Food Traceability List that are handled", c.FoodsHandled.Text, foods},
		{"1.1315(a)(3)", "How traceability lot codes are assigned", c.TLCAssignment.Text, tlc},
		{"1.1315(a)(4)", "Point of contact for questions about the plan and records", c.PointOfContact.Text, contact},
		{"1.1315(a)(5)", "Farm map (farms only)", farmText(c.FarmMap), farm},
	}
}

func farmText(f PlanFarmMap) string {
	if !f.Applicable {
		return "Not applicable: the covered person is not a farm."
	}
	return f.Text
}

func renderPlanMarkdown(ex PlanExport) string {
	var b strings.Builder
	title := "Food traceability plan - " + ex.Tenant
	if ex.Site != "" {
		title += " - " + ex.Site
	}
	fmt.Fprintf(&b, "# %s\n\nVersion %d, saved %s by %s.\nReason: %s\n\n", title, ex.Version.Version, ex.Version.CreatedAt.UTC().Format(time.RFC3339), ex.Version.CreatedBy, ex.Version.Reason)
	for i, s := range planSections(ex) {
		fmt.Fprintf(&b, "## %d. %s (21 CFR %s)\n\n", i+1, s.Title, s.Ref)
		if strings.TrimSpace(s.Text) != "" {
			b.WriteString(s.Text + "\n\n")
		}
		for _, l := range s.Lines {
			b.WriteString("- " + l + "\n")
		}
		if len(s.Lines) > 0 {
			b.WriteString("\n")
		}
		if strings.TrimSpace(s.Text) == "" && len(s.Lines) == 0 {
			b.WriteString("_Not yet described._\n\n")
		}
	}
	b.WriteString("## Notes\n\n")
	for _, n := range ex.Notes {
		b.WriteString("- " + n + "\n")
	}
	return b.String()
}

func renderPlanHTML(ex PlanExport) string {
	e := html.EscapeString
	var b strings.Builder
	title := "Food traceability plan - " + ex.Tenant
	if ex.Site != "" {
		title += " - " + ex.Site
	}
	fmt.Fprintf(&b, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>%s</title><style>
body{font:14px/1.5 system-ui,sans-serif;max-width:48rem;margin:2rem auto;padding:0 1rem;color:#111}
h1{font-size:1.5rem}h2{font-size:1.1rem;margin-top:1.8rem;border-bottom:1px solid #ccc}
.ref{color:#555;font-weight:normal;font-size:.85rem}.text{white-space:pre-wrap}.notes{color:#444;font-size:.85rem;margin-top:2rem}
svg{max-width:100%%;height:auto;border:1px solid #ccc}@media print{body{margin:0}}</style></head><body>
<h1>%s</h1><p>Version %d, saved %s by %s.<br>Reason: %s</p>`, e(title), e(title), ex.Version.Version, e(ex.Version.CreatedAt.UTC().Format(time.RFC3339)), e(ex.Version.CreatedBy), e(ex.Version.Reason))
	for i, s := range planSections(ex) {
		fmt.Fprintf(&b, `<h2>%d. %s <span class="ref">21 CFR %s</span></h2>`, i+1, e(s.Title), e(s.Ref))
		if strings.TrimSpace(s.Text) != "" {
			fmt.Fprintf(&b, `<p class="text">%s</p>`, e(s.Text))
		}
		if len(s.Lines) > 0 {
			b.WriteString("<ul>")
			for _, l := range s.Lines {
				b.WriteString("<li>" + e(l) + "</li>")
			}
			b.WriteString("</ul>")
		}
		if strings.TrimSpace(s.Text) == "" && len(s.Lines) == 0 {
			b.WriteString("<p><em>Not yet described.</em></p>")
		}
		if i == 4 && ex.Version.Content.FarmMap.Applicable {
			b.WriteString(PlotOverviewSVG(ex.Plots))
		}
	}
	b.WriteString(`<div class="notes"><strong>Notes</strong><ul>`)
	for _, n := range ex.Notes {
		b.WriteString("<li>" + e(n) + "</li>")
	}
	b.WriteString("</ul></div></body></html>")
	return b.String()
}

// PlotOverviewSVG draws the plots as a schematic overview (equirectangular, no base map).
// Names are escaped. Plots whose geometry cannot be read are skipped.
func PlotOverviewSVG(plots []Plot) string {
	type shape struct {
		name string
		ring [][2]float64
		pt   bool
	}
	var shapes []shape
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range plots {
		g := p.GeoJSON
		var f struct {
			Type     string          `json:"type"`
			Geometry json.RawMessage `json:"geometry"`
			Coords   json.RawMessage `json:"coordinates"`
		}
		if json.Unmarshal(g, &f) != nil {
			continue
		}
		if f.Type == "Feature" {
			g = f.Geometry
			if json.Unmarshal(g, &f) != nil {
				continue
			}
		}
		s := shape{name: p.Name}
		switch f.Type {
		case "Point":
			var c []float64
			if json.Unmarshal(f.Coords, &c) != nil || len(c) < 2 {
				continue
			}
			s.pt, s.ring = true, [][2]float64{{c[0], c[1]}}
		case "Polygon":
			var rings [][][]float64
			if json.Unmarshal(f.Coords, &rings) != nil || len(rings) == 0 {
				continue
			}
			for _, c := range rings[0] {
				if len(c) >= 2 {
					s.ring = append(s.ring, [2]float64{c[0], c[1]})
				}
			}
		default:
			continue
		}
		for _, c := range s.ring {
			minX, maxX = math.Min(minX, c[0]), math.Max(maxX, c[0])
			minY, maxY = math.Min(minY, c[1]), math.Max(maxY, c[1])
		}
		shapes = append(shapes, s)
	}
	if len(shapes) == 0 {
		return `<p><em>No plots with a readable location are registered.</em></p>`
	}
	const w, h, pad = 720.0, 420.0, 30.0
	kx := math.Cos((minY + maxY) / 2 * math.Pi / 180)
	spanX, spanY := math.Max((maxX-minX)*kx, 1e-6), math.Max(maxY-minY, 1e-6)
	scale := math.Min((w-2*pad)/spanX, (h-2*pad)/spanY)
	proj := func(c [2]float64) (float64, float64) {
		return pad + (c[0]-minX)*kx*scale, h - pad - (c[1]-minY)*scale
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" role="img" aria-label="Farm plot overview">`, w, h)
	for _, s := range shapes {
		if s.pt {
			x, y := proj(s.ring[0])
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="5" fill="#2a7"/><text x="%.1f" y="%.1f" font-size="11">%s</text>`, x, y, x+8, y+4, html.EscapeString(s.name))
			continue
		}
		var pts []string
		for _, c := range s.ring {
			x, y := proj(c)
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
		}
		x, y := proj(s.ring[0])
		fmt.Fprintf(&b, `<polygon points="%s" fill="#2a7" fill-opacity=".35" stroke="#165"/><text x="%.1f" y="%.1f" font-size="11">%s</text>`, strings.Join(pts, " "), x, y-3, html.EscapeString(s.name))
	}
	b.WriteString(`<text x="8" y="14" font-size="10" fill="#555">Schematic overview, no base map (helper, not part of the rule text)</text></svg>`)
	return b.String()
}
