package dscsa

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/pkg/as2"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// AS2 (RFC 4130) transport, in process (ADR-007): S/MIME signing and encryption and MDN receipts
// come from pkg/as2; this file adds the node's identity, the partner profiles, the Message-ID
// idempotency guard, audit entries and message-log rows (as2_message).

// JobAS2MDN delivers an asynchronous receipt to the sender.
const JobAS2MDN = "dscsa.as2.mdn"

const (
	// as2StaleAfter is how long a message may stay "processing" before a resend takes it over
	// (the first attempt is assumed to have crashed).
	as2StaleAfter = 2 * time.Minute
	// as2CertValidity is the lifetime of a generated station certificate.
	as2CertValidity = 2 * 365 * 24 * time.Hour
	// as2ContentType is the payload media type of a TI/TS over AS2 (the EPCIS 2.0 JSON-LD document).
	// TODO(spec): the media type, file naming and document shape (single EPCIS document, or the
	// GS1 US DSCSA envelope) that DSCSA trading partners expect over AS2 are not specified in any
	// document we hold.
	as2ContentType = "application/ld+json"
)

// AS2Station is the node's AS2 identity as shown to administrators (never the private key).
type AS2Station struct {
	AS2ID       string     `json:"as2Id" db:"as2_id"`
	CertPEM     string     `json:"certPem" db:"cert_pem"`
	Fingerprint string     `json:"fingerprint" db:"fingerprint"`
	Subject     string     `json:"subject" db:"subject"`
	NotBefore   *time.Time `json:"notBefore,omitempty" db:"not_before"`
	NotAfter    time.Time  `json:"notAfter" db:"not_after"`
	ReceiveURL  string     `json:"receiveUrl" db:"-"`
	UpdatedAt   time.Time  `json:"updatedAt" db:"updated_at"`
	// The next certificate of a rotation as stored (the key is never returned); Next is the view.
	NextCertPEM     *string      `json:"-" db:"next_cert_pem"`
	NextFingerprint *string      `json:"-" db:"next_fingerprint"`
	NextSubject     *string      `json:"-" db:"next_subject"`
	NextNotBefore   *time.Time   `json:"-" db:"next_not_before"`
	NextNotAfter    *time.Time   `json:"-" db:"next_not_after"`
	SwitchAt        *time.Time   `json:"-" db:"switch_at"`
	Next            *StationNext `json:"next,omitempty" db:"-"`
	// Active is the certificate that signs now: current, or next once the switch date passed.
	Active string `json:"active" db:"-"`
}

// StationNext is the node's next certificate, held alongside the current one during a rotation.
// Both decrypt; signing switches to the next one at SwitchAt.
type StationNext struct {
	CertPEM     string    `json:"certPem"`
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	SwitchAt    time.Time `json:"switchAt"`
}

const stationCols = `as2_id, cert_pem, fingerprint, subject, not_before, not_after, updated_at, next_cert_pem, next_fingerprint, next_subject, next_not_before, next_not_after, switch_at`

// assemble fills Next and Active from the stored columns.
func (s *AS2Station) assemble(now time.Time) {
	s.Active = "current"
	if s.NextCertPEM == nil || s.NextFingerprint == nil || s.NextSubject == nil || s.NextNotBefore == nil || s.NextNotAfter == nil || s.SwitchAt == nil {
		return
	}
	s.Next = &StationNext{CertPEM: *s.NextCertPEM, Fingerprint: *s.NextFingerprint, Subject: *s.NextSubject, NotBefore: *s.NextNotBefore, NotAfter: *s.NextNotAfter, SwitchAt: *s.SwitchAt}
	if !s.SwitchAt.After(now) {
		s.Active = "next"
	}
}

func stationAAD(tenant uuid.UUID) []byte { return []byte("as2:station:" + tenant.String()) }

func (m *Module) receiveURL() string {
	return strings.TrimRight(m.Cfg.PublicURL, "/") + "/api/v1/as2/receive"
}

// loadStation returns the tenant's AS2 identity with its private key, or db.ErrNotFound. During a
// rotation it holds both certificates: the one that signs now (the next one once its switch date
// passed) and the other, which still decrypts.
func (m *Module) loadStation(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (as2.Station, error) {
	var id, certPEM string
	var sealed, nextSealed []byte
	var nextPEM *string
	var switchAt *time.Time
	if err := tx.QueryRow(ctx, `select as2_id, cert_pem, key_sealed, next_cert_pem, next_key_sealed, switch_at from as2_station where tenant_id=$1`, tenant).Scan(&id, &certPEM, &sealed, &nextPEM, &nextSealed, &switchAt); err != nil {
		return as2.Station{}, db.MapErr(err)
	}
	cur, err := m.openIdentity(ctx, tenant, certPEM, sealed)
	if err != nil {
		return as2.Station{}, err
	}
	st := as2.Station{ID: id, Cert: cur.cert, Key: cur.key}
	if nextPEM != nil && nextSealed != nil && switchAt != nil {
		next, err := m.openIdentity(ctx, tenant, *nextPEM, nextSealed)
		if err != nil {
			return as2.Station{}, err
		}
		st.Other, st.OtherKey = next.cert, next.key
		if !switchAt.After(time.Now()) {
			st = as2.Station{ID: id, Cert: next.cert, Key: next.key, Other: cur.cert, OtherKey: cur.key}
		}
	}
	return st, nil
}

type identity struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
}

func (m *Module) openIdentity(ctx context.Context, tenant uuid.UUID, certPEM string, sealed []byte) (identity, error) {
	keyPEM, err := m.Keys.Open(ctx, tenant, sealed, stationAAD(tenant))
	if err != nil {
		return identity{}, err
	}
	cert, err := as2.ParseCertPEM(certPEM, time.Time{})
	if err != nil {
		return identity{}, err
	}
	key, err := as2.ParseKeyPEM(string(keyPEM))
	if err != nil {
		return identity{}, err
	}
	return identity{cert, key}, nil
}

func (m *Module) getStation(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := db.One[AS2Station](r.Context(), tx, `select `+stationCols+` from as2_station where tenant_id = app_tenant()`)
		if err != nil {
			return err
		}
		s.ReceiveURL = m.receiveURL()
		s.assemble(time.Now())
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

func (m *Module) putStation(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		AS2ID    string `json:"as2Id"`
		CertPEM  string `json:"certPem"`
		KeyPEM   string `json:"keyPem"`
		Generate bool   `json:"generate"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !as2.ValidID(in.AS2ID) {
		return httpx.BadRequest("as2Id must be 1-128 characters of letters, digits and . _ : @ -")
	}
	now := time.Now()
	var key *rsa.PrivateKey
	var certPEM string
	var info as2.CertInfo
	if in.Generate {
		c, k, err := as2.GenerateStation(in.AS2ID, as2CertValidity, now)
		if err != nil {
			return err
		}
		key, certPEM, info = k, as2.CertPEM(c), as2.Describe(c)
	} else {
		c, err := as2.ParseCertPEM(in.CertPEM, now)
		if err != nil {
			return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
		}
		if key, err = as2.ParseKeyPEM(in.KeyPEM); err != nil {
			return httpx.BadRequest(strings.TrimPrefix(err.Error(), "as2: "))
		}
		if pub, ok := c.PublicKey.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
			return httpx.BadRequest("the private key does not match the certificate")
		}
		certPEM, info = as2.CertPEM(c), as2.Describe(c)
	}
	keyPEM, err := as2.EncodeKeyPEM(key)
	if err != nil {
		return err
	}
	tenant := kernel.TenantOf(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		sealed, err := m.Keys.Seal(r.Context(), tenant, keyPEM, stationAAD(tenant))
		if err != nil {
			return err
		}
		var oldFP string
		err = tx.QueryRow(r.Context(), `select fingerprint from as2_station where tenant_id=$1`, tenant).Scan(&oldFP)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		s, err := db.One[AS2Station](r.Context(), tx, `insert into as2_station (tenant_id, as2_id, cert_pem, key_sealed, fingerprint, subject, not_before, not_after) values (app_tenant(), $1,$2,$3,$4,$5,$6,$7)
			on conflict (tenant_id) do update set as2_id=excluded.as2_id, cert_pem=excluded.cert_pem, key_sealed=excluded.key_sealed, fingerprint=excluded.fingerprint,
				subject=excluded.subject, not_before=excluded.not_before, not_after=excluded.not_after, updated_at=now()
			returning `+stationCols, in.AS2ID, certPEM, sealed, info.Fingerprint, info.Subject, info.NotBefore, info.NotAfter)
		if err != nil {
			if errors.Is(err, db.ErrConflict) {
				return httpx.Conflict("that AS2 identifier is used by another station on this node")
			}
			return err
		}
		s.ReceiveURL = m.receiveURL()
		s.assemble(time.Now())
		if oldFP != "" && oldFP != s.Fingerprint {
			if err := partners.ResolveCertAlerts(r.Context(), tx, oldFP); err != nil {
				return err
			}
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "as2.station.set", Entity: "as2_station", EntityID: tenant.String(),
			After: map[string]any{"as2Id": s.AS2ID, "fingerprint": s.Fingerprint, "subject": s.Subject, "notBefore": info.NotBefore, "notAfter": s.NotAfter, "generated": in.Generate}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

// --- outbound ---

// AS2Transport sends TI/TS as a signed (and optionally encrypted) AS2 message and checks the
// partner's signed receipt. The zero value (no module) refuses.
type AS2Transport struct{ M *Module }

// Send implements Transport.
func (t AS2Transport) Send(ctx context.Context, from uuid.UUID, p partners.Partner, s Shipment, doc []byte) (map[string]any, error) {
	m := t.M
	if m == nil {
		return nil, httpx.Conflict("AS2 is not configured on this installation")
	}
	if p.Endpoints.AS2URL == "" || p.Endpoints.AS2ID == "" {
		return nil, httpx.Conflict("partner has no AS2 URL and AS2 identifier")
	}
	var st as2.Station
	var prof partners.AS2Profile
	var certs partners.AS2Certs
	err := m.DB.Tx(ctx, from, func(tx pgx.Tx) error {
		var err error
		if st, err = m.loadStation(ctx, tx, from); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return httpx.Conflict("AS2 is not configured: set this node's AS2 station first")
			}
			return err
		}
		if prof, certs, err = partners.LoadAS2Certs(ctx, tx, m.Keys, from, p.ID, time.Now()); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return httpx.Conflict("AS2 is not configured: the partner connection has no AS2 profile")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	peer := certs.Peer(p.Endpoints.AS2ID)
	if now.After(peer.Cert.NotAfter) || now.After(st.Cert.NotAfter) {
		return nil, httpx.Conflict("AS2 certificate has expired")
	}
	msgID := as2.NewMessageID(st.ID)
	out := as2.Outgoing{MessageID: msgID, Subject: "TI/TS " + s.PONumber, Filename: "tits-" + s.ID.String() + ".json", ContentType: as2ContentType, Payload: doc,
		Sign: prof.Sign, Encrypt: prof.Encrypt, MDN: prof.MDN, Now: now,
		Digest: prof.SignatureAlgorithm, Encryption: prof.Encryption, Compress: prof.Compress, Legacy: prof.Legacy}
	if prof.MDN == as2.MDNAsync {
		out.AsyncURL = m.receiveURL()
	}
	wire, err := as2.Build(st, peer, out)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(doc)
	rowID := ids.New()
	// The row exists before the message leaves: an async receipt can arrive before Post returns.
	if err := m.DB.Tx(ctx, from, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into as2_message (id, tenant_id, partner_id, direction, message_id, as2_from, as2_to, subject, status, signed, encrypted, mdn_mode, mic, mic_alg, payload_sha256, shipment_id)
			values ($1, app_tenant(), $2, 'outbound', $3, $4, $5, $6, 'mdn_pending', $7, $8, $9, $10, $11, $12, $13)`,
			rowID, p.ID, msgID, st.ID, p.Endpoints.AS2ID, wire.Header["Subject"], prof.Sign, prof.Encrypt, prof.MDN, wire.MIC, prof.SignatureAlgorithm, hex.EncodeToString(sum[:]), s.ID)
		return err
	}); err != nil {
		return nil, err
	}

	resp := map[string]any{"transport": "as2", "messageId": msgID, "signed": prof.Sign, "encrypted": prof.Encrypt, "mdnMode": prof.MDN, "signatureAlgorithm": prof.SignatureAlgorithm}
	status, sendErr := "mdn_pending", error(nil)
	disposition := ""
	code, h, body, postErr := as2.Post(ctx, m.Client, p.Endpoints.AS2URL, wire)
	switch {
	case postErr != nil:
		status, sendErr = "failed", postErr
	case code < 200 || code >= 300:
		status, sendErr = "failed", fmt.Errorf("partner returned HTTP %d", code)
		resp["status"] = code
	case prof.MDN == as2.MDNAsync:
		resp["status"], resp["mdn"] = code, "pending"
	default:
		resp["status"] = code
		mdn, err := as2.ParseMDNMin(h, body, peer, true, time.Now(), receiptFloor(prof), prof.Legacy)
		if err == nil {
			disposition = mdn.Disposition
			err = checkMDN(mdn, msgID, wire.MIC, prof.SignatureAlgorithm, p.Endpoints.AS2ID)
		}
		if err != nil {
			status, sendErr = "failed", err
		} else {
			status = "delivered"
			resp["mdn"] = "processed"
		}
	}
	errText := ""
	if sendErr != nil {
		errText = sendErr.Error()
		resp["mdn"] = "failed"
	}
	if err := m.DB.Tx(ctx, from, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update as2_message set status=$2, error=$3, disposition=$4, updated_at=now() where id=$1`, rowID, status, errText, disposition); err != nil {
			return err
		}
		_, err := audit.Write(ctx, tx, audit.Record{Action: "as2.send", Entity: "as2_message", EntityID: rowID.String(),
			After: map[string]any{"messageId": msgID, "to": p.Endpoints.AS2ID, "shipmentId": s.ID, "status": status, "signed": prof.Sign, "encrypted": prof.Encrypt, "mdn": prof.MDN, "digest": prof.SignatureAlgorithm, "cipher": prof.Encryption, "compressed": prof.Compress, "legacy": prof.Legacy, "error": errText}})
		return err
	}); err != nil {
		return resp, err
	}
	return resp, sendErr
}

// receiptFloor is the weakest signature digest accepted on a partner's receipts: SHA-256, or
// SHA-1 for a partner flagged legacy.
func receiptFloor(prof partners.AS2Profile) string {
	if prof.Legacy {
		return as2.DigestSHA1
	}
	return as2.DigestSHA256
}

// inboundPolicy is what a connection demands of messages it sends us: signatures at least as strong
// as the partner's configured digest (any digest for a partner flagged legacy), AES-256 or the
// configured cipher, compression only when enabled.
func inboundPolicy(prof partners.AS2Profile) as2.Policy {
	min := prof.SignatureAlgorithm
	if prof.Legacy {
		min = as2.DigestSHA1
	}
	return as2.Policy{RequireSigned: prof.Sign, RequireEncrypted: prof.Encrypt, MinDigest: min, MinEncryption: prof.Encryption, Legacy: prof.Legacy, AllowCompressed: prof.Compress}
}

// checkMDN validates a receipt against the message it answers; micAlg is the digest the message's
// integrity check was computed with.
func checkMDN(mdn *as2.MDN, messageID, mic, micAlg, peerID string) error {
	switch {
	case mdn.OriginalMessageID != messageID:
		return errors.New("receipt answers a different message")
	case !strings.EqualFold(mdn.From, peerID):
		return errors.New("receipt is from a different AS2 identifier")
	case !mdn.Processed:
		reason := mdn.ErrorCode
		if reason == "" {
			reason = mdn.Disposition
		}
		return errors.New("partner did not accept the message: " + reason)
	case mdn.MIC == "" || mdn.MICAlg != micAlg:
		return errors.New("receipt carries no " + strings.ToUpper(micAlg) + " message integrity check")
	case mdn.MIC != mic:
		return errors.New("receipt message integrity check does not match what was sent")
	}
	return nil
}

// --- inbound ---

func as2Text(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n")) //nolint:gosec // G705: fixed text/plain diagnostics with nosniff, never HTML
}

func writeWire(w http.ResponseWriter, wire *as2.Wire) {
	for k, v := range wire.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(wire.Body) //nolint:gosec // G705: MIME receipt with an explicit non-HTML content type, nosniff set
}

// as2Receive is the public AS2 endpoint: it accepts messages and asynchronous receipts. Senders
// are identified by AS2-From and authenticated by their pinned certificate, not by an API key.
func (m *Module) as2Receive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, as2.MaxBody))
	if err != nil {
		as2Text(w, http.StatusRequestEntityTooLarge, "message too large")
		return
	}
	hd, err := as2.ParseHeader(r.Header)
	if err != nil {
		// A receipt carries no Disposition-Notification-To but the same identifiers; both need them.
		as2Text(w, http.StatusBadRequest, err.Error())
		return
	}
	var tenant uuid.UUID
	if err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select tenant_id from as2_station where lower(as2_id) = lower($1)`, hd.To).Scan(&tenant)
	}); err != nil {
		as2Text(w, http.StatusNotFound, "unknown AS2-To")
		return
	}
	ctx = auth.With(ctx, auth.System(tenant))
	var (
		st      as2.Station
		partner partners.Partner
		prof    partners.AS2Profile
		certs   partners.AS2Certs
	)
	err = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		if st, err = m.loadStation(ctx, tx, tenant); err != nil {
			return err
		}
		if partner, err = partners.ByAS2ID(ctx, tx, hd.From); err != nil {
			return err
		}
		prof, certs, err = partners.LoadAS2Certs(ctx, tx, m.Keys, tenant, partner.ID, time.Now())
		return err
	})
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Unknown sender or no AS2 profile: nothing to authenticate against, so no receipt.
			as2Text(w, http.StatusForbidden, "sender is not a connected AS2 partner")
			return
		}
		as2Text(w, http.StatusInternalServerError, "temporarily unavailable")
		return
	}
	peer := certs.Peer(partner.Endpoints.AS2ID)
	if as2.IsMDN(r.Header, body) {
		m.as2ReceiveMDN(ctx, w, r, tenant, partner, prof, peer, hd, body)
		return
	}
	m.as2ReceiveMessage(ctx, w, r, tenant, st, partner, prof, peer, hd, body)
}

// as2ReceiveMDN settles an outbound message from an asynchronous receipt.
func (m *Module) as2ReceiveMDN(ctx context.Context, w http.ResponseWriter, r *http.Request, tenant uuid.UUID, partner partners.Partner, prof partners.AS2Profile, peer as2.Peer, hd *as2.Header, body []byte) {
	actor := "as2:" + partner.Endpoints.AS2ID
	mdn, perr := as2.ParseMDNMin(r.Header, body, peer, true, time.Now(), receiptFloor(prof), prof.Legacy)
	if perr != nil {
		_ = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
			_, err := audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "as2.mdn.rejected", Entity: "partner", EntityID: partner.ID.String(), After: map[string]any{"messageId": hd.MessageID, "error": perr.Error()}})
			return err
		})
		as2Text(w, http.StatusBadRequest, "receipt rejected: "+perr.Error())
		return
	}
	status := http.StatusOK
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		var rowID uuid.UUID
		var mic, micAlg, cur string
		var shipID *uuid.UUID
		err := tx.QueryRow(ctx, `select id, mic, mic_alg, status, shipment_id from as2_message where tenant_id=$1 and direction='outbound' and message_id=$2 and partner_id=$3`,
			tenant, mdn.OriginalMessageID, partner.ID).Scan(&rowID, &mic, &micAlg, &cur, &shipID)
		if errors.Is(err, pgx.ErrNoRows) {
			status = http.StatusNotFound
			return nil
		}
		if err != nil {
			return err
		}
		if cur != "mdn_pending" { // a repeated receipt is acknowledged without changing anything
			return nil
		}
		checkErr := checkMDN(mdn, mdn.OriginalMessageID, mic, micAlg, partner.Endpoints.AS2ID)
		to, errText := "delivered", ""
		if checkErr != nil {
			to, errText = "failed", checkErr.Error()
		}
		if _, err := tx.Exec(ctx, `update as2_message set status=$2, error=$3, disposition=$4, updated_at=now() where id=$1`, rowID, to, errText, mdn.Disposition); err != nil {
			return err
		}
		if shipID != nil {
			if err := settleShipment(ctx, tx, *shipID, partner.Name, to, errText); err != nil {
				return err
			}
		}
		_, err = audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "as2.mdn.receive", Entity: "as2_message", EntityID: rowID.String(),
			After: map[string]any{"messageId": mdn.OriginalMessageID, "status": to, "disposition": mdn.Disposition, "error": errText}})
		return err
	})
	switch {
	case err != nil:
		as2Text(w, http.StatusInternalServerError, "temporarily unavailable")
	case status == http.StatusNotFound:
		as2Text(w, status, "no message awaiting this receipt")
	default:
		as2Text(w, status, "receipt accepted")
	}
}

// settleShipment records the outcome of an asynchronous receipt on the shipment.
func settleShipment(ctx context.Context, tx pgx.Tx, id uuid.UUID, partnerName, to, errText string) error {
	if to == "delivered" {
		_, err := tx.Exec(ctx, `update shipment set response = coalesce(response, '{}'::jsonb) || '{"mdn":"processed"}'::jsonb where id=$1`, id)
		return err
	}
	if _, err := tx.Exec(ctx, `update shipment set status='send_failed', response = coalesce(response, '{}'::jsonb) || jsonb_build_object('mdn', 'failed', 'error', $2::text) where id=$1`, id, errText); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id) values ($1, app_tenant(), 'dscsa.send_failed', 'error', $2, $3, 'shipment', $4)`,
		ids.New(), "TI/TS delivery to "+partnerName+" failed", map[string]any{"error": errText, "shipmentId": id}, id.String())
	return err
}

// as2ReceiveMessage authenticates, de-duplicates and processes one inbound TI/TS message and
// answers with a signed receipt (inline, or queued for async).
func (m *Module) as2ReceiveMessage(ctx context.Context, w http.ResponseWriter, r *http.Request, tenant uuid.UUID, st as2.Station, partner partners.Partner, prof partners.AS2Profile, peer as2.Peer, hd *as2.Header, body []byte) {
	actor := "as2:" + partner.Endpoints.AS2ID
	if hd.MDN == as2.MDNAsync {
		want := prof.MDNURL
		if want == "" {
			want = partner.Endpoints.AS2URL
		}
		if hd.ReceiptURL != want {
			as2Text(w, http.StatusBadRequest, "Receipt-Delivery-Option does not match the receipt URL configured for this connection")
			return
		}
	}
	rcv, openErr := as2.Open(r.Header, body, st, peer, inboundPolicy(prof), time.Now())
	rowID := ids.New()
	sum := sha256.Sum256(body)
	if openErr != nil {
		code := as2.ErrUnexpected
		var f *as2.Failure
		if errors.As(openErr, &f) {
			code = f.Code
		}
		m.as2Answer(ctx, w, tenant, st, partner, hd, rowID, answer{errCode: code, errText: openErr.Error(), rawSum: sum[:], actor: actor, mdnURL: hd.ReceiptURL, prof: prof})
		return
	}

	// Idempotency: claim the Message-ID. An authenticated repeat is answered with the stored
	// receipt and never processed twice.
	var dup bool
	var existing struct {
		ID     uuid.UUID
		Status string
		Header map[string]string
		Body   []byte
		State  string
		URL    string
	}
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `insert into as2_message (id, tenant_id, partner_id, direction, message_id, as2_from, as2_to, subject, status, signed, encrypted, mdn_mode, mic, payload_sha256)
			values ($1, app_tenant(), $2, 'inbound', $3, $4, $5, $6, 'processing', $7, $8, $9, $10, $11)
			on conflict (tenant_id, direction, as2_from, message_id) where status <> 'rejected' do nothing`,
			rowID, partner.ID, hd.MessageID, hd.From, hd.To, hd.Subject, rcv.Signed, rcv.Encrypted, hd.MDN, rcv.MIC, hex.EncodeToString(sum[:]))
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 1 {
			return nil
		}
		dup = true
		if err := tx.QueryRow(ctx, `select id, status, mdn_header, mdn_body, mdn_state, mdn_url from as2_message where tenant_id=$1 and direction='inbound' and as2_from=$2 and message_id=$3 and status <> 'rejected'`,
			tenant, hd.From, hd.MessageID).Scan(&existing.ID, &existing.Status, &existing.Header, &existing.Body, &existing.State, &existing.URL); err != nil {
			return err
		}
		if existing.Status == "processing" {
			// A crashed first attempt is taken over after as2StaleAfter; otherwise it is still running.
			ct, err := tx.Exec(ctx, `update as2_message set updated_at=now() where id=$1 and status='processing' and updated_at < now() - $2::interval`, existing.ID, fmt.Sprintf("%d seconds", int(as2StaleAfter.Seconds())))
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 1 {
				dup, rowID = false, existing.ID
			}
		}
		if dup {
			_, err = audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "as2.receive.duplicate", Entity: "as2_message", EntityID: existing.ID.String(), After: map[string]any{"messageId": hd.MessageID, "status": existing.Status}})
		}
		return err
	})
	if err != nil {
		as2Text(w, http.StatusInternalServerError, "temporarily unavailable")
		return
	}
	if dup {
		switch {
		case existing.Status == "processing":
			as2Text(w, http.StatusConflict, "message is being processed")
		case existing.Header == nil:
			as2Text(w, http.StatusOK, "message already received")
		case hd.MDN == as2.MDNAsync:
			if existing.State != "delivered" {
				_ = m.queueMDN(ctx, tenant, existing.ID)
			}
			as2Text(w, http.StatusOK, "message already received")
		default:
			writeWire(w, &as2.Wire{Header: existing.Header, Body: existing.Body})
		}
		return
	}

	ship, perr := m.as2Process(ctx, partner.ID, rcv)
	a := answer{rcv: rcv, actor: actor, rawSum: sum[:], mdnURL: hd.ReceiptURL, prof: prof}
	if perr != nil {
		a.errCode, a.errText = as2.ErrUnexpected, perr.Error()
	} else {
		a.shipment = &ship
	}
	m.as2Answer(ctx, w, tenant, st, partner, hd, rowID, a)
}

// answer is the outcome of an inbound message that the receipt reports.
type answer struct {
	rcv      *as2.Received
	shipment *Shipment
	errCode  string
	errText  string
	rawSum   []byte
	actor    string
	mdnURL   string
	prof     partners.AS2Profile
}

// as2Answer builds the signed receipt, stores the outcome (message-log row, audit entry, receipt)
// and replies: the receipt inline for sync, an empty 200 and a queued delivery for async.
func (m *Module) as2Answer(ctx context.Context, w http.ResponseWriter, tenant uuid.UUID, st as2.Station, partner partners.Partner, hd *as2.Header, rowID uuid.UUID, a answer) {
	mic, micAlg := "", ""
	if a.rcv != nil {
		mic, micAlg = a.rcv.MIC, a.rcv.MICAlg
	}
	var mdn *as2.Wire
	if hd.MDN != as2.MDNNone {
		var err error
		mdn, err = as2.BuildMDN(st, as2.MDNInput{MessageID: as2.NewMessageID(st.ID), OriginalMessageID: hd.MessageID, To: hd.From, MIC: mic, MICAlg: micAlg, ErrorCode: a.errCode, Text: publicText(a.errCode), Sign: true,
			Digest: a.prof.MDNSignatureAlgorithm, Legacy: a.prof.Legacy})
		if err != nil {
			as2Text(w, http.StatusInternalServerError, "cannot build receipt")
			return
		}
	}
	status := "received"
	if a.errCode != "" {
		status = "rejected"
	}
	mdnState := ""
	switch {
	case mdn == nil:
	case hd.MDN == as2.MDNAsync:
		mdnState = "queued"
	default:
		mdnState = "returned"
	}
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		signed, enc := false, false
		if a.rcv != nil {
			signed, enc = a.rcv.Signed, a.rcv.Encrypted
		}
		var shipID *uuid.UUID
		if a.shipment != nil {
			shipID = &a.shipment.ID
		}
		var hdr map[string]string
		var mbody []byte
		if mdn != nil {
			hdr, mbody = mdn.Header, mdn.Body
		}
		// A rejected message that failed authentication has no row yet; one that failed
		// processing has its "processing" row updated.
		ct, err := tx.Exec(ctx, `update as2_message set status=$2, error=$3, disposition=$4, shipment_id=$5, mdn_header=$6, mdn_body=$7, mdn_url=$8, mdn_state=$9, updated_at=now() where id=$1`,
			rowID, status, a.errText, a.errCode, shipID, hdr, mbody, a.mdnURL, mdnState)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx, `insert into as2_message (id, tenant_id, partner_id, direction, message_id, as2_from, as2_to, subject, status, signed, encrypted, mdn_mode, payload_sha256, disposition, error, mdn_header, mdn_body, mdn_url, mdn_state)
				values ($1, app_tenant(), $2, 'inbound', $3, $4, $5, $6, 'rejected', $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
				rowID, partner.ID, hd.MessageID, hd.From, hd.To, hd.Subject, signed, enc, hd.MDN, hex.EncodeToString(a.rawSum), a.errCode, a.errText, hdr, mbody, a.mdnURL, mdnState); err != nil {
				return err
			}
		}
		action := "as2.receive"
		after := map[string]any{"messageId": hd.MessageID, "from": hd.From, "signed": signed, "encrypted": enc, "mic": mic, "micAlg": micAlg, "mdn": hd.MDN, "shipmentId": shipID}
		if a.errCode != "" {
			action = "as2.receive.rejected"
			after["code"], after["error"] = a.errCode, a.errText
			// One open alert per sender and reason, however often the sender retries.
			if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
				values ($1, app_tenant(), 'dscsa.as2_rejected', 'warning', $2, $3, 'partner', $4, $5) on conflict do nothing`,
				ids.New(), "AS2 message from "+partner.Name+" rejected ("+a.errCode+")", map[string]any{"messageId": hd.MessageID, "code": a.errCode, "error": a.errText}, partner.ID.String(), "as2rej:"+partner.ID.String()+":"+a.errCode); err != nil {
				return err
			}
		}
		if _, err := audit.WriteAs(ctx, tx, tenant, a.actor, audit.Record{Action: action, Entity: "as2_message", EntityID: rowID.String(), After: after}); err != nil {
			return err
		}
		if hd.MDN == as2.MDNAsync && mdn != nil {
			_, err = jobs.Enqueue(ctx, tx, &tenant, JobAS2MDN, map[string]any{"id": rowID}, jobs.MaxAttempts(6), jobs.Dedupe("as2mdn:"+rowID.String()))
		}
		return err
	})
	if err != nil {
		as2Text(w, http.StatusInternalServerError, "temporarily unavailable")
		return
	}
	switch {
	case mdn == nil && a.errCode != "":
		as2Text(w, http.StatusBadRequest, publicText(a.errCode))
	case mdn == nil || hd.MDN == as2.MDNAsync:
		as2Text(w, http.StatusOK, "message received")
	default:
		writeWire(w, mdn)
	}
}

// publicText is the receipt text for an error code: it never echoes parser details to the sender.
func publicText(code string) string {
	if code == "" {
		return ""
	}
	return "The message could not be accepted: " + code + "."
}

// queueMDN re-queues delivery of a stored receipt.
func (m *Module) queueMDN(ctx context.Context, tenant, id uuid.UUID) error {
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := jobs.Enqueue(ctx, tx, &tenant, JobAS2MDN, map[string]any{"id": id}, jobs.MaxAttempts(6), jobs.Dedupe("as2mdn:"+id.String()))
		return err
	})
}

// as2Process hands an authenticated payload to the inbound TI/TS path.
func (m *Module) as2Process(ctx context.Context, partner uuid.UUID, rcv *as2.Received) (Shipment, error) {
	ct := strings.ToLower(rcv.ContentType)
	if !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return Shipment{}, errors.New("unsupported payload content type")
	}
	doc, _, err := lib.Parse("", rcv.Payload)
	if err != nil {
		return Shipment{}, err
	}
	s, err := m.Receive(ctx, InboundInput{Document: rcv.Payload, PONumber: poNumberOf(doc), PartnerID: &partner})
	var ce *epcis.CaptureError
	if errors.As(err, &ce) {
		return Shipment{}, fmt.Errorf("TI/TS document rejected: %d validation errors", len(ce.Capture.Errors))
	}
	return s, err
}

// TODO(spec): an inbound TI/TS arrives without an ASN, so the reconciliation against the ASN that
// the HTTPS path performs (the caller supplies it) is empty for AS2 messages; where partners put
// the despatch advice (a second payload, a business transaction in the document) is not specified.

// poNumberOf reads the purchase order number of a TI/TS from its header, else from the first
// purchase-order business transaction on a shipping event.
func poNumberOf(d *lib.Document) string {
	var h struct {
		TI struct {
			PO string `json:"poNumber"`
		} `json:"dscsa:transactionInformation"`
	}
	if json.Unmarshal(d.Header, &h) == nil && h.TI.PO != "" {
		return h.TI.PO
	}
	for _, e := range d.Body.EventList {
		for _, bt := range e.BizTransactionList {
			if bt.Type == "po" || strings.HasSuffix(bt.Type, ":po") {
				if i := strings.LastIndex(bt.BizTransaction, ":"); i >= 0 {
					return bt.BizTransaction[i+1:]
				}
			}
		}
	}
	return ""
}

// RegisterJobs implements kernel.Worker.
func (m *Module) RegisterJobs(r *jobs.Runner) { r.Handle(JobAS2MDN, m.deliverMDNJob) }

// deliverMDNJob posts a stored asynchronous receipt to the sender.
func (m *Module) deliverMDNJob(ctx context.Context, j jobs.Job) error {
	var p struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(j.Payload, &p); err != nil || j.TenantID == nil {
		return jobs.Permanent{Err: errors.New("invalid AS2 receipt job")}
	}
	tenant := *j.TenantID
	var url, state, msgID, from string
	var hdr map[string]string
	var body []byte
	if err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select mdn_url, mdn_state, message_id, as2_from, mdn_header, mdn_body from as2_message where id=$1 and direction='inbound'`, p.ID).Scan(&url, &state, &msgID, &from, &hdr, &body)
	}); err != nil {
		return jobs.Permanent{Err: err}
	}
	if state == "delivered" || hdr == nil || url == "" {
		return nil
	}
	code, _, _, err := as2.Post(ctx, m.Client, url, &as2.Wire{Header: hdr, Body: body})
	if err == nil && (code < 200 || code >= 300) {
		err = fmt.Errorf("sender returned HTTP %d", code)
	}
	final := err == nil || j.Attempts >= j.MaxAttempts
	if !final {
		return err
	}
	newState, errText := "delivered", ""
	if err != nil {
		newState, errText = "failed", err.Error()
	}
	werr := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update as2_message set mdn_state=$2, updated_at=now() where id=$1`, p.ID, newState); err != nil {
			return err
		}
		if newState == "failed" {
			if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
				values ($1, app_tenant(), 'dscsa.as2_mdn_failed', 'warning', $2, $3, 'as2_message', $4, $5) on conflict do nothing`,
				ids.New(), "AS2 receipt to "+from+" could not be delivered", map[string]any{"messageId": msgID, "error": errText}, p.ID.String(), "as2mdn:"+p.ID.String()); err != nil {
				return err
			}
		}
		_, err := audit.WriteAs(ctx, tx, tenant, "system", audit.Record{Action: "as2.mdn.send", Entity: "as2_message", EntityID: p.ID.String(), After: map[string]any{"messageId": msgID, "state": newState, "error": errText}})
		return err
	})
	if werr != nil {
		return werr
	}
	return nil
}
