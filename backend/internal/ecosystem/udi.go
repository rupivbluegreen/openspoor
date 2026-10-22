package ecosystem

import (
	"encoding/json"
	"encoding/xml"
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
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Device is a medical device UDI record (device identifier = GTIN when GS1 is the issuing agency).
type Device struct {
	ID          uuid.UUID      `json:"id" db:"id"`
	DI          string         `json:"di" db:"di"`
	BrandName   string         `json:"brandName" db:"brand_name"`
	Model       string         `json:"model" db:"model"`
	Version     string         `json:"version" db:"version"`
	Description string         `json:"description" db:"description"`
	RiskClass   string         `json:"riskClass" db:"risk_class"`
	GMDNCode    string         `json:"gmdnCode" db:"gmdn_code"`
	EMDNCode    string         `json:"emdnCode" db:"emdn_code"`
	BasicUDIDI  string         `json:"basicUdiDi" db:"basic_udi_di"`
	Sterile     bool           `json:"sterile" db:"sterile"`
	SingleUse   bool           `json:"singleUse" db:"single_use"`
	Rx          bool           `json:"rx" db:"rx"`
	Labeler     string         `json:"labeler" db:"labeler"`
	Submissions map[string]any `json:"submissions" db:"submissions"`
	CreatedAt   time.Time      `json:"createdAt" db:"created_at"`
}

const deviceCols = `id, di, brand_name, model, version, description, risk_class, gmdn_code, emdn_code, basic_udi_di, sterile, single_use, rx, labeler, submissions, created_at`

var riskClasses = map[string]bool{"US-I": true, "US-II": true, "US-III": true, "EU-I": true, "EU-IIa": true, "EU-IIb": true, "EU-III": true}

func (m *Module) listDevices(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Device](r.Context(), tx, `select `+deviceCols+` from udi_device order by brand_name`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Device]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createDevice(w http.ResponseWriter, r *http.Request) error {
	var in Device
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.DI = gs1.NormalizeGTIN(in.DI)
	if !gs1.ValidGTIN(in.DI) {
		return httpx.BadRequest("di must be a valid GTIN (GS1 issuing agency)")
	}
	if in.BrandName == "" || in.Labeler == "" || !riskClasses[in.RiskClass] {
		return httpx.BadRequest("brandName, labeler and riskClass (US-I/II/III or EU-I/IIa/IIb/III) are required")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := db.One[Device](r.Context(), tx, `insert into udi_device (id, tenant_id, di, brand_name, model, version, description, risk_class, gmdn_code, emdn_code, basic_udi_di, sterile, single_use, rx, labeler)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) returning `+deviceCols,
			ids.New(), in.DI, in.BrandName, in.Model, in.Version, in.Description, in.RiskClass, in.GMDNCode, in.EMDNCode, in.BasicUDIDI, in.Sterile, in.SingleUse, in.Rx, in.Labeler)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "udi.device.create", Entity: "udi_device", EntityID: d.ID.String(), After: d}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, d)
		return nil
	})
}

// gudidRecord is a simplified GUDID device identifier record.
// TODO(spec): GUDID accepts HL7 SPL XML through the FDA Electronic Submissions Gateway; map these
// fields to the SPL structure from the current GUDID Data Elements Reference Table before use.
type gudidRecord struct {
	XMLName     xml.Name `xml:"gudidDeviceRecord"`
	PrimaryDI   string   `xml:"primaryDI"`
	IssuingAgcy string   `xml:"issuingAgency"`
	BrandName   string   `xml:"brandName"`
	Model       string   `xml:"versionModelNumber"`
	Labeler     string   `xml:"companyName"`
	Description string   `xml:"deviceDescription"`
	GMDN        string   `xml:"gmdnPTCode"`
	Sterile     bool     `xml:"deviceSterile"`
	SingleUse   bool     `xml:"singleUse"`
	Rx          bool     `xml:"rx"`
}

// eudamedRecord is a simplified EUDAMED UDI/device registration.
// TODO(spec): EUDAMED machine-to-machine uses the Commission's XSD-defined XML; map to the current
// "UDI/Device registration" data dictionary before use.
type eudamedRecord struct {
	XMLName    xml.Name `xml:"eudamedDeviceRegistration"`
	BasicUDIDI string   `xml:"basicUdiDi"`
	UDIDI      string   `xml:"udiDi"`
	Agency     string   `xml:"issuingEntity"`
	RiskClass  string   `xml:"riskClass"`
	TradeName  string   `xml:"tradeName"`
	Model      string   `xml:"modelName"`
	EMDN       string   `xml:"emdnCode"`
	Sterile    bool     `xml:"sterile"`
	SingleUse  bool     `xml:"singleUse"`
	Labeler    string   `xml:"manufacturer"`
}

// submitDevice validates against the target's required fields, builds the payload and records
// the submission. Transmission is stubbed until gateway credentials are configured.
func (m *Module) submitDevice(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	target := strings.ToLower(r.URL.Query().Get("target"))
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := db.One[Device](r.Context(), tx, `select `+deviceCols+` from udi_device where id=$1`, id)
		if err != nil {
			return err
		}
		var payload []byte
		var missing []string
		switch target {
		case "gudid":
			if !strings.HasPrefix(d.RiskClass, "US-") {
				missing = append(missing, "riskClass must be a US class")
			}
			if d.GMDNCode == "" {
				missing = append(missing, "gmdnCode")
			}
			if d.Model == "" {
				missing = append(missing, "model (version/model number)")
			}
			payload, _ = xml.MarshalIndent(gudidRecord{PrimaryDI: d.DI, IssuingAgcy: "GS1", BrandName: d.BrandName, Model: d.Model, Labeler: d.Labeler, Description: d.Description,
				GMDN: d.GMDNCode, Sterile: d.Sterile, SingleUse: d.SingleUse, Rx: d.Rx}, "", "  ")
		case "eudamed":
			if !strings.HasPrefix(d.RiskClass, "EU-") {
				missing = append(missing, "riskClass must be an EU class")
			}
			if d.BasicUDIDI == "" {
				missing = append(missing, "basicUdiDi")
			}
			if d.EMDNCode == "" {
				missing = append(missing, "emdnCode")
			}
			payload, _ = xml.MarshalIndent(eudamedRecord{BasicUDIDI: d.BasicUDIDI, UDIDI: d.DI, Agency: "GS1", RiskClass: d.RiskClass, TradeName: d.BrandName, Model: d.Model,
				EMDN: d.EMDNCode, Sterile: d.Sterile, SingleUse: d.SingleUse, Labeler: d.Labeler}, "", "  ")
		default:
			return httpx.BadRequest("target must be gudid or eudamed")
		}
		if len(missing) > 0 {
			return httpx.Unprocessable("device record is incomplete for "+target, missing)
		}
		sub := map[string]any{"status": "prepared_stub", "at": time.Now().UTC(), "ref": fmt.Sprintf("LOCAL-%s-%s", strings.ToUpper(target), ids.Secret(4)),
			"note": "TODO(spec): transmission requires gateway onboarding (FDA ESG / EUDAMED M2M); payload prepared locally"}
		patch, _ := json.Marshal(map[string]any{target: sub})
		if _, err := tx.Exec(r.Context(), `update udi_device set submissions = submissions || $2::jsonb where id=$1`, id, patch); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "udi.submit." + target, Entity: "udi_device", EntityID: id.String(), After: sub}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"submission": sub, "payload": string(payload)})
		return nil
	})
}

// --- benchmarks ---

type run struct {
	ID          uuid.UUID      `json:"id" db:"id"`
	MeasuredAt  time.Time      `json:"measuredAt" db:"measured_at"`
	Environment string         `json:"environment" db:"environment"`
	Version     string         `json:"version" db:"version"`
	Results     map[string]any `json:"results" db:"results"`
}

func (m *Module) recordRun(w http.ResponseWriter, r *http.Request) error {
	var in run
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.MeasuredAt.IsZero() || in.Environment == "" || len(in.Results) == 0 {
		return httpx.BadRequest("measuredAt, environment and results are required")
	}
	in.ID = ids.New()
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `insert into benchmark_run (id, measured_at, environment, version, results) values ($1,$2,$3,$4,$5)`, in.ID, in.MeasuredAt, in.Environment, in.Version, in.Results)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, in)
	return nil
}

// systemBenchmarks is public: the latest measured performance of this installation.
func (m *Module) systemBenchmarks(w http.ResponseWriter, r *http.Request) error {
	var runs []run
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		runs, err = db.List[run](r.Context(), tx, `select id, measured_at, environment, version, results from benchmark_run order by measured_at desc limit 20`)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[run]{Items: runs, Total: len(runs)})
	return nil
}

// minPeers is the k-anonymity threshold for peer comparisons.
const minPeers = 5

// benchmarks compares the tenant's operational KPIs with anonymized peers on this installation.
func (m *Module) benchmarks(w http.ResponseWriter, r *http.Request) error {
	type kpi struct {
		Key         string   `json:"key"`
		Name        string   `json:"name"`
		Unit        string   `json:"unit"`
		Yours       *float64 `json:"yours"`
		PeerMedian  *float64 `json:"peerMedian"`
		PeerP90     *float64 `json:"peerP90"`
		Peers       int      `json:"peers"`
		LowerBetter bool     `json:"lowerIsBetter"`
	}
	defs := []struct {
		key, name, unit, sql string
		lower                bool
	}{
		{"capture_rejection_pct", "EPCIS documents rejected (30 d)", "%",
			`select tenant_id, 100.0 * count(*) filter (where status='rejected') / nullif(count(*),0) from epcis_capture where created_at > now() - interval '30 days' group by tenant_id`, true},
		{"report_acceptance_pct", "Regulatory reports accepted first time (30 d)", "%",
			`select tenant_id, 100.0 * count(*) filter (where status='accepted' and attempts <= 1) / nullif(count(*),0) from regulatory_report where created_at > now() - interval '30 days' group by tenant_id`, false},
		{"case_resolution_hours", "Median hours to resolve a case (30 d)", "h",
			`select tenant_id, percentile_cont(0.5) within group (order by extract(epoch from updated_at - created_at)/3600) from work_case where status='resolved' and updated_at > now() - interval '30 days' group by tenant_id`, true},
	}
	me := kernelTenant(r)
	out := []kpi{}
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		for _, d := range defs {
			rows, err := tx.Query(r.Context(), d.sql)
			if err != nil {
				return err
			}
			var vals []float64
			k := kpi{Key: d.key, Name: d.name, Unit: d.unit, LowerBetter: d.lower}
			for rows.Next() {
				var t uuid.UUID
				var v *float64
				if err := rows.Scan(&t, &v); err != nil {
					rows.Close()
					return err
				}
				if v == nil {
					continue
				}
				if t == me {
					x := *v
					k.Yours = &x
				} else {
					vals = append(vals, *v)
				}
			}
			rows.Close()
			k.Peers = len(vals)
			if len(vals) >= minPeers {
				med, p90 := percentile(vals, 0.5), percentile(vals, 0.9)
				k.PeerMedian, k.PeerP90 = &med, &p90
			}
			out = append(out, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"kpis": out, "minPeers": minPeers, "note": "Peer figures appear only when at least 5 other tenants have data (k-anonymity)."})
	return nil
}

func percentile(xs []float64, q float64) float64 {
	s := append([]float64(nil), xs...)
	sortFloats(s)
	i := q * float64(len(s)-1)
	lo := int(i)
	if lo+1 >= len(s) {
		return s[lo]
	}
	return s[lo] + (s[lo+1]-s[lo])*(i-float64(lo))
}

func sortFloats(s []float64) { sort.Float64s(s) }

func kernelTenant(r *http.Request) uuid.UUID { return kernel.TenantOf(r.Context()) }
