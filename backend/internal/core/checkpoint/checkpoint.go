// Package checkpoint defines the signed tamper-evidence checkpoints: a statement, signed with
// the node's Ed25519 key, of the head of a tenant's audit hash chain and of the Merkle root of a
// batch of EPCIS events. A checkpoint can be exported and checked offline (`openspoor
// verify-checkpoint`) without database access.
//
// What a checkpoint proves: when it was signed, the audit chain ended at the stated entry and
// the stated batch of events had the stated content. It does not prove when it was signed
// beyond the signer's own clock; an independent timestamp authority is not used.
// TODO(spec): optional RFC 3161 timestamp token over the document hash.
package checkpoint

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openspoor/openspoor/internal/core/audit"
)

// Format identifiers.
const (
	Version      = 1
	Type         = "openspoor.checkpoint"
	SigAlgorithm = "Ed25519"
	// RootAlgorithm names the event batch digest: a binary Merkle tree over event leaves in
	// (record time, event id) order, with 0x00 / 0x01 domain separation for leaves and nodes
	// (the construction of RFC 6962 section 2.1).
	RootAlgorithm = "sha256-merkle-rfc6962"
	signingPrefix = "openspoor-checkpoint-v1\n"
	leafPrefix    = "openspoor-event-leaf-v1|"
)

// Link points at the checkpoint before this one, so a missing or replaced checkpoint shows.
type Link struct {
	Seq     int64  `json:"seq"`
	DocHash string `json:"docHash"`
}

// AuditHead is the end of the tenant's audit chain at signing time.
type AuditHead struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

// EventBatch summarizes the events recorded in (From, To].
type EventBatch struct {
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
	Count     int64  `json:"count"`
	Root      string `json:"root"`
	Algorithm string `json:"algorithm"`
}

// Doc is the signed statement. Times are RFC 3339 UTC strings so the signed bytes never
// depend on a time encoder.
type Doc struct {
	Version   int        `json:"version"`
	Type      string     `json:"type"`
	TenantID  string     `json:"tenantId"`
	Seq       int64      `json:"seq"`
	CreatedAt string     `json:"createdAt"`
	Previous  *Link      `json:"previous,omitempty"`
	Audit     AuditHead  `json:"audit"`
	Events    EventBatch `json:"events"`
	KeyID     string     `json:"keyId"`
}

// Signature is an Ed25519 signature over SigningBytes(Doc).
type Signature struct {
	Alg       string `json:"alg"`
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"` // base64url, no padding
	Value     string `json:"value"`     // base64url, no padding
}

// Envelope is a document with its signature.
type Envelope struct {
	Checkpoint Doc       `json:"checkpoint"`
	Signature  Signature `json:"signature"`
}

// Leaf is one event's leaf hash.
type Leaf struct {
	EventID    string `json:"eventId"`
	RecordTime string `json:"recordTime"`
	Hash       string `json:"hash"`
}

// Evidence is the optional material that lets a verifier check the audit chain slice and the
// event batch without the database.
type Evidence struct {
	// AuditFromSeq/AuditFromHash say where the exported slice starts: the entry before the
	// first one in Audit (0 and "" at the start of the chain).
	AuditFromSeq  int64         `json:"auditFromSeq"`
	AuditFromHash string        `json:"auditFromHash"`
	Audit         []audit.Entry `json:"audit"`
	Leaves        []Leaf        `json:"leaves"`
}

// Bundle is an exported checkpoint, optionally with its evidence.
type Bundle struct {
	Envelope
	Evidence *Evidence `json:"evidence,omitempty"`
}

// SigningBytes is the exact byte string that is signed.
func SigningBytes(d Doc) ([]byte, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return append([]byte(signingPrefix), b...), nil
}

// DocHash is the hex SHA-256 of the signing bytes; the next checkpoint links to it.
func DocHash(d Doc) (string, error) {
	b, err := SigningBytes(d)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// PublicKeyString renders a public key the way checkpoints and `--pubkey` carry it.
func PublicKeyString(pub ed25519.PublicKey) string { return b64(pub) }

// ParsePublicKey reads a base64url (or standard base64) Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		if b, err = base64.StdEncoding.DecodeString(s); err != nil {
			return nil, errors.New("public key is not base64")
		}
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	return ed25519.PublicKey(b), nil
}

// Sign signs d (KeyID is set from keyID).
func Sign(d Doc, keyID string, priv ed25519.PrivateKey) (Envelope, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("checkpoint: invalid private key")
	}
	d.Version, d.Type, d.KeyID = Version, Type, keyID
	if d.Events.Algorithm == "" {
		d.Events.Algorithm = RootAlgorithm
	}
	msg, err := SigningBytes(d)
	if err != nil {
		return Envelope{}, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return Envelope{Checkpoint: d, Signature: Signature{Alg: SigAlgorithm, KeyID: keyID, PublicKey: b64(pub), Value: b64(ed25519.Sign(priv, msg))}}, nil
}

// LeafHash is the leaf for one stored event: it binds the event id, the record time and the
// canonical (sorted-key) JSON of the stored document.
func LeafHash(eventID string, recordTime time.Time, doc []byte) []byte {
	var v any
	canon := doc
	if json.Unmarshal(doc, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			canon = b
		}
	}
	dh := sha256.Sum256(canon)
	h := sha256.New()
	fmt.Fprintf(h, "%s%s|%s|%s", leafPrefix, eventID, recordTime.UTC().Format(time.RFC3339Nano), hex.EncodeToString(dh[:]))
	return h.Sum(nil)
}

// Root computes the Merkle root of leaves in the order given. An empty batch has the root
// of SHA-256 over the empty string.
func Root(leaves [][]byte) []byte {
	if len(leaves) == 0 {
		h := sha256.Sum256(nil)
		return h[:]
	}
	if len(leaves) == 1 {
		h := sha256.Sum256(append([]byte{0}, leaves[0]...))
		return h[:]
	}
	k := 1
	for k*2 < len(leaves) {
		k *= 2
	}
	l, r := Root(leaves[:k]), Root(leaves[k:])
	h := sha256.Sum256(append(append([]byte{1}, l...), r...))
	return h[:]
}

// RootOfLeaves decodes hex leaves and returns the hex root.
func RootOfLeaves(leaves []Leaf) (string, error) {
	raw := make([][]byte, len(leaves))
	for i, l := range leaves {
		b, err := hex.DecodeString(l.Hash)
		if err != nil {
			return "", fmt.Errorf("leaf %s: %w", l.EventID, err)
		}
		raw[i] = b
	}
	return hex.EncodeToString(Root(raw)), nil
}

// Check is one verification step.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Result is the outcome of verifying a bundle.
type Result struct {
	Seq    int64   `json:"seq"`
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

func (r *Result) add(name string, ok bool, detail string) {
	r.Checks = append(r.Checks, Check{name, ok, detail})
	if !ok {
		r.OK = false
	}
}

// Verify checks a bundle: the signature (against trusted when given, otherwise only against the
// public key the bundle carries, which proves integrity but not who signed), the document hash
// shape, and, when evidence is attached, the audit chain slice and the event root.
func Verify(b Bundle, trusted ed25519.PublicKey) Result {
	r := Result{Seq: b.Checkpoint.Seq, OK: true}
	d := b.Checkpoint
	r.add("format", d.Version == Version && d.Type == Type && b.Signature.Alg == SigAlgorithm, fmt.Sprintf("type %q version %d alg %q", d.Type, d.Version, b.Signature.Alg))
	carried, err := ParsePublicKey(b.Signature.PublicKey)
	if err != nil {
		r.add("signature", false, err.Error())
		return r
	}
	sig, err := base64.RawURLEncoding.DecodeString(b.Signature.Value)
	msg, merr := SigningBytes(d)
	switch {
	case err != nil || merr != nil || len(sig) != ed25519.SignatureSize:
		r.add("signature", false, "malformed signature")
	case !ed25519.Verify(carried, msg, sig):
		r.add("signature", false, "signature does not match the document")
	default:
		r.add("signature", true, "valid Ed25519 signature over the checkpoint document")
	}
	if b.Signature.KeyID != d.KeyID {
		r.add("key id", false, "signature key id differs from the signed document")
	}
	if trusted != nil {
		r.add("trusted key", bytes.Equal(trusted, carried), "signer key compared with the key given to --pubkey")
	} else {
		r.add("trusted key", true, "not checked: no trusted public key given (integrity only, not origin)")
	}
	if ev := b.Evidence; ev != nil {
		verifyEvidence(&r, d, ev)
	}
	return r
}

func verifyEvidence(r *Result, d Doc, ev *Evidence) {
	prev, seq := ev.AuditFromHash, ev.AuditFromSeq
	ok, detail := true, fmt.Sprintf("%d audit entries from seq %d", len(ev.Audit), seq+1)
	for _, e := range ev.Audit {
		if e.Seq != seq+1 || e.PrevHash != prev || audit.Hash(e) != e.Hash {
			ok, detail = false, fmt.Sprintf("audit chain breaks at seq %d", e.Seq)
			break
		}
		prev, seq = e.Hash, e.Seq
	}
	if ok && (seq != d.Audit.Seq || prev != d.Audit.Hash) {
		ok, detail = false, fmt.Sprintf("audit slice ends at seq %d, checkpoint says %d", seq, d.Audit.Seq)
	}
	r.add("audit chain", ok, detail)
	root, err := RootOfLeaves(ev.Leaves)
	switch {
	case err != nil:
		r.add("event root", false, err.Error())
	case int64(len(ev.Leaves)) != d.Events.Count:
		r.add("event root", false, fmt.Sprintf("%d leaves, checkpoint says %d events", len(ev.Leaves), d.Events.Count))
	default:
		r.add("event root", root == d.Events.Root, fmt.Sprintf("Merkle root over %d event leaves", len(ev.Leaves)))
	}
}

// VerifyChain checks a set of bundles of one tenant: each verifies, and consecutive sequence
// numbers link by document hash and do not move the audit head backwards.
func VerifyChain(bundles []Bundle, trusted ed25519.PublicKey) []Result {
	sort.SliceStable(bundles, func(i, j int) bool { return bundles[i].Checkpoint.Seq < bundles[j].Checkpoint.Seq })
	out := make([]Result, 0, len(bundles))
	for i, b := range bundles {
		res := Verify(b, trusted)
		if i > 0 {
			p := bundles[i-1].Checkpoint
			if b.Checkpoint.TenantID != p.TenantID {
				res.add("tenant", false, "checkpoint belongs to another tenant than the one before it")
			}
			if b.Checkpoint.Seq == p.Seq+1 {
				h, _ := DocHash(p)
				res.add("link", b.Checkpoint.Previous != nil && b.Checkpoint.Previous.Seq == p.Seq && b.Checkpoint.Previous.DocHash == h, "previous checkpoint hash")
			}
			res.add("audit head", b.Checkpoint.Audit.Seq >= p.Audit.Seq, "audit head does not move backwards")
		}
		out = append(out, res)
	}
	return out
}
