package partners

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/core/secrets"
	"github.com/openspoor/openspoor/pkg/as2"
)

// AS2 certificate lifecycle for partner connections: a rotation flow (a current and a next
// certificate held together, both accepted to verify, encryption switching to the next on a chosen
// date), expiry alerts and the overview shown in the console.

// JobAS2Certs is the schedule that promotes due certificates and raises expiry alerts.
const JobAS2Certs = "partners.as2_certs"

// switchSlack is how far in the past a requested switch date may be (a client asking for "now").
const switchSlack = time.Minute

// Certificate scopes of the alert tracking table.
const (
	ScopePartner     = "partner"
	ScopePartnerNext = "partner_next"
	ScopeStation     = "station"
	ScopeStationNext = "station_next"
)

// SwitchDate resolves a requested switch date: absent means now; a date in the past is refused.
func SwitchDate(req *time.Time, now time.Time) (time.Time, error) {
	if req == nil || req.IsZero() {
		return now, nil
	}
	if req.Before(now.Add(-switchSlack)) {
		return now, httpx.BadRequest("switchAt must not be in the past")
	}
	if req.Before(now) {
		return now, nil
	}
	return req.UTC(), nil
}

func (m *Module) putAS2Next(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		CertPEM  string     `json:"certPem"`
		SwitchAt *time.Time `json:"switchAt"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	cert, err := as2.ParseCertPEM(in.CertPEM, time.Time{})
	if err != nil {
		return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
	}
	now := time.Now()
	switchAt, err := SwitchDate(in.SwitchAt, now)
	if err != nil {
		return err
	}
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		old, certs, err := LoadAS2Certs(r.Context(), tx, m.Keys, tenant, id, now)
		if err != nil {
			return err
		}
		if old.SwitchDue(now) {
			return httpx.Conflict("a rotation is already due; wait for the switch to complete")
		}
		if err := as2.CheckRotation(certs.Current, cert, switchAt, now); err != nil {
			return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
		}
		sealed, err := m.Keys.Seal(r.Context(), tenant, []byte(as2.CertPEM(cert)), as2AAD(id))
		if err != nil {
			return err
		}
		info := as2.Describe(cert)
		if _, err := tx.Exec(r.Context(), `update partner_as2 set next_cert_sealed=$2, next_fingerprint=$3, next_subject=$4, next_not_before=$5, next_not_after=$6, switch_at=$7, updated_at=now() where partner_id=$1`,
			id, sealed, info.Fingerprint, info.Subject, info.NotBefore, info.NotAfter, switchAt); err != nil {
			return err
		}
		if old.Next != nil && old.Next.Fingerprint != info.Fingerprint {
			if err := resolveCertAlerts(r.Context(), tx, old.Next.Fingerprint); err != nil {
				return err
			}
		}
		p, _, err := LoadAS2Certs(r.Context(), tx, m.Keys, tenant, id, now)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.as2.next.set", Entity: "partner", EntityID: id.String(), Before: auditProfile(old), After: auditProfile(p)}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

func (m *Module) deleteAS2Next(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		old, _, err := LoadAS2Certs(r.Context(), tx, m.Keys, tenant, id, time.Now())
		if err != nil {
			return err
		}
		if old.Next == nil {
			return db.ErrNotFound
		}
		if _, err := tx.Exec(r.Context(), `update partner_as2 set next_cert_sealed=null, next_fingerprint=null, next_subject=null, next_not_before=null, next_not_after=null, switch_at=null, updated_at=now() where partner_id=$1`, id); err != nil {
			return err
		}
		if err := resolveCertAlerts(r.Context(), tx, old.Next.Fingerprint); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.as2.next.cancel", Entity: "partner", EntityID: id.String(), Before: auditProfile(old)}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// resolveCertAlerts closes the open expiry alerts of a certificate that was replaced or removed.
func resolveCertAlerts(ctx context.Context, tx pgx.Tx, fingerprint string) error {
	_, err := tx.Exec(ctx, `update alert set status='resolved', resolved_at=now() where kind='dscsa.as2_cert_expiring' and status='open' and detail->>'fingerprint'=$1`, fingerprint)
	return err
}

// ResolveCertAlerts closes the open expiry alerts of a replaced or removed certificate.
func ResolveCertAlerts(ctx context.Context, tx pgx.Tx, fingerprint string) error {
	return resolveCertAlerts(ctx, tx, fingerprint)
}

// RaiseCertAlert raises the alert for a certificate's current expiry stage (60, 30 or 7 days, or
// expired) once per certificate and stage, so a resolved alert does not come back. It reports
// whether an alert was raised. The audit entry is written as the system.
func RaiseCertAlert(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, scope, subjectID, subjectName, fingerprint string, notAfter, now time.Time) (bool, error) {
	stage, days := as2.ExpiryStage(notAfter, now)
	if stage == as2.ExpiryOK {
		return false, nil
	}
	ct, err := tx.Exec(ctx, `insert into as2_cert_alert (tenant_id, scope, subject_id, fingerprint, stage) values (app_tenant(), $1,$2,$3,$4) on conflict do nothing`, scope, subjectID, fingerprint, stage)
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 0 {
		return false, nil
	}
	what := "AS2 certificate of " + subjectName
	switch scope {
	case ScopeStation:
		what = "This node's AS2 certificate"
	case ScopeStationNext:
		what = "This node's next AS2 certificate"
	case ScopePartnerNext:
		what = "Next AS2 certificate of " + subjectName
	}
	severity, title := "warning", fmt.Sprintf("%s expires in %d days (%s)", what, days, notAfter.UTC().Format("2006-01-02"))
	switch stage {
	case as2.ExpiryExpired:
		severity, title = "error", fmt.Sprintf("%s expired on %s", what, notAfter.UTC().Format("2006-01-02"))
	case "7d":
		severity = "error"
	}
	entity, entityID := "partner", subjectID
	if scope == ScopeStation || scope == ScopeStationNext {
		entity = "as2_station"
	}
	detail := map[string]any{"scope": scope, "subjectId": subjectID, "fingerprint": fingerprint, "notAfter": notAfter.UTC(), "daysLeft": days, "stage": stage}
	if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
		values ($1, app_tenant(), 'dscsa.as2_cert_expiring', $2, $3, $4, $5, $6, $7) on conflict do nothing`,
		ids.New(), severity, title, detail, entity, entityID, "as2cert:"+scope+":"+subjectID+":"+fingerprint+":"+stage); err != nil {
		return false, err
	}
	_, err = audit.WriteAs(ctx, tx, tenant, "system", audit.Record{Action: "as2.cert.expiry_alert", Entity: entity, EntityID: entityID, After: detail})
	return true, err
}

// --- scheduled work ---

func (m *Module) certSchedule() kernel.Schedule {
	return kernel.Schedule{Name: JobAS2Certs, Every: 30 * time.Minute, Run: m.as2CertsJob}
}

// as2CertsJob promotes next certificates that reached their switch date and raises expiry alerts
// for every tenant with a partner AS2 profile.
func (m *Module) as2CertsJob(ctx context.Context) error {
	var tenants []uuid.UUID
	if err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct tenant_id from partner_as2`)
		if err != nil {
			return err
		}
		tenants, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return err
	}
	for _, t := range tenants {
		if err := m.DB.Tx(ctx, t, func(tx pgx.Tx) error { return m.as2CertsTenant(ctx, tx, t, time.Now()) }); err != nil {
			return err
		}
	}
	return nil
}

type certRow struct {
	PartnerID   uuid.UUID  `db:"partner_id"`
	Name        string     `db:"name"`
	Fingerprint string     `db:"fingerprint"`
	NotAfter    time.Time  `db:"not_after"`
	NextFP      *string    `db:"next_fingerprint"`
	NextAfter   *time.Time `db:"next_not_after"`
	SwitchAt    *time.Time `db:"switch_at"`
}

func (m *Module) as2CertsTenant(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, now time.Time) error {
	rows, err := db.List[certRow](ctx, tx, `select a.partner_id, p.name, a.fingerprint, a.not_after, a.next_fingerprint, a.next_not_after, a.switch_at
		from partner_as2 a join partner p on p.id = a.partner_id order by p.name, a.partner_id`)
	if err != nil {
		return err
	}
	for _, c := range rows {
		id := c.PartnerID.String()
		if c.NextFP != nil && c.SwitchAt != nil && !c.SwitchAt.After(now) {
			if err := m.promoteAS2(ctx, tx, tenant, c, now); err != nil {
				return err
			}
			c.Fingerprint, c.NotAfter, c.NextFP, c.NextAfter = *c.NextFP, *c.NextAfter, nil, nil
		}
		if _, err := RaiseCertAlert(ctx, tx, tenant, ScopePartner, id, c.Name, c.Fingerprint, c.NotAfter, now); err != nil {
			return err
		}
		if c.NextFP != nil && c.NextAfter != nil {
			if _, err := RaiseCertAlert(ctx, tx, tenant, ScopePartnerNext, id, c.Name, *c.NextFP, *c.NextAfter, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// promoteAS2 makes the next certificate the current one. The certificates are sealed under the
// same key and context, so the sealed value moves as it is.
func (m *Module) promoteAS2(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, c certRow, now time.Time) error {
	if _, err := tx.Exec(ctx, `update partner_as2 set cert_sealed=next_cert_sealed, fingerprint=next_fingerprint, subject=next_subject, not_before=next_not_before, not_after=next_not_after,
			next_cert_sealed=null, next_fingerprint=null, next_subject=null, next_not_before=null, next_not_after=null, switch_at=null, updated_at=now() where partner_id=$1`, c.PartnerID); err != nil {
		return err
	}
	if err := resolveCertAlerts(ctx, tx, c.Fingerprint); err != nil {
		return err
	}
	_, err := audit.WriteAs(ctx, tx, tenant, "system", audit.Record{Action: "partner.as2.next.promote", Entity: "partner", EntityID: c.PartnerID.String(),
		Before: map[string]any{"fingerprint": c.Fingerprint, "notAfter": c.NotAfter},
		After:  map[string]any{"fingerprint": *c.NextFP, "notAfter": *c.NextAfter, "switchAt": c.SwitchAt, "promotedAt": now.UTC()}})
	return err
}

// --- overview ---

// AS2CertView is a certificate as shown in the overview.
type AS2CertView struct {
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Fingerprint string    `json:"fingerprint"`
	DaysLeft    int       `json:"daysLeft"`
	// Stage is ok, 60d, 30d, 7d or expired.
	Stage string `json:"stage"`
}

// AS2Warning is something an administrator should look at.
type AS2Warning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// AS2PartnerCerts is one partner connection in the certificate overview.
type AS2PartnerCerts struct {
	PartnerID uuid.UUID    `json:"partnerId"`
	Name      string       `json:"name"`
	AS2ID     string       `json:"as2Id"`
	Current   AS2CertView  `json:"current"`
	Next      *AS2CertView `json:"next,omitempty"`
	SwitchAt  *time.Time   `json:"switchAt,omitempty"`
	// Active is the certificate in use at now: current, or next once the switch date passed.
	Active   string       `json:"active"`
	Options  AS2Options   `json:"options"`
	Warnings []AS2Warning `json:"warnings"`
}

// CertView builds the overview form of a certificate.
func CertView(subject string, notBefore, notAfter time.Time, fp string, now time.Time) AS2CertView {
	stage, days := as2.ExpiryStage(notAfter, now)
	return AS2CertView{Subject: subject, NotBefore: notBefore, NotAfter: notAfter, Fingerprint: fp, DaysLeft: days, Stage: stage}
}

// CertWarnings turns a certificate's stage into warnings; what names it ("certificate",
// "next certificate").
func CertWarnings(v AS2CertView, what string) []AS2Warning {
	switch v.Stage {
	case as2.ExpiryOK:
		return nil
	case as2.ExpiryExpired:
		return []AS2Warning{{Code: "expired", Severity: "error", Message: fmt.Sprintf("The %s expired on %s.", what, v.NotAfter.Format("2006-01-02"))}}
	}
	sev := "warning"
	if v.Stage == "7d" {
		sev = "error"
	}
	return []AS2Warning{{Code: "expiring_" + v.Stage, Severity: sev, Message: fmt.Sprintf("The %s expires in %d days (%s).", what, v.DaysLeft, v.NotAfter.Format("2006-01-02"))}}
}

// AS2Overview lists the certificates and options of every partner connection with an AS2 profile.
// Rows stored before the subject and notBefore columns existed are read from the sealed
// certificate.
func AS2Overview(ctx context.Context, tx pgx.Tx, keys *secrets.Keyring, tenant uuid.UUID, now time.Time) ([]AS2PartnerCerts, error) {
	type head struct {
		ID   uuid.UUID `db:"id"`
		Name string    `db:"name"`
		AS2  string    `db:"as2_id"`
	}
	hs, err := db.List[head](ctx, tx, `select p.id, p.name, coalesce(p.endpoints->>'as2Id','') as as2_id from partner p join partner_as2 a on a.partner_id = p.id order by p.name, p.id`)
	if err != nil {
		return nil, err
	}
	out := make([]AS2PartnerCerts, 0, len(hs))
	for _, h := range hs {
		p, certs, err := LoadAS2Certs(ctx, tx, keys, tenant, h.ID, now)
		if err != nil {
			return nil, err
		}
		subject, nb := p.Subject, p.NotBefore
		if subject == "" || nb == nil {
			info := as2.Describe(certs.Current)
			subject, nb = info.Subject, &info.NotBefore
		}
		row := AS2PartnerCerts{PartnerID: h.ID, Name: h.Name, AS2ID: h.AS2, Current: CertView(subject, *nb, p.NotAfter, p.Fingerprint, now), Active: "current",
			Options: p.options(), Warnings: []AS2Warning{}}
		row.Warnings = append(row.Warnings, CertWarnings(row.Current, "certificate")...)
		if p.Next != nil {
			nv := CertView(p.Next.Subject, p.Next.NotBefore, p.Next.NotAfter, p.Next.Fingerprint, now)
			row.Next, row.SwitchAt = &nv, &p.Next.SwitchAt
			if p.SwitchDue(now) {
				row.Active = "next"
			} else {
				row.Warnings = append(row.Warnings, AS2Warning{Code: "rotation_pending", Severity: "info", Message: "Switching to the next certificate on " + p.Next.SwitchAt.Format("2006-01-02 15:04 MST") + "."})
			}
			row.Warnings = append(row.Warnings, CertWarnings(nv, "next certificate")...)
		}
		if p.Legacy {
			row.Warnings = append(row.Warnings, AS2Warning{Code: "legacy", Severity: "warning", Message: "Flagged legacy: SHA-1 signatures and receipts are accepted from this partner."})
		}
		out = append(out, row)
	}
	return out, nil
}
