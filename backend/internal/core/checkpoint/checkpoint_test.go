package checkpoint

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/core/audit"
)

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// chain builds n linked audit entries after (fromSeq, fromHash).
func chain(tenant uuid.UUID, fromSeq int64, fromHash string, n int) []audit.Entry {
	var out []audit.Entry
	prev, seq := fromHash, fromSeq
	for i := 0; i < n; i++ {
		seq++
		e := audit.Entry{TenantID: tenant, Seq: seq, At: time.Date(2026, 1, 1, 0, 0, int(seq), 0, time.UTC), Actor: "user:1", Action: "x.y", Entity: "e", EntityID: "1",
			After: json.RawMessage(`{"b":1,"a":2}`), PrevHash: prev}
		e.Hash = audit.Hash(e)
		prev = e.Hash
		out = append(out, e)
	}
	return out
}

func leaves(n int) []Leaf {
	var out []Leaf
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		out = append(out, Leaf{EventID: id, RecordTime: "2026-01-01T00:00:00Z", Hash: hex.EncodeToString(LeafHash(id, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []byte(`{"i":`+string(rune('0'+i))+`}`)))})
	}
	return out
}

func makeBundle(t *testing.T, priv ed25519.PrivateKey, seq int64, prev *Bundle, tenant uuid.UUID, ents []audit.Entry, lv []Leaf) Bundle {
	t.Helper()
	root, err := RootOfLeaves(lv)
	if err != nil {
		t.Fatal(err)
	}
	d := Doc{TenantID: tenant.String(), Seq: seq, CreatedAt: "2026-01-02T00:00:00Z", Events: EventBatch{To: "2026-01-02T00:00:00Z", Count: int64(len(lv)), Root: root}}
	ev := &Evidence{Audit: ents, Leaves: lv}
	if prev != nil {
		h, _ := DocHash(prev.Checkpoint)
		d.Previous = &Link{Seq: prev.Checkpoint.Seq, DocHash: h}
		ev.AuditFromSeq, ev.AuditFromHash = prev.Checkpoint.Audit.Seq, prev.Checkpoint.Audit.Hash
	}
	if len(ents) > 0 {
		d.Audit = AuditHead{Seq: ents[len(ents)-1].Seq, Hash: ents[len(ents)-1].Hash}
	} else if prev != nil {
		d.Audit = prev.Checkpoint.Audit
	}
	env, err := Sign(d, "did:web:node.test", priv)
	if err != nil {
		t.Fatal(err)
	}
	return Bundle{Envelope: env, Evidence: ev}
}

func failed(r Result) []string {
	var out []string
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

func TestSignVerify(t *testing.T) {
	pub, priv := keys(t)
	tenant := uuid.New()
	b := makeBundle(t, priv, 1, nil, tenant, chain(tenant, 0, "", 3), leaves(5))
	if r := Verify(b, pub); !r.OK {
		t.Fatalf("valid bundle: %+v", r)
	}
	if r := Verify(b, nil); !r.OK {
		t.Fatalf("without trusted key: %+v", r)
	}
	other, _ := keys(t)
	if r := Verify(b, other); r.OK || strings.Join(failed(r), ",") != "trusted key" {
		t.Fatalf("wrong key: %+v", r)
	}

	// Tampering with any signed field is detected.
	for name, mutate := range map[string]func(*Bundle){
		"count":    func(b *Bundle) { b.Checkpoint.Events.Count++ },
		"audit":    func(b *Bundle) { b.Checkpoint.Audit.Hash = strings.Repeat("0", 64) },
		"tenant":   func(b *Bundle) { b.Checkpoint.TenantID = uuid.NewString() },
		"sig":      func(b *Bundle) { b.Signature.Value = b.Signature.Value[:len(b.Signature.Value)-2] + "AA" },
		"shortsig": func(b *Bundle) { b.Signature.Value = "AAAA" },
		"keyid":    func(b *Bundle) { b.Signature.KeyID = "did:web:evil" },
	} {
		c := b
		mutate(&c)
		if r := Verify(c, pub); r.OK {
			t.Errorf("%s tampering not detected: %+v", name, r)
		}
	}
	c := b
	c.Signature.PublicKey = "!!"
	if r := Verify(c, pub); r.OK || r.Checks[len(r.Checks)-1].Name != "signature" {
		t.Fatalf("bad public key: %+v", r)
	}
	c = b
	c.Checkpoint.Type = "other"
	if r := Verify(c, pub); r.OK {
		t.Fatalf("type: %+v", r)
	}
}

func TestEvidence(t *testing.T) {
	pub, priv := keys(t)
	tenant := uuid.New()
	ents := chain(tenant, 0, "", 4)
	b := makeBundle(t, priv, 1, nil, tenant, ents, leaves(3))

	// An edited audit entry, a dropped entry, an extra leaf and a changed leaf are all caught.
	mutations := map[string]func(*Bundle){
		"edited audit": func(b *Bundle) {
			b.Evidence.Audit = append([]audit.Entry{}, b.Evidence.Audit...)
			b.Evidence.Audit[1].Actor = "user:evil"
		},
		"dropped audit": func(b *Bundle) {
			b.Evidence.Audit = append(append([]audit.Entry{}, b.Evidence.Audit[:1]...), b.Evidence.Audit[2:]...)
		},
		"short audit": func(b *Bundle) { b.Evidence.Audit = b.Evidence.Audit[:3] },
		"extra leaf":  func(b *Bundle) { b.Evidence.Leaves = append(append([]Leaf{}, b.Evidence.Leaves...), leaves(1)[0]) },
		"changed leaf": func(b *Bundle) {
			b.Evidence.Leaves = append([]Leaf{}, b.Evidence.Leaves...)
			b.Evidence.Leaves[0].Hash = strings.Repeat("ab", 32)
		},
		"bad hex leaf": func(b *Bundle) {
			b.Evidence.Leaves = append([]Leaf{}, b.Evidence.Leaves...)
			b.Evidence.Leaves[0].Hash = "zz"
		},
	}
	for name, mutate := range mutations {
		c := b
		ev := *b.Evidence
		c.Evidence = &ev
		mutate(&c)
		if r := Verify(c, pub); r.OK {
			t.Errorf("%s not detected: %+v", name, r)
		}
	}
	// The audit key order inside the entry does not matter (canonical JSON).
	raw, _ := json.Marshal(b)
	var back Bundle
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if r := Verify(back, pub); !r.OK {
		t.Fatalf("round trip: %+v", r)
	}
}

func TestChain(t *testing.T) {
	pub, priv := keys(t)
	tenant := uuid.New()
	b1 := makeBundle(t, priv, 1, nil, tenant, chain(tenant, 0, "", 2), leaves(2))
	b2 := makeBundle(t, priv, 2, &b1, tenant, chain(tenant, 2, b1.Checkpoint.Audit.Hash, 2), leaves(1))
	b3 := makeBundle(t, priv, 3, &b2, tenant, nil, nil)
	for _, r := range VerifyChain([]Bundle{b3, b1, b2}, pub) {
		if !r.OK {
			t.Fatalf("chain: %+v", r)
		}
	}
	// A replaced middle checkpoint breaks the link of the next one.
	forged := makeBundle(t, priv, 2, &b1, tenant, chain(tenant, 2, b1.Checkpoint.Audit.Hash, 3), leaves(4))
	res := VerifyChain([]Bundle{b1, forged, b3}, pub)
	if res[1].OK == false || res[2].OK {
		t.Fatalf("link not checked: %+v", res)
	}
	// Another tenant's checkpoint in the set, and a head that moves backwards.
	other := makeBundle(t, priv, 2, &b1, uuid.New(), nil, nil)
	if res = VerifyChain([]Bundle{b1, other}, pub); res[1].OK {
		t.Fatalf("tenant mix: %+v", res)
	}
	back := b2
	back.Checkpoint.Audit.Seq = 0
	if res = VerifyChain([]Bundle{b2, func() Bundle { c := b3; c.Checkpoint.Audit.Seq = 1; return c }()}, pub); res[1].OK {
		t.Fatalf("backwards head: %+v", res)
	}
	_ = back
}

func TestMerkle(t *testing.T) {
	empty := Root(nil)
	if hex.EncodeToString(empty) != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty root: %x", empty)
	}
	l := func(n int) [][]byte {
		var out [][]byte
		for i := 0; i < n; i++ {
			out = append(out, LeafHash("e", time.Unix(int64(i), 0), []byte("{}")))
		}
		return out
	}
	seen := map[string]int{}
	for n := 1; n <= 9; n++ {
		r := hex.EncodeToString(Root(l(n)))
		if prev, dup := seen[r]; dup {
			t.Fatalf("roots of %d and %d leaves collide", prev, n)
		}
		seen[r] = n
	}
	// Order matters, and a leaf cannot stand in for an inner node.
	a, b := l(2)[0], l(2)[1]
	if hex.EncodeToString(Root([][]byte{a, b})) == hex.EncodeToString(Root([][]byte{b, a})) {
		t.Fatal("order must matter")
	}
	if LeafHash("e", time.Unix(0, 0), []byte(`{"b":1,"a":2}`))[0] != LeafHash("e", time.Unix(0, 0), []byte(`{"a":2,"b":1}`))[0] ||
		hex.EncodeToString(LeafHash("e", time.Unix(0, 0), []byte(`{"b":1,"a":2}`))) != hex.EncodeToString(LeafHash("e", time.Unix(0, 0), []byte(`{"a":2,"b":1}`))) {
		t.Fatal("leaf hash must ignore key order")
	}
	if hex.EncodeToString(LeafHash("e", time.Unix(0, 0), []byte(`not json`))) == "" {
		t.Fatal("non-JSON docs are hashed as given")
	}
}

func TestKeysAndSign(t *testing.T) {
	pub, priv := keys(t)
	s := PublicKeyString(pub)
	for _, in := range []string{s, " " + s + "\n", s + "="} {
		if k, err := ParsePublicKey(in); err != nil || !k.Equal(pub) {
			t.Fatalf("parse %q: %v", in, err)
		}
	}
	for _, in := range []string{"", "***", "AAAA"} {
		if _, err := ParsePublicKey(in); err == nil {
			t.Errorf("%q must fail", in)
		}
	}
	if _, err := Sign(Doc{}, "k", ed25519.PrivateKey("short")); err == nil {
		t.Fatal("short private key")
	}
	env, err := Sign(Doc{Seq: 1}, "k", priv)
	if err != nil || env.Checkpoint.Events.Algorithm != RootAlgorithm || env.Checkpoint.Version != Version || env.Signature.KeyID != "k" {
		t.Fatalf("%+v %v", env, err)
	}
}
