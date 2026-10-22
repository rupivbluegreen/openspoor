package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
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
		"@context":           []string{"https://www.w3.org/ns/did/v1", "https://w3id.org/security/suites/jws-2020/v1"},
		"id":                 n.DID,
		"verificationMethod": []map[string]any{{"id": n.DID + "#key-1", "type": "JsonWebKey2020", "controller": n.DID, "publicKeyJwk": jwk}},
		"assertionMethod":    []string{n.DID + "#key-1"},
		"service":            []map[string]string{{"id": n.DID + "#inbox", "type": "OpenSpoorFederationInbox", "serviceEndpoint": "/api/v1/federation/inbox"}},
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
func (m *Module) fetchDID(ctx context.Context, base string) (string, ed25519.PublicKey, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/.well-known/did.json", nil)
	res, err := m.Client.Do(req)
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
	var key ed25519.PublicKey
	did := in.DID
	if in.PublicKey != "" {
		k, err := base64.RawURLEncoding.DecodeString(in.PublicKey)
		if err != nil || len(k) != ed25519.PublicKeySize || did == "" {
			return httpx.BadRequest("publicKey must be a base64url Ed25519 key and did is required with it")
		}
		key = k
	} else if did, key, err = m.fetchDID(r.Context(), in.URL); err != nil {
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
		if _, err := tx.Exec(ctx, `insert into federation_message (id, tenant_id, peer_id, direction, msg_id, type, status) values ($1, app_tenant(), $2, 'outbound', $3, $4, 'queued')`,
			ids.New(), p.ID, env.ID, typ); err != nil {
			return err
		}
		tid := me.TenantID
		_, err = jobs.Enqueue(ctx, tx, &tid, JobDeliver, map[string]any{"url": p.URL + "/api/v1/federation/inbox", "envelope": env}, jobs.MaxAttempts(10))
		return err
	})
	return msgID, err
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
	b, _ := json.Marshal(p.Envelope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(b))
	if err != nil {
		return jobs.Permanent{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := m.Client.Do(req)
	status, detail := "delivered", map[string]any{}
	if err != nil {
		status, detail["error"] = "retrying", err.Error()
	} else {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		detail["status"], detail["response"] = res.StatusCode, string(body)
		switch {
		case res.StatusCode == http.StatusConflict: // already received (replay) counts as delivered
		case res.StatusCode >= 400 && res.StatusCode < 500:
			status = "rejected"
		case res.StatusCode >= 500:
			status = "retrying"
		}
	}
	_ = m.DB.Tx(ctx, *j.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update federation_message set status=$3, detail=$4 where direction='outbound' and msg_id=$1 and tenant_id=$2`, p.Envelope.ID, *j.TenantID, status, detail)
		return err
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
