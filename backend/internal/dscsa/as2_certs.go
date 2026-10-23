package dscsa

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/pkg/as2"
)

// AS2 certificate lifecycle of this node: a rotation flow for the station certificate, expiry
// alerts, and the overview of the station's and every partner's certificates.

// JobAS2StationCerts is the schedule that promotes a due next certificate and raises expiry alerts.
const JobAS2StationCerts = "dscsa.as2_station_certs"

func (m *Module) certSchedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: JobAS2StationCerts, Every: 30 * time.Minute, Run: m.stationCertsJob}}
}

// putStationNext stores the node's next certificate and key and the date signing switches to it.
// Until then the current certificate signs and both decrypt. {generate: true} makes a new
// self-signed certificate for the station's AS2 identifier.
func (m *Module) putStationNext(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		CertPEM  string     `json:"certPem"`
		KeyPEM   string     `json:"keyPem"`
		Generate bool       `json:"generate"`
		SwitchAt *time.Time `json:"switchAt"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	now := time.Now()
	switchAt, err := partners.SwitchDate(in.SwitchAt, now)
	if err != nil {
		return err
	}
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		old, err := db.One[AS2Station](r.Context(), tx, `select `+stationCols+` from as2_station where tenant_id = app_tenant()`)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return httpx.Conflict("set this node's AS2 station first")
			}
			return err
		}
		old.assemble(now)
		if old.Active == "next" {
			return httpx.Conflict("a rotation is already due; wait for the switch to complete")
		}
		cur, err := as2.ParseCertPEM(old.CertPEM, time.Time{})
		if err != nil {
			return err
		}
		var key *rsa.PrivateKey
		var certPEM string
		var next as2.CertInfo
		var nextCert *x509.Certificate
		if in.Generate {
			c, k, err := as2.GenerateStation(old.AS2ID, as2CertValidity, now)
			if err != nil {
				return err
			}
			key, certPEM, nextCert = k, as2.CertPEM(c), c
		} else {
			c, err := as2.ParseCertPEM(in.CertPEM, time.Time{})
			if err != nil {
				return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
			}
			if key, err = as2.ParseKeyPEM(in.KeyPEM); err != nil {
				return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
			}
			if pub, ok := c.PublicKey.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
				return httpx.BadRequest("the private key does not match the certificate")
			}
			certPEM, nextCert = as2.CertPEM(c), c
		}
		if err := as2.CheckRotation(cur, nextCert, switchAt, now); err != nil {
			return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
		}
		next = as2.Describe(nextCert)
		keyPEM, err := as2.EncodeKeyPEM(key)
		if err != nil {
			return err
		}
		sealed, err := m.Keys.Seal(r.Context(), tenant, keyPEM, stationAAD(tenant))
		if err != nil {
			return err
		}
		s, err := db.One[AS2Station](r.Context(), tx, `update as2_station set next_cert_pem=$1, next_key_sealed=$2, next_fingerprint=$3, next_subject=$4, next_not_before=$5, next_not_after=$6, switch_at=$7, updated_at=now()
			where tenant_id = app_tenant() returning `+stationCols, certPEM, sealed, next.Fingerprint, next.Subject, next.NotBefore, next.NotAfter, switchAt)
		if err != nil {
			return err
		}
		if old.Next != nil && old.Next.Fingerprint != next.Fingerprint {
			if err := partners.ResolveCertAlerts(r.Context(), tx, old.Next.Fingerprint); err != nil {
				return err
			}
		}
		s.ReceiveURL = m.receiveURL()
		s.assemble(now)
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "as2.station.next.set", Entity: "as2_station", EntityID: tenant.String(),
			Before: stationAudit(old), After: stationAudit(s)}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

// stationAudit names the certificates by fingerprint; keys and PEM are never recorded.
func stationAudit(s AS2Station) map[string]any {
	out := map[string]any{"as2Id": s.AS2ID, "fingerprint": s.Fingerprint, "subject": s.Subject, "notAfter": s.NotAfter}
	if s.Next != nil {
		out["next"] = map[string]any{"fingerprint": s.Next.Fingerprint, "notAfter": s.Next.NotAfter, "switchAt": s.Next.SwitchAt}
	}
	return out
}

func (m *Module) deleteStationNext(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		old, err := db.One[AS2Station](r.Context(), tx, `select `+stationCols+` from as2_station where tenant_id = app_tenant()`)
		if err != nil {
			return err
		}
		old.assemble(time.Now())
		if old.Next == nil {
			return db.ErrNotFound
		}
		if _, err := tx.Exec(r.Context(), `update as2_station set next_cert_pem=null, next_key_sealed=null, next_fingerprint=null, next_subject=null, next_not_before=null, next_not_after=null, switch_at=null, updated_at=now() where tenant_id = app_tenant()`); err != nil {
			return err
		}
		if err := partners.ResolveCertAlerts(r.Context(), tx, old.Next.Fingerprint); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "as2.station.next.cancel", Entity: "as2_station", EntityID: kernel.TenantOf(r.Context()).String(), Before: stationAudit(old)}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// stationCertsJob promotes a next certificate that reached its switch date and raises expiry
// alerts for every tenant with an AS2 station.
func (m *Module) stationCertsJob(ctx context.Context) error {
	var tenants []uuid.UUID
	if err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select tenant_id from as2_station`)
		if err != nil {
			return err
		}
		tenants, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return err
	}
	for _, t := range tenants {
		if err := m.DB.Tx(ctx, t, func(tx pgx.Tx) error { return m.stationCertsTenant(ctx, tx, t, time.Now()) }); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) stationCertsTenant(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, now time.Time) error {
	s, err := db.One[AS2Station](ctx, tx, `select `+stationCols+` from as2_station where tenant_id=$1`, tenant)
	if err != nil {
		return err
	}
	s.assemble(now)
	if s.Active == "next" {
		if _, err := tx.Exec(ctx, `update as2_station set cert_pem=next_cert_pem, key_sealed=next_key_sealed, fingerprint=next_fingerprint, subject=next_subject, not_before=next_not_before, not_after=next_not_after,
			next_cert_pem=null, next_key_sealed=null, next_fingerprint=null, next_subject=null, next_not_before=null, next_not_after=null, switch_at=null, updated_at=now() where tenant_id=$1`, tenant); err != nil {
			return err
		}
		if err := partners.ResolveCertAlerts(ctx, tx, s.Fingerprint); err != nil {
			return err
		}
		if _, err := audit.WriteAs(ctx, tx, tenant, "system", audit.Record{Action: "as2.station.next.promote", Entity: "as2_station", EntityID: tenant.String(),
			Before: stationAudit(s), After: map[string]any{"fingerprint": s.Next.Fingerprint, "notAfter": s.Next.NotAfter, "switchAt": s.Next.SwitchAt, "promotedAt": now.UTC()}}); err != nil {
			return err
		}
		s.Fingerprint, s.NotAfter, s.Next = s.Next.Fingerprint, s.Next.NotAfter, nil
	}
	if _, err := partners.RaiseCertAlert(ctx, tx, tenant, partners.ScopeStation, tenant.String(), s.AS2ID, s.Fingerprint, s.NotAfter, now); err != nil {
		return err
	}
	if s.Next != nil {
		if _, err := partners.RaiseCertAlert(ctx, tx, tenant, partners.ScopeStationNext, tenant.String(), s.AS2ID, s.Next.Fingerprint, s.Next.NotAfter, now); err != nil {
			return err
		}
	}
	return nil
}

// --- overview ---

// StationCerts is this node's certificates in the overview.
type StationCerts struct {
	AS2ID      string                `json:"as2Id"`
	ReceiveURL string                `json:"receiveUrl"`
	Current    partners.AS2CertView  `json:"current"`
	Next       *partners.AS2CertView `json:"next,omitempty"`
	SwitchAt   *time.Time            `json:"switchAt,omitempty"`
	Active     string                `json:"active"`
	Warnings   []partners.AS2Warning `json:"warnings"`
}

// CertSummary counts what needs attention.
type CertSummary struct {
	Partners  int `json:"partners"`
	Expired   int `json:"expired"`
	Expiring  int `json:"expiring"`
	Legacy    int `json:"legacy"`
	Rotations int `json:"rotations"`
}

// CertOverview is the response of GET /as2/certificates.
type CertOverview struct {
	Station    *StationCerts              `json:"station"`
	Partners   []partners.AS2PartnerCerts `json:"partners"`
	Summary    CertSummary                `json:"summary"`
	Thresholds []int                      `json:"thresholdsDays"`
}

func (m *Module) getCertOverview(w http.ResponseWriter, r *http.Request) error {
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ov, err := m.certOverview(r.Context(), tx, tenant, time.Now())
		if err == nil {
			httpx.JSON(w, http.StatusOK, ov)
		}
		return err
	})
}

func (m *Module) certOverview(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, now time.Time) (CertOverview, error) {
	ov := CertOverview{Thresholds: as2.ExpiryThresholds}
	s, err := db.One[AS2Station](ctx, tx, `select `+stationCols+` from as2_station where tenant_id = app_tenant()`)
	switch {
	case err == nil:
		s.assemble(now)
		subject, nb := s.Subject, s.NotBefore
		if subject == "" || nb == nil {
			c, perr := as2.ParseCertPEM(s.CertPEM, time.Time{})
			if perr != nil {
				return ov, perr
			}
			info := as2.Describe(c)
			subject, nb = info.Subject, &info.NotBefore
		}
		sc := &StationCerts{AS2ID: s.AS2ID, ReceiveURL: m.receiveURL(), Current: partners.CertView(subject, *nb, s.NotAfter, s.Fingerprint, now), Active: s.Active, Warnings: []partners.AS2Warning{}}
		sc.Warnings = append(sc.Warnings, partners.CertWarnings(sc.Current, "node certificate")...)
		if s.Next != nil {
			nv := partners.CertView(s.Next.Subject, s.Next.NotBefore, s.Next.NotAfter, s.Next.Fingerprint, now)
			sc.Next, sc.SwitchAt = &nv, &s.Next.SwitchAt
			if s.Active == "current" {
				sc.Warnings = append(sc.Warnings, partners.AS2Warning{Code: "rotation_pending", Severity: "info", Message: "Signing switches to the next certificate on " + s.Next.SwitchAt.Format("2006-01-02 15:04 MST") + "."})
			}
			sc.Warnings = append(sc.Warnings, partners.CertWarnings(nv, "next node certificate")...)
		}
		ov.Station = sc
	case !errors.Is(err, db.ErrNotFound):
		return ov, err
	}
	if ov.Partners, err = partners.AS2Overview(ctx, tx, m.Keys, tenant, now); err != nil {
		return ov, err
	}
	ov.Summary = summarize(ov)
	return ov, nil
}

func summarize(ov CertOverview) CertSummary {
	var sum CertSummary
	count := func(ws []partners.AS2Warning) {
		expired, expiring := false, false
		for _, w := range ws {
			switch {
			case w.Code == "expired":
				expired = true
			case strings.HasPrefix(w.Code, "expiring_"):
				expiring = true
			case w.Code == "legacy":
				sum.Legacy++
			case w.Code == "rotation_pending":
				sum.Rotations++
			}
		}
		if expired {
			sum.Expired++
		} else if expiring {
			sum.Expiring++
		}
	}
	if ov.Station != nil {
		count(ov.Station.Warnings)
	}
	for _, p := range ov.Partners {
		sum.Partners++
		count(p.Warnings)
	}
	return sum
}
