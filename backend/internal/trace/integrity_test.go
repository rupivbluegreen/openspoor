package trace_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/checkpoint"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/internal/trace"
)

func traceModule(t *testing.T, e *testkit.Env) *trace.Module {
	t.Helper()
	for _, m := range e.App.Modules {
		if tm, ok := m.(*trace.Module); ok {
			tm.SettleLag = 0
			return tm
		}
	}
	t.Fatal("no trace module")
	return nil
}

type cpRow struct {
	Seq        int64
	AuditSeq   int64
	EventCount int64
	EventRoot  string
	DocHash    string
	KeyID      string
}

func nodeKey(t *testing.T, e *testkit.Env) (pub string) {
	t.Helper()
	var n struct{ KeyId, PublicKey, Algorithm string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/integrity/node", nil), &n)
	if n.Algorithm != "Ed25519" || n.PublicKey == "" || n.KeyId == "" {
		t.Fatalf("node: %+v", n)
	}
	return n.PublicKey
}

func bundle(t *testing.T, e *testkit.Env, seq string, evidence bool) checkpoint.Bundle {
	t.Helper()
	q := ""
	if evidence {
		q = "?evidence=1"
	}
	var b checkpoint.Bundle
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/integrity/checkpoints/"+seq+q, nil), &b)
	return b
}

func TestCheckpoints(t *testing.T) {
	e := testkit.New(t)
	traceModule(t, e)
	scenario(t, e)
	viewer := userToken(t, e, "acme", "viewer@acme.test", "Viewer")
	_, gtok := e.NewTenant("globex", "admin@globex.test")
	pubStr := nodeKey(t, e)
	pub, err := checkpoint.ParsePublicKey(pubStr)
	if err != nil {
		t.Fatal(err)
	}

	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/trace/integrity/checkpoints", nil), nil)
	var c1 cpRow
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/trace/integrity/checkpoints", nil), &c1)
	if c1.Seq != 1 || c1.EventCount != 7 || c1.AuditSeq < 1 || c1.EventRoot == "" {
		t.Fatalf("first checkpoint: %+v", c1)
	}
	// Only the checkpoint's own audit entry is new: nothing to sign.
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/trace/integrity/checkpoints", nil), nil)

	// Offline-verifiable export, with and without evidence.
	b1 := bundle(t, e, "1", true)
	if res := checkpoint.Verify(b1, pub); !res.OK || b1.Evidence == nil || len(b1.Evidence.Leaves) != 7 || len(b1.Evidence.Audit) == 0 {
		t.Fatalf("verify first: %+v", res)
	}
	if bare := bundle(t, e, "1", false); bare.Evidence != nil || !checkpoint.Verify(bare, pub).OK {
		t.Fatalf("bare export: %+v", bare)
	}

	// A later change produces a linked second checkpoint whose audit slice starts at the first.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/trace/balance/settings", map[string]any{"toleranceUnits": 2, "checkpointHours": 24}), nil)
	var c2 cpRow
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/trace/integrity/checkpoints", nil), &c2)
	b2 := bundle(t, e, "2", true)
	if c2.Seq != 2 || c2.EventCount != 0 || b2.Checkpoint.Previous == nil || b2.Checkpoint.Previous.DocHash != c1.DocHash || b2.Evidence.AuditFromSeq != c1.AuditSeq {
		t.Fatalf("second checkpoint: %+v %+v", c2, b2.Checkpoint)
	}
	for _, r := range checkpoint.VerifyChain([]checkpoint.Bundle{b2, b1}, pub) {
		if !r.OK {
			t.Fatalf("chain: %+v", r)
		}
	}

	var list struct{ Items []cpRow }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/trace/integrity/checkpoints", nil), &list)
	if len(list.Items) != 2 || list.Items[0].Seq != 2 {
		t.Fatalf("list: %+v", list)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/trace/integrity/checkpoints/9", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/trace/integrity/checkpoints/zero", nil), nil)

	// Other tenants see none of it.
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/trace/integrity/checkpoints", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("leak: %+v", list)
	}
	e.Must(http.StatusNotFound, e.Call(gtok, "GET", "/api/v1/trace/integrity/checkpoints/1", nil), nil)

	// Online verification is clean, then catches an edited event.
	var v struct {
		OK         bool
		AuditChain struct {
			OK      bool
			Checked int64
		}
		Checkpoints []struct {
			OK     bool
			Checks []struct {
				Name string
				OK   bool
			}
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/trace/integrity/verify", nil), &v)
	if !v.OK || !v.AuditChain.OK || len(v.Checkpoints) != 2 {
		t.Fatalf("verify: %+v", v)
	}
	superExec(t, e, `update epcis_event set doc = doc || '{"tampered": true}'::jsonb where event_id = (select event_id from epcis_event order by record_time, event_id limit 1)`)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/integrity/verify", nil), &v)
	if v.OK || v.Checkpoints[0].OK && v.Checkpoints[1].OK {
		t.Fatalf("tampering not detected: %+v", v)
	}

	// Stored checkpoints cannot be changed or removed, even by the database owner.
	for _, sql := range []string{`delete from integrity_checkpoint`, `update integrity_checkpoint set event_root='x'`, `truncate integrity_checkpoint`} {
		c, err := pgx.Connect(context.Background(), e.SuperURL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(context.Background(), sql); err == nil {
			t.Fatalf("%s must fail", sql)
		}
		c.Close(context.Background())
	}

	var au struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=200", nil), &au)
	n := 0
	for _, a := range au.Items {
		if a.Action == "trace.checkpoint.create" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("audit entries for checkpoints: %d", n)
	}
	if _, err := json.Marshal(b2); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointSchedule(t *testing.T) {
	e := testkit.New(t)
	traceModule(t, e)
	scenario(t, e)
	var list struct{ Items []cpRow }
	runSchedule(t, e, "trace.checkpoint")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/integrity/checkpoints", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("scheduled checkpoint: %+v", list)
	}
	runSchedule(t, e, "trace.checkpoint") // interval not elapsed
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/integrity/checkpoints", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("interval ignored: %+v", list)
	}
	// Switched off for the tenant: nothing is signed even when the last one is old.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/trace/balance/settings", map[string]any{"checkpointHours": 0}), nil)
	runSchedule(t, e, "trace.checkpoint")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/integrity/checkpoints", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("disabled checkpoints still signed: %+v", list)
	}
}
