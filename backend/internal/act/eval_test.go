package act_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/act"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

// TestGoldenDatasets is the CI gate: every governed agent must meet its dataset's pass threshold.
// The datasets are synthetic and small; passing says the agent behaves as the dataset expects.
func TestGoldenDatasets(t *testing.T) {
	e := testkit.New(t)
	res, err := actOf(e).RunEvals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exception-triage", "inspection-agent", "investigation-assistant", "mapping-agent", "onboarding-helper", "reg-watch"}
	if len(res) != len(want) {
		t.Fatalf("%d agents evaluated, want %d", len(res), len(want))
	}
	for i, r := range res {
		if r.Agent != want[i] || !r.Synthetic || r.Cases < 5 {
			t.Errorf("unexpected result %+v", r)
		}
		if r.Error != "" {
			t.Errorf("%s: %s", r.Agent, r.Error)
		}
		if !r.Pass {
			t.Errorf("%s scored %.4f, threshold %.2f", r.Agent, r.Score, r.Threshold)
		}
		for _, f := range r.Failures {
			t.Errorf("%s / %s: %s", r.Agent, f.Case, f.Detail)
		}
	}
	var sb strings.Builder
	act.FormatEvalTable(&sb, res)
	t.Log("\n" + sb.String())
}

// recordEvals runs the golden datasets through the API so the tenant has recorded scores.
func recordEvals(t *testing.T, e *testkit.Env) {
	t.Helper()
	var out struct {
		Items []struct {
			Agent string
			Pass  bool
		}
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/evals/run", nil), &out)
	if len(out.Items) != 6 {
		t.Fatalf("evaluated %d agents", len(out.Items))
	}
	for _, i := range out.Items {
		if !i.Pass {
			t.Fatalf("%s did not pass its dataset", i.Agent)
		}
	}
}

func TestEvalScoresAndTheAutonomyLadder(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")
	qa := login(t, e, "qa@acme.test", "Compliance")
	type listing struct {
		Items []struct {
			Agent  string
			Latest *struct {
				Score    float64
				Meets    bool
				Required float64
			}
		}
		MinScore          float64
		MinScoreIsDefault bool
		Note              string
	}
	var l listing
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/evals", nil), &l)
	if len(l.Items) != 6 || l.Items[0].Latest != nil || l.MinScore != 0.9 || !l.MinScoreIsDefault || !strings.Contains(l.Note, "synthetic") {
		t.Fatalf("before any run: %+v", l)
	}

	// Roles and validation. Viewer is read-only.
	for _, tok := range []string{viewer, ops} {
		problem(t, e.Call(tok, "POST", "/api/v1/agents/evals/run", nil), 403, "")
		problem(t, e.Call(tok, "PUT", "/api/v1/agents/evals/policy", map[string]any{"minScore": 0.95}), 403, "")
	}
	problem(t, e.Call(qa, "POST", "/api/v1/agents/evals/run", map[string]any{"agents": []string{"nope"}}), 400, "no evaluation dataset for: nope")
	problem(t, e.Call(qa, "PUT", "/api/v1/agents/evals/policy", map[string]any{"minScore": 0.3}), 400, "between 0.5 and 1")
	problem(t, e.Call(qa, "POST", "/api/v1/agents/evals/run", "{"), 400, "")

	// One agent's score does not unlock a tool used by other evaluated agents.
	var one struct{ Items []struct{ Agent string } }
	e.Must(http.StatusOK, e.Call(qa, "POST", "/api/v1/agents/evals/run", map[string]any{"agents": []string{"exception-triage"}}), &one)
	if len(one.Items) != 1 || auditCount(t, e, "agent.eval.record") != 1 {
		t.Fatalf("one agent: %+v", one)
	}
	problem(t, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "auto"}), 409, "investigation-assistant: no evaluation score recorded")
	// Approve and suggest are never gated.
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "suggest"}), nil)
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "approve"}), nil)

	// A stricter tenant bar is audited and applies to the next decision.
	recordEvals(t, e)
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/evals/policy", map[string]any{"minScore": 0.97}), nil)
	if auditCount(t, e, "agent.eval.policy") != 1 {
		t.Fatal("policy change must be audited")
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/evals", nil), &l)
	if l.MinScore != 0.97 || l.MinScoreIsDefault || l.Items[0].Latest == nil || l.Items[0].Latest.Score != 1 || !l.Items[0].Latest.Meets {
		t.Fatalf("after run: %+v", l)
	}
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "auto"}), nil)

	// The score drops (a later, worse run): the auto setting stops applying and says why.
	superExec(t, e, `insert into agent_eval_score (id, tenant_id, agent, score, precision, recall, cases, passed_cases, pass_score, dataset, run_by, run_at)
		values (gen_random_uuid(), $1, 'exception-triage', 0.5, 0.5, 0.5, 9, 4, 0.95, 'x', 'test', now() + interval '1 minute')`, e.Tenant.ID)
	var tools struct {
		Items []struct {
			Name, Mode  string
			EvalBlocked []string
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/tools", nil), &tools)
	for _, x := range tools.Items {
		if x.Name == "case.open" && (x.Mode != "approve" || len(x.EvalBlocked) != 1 || !strings.Contains(x.EvalBlocked[0], "exception-triage: score 0.50 is below the required 0.97")) {
			t.Fatalf("case.open after the drop: %+v", x)
		}
	}
	alert(t, e, uuid.NewString(), "regulatory.rejection", "error", "After the drop")
	runAgent(t, e, "exception-triage")
	if len(agentActions(t, e, "exception-triage", "executed")) != 0 {
		t.Fatal("a blocked agent must not auto-apply")
	}
	pending := agentActions(t, e, "exception-triage", "pending")
	if len(pending) != 1 {
		t.Fatalf("pending: %+v", pending)
	}
	var full struct{ Autonomy string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions/"+pending[0].ID, nil), &full)
	if full.Autonomy != "approve" {
		t.Fatalf("autonomy shown %q", full.Autonomy)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions?status=pending", nil), nil)
	problem(t, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "auto"}), 409, "score 0.50 is below the required 0.97")
	if auditCount(t, e, "agent.tool.autonomy.refused") != 2 {
		t.Fatal("the refusal must be audited")
	}

	// A fresh run restores it.
	recordEvals(t, e)
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "auto"}), nil)
	runAgent(t, e, "exception-triage")
	alert(t, e, uuid.NewString(), "regulatory.rejection", "error", "After the rerun")
	runAgent(t, e, "exception-triage")
	if len(agentActions(t, e, "exception-triage", "executed")) == 0 {
		t.Fatal("auto should apply again")
	}
}

func TestRecordEvalsSkipsFailedRunsAndRunEvalsFilters(t *testing.T) {
	e := testkit.New(t)
	m := actOf(e)
	ctx := context.Background()
	res, err := m.RunEvals(ctx, "reg-watch", "reg-watch")
	if err != nil || len(res) != 1 || res[0].Agent != "reg-watch" || !res[0].Pass {
		t.Fatalf("filtered run: %+v %v", res, err)
	}
	if _, err := m.RunEvals(ctx, "no-such-agent"); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if err := m.RecordEvals(ctx, e.Tenant.ID, "system:test", []act.AgentEval{{Agent: "x", Error: "boom"}, res[0]}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.App.Deps.DB.Pool.QueryRow(ctx, `select count(*) from agent_eval_score`).Scan(&n); err == nil && n != 0 {
		t.Fatalf("RLS must hide rows without a tenant: %d", n)
	}
	var list struct {
		Items []struct {
			Agent  string
			Latest *struct{ RunBy string }
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/evals", nil), &list)
	got := 0
	for _, i := range list.Items {
		if i.Latest != nil {
			got++
			if i.Agent != "reg-watch" || i.Latest.RunBy != "system:test" {
				t.Fatalf("recorded: %+v", i)
			}
		}
	}
	if got != 1 {
		t.Fatalf("recorded %d scores", got)
	}
	// Scores are per tenant.
	if _, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "globex", Name: "Globex", AdminEmail: "admin@globex.test", AdminPassword: testkit.Password}); err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "globex", "email": "admin@globex.test", "password": testkit.Password}), &tok)
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/agents/evals", nil), &list)
	for _, i := range list.Items {
		if i.Latest != nil {
			t.Fatalf("another tenant's score is visible: %+v", i)
		}
	}
}
