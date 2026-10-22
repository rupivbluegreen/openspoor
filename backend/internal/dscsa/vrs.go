package dscsa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

var vrsSeconds = promauto.NewHistogram(prometheus.HistogramOpts{Name: "openspoor_vrs_response_seconds", Help: "VRS responder latency",
	Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2}})

// VRSRequest is a GS1 Lightweight Messaging verification request.
// TODO(spec): field names follow the GS1 Lightweight Messaging Standard for Verification
// of Product Identifiers; confirm against the current release before certification.
type VRSRequest struct {
	GTIN           string `json:"gtin"`
	SerialNumber   string `json:"serialNumber"`
	LotNumber      string `json:"lotNumber"`
	ExpirationDate string `json:"expirationDate"` // YYMMDD
	RequestorGLN   string `json:"requestorGLN"`
	Context        string `json:"context"` // e.g. dscsaSaleableReturn
	CorrUUID       string `json:"corrUUID"`
}

// VRSResponse is the responder's answer.
type VRSResponse struct {
	CorrUUID              string    `json:"corrUUID"`
	VerificationTimestamp time.Time `json:"verificationTimestamp"`
	ResponderGLN          string    `json:"responderGLN,omitempty"`
	Data                  VRSData   `json:"data"`
}

// VRSData carries the verification result.
type VRSData struct {
	Verified                  bool   `json:"verified"`
	VerificationFailureReason string `json:"verificationFailureReason,omitempty"`
	AdditionalInfo            string `json:"additionalInfo,omitempty"`
}

// Verify answers a VRS request for this tenant's products (we are the manufacturer).
func Verify(ctx context.Context, tx pgx.Tx, q VRSRequest) VRSData {
	gtin := gs1.NormalizeGTIN(q.GTIN)
	if _, err := masterdata.ByGTIN(ctx, tx, gtin); err != nil {
		return VRSData{VerificationFailureReason: "No_match_GTIN"}
	}
	st, err := trace.Lookup(ctx, tx, gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: gtin, Serial: q.SerialNumber}))
	if err != nil {
		return VRSData{VerificationFailureReason: "No_match_GTIN_Serial"}
	}
	if q.LotNumber != "" && (st.Lot == nil || *st.Lot != q.LotNumber) {
		return VRSData{VerificationFailureReason: "No_match_GTIN_Serial_Lot"}
	}
	if q.ExpirationDate != "" && st.Expiry != nil {
		want, err := gs1.ExpiryDate(q.ExpirationDate)
		if err != nil || !sameDay(want, *st.Expiry) {
			return VRSData{VerificationFailureReason: "No_match_GTIN_Serial_Lot_Expiry"}
		}
	}
	switch st.Status {
	case "recalled":
		return VRSData{VerificationFailureReason: "Recalled", AdditionalInfo: "product is under recall"}
	case "decommissioned", "destroyed", "stolen", "dispensed":
		return VRSData{VerificationFailureReason: "Not_saleable", AdditionalInfo: "product status is " + st.Status}
	}
	if st.Expiry != nil && st.Expiry.Before(time.Now()) {
		return VRSData{VerificationFailureReason: "Expired"}
	}
	return VRSData{Verified: true}
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// vrsRespond is the public responder endpoint. Callers authenticate with an API key
// (VRS routers are configured with one per trading relationship).
func (m *Module) vrsRespond(w http.ResponseWriter, r *http.Request) error {
	start := time.Now()
	p, err := m.Auth.Authenticate(r)
	if err != nil {
		return httpx.Unauthorized("VRS requests require an API key")
	}
	ctx := auth.With(r.Context(), p)
	var q VRSRequest
	if err := httpx.Decode(r, &q); err != nil {
		return err
	}
	if q.GTIN == "" || q.SerialNumber == "" {
		return httpx.BadRequest("gtin and serialNumber are required")
	}
	if q.CorrUUID == "" {
		q.CorrUUID = uuid.NewString()
	}
	resp := VRSResponse{CorrUUID: q.CorrUUID, VerificationTimestamp: time.Now().UTC()}
	err = m.DB.Tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		resp.Data = Verify(ctx, tx, q)
		resp.ResponderGLN = sellerParty(ctx, tx).GLN
		lat := int(time.Since(start).Milliseconds())
		corr, _ := uuid.Parse(q.CorrUUID)
		_, err := tx.Exec(ctx, `insert into vrs_request (id, tenant_id, direction, corr_uuid, gtin, serial, lot, expiry, requestor_gln, context, verified, reason, responder, latency_ms, response)
			values ($1, app_tenant(), 'inbound', $2,$3,$4,$5,$6,$7,$8,$9,$10,'self',$11,$12)`,
			ids.New(), corr, q.GTIN, q.SerialNumber, q.LotNumber, q.ExpirationDate, q.RequestorGLN, q.Context, resp.Data.Verified, resp.Data.VerificationFailureReason, lat, resp)
		return err
	})
	if err != nil {
		return err
	}
	vrsSeconds.Observe(time.Since(start).Seconds())
	httpx.JSON(w, http.StatusOK, resp)
	return nil
}

type vrsRow struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	Direction    string         `json:"direction" db:"direction"`
	CorrUUID     uuid.UUID      `json:"corrUUID" db:"corr_uuid"`
	GTIN         string         `json:"gtin" db:"gtin"`
	Serial       string         `json:"serial" db:"serial"`
	Lot          string         `json:"lot" db:"lot"`
	Expiry       string         `json:"expiry" db:"expiry"`
	RequestorGLN string         `json:"requestorGLN" db:"requestor_gln"`
	Context      string         `json:"context" db:"context"`
	Verified     *bool          `json:"verified" db:"verified"`
	Reason       string         `json:"reason" db:"reason"`
	Responder    string         `json:"responder" db:"responder"`
	LatencyMs    int            `json:"latencyMs" db:"latency_ms"`
	Response     map[string]any `json:"response,omitempty" db:"response"`
	CreatedAt    time.Time      `json:"createdAt" db:"created_at"`
}

func (m *Module) listVRS(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[vrsRow](r.Context(), tx, `select id, direction, corr_uuid, gtin, serial, lot, expiry, requestor_gln, context, verified, reason, responder, latency_ms, response, created_at
			from vrs_request where ($1='' or direction=$1) order by created_at desc limit 500`, r.URL.Query().Get("direction"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[vrsRow]{Items: items, Total: len(items)})
		}
		return err
	})
}

type dirEntry struct {
	ID           uuid.UUID `json:"id" db:"id"`
	GTINPrefix   string    `json:"gtinPrefix" db:"gtin_prefix"`
	ResponderURL string    `json:"responderUrl" db:"responder_url"`
	ResponderGLN *string   `json:"responderGln,omitempty" db:"responder_gln"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
	HasAPIKey    bool      `json:"hasApiKey" db:"-"`
}

func (m *Module) listDirectory(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[dirEntry](r.Context(), tx, `select id, gtin_prefix, responder_url, responder_gln, created_at from vrs_directory order by gtin_prefix`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[dirEntry]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) putDirectory(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		GTINPrefix   string  `json:"gtinPrefix"`
		ResponderURL string  `json:"responderUrl"`
		ResponderGLN *string `json:"responderGln"`
		APIKey       string  `json:"apiKey"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.GTINPrefix) < 4 || strings.Trim(in.GTINPrefix, "0123456789") != "" {
		return httpx.BadRequest("gtinPrefix must be at least 4 digits")
	}
	if !strings.HasPrefix(in.ResponderURL, "https://") && !strings.HasPrefix(in.ResponderURL, "http://") && !strings.HasPrefix(in.ResponderURL, "local:") {
		return httpx.BadRequest("responderUrl must be http(s) or local:<tenantId>")
	}
	id := ids.New()
	var creds []byte
	if in.APIKey != "" {
		creds = m.Seal.Seal([]byte(in.APIKey), []byte("vrs:"+in.GTINPrefix))
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := db.One[dirEntry](r.Context(), tx, `insert into vrs_directory (id, tenant_id, gtin_prefix, responder_url, responder_gln, credentials) values ($1, app_tenant(), $2,$3,$4,$5)
			on conflict (tenant_id, gtin_prefix) do update set responder_url=excluded.responder_url, responder_gln=excluded.responder_gln, credentials=coalesce(excluded.credentials, vrs_directory.credentials)
			returning id, gtin_prefix, responder_url, responder_gln, created_at`, id, in.GTINPrefix, in.ResponderURL, in.ResponderGLN, creds)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "vrs.directory.put", Entity: "vrs_directory", EntityID: d.ID.String(), After: d}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, d)
		return nil
	})
}

func (m *Module) deleteDirectory(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ct, err := tx.Exec(r.Context(), `delete from vrs_directory where id=$1`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return db.ErrNotFound
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "vrs.directory.delete", Entity: "vrs_directory", EntityID: id.String()})
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
		}
		return err
	})
}

// vrsRequest sends a verification request through the lookup directory (longest GTIN prefix wins).
func (m *Module) vrsRequest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		GTIN    string `json:"gtin"`
		Serial  string `json:"serial"`
		Lot     string `json:"lot"`
		Expiry  string `json:"expiry"`
		Context string `json:"context"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.GTIN = gs1.NormalizeGTIN(in.GTIN)
	if !gs1.ValidGTIN(in.GTIN) || in.Serial == "" {
		return httpx.BadRequest("valid gtin and serial are required")
	}
	if in.Context == "" {
		in.Context = "dscsaSaleableReturn"
	}
	ctx := r.Context()
	type entry struct {
		prefix, url string
		gln         *string
		creds       []byte
	}
	var dir []entry
	var me party
	err := m.Tx(ctx, func(tx pgx.Tx) error {
		me = sellerParty(ctx, tx)
		rows, err := tx.Query(ctx, `select gtin_prefix, responder_url, responder_gln, credentials from vrs_directory`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.prefix, &e.url, &e.gln, &e.creds); err != nil {
				return err
			}
			dir = append(dir, e)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	sort.Slice(dir, func(i, j int) bool { return len(dir[i].prefix) > len(dir[j].prefix) })
	var target *entry
	for i := range dir {
		if strings.HasPrefix(in.GTIN, dir[i].prefix) || strings.HasPrefix(in.GTIN[1:], dir[i].prefix) {
			target = &dir[i]
			break
		}
	}
	if target == nil {
		return httpx.NotFound("no VRS responder in the lookup directory for this GTIN")
	}
	q := VRSRequest{GTIN: in.GTIN, SerialNumber: in.Serial, LotNumber: in.Lot, ExpirationDate: in.Expiry, RequestorGLN: me.GLN, Context: in.Context, CorrUUID: uuid.NewString()}
	start := time.Now()
	var resp VRSResponse
	var callErr error
	if tenant, ok := strings.CutPrefix(target.url, "local:"); ok {
		tid, err := uuid.Parse(tenant)
		if err != nil {
			return httpx.BadRequest("invalid local responder")
		}
		resp = VRSResponse{CorrUUID: q.CorrUUID, VerificationTimestamp: time.Now().UTC()}
		callErr = m.DB.Tx(ctx, tid, func(tx pgx.Tx) error {
			resp.Data = Verify(ctx, tx, q)
			resp.ResponderGLN = sellerParty(ctx, tx).GLN
			return nil
		})
	} else {
		apiKey := ""
		if len(target.creds) > 0 {
			if b, err := m.Seal.Open(target.creds, []byte("vrs:"+target.prefix)); err == nil {
				apiKey = string(b)
			}
		}
		resp, callErr = m.callVRS(ctx, target.url, apiKey, q)
	}
	lat := int(time.Since(start).Milliseconds())
	verified := callErr == nil && resp.Data.Verified
	reason := resp.Data.VerificationFailureReason
	if callErr != nil {
		reason = "responder_error: " + callErr.Error()
	}
	corr, _ := uuid.Parse(q.CorrUUID)
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into vrs_request (id, tenant_id, direction, corr_uuid, gtin, serial, lot, expiry, requestor_gln, context, verified, reason, responder, latency_ms, response)
			values ($1, app_tenant(), 'outbound', $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			ids.New(), corr, q.GTIN, q.SerialNumber, q.LotNumber, q.ExpirationDate, q.RequestorGLN, q.Context, verified, reason, target.url, lat, resp)
		if err != nil {
			return err
		}
		_, err = audit.Write(ctx, tx, audit.Record{Action: "vrs.request", Entity: "vrs_request", EntityID: corr.String(), After: map[string]any{"gtin": q.GTIN, "serial": q.SerialNumber, "verified": verified, "reason": reason}})
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"verified": verified, "reason": reason, "responder": target.url, "latencyMs": lat, "corrUUID": q.CorrUUID, "response": resp})
	return nil
}

func (m *Module) callVRS(ctx context.Context, url, apiKey string, q VRSRequest) (VRSResponse, error) {
	var out VRSResponse
	b, _ := json.Marshal(q)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	res, err := m.Client.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return out, fmt.Errorf("responder returned %d", res.StatusCode)
	}
	return out, json.Unmarshal(body, &out)
}

// --- retention ---

type retention struct {
	DataClass string `json:"dataClass" db:"data_class"`
	Years     int    `json:"years" db:"years"`
	Basis     string `json:"basis" db:"basis"`
}

// Defaults applied when a tenant has not configured a class.
var defaultRetention = []retention{
	{"epcis_events", RetentionYears, "DSCSA: FD&C Act 582 — transaction information, history and statements kept 6 years"},
	{"vrs_requests", RetentionYears, "DSCSA verification records kept 6 years"},
}

func (m *Module) listRetention(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[retention](r.Context(), tx, `select data_class, years, basis from retention_policy order by data_class`)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, it := range items {
			have[it.DataClass] = true
		}
		for _, d := range defaultRetention {
			if !have[d.DataClass] {
				items = append(items, d)
			}
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[retention]{Items: items, Total: len(items)})
		return nil
	})
}

func (m *Module) putRetention(w http.ResponseWriter, r *http.Request) error {
	class := chi.URLParam(r, "class")
	var in struct {
		Years int    `json:"years"`
		Basis string `json:"basis"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	for _, d := range defaultRetention {
		if d.DataClass == class && in.Years < d.Years {
			return httpx.BadRequest(fmt.Sprintf("%s cannot be kept less than %d years (%s)", class, d.Years, d.Basis))
		}
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rt, err := db.One[retention](r.Context(), tx, `insert into retention_policy (tenant_id, data_class, years, basis) values (app_tenant(), $1,$2,$3)
			on conflict (tenant_id, data_class) do update set years=excluded.years, basis=excluded.basis returning data_class, years, basis`, class, in.Years, in.Basis)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "retention.put", Entity: "retention_policy", EntityID: class, After: rt}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, rt)
		return nil
	})
}

// enforceRetention deletes event and VRS records older than each tenant's retention period.
// Nothing younger than the regulatory minimum is ever deleted.
func (m *Module) enforceRetention(ctx context.Context) error {
	return m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			delete from epcis_event e using tenant t
			where e.tenant_id = t.id and e.record_time < now() - make_interval(years => greatest($1,
				coalesce((select years from retention_policy p where p.tenant_id = t.id and p.data_class = 'epcis_events'), $1)))`, RetentionYears)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			delete from vrs_request v where v.created_at < now() - make_interval(years => greatest($1,
				coalesce((select years from retention_policy p where p.tenant_id = v.tenant_id and p.data_class = 'vrs_requests'), $1)))`, RetentionYears)
		return err
	})
}

var _ = lib.Canonical
