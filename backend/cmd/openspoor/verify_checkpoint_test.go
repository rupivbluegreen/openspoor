package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/checkpoint"
	"github.com/openspoor/openspoor/internal/testkit"
)

func writeBundle(t *testing.T, dir, name string, priv ed25519.PrivateKey, seq int64, prev *checkpoint.Bundle) checkpoint.Bundle {
	t.Helper()
	root, _ := checkpoint.RootOfLeaves(nil)
	d := checkpoint.Doc{TenantID: "t1", Seq: seq, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Events: checkpoint.EventBatch{To: "2026-01-01T00:00:00Z", Root: root}}
	if prev != nil {
		h, _ := checkpoint.DocHash(prev.Checkpoint)
		d.Previous = &checkpoint.Link{Seq: prev.Checkpoint.Seq, DocHash: h}
	}
	env, err := checkpoint.Sign(d, "did:web:node.test", priv)
	if err != nil {
		t.Fatal(err)
	}
	b := checkpoint.Bundle{Envelope: env, Evidence: &checkpoint.Evidence{Audit: nil, Leaves: nil}}
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyCheckpointCommand(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	b1 := writeBundle(t, dir, "1.json", priv, 1, nil)
	writeBundle(t, dir, "2.json", priv, 2, &b1)
	pubFile := filepath.Join(dir, "pub.txt")
	_ = os.WriteFile(pubFile, []byte(checkpoint.PublicKeyString(pub)+"\n"), 0o600)
	f := func(n string) string { return filepath.Join(dir, n) }

	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runVerifyCheckpoint(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, out, _ := run(f("1.json"), f("2.json")); code != 0 || !strings.Contains(out, "all checks passed") || !strings.Contains(out, "not checked: no trusted public key") {
		t.Fatalf("plain: %d %s", code, out)
	}
	if code, out, _ := run("--pubkey", checkpoint.PublicKeyString(pub), f("1.json"), f("2.json")); code != 0 || strings.Contains(out, "not checked") {
		t.Fatalf("with key: %d %s", code, out)
	}
	if code, _, _ := run("--pubkey", "@"+pubFile, f("1.json")); code != 0 {
		t.Fatalf("key file: %d", code)
	}
	code, out, _ := run("--json", "--pubkey", checkpoint.PublicKeyString(pub), f("1.json"), f("2.json"))
	var res struct {
		OK      bool
		Results []struct{ Seq int64 }
	}
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || !res.OK || len(res.Results) != 2 {
		t.Fatalf("json: %d %s", code, out)
	}

	// Wrong key and a tampered file fail with status 1.
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if code, out, _ := run("--pubkey", checkpoint.PublicKeyString(other), f("1.json")); code != 1 || !strings.Contains(out, "VERIFICATION FAILED") || !strings.Contains(out, "FAIL trusted key") {
		t.Fatalf("wrong key: %d %s", code, out)
	}
	raw, _ := os.ReadFile(f("2.json"))
	_ = os.WriteFile(f("bad.json"), bytes.Replace(raw, []byte(`"seq":2`), []byte(`"seq":3`), 1), 0o600)
	if code, out, _ := run(f("bad.json")); code != 1 || !strings.Contains(out, "FAIL signature") {
		t.Fatalf("tampered: %d %s", code, out)
	}
	if code, _, _ := run("--json", f("bad.json")); code != 1 {
		t.Fatalf("tampered json: %d", code)
	}

	// Usage and input errors are status 2.
	_ = os.WriteFile(f("junk.json"), []byte(`{"hello":1}`), 0o600)
	_ = os.WriteFile(f("notjson.json"), []byte(`nope`), 0o600)
	for _, args := range [][]string{{}, {f("missing.json")}, {f("junk.json")}, {f("notjson.json")}, {"--pubkey", "@" + f("missing.txt"), f("1.json")}, {"--pubkey", "zzz", f("1.json")}, {"--bogus"}} {
		if code, _, errb := run(args...); code != 2 {
			t.Errorf("%v: code %d, stderr %q", args, code, errb)
		}
	}
}

// TestVerifyCheckpointEndToEnd exports a checkpoint from a running node and checks it offline.
func TestVerifyCheckpointEndToEnd(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/trace/balance/settings", map[string]any{"toleranceUnits": 1, "checkpointHours": 24}), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/trace/integrity/checkpoints", nil), nil)
	var node struct{ PublicKey string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/integrity/node", nil), &node)
	dir := t.TempDir()
	res := e.Do("GET", "/api/v1/trace/integrity/checkpoints/1?evidence=1", nil)
	e.Must(http.StatusOK, res, nil)
	good := filepath.Join(dir, "cp1.json")
	if err := os.WriteFile(good, res.Body, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runVerifyCheckpoint([]string{"--pubkey", node.PublicKey, good}, &out, &errb); code != 0 || !strings.Contains(out.String(), "ok   audit chain") {
		t.Fatalf("export does not verify: %d\n%s%s", code, out.String(), errb.String())
	}
	// Editing one audited action in the exported evidence is caught by the offline check.
	bad := filepath.Join(dir, "cp1-edited.json")
	if err := os.WriteFile(bad, bytes.Replace(res.Body, []byte("trace.settings.update"), []byte("trace.settings.upd4te"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runVerifyCheckpoint([]string{"--pubkey", node.PublicKey, bad}, &out, &errb); code != 1 || !strings.Contains(out.String(), "FAIL audit chain") {
		t.Fatalf("edited evidence passed: %d\n%s", code, out.String())
	}
}
