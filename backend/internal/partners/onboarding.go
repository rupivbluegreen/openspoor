package partners

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// Partner onboarding: a checklist instantiated from a template, test runs recorded by the
// link test harness (RecordTest), and a readiness score from 0 to 100.

// TemplateItem is one checklist step. Test items are satisfied by a passing test run on their
// channel ("" = any channel).
type TemplateItem struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Kind    string `json:"kind"` // manual | test
	Channel string `json:"channel,omitempty"`
}

// Template is an onboarding checklist template.
type Template struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Items       []TemplateItem `json:"items"`
}

// TODO(spec): the checklists below are practical integration steps, not a statement of what a
// regulation requires of a trading partner. Confirm the DSCSA and EU FMD items with the
// applicable guidance before presenting them as compliance checklists.
var templates = []Template{
	{Key: "dscsa-wholesaler", Name: "DSCSA wholesaler (US)", Description: "Onboard a US wholesale distributor for serialized TI/TS exchange.", Items: []TemplateItem{
		{Key: "identifiers", Title: "Partner GLNs and company prefix recorded", Kind: "manual"},
		{Key: "license", Title: "Partner license or registration checked (authorized trading partner)", Kind: "manual"},
		{Key: "credential", Title: "Partner credential verified in Link", Kind: "manual"},
		{Key: "endpoints", Title: "EPCIS / TI-TS exchange endpoint agreed", Kind: "manual"},
		{Key: "vrs", Title: "Product verification (VRS) routing agreed", Kind: "manual"},
		{Key: "contacts", Title: "Exception and suspect-product contacts exchanged", Kind: "manual"},
		{Key: "test", Title: "Test message delivered over the configured channel", Kind: "test"},
	}},
	{Key: "eu-fmd-pharmacy", Name: "EU FMD pharmacy (EU)", Description: "Onboard a pharmacy customer in an EU market.", Items: []TemplateItem{
		{Key: "identifiers", Title: "Pharmacy GLN or location identifier recorded", Kind: "manual"},
		{Key: "license", Title: "Pharmacy license checked", Kind: "manual"},
		{Key: "nmvs", Title: "Partner confirms connection to its national verification system", Kind: "manual"},
		{Key: "formats", Title: "Order and despatch advice formats agreed", Kind: "manual"},
		{Key: "test", Title: "Test message delivered over the configured channel", Kind: "test"},
	}},
	{Key: "b2b-edi", Name: "B2B EDI partner", Description: "Onboard a partner exchanging orders, despatch advices and invoices.", Items: []TemplateItem{
		{Key: "identifiers", Title: "Partner identifiers (GLN, interchange IDs) exchanged", Kind: "manual"},
		{Key: "formats", Title: "Document types and formats agreed", Kind: "manual"},
		{Key: "mapping", Title: "Inbound mapping saved in the mapping studio", Kind: "manual"},
		{Key: "sla", Title: "Acknowledgement service level agreed", Kind: "manual"},
		{Key: "test", Title: "Test message delivered over the configured channel", Kind: "test"},
	}},
}

// Templates returns the onboarding templates.
func Templates() []Template { return templates }

func templateByKey(k string) (Template, bool) {
	for _, t := range templates {
		if t.Key == k {
			return t, true
		}
	}
	return Template{}, false
}

// OnboardingItem is a checklist item of a partner.
type OnboardingItem struct {
	ID       uuid.UUID  `json:"id" db:"id"`
	Position int        `json:"position" db:"position"`
	Key      string     `json:"key" db:"key"`
	Title    string     `json:"title" db:"title"`
	Kind     string     `json:"kind" db:"kind"`
	Channel  string     `json:"channel" db:"channel"`
	Done     bool       `json:"done" db:"done"`
	DoneBy   string     `json:"doneBy" db:"done_by"`
	DoneAt   *time.Time `json:"doneAt,omitempty" db:"done_at"`
	Note     string     `json:"note" db:"note"`
}

const itemCols = `id, position, key, title, kind, channel, done, done_by, done_at, note`

// TestRun is one run of the onboarding test harness.
type TestRun struct {
	ID        uuid.UUID      `json:"id" db:"id"`
	Channel   string         `json:"channel" db:"channel"`
	Passed    bool           `json:"passed" db:"passed"`
	Detail    map[string]any `json:"detail" db:"detail"`
	CreatedAt time.Time      `json:"createdAt" db:"created_at"`
}

// Onboarding is a partner's onboarding state with its readiness score.
type Onboarding struct {
	PartnerID uuid.UUID        `json:"partnerId"`
	Started   bool             `json:"started"`
	Template  string           `json:"template,omitempty"`
	StartedAt *time.Time       `json:"startedAt,omitempty"`
	Items     []OnboardingItem `json:"items"`
	Tests     []TestRun        `json:"tests"`
	Score     int              `json:"score"`
}

// Score computes readiness: the share of checklist items satisfied (manual items done, test
// items backed by a passing latest test run on their channel), from 0 to 100.
func Score(items []OnboardingItem, tests []TestRun) int {
	if len(items) == 0 {
		return 0
	}
	ok := 0
	for _, it := range items {
		if satisfied(it, tests) {
			ok++
		}
	}
	return int(math.Round(100 * float64(ok) / float64(len(items))))
}

// satisfied: tests are ordered newest first; the latest run on the channel decides.
func satisfied(it OnboardingItem, tests []TestRun) bool {
	if it.Kind != "test" {
		return it.Done
	}
	for _, t := range tests {
		if it.Channel == "" || t.Channel == it.Channel {
			return t.Passed
		}
	}
	return false
}

// LoadOnboarding reads a partner's onboarding inside tx.
func LoadOnboarding(ctx context.Context, tx pgx.Tx, partnerID uuid.UUID) (Onboarding, error) {
	o := Onboarding{PartnerID: partnerID}
	var tpl string
	var started time.Time
	err := tx.QueryRow(ctx, `select template, started_at from partner_onboarding where partner_id = $1`, partnerID).Scan(&tpl, &started)
	if err == nil {
		o.Started, o.Template, o.StartedAt = true, tpl, &started
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return o, err
	}
	if o.Items, err = db.List[OnboardingItem](ctx, tx, `select `+itemCols+` from partner_onboarding_item where partner_id = $1 order by position`, partnerID); err != nil {
		return o, err
	}
	if o.Tests, err = db.List[TestRun](ctx, tx, `select id, channel, passed, detail, created_at from partner_test_run where partner_id = $1 order by created_at desc limit 50`, partnerID); err != nil {
		return o, err
	}
	for i := range o.Items {
		if o.Items[i].Kind == "test" {
			o.Items[i].Done = satisfied(o.Items[i], o.Tests)
		}
	}
	o.Score = Score(o.Items, o.Tests)
	return o, nil
}

// RecordTest stores a test-harness result for a partner inside tx (used by link).
func RecordTest(ctx context.Context, tx pgx.Tx, partnerID uuid.UUID, channel string, passed bool, detail map[string]any) (TestRun, error) {
	if detail == nil {
		detail = map[string]any{}
	}
	t, err := db.One[TestRun](ctx, tx, `insert into partner_test_run (id, tenant_id, partner_id, channel, passed, detail) values ($1, app_tenant(), $2,$3,$4,$5)
		returning id, channel, passed, detail, created_at`, ids.New(), partnerID, channel, passed, detail)
	if err != nil {
		return t, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "partner.onboarding.test", Entity: "partner", EntityID: partnerID.String(),
		After: map[string]any{"channel": channel, "passed": passed, "detail": detail}})
	return t, err
}

func (m *Module) listTemplates(w http.ResponseWriter, _ *http.Request) error {
	httpx.JSON(w, http.StatusOK, httpx.Page[Template]{Items: templates, Total: len(templates)})
	return nil
}

func (m *Module) getOnboarding(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := Get(r.Context(), tx, id); err != nil {
			return err
		}
		o, err := LoadOnboarding(r.Context(), tx, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, o)
		}
		return err
	})
}

func (m *Module) startOnboarding(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Template string `json:"template"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	tpl, ok := templateByKey(in.Template)
	if !ok {
		return httpx.BadRequest("unknown template")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := Get(r.Context(), tx, id); err != nil {
			return err
		}
		ct, err := tx.Exec(r.Context(), `insert into partner_onboarding (tenant_id, partner_id, template) values (app_tenant(), $1, $2) on conflict do nothing`, id, tpl.Key)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.Conflict("onboarding has already started for this partner")
		}
		for i, it := range tpl.Items {
			if _, err := tx.Exec(r.Context(), `insert into partner_onboarding_item (id, tenant_id, partner_id, position, key, title, kind, channel) values ($1, app_tenant(), $2,$3,$4,$5,$6,$7)`,
				ids.New(), id, i+1, it.Key, it.Title, it.Kind, it.Channel); err != nil {
				return err
			}
		}
		o, err := LoadOnboarding(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.onboarding.start", Entity: "partner", EntityID: id.String(), After: map[string]any{"template": tpl.Key}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, o)
		return nil
	})
}

func (m *Module) updateItem(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	itemID, err := ids.Param(r, "itemId")
	if err != nil {
		return err
	}
	var in struct {
		Done bool    `json:"done"`
		Note *string `json:"note"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	me := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[OnboardingItem](r.Context(), tx, `select `+itemCols+` from partner_onboarding_item where id = $1 and partner_id = $2`, itemID, id)
		if err != nil {
			return err
		}
		if before.Kind == "test" {
			return httpx.Conflict("test items are completed by a passing test run")
		}
		note := before.Note
		if in.Note != nil {
			note = *in.Note
		}
		after, err := db.One[OnboardingItem](r.Context(), tx, `update partner_onboarding_item set done = $2, note = $3,
			done_by = case when $2 then $4 else '' end, done_at = case when $2 then now() end where id = $1 returning `+itemCols, itemID, in.Done, note, me.Actor())
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.onboarding.item", Entity: "partner", EntityID: id.String(), Before: before, After: after}); err != nil {
			return err
		}
		o, err := LoadOnboarding(r.Context(), tx, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, o)
		}
		return err
	})
}
