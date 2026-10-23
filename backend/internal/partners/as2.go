package partners

import (
	"context"
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
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/core/secrets"
	"github.com/openspoor/openspoor/pkg/as2"
)

// AS2Profile is the AS2 profile of a partner connection: the partner's certificate (sealed at
// rest) and what the connection requires. Certificates are public, but they are sealed with the
// tenant key like every other connection credential so that deleting the key erases them.
type AS2Profile struct {
	PartnerID   uuid.UUID  `json:"partnerId" db:"partner_id"`
	CertPEM     string     `json:"certPem" db:"-"`
	Fingerprint string     `json:"fingerprint" db:"fingerprint"`
	Subject     string     `json:"subject" db:"subject"`
	NotBefore   *time.Time `json:"notBefore,omitempty" db:"not_before"`
	NotAfter    time.Time  `json:"notAfter" db:"not_after"`
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
	MDNURL string `json:"mdnUrl" db:"mdn_url"`
	// Per-partner algorithm options (see AS2Options).
	SignatureAlgorithm    string    `json:"signatureAlgorithm" db:"signature_alg"`
	MDNSignatureAlgorithm string    `json:"mdnSignatureAlgorithm" db:"mdn_signature_alg"`
	Encryption            string    `json:"encryption" db:"encryption"`
	Compress              bool      `json:"compress" db:"compress"`
	Legacy                bool      `json:"legacy" db:"legacy"`
	UpdatedAt             time.Time `json:"updatedAt" db:"updated_at"`

	// The next certificate of a rotation, as stored; Next is the assembled view.
	NextFingerprint *string    `json:"-" db:"next_fingerprint"`
	NextSubject     *string    `json:"-" db:"next_subject"`
	NextNotBefore   *time.Time `json:"-" db:"next_not_before"`
	NextNotAfter    *time.Time `json:"-" db:"next_not_after"`
	SwitchAt        *time.Time `json:"-" db:"switch_at"`
	Next            *AS2Next   `json:"next,omitempty" db:"-"`
}

// AS2Next is a partner's next certificate, held alongside the current one during a rotation. Both
// verify the partner's signatures; from SwitchAt we encrypt to the next one.
type AS2Next struct {
	CertPEM     string    `json:"certPem"`
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	SwitchAt    time.Time `json:"switchAt"`
}

// SwitchDue reports whether the next certificate has taken over at now.
func (p AS2Profile) SwitchDue(now time.Time) bool {
	return p.SwitchAt != nil && !p.SwitchAt.After(now)
}

// AS2Options are the per-partner algorithm choices.
// TODO(spec): the digests, ciphers and compression that DSCSA trading partners require are not
// defined in any document we hold; the defaults (SHA-256, AES-256, no compression, signed
// receipts) and the choices offered follow common public AS2 practice and stay configurable.
type AS2Options struct {
	// SignatureAlgorithm signs outbound messages and sets the integrity-check digest we ask the
	// receipt to carry; inbound signatures must be at least as strong. sha-256 (default), sha-384,
	// sha-512, or sha-1 (only with Legacy).
	SignatureAlgorithm string `json:"signatureAlgorithm"`
	// MDNSignatureAlgorithm signs the receipts we send to this partner (same choices).
	MDNSignatureAlgorithm string `json:"mdnSignatureAlgorithm"`
	// Encryption is the content cipher: aes-256-cbc (default) or aes-128-cbc.
	Encryption string `json:"encryption"`
	// Compress compresses outbound payloads (RFC 5402 style) and accepts compressed inbound ones.
	Compress bool `json:"compress"`
	// Legacy is the explicit flag that admits SHA-1: for signatures and receipts to and from this
	// partner. It is audited and shown as a warning in the certificate overview.
	Legacy bool `json:"legacy"`
}

// DefaultAS2Options are the options of a new profile.
func DefaultAS2Options() AS2Options {
	return AS2Options{SignatureAlgorithm: as2.DigestSHA256, MDNSignatureAlgorithm: as2.DigestSHA256, Encryption: as2.EncAES256}
}

func (p AS2Profile) options() AS2Options {
	return AS2Options{p.SignatureAlgorithm, p.MDNSignatureAlgorithm, p.Encryption, p.Compress, p.Legacy}
}

// as2OptionsInput is the request form of AS2Options: an absent field keeps the current value.
type as2OptionsInput struct {
	SignatureAlgorithm    string `json:"signatureAlgorithm"`
	MDNSignatureAlgorithm string `json:"mdnSignatureAlgorithm"`
	Encryption            string `json:"encryption"`
	Compress              *bool  `json:"compress"`
	Legacy                *bool  `json:"legacy"`
}

// apply merges the input into base and validates the result.
func (in as2OptionsInput) apply(base AS2Options) (AS2Options, error) {
	out := base
	for _, f := range []struct {
		in  string
		dst *string
		ok  func(string) bool
		msg string
	}{
		{in.SignatureAlgorithm, &out.SignatureAlgorithm, as2.ValidDigest, "signatureAlgorithm must be sha-256, sha-384, sha-512 or sha-1"},
		{in.MDNSignatureAlgorithm, &out.MDNSignatureAlgorithm, as2.ValidDigest, "mdnSignatureAlgorithm must be sha-256, sha-384, sha-512 or sha-1"},
		{in.Encryption, &out.Encryption, as2.ValidEncryption, "encryption must be aes-256-cbc or aes-128-cbc"},
	} {
		if f.in == "" {
			continue
		}
		if !f.ok(f.in) {
			return out, httpx.BadRequest(f.msg)
		}
		*f.dst = f.in
	}
	if in.Compress != nil {
		out.Compress = *in.Compress
	}
	if in.Legacy != nil {
		out.Legacy = *in.Legacy
	}
	if !out.Legacy && (out.SignatureAlgorithm == as2.DigestSHA1 || out.MDNSignatureAlgorithm == as2.DigestSHA1) {
		return out, httpx.BadRequest("sha-1 is only available for a partner explicitly flagged legacy")
	}
	return out, nil
}

func as2AAD(id uuid.UUID) []byte { return []byte("as2:partner:" + id.String()) }

const as2Cols = `partner_id, fingerprint, subject, not_before, not_after, sign, encrypt, mdn, mdn_url, signature_alg, mdn_signature_alg, encryption, compress, legacy, updated_at,
	next_fingerprint, next_subject, next_not_before, next_not_after, switch_at`

// AS2Certs are the certificates of a connection at one moment: Active encrypts and is tried first
// to verify, Other (if any) is also accepted to verify.
type AS2Certs struct {
	Current, Next *x509.Certificate
	Active, Other *x509.Certificate
}

// Peer is the connection as the AS2 layer needs it.
func (c AS2Certs) Peer(as2ID string) as2.Peer {
	return as2.Peer{ID: as2ID, Cert: c.Active, Other: c.Other}
}

// LoadAS2 returns a partner's AS2 profile and the certificate in use now, or db.ErrNotFound when
// the connection has no AS2 profile. It runs inside the tenant transaction tx.
func LoadAS2(ctx context.Context, tx pgx.Tx, keys *secrets.Keyring, tenant, partner uuid.UUID) (AS2Profile, *x509.Certificate, error) {
	p, c, err := LoadAS2Certs(ctx, tx, keys, tenant, partner, time.Now())
	return p, c.Active, err
}

// LoadAS2Certs is LoadAS2 with both certificates of a rotation. Expiry is not checked here.
func LoadAS2Certs(ctx context.Context, tx pgx.Tx, keys *secrets.Keyring, tenant, partner uuid.UUID, now time.Time) (AS2Profile, AS2Certs, error) {
	var cs AS2Certs
	p, err := db.One[AS2Profile](ctx, tx, `select `+as2Cols+` from partner_as2 where partner_id=$1`, partner)
	if err != nil {
		return p, cs, err
	}
	var sealed, nextSealed []byte
	if err := tx.QueryRow(ctx, `select cert_sealed, next_cert_sealed from partner_as2 where partner_id=$1`, partner).Scan(&sealed, &nextSealed); err != nil {
		return p, cs, err
	}
	pemBytes, err := keys.Open(ctx, tenant, sealed, as2AAD(partner))
	if err != nil {
		return p, cs, err
	}
	p.CertPEM = string(pemBytes)
	// Validated when stored; expiry is checked at use.
	if cs.Current, err = as2.ParseCertPEM(p.CertPEM, time.Time{}); err != nil {
		return p, cs, err
	}
	cs.Active = cs.Current
	if nextSealed != nil && p.NextFingerprint != nil && p.NextSubject != nil && p.NextNotBefore != nil && p.NextNotAfter != nil && p.SwitchAt != nil {
		np, err := keys.Open(ctx, tenant, nextSealed, as2AAD(partner))
		if err != nil {
			return p, cs, err
		}
		if cs.Next, err = as2.ParseCertPEM(string(np), time.Time{}); err != nil {
			return p, cs, err
		}
		p.Next = &AS2Next{CertPEM: string(np), Fingerprint: *p.NextFingerprint, Subject: *p.NextSubject, NotBefore: *p.NextNotBefore, NotAfter: *p.NextNotAfter, SwitchAt: *p.SwitchAt}
		cs.Active, cs.Other = cs.Current, cs.Next
		if p.SwitchDue(now) {
			cs.Active, cs.Other = cs.Next, cs.Current
		}
	}
	return p, cs, nil
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
		p, _, err := LoadAS2Certs(r.Context(), tx, m.Keys, kernel.TenantOf(r.Context()), id, time.Now())
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

// auditProfile is what an audit entry records of a profile: the certificates by fingerprint only.
func auditProfile(p AS2Profile) map[string]any {
	out := map[string]any{"partnerId": p.PartnerID, "fingerprint": p.Fingerprint, "subject": p.Subject, "notAfter": p.NotAfter, "sign": p.Sign, "encrypt": p.Encrypt,
		"mdn": p.MDN, "mdnUrl": p.MDNURL, "options": p.options()}
	if p.Next != nil {
		out["next"] = map[string]any{"fingerprint": p.Next.Fingerprint, "notAfter": p.Next.NotAfter, "switchAt": p.Next.SwitchAt}
	}
	return out
}

// legacyChanged records an explicit change of the legacy flag: an audit entry, and an alert while
// it is set.
func (m *Module) legacyChanged(ctx context.Context, tx pgx.Tx, pt Partner, from, to AS2Options) error {
	if from.Legacy == to.Legacy {
		return nil
	}
	if _, err := audit.Write(ctx, tx, audit.Record{Action: "partner.as2.legacy.set", Entity: "partner", EntityID: pt.ID.String(), Before: from, After: to}); err != nil {
		return err
	}
	key := "as2legacy:" + pt.ID.String()
	if !to.Legacy {
		_, err := tx.Exec(ctx, `update alert set status='resolved', resolved_at=now() where kind='dscsa.as2_legacy' and dedupe_key=$1 and status='open'`, key)
		return err
	}
	_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
		values ($1, app_tenant(), 'dscsa.as2_legacy', 'warning', $2, $3, 'partner', $4, $5) on conflict do nothing`,
		ids.New(), "AS2 partner "+pt.Name+" is flagged legacy (SHA-1 accepted)", map[string]any{"partnerId": pt.ID, "options": to}, pt.ID.String(), key)
	return err
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
		as2OptionsInput
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
		// Options not in the request keep their current values (defaults for a new profile).
		old, oldErr := db.One[AS2Profile](r.Context(), tx, `select `+as2Cols+` from partner_as2 where partner_id=$1`, id)
		if oldErr != nil && !errors.Is(oldErr, db.ErrNotFound) {
			return oldErr
		}
		base := DefaultAS2Options()
		if oldErr == nil {
			base = old.options()
		}
		opts, err := in.as2OptionsInput.apply(base)
		if err != nil {
			return err
		}
		sealed, err := m.Keys.Seal(r.Context(), tenant, []byte(as2.CertPEM(cert)), as2AAD(id))
		if err != nil {
			return err
		}
		info := as2.Describe(cert)
		if _, err := tx.Exec(r.Context(), `insert into partner_as2 (tenant_id, partner_id, cert_sealed, fingerprint, subject, not_before, not_after, sign, encrypt, mdn, mdn_url,
				signature_alg, mdn_signature_alg, encryption, compress, legacy)
			values (app_tenant(), $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			on conflict (tenant_id, partner_id) do update set cert_sealed=excluded.cert_sealed, fingerprint=excluded.fingerprint, subject=excluded.subject,
				not_before=excluded.not_before, not_after=excluded.not_after, sign=excluded.sign, encrypt=excluded.encrypt, mdn=excluded.mdn, mdn_url=excluded.mdn_url,
				signature_alg=excluded.signature_alg, mdn_signature_alg=excluded.mdn_signature_alg, encryption=excluded.encryption, compress=excluded.compress,
				legacy=excluded.legacy, updated_at=now()`,
			id, sealed, info.Fingerprint, info.Subject, info.NotBefore, info.NotAfter, sign, enc, in.MDN, in.MDNURL,
			opts.SignatureAlgorithm, opts.MDNSignatureAlgorithm, opts.Encryption, opts.Compress, opts.Legacy); err != nil {
			return err
		}
		if oldErr == nil && old.Fingerprint != info.Fingerprint {
			if err := resolveCertAlerts(r.Context(), tx, old.Fingerprint); err != nil {
				return err
			}
		}
		p, _, err := LoadAS2Certs(r.Context(), tx, m.Keys, tenant, id, time.Now())
		if err != nil {
			return err
		}
		rec := audit.Record{Action: "partner.as2.set", Entity: "partner", EntityID: id.String(), After: auditProfile(p)}
		if oldErr == nil {
			rec.Before = auditProfile(old)
		}
		if _, err := audit.Write(r.Context(), tx, rec); err != nil {
			return err
		}
		if err := m.legacyChanged(r.Context(), tx, pt, base, opts); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

// putAS2Options changes the algorithm options of an existing profile without touching its
// certificates. An absent field keeps its current value.
func (m *Module) putAS2Options(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in as2OptionsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		old, _, err := LoadAS2Certs(r.Context(), tx, m.Keys, tenant, id, time.Now())
		if err != nil {
			return err
		}
		opts, err := in.apply(old.options())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `update partner_as2 set signature_alg=$2, mdn_signature_alg=$3, encryption=$4, compress=$5, legacy=$6, updated_at=now() where partner_id=$1`,
			id, opts.SignatureAlgorithm, opts.MDNSignatureAlgorithm, opts.Encryption, opts.Compress, opts.Legacy); err != nil {
			return err
		}
		pt, err := Get(r.Context(), tx, id)
		if err != nil {
			return err
		}
		p, _, err := LoadAS2Certs(r.Context(), tx, m.Keys, tenant, id, time.Now())
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.as2.options.set", Entity: "partner", EntityID: id.String(), Before: old.options(), After: opts}); err != nil {
			return err
		}
		if err := m.legacyChanged(r.Context(), tx, pt, old.options(), opts); err != nil {
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
		var legacy bool
		if err := tx.QueryRow(r.Context(), `select legacy from partner_as2 where partner_id=$1`, id).Scan(&legacy); err != nil {
			return db.MapErr(err)
		}
		if _, err := tx.Exec(r.Context(), `delete from partner_as2 where partner_id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `update alert set status='resolved', resolved_at=now() where kind in ('dscsa.as2_legacy','dscsa.as2_cert_expiring') and entity='partner' and entity_id=$1 and status='open'`, id.String()); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.as2.delete", Entity: "partner", EntityID: id.String(), After: map[string]any{"legacy": legacy}}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
