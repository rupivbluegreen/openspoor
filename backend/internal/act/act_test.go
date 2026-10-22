package act_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/act"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

type action struct {
	ID, Status, Kind, Title, Risk string
	Reasoning                     []string
	Reversible                    bool
}

func actOf(e *testkit.Env) *act.Module {
	for _, m := range e.App.Modules {
		if a, ok := m.(*act.Module); ok {
			return a
		}
	}
	return nil
}

type fakeLLM struct{ system string }

func (f *fakeLLM) Name() string { return "fake" }
func (f *fakeLLM) Answer(_ context.Context, system string, msgs []act.ChatMessage) (string, error) {
	f.system = system
	return "grounded answer to: " + msgs[len(msgs)-1].Content, nil
}

func TestAgentsApprovalsAndControlTower(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Old stock"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 3}, "Idempotency-Key", "k"), &a)
	var epcs []string
	for _, s := range a.Serials {
		u, _ := gs1.SGTINURN(gtin, s, 7)
		epcs = append(epcs, u)
	}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active",
		EventTime: time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC), EventTimeZoneOffset: "+00:00", EPCList: epcs,
		ILMD: map[string]any{"lotNumber": "OLD-1", "itemExpirationDate": "2020-01-31"}}}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()

	// An exception: a public scan of a never-issued serial raises an alert.
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": "(01)" + gtin + "(21)FAKE123"}), nil)

	// Exception triage proposes a case; a Viewer cannot approve.
	var run struct{ Proposed int }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/exception-triage/run", nil), &run)
	if run.Proposed < 1 {
		t.Fatalf("proposals: %d", run.Proposed)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/exception-triage/run", nil), &run)
	if run.Proposed != 0 {
		t.Fatal("agent proposed a duplicate")
	}
	var acts struct{ Items []action }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions?status=pending", nil), &acts)
	var triage action
	for _, x := range acts.Items {
		if strings.Contains(x.Title, "FAKE123") {
			triage = x
		}
	}
	if triage.ID == "" || len(triage.Reasoning) < 2 || triage.Kind != "case.open" {
		t.Fatalf("action: %+v", acts.Items)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "v@acme.test", "roles": []string{"Viewer"}, "password": testkit.Password}), nil)
	var vt struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "v@acme.test", "password": testkit.Password}), &vt)
	e.Must(http.StatusForbidden, e.Call(vt.Token, "POST", "/api/v1/agents/actions/"+triage.ID+"/approve", nil), nil)
	var done action
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+triage.ID+"/approve", nil), &done)
	if done.Status != "executed" {
		t.Fatalf("approve: %+v", done)
	}

	// The case is first in the control tower queue (patient-safety alert = priority 1).
	var ct struct {
		Monitors []struct {
			ID     string
			Value  float64
			Status string
		}
		Queue []struct {
			Title    string
			Priority int
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/control-tower", nil), &ct)
	if len(ct.Queue) != 1 || ct.Queue[0].Priority != 1 || len(ct.Monitors) < 8 || !strings.Contains(ct.Queue[0].Title, "FAKE123") {
		t.Fatalf("control tower: %+v", ct)
	}

	// Revert closes the case again; the audit trail records every step.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+triage.ID+"/revert", nil), nil)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/control-tower", nil), &ct)
	if len(ct.Queue) != 0 {
		t.Fatalf("revert did not resolve the case: %+v", ct.Queue)
	}
	var aud struct{ Items []struct{ Action, Actor string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=agent.", nil), &aud)
	if len(aud.Items) < 3 {
		t.Fatalf("agent audit trail: %+v", aud.Items)
	}

	// High-risk tool: expiry quarantine needs an e-signature on approval; it is reversible.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/expiry-quarantine/run", nil), &run)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions?status=proposed", nil), &acts)
	var q action
	for _, x := range acts.Items {
		if x.Kind == "serials.set_status" {
			q = x
		}
	}
	if q.Risk != "high" || !q.Reversible || !strings.Contains(q.Title, "3 packs") {
		t.Fatalf("quarantine proposal: %+v", acts.Items)
	}
	e.Must(http.StatusPreconditionRequired, e.Do("POST", "/api/v1/agents/actions/"+q.ID+"/approve", nil), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Approved", "reason": "expired stock", "entity": "agent_action", "entityId": q.ID}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+q.ID+"/approve", nil), &done)
	var st struct{ State struct{ Status string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/?id="+epcs[0], nil), &st)
	if st.State.Status != "expired" {
		t.Fatalf("after quarantine: %+v", st)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+q.ID+"/revert", nil), nil)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/?id="+epcs[0], nil), &st)
	if st.State.Status != "active" {
		t.Fatalf("after revert: %+v", st)
	}

	// Autonomy: low-risk reversible tools may run automatically when the tenant opts in.
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": "(01)" + gtin + "(21)FAKE999"}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/agents/exception-triage", map[string]any{"enabled": true, "autonomy": "auto_low_risk"}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/exception-triage/run", nil), &run)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions?status=executed", nil), &acts)
	if len(acts.Items) < 1 {
		t.Fatalf("auto-executed actions: %+v", acts.Items)
	}
}

func TestWorkflowsFormsAndAssistant(t *testing.T) {
	e := testkit.New(t)
	var procs struct{ Items []struct{ Key string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/processes", nil), &procs)
	if len(procs.Items) != 3 {
		t.Fatalf("process library: %+v", procs.Items)
	}
	type run struct {
		ID, Status  string
		Step        int
		CurrentStep *struct{ Key string }
	}
	var r run
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/processes/batch-release/start", map[string]any{"context": map[string]string{"batch": "B-77"}}), &r)
	adv := func(status int, data map[string]any) {
		t.Helper()
		if status != http.StatusOK {
			e.Must(status, e.Do("POST", "/api/v1/workflow-runs/"+r.ID+"/advance", map[string]any{"data": data}), nil)
			return
		}
		e.Must(status, e.Do("POST", "/api/v1/workflow-runs/"+r.ID+"/advance", map[string]any{"data": data}), &r)
	}
	adv(http.StatusUnprocessableEntity, map[string]any{"batch": "B-77"})
	adv(http.StatusOK, map[string]any{"batch": "B-77", "deviations": 0})
	adv(http.StatusUnprocessableEntity, map[string]any{"assayPct": "high", "micro": "pass"})
	adv(http.StatusOK, map[string]any{"assayPct": 99.4, "micro": "pass"})
	if r.CurrentStep == nil || r.CurrentStep.Key != "qp_certification" {
		t.Fatalf("expected QP step: %+v", r)
	}
	e.Must(http.StatusPreconditionRequired, e.Do("POST", "/api/v1/workflow-runs/"+r.ID+"/advance", nil), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "QP certification", "reason": "batch conforms", "entity": "workflow_run", "entityId": r.ID}), nil)
	adv(http.StatusOK, nil)
	adv(http.StatusOK, nil)
	if r.Status != "completed" {
		t.Fatalf("batch release: %+v", r)
	}

	// Designer: custom workflow and a form with validation.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Cold chain excursion", "steps": []map[string]any{{"key": "assess", "name": "Assess", "role": "Ops"}, {"key": "decide", "name": "Decide", "role": "Compliance", "requiresSignature": true}}}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Bad", "steps": []map[string]any{{"key": "a", "name": "A", "role": "Wizard"}}}), nil)
	var f struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/forms", map[string]any{"name": "Temperature log", "schema": map[string]any{"type": "object",
		"properties": map[string]any{"tempC": map[string]any{"type": "number"}, "probe": map[string]any{"type": "string", "pattern": "^P-[0-9]+$"}}, "required": []string{"tempC"}}}), &f)
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/forms/"+f.ID+"/submit", map[string]any{"data": map[string]any{"probe": "X"}}), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/forms/"+f.ID+"/submit", map[string]any{"data": map[string]any{"tempC": 4.5, "probe": "P-12"}}), nil)

	// Assistant without a model: deterministic answer grounded in the workspace.
	var rep struct {
		Reply     string
		Citations []any
		Model     string
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/assistant/chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": "How is my workspace doing?"}}}), &rep)
	if rep.Model != "none" || !strings.Contains(rep.Reply, "EPCIS events") {
		t.Fatalf("offline assistant: %+v", rep)
	}
	// With a model: the system prompt carries the tenant's own context.
	fake := &fakeLLM{}
	actOf(e).LLM = fake
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/assistant/chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": "What about urn:epc:id:sgtin:9506000.013435.X1?"}}}), &rep)
	if rep.Model != "fake" || !strings.Contains(fake.system, "lookup:urn:epc:id:sgtin:9506000.013435.X1") || !strings.Contains(fake.system, "openAlerts") {
		t.Fatalf("grounding: %+v\n%s", rep, fake.system)
	}
}
