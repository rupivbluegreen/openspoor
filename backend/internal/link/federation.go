package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openspoor/openspoor/internal/core/egress"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/pkg/edi"
)

// JobDeliver delivers one signed federation envelope with retries.
const JobDeliver = "federation.deliver"

// MaxSkew bounds clock difference between nodes.
const MaxSkew = 5 * time.Minute

type nodeIdentity struct {
	DID  string
	Pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

var nodeMu sync.Mutex

// DIDFor builds a did:web identifier from a public base URL (port encoded as %3A).
func DIDFor(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return "did:web:localhost"
	}
	did := "did:web:" + strings.ReplaceAll(u.Host, ":", "%3A")
	if p := strings.Trim(u.Path, "/"); p != "" {
		did += ":" + strings.ReplaceAll(p, "/", ":")
	}
	return did
}

// Node loads (or creates on first use) this installation's federation identity.
// The private key is sealed with the installation master key.
func (m *Module) Node(ctx context.Context) (*nodeIdentity, error) {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if m.node != nil {
		return m.node, nil
	}
	master := m.Keys.Master()
	var did string
	var pub, sealed []byte
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select did, public_key, private_key from federation_node where id=1`).Scan(&did, &pub, &sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			p, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			did, pub, sealed = DIDFor(m.Cfg.PublicURL), p, master.Seal(priv, []byte("federation-node"))
			_, err = tx.Exec(ctx, `insert into federation_node (id, did, public_key, private_key) values (1, $1, $2, $3)`, did, pub, sealed)
			return err
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	priv, err := master.Open(sealed, []byte("federation-node"))
	if err != nil {
		return nil, fmt.Errorf("unseal node key: %w", err)
	}
	m.node = &nodeIdentity{DID: did, Pub: pub, priv: priv}
	return m.node, nil
}

// Root mounts /.well-known/did.json.
func (m *Module) Root(r chi.Router) {
	r.Get("/.well-known/did.json", func(w http.ResponseWriter, r *http.Request) {
		n, err := m.Node(r.Context())
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, didDocument(n))
	})
}

func didDocument(n *nodeIdentity) map[string]any {
	jwk := map[string]string{"kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(n.Pub)}
	return map[string]any{
		"@context": []string{"https://www.w3.org/ns/did/v1", "https://w3id.org/security/suites/jws-2020/v1", "https://w3id.org/security/multikey/v1"},
		"id":       n.DID,
		// key-1 (JWK) signs federation envelopes; key-2 is the same key as a Multikey, used by
		// eddsa-jcs-2022 Data Integrity proofs on credentials (ADR-010).
		"verificationMethod": []map[string]any{
			{"id": n.DID + "#key-1", "type": "JsonWebKey2020", "controller": n.DID, "publicKeyJwk": jwk},
			{"id": n.DID + multikeyVMIndex, "type": "Multikey", "controller": n.DID, "publicKeyMultibase": multikeyEd25519(n.Pub)},
		},
		"assertionMethod": []string{n.DID + "#key-1", n.DID + multikeyVMIndex},
		"service":         []map[string]string{{"id": n.DID + "#inbox", "type": "OpenSpoorFederationInbox", "serviceEndpoint": "/api/v1/federation/inbox"}},
	}
}

func (m *Module) getNode(w http.ResponseWriter, r *http.Request) error {
	n, err := m.Node(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, didDocument(n))
	return nil
}

// Envelope is a signed node-to-node message.
type Envelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	From       string          `json:"from"`
	FromTenant string          `json:"fromTenant"`
	To         string          `json:"to"`
	ToTenant   string          `json:"toTenant"`
	CreatedAt  time.Time       `json:"createdAt"`
	Payload    json.RawMessage `json:"payload"`
	Sig        string          `json:"sig,omitempty"`
}

// signingBytes is the canonical encoding of the envelope without its signature: the payload
// is re-encoded with sorted object keys (numbers kept verbatim) so storage or proxies that
// reorder JSON keys do not break verification.
func signingBytes(e Envelope) []byte {
	e.Sig = ""
	dec := json.NewDecoder(bytes.NewReader(e.Payload))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) == nil {
		if c, err := json.Marshal(v); err == nil {
			e.Payload = c
		}
	}
	e.CreatedAt = e.CreatedAt.UTC()
	b, _ := json.Marshal(e)
	return b
}

// Sign signs an envelope with the node key.
func (n *nodeIdentity) Sign(e Envelope) Envelope {
	e.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(n.priv, signingBytes(e)))
	return e
}

// VerifyEnvelope checks the Ed25519 signature.
func VerifyEnvelope(pub ed25519.PublicKey, e Envelope) bool {
	sig, err := base64.RawURLEncoding.DecodeString(e.Sig)
	return err == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, signingBytes(e), sig)
}

// Peer is a trusted remote node (per tenant).
type Peer struct {
	ID             uuid.UUID `json:"id" db:"id"`
	Name           string    `json:"name" db:"name"`
	DID            string    `json:"did" db:"did"`
	URL            string    `json:"url" db:"url"`
	PublicKey      []byte    `json:"-" db:"public_key"`
	RemoteTenant   string    `json:"remoteTenant" db:"remote_tenant"`
	TLSFingerprint string    `json:"tlsFingerprint" db:"tls_fingerprint"`
	Status         string    `json:"status" db:"status"`
	CreatedAt      time.Time `json:"createdAt" db:"created_at"`
	PublicKeyB64   string    `json:"publicKey" db:"-"`
}

const peerCols = `id, name, did, url, public_key, remote_tenant, tls_fingerprint, status, created_at`

func (p *Peer) fill() { p.PublicKeyB64 = base64.RawURLEncoding.EncodeToString(p.PublicKey) }

func (m *Module) listPeers(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Peer](r.Context(), tx, `select `+peerCols+` from federation_peer order by name`)
		for i := range items {
			items[i].fill()
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Peer]{Items: items, Total: len(items)})
		}
		return err
	})
}

// fetchDID resolves a node's DID document from its base URL.
func (m *Module) fetchDID(ctx context.Context, base, fingerprint string) (string, ed25519.PublicKey, error) {
	cl, err := m.clientFor(fingerprint)
	if err != nil {
		return "", nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/.well-known/did.json", nil)
	res, err := cl.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer res.Body.Close()
	var doc struct {
		ID                 string `json:"id"`
		VerificationMethod []struct {
			PublicKeyJwk struct {
				Crv string `json:"crv"`
				X   string `json:"x"`
			} `json:"publicKeyJwk"`
		} `json:"verificationMethod"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&doc); err != nil || len(doc.VerificationMethod) == 0 {
		return "", nil, errors.New("no DID document at " + base)
	}
	k, err := base64.RawURLEncoding.DecodeString(doc.VerificationMethod[0].PublicKeyJwk.X)
	if err != nil || len(k) != ed25519.PublicKeySize || doc.VerificationMethod[0].PublicKeyJwk.Crv != "Ed25519" {
		return "", nil, errors.New("DID document has no Ed25519 key")
	}
	return doc.ID, k, nil
}

func (m *Module) addPeer(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name           string `json:"name"`
		URL            string `json:"url"`
		RemoteTenant   string `json:"remoteTenant"`
		PublicKey      string `json:"publicKey"`
		DID            string `json:"did"`
		TLSFingerprint string `json:"tlsFingerprint"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return httpx.BadRequest("url must be the peer's http(s) base URL")
	}
	if in.Name == "" || in.RemoteTenant == "" {
		return httpx.BadRequest("name and remoteTenant (the peer tenant slug) are required")
	}
	if in.TLSFingerprint != "" {
		fp, err := NormalizeFingerprint(in.TLSFingerprint)
		if err != nil {
			return httpx.BadRequest("tlsFingerprint: " + err.Error())
		}
		if u.Scheme != "https" {
			return httpx.BadRequest("a pinned tlsFingerprint needs an https peer url")
		}
		in.TLSFingerprint = fp
	}
	var key ed25519.PublicKey
	did := in.DID
	if in.PublicKey != "" {
		k, err := base64.RawURLEncoding.DecodeString(in.PublicKey)
		if err != nil || len(k) != ed25519.PublicKeySize || did == "" {
			return httpx.BadRequest("publicKey must be a base64url Ed25519 key and did is required with it")
		}
		key = k
	} else if did, key, err = m.fetchDID(r.Context(), in.URL, in.TLSFingerprint); err != nil {
		return httpx.BadRequest("cannot resolve peer DID: " + err.Error())
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[Peer](r.Context(), tx, `insert into federation_peer (id, tenant_id, name, did, url, public_key, remote_tenant, tls_fingerprint) values ($1, app_tenant(), $2,$3,$4,$5,$6,$7)
			returning `+peerCols, ids.New(), in.Name, did, strings.TrimSuffix(in.URL, "/"), []byte(key), in.RemoteTenant, in.TLSFingerprint)
		if err != nil {
			return err
		}
		p.fill()
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "federation.peer.add", Entity: "federation_peer", EntityID: p.ID.String(), After: p}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, p)
		return nil
	})
}

func (m *Module) approvePeer(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[Peer](r.Context(), tx, `update federation_peer set status='active' where id=$1 returning `+peerCols, id)
		if err != nil {
			return err
		}
		p.fill()
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "federation.peer.approve", Entity: "federation_peer", EntityID: id.String(), After: p}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, p)
		return nil
	})
}

// Send signs and queues a message to an active peer; returns the message id.
func (m *Module) Send(ctx context.Context, peerID uuid.UUID, typ string, payload any) (string, error) {
	n, err := m.Node(ctx)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	me := auth.MustFrom(ctx)
	var msgID string
	err = m.DB.Tx(ctx, me.TenantID, func(tx pgx.Tx) error {
		p, err := db.One[Peer](ctx, tx, `select `+peerCols+` from federation_peer where id=$1`, peerID)
		if err != nil {
			return err
		}
		if p.Status != "active" {
			return httpx.Conflict("peer is " + p.Status)
		}
		var slug string
		if err := tx.QueryRow(ctx, `select slug from tenant where id=app_tenant()`).Scan(&slug); err != nil {
			return err
		}
		env := n.Sign(Envelope{ID: uuid.NewString(), Type: typ, From: n.DID, FromTenant: slug, To: p.DID, ToTenant: p.RemoteTenant, CreatedAt: time.Now().UTC(), Payload: raw})
		msgID = env.ID
		tid := me.TenantID
		jobID, err := jobs.Enqueue(ctx, tx, &tid, JobDeliver, map[string]any{"url": p.URL + "/api/v1/federation/inbox", "envelope": env}, jobs.MaxAttempts(m.maxAttempts()))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into federation_message (id, tenant_id, peer_id, direction, msg_id, type, status, job_id) values ($1, app_tenant(), $2, 'outbound', $3, $4, 'queued', $5)`,
			ids.New(), p.ID, env.ID, typ, jobID)
		return err
	})
	return msgID, err
}

func (m *Module) maxAttempts() int {
	if m.DeliveryAttempts > 0 {
		return m.DeliveryAttempts
	}
	return 10
}

// RegisterJobs implements kernel.Worker.
func (m *Module) RegisterJobs(r *jobs.Runner) { r.Handle(JobDeliver, m.deliverJob) }

func (m *Module) deliverJob(ctx context.Context, j jobs.Job) error {
	var p struct {
		URL      string   `json:"url"`
		Envelope Envelope `json:"envelope"`
	}
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent{Err: err}
	}
	// Re-sign our own envelopes on every attempt: receivers reject a createdAt outside MaxSkew,
	// and a retry can run long after the message was queued. The id (replay key) is unchanged.
	if n, err := m.Node(ctx); err == nil && p.Envelope.From == n.DID {
		p.Envelope.CreatedAt = time.Now().UTC()
		p.Envelope = n.Sign(p.Envelope)
	}
	b, _ := json.Marshal(p.Envelope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(b))
	if err != nil {
		return jobs.Permanent{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	var res *http.Response
	cl, err := m.deliveryClient(ctx, *j.TenantID, p.URL, p.Envelope.To)
	if err == nil {
		res, err = cl.Do(req)
	}
	status, detail := "delivered", map[string]any{}
	if err != nil {
		status, detail["error"] = "retrying", err.Error()
	} else {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		_ = res.Body.Close()
		detail["status"], detail["response"] = res.StatusCode, string(body)
		switch {
		case res.StatusCode == http.StatusConflict: // already received (replay) counts as delivered
		case res.StatusCode >= 400 && res.StatusCode < 500:
			status = "rejected"
		case res.StatusCode >= 500:
			status = "retrying"
		}
	}
	dead := status == "rejected" || (status == "retrying" && j.Attempts >= j.MaxAttempts)
	_ = m.DB.Tx(ctx, *j.TenantID, func(tx pgx.Tx) error {
		var fmID uuid.UUID
		if err := tx.QueryRow(ctx, `update federation_message set status=$3, detail=$4 where direction='outbound' and msg_id=$1 and tenant_id=$2 returning id`,
			p.Envelope.ID, *j.TenantID, status, detail).Scan(&fmID); err != nil {
			return err
		}
		if status == "delivered" {
			// The peer accepted the message: that is the acknowledgement of a B2B document sent over federation.
			_, err := tx.Exec(ctx, `update b2b_document set acked_at = coalesce(acked_at, now()) where direction='outbound' and delivery->>'messageId' = $1`, p.Envelope.ID)
			return err
		}
		if dead {
			_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
				values ($1, app_tenant(), 'link.message_dead', 'error', $2, $3, 'federation_message', $4, $5) on conflict do nothing`,
				ids.New(), "Federation "+p.Envelope.Type+" message could not be delivered (dead letter)", detail, fmID.String(), "dead:"+fmID.String())
			return err
		}
		return nil
	})
	switch status {
	case "retrying":
		return fmt.Errorf("delivery failed: %v", detail)
	case "rejected":
		return jobs.Permanent{Err: fmt.Errorf("peer rejected message: %v", detail)}
	}
	return nil
}

// inbox receives signed messages from peers.
func (m *Module) inbox(w http.ResponseWriter, r *http.Request) error {
	enforce := m.Cfg.TLS.RequireFederationClientCert
	if enforce && (r.TLS == nil || len(r.TLS.PeerCertificates) == 0) {
		slog.Warn("federation inbox: no client certificate", "remote", r.RemoteAddr)
		return httpx.Unauthorized("client certificate required")
	}
	var env Envelope
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<20))
	if err := dec.Decode(&env); err != nil {
		return httpx.BadRequest("invalid envelope")
	}
	ctx := r.Context()
	n, err := m.Node(ctx)
	if err != nil {
		return err
	}
	if env.To != n.DID {
		return httpx.BadRequest("message is not addressed to this node")
	}
	if d := time.Since(env.CreatedAt); d > MaxSkew || d < -MaxSkew {
		return httpx.BadRequest("message timestamp outside the allowed window")
	}
	var tenant uuid.UUID
	if err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id from tenant where slug=$1 and status='active'`, env.ToTenant).Scan(&tenant)
	}); err != nil {
		return httpx.NotFound("unknown recipient tenant")
	}
	var peer Peer
	err = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		peer, err = db.One[Peer](ctx, tx, `select `+peerCols+` from federation_peer where did=$1 and remote_tenant=$2 and status='active'`, env.From, env.FromTenant)
		return err
	})
	if err != nil {
		return httpx.Forbidden("sender is not an approved peer of the recipient")
	}
	if enforce {
		if err := checkClientCert(r.TLS.PeerCertificates[0], peer, time.Now()); err != nil {
			slog.Warn("federation inbox: client certificate refused", "peer", peer.DID, "reason", err.Error())
			return httpx.Forbidden("client certificate does not match the pinned certificate of the sender")
		}
	}
	if !VerifyEnvelope(peer.PublicKey, env) {
		return httpx.Unauthorized("invalid signature")
	}
	sys := auth.With(ctx, auth.System(tenant))
	result := map[string]any{"accepted": true, "id": env.ID}
	err = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `insert into federation_message (id, tenant_id, peer_id, direction, msg_id, type, status) values ($1, app_tenant(), $2, 'inbound', $3, $4, 'received')
			on conflict (tenant_id, direction, msg_id) do nothing`, ids.New(), peer.ID, env.ID, env.Type)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.Conflict("duplicate message (replay)")
		}
		_, err = audit.WriteAs(ctx, tx, tenant, "peer:"+peer.DID, audit.Record{Action: "federation.receive." + env.Type, Entity: "federation_message", EntityID: env.ID})
		return err
	})
	if err != nil {
		return err
	}
	switch env.Type {
	case "ping":
	case "epcis.capture":
		c, err := m.EPCIS.Capture(sys, "application/ld+json", env.Payload, "federation:"+peer.Name)
		if err != nil {
			return err
		}
		result["captureId"] = c.ID
	case "b2b.document":
		var d edi.Document
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return httpx.BadRequest("invalid b2b payload")
		}
		err = m.DB.Tx(sys, tenant, func(tx pgx.Tx) error {
			doc, err := StoreDoc(sys, tx, "inbound", nil, nil, d)
			result["documentId"] = doc.ID
			return err
		})
		if err != nil {
			return err
		}
	default:
		return httpx.BadRequest("unsupported message type " + env.Type)
	}
	httpx.JSON(w, http.StatusAccepted, result)
	return nil
}

func (m *Module) fedSend(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PeerID  uuid.UUID       `json:"peerId"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	switch in.Type {
	case "ping", "epcis.capture", "b2b.document":
	default:
		return httpx.BadRequest("type must be ping, epcis.capture or b2b.document")
	}
	if len(in.Payload) == 0 {
		in.Payload = json.RawMessage(`{}`)
	}
	id, err := m.Send(r.Context(), in.PeerID, in.Type, in.Payload)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"messageId": id})
	return nil
}

type message struct {
	ID        uuid.UUID      `json:"id" db:"id"`
	PeerID    *uuid.UUID     `json:"peerId,omitempty" db:"peer_id"`
	Direction string         `json:"direction" db:"direction"`
	MsgID     string         `json:"messageId" db:"msg_id"`
	Type      string         `json:"type" db:"type"`
	Status    string         `json:"status" db:"status"`
	Detail    map[string]any `json:"detail" db:"detail"`
	CreatedAt time.Time      `json:"createdAt" db:"created_at"`
}

func (m *Module) listMessages(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[message](r.Context(), tx, `select id, peer_id, direction, msg_id, type, status, detail, created_at from federation_message order by created_at desc limit 500`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[message]{Items: items, Total: len(items)})
		}
		return err
	})
}

var _ kernel.Module = (*Module)(nil)

// --- mutual TLS pinning (ADR-008) ---

// NormalizeFingerprint returns the canonical form of a SHA-256 certificate fingerprint: 64 lower-case
// hex digits. It accepts colon- or space-separated octets, upper or lower case, and the prefixes
// "sha256:" and "SHA256 Fingerprint=" that common tools print.
func NormalizeFingerprint(s string) (string, error) {
	low := strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(low, '='); i >= 0 && strings.HasPrefix(low, "sha") {
		low = low[i+1:]
	}
	low = strings.TrimPrefix(strings.TrimPrefix(low, "sha256:"), "sha-256:")
	low = strings.NewReplacer(":", "", " ", "").Replace(low)
	if b, err := hex.DecodeString(low); err != nil || len(b) != sha256.Size {
		return "", errors.New("a SHA-256 fingerprint is 64 hex digits (optionally colon separated)")
	}
	return low, nil
}

// CertFingerprint is the canonical SHA-256 fingerprint of a DER certificate.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// fingerprintMatches compares a certificate to a stored (possibly unnormalised) pin in constant time.
// An unusable pin never matches.
func fingerprintMatches(der []byte, pin string) bool {
	want, err := NormalizeFingerprint(pin)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(CertFingerprint(der)), []byte(want)) == 1
}

// checkClientCert decides whether the leaf certificate a connection presented is the one pinned for
// the peer. Inbound client certificates are not necessarily chain-verified (peers often use
// self-signed certificates), so validity dates are checked here, and a peer without a pin is refused.
func checkClientCert(leaf *x509.Certificate, p Peer, now time.Time) error {
	if p.TLSFingerprint == "" {
		return errors.New("peer has no pinned TLS fingerprint")
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return errors.New("client certificate is not valid now")
	}
	if !fingerprintMatches(leaf.Raw, p.TLSFingerprint) {
		return errors.New("client certificate fingerprint differs from the pinned one")
	}
	return nil
}

// pinnedTLSConfig builds the client TLS configuration for one peer. The normal chain and host name
// verification stays on; pinning is an additional check of the leaf certificate, run on every
// connection (VerifyConnection also runs for resumed sessions). pin may be empty (no pinning).
func (m *Module) pinnedTLSConfig(pin string) (*tls.Config, error) {
	t := m.Cfg.TLS
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if t.MinVersion != 0 {
		cfg.MinVersion = t.MinVersion
	}
	if pin != "" {
		want, err := NormalizeFingerprint(pin)
		if err != nil {
			return nil, fmt.Errorf("peer TLS fingerprint: %w", err)
		}
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !fingerprintMatches(cs.PeerCertificates[0].Raw, want) {
				return errors.New("peer certificate does not match the pinned fingerprint")
			}
			return nil
		}
	}
	if t.PeerCA != "" {
		pool, err := config.LoadPool(t.PeerCA)
		if err != nil {
			return nil, fmt.Errorf("OPENSPOOR_TLS_PEER_CA: %w", err)
		}
		cfg.RootCAs = pool
	}
	if t.ClientCert != "" {
		// Read on each handshake so a renewed certificate is picked up without a restart.
		cert, key := t.ClientCert, t.ClientKey
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			pair, err := tls.LoadX509KeyPair(cert, key)
			if err != nil {
				return nil, fmt.Errorf("OPENSPOOR_TLS_CLIENT_CERT: %w", err)
			}
			return &pair, nil
		}
	}
	return cfg, nil
}

type clientKey struct {
	pin, cert, key, ca string
	min                uint16
}

var (
	clientsMu sync.Mutex
	clients   = map[clientKey]*http.Client{}
)

// clientFor returns the HTTP client for a peer: m.Client when nothing TLS-specific is configured and
// the peer is not pinned, otherwise a client that pins the peer certificate, requires at least
// TLS 1.3 (unless OPENSPOOR_TLS_MIN_VERSION=1.2 was set) and presents this node's client certificate.
func (m *Module) clientFor(pin string) (*http.Client, error) {
	t := m.Cfg.TLS
	if pin == "" && t.ClientCert == "" && t.PeerCA == "" && t.MinVersion == 0 {
		return m.Client, nil
	}
	if pin != "" {
		fp, err := NormalizeFingerprint(pin)
		if err != nil {
			return nil, fmt.Errorf("peer TLS fingerprint: %w", err)
		}
		pin = fp
	}
	k := clientKey{pin, t.ClientCert, t.ClientKey, t.PeerCA, t.MinVersion}
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[k]; ok {
		return c, nil
	}
	cfg, err := m.pinnedTLSConfig(pin)
	if err != nil {
		return nil, err
	}
	tr := egress.NewTransport()
	tr.TLSClientConfig = cfg
	timeout := 15 * time.Second
	if m.Client != nil && m.Client.Timeout > 0 {
		timeout = m.Client.Timeout
	}
	c := &http.Client{Timeout: timeout, Transport: tr,
		// A redirect would send the signed envelope to a host the pin was not made for.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	clients[k] = c
	return c, nil
}

// deliveryClient picks the client for one delivery: the pin comes from the peer record (looked up by
// the addressed DID at delivery time, so a re-pinned or corrected fingerprint applies to queued jobs).
func (m *Module) deliveryClient(ctx context.Context, tenant uuid.UUID, target, toDID string) (*http.Client, error) {
	var pin string
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select tls_fingerprint from federation_peer where did=$1 and url = $2 order by created_at desc limit 1`,
			toDID, strings.TrimSuffix(target, "/api/v1/federation/inbox")).Scan(&pin)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if pin != "" && !strings.HasPrefix(target, "https://") {
		return nil, errors.New("peer has a pinned TLS fingerprint but its URL is not https; refusing to send")
	}
	return m.clientFor(pin)
}
