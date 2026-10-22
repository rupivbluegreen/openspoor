// Package connectors hosts country connectors: the catalog, per-tenant connector
// instances, mapping captured EPCIS events to regulatory reports, submission with retries
// and backoff, status polling, the regulatory alert inbox (parsed reason, affected serials,
// suggested fix, one-click resubmit) and versioned YAML rule packs.
package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	connector "github.com/openspoor/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/eufmd"
	"github.com/openspoor/openspoor/internal/connectors/mockhub"
	"github.com/openspoor/openspoor/internal/connectors/sarsd"
	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/rulepacks"
)

// Job kinds.
const (
	JobDispatch = "connectors.dispatch"
	JobSubmit   = "connectors.submit"
	JobPoll     = "connectors.poll"
)

// Module is the connectors bounded context.
type Module struct {
	kernel.Deps
	Mocks    *mockhub.Transport
	registry map[string]connector.Connector
	packs    []rulepacks.Pack
}

// New creates the module with the reference connectors. Their HTTP clients route
// *.mock.local to the in-process mock hubs and everything else to the network.
func New(d kernel.Deps) (*Module, error) {
	mt := mockhub.NewTransport()
	client := &http.Client{Timeout: 15 * time.Second, Transport: mt}
	eu, sa := eufmd.New(), sarsd.New()
	eu.Client, sa.Client = client, client
	packs, err := rulepacks.Load()
	if err != nil {
		return nil, err
	}
	m := &Module{Deps: d, Mocks: mt, packs: packs, registry: map[string]connector.Connector{}}
	for _, c := range []connector.Connector{eu, sa} {
		m.registry[c.Info().ID] = c
	}
	return m, nil
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "connectors" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleCompliance, auth.RoleIntegration))
		r.Get("/connectors", httpx.Handler(m.catalog))
		r.Get("/connectors/instances", httpx.Handler(m.listInstances))
		r.Post("/connectors/instances", httpx.Handler(m.createInstance))
		r.Put("/connectors/instances/{id}", httpx.Handler(m.updateInstance))
		r.Post("/connectors/instances/{id}/submit", httpx.Handler(m.manualSubmit))
		r.Get("/reports", httpx.Handler(m.listReports))
		r.Get("/reports/{id}", httpx.Handler(m.getReport))
		r.Post("/reports/{id}/resubmit", httpx.Handler(m.resubmit))
		r.Get("/rulepacks", httpx.Handler(m.listPacks))
		r.Get("/rulepacks/{id}", httpx.Handler(m.getPack))
		r.Post("/rulepacks/evaluate", httpx.Handler(m.evaluate))
	})
}

// RegisterJobs implements kernel.Worker.
func (m *Module) RegisterJobs(r *jobs.Runner) {
	r.Handle(JobDispatch, m.dispatchJob)
	r.Handle(JobSubmit, m.submitJob)
	r.Handle(JobPoll, m.pollJob)
}

func (m *Module) catalog(w http.ResponseWriter, _ *http.Request) error {
	out := make([]connector.Info, 0, len(m.registry))
	for _, c := range m.registry {
		out = append(out, c.Info())
	}
	slices.SortFunc(out, func(a, b connector.Info) int { return strings.Compare(a.ID, b.ID) })
	httpx.JSON(w, http.StatusOK, httpx.Page[connector.Info]{Items: out, Total: len(out)})
	return nil
}

// Instance is a tenant's configured connector.
type Instance struct {
	ID        uuid.UUID         `json:"id" db:"id"`
	Connector string            `json:"connector" db:"connector"`
	Name      string            `json:"name" db:"name"`
	Config    map[string]string `json:"config" db:"config"`
	Enabled   bool              `json:"enabled" db:"enabled"`
	CreatedAt time.Time         `json:"createdAt" db:"created_at"`
	Secrets   []byte            `json:"-" db:"secrets"`
	TenantID  uuid.UUID         `json:"-" db:"tenant_id"`
	HasSecret map[string]bool   `json:"secretsSet" db:"-"`
}

const instCols = `id, connector, name, config, enabled, created_at, secrets, tenant_id`

func (m *Module) listInstances(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Instance](r.Context(), tx, `select `+instCols+` from connector_instance order by created_at`)
		for i := range items {
			items[i].HasSecret = m.secretKeys(r.Context(), items[i])
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Instance]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) secretKeys(ctx context.Context, in Instance) map[string]bool {
	out := map[string]bool{}
	if len(in.Secrets) == 0 {
		return out
	}
	b, err := m.Keys.Open(ctx, in.TenantID, in.Secrets, []byte(in.ID.String()))
	if err != nil {
		return out
	}
	var s map[string]string
	_ = json.Unmarshal(b, &s)
	for k := range s {
		out[k] = true
	}
	return out
}

type instanceInput struct {
	Connector string            `json:"connector"`
	Name      string            `json:"name"`
	Config    map[string]string `json:"config"`
	Enabled   *bool             `json:"enabled"`
}

// split separates secret config values (sealed at rest) from plain ones.
func (m *Module) split(ctx context.Context, c connector.Connector, id uuid.UUID, cfg map[string]string, prev []byte) (map[string]string, []byte, error) {
	tenant := kernel.TenantOf(ctx)
	plain := map[string]string{}
	secret := map[string]string{}
	if len(prev) > 0 {
		if b, err := m.Keys.Open(ctx, tenant, prev, []byte(id.String())); err == nil {
			_ = json.Unmarshal(b, &secret)
		}
	}
	fields := map[string]connector.ConfigField{}
	for _, f := range c.Info().Config {
		fields[f.Key] = f
	}
	for k, v := range cfg {
		f, ok := fields[k]
		if !ok {
			return nil, nil, httpx.BadRequest("unknown config key " + k)
		}
		if f.Secret {
			if v != "" {
				secret[k] = v
			}
		} else {
			plain[k] = v
		}
	}
	for _, f := range c.Info().Config {
		if _, ok := plain[f.Key]; !ok && !f.Secret && f.Default != "" {
			plain[f.Key] = f.Default
		}
		if f.Required && plain[f.Key] == "" && secret[f.Key] == "" {
			return nil, nil, httpx.BadRequest("config " + f.Key + " is required")
		}
	}
	var sealed []byte
	if len(secret) > 0 {
		b, _ := json.Marshal(secret)
		var err error
		if sealed, err = m.Keys.Seal(ctx, tenant, b, []byte(id.String())); err != nil {
			return nil, nil, err
		}
	}
	return plain, sealed, nil
}

func (m *Module) createInstance(w http.ResponseWriter, r *http.Request) error {
	var in instanceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	c, ok := m.registry[in.Connector]
	if !ok {
		return httpx.BadRequest("unknown connector " + in.Connector)
	}
	if in.Name == "" {
		in.Name = c.Info().Name
	}
	id := ids.New()
	plain, sealed, err := m.split(r.Context(), c, id, in.Config, nil)
	if err != nil {
		return err
	}
	enabled := in.Enabled == nil || *in.Enabled
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		inst, err := db.One[Instance](r.Context(), tx, `insert into connector_instance (id, tenant_id, connector, name, config, secrets, enabled) values ($1, app_tenant(), $2,$3,$4,$5,$6)
			returning `+instCols, id, in.Connector, in.Name, plain, sealed, enabled)
		if err != nil {
			return err
		}
		inst.HasSecret = m.secretKeys(r.Context(), inst)
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "connector.create", Entity: "connector_instance", EntityID: id.String(), After: inst}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, inst)
		return nil
	})
}

func (m *Module) updateInstance(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in instanceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		cur, err := db.One[Instance](r.Context(), tx, `select `+instCols+` from connector_instance where id=$1`, id)
		if err != nil {
			return err
		}
		c := m.registry[cur.Connector]
		if in.Config == nil {
			in.Config = cur.Config
		}
		plain, sealed, err := m.split(r.Context(), c, id, in.Config, cur.Secrets)
		if err != nil {
			return err
		}
		if in.Name == "" {
			in.Name = cur.Name
		}
		enabled := cur.Enabled
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		inst, err := db.One[Instance](r.Context(), tx, `update connector_instance set name=$2, config=$3, secrets=$4, enabled=$5 where id=$1 returning `+instCols,
			id, in.Name, plain, sealed, enabled)
		if err != nil {
			return err
		}
		inst.HasSecret = m.secretKeys(r.Context(), inst)
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "connector.update", Entity: "connector_instance", EntityID: id.String(), Before: cur, After: inst}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, inst)
		return nil
	})
}

// effectiveConfig merges plain and decrypted secret config and resolves "local-mock".
func (m *Module) effectiveConfig(ctx context.Context, in Instance) connector.Config {
	cfg := connector.Config{}
	for k, v := range in.Config {
		cfg[k] = v
	}
	if len(in.Secrets) > 0 {
		if b, err := m.Keys.Open(ctx, in.TenantID, in.Secrets, []byte(in.ID.String())); err == nil {
			var s map[string]string
			_ = json.Unmarshal(b, &s)
			for k, v := range s {
				cfg[k] = v
			}
		}
	}
	if cfg["baseUrl"] == "local-mock" {
		host := mockhub.EUHost
		if in.Connector == "sa-rsd" {
			host = mockhub.SAHost
		}
		cfg["baseUrl"] = "http://" + host
	}
	return cfg
}

// --- dispatch: captured events -> reports ---

type capturePayload struct {
	CaptureID  uuid.UUID  `json:"captureId"`
	InstanceID *uuid.UUID `json:"instanceId,omitempty"`
}

func glnOf(id string) string {
	c := lib.Canonical(id)
	if i := strings.Index(c, "/414/"); i >= 0 {
		g := c[i+5:]
		if j := strings.Index(g, "/"); j >= 0 {
			g = g[:j]
		}
		return g
	}
	return ""
}

// toEvents converts a capture's events into the connector SDK view.
func toEvents(ctx context.Context, tx pgx.Tx, captureID uuid.UUID) ([]connector.Event, error) {
	rows, err := tx.Query(ctx, `select doc from epcis_event where capture_id=$1 order by event_time`, captureID)
	if err != nil {
		return nil, err
	}
	var evs []lib.Event
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		var e lib.Event
		if err := json.Unmarshal(b, &e); err != nil {
			rows.Close()
			return nil, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	products := map[string]connector.Product{}
	var out []connector.Event
	for _, e := range evs {
		ce := connector.Event{EventID: e.EventID, Type: e.Type, Action: e.Action, BizStep: e.BizStep, Disposition: e.Disposition, EventTime: e.EventTime, Products: map[string]connector.Product{}}
		if e.BizLocation != nil {
			ce.Location = glnOf(e.BizLocation.ID)
		} else if e.ReadPoint != nil {
			ce.Location = glnOf(e.ReadPoint.ID)
		}
		for _, s := range e.SourceList {
			if s.Type == "owning_party" {
				ce.Source = glnOf(s.Source)
			}
		}
		for _, d := range e.DestinationList {
			if d.Type == "owning_party" {
				ce.Destination = glnOf(d.Destination)
			}
		}
		ids := e.EPCList
		if e.Type == lib.AggregationEvent {
			ids = e.ChildEPCs
		}
		if e.Type == lib.TransformationEvent {
			ids = e.OutputEPCList
		}
		// Expand containers (SSCC) to the packs inside them.
		var expand []string
		for _, id := range ids {
			c := lib.Canonical(id)
			if g, s, _ := lib.Key(c); g != "" && s != "" {
				expand = append(expand, c)
			} else {
				ce.Containers = append(ce.Containers, c)
			}
		}
		if len(ce.Containers) > 0 {
			r, err := tx.Query(ctx, `with recursive t(epc, d) as (select epc, 0 from serial_state where parent_epc = any($1)
				union all select s.epc, t.d+1 from serial_state s join t on s.parent_epc=t.epc where t.d < 5) select epc from t`, ce.Containers)
			if err != nil {
				return nil, err
			}
			for r.Next() {
				var x string
				if r.Scan(&x) == nil {
					if g, s, _ := lib.Key(x); g != "" && s != "" {
						expand = append(expand, x)
					}
				}
			}
			r.Close()
		}
		lotOf := map[string][2]string{}
		if len(expand) > 0 {
			r, err := tx.Query(ctx, `select epc, coalesce(lot,''), coalesce(to_char(expiry,'YYYY-MM-DD'),'') from serial_state where epc = any($1)`, expand)
			if err != nil {
				return nil, err
			}
			for r.Next() {
				var x, l, ex string
				if r.Scan(&x, &l, &ex) == nil {
					lotOf[x] = [2]string{l, ex}
				}
			}
			r.Close()
		}
		ilmdLot, _ := e.ILMD["lotNumber"].(string)
		ilmdExp, _ := e.ILMD["itemExpirationDate"].(string)
		for _, c := range expand {
			g, s, _ := lib.Key(c)
			p := connector.Pack{GTIN: g, Serial: s, Lot: lotOf[c][0], Expiry: lotOf[c][1]}
			if p.Lot == "" {
				p.Lot = ilmdLot
			}
			if p.Expiry == "" {
				p.Expiry = ilmdExp
			}
			ce.Packs = append(ce.Packs, p)
			if _, ok := products[g]; !ok {
				if pr, err := masterdata.ByGTIN(ctx, tx, g); err == nil {
					np := connector.Product{GTIN: g, Name: pr.Name, MarketCodes: pr.MarketCodes, DosageForm: pr.DosageForm, Strength: pr.Strength, PackSize: pr.PackSize}
					if pr.NDC != nil {
						np.NDC = *pr.NDC
					}
					products[g] = np
				} else {
					products[g] = connector.Product{GTIN: g}
				}
			}
			ce.Products[g] = products[g]
		}
		out = append(out, ce)
	}
	return out, nil
}

// targets reports whether the event's products are destined for the connector's market.
func targets(ctx context.Context, tx pgx.Tx, market string, e connector.Event) bool {
	for g := range e.Products {
		p, err := masterdata.ByGTIN(ctx, tx, g)
		if err != nil || len(p.TargetMarkets) == 0 || slices.Contains(p.TargetMarkets, market) {
			return true
		}
		if market == "EU" {
			for _, mk := range p.TargetMarkets {
				if euMember[mk] {
					return true
				}
			}
		}
	}
	return false
}

var euMember = map[string]bool{"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true, "DK": true, "EE": true, "FI": true, "FR": true, "DE": true,
	"GR": true, "HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true, "MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true,
	"SI": true, "ES": true, "SE": true, "IS": true, "LI": true, "NO": true}

func (m *Module) dispatchJob(ctx context.Context, j jobs.Job) error {
	var p capturePayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent{Err: err}
	}
	tenant := *j.TenantID
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		insts, err := db.List[Instance](ctx, tx, `select `+instCols+` from connector_instance where enabled and ($1::uuid is null or id=$1)`, p.InstanceID)
		if err != nil || len(insts) == 0 {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `select status from epcis_capture where id=$1`, p.CaptureID).Scan(&status); err != nil {
			return err
		}
		if status != "completed" {
			return fmt.Errorf("capture %s not projected yet", p.CaptureID) // retried with backoff
		}
		events, err := toEvents(ctx, tx, p.CaptureID)
		if err != nil {
			return err
		}
		for _, inst := range insts {
			c := m.registry[inst.Connector]
			if c == nil {
				continue
			}
			info := c.Info()
			for _, e := range events {
				if !slices.Contains(info.Triggers, e.BizStep) || len(e.Packs) == 0 || !targets(ctx, tx, info.Market, e) {
					continue
				}
				payloads, err := c.Map(ctx, e)
				if err != nil {
					return jobs.Permanent{Err: err}
				}
				for _, pl := range payloads {
					rid := ids.New()
					ct, err := tx.Exec(ctx, `insert into regulatory_report (id, tenant_id, instance_id, connector, kind, idem_key, payload, packs, capture_id, event_id)
						values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9) on conflict (tenant_id, instance_id, idem_key) do nothing`,
						rid, inst.ID, inst.Connector, pl.Kind, pl.Key, pl.Body, pl.Packs, p.CaptureID, e.EventID)
					if err != nil {
						return err
					}
					if ct.RowsAffected() == 1 {
						if _, err := jobs.Enqueue(ctx, tx, &tenant, JobSubmit, map[string]any{"reportId": rid}, jobs.MaxAttempts(6)); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	})
}

type reportPayload struct {
	ReportID uuid.UUID `json:"reportId"`
	Poll     int       `json:"poll"`
}

// Report is a regulatory submission.
type Report struct {
	ID            uuid.UUID        `json:"id" db:"id"`
	InstanceID    uuid.UUID        `json:"instanceId" db:"instance_id"`
	Connector     string           `json:"connector" db:"connector"`
	Kind          string           `json:"kind" db:"kind"`
	Key           string           `json:"key" db:"idem_key"`
	Payload       json.RawMessage  `json:"payload" db:"payload"`
	Packs         []connector.Pack `json:"packs" db:"packs"`
	Status        string           `json:"status" db:"status"`
	SubmissionRef string           `json:"submissionRef" db:"submission_ref"`
	Attempts      int              `json:"attempts" db:"attempts"`
	Response      json.RawMessage  `json:"response,omitempty" db:"response"`
	Rejection     json.RawMessage  `json:"rejection,omitempty" db:"rejection"`
	Alert         json.RawMessage  `json:"alert,omitempty" db:"alert"`
	CaptureID     *uuid.UUID       `json:"captureId,omitempty" db:"capture_id"`
	EventID       string           `json:"eventId" db:"event_id"`
	CreatedAt     time.Time        `json:"createdAt" db:"created_at"`
	UpdatedAt     time.Time        `json:"updatedAt" db:"updated_at"`
}

const reportCols = `id, instance_id, connector, kind, idem_key, payload, packs, status, submission_ref, attempts, response, rejection, alert, capture_id, event_id, created_at, updated_at`

func (m *Module) loadReport(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Report, Instance, error) {
	r, err := db.One[Report](ctx, tx, `select `+reportCols+` from regulatory_report where id=$1`, id)
	if err != nil {
		return r, Instance{}, err
	}
	in, err := db.One[Instance](ctx, tx, `select `+instCols+` from connector_instance where id=$1`, r.InstanceID)
	return r, in, err
}

func (m *Module) submitJob(ctx context.Context, j jobs.Job) error {
	var p reportPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent{Err: err}
	}
	tenant := *j.TenantID
	var rep Report
	var inst Instance
	if err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		rep, inst, err = m.loadReport(ctx, tx, p.ReportID)
		return err
	}); err != nil {
		return err
	}
	if rep.Status != "queued" {
		return nil
	}
	c := m.registry[inst.Connector]
	sub, err := c.Submit(ctx, m.effectiveConfig(ctx, inst), connector.Payload{Kind: rep.Kind, Key: rep.Key, Body: rep.Payload, Packs: rep.Packs})
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		if err != nil {
			last := j.Attempts >= j.MaxAttempts
			status := "queued"
			if last {
				status = "failed"
			}
			if _, e := tx.Exec(ctx, `update regulatory_report set attempts=attempts+1, status=$2, response=$3, updated_at=now() where id=$1`,
				rep.ID, status, map[string]string{"error": err.Error()}); e != nil {
				return e
			}
			if last {
				al := connector.Alert{Severity: "error", Title: c.Info().Name + ": submission failed after retries", Reason: err.Error(),
					SuggestedFix: "Check the hub endpoint and credentials in the connector settings, then resubmit.", Packs: rep.Packs, Retryable: true}
				if e := m.raise(ctx, tx, rep, al); e != nil {
					return e
				}
				return jobs.Permanent{Err: err}
			}
			return err // retried with backoff by the job runner
		}
		if _, err := tx.Exec(ctx, `update regulatory_report set attempts=attempts+1, status='submitted', submission_ref=$2, updated_at=now() where id=$1`, rep.ID, sub.Ref); err != nil {
			return err
		}
		_, err := jobs.Enqueue(ctx, tx, &tenant, JobPoll, reportPayload{ReportID: rep.ID}, jobs.RunAt(time.Now().Add(500*time.Millisecond)))
		return err
	})
}

func (m *Module) pollJob(ctx context.Context, j jobs.Job) error {
	var p reportPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent{Err: err}
	}
	tenant := *j.TenantID
	var rep Report
	var inst Instance
	if err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		rep, inst, err = m.loadReport(ctx, tx, p.ReportID)
		return err
	}); err != nil {
		return err
	}
	if rep.Status != "submitted" {
		return nil
	}
	c := m.registry[inst.Connector]
	st, err := c.PollStatus(ctx, m.effectiveConfig(ctx, inst), connector.Submission{Ref: rep.SubmissionRef})
	if err != nil {
		return err
	}
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		switch st.State {
		case connector.Pending:
			if p.Poll >= 40 {
				_, err := tx.Exec(ctx, `update regulatory_report set status='failed', response=$2, updated_at=now() where id=$1`, rep.ID, st.Raw)
				return err
			}
			delay := time.Duration(min(1<<p.Poll, 300)) * time.Second
			_, err := jobs.Enqueue(ctx, tx, &tenant, JobPoll, reportPayload{ReportID: rep.ID, Poll: p.Poll + 1}, jobs.RunAt(time.Now().Add(delay)))
			return err
		case connector.Accepted:
			_, err := tx.Exec(ctx, `update regulatory_report set status='accepted', response=$2, updated_at=now() where id=$1`, rep.ID, st.Raw)
			return err
		default:
			rej := *st.Rejection
			rej.Payload = connector.Payload{Kind: rep.Kind, Key: rep.Key, Body: rep.Payload, Packs: rep.Packs}
			al := c.HandleRejection(ctx, rej)
			if _, err := tx.Exec(ctx, `update regulatory_report set status='rejected', response=$2, rejection=$3, alert=$4, updated_at=now() where id=$1`,
				rep.ID, st.Raw, rej, al); err != nil {
				return err
			}
			return m.raise(ctx, tx, rep, al)
		}
	})
}

func (m *Module) raise(ctx context.Context, tx pgx.Tx, rep Report, al connector.Alert) error {
	detail := map[string]any{"reportId": rep.ID, "connector": rep.Connector, "kind": rep.Kind, "reason": al.Reason, "suggestedFix": al.SuggestedFix,
		"packs": al.Packs, "retryable": al.Retryable}
	_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
		values ($1, app_tenant(), 'regulatory.rejection', $2, $3, $4, 'regulatory_report', $5, $6) on conflict do nothing`,
		ids.New(), al.Severity, al.Title, detail, rep.ID.String(), "report:"+rep.ID.String())
	return err
}

func (m *Module) listReports(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Report](r.Context(), tx, `select `+reportCols+` from regulatory_report where ($1='' or status=$1) and ($2='' or connector=$2)
			order by created_at desc limit 500`, q.Get("status"), q.Get("connector"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Report]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getReport(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rep, err := db.One[Report](r.Context(), tx, `select `+reportCols+` from regulatory_report where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, rep)
		}
		return err
	})
}

// Resubmit requeues a rejected or failed report inside a tenant tx and resolves its alert.
// It is used by the alert inbox (one click) and by the Act agents.
func Resubmit(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, id uuid.UUID) (Report, error) {
	rep, err := db.One[Report](ctx, tx, `update regulatory_report set status='queued', submission_ref='', updated_at=now()
		where id=$1 and status in ('rejected','failed') returning `+reportCols, id)
	if err != nil {
		return rep, httpx.Conflict("only rejected or failed reports can be resubmitted")
	}
	if _, err := tx.Exec(ctx, `update alert set status='resolved', resolved_at=now() where entity='regulatory_report' and entity_id=$1 and status='open'`, id.String()); err != nil {
		return rep, err
	}
	if _, err := jobs.Enqueue(ctx, tx, &tenant, JobSubmit, map[string]any{"reportId": id}, jobs.MaxAttempts(6)); err != nil {
		return rep, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "report.resubmit", Entity: "regulatory_report", EntityID: id.String(), After: map[string]any{"kind": rep.Kind}})
	return rep, err
}

func (m *Module) resubmit(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rep, err := Resubmit(r.Context(), tx, kernel.TenantOf(r.Context()), id)
		if err == nil {
			httpx.JSON(w, http.StatusAccepted, rep)
		}
		return err
	})
}

func (m *Module) manualSubmit(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		CaptureID uuid.UUID `json:"captureId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := db.One[Instance](r.Context(), tx, `select `+instCols+` from connector_instance where id=$1`, id); err != nil {
			return err
		}
		jid, err := jobs.Enqueue(r.Context(), tx, &tenant, JobDispatch, capturePayload{CaptureID: in.CaptureID, InstanceID: &id})
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusAccepted, map[string]any{"jobId": jid})
		return nil
	})
}

// --- rule packs ---

func (m *Module) listPacks(w http.ResponseWriter, _ *http.Request) error {
	httpx.JSON(w, http.StatusOK, httpx.Page[rulepacks.Pack]{Items: m.packs, Total: len(m.packs)})
	return nil
}

func (m *Module) getPack(w http.ResponseWriter, r *http.Request) error {
	for _, p := range m.packs {
		if p.ID == chi.URLParam(r, "id") {
			httpx.JSON(w, http.StatusOK, p)
			return nil
		}
	}
	return httpx.NotFound("unknown rule pack")
}

// Finding is one rule evaluation result.
type Finding struct {
	Pack     string `json:"pack"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Entity   string `json:"entity"`
	EntityID string `json:"entityId"`
	Message  string `json:"message"`
	Source   string `json:"source"`
}

// evaluate checks the tenant's products and serial pools against the packs for their markets.
func (m *Module) evaluate(w http.ResponseWriter, r *http.Request) error {
	var findings []Finding
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select p.id, p.gtin, p.name, p.target_markets, p.ndc is not null and p.ndc <> '',
			p.dosage_form <> '' and p.strength <> '' and p.pack_size <> '', p.vertical from product p`)
		if err != nil {
			return err
		}
		type prod struct {
			id                uuid.UUID
			gtin, name, vert  string
			markets           []string
			hasNDC, hasDetail bool
		}
		var prods []prod
		for rows.Next() {
			var p prod
			if err := rows.Scan(&p.id, &p.gtin, &p.name, &p.markets, &p.hasNDC, &p.hasDetail, &p.vert); err != nil {
				rows.Close()
				return err
			}
			prods = append(prods, p)
		}
		rows.Close()
		applies := func(pk rulepacks.Pack, markets []string) bool {
			for _, mk := range markets {
				if mk == pk.Market || (pk.Market == "EU" && euMember[mk]) {
					return true
				}
			}
			return false
		}
		for _, pk := range m.packs {
			for _, p := range prods {
				if !applies(pk, p.markets) || (pk.Vertical != "" && pk.Vertical != p.vert) {
					continue
				}
				facts := map[string]any{"has_ndc": p.hasNDC, "has_name_form_strength_packsize": p.hasDetail}
				for _, rule := range pk.Rules {
					if rule.AppliesTo != "product" {
						continue
					}
					if ok, applied := rulepacks.Eval(rule.Check, facts); applied && !ok {
						findings = append(findings, Finding{pk.ID, rule.ID, rule.Severity, "product", p.id.String(), p.name + " (" + p.gtin + "): " + rule.Description, rule.Source})
					}
				}
			}
			pools, err := tx.Query(r.Context(), `select id, gtin, market, mode, length + length(prefix), capacity, greatest(next_counter,1) from serial_pool`)
			if err != nil {
				return err
			}
			for pools.Next() {
				var id uuid.UUID
				var gtin, market, mode string
				var total int
				var capacity, issued int64
				if err := pools.Scan(&id, &gtin, &market, &mode, &total, &capacity, &issued); err != nil {
					pools.Close()
					return err
				}
				if !applies(pk, []string{market}) {
					continue
				}
				facts := map[string]any{"mode": mode, "total_length": total, "capacity_per_issued": float64(capacity) / float64(issued)}
				for _, rule := range pk.Rules {
					if rule.AppliesTo != "serial_pool" {
						continue
					}
					if ok, applied := rulepacks.Eval(rule.Check, facts); applied && !ok {
						findings = append(findings, Finding{pk.ID, rule.ID, rule.Severity, "serial_pool", id.String(), "Pool " + gtin + "/" + market + ": " + rule.Description, rule.Source})
					}
				}
			}
			pools.Close()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if findings == nil {
		findings = []Finding{}
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Finding]{Items: findings, Total: len(findings)})
	return nil
}

var _ = trace.Lookup

// RegisterDemoHubProduct registers a GTIN in the in-process EU mock hub (demo seed).
func (m *Module) RegisterDemoHubProduct(gtin string) { m.Mocks.RegisterEUProduct(gtin) }
