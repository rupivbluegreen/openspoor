package act_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

func runAgent(t *testing.T, e *testkit.Env, name string) int {
	t.Helper()
	var r struct{ Proposed int }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/"+name+"/run", nil), &r)
	return r.Proposed
}

// agentActions lists one agent's actions in a status.
func agentActions(t *testing.T, e *testkit.Env, agent, status string) []fullAction {
	t.Helper()
	var out []fullAction
	for _, a := range actions(t, e, status) {
		if a.Agent == agent {
			out = append(out, a)
		}
	}
	return out
}

func signID(t *testing.T, e *testkit.Env, token, entity, id string) string {
	t.Helper()
	var s struct{ ID string }
	e.Must(http.StatusCreated, e.Call(token, "POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Approved", "reason": "release", "entity": entity, "entityId": id}), &s)
	return s.ID
}

func auditCount(t *testing.T, e *testkit.Env, prefix string) int {
	t.Helper()
	var p struct{ Total int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action="+prefix, nil), &p)
	return p.Total
}

func TestToolAutonomyLadder(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")
	qa := login(t, e, "qa@acme.test", "Compliance")

	var tools struct {
		Items []struct {
			Name, Risk, Mode string
			Reversible       bool
			AutoAllowed      bool
			Agents           []string
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/tools", nil), &tools)
	by := map[string]int{}
	for i, x := range tools.Items {
		by[x.Name] = i
	}
	for _, n := range []string{"case.open", "report.resubmit", "serials.set_status", "mapping.save", "investigation.assemble_answer", "inspection.assemble"} {
		if _, ok := by[n]; !ok {
			t.Fatalf("tool %s is not listed: %+v", n, tools.Items)
		}
	}
	if c := tools.Items[by["case.open"]]; c.Mode != "approve" || !c.AutoAllowed || len(c.Agents) < 5 {
		t.Fatalf("case.open: %+v", c)
	}
	if r := tools.Items[by["report.resubmit"]]; r.AutoAllowed || r.Reversible {
		t.Fatalf("report.resubmit must not allow auto: %+v", r)
	}

	// Validation and roles.
	problem(t, e.Do("PUT", "/api/v1/agents/tools/report.resubmit", map[string]any{"mode": "auto"}), 400, "auto is only allowed")
	problem(t, e.Do("PUT", "/api/v1/agents/tools/serials.set_status", map[string]any{"mode": "auto"}), 400, "auto is only allowed")
	problem(t, e.Do("PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "whenever"}), 400, "mode must be")
	problem(t, e.Do("PUT", "/api/v1/agents/tools/nope", map[string]any{"mode": "approve"}), 404, "unknown tool")
	problem(t, e.Call(ops, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "suggest"}), 403, "")
	problem(t, e.Call(viewer, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "suggest"}), 403, "")

	// Suggest only: the proposal exists but cannot be approved; it can be rejected.
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "suggest"}), nil)
	alert(t, e, uuid.NewString(), "regulatory.rejection", "error", "Hub said no")
	if n := runAgent(t, e, "exception-triage"); n != 1 {
		t.Fatalf("proposed %d", n)
	}
	a := agentActions(t, e, "exception-triage", "pending")[0]
	var full struct{ Autonomy string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions/"+a.ID, nil), &full)
	if full.Autonomy != "suggest" {
		t.Fatalf("autonomy %q", full.Autonomy)
	}
	problem(t, e.Do("POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), 409, "suggest only")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+a.ID+"/reject", map[string]any{"reason": "will do by hand"}), nil)

	// Without a recorded evaluation score, raising autonomy is refused (and the refusal is audited).
	before := auditCount(t, e, "agent.agent.autonomy.refused")
	problem(t, e.Do("PUT", "/api/v1/agents/exception-triage", map[string]any{"enabled": true, "autonomy": "auto_low_risk"}), 409, "no evaluation score recorded")
	problem(t, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "auto"}), 409, "no evaluation score recorded")
	if auditCount(t, e, "agent.agent.autonomy.refused") != before+1 || auditCount(t, e, "agent.tool.autonomy.refused") != 1 {
		t.Fatal("refusals must be audited")
	}
	recordEvals(t, e)

	// A tool setting wins over the agent setting: approve keeps proposals pending even when the
	// agent is configured for auto_low_risk.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/agents/exception-triage", map[string]any{"enabled": true, "autonomy": "auto_low_risk"}), nil)
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "approve"}), nil)
	alert(t, e, uuid.NewString(), "regulatory.rejection", "error", "Second rejection")
	runAgent(t, e, "exception-triage")
	pend := 0
	for _, x := range agentActions(t, e, "exception-triage", "pending") {
		if strings.Contains(x.Title, "Second") {
			pend++
		}
	}
	if pend != 1 {
		t.Fatalf("approve mode must keep the proposal pending")
	}

	// Auto: applied with audit and an undo.
	e.Must(http.StatusOK, e.Call(qa, "PUT", "/api/v1/agents/tools/case.open", map[string]any{"mode": "auto"}), nil)
	alert(t, e, uuid.NewString(), "regulatory.rejection", "error", "Third rejection")
	runAgent(t, e, "exception-triage")
	var auto fullAction
	for _, x := range agentActions(t, e, "exception-triage", "executed") {
		if strings.Contains(x.Title, "Third") {
			auto = x
		}
	}
	if auto.ID == "" || auto.Result["auto"] != true || !strings.HasSuffix(auto.Result["undo"].(string), auto.ID+"/revert") || auto.DecidedBy == nil || !strings.Contains(*auto.DecidedBy, "auto") {
		t.Fatalf("auto-applied action: %+v", auto)
	}
	if auditCount(t, e, "agent.action.executed") < 1 || auditCount(t, e, "agent.tool.autonomy") < 3 {
		t.Fatal("auto-apply and the autonomy changes must be audited")
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+auto.ID+"/revert", nil), nil)
	if got := agentActions(t, e, "exception-triage", "reverted"); len(got) != 1 {
		t.Fatalf("revert: %+v", got)
	}
}

const orderCSV = "PO Number,Order Date,Buyer GLN,GTIN,Qty,Lot,Notes\nPO-1,2026-01-05,9506000000008,09506000134352,10,L1,rush\nPO-1,2026-01-05,9506000000008,09506000134369,5,L2,\n"

func TestMappingAgent(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")
	sample := func(tok, name, docType, content string, extra map[string]any) testkit.Response {
		body := map[string]any{"name": name, "docType": docType, "content": content}
		for k, v := range extra {
			body[k] = v
		}
		return e.Call(tok, "POST", "/api/v1/agents/mapping/samples", body)
	}
	// Validation and roles.
	e.Must(http.StatusForbidden, sample(viewer, "x", "order", orderCSV, nil), nil)
	e.Must(http.StatusBadRequest, sample(e.Token, "", "order", orderCSV, nil), nil)
	e.Must(http.StatusBadRequest, sample(e.Token, "x", "memo", orderCSV, nil), nil)
	e.Must(http.StatusBadRequest, sample(e.Token, "x", "order", "  ", nil), nil)
	e.Must(http.StatusBadRequest, sample(e.Token, "x", "order", orderCSV, map[string]any{"format": "yaml"}), nil)
	e.Must(http.StatusBadRequest, sample(e.Token, "x", "order", orderCSV, map[string]any{"source": "email"}), nil)
	e.Must(http.StatusBadRequest, sample(e.Token, "x", "order", "", map[string]any{"contentBase64": "***"}), nil)
	e.Must(http.StatusBadRequest, sample(e.Token, "x", "order", orderCSV, map[string]any{"partnerId": uuid.NewString()}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/agents/mapping/samples", "{"), nil)

	var s struct{ ID, Status, Format string }
	e.Must(http.StatusCreated, sample(ops, "Acme orders", "order", orderCSV, map[string]any{"source": "failed_import", "error": "number is missing on row 3"}), &s)
	if s.Status != "new" || s.Format != "unknown" {
		t.Fatalf("sample: %+v", s)
	}
	var xmlS struct{ ID string }
	e.Must(http.StatusCreated, sample(e.Token, "Odd XML", "order", "<Order><No>1</No></Order>", nil), &xmlS)
	var dismiss struct{ ID string }
	e.Must(http.StatusCreated, sample(e.Token, "Throwaway", "order", "a;b\n1;2\n", nil), &dismiss)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/mapping/samples/"+dismiss.ID+"/dismiss", nil), nil)
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/agents/mapping/samples/"+dismiss.ID+"/dismiss", nil), nil)
	var list struct {
		Items []struct{ Name, Status string }
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/mapping/samples?status=new", nil), &list)
	if len(list.Items) != 2 {
		t.Fatalf("new samples: %+v", list.Items)
	}

	if n := runAgent(t, e, "mapping-agent"); n != 2 {
		t.Fatalf("proposed %d, want 2 (mapping + manual case)", n)
	}
	if n := runAgent(t, e, "mapping-agent"); n != 0 {
		t.Fatalf("duplicate proposals: %d", n)
	}
	var save, manual fullAction
	for _, a := range agentActions(t, e, "mapping-agent", "pending") {
		if a.Kind == "mapping.save" {
			save = a
		} else {
			manual = a
		}
	}
	if save.ID == "" || !strings.Contains(save.Title, "Acme orders") || !strings.Contains(strings.Join(save.Reasoning, "\n"), "lines.quantity <- column \"Qty\"") ||
		!strings.Contains(strings.Join(save.Reasoning, "\n"), "Not mapped") || !strings.Contains(strings.Join(save.Reasoning, "\n"), "number is missing on row 3") {
		t.Fatalf("mapping proposal: %+v", save)
	}
	if manual.Kind != "case.open" || !strings.Contains(manual.Title, "Odd XML") || !strings.Contains(strings.Join(manual.Reasoning, " "), "xml") {
		t.Fatalf("xml sample should become a case for a person: %+v", manual)
	}
	var detail struct {
		Confidence float64
		After      map[string]any
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions/"+save.ID, nil), &detail)
	if detail.Confidence < 0.7 || detail.Confidence > 0.95 || detail.After["regressionCases"] != float64(2) {
		t.Fatalf("detail: %+v", detail)
	}

	// Approval saves the mapping through the studio's validators and stores the regression cases.
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/agents/actions/"+save.ID+"/approve", nil), nil)
	var done struct{ Status string }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+save.ID+"/approve", nil), &done)
	if done.Status != "executed" {
		t.Fatalf("status %s", done.Status)
	}
	var maps struct {
		Items []struct {
			ID, Name string
			Fields   []map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/mappings", nil), &maps)
	if len(maps.Items) != 1 || maps.Items[0].Name != "Acme orders" || len(maps.Items[0].Fields) != 6 {
		t.Fatalf("mappings: %+v", maps.Items)
	}
	mid := maps.Items[0].ID
	var cases struct {
		Items []struct{ Expected map[string]string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/mapping/cases?mappingId="+mid, nil), &cases)
	if len(cases.Items) != 2 || cases.Items[0].Expected["number"] != "PO-1" || cases.Items[1].Expected["lines.quantity"] != "5" {
		t.Fatalf("cases: %+v", cases.Items)
	}
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/agents/mapping/cases", nil), nil)
	var reg struct {
		Cases, Passed int
		Failures      []map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/mapping/regression", map[string]any{"mappingId": mid}), &reg)
	if reg.Cases != 2 || reg.Passed != 2 || len(reg.Failures) != 0 {
		t.Fatalf("regression: %+v", reg)
	}
	// Someone edits the mapping in the studio so lots come out lower-case: the regression notices.
	fields := maps.Items[0].Fields
	for _, f := range fields {
		if f["target"] == "lines.lot" {
			f["transforms"] = []map[string]any{{"op": "lower"}}
		}
	}
	var full map[string]any
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/mappings/"+mid, nil), &full)
	put := map[string]any{"name": full["name"], "docType": full["docType"], "format": full["format"], "options": full["options"], "fields": fields}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/b2b/mappings/"+mid, put), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/mapping/regression", map[string]any{"mappingId": mid}), &reg)
	if reg.Passed != 0 || len(reg.Failures) != 2 {
		t.Fatalf("regression after edit: %+v", reg)
	}
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/agents/mapping/regression", map[string]any{"mappingId": uuid.NewString()}), nil)
	// Reverting the (executed) agent action removes the mapping again and re-opens the sample.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+save.ID+"/revert", nil), nil)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/mappings", nil), &maps)
	if len(maps.Items) != 0 {
		t.Fatalf("mapping survived the revert: %+v", maps.Items)
	}
	if n := runAgent(t, e, "mapping-agent"); n != 1 {
		t.Fatalf("after revert the sample is proposed again, proposed %d", n)
	}
	// The manual case is an ordinary approval.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+manual.ID+"/approve", nil), nil)
	var cs struct {
		Items []struct {
			Title string
			Notes []any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/cases", nil), &cs)
	if len(cs.Items) != 1 || len(cs.Items[0].Notes) == 0 {
		t.Fatalf("cases: %+v", cs.Items)
	}
}

func TestMappingAgentFormats(t *testing.T) {
	e := testkit.New(t)
	up := func(name, docType, content string) {
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/agents/mapping/samples", map[string]any{"name": name, "docType": docType, "content": content}), nil)
	}
	up("semi", "order", "PO No;Currency;Description;Qty;GTIN\nP1;EUR;Pills;3;09506000134352\n")
	up("json", "order", `[{"number":"J1","lines":[{"gtin":"09506000134352","qty":2}]}]`)
	up("nokey", "order", "Foo,Bar\n1,2\n")
	up("edi", "order", "UNB+UNOC:3+SENDER+RECV+260101:1200+1'")
	up("onecol", "order", "justonecolumn\nvalue\n")
	up("epcis", "epcis", "Event Time,Biz Step,EPC List\n2026-01-01T10:00:00Z,shipping,urn:epc:id:sgtin:9506000.013435.1\n")
	// An existing mapping with the same name forces a unique name.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", map[string]any{"name": "semi", "docType": "order", "format": "csv",
		"fields": []map[string]any{{"target": "number", "source": "PO No"}}}), nil)
	runAgent(t, e, "mapping-agent")
	titles := map[string]fullAction{}
	for _, a := range agentActions(t, e, "mapping-agent", "pending") {
		titles[a.Title] = a
	}
	var got []string
	for k := range titles {
		got = append(got, k)
	}
	has := func(sub string) fullAction {
		for k, a := range titles {
			if strings.Contains(k, sub) {
				return a
			}
		}
		t.Fatalf("no proposal containing %q in %v", sub, got)
		return fullAction{}
	}
	if a := has(`"semi (2)"`); a.Kind != "mapping.save" {
		t.Fatalf("semicolon csv: %+v", a)
	}
	has(`Save mapping "json"`)
	has("needs a person: nokey")
	has("needs a person: edi")
	has("needs a person: onecol")
	if a := has(`Save mapping "epcis"`); a.Kind != "mapping.save" {
		t.Fatalf("epcis: %+v", a)
	}
}

func TestAgentWordingRefinedByModel(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/agents/mapping/samples", map[string]any{"name": "Acme orders", "docType": "order", "content": orderCSV}), nil)
	llm := &scriptedLLM{reply: "Polished wording."}
	actOf(e).LLM = llm
	runAgent(t, e, "mapping-agent")
	a := agentActions(t, e, "mapping-agent", "pending")[0]
	var d struct{ Summary string }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions/"+a.ID, nil), &d)
	if d.Summary != "Polished wording." || !strings.Contains(strings.Join(a.Reasoning, "\n"), "refined by model scripted") || llm.msgs != 1 {
		t.Fatalf("refined: %q %v", d.Summary, a.Reasoning)
	}
	var au struct {
		Items []struct{ After map[string]any }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=agent.propose", nil), &au)
	ev, _ := au.Items[0].After["llm"].(map[string]any)
	if ev["model"] != "scripted" || ev["output"] != "Polished wording." || !strings.Contains(ev["prompt"].(string), "Acme orders") {
		t.Fatalf("model evidence must be in the audit entry: %+v", au.Items[0].After)
	}

	// A failing or rambling model never blocks or changes the deterministic proposal.
	for i, bad := range []*scriptedLLM{{err: errors.New("overloaded")}, {reply: strings.Repeat("x", 700)}, {reply: "  "}} {
		actOf(e).LLM = bad
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/agents/mapping/samples", map[string]any{"name": "Again " + string(rune('A'+i)), "docType": "order", "content": orderCSV}), nil)
		if n := runAgent(t, e, "mapping-agent"); n != 1 {
			t.Fatalf("model failure %d: proposed %d", i, n)
		}
	}
	for _, x := range agentActions(t, e, "mapping-agent", "pending") {
		if strings.Contains(x.Title, "Again") {
			var d struct{ Summary string }
			e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions/"+x.ID, nil), &d)
			if strings.HasPrefix(d.Summary, "xxx") || strings.TrimSpace(d.Summary) == "" {
				t.Fatalf("bad model output leaked: %q", d.Summary)
			}
		}
	}
}

func TestOnboardingHelper(t *testing.T) {
	e := testkit.New(t)
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Globex Wholesale", "gln": "0614141000005", "kind": "wholesaler", "country": "US"}), &p)
	var ob struct{ Items []struct{ ID, Key string } }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners/"+p.ID+"/onboarding", map[string]any{"template": "b2b-edi"}), &ob)
	if n := runAgent(t, e, "onboarding-helper"); n != 0 {
		t.Fatalf("fresh onboarding proposed %d", n)
	}
	superExec(t, e, `update partner_onboarding set started_at = now() - interval '10 days'`)
	if n := runAgent(t, e, "onboarding-helper"); n != 1 {
		t.Fatalf("stalled onboarding proposed %d", n)
	}
	a := agentActions(t, e, "onboarding-helper", "pending")[0]
	r := strings.Join(a.Reasoning, "\n")
	if a.Kind != "case.open" || !strings.Contains(a.Title, "Globex Wholesale") || !strings.Contains(r, "Partner identifiers") || !strings.Contains(r, "No progress for 10 days") || !strings.Contains(r, "0 of 5") {
		t.Fatalf("stalled proposal: %+v", a)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), nil)
	var cs struct {
		Items []struct {
			Kind     string
			Priority int
			Notes    []map[string]string
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/cases", nil), &cs)
	if len(cs.Items) != 1 || cs.Items[0].Kind != "partner.onboarding_stalled" || len(cs.Items[0].Notes) == 0 || !strings.Contains(cs.Items[0].Notes[0]["text"], "Onboarding template") {
		t.Fatalf("case: %+v", cs.Items)
	}
	if n := runAgent(t, e, "onboarding-helper"); n != 0 {
		t.Fatalf("same step proposed again: %d", n)
	}

	// Progress resets the clock.
	e.Must(http.StatusOK, e.Do("PATCH", "/api/v1/partners/"+p.ID+"/onboarding/items/"+ob.Items[0].ID, map[string]any{"done": true}), nil)
	if n := runAgent(t, e, "onboarding-helper"); n != 0 {
		t.Fatalf("progress was made, proposed %d", n)
	}
	// A failing test, idle for three days, is cited with its detail.
	superExec(t, e, `update partner_onboarding_item set done_at = now() - interval '3 days'`)
	superExec(t, e, `update partner_onboarding set started_at = now() - interval '3 days'`)
	superExec(t, e, `insert into partner_test_run (id, tenant_id, partner_id, channel, passed, detail, created_at) values (gen_random_uuid(), $1, $2, 'https', false, '{"error":"HTTP 502 from endpoint"}', now() - interval '3 days')`, e.Tenant.ID, p.ID)
	if n := runAgent(t, e, "onboarding-helper"); n != 1 {
		t.Fatalf("failing test proposed %d", n)
	}
	b := agentActions(t, e, "onboarding-helper", "pending")[0]
	r = strings.Join(b.Reasoning, "\n")
	if !strings.Contains(b.Title, "test failing") || !strings.Contains(r, "HTTP 502 from endpoint") || !strings.Contains(r, "Failing test") {
		t.Fatalf("failing test proposal: %+v", b)
	}
	// Once the latest run passes, the test is no longer cited.
	superExec(t, e, `insert into partner_test_run (id, tenant_id, partner_id, channel, passed, created_at) values (gen_random_uuid(), $1, $2, 'https', true, now() - interval '2 days')`, e.Tenant.ID, p.ID)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+b.ID+"/reject", nil), nil)
	superExec(t, e, `update partner_test_run set created_at = now() - interval '1 hour' where passed`)
	if n := runAgent(t, e, "onboarding-helper"); n != 0 {
		t.Fatalf("a passing latest test and recent progress proposed %d", n)
	}
}

// supply commissions two packs of a product and returns their serials.
func supply(t *testing.T, e *testkit.Env, tok, gtin string) []string {
	t.Helper()
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Amoxicillin 500mg", "targetMarkets": []string{"US"}}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "US"}), &pool)
	var al struct{ Serials []string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 2}, "Idempotency-Key", uuid.NewString()), &al)
	var epcs []string
	for _, s := range al.Serials {
		u, _ := gs1.SGTINURN(gtin, s, 7)
		epcs = append(epcs, u)
	}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: time.Now().Add(-2 * time.Hour).UTC(),
		EventTimeZoneOffset: "+00:00", EPCList: epcs, ILMD: map[string]any{"lotNumber": "L1", "itemExpirationDate": "2030-01-31"}}}))
	e.Must(http.StatusAccepted, e.Call(tok, "POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()
	return al.Serials
}

func TestInvestigationAssistant(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	serials := supply(t, e, e.Token, gtin)
	ops := login(t, e, "ops@acme.test", "Ops")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	open := func(body map[string]any) string {
		var inv struct{ ID string }
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/investigations", body), &inv)
		return inv.ID
	}
	// Far from its due date and young: nothing to say.
	open(map[string]any{"kind": "suspect", "gtin": gtin, "serials": serials[:1], "title": "Calm"})
	if n := runAgent(t, e, "investigation-assistant"); n != 0 {
		t.Fatalf("calm investigation proposed %d", n)
	}
	id := open(map[string]any{"kind": "tracing_request", "gtin": gtin, "serials": append(serials[:1:1], "NOSUCHSERIAL"), "title": "State board request", "requestRef": "REQ-1",
		"requester": map[string]any{"type": "authority", "name": "State board"}, "dueAt": time.Now().Add(3 * time.Hour)})
	if n := runAgent(t, e, "investigation-assistant"); n != 1 {
		t.Fatalf("due soon proposed %d", n)
	}
	a := agentActions(t, e, "investigation-assistant", "pending")[0]
	r := strings.Join(a.Reasoning, "\n")
	if a.Kind != "investigation.assemble_answer" || !strings.Contains(r, "Due in") || !strings.Contains(r, "has not been assembled") ||
		!strings.Contains(r, "no evidence") || !strings.Contains(r, "1 identifiers in scope were not found") || a.Entity != "investigation" || a.EntityID != id {
		t.Fatalf("proposal: %+v", a)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), nil)
	e.Must(http.StatusForbidden, e.Call(ops, "POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), nil)
	var inv struct {
		Investigation struct {
			Status, Decision string
			AnsweredAt       *time.Time
			SignatureID      *string
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/investigations/"+id, nil), &inv)
	if inv.Investigation.AnsweredAt == nil || inv.Investigation.Status != "open" || inv.Investigation.Decision != "" || inv.Investigation.SignatureID != nil {
		t.Fatalf("the agent assembled the answer but must not move the investigation: %+v", inv.Investigation)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/investigations/"+id+"/tracing-answer", nil), nil)
	// The assembly is irreversible by design.
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/agents/actions/"+a.ID+"/revert", nil), nil)

	// The answer exists; the missing evidence is listed in a case, once.
	if n := runAgent(t, e, "investigation-assistant"); n != 1 {
		t.Fatalf("missing evidence proposed %d", n)
	}
	c := agentActions(t, e, "investigation-assistant", "pending")[0]
	if c.Kind != "case.open" || !strings.Contains(strings.Join(c.Reasoning, "\n"), "no evidence or verification finding") || strings.Contains(strings.Join(c.Reasoning, "\n"), "has not been assembled") {
		t.Fatalf("case proposal: %+v", c)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+c.ID+"/reject", nil), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/investigations/"+id+"/findings", map[string]any{"kind": "evidence", "summary": "Pack photos attached"}), nil)
	superExec(t, e, `update investigation set not_found = '{}'`)
	if n := runAgent(t, e, "investigation-assistant"); n != 0 {
		t.Fatalf("complete investigation proposed %d", n)
	}
	// A year-old investigation without evidence is flagged even with no due date.
	superExec(t, e, `update investigation set created_at = now() - interval '2 days' where title = 'Calm'`)
	if n := runAgent(t, e, "investigation-assistant"); n != 1 {
		t.Fatalf("stale investigation proposed %d", n)
	}
	_ = context.Background
}

func TestAgentToolsFailSafely(t *testing.T) {
	e := testkit.New(t)
	// mapping.save: a name taken between proposal and approval fails the action, nothing is half-saved.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/agents/mapping/samples", map[string]any{"name": "Taken", "docType": "order", "content": orderCSV}), nil)
	runAgent(t, e, "mapping-agent")
	a := agentActions(t, e, "mapping-agent", "pending")[0]
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/mappings", map[string]any{"name": "Taken", "docType": "order", "format": "csv",
		"fields": []map[string]any{{"target": "number", "source": "PO Number"}}}), nil)
	var done fullAction
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), &done)
	if done.Status != "failed" || done.Result["error"] == nil {
		t.Fatalf("expected a failed action: %+v", done)
	}
	var cs struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/mapping/cases?mappingId="+uuid.NewString(), nil), &cs)
	if len(cs.Items) != 0 {
		t.Fatal("regression cases of a failed save")
	}

	// investigation.assemble_answer on an investigation that vanished.
	gtin := "09506000134352"
	serials := supply(t, e, e.Token, gtin)
	var inv struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/investigations", map[string]any{"kind": "suspect", "gtin": gtin, "serials": serials[:1], "title": "Gone",
		"dueAt": time.Now().Add(time.Hour)}), &inv)
	runAgent(t, e, "investigation-assistant")
	b := agentActions(t, e, "investigation-assistant", "pending")[0]
	superExec(t, e, `delete from investigation where id = $1`, inv.ID)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+b.ID+"/approve", nil), &done)
	if done.Status != "failed" {
		t.Fatalf("expected failed: %+v", done)
	}
	// ... and on one that is already closed.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/investigations", map[string]any{"kind": "suspect", "gtin": gtin, "serials": serials[:1], "title": "Closing",
		"dueAt": time.Now().Add(time.Hour)}), &inv)
	runAgent(t, e, "investigation-assistant")
	var c fullAction
	for _, x := range agentActions(t, e, "investigation-assistant", "pending") {
		if x.EntityID == inv.ID {
			c = x
		}
	}
	superExec(t, e, `update investigation set status = 'closed' where id = $1`, inv.ID)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/actions/"+c.ID+"/approve", nil), &done)
	if done.Status != "failed" || !strings.Contains(done.Result["error"].(string), "closed") {
		t.Fatalf("closed investigation: %+v", done)
	}
	// A closed investigation is no longer watched.
	runAgent(t, e, "investigation-assistant")
	for _, x := range agentActions(t, e, "investigation-assistant", "pending") {
		if x.EntityID == inv.ID {
			t.Fatalf("closed investigation proposed: %+v", x)
		}
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/agents/inspection/packages/"+uuid.NewString(), nil), nil)
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/agents/mapping/samples/"+uuid.NewString()+"/dismiss", nil), nil)
}
