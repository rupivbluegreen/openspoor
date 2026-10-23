package food

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
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

// Cold chain: mean kinetic temperature, the disposition workflow for temperature excursions and
// the excursion report. Readings and excursions are written by the ecosystem module
// (sensor_reading, sensor_excursion); this package reads those tables and keeps its own
// decisions in food_excursion_disposition.

// Constants of the Haynes mean kinetic temperature formula.
const (
	// GasConstantKJ is the gas constant R in kJ/(mol·K).
	GasConstantKJ = 0.0083144598
	// DefaultActivationEnergyKJ is the activation energy (delta H) in kJ/mol used when the caller
	// gives none. 83.144 kJ/mol is the value usually quoted for the general case in the
	// pharmaceutical storage literature (it makes delta H / R about 10,000 K). It is a
	// convention, not a property of any product.
	// TODO(spec): product-specific activation energies must come from the product's stability
	// data or the applicable pharmacopoeia / regulator guidance; none is asserted here.
	DefaultActivationEnergyKJ = 83.144
	kelvin                    = 273.15
)

// MeanKineticTemperature returns the mean kinetic temperature in degrees Celsius of equally
// spaced readings (Haynes formula):
//
//	MKT = (dH/R) / -ln( (sum(exp(-dH/(R*Ti))) / n) ),  Ti in kelvin
//
// activationKJ is dH in kJ/mol. Irregular sampling is not weighted by interval; the report says
// whether the readings were evenly spaced.
func MeanKineticTemperature(tempsC []float64, activationKJ float64) (float64, error) {
	if len(tempsC) == 0 {
		return 0, errors.New("at least one temperature reading is required")
	}
	if activationKJ <= 0 || activationKJ > 1000 || math.IsNaN(activationKJ) {
		return 0, errors.New("activation energy must be above 0 and at most 1000 kJ/mol")
	}
	k := activationKJ / GasConstantKJ // kelvin
	var sum float64
	for _, c := range tempsC {
		t := c + kelvin
		if t <= 0 || math.IsNaN(c) || math.IsInf(c, 0) {
			return 0, fmt.Errorf("temperature %v °C is not physically valid", c)
		}
		sum += math.Exp(-k / t)
	}
	mean := sum / float64(len(tempsC))
	if mean <= 0 {
		return 0, errors.New("readings are too far out of range to evaluate")
	}
	return k/-math.Log(mean) - kelvin, nil
}

// TempStats summarises readings of one device over a window.
type TempStats struct {
	Count        int     `json:"count"`
	MinC         float64 `json:"minC"`
	MaxC         float64 `json:"maxC"`
	MeanC        float64 `json:"meanC"`
	MKTC         float64 `json:"mktC"`
	ActivationKJ float64 `json:"activationEnergyKJ"`
	EvenSampling bool    `json:"evenlySampled"`
}

// Reading is one temperature reading.
type Reading struct {
	At    time.Time `json:"at" db:"at"`
	TempC float64   `json:"tempC" db:"temp_c"`
}

// Stats computes the summary including the mean kinetic temperature.
func Stats(rs []Reading, activationKJ float64) (TempStats, error) {
	if activationKJ == 0 {
		activationKJ = DefaultActivationEnergyKJ
	}
	temps := make([]float64, len(rs))
	var sum float64
	st := TempStats{Count: len(rs), ActivationKJ: activationKJ, EvenSampling: true}
	for i, r := range rs {
		temps[i] = r.TempC
		sum += r.TempC
		if i == 0 || r.TempC < st.MinC {
			st.MinC = r.TempC
		}
		if i == 0 || r.TempC > st.MaxC {
			st.MaxC = r.TempC
		}
	}
	mkt, err := MeanKineticTemperature(temps, activationKJ)
	if err != nil {
		return st, err
	}
	st.MKTC, st.MeanC = round3(mkt), round3(sum/float64(len(rs)))
	st.MinC, st.MaxC = round3(st.MinC), round3(st.MaxC)
	if len(rs) > 2 {
		lo, hi := time.Duration(math.MaxInt64), time.Duration(0)
		for i := 1; i < len(rs); i++ {
			d := rs[i].At.Sub(rs[i-1].At)
			lo, hi = min(lo, d), max(hi, d)
		}
		st.EvenSampling = lo > 0 && float64(hi)/float64(lo) <= 1.5
	}
	return st, nil
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// Dispositions of an excursion.
const (
	DispQuarantine = "quarantine"
	DispRelease    = "release"
	DispDestroy    = "destroy"
	DispOpen       = "open" // no decision yet
)

// Disposition is one recorded decision on an excursion.
type Disposition struct {
	ID          uuid.UUID         `json:"id" db:"id"`
	ExcursionID uuid.UUID         `json:"excursionId" db:"excursion_id"`
	Disposition string            `json:"disposition" db:"disposition"`
	Reason      string            `json:"reason" db:"reason"`
	TLCs        []string          `json:"tlcs" db:"tlcs"`
	PriorStatus map[string]string `json:"priorStatus" db:"prior_status"`
	SignatureID *uuid.UUID        `json:"signatureId,omitempty" db:"signature_id"`
	SignedBy    string            `json:"signedBy,omitempty" db:"signed_by"`
	Meaning     string            `json:"signatureMeaning,omitempty" db:"meaning"`
	DecidedBy   uuid.UUID         `json:"decidedBy" db:"decided_by"`
	DecidedAt   time.Time         `json:"decidedAt" db:"decided_at"`
}

const dispCols = `id, excursion_id, disposition, reason, tlcs, prior_status, signature_id, signed_by, meaning, decided_by, decided_at`

// Excursion is a sensor excursion with its current disposition.
type Excursion struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	DeviceID    string     `json:"deviceId" db:"device_id"`
	EPC         *string    `json:"epc,omitempty" db:"epc"`
	ShipmentID  *uuid.UUID `json:"shipmentId,omitempty" db:"shipment_id"`
	MinC        float64    `json:"minC" db:"min_c"`
	MaxC        float64    `json:"maxC" db:"max_c"`
	PeakC       float64    `json:"peakC" db:"peak_c"`
	Minutes     int        `json:"minutes" db:"minutes"`
	StartedAt   time.Time  `json:"startedAt" db:"started_at"`
	EndedAt     time.Time  `json:"endedAt" db:"ended_at"`
	SensorState string     `json:"sensorStatus" db:"status"`
	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
	Disposition string     `json:"disposition" db:"disposition"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty" db:"decided_at"`
}

const excCols = `e.id, e.device_id, e.epc, e.shipment_id, e.min_c, e.max_c, e.peak_c, e.minutes, e.started_at, e.ended_at, e.status, e.created_at,
	coalesce(d.disposition, 'open') as disposition, d.decided_at`

const excFrom = ` from sensor_excursion e left join lateral (select disposition, decided_at from food_excursion_disposition x
	where x.excursion_id = e.id order by x.decided_at desc, x.id desc limit 1) d on true`

func (m *Module) listExcursions(w http.ResponseWriter, r *http.Request) error {
	want := r.URL.Query().Get("disposition")
	if want != "" && !slices.Contains([]string{DispOpen, DispQuarantine, DispRelease, DispDestroy}, want) {
		return httpx.BadRequest("disposition must be open, quarantine, release or destroy")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Excursion](r.Context(), tx, `select `+excCols+excFrom+` where ($1='' or coalesce(d.disposition,'open')=$1) order by e.started_at desc limit 500`, want)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Excursion]{Items: items, Total: len(items)})
		}
		return err
	})
}

func loadExcursion(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Excursion, error) {
	return db.One[Excursion](ctx, tx, `select `+excCols+excFrom+` where e.id=$1`, id)
}

func loadReadings(ctx context.Context, tx pgx.Tx, device string, from, to time.Time) ([]Reading, error) {
	return db.List[Reading](ctx, tx, `select at, temp_c from sensor_reading where device_id=$1 and at >= $2 and at <= $3 and temp_c is not null order by at limit 100000`, device, from, to)
}

func activationParam(r *http.Request) (float64, error) {
	v := r.URL.Query().Get("activationEnergyKJ")
	if v == "" {
		return DefaultActivationEnergyKJ, nil
	}
	var f float64
	if _, err := fmt.Sscanf(v, "%g", &f); err != nil || f <= 0 || f > 1000 {
		return 0, httpx.BadRequest("activationEnergyKJ must be a number above 0 and at most 1000")
	}
	return f, nil
}

var mktNotes = []string{
	fmt.Sprintf("Mean kinetic temperature uses the Haynes formula with an activation energy of %.3f kJ/mol unless another value is given. TODO(spec): product-specific activation energies are not modelled; take them from the product's stability data or the applicable guidance.", DefaultActivationEnergyKJ),
	"Readings are weighted equally; when sampling is uneven the result says so (evenlySampled=false).",
}

// mktWindow serves GET /cold-chain/mkt?deviceId&from&to[&activationEnergyKJ].
func (m *Module) mktWindow(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	dev := q.Get("deviceId")
	from, err1 := time.Parse(time.RFC3339, q.Get("from"))
	to, err2 := time.Parse(time.RFC3339, q.Get("to"))
	if dev == "" || err1 != nil || err2 != nil || !to.After(from) {
		return httpx.BadRequest("deviceId and an RFC 3339 from < to window are required")
	}
	ae, err := activationParam(r)
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rs, err := loadReadings(r.Context(), tx, dev, from, to)
		if err != nil {
			return err
		}
		st, err := Stats(rs, ae)
		if err != nil {
			return httpx.BadRequest(err.Error())
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"deviceId": dev, "from": from, "to": to, "stats": st, "notes": mktNotes})
		return nil
	})
}

// ExcursionReport is the exportable record of one excursion.
type ExcursionReport struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Excursion   Excursion        `json:"excursion"`
	Stats       *TempStats       `json:"stats,omitempty"`
	StatsError  string           `json:"statsError,omitempty"`
	Readings    []Reading        `json:"readings"`
	History     []Disposition    `json:"dispositions"`
	Lots        []map[string]any `json:"lots"`
	Notes       []string         `json:"notes"`
}

func buildReport(ctx context.Context, tx pgx.Tx, id uuid.UUID, ae float64) (ExcursionReport, error) {
	ex, err := loadExcursion(ctx, tx, id)
	if err != nil {
		return ExcursionReport{}, err
	}
	rep := ExcursionReport{GeneratedAt: time.Now().UTC(), Excursion: ex, Notes: append([]string{}, mktNotes...)}
	if rep.Readings, err = loadReadings(ctx, tx, ex.DeviceID, ex.StartedAt, ex.EndedAt); err != nil {
		return rep, err
	}
	if len(rep.Readings) > 0 {
		st, err := Stats(rep.Readings, ae)
		if err != nil {
			rep.StatsError = err.Error()
		} else {
			rep.Stats = &st
		}
	} else {
		rep.StatsError = "no readings stored for the excursion window"
	}
	if rep.History, err = db.List[Disposition](ctx, tx, `select `+dispCols+` from food_excursion_disposition where excursion_id=$1 order by decided_at, id`, id); err != nil {
		return rep, err
	}
	rep.Lots = []map[string]any{}
	seen := map[string]bool{}
	for _, d := range rep.History {
		for _, t := range d.TLCs {
			seen[t] = true
		}
	}
	tlcs := make([]string, 0, len(seen))
	for t := range seen {
		tlcs = append(tlcs, t)
	}
	slices.Sort(tlcs)
	for _, t := range tlcs {
		var status string
		if err := tx.QueryRow(ctx, `select status from food_lot where tlc=$1`, t).Scan(&status); err == nil {
			rep.Lots = append(rep.Lots, map[string]any{"tlc": t, "status": status})
		}
	}
	return rep, nil
}

func (m *Module) getExcursion(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	ae, err := activationParam(r)
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rep, err := buildReport(r.Context(), tx, id, ae)
		if err == nil {
			httpx.JSON(w, http.StatusOK, rep)
		}
		return err
	})
}

// exportExcursion serves the report as JSON or CSV; every download is audited.
func (m *Module) exportExcursion(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		return httpx.BadRequest("format must be json or csv")
	}
	ae, err := activationParam(r)
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rep, err := buildReport(r.Context(), tx, id, ae)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "food.coldchain.report.export", Entity: "sensor_excursion", EntityID: id.String(),
			After: map[string]any{"format": format, "readings": len(rep.Readings), "disposition": rep.Excursion.Disposition}}); err != nil {
			return err
		}
		name := "excursion-" + id.String()[:8]
		if format == "json" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
			httpx.JSON(w, http.StatusOK, rep)
			return nil
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		writeReportCSV(w, rep)
		return nil
	})
}

func writeReportCSV(w http.ResponseWriter, rep ExcursionReport) {
	ex := rep.Excursion
	c := func(format string, a ...any) { fmt.Fprintf(w, "# "+format+"\n", a...) }
	c("Cold-chain excursion report %s generated %s", ex.ID, rep.GeneratedAt.Format(time.RFC3339))
	c("device %s; allowed %.1f to %.1f C; peak %.1f C; %d min; %s to %s", ex.DeviceID, ex.MinC, ex.MaxC, ex.PeakC, ex.Minutes, ex.StartedAt.UTC().Format(time.RFC3339), ex.EndedAt.UTC().Format(time.RFC3339))
	if rep.Stats != nil {
		c("readings %d; min %.3f; max %.3f; mean %.3f; MKT %.3f C (activation energy %.3f kJ/mol, evenly sampled %t)",
			rep.Stats.Count, rep.Stats.MinC, rep.Stats.MaxC, rep.Stats.MeanC, rep.Stats.MKTC, rep.Stats.ActivationKJ, rep.Stats.EvenSampling)
	} else {
		c("statistics unavailable: %s", rep.StatsError)
	}
	c("current disposition: %s", ex.Disposition)
	for _, d := range rep.History {
		sig := "unsigned"
		if d.SignatureID != nil {
			sig = fmt.Sprintf("signed by %s (%s)", d.SignedBy, d.Meaning)
		}
		c("%s %s: %s; lots %s; %s", d.DecidedAt.UTC().Format(time.RFC3339), d.Disposition, oneLine(d.Reason), strings.Join(d.TLCs, " "), sig)
	}
	for _, n := range rep.Notes {
		c("%s", n)
	}
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time_utc", "temp_c"})
	for _, r := range rep.Readings {
		_ = cw.Write([]string{r.At.UTC().Format(time.RFC3339), trimFloat(r.TempC)})
	}
	cw.Flush()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// dispositionAllowed reports whether to may follow the current state.
func dispositionAllowed(current, to string) bool {
	switch current {
	case DispOpen:
		return to == DispQuarantine || to == DispRelease || to == DispDestroy
	case DispQuarantine:
		return to == DispRelease || to == DispDestroy
	}
	return false // release and destroy are final
}

// disposeExcursion records a decision. Quarantine needs a reason; release and destroy also need
// the caller's electronic signature on this excursion (POST /esignatures, entity food_excursion).
// TODO(spec): which dispositions need which approvals is a quality-system decision, not a rule
// of this software; the signature requirement is a conservative default.
func (m *Module) disposeExcursion(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Disposition string     `json:"disposition"`
		Reason      string     `json:"reason"`
		SignatureID *uuid.UUID `json:"signatureId"`
		TLCs        []string   `json:"tlcs"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !slices.Contains([]string{DispQuarantine, DispRelease, DispDestroy}, in.Disposition) {
		return httpx.BadRequest("disposition must be quarantine, release or destroy")
	}
	if in.Reason == "" || len(in.Reason) > 2000 {
		return httpx.BadRequest("a reason is required (max 2000 characters)")
	}
	if len(in.TLCs) > 200 {
		return httpx.BadRequest("at most 200 lots per decision")
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		// Serialise decisions on one excursion.
		var created time.Time
		if err := tx.QueryRow(r.Context(), `select created_at from sensor_excursion where id=$1 for update`, id).Scan(&created); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("excursion not found")
			}
			return err
		}
		ex, err := loadExcursion(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !dispositionAllowed(ex.Disposition, in.Disposition) {
			return httpx.Conflict(fmt.Sprintf("an excursion in state %s cannot be moved to %s", ex.Disposition, in.Disposition))
		}
		d := Disposition{ID: ids.New(), ExcursionID: id, Disposition: in.Disposition, Reason: in.Reason, DecidedBy: p.UserID, TLCs: []string{}, PriorStatus: map[string]string{}}
		// Lots: the ones named, or those held under the earlier quarantine.
		var held Disposition
		hasHeld := false
		if ex.Disposition == DispQuarantine {
			held, err = db.One[Disposition](r.Context(), tx, `select `+dispCols+` from food_excursion_disposition where excursion_id=$1 and disposition='quarantine' order by decided_at desc limit 1`, id)
			if err != nil {
				return err
			}
			hasHeld = true
		}
		tlcs := uniqueTrimmed(in.TLCs)
		if len(tlcs) == 0 && hasHeld {
			tlcs = held.TLCs
		}
		if in.Disposition != DispQuarantine {
			since := created
			if ex.DecidedAt != nil {
				since = *ex.DecidedAt
			}
			if in.SignatureID == nil {
				return &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "releasing or destroying needs your electronic signature (POST /esignatures with entity food_excursion and the excursion id as entityId) and its id as signatureId"}
			}
			var email, meaning string
			if err := tx.QueryRow(r.Context(), `select email, meaning from esignature where id=$1 and entity='food_excursion' and entity_id=$2 and user_id=$3 and signed_at >= $4`,
				*in.SignatureID, id.String(), p.UserID, since).Scan(&email, &meaning); err != nil {
				return &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "signature not found: sign this excursion after its last decision (POST /esignatures with entity food_excursion) and pass its id"}
			}
			d.SignatureID, d.SignedBy, d.Meaning = in.SignatureID, email, meaning
		}
		for _, t := range tlcs {
			var status string
			if err := tx.QueryRow(r.Context(), `select status from food_lot where tlc=$1 for update`, t).Scan(&status); err != nil {
				return httpx.BadRequest("unknown lot " + t)
			}
			switch in.Disposition {
			case DispQuarantine:
				if status != "active" && status != "shipped" {
					return httpx.Conflict(fmt.Sprintf("lot %s is %s and cannot be quarantined", t, status))
				}
				d.PriorStatus[t] = status
				_, err = tx.Exec(r.Context(), `update food_lot set status='quarantined' where tlc=$1`, t)
			case DispRelease:
				if status == "quarantined" {
					back := held.PriorStatus[t]
					if back == "" {
						back = "active"
					}
					_, err = tx.Exec(r.Context(), `update food_lot set status=$2 where tlc=$1`, t, back)
				}
			case DispDestroy:
				if status == "consumed" || status == "destroyed" {
					return httpx.Conflict(fmt.Sprintf("lot %s is already %s", t, status))
				}
				_, err = tx.Exec(r.Context(), `update food_lot set status='destroyed' where tlc=$1`, t)
			}
			if err != nil {
				return err
			}
		}
		d.TLCs = tlcs
		out, err := db.One[Disposition](r.Context(), tx, `insert into food_excursion_disposition (id, tenant_id, excursion_id, disposition, reason, tlcs, prior_status, signature_id, signed_by, meaning, decided_by)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10) returning `+dispCols,
			d.ID, id, d.Disposition, d.Reason, d.TLCs, d.PriorStatus, d.SignatureID, d.SignedBy, d.Meaning, d.DecidedBy)
		if errors.Is(err, db.ErrConflict) {
			return httpx.Conflict("this signature was already used for another decision; sign again")
		}
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "food.coldchain.disposition." + out.Disposition, Entity: "sensor_excursion", EntityID: id.String(),
			Before: map[string]string{"disposition": ex.Disposition}, After: out, Reason: out.Reason}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, out)
		return nil
	})
}

func uniqueTrimmed(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// coldChainRoutes registers the cold-chain endpoints under /food.
func (m *Module) coldChainRoutes(r chi.Router) {
	r.Get("/cold-chain/mkt", httpx.Handler(m.mktWindow))
	r.Get("/cold-chain/excursions", httpx.Handler(m.listExcursions))
	r.Get("/cold-chain/excursions/{id}", httpx.Handler(m.getExcursion))
	r.Get("/cold-chain/excursions/{id}/report", httpx.Handler(m.exportExcursion))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance)).Post("/cold-chain/excursions/{id}/disposition", httpx.Handler(m.disposeExcursion))
}
