package partners

import (
	"context"
	"crypto/x509"
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

// AS2Profile is the AS2 profile of a partner connection: the partner's certificate (sealed at
// rest) and what the connection requires. Certificates are public, but they are sealed with the
// tenant key like every other connection credential so that deleting the key erases them.
type AS2Profile struct {
	PartnerID   uuid.UUID `json:"partnerId" db:"partner_id"`
	CertPEM     string    `json:"certPem" db:"-"`
	Fingerprint string    `json:"fingerprint" db:"fingerprint"`
	NotAfter    time.Time `json:"notAfter" db:"not_after"`
	// Sign: inbound messages must be signed and outbound ones are. Encrypt: likewise for encryption.
	// Both default to true when a profile is created.
	// TODO(spec): which of signing, encryption and signed receipts DSCSA partners require is not
	// specified in any document we hold; the defaults are the strictest combination.
	Sign    bool `json:"sign" db:"sign"`
	Encrypt bool `json:"encrypt" db:"encrypt"`
	// MDN is the receipt mode requested on outbound messages: sync or async.
	MDN string `json:"mdn" db:"mdn"`
	// MDNURL is where async receipts for messages we receive are delivered; empty means the
	// partner's AS2 URL.
	MDNURL    string    `json:"mdnUrl" db:"mdn_url"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

func as2AAD(id uuid.UUID) []byte { return []byte("as2:partner:" + id.String()) }

const as2Cols = `partner_id, fingerprint, not_after, sign, encrypt, mdn, mdn_url, updated_at`

// LoadAS2 returns a partner's AS2 profile and parsed certificate, or db.ErrNotFound when the
// connection has no AS2 profile. It runs inside the tenant transaction tx.
func LoadAS2(ctx context.Context, tx pgx.Tx, keys *secrets.Keyring, tenant, partner uuid.UUID) (AS2Profile, *x509.Certificate, error) {
	p, err := db.One[AS2Profile](ctx, tx, `select `+as2Cols+` from partner_as2 where partner_id=$1`, partner)
	if err != nil {
		return p, nil, err
	}
	var sealed []byte
	if err := tx.QueryRow(ctx, `select cert_sealed from partner_as2 where partner_id=$1`, partner).Scan(&sealed); err != nil {
		return p, nil, err
	}
	pemBytes, err := keys.Open(ctx, tenant, sealed, as2AAD(partner))
	if err != nil {
		return p, nil, err
	}
	p.CertPEM = string(pemBytes)
	// Validated when stored; expiry is checked at use.
	c, err := as2.ParseCertPEM(p.CertPEM, time.Time{})
	return p, c, err
}

// ByAS2ID returns the active partner whose AS2 identifier is id.
func ByAS2ID(ctx context.Context, tx pgx.Tx, id string) (Partner, error) {
	return db.One[Partner](ctx, tx, `select `+partnerCols+` from partner where endpoints->>'as2Id' = $1 and status='active' order by created_at limit 1`, id)
}

func (m *Module) getAS2(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, _, err := LoadAS2(r.Context(), tx, m.Keys, kernel.TenantOf(r.Context()), id)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

func (m *Module) putAS2(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		CertPEM string `json:"certPem"`
		Sign    *bool  `json:"sign"`
		Encrypt *bool  `json:"encrypt"`
		MDN     string `json:"mdn"`
		MDNURL  string `json:"mdnUrl"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	cert, err := as2.ParseCertPEM(in.CertPEM, time.Now())
	if err != nil {
		return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
	}
	if in.MDN == "" {
		in.MDN = as2.MDNSync
	}
	if in.MDN != as2.MDNSync && in.MDN != as2.MDNAsync {
		return httpx.BadRequest("mdn must be sync or async")
	}
	if in.MDNURL != "" && !strings.HasPrefix(in.MDNURL, "https://") && !strings.HasPrefix(in.MDNURL, "http://") {
		return httpx.BadRequest("mdnUrl must be an http(s) URL")
	}
	sign, enc := in.Sign == nil || *in.Sign, in.Encrypt == nil || *in.Encrypt
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		pt, err := Get(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if pt.Endpoints.AS2ID == "" {
			return httpx.Conflict("set the partner's AS2 identifier (endpoints.as2Id) first")
		}
		sealed, err := m.Keys.Seal(r.Context(), tenant, []byte(as2.CertPEM(cert)), as2AAD(id))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into partner_as2 (tenant_id, partner_id, cert_sealed, fingerprint, not_after, sign, encrypt, mdn, mdn_url)
			values (app_tenant(), $1,$2,$3,$4,$5,$6,$7,$8)
			on conflict (tenant_id, partner_id) do update set cert_sealed=excluded.cert_sealed, fingerprint=excluded.fingerprint, not_after=excluded.not_after,
				sign=excluded.sign, encrypt=excluded.encrypt, mdn=excluded.mdn, mdn_url=excluded.mdn_url, updated_at=now()`,
			id, sealed, as2.Fingerprint(cert), cert.NotAfter, sign, enc, in.MDN, in.MDNURL); err != nil {
			return err
		}
		p, _, err := LoadAS2(r.Context(), tx, m.Keys, tenant, id)
		if err != nil {
			return err
		}
		rec := p
		rec.CertPEM = "" // the audit entry names the certificate by fingerprint
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.as2.set", Entity: "partner", EntityID: id.String(), After: rec}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

func (m *Module) deleteAS2(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ct, err := tx.Exec(r.Context(), `delete from partner_as2 where partner_id=$1`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return db.ErrNotFound
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.as2.delete", Entity: "partner", EntityID: id.String()}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
