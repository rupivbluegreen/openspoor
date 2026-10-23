// Package ecosystem holds the extension surfaces: the app/agent marketplace with developer
// sandboxes, cold-chain sensor ingest with excursion detection (EPCIS sensor events), medical
// device UDI records with GUDID/EUDAMED submission stubs, the learning hub and the benchmark
// dashboards (measured system performance and anonymized peer comparisons).
package ecosystem

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
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
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/platform"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

//go:embed learn/*.md
var learnFS embed.FS

// Module is the ecosystem bounded context.
type Module struct {
	kernel.Deps
	Platform *platform.Module
	EPCIS    *epcis.Module
}

// New creates the module.
func New(d kernel.Deps, p *platform.Module, ep *epcis.Module) *Module {
	return &Module{Deps: d, Platform: p, EPCIS: ep}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "ecosystem" }

// Public implements kernel.PublicRoutes.
func (m *Module) Public(r chi.Router) {
	r.Get("/learn", httpx.Handler(m.listLearn))
	r.Get("/learn/{slug}", httpx.Handler(m.getLearn))
	r.Get("/benchmarks/system", httpx.Handler(m.systemBenchmarks))
	r.Get("/marketplace", httpx.Handler(m.listMarketplace))
}

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Get("/apps", httpx.Handler(m.listApps))
	r.With(auth.Require(auth.RoleAdmin)).Post("/apps", httpx.Handler(m.publishApp))
	r.With(auth.Require(auth.RoleAdmin)).Post("/apps/{id}/install", httpx.Handler(m.install))
	r.With(auth.Require(auth.RoleAdmin)).Delete("/apps/{id}/install", httpx.Handler(m.uninstall))
	r.Get("/apps/installed", httpx.Handler(m.installed))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleIntegration)).Post("/apps/sandbox", httpx.Handler(m.sandbox))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleIntegration)).Post("/sensors/readings", httpx.Handler(m.ingest))
	r.Get("/sensors/excursions", httpx.Handler(m.listExcursions))
	r.Get("/udi/devices", httpx.Handler(m.listDevices))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance)).Post("/udi/devices", httpx.Handler(m.createDevice))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance)).Post("/udi/devices/{id}/submit", httpx.Handler(m.submitDevice))
	r.Get("/kpis", httpx.Handler(m.listKPIs))
	r.Get("/kpis/{id}/series", httpx.Handler(m.kpiSeries))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance)).Put("/kpis/{id}/goal", httpx.Handler(m.putGoal))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance)).Delete("/kpis/{id}/goal", httpx.Handler(m.deleteGoal))
	r.Get("/benchmarks", httpx.Handler(m.benchmarks))
	r.With(auth.RequireOperator).Post("/benchmarks/runs", httpx.Handler(m.recordRun))
}

// --- marketplace ---

// Manifest describes an app or agent package.
type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Publisher   string   `json:"publisher"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Roles       []string `json:"roles"`             // roles the app's API key receives
	Webhook     string   `json:"webhook,omitempty"` // receives EPCIS subscriptions when set
	Homepage    string   `json:"homepage,omitempty"`
}

// Validate checks a manifest.
func (mf Manifest) Validate() error {
	if mf.ID == "" || strings.ContainsAny(mf.ID, " /") || mf.Name == "" || mf.Publisher == "" || mf.Version == "" {
		return httpx.BadRequest("manifest needs id (no spaces), name, publisher and version")
	}
	switch mf.Kind {
	case "connector", "agent", "rulepack", "integration", "app":
	default:
		return httpx.BadRequest("kind must be connector, agent, rulepack, integration or app")
	}
	for _, r := range mf.Roles {
		if r == auth.RoleAdmin {
			return httpx.BadRequest("apps cannot request the Admin role")
		}
		if !contains(auth.AllRoles, r) {
			return httpx.BadRequest("unknown role " + r)
		}
	}
	if mf.Webhook != "" && !strings.HasPrefix(mf.Webhook, "https://") {
		return httpx.BadRequest("webhook must be https")
	}
	// The homepage is rendered as a link in every tenant's catalogue.
	if mf.Homepage != "" && !strings.HasPrefix(mf.Homepage, "https://") {
		return httpx.BadRequest("homepage must be https")
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// builtinListings are the packages that ship with Open Spoor (verified).
var builtinListings = []Manifest{
	{ID: "eu-fmd-emvs", Name: "EU FMD — EMVS connector", Kind: "connector", Publisher: "Open Spoor", Version: "1.0.0", Description: "Pack data upload and status updates to the EU Hub.", Roles: []string{auth.RoleIntegration}},
	{ID: "sa-rsd", Name: "Saudi RSD connector", Kind: "connector", Publisher: "Open Spoor", Version: "1.0.0", Description: "Production, dispatch, accept and deactivation notifications to SFDA RSD.", Roles: []string{auth.RoleIntegration}},
	{ID: "exception-triage", Name: "Exception triage agent", Kind: "agent", Publisher: "Open Spoor", Version: "1.0.0", Description: "Turns alerts into prioritized cases.", Roles: []string{auth.RoleViewer}},
	{ID: "expiry-quarantine", Name: "Expiry quarantine agent", Kind: "agent", Publisher: "Open Spoor", Version: "1.0.0", Description: "Proposes marking expired saleable packs.", Roles: []string{auth.RoleViewer}},
	{ID: "rulepack-eu-fmd", Name: "EU FMD rule pack", Kind: "rulepack", Publisher: "Open Spoor community", Version: "2026.1", Description: "Delegated Regulation 2016/161 checks.", Roles: []string{auth.RoleViewer}},
	{ID: "rulepack-us-fsma204", Name: "FSMA 204 rule pack", Kind: "rulepack", Publisher: "Open Spoor community", Version: "2026.1", Description: "21 CFR Part 1 Subpart S checks.", Roles: []string{auth.RoleViewer}},
}

type listing struct {
	Manifest
	Verified  bool      `json:"verified"`
	Installed bool      `json:"installed"`
	CreatedAt time.Time `json:"createdAt"`
}

func (m *Module) ensureBuiltins(ctx context.Context) error {
	return m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		for _, l := range builtinListings {
			if _, err := tx.Exec(ctx, `insert into app_listing (id, name, kind, publisher, version, description, manifest, verified) values ($1,$2,$3,$4,$5,$6,$7,true)
				on conflict (id) do update set version=excluded.version, description=excluded.description, manifest=excluded.manifest`,
				l.ID, l.Name, l.Kind, l.Publisher, l.Version, l.Description, l); err != nil {
				return err
			}
		}
		return nil
	})
}

// listMarketplace is the public catalogue. It stays readable without credentials; when the
// request carries a valid token or API key it also reports which listings that tenant has
// installed. Invalid credentials are treated as anonymous (the catalogue is public anyway).
func (m *Module) listMarketplace(w http.ResponseWriter, r *http.Request) error {
	if _, ok := auth.From(r.Context()); !ok && m.Auth != nil {
		if p, err := m.Auth.Authenticate(r); err == nil {
			r = r.WithContext(auth.With(r.Context(), p))
		}
	}
	return m.listApps(w, r)
}

func (m *Module) listApps(w http.ResponseWriter, r *http.Request) error {
	if err := m.ensureBuiltins(r.Context()); err != nil {
		return err
	}
	installed := map[string]bool{}
	if p, ok := auth.From(r.Context()); ok {
		_ = m.DB.Tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(r.Context(), `select app_id from app_install`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					installed[id] = true
				}
			}
			return nil
		})
	}
	var out []listing
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select manifest, verified, created_at from app_listing where ($1='' or kind=$1) order by verified desc, name`, r.URL.Query().Get("kind"))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l listing
			if err := rows.Scan(&l.Manifest, &l.Verified, &l.CreatedAt); err != nil {
				return err
			}
			l.Installed = installed[l.ID]
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[listing]{Items: out, Total: len(out)})
	return nil
}

// publishApp lists a community app (unverified until an operator verifies it).
func (m *Module) publishApp(w http.ResponseWriter, r *http.Request) error {
	var mf Manifest
	if err := httpx.Decode(r, &mf); err != nil {
		return err
	}
	if err := mf.Validate(); err != nil {
		return err
	}
	out := listing{Manifest: mf}
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var verified bool
		err := tx.QueryRow(r.Context(), `select verified from app_listing where id=$1`, mf.ID).Scan(&verified)
		if err == nil && verified {
			return httpx.Conflict("a verified package with this id exists")
		}
		err = tx.QueryRow(r.Context(), `insert into app_listing (id, name, kind, publisher, version, description, manifest) values ($1,$2,$3,$4,$5,$6,$7)
			on conflict (id) do update set name=excluded.name, version=excluded.version, description=excluded.description, manifest=excluded.manifest
			returning verified, created_at`,
			mf.ID, mf.Name, mf.Kind, mf.Publisher, mf.Version, mf.Description, mf).Scan(&out.Verified, &out.CreatedAt)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

// install grants the app an API key with exactly the roles its manifest requests
// and, if it declares a webhook, an EPCIS subscription. Both are audited.
func (m *Module) install(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := m.ensureBuiltins(r.Context()); err != nil {
		return err
	}
	var mf Manifest
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `select manifest from app_listing where id=$1`, id).Scan(&mf)
	})
	if err != nil {
		return httpx.NotFound("unknown app")
	}
	p := auth.MustFrom(r.Context())
	roles := mf.Roles
	if roles == nil { // an app that declares no roles gets a key that can do nothing; the column is not null
		roles = []string{}
	}
	secret := "osk_" + ids.Secret(32)
	keyID := ids.New()
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `insert into api_key (id, tenant_id, name, prefix, hash, roles, created_by) values ($1, app_tenant(), $2, $3, $4, $5, $6)`,
			keyID, "app:"+mf.ID, secret[:10], auth.HashKey(secret), roles, p.Actor()); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into app_install (tenant_id, app_id, api_key_id, installed_by) values (app_tenant(), $1, $2, $3)`, mf.ID, keyID, p.Actor()); err != nil {
			return db.MapErr(err)
		}
		if mf.Webhook != "" {
			if _, err := tx.Exec(r.Context(), `insert into epcis_subscription (id, tenant_id, query, dest, secret) values ($1, app_tenant(), '{}', $2, $3)`, ids.New(), mf.Webhook, ids.Secret(24)); err != nil {
				return err
			}
		}
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "app.install", Entity: "app", EntityID: mf.ID, After: map[string]any{"roles": mf.Roles, "webhook": mf.Webhook}})
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"app": mf, "apiKey": secret, "note": "The key is shown once; give it to the app."})
	return nil
}

func (m *Module) uninstall(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var keyID *uuid.UUID
		if err := tx.QueryRow(r.Context(), `delete from app_install where app_id=$1 returning api_key_id`, id).Scan(&keyID); err != nil {
			return db.MapErr(err)
		}
		if keyID != nil {
			if _, err := tx.Exec(r.Context(), `update api_key set revoked_at=now() where id=$1`, *keyID); err != nil {
				return err
			}
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "app.uninstall", Entity: "app", EntityID: id}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

func (m *Module) installed(w http.ResponseWriter, r *http.Request) error {
	type inst struct {
		AppID       string    `json:"appId" db:"app_id"`
		InstalledBy string    `json:"installedBy" db:"installed_by"`
		CreatedAt   time.Time `json:"createdAt" db:"created_at"`
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[inst](r.Context(), tx, `select app_id, installed_by, created_at from app_install order by created_at`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[inst]{Items: items, Total: len(items)})
		}
		return err
	})
}

// sandbox provisions an isolated developer tenant with sample data and an Integration API key.
func (m *Module) sandbox(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFrom(r.Context())
	slug := "sandbox-" + strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(ids.Secret(6)))
	password := ids.Secret(18)
	t, _, err := m.Platform.CreateTenant(r.Context(), platform.NewTenant{Slug: slug, Name: "Sandbox (" + p.Email + ")", Plan: "trial", AdminEmail: firstNonEmpty(p.Email, "dev@sandbox.local"), AdminPassword: password, Verticals: []string{"pharma", "food"}})
	if err != nil {
		return err
	}
	sys := auth.With(r.Context(), auth.System(t.ID))
	secret := "osk_" + ids.Secret(32)
	gtin := "09506000134352"
	err = m.DB.Tx(sys, t.ID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(sys, `insert into api_key (id, tenant_id, name, prefix, hash, roles, created_by) values ($1, app_tenant(), 'sandbox', $2, $3, $4, 'system')`,
			ids.New(), secret[:10], auth.HashKey(secret), []string{auth.RoleIntegration, auth.RoleSerialization, auth.RoleOps}); err != nil {
			return err
		}
		_, err := tx.Exec(sys, `insert into product (id, tenant_id, gtin, name, target_markets) values ($1, app_tenant(), $2, 'Sandbox Paracetamol 500mg', '{EU,US}')`, ids.New(), gtin)
		return err
	})
	if err != nil {
		return err
	}
	ev := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", Disposition: "in_transit", EventTime: time.Now().UTC().Add(-time.Hour), EventTimeZoneOffset: "+00:00",
		EPCList: []string{"urn:epc:id:sscc:9506000.0000000017"}}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{ev}))
	_, _ = m.EPCIS.Capture(sys, "application/ld+json", b, "sandbox-seed")
	_ = m.Tx(r.Context(), func(tx pgx.Tx) error {
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "sandbox.create", Entity: "tenant", EntityID: t.ID.String(), After: map[string]string{"slug": slug}})
		return err
	})
	httpx.JSON(w, http.StatusCreated, map[string]any{"tenant": slug, "apiKey": secret, "adminPassword": password, "sampleGtin": gtin,
		"note": "Sandbox data is isolated by row-level security like any tenant. Credentials are shown once."})
	return nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// --- cold chain ---

type reading struct {
	Time     time.Time `json:"time"`
	TempC    *float64  `json:"tempC"`
	Humidity *float64  `json:"humidity"`
}

// Excursion is a period outside the allowed temperature range.
type Excursion struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	DeviceID   string     `json:"deviceId" db:"device_id"`
	EPC        *string    `json:"epc,omitempty" db:"epc"`
	ShipmentID *uuid.UUID `json:"shipmentId,omitempty" db:"shipment_id"`
	MinC       float64    `json:"minC" db:"min_c"`
	MaxC       float64    `json:"maxC" db:"max_c"`
	StartedAt  time.Time  `json:"startedAt" db:"started_at"`
	EndedAt    time.Time  `json:"endedAt" db:"ended_at"`
	PeakC      float64    `json:"peakC" db:"peak_c"`
	Minutes    int        `json:"minutes" db:"minutes"`
	Status     string     `json:"status" db:"status"`
}

// DetectExcursions groups consecutive out-of-range readings (sorted by time).
func DetectExcursions(rs []reading, minC, maxC float64) []Excursion {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Time.Before(rs[j].Time) })
	var out []Excursion
	var cur *Excursion
	for _, r := range rs {
		if r.TempC == nil {
			continue
		}
		t := *r.TempC
		if t < minC || t > maxC {
			if cur == nil {
				cur = &Excursion{MinC: minC, MaxC: maxC, StartedAt: r.Time, PeakC: t}
			}
			cur.EndedAt = r.Time
			if math.Abs(t-(minC+maxC)/2) > math.Abs(cur.PeakC-(minC+maxC)/2) {
				cur.PeakC = t
			}
			continue
		}
		if cur != nil {
			cur.EndedAt = r.Time
			cur.Minutes = int(cur.EndedAt.Sub(cur.StartedAt).Minutes())
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		cur.Minutes = int(cur.EndedAt.Sub(cur.StartedAt).Minutes())
		out = append(out, *cur)
	}
	return out
}

func (m *Module) ingest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		DeviceID   string     `json:"deviceId"`
		EPC        string     `json:"epc"`
		ShipmentID *uuid.UUID `json:"shipmentId"`
		MinC       *float64   `json:"minC"`
		MaxC       *float64   `json:"maxC"`
		Readings   []reading  `json:"readings"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.DeviceID == "" || len(in.Readings) == 0 || len(in.Readings) > 100_000 {
		return httpx.BadRequest("deviceId and 1-100000 readings are required")
	}
	minC, maxC := 2.0, 8.0 // pharmaceutical cold chain default; override per call
	if in.MinC != nil {
		minC = *in.MinC
	}
	if in.MaxC != nil {
		maxC = *in.MaxC
	}
	if minC >= maxC {
		return httpx.BadRequest("minC must be below maxC")
	}
	epc := lib.Canonical(in.EPC)
	var excursions []Excursion
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		n := len(in.Readings)
		times, temps, hums := make([]time.Time, n), make([]*float64, n), make([]*float64, n)
		for i, rd := range in.Readings {
			times[i], temps[i], hums[i] = rd.Time, rd.TempC, rd.Humidity
		}
		if _, err := tx.Exec(r.Context(), `insert into sensor_reading (tenant_id, device_id, epc, shipment_id, at, temp_c, humidity)
			select app_tenant(), $1, nullif($2,''), $3, t, c, h from unnest($4::timestamptz[], $5::numeric[], $6::numeric[]) as x(t, c, h)
			on conflict do nothing`, in.DeviceID, epc, in.ShipmentID, times, temps, hums); err != nil {
			return err
		}
		excursions = DetectExcursions(in.Readings, minC, maxC)
		for i := range excursions {
			x := &excursions[i]
			x.ID, x.DeviceID, x.ShipmentID, x.Status = ids.New(), in.DeviceID, in.ShipmentID, "open"
			if epc != "" {
				x.EPC = &epc
			}
			if _, err := tx.Exec(r.Context(), `insert into sensor_excursion (id, tenant_id, device_id, epc, shipment_id, min_c, max_c, started_at, ended_at, peak_c, minutes)
				values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10)`, x.ID, x.DeviceID, x.EPC, x.ShipmentID, x.MinC, x.MaxC, x.StartedAt, x.EndedAt, x.PeakC, x.Minutes); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id) values ($1, app_tenant(), 'coldchain.excursion', 'error', $2, $3, 'sensor_excursion', $4)`,
				ids.New(), fmt.Sprintf("Temperature excursion on %s: peak %.1f °C for %d min (allowed %.1f–%.1f °C)", in.DeviceID, x.PeakC, x.Minutes, minC, maxC), x, x.ID.String()); err != nil {
				return err
			}
		}
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "sensor.ingest", Entity: "device", EntityID: in.DeviceID, After: map[string]any{"readings": len(in.Readings), "excursions": len(excursions)}})
		return err
	})
	if err != nil {
		return err
	}
	// Record the readings as an EPCIS 2.0 sensor event on the monitored object.
	if epc != "" {
		var temps []float64
		for _, rd := range in.Readings {
			if rd.TempC != nil {
				temps = append(temps, *rd.TempC)
			}
		}
		if len(temps) > 0 {
			lo, hi, sum := temps[0], temps[0], 0.0
			for _, t := range temps {
				lo, hi, sum = math.Min(lo, t), math.Max(hi, t), sum+t
			}
			sensor, _ := json.Marshal([]map[string]any{{"sensorMetadata": map[string]any{"deviceID": "urn:openspoor:device:" + in.DeviceID},
				"sensorReport": []map[string]any{{"type": "gs1:Temperature", "minValue": lo, "maxValue": hi, "meanValue": sum / float64(len(temps)), "uom": "CEL"}}}})
			ev := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "sensor_reporting", EventTime: in.Readings[len(in.Readings)-1].Time, EventTimeZoneOffset: "+00:00",
				EPCList: []string{in.EPC}, SensorElementList: sensor}
			b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{ev}))
			_, _ = m.EPCIS.Capture(r.Context(), "application/ld+json", b, "sensor:"+in.DeviceID)
		}
	}
	if excursions == nil {
		excursions = []Excursion{}
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"stored": len(in.Readings), "excursions": excursions})
	return nil
}

func (m *Module) listExcursions(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Excursion](r.Context(), tx, `select id, device_id, epc, shipment_id, min_c, max_c, started_at, ended_at, peak_c, minutes, status from sensor_excursion order by started_at desc limit 500`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Excursion]{Items: items, Total: len(items)})
		}
		return err
	})
}

// --- learning hub ---

type article struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	Body  string   `json:"body,omitempty"`
}

func parseArticle(name string, b []byte) article {
	a := article{Slug: strings.TrimSuffix(name, ".md")}
	s := string(b)
	if strings.HasPrefix(s, "---\n") {
		if end := strings.Index(s[4:], "\n---\n"); end >= 0 {
			for _, line := range strings.Split(s[4:4+end], "\n") {
				k, v, _ := strings.Cut(line, ":")
				v = strings.TrimSpace(v)
				switch strings.TrimSpace(k) {
				case "title":
					a.Title = v
				case "tags":
					for _, t := range strings.Split(strings.Trim(v, "[]"), ",") {
						a.Tags = append(a.Tags, strings.TrimSpace(t))
					}
				}
			}
			s = s[4+end+5:]
		}
	}
	a.Body = strings.TrimSpace(s)
	return a
}

func (m *Module) listLearn(w http.ResponseWriter, r *http.Request) error {
	entries, err := fs.ReadDir(learnFS, "learn")
	if err != nil {
		return err
	}
	var out []article
	for _, e := range entries {
		b, _ := learnFS.ReadFile("learn/" + e.Name())
		a := parseArticle(e.Name(), b)
		a.Body = ""
		if tag := r.URL.Query().Get("tag"); tag == "" || contains(a.Tags, tag) {
			out = append(out, a)
		}
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[article]{Items: out, Total: len(out)})
	return nil
}

func (m *Module) getLearn(w http.ResponseWriter, r *http.Request) error {
	slug := chi.URLParam(r, "slug")
	if strings.ContainsAny(slug, "/.") {
		return httpx.NotFound("no such article")
	}
	b, err := learnFS.ReadFile("learn/" + slug + ".md")
	if err != nil {
		return httpx.NotFound("no such article")
	}
	httpx.JSON(w, http.StatusOK, parseArticle(slug+".md", b))
	return nil
}

var _ = gs1.ValidGTIN
