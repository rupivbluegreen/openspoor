package act_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/act"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/testkit"
)

func problem(t *testing.T, r testkit.Response, status int, want string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Body)
	}
	var p struct {
		Title  string
		Status int
	}
	if err := r.JSON(&p); err != nil || p.Status != status || !strings.Contains(p.Title, want) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("problem %+v (want %q): %s", p, want, r.Body)
	}
}

func login(t *testing.T, e *testkit.Env, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

// superExec runs SQL as the database superuser (bypassing RLS) to set up fixtures other modules own.
func superExec(t *testing.T, e *testkit.Env, sql string, args ...any) {
	t.Helper()
	u, _ := url.Parse(os.Getenv("OPENSPOOR_TEST_ADMIN_URL"))
	app, _ := url.Parse(e.DBURL)
	u.Path = app.Path
	ctx := context.Background()
	c, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func alert(t *testing.T, e *testkit.Env, id, kind, severity, title string) {
	t.Helper()
	superExec(t, e, `insert into alert (id, tenant_id, kind, severity, title, entity, entity_id) values ($1,$2,$3,$4,$5,'thing',$6)`, id, e.Tenant.ID, kind, severity, title, id)
}

func sign(t *testing.T, e *testkit.Env, token, entity, id string) {
	t.Helper()
	e.Must(http.StatusCreated, e.Call(token, "POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "Approved", "reason": "test", "entity": entity, "entityId": id}), nil)
}

type fullAction struct {
	ID, Agent, Kind, Title, Status, Risk, Entity, EntityID string
	Reasoning                                              []string
	Reversible                                             bool
	ApproverRoles                                          []string
	DecidedBy                                              *string
	Result                                                 map[string]any
	Input                                                  map[string]any
}

func actions(t *testing.T, e *testkit.Env, status string) []fullAction {
	t.Helper()
	var page struct{ Items []fullAction }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions?status="+status, nil), &page)
	return page.Items
}

func TestModuleMetadata(t *testing.T) {
	m := act.New(kernel.Deps{})
	if m.Name() != "act" || m.LLM != nil {
		t.Fatalf("module: %+v", m)
	}
	if s := m.Schedules(); len(s) != 1 || s[0].Name != "act.agents" || s[0].Every != 10*time.Minute {
		t.Fatalf("schedules: %+v", s)
	}
}

func TestValidateData(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"lot", "qty"}, "properties": map[string]any{
		"lot":   map[string]any{"type": "string", "pattern": "^L-[0-9]+$"},
		"qty":   map[string]any{"type": "integer"},
		"temp":  map[string]any{"type": "number"},
		"ok":    map[string]any{"type": "boolean"},
		"grade": map[string]any{"type": "string", "enum": []any{"A", "B"}},
		"code":  map[string]any{"type": "string", "pattern": "("}, // invalid patterns are ignored
	}}
	cases := []struct {
		data map[string]any
		want []string
	}{
		{map[string]any{"lot": "L-1", "qty": 3.0, "temp": 2.5, "ok": true, "grade": "A", "code": "x"}, nil},
		{map[string]any{}, []string{"lot is required", "qty is required"}},
		// An empty string is missing and, having a pattern, also malformed.
		{map[string]any{"lot": "", "qty": nil}, []string{"lot is required", "qty is required", "lot does not match the required format"}},
		{map[string]any{"lot": "X-1", "qty": 1.0}, []string{"lot does not match the required format"}},
		{map[string]any{"lot": "L-1", "qty": 1.5}, []string{"qty must be an integer"}},
		{map[string]any{"lot": "L-1", "qty": "3"}, []string{"qty must be integer"}},
		{map[string]any{"lot": 7.0, "qty": 1.0}, []string{"lot must be string"}},
		{map[string]any{"lot": "L-1", "qty": 1.0, "ok": "yes"}, []string{"ok must be boolean"}},
		{map[string]any{"lot": "L-1", "qty": 1.0, "temp": false}, []string{"temp must be number"}},
		{map[string]any{"lot": "L-1", "qty": 1.0, "grade": "C"}, []string{"grade must be one of the listed values"}},
		{map[string]any{"lot": "L-1", "qty": 1.0, "color": "red"}, []string{"color is not a field of this form"}},
	}
	for i, c := range cases {
		got := act.ValidateData(schema, c.data)
		if strings.Join(sorted(got), "|") != strings.Join(sorted(c.want), "|") {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
	// A schema without properties or requirements rejects any field.
	if errs := act.ValidateData(map[string]any{}, map[string]any{"x": 1.0}); len(errs) != 1 {
		t.Fatalf("empty schema: %v", errs)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestAgentSettingsAndRuns(t *testing.T) {
	e := testkit.New(t)
	ops := login(t, e, "ops@acme.test", "Ops")
	viewer := login(t, e, "viewer@acme.test", "Viewer")

	var agents struct {
		Total int
		Items []struct {
			Name, Autonomy string
			Enabled        bool
			Tools          []string
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents", nil), &agents)
	if agents.Total != 7 || agents.Items[0].Name != "exception-triage" || !agents.Items[0].Enabled || agents.Items[0].Autonomy != "propose" || agents.Items[0].Tools[0] != "case.open" {
		t.Fatalf("agents: %+v", agents)
	}
	problem(t, e.Do("PUT", "/api/v1/agents/skynet", map[string]any{"enabled": true}), 404, "unknown agent")
	problem(t, e.Do("PUT", "/api/v1/agents/partner-watch", map[string]any{"enabled": true, "autonomy": "full"}), 400, "autonomy must be propose or auto_low_risk")
	problem(t, e.Do("PUT", "/api/v1/agents/partner-watch", "{"), 400, "invalid JSON body")
	problem(t, e.Call(ops, "PUT", "/api/v1/agents/partner-watch", map[string]any{"enabled": false}), 403, "missing role")
	problem(t, e.Do("POST", "/api/v1/agents/skynet/run", nil), 404, "unknown agent")
	problem(t, e.Call(viewer, "POST", "/api/v1/agents/partner-watch/run", nil), 403, "missing role")

	// Disabled agents propose nothing, even when there is something to report.
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Flaky Wholesale"}), &p)
	superExec(t, e, `update partner set health='{"status":"down","detail":"connection refused"}' where id=$1`, p.ID)
	var cfg struct {
		Enabled  bool
		Autonomy string
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/agents/partner-watch", map[string]any{"enabled": false}), &cfg)
	if cfg.Enabled || cfg.Autonomy != "propose" {
		t.Fatalf("configure: %+v", cfg)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents", nil), &agents)
	if agents.Items[3].Name != "partner-watch" || agents.Items[3].Enabled {
		t.Fatalf("disabled setting not listed: %+v", agents.Items[3])
	}
	var run struct {
		Agent    string
		Proposed int
	}
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/agents/partner-watch/run", nil), &run)
	if run.Proposed != 0 || run.Agent != "partner-watch" {
		t.Fatalf("disabled run: %+v", run)
	}

	// Enabled again: partner-watch proposes a case for the down partner.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/agents/partner-watch", map[string]any{"enabled": true}), nil)
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/agents/partner-watch/run", nil), &run)
	if run.Proposed != 1 {
		t.Fatalf("partner-watch: %+v", run)
	}
	items := actions(t, e, "proposed")
	if len(items) != 1 || items[0].Title != "Partner endpoint down: Flaky Wholesale" || items[0].Reasoning[0] != "Last health probe failed: connection refused" ||
		items[0].Entity != "partner" || items[0].EntityID != p.ID || !items[0].Reversible || len(items[0].ApproverRoles) != 2 {
		t.Fatalf("partner-watch proposal: %+v", items)
	}
	var one fullAction
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/agents/actions/"+items[0].ID, nil), &one)
	if one.ID != items[0].ID || one.Agent != "partner-watch" || one.Input["priority"] != float64(2) {
		t.Fatalf("get action: %+v", one)
	}
	problem(t, e.Do("GET", "/api/v1/agents/actions/00000000-0000-0000-0000-000000000001", nil), 404, "not found")
	problem(t, e.Do("GET", "/api/v1/agents/actions/nope", nil), 400, "invalid id")

	// The scheduled run covers every tenant; the proposal is not duplicated.
	_, gtok := e.NewTenant("globex", "admin@globex.test")
	gAlert := "00000000-0000-4000-8000-00000000c0de"
	superExec(t, e, `insert into alert (id, tenant_id, kind, severity, title) select $1, id, 'misc', 'warning', 'Globex alert' from tenant where slug='globex'`, gAlert)
	if err := actOf(e).Schedules()[0].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := actions(t, e, "proposed"); len(got) != 1 {
		t.Fatalf("scheduled run duplicated proposals: %+v", got)
	}
	var gActs struct{ Items []fullAction }
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/agents/actions", nil), &gActs)
	if len(gActs.Items) != 1 || gActs.Items[0].EntityID != gAlert {
		t.Fatalf("other tenant's scheduled proposals: %+v", gActs.Items)
	}
	// Tenant isolation: globex cannot see or decide acme's action.
	problem(t, e.Call(gtok, "GET", "/api/v1/agents/actions/"+items[0].ID, nil), 404, "not found")
	problem(t, e.Call(gtok, "POST", "/api/v1/agents/actions/"+items[0].ID+"/approve", nil), 409, "not awaiting approval")
	problem(t, e.Call(gtok, "POST", "/api/v1/agents/actions/"+items[0].ID+"/reject", nil), 409, "not awaiting approval")
}

func TestActionDecisions(t *testing.T) {
	e := testkit.New(t)
	ops := login(t, e, "ops@acme.test", "Ops")
	ser := login(t, e, "ser@acme.test", "Serialization")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	qa := login(t, e, "qa@acme.test", "Compliance")

	alert(t, e, "00000000-0000-4000-8000-000000000001", "regulatory.rejection", "error", "Hub rejected report")
	alert(t, e, "00000000-0000-4000-8000-000000000002", "epcis.rejected", "error", "Capture failed")
	alert(t, e, "00000000-0000-4000-8000-000000000003", "pool.low", "warning", "Pool low")
	alert(t, e, "00000000-0000-4000-8000-000000000004", "verify.suspect", "error", "Suspect pack")
	var run struct{ Proposed int }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/exception-triage/run", nil), &run)
	if run.Proposed != 4 {
		t.Fatalf("triage: %+v", run)
	}
	prio := map[string]float64{}
	byTitle := map[string]fullAction{}
	for _, a := range actions(t, e, "pending") {
		prio[a.Title] = a.Input["priority"].(float64)
		byTitle[a.Title] = a
	}
	if prio["Open case: Hub rejected report"] != 1 || prio["Open case: Suspect pack"] != 1 || prio["Open case: Capture failed"] != 2 || prio["Open case: Pool low"] != 3 {
		t.Fatalf("triage priorities: %v", prio)
	}
	if r := byTitle["Open case: Capture failed"].Reasoning; len(r) != 2 || r[1] != "Error severity: priority 2." {
		t.Fatalf("reasoning: %v", r)
	}
	rej, ok := byTitle["Open case: Pool low"], byTitle["Open case: Capture failed"]

	// Reject: Viewer and roles outside the tool's scope may not decide.
	problem(t, e.Call(viewer, "POST", "/api/v1/agents/actions/"+rej.ID+"/reject", nil), 403, "rejecting this action needs one of")
	problem(t, e.Call(ser, "POST", "/api/v1/agents/actions/"+rej.ID+"/reject", map[string]any{"reason": "no"}), 403, "rejecting this action needs one of")
	var done fullAction
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/agents/actions/"+rej.ID+"/reject", map[string]any{"reason": "noise"}), &done)
	if done.Status != "rejected" || done.DecidedBy == nil || *done.DecidedBy != "user:ops@acme.test" {
		t.Fatalf("reject: %+v", done)
	}
	var aud struct {
		Items []struct{ Reason, Actor string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=agent.action.rejected&entityId="+rej.ID, nil), &aud)
	if len(aud.Items) != 1 || aud.Items[0].Reason != "noise" {
		t.Fatalf("reject audit: %+v", aud)
	}
	problem(t, e.Call(ops, "POST", "/api/v1/agents/actions/"+rej.ID+"/reject", nil), 409, "not awaiting approval")
	problem(t, e.Call(ops, "POST", "/api/v1/agents/actions/"+rej.ID+"/approve", nil), 409, "not awaiting approval")
	problem(t, e.Call(ops, "POST", "/api/v1/agents/actions/"+rej.ID+"/revert", nil), 409, "only executed actions can be reverted")
	problem(t, e.Do("POST", "/api/v1/agents/actions/bad/reject", nil), 400, "invalid id")
	problem(t, e.Do("POST", "/api/v1/agents/actions/bad/approve", nil), 400, "invalid id")
	problem(t, e.Do("POST", "/api/v1/agents/actions/bad/revert", nil), 400, "invalid id")
	// A rejected alert is proposed again on the next run (it still has no case).
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/exception-triage/run", nil), &run)
	if run.Proposed != 1 {
		t.Fatalf("re-proposal after reject: %+v", run)
	}

	// Approve: scope enforced; revert: scope enforced as well.
	problem(t, e.Call(ser, "POST", "/api/v1/agents/actions/"+ok.ID+"/approve", nil), 403, "approving this action needs one of")
	problem(t, e.Call(viewer, "POST", "/api/v1/agents/actions/"+ok.ID+"/approve", nil), 403, "approving this action needs one of")
	e.Must(http.StatusOK, e.Call(qa, "POST", "/api/v1/agents/actions/"+ok.ID+"/approve", nil), &done)
	if done.Status != "executed" || done.Result["ok"] != true {
		t.Fatalf("approve: %+v", done)
	}
	problem(t, e.Call(viewer, "POST", "/api/v1/agents/actions/"+ok.ID+"/revert", nil), 403, "reverting needs one of")
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/agents/actions/"+ok.ID+"/revert", nil), &done)
	if done.Status != "reverted" {
		t.Fatalf("revert: %+v", done)
	}
	problem(t, e.Call(ops, "POST", "/api/v1/agents/actions/"+ok.ID+"/revert", nil), 409, "only executed actions")
}

func TestHighRiskToolsAndFailures(t *testing.T) {
	e := testkit.New(t)
	qa := login(t, e, "qa@acme.test", "Compliance")
	ops := login(t, e, "ops@acme.test", "Ops")

	// A rejected, retryable regulatory report: the resubmitter proposes a high-risk, irreversible action.
	inst, rep := "00000000-0000-4000-8000-0000000000e1", "00000000-0000-4000-8000-0000000000e2"
	superExec(t, e, `insert into connector_instance (id, tenant_id, connector, name) values ($1,$2,'test-hub','Hub')`, inst, e.Tenant.ID)
	superExec(t, e, `insert into regulatory_report (id, tenant_id, instance_id, connector, kind, idem_key, payload, packs, status, alert)
		values ($1,$2,$3,'test-hub','decommission','k1','{}','[{},{}]','rejected','{"reason":"unknown product","suggestedFix":"upload master data","retryable":true}')`, rep, e.Tenant.ID, inst)
	superExec(t, e, `insert into regulatory_report (id, tenant_id, instance_id, connector, kind, idem_key, payload, status, alert)
		values (gen_random_uuid(),$1,$2,'test-hub','decommission','k2','{}','rejected','{"retryable":false}')`, e.Tenant.ID, inst)
	var run struct{ Proposed int }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/regulatory-resubmitter/run", nil), &run)
	if run.Proposed != 1 {
		t.Fatalf("resubmitter: %+v", run)
	}
	a := actions(t, e, "proposed")[0]
	if a.Kind != "report.resubmit" || a.Risk != "high" || a.Reversible || a.Title != "Resubmit test-hub decommission (2 packs)" || a.Reasoning[0] != "Hub reason: unknown product" {
		t.Fatalf("resubmit proposal: %+v", a)
	}
	// Only Compliance may approve; it needs the approver's own signature.
	problem(t, e.Call(ops, "POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), 403, "approving this action needs one of")
	sign(t, e, e.Token, "agent_action", a.ID) // the admin's signature does not count for the QA approver
	problem(t, e.Call(qa, "POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), 428, "high-risk action: sign it first")
	sign(t, e, qa, "agent_action", a.ID)
	var done fullAction
	e.Must(http.StatusOK, e.Call(qa, "POST", "/api/v1/agents/actions/"+a.ID+"/approve", nil), &done)
	if done.Status != "executed" {
		t.Fatalf("resubmit: %+v", done)
	}
	var st struct{ Status string }
	_ = e.App.Deps.DB.Tx(context.Background(), e.Tenant.ID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select status from regulatory_report where id=$1`, rep).Scan(&st.Status)
	})
	if st.Status != "queued" {
		t.Fatalf("report after resubmit: %+v", st)
	}
	problem(t, e.Call(qa, "POST", "/api/v1/agents/actions/"+a.ID+"/revert", nil), 409, "this action cannot be reverted")

	// A tool failure is recorded as a failed action, not an executed one.
	bad := "00000000-0000-4000-8000-0000000000f1"
	superExec(t, e, `insert into agent_action (id, tenant_id, agent, tool, title, input, risk) values ($1,$2,'expiry-quarantine','serials.set_status','Bad','{"epcs":["urn:x"],"status":"sold"}','high')`, bad, e.Tenant.ID)
	sign(t, e, qa, "agent_action", bad)
	e.Must(http.StatusOK, e.Call(qa, "POST", "/api/v1/agents/actions/"+bad+"/approve", nil), &done)
	if done.Status != "failed" || !strings.Contains(done.Result["error"].(string), "only set expired or quarantined") {
		t.Fatalf("failed action: %+v", done)
	}
	var aud struct{ Total int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=agent.action.failed&entityId="+bad, nil), &aud)
	if aud.Total != 1 {
		t.Fatalf("failure audit: %+v", aud)
	}
	problem(t, e.Call(qa, "POST", "/api/v1/agents/actions/"+bad+"/revert", nil), 409, "only executed actions")
}

func TestCasesAndControlTower(t *testing.T) {
	e := testkit.New(t)
	ops := login(t, e, "ops@acme.test", "Ops")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	type wcase struct {
		ID, Kind, Title, Status, Source, SourceID, Assignee string
		Priority                                            int
		Score                                               float64
		Notes                                               []map[string]any
		DueAt                                               *time.Time
	}
	problem(t, e.Call(ops, "POST", "/api/v1/cases", map[string]any{"priority": 2}), 400, "title is required")
	problem(t, e.Call(viewer, "POST", "/api/v1/cases", map[string]any{"title": "Viewer case"}), 403, "missing role")
	problem(t, e.Call(ops, "POST", "/api/v1/cases", "{"), 400, "invalid JSON body")
	var c wcase
	e.Must(http.StatusCreated, e.Call(ops, "POST", "/api/v1/cases", map[string]any{"title": "Damaged pallet", "priority": 9, "source": "ignored"}), &c)
	if c.Kind != "manual" || c.Priority != 3 || c.Score != 50 || c.Source != "manual" || c.DueAt == nil || time.Until(*c.DueAt) < 71*time.Hour {
		t.Fatalf("case defaults: %+v", c)
	}
	var urgent wcase
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/cases", map[string]any{"title": "Recall", "kind": "recall", "priority": 1}), &urgent)
	if urgent.Score != 100 || time.Until(*urgent.DueAt) > 4*time.Hour {
		t.Fatalf("priority 1 case: %+v", urgent)
	}

	// Updates: validation, notes, assignment.
	problem(t, e.Call(ops, "PATCH", "/api/v1/cases/"+c.ID, map[string]any{"status": "closed"}), 400, "status must be open, in_progress or resolved")
	problem(t, e.Call(ops, "PATCH", "/api/v1/cases/"+c.ID, map[string]any{"priority": 0}), 400, "priority must be 1-4")
	problem(t, e.Call(ops, "PATCH", "/api/v1/cases/00000000-0000-0000-0000-000000000001", map[string]any{"note": "x"}), 404, "not found")
	problem(t, e.Call(ops, "PATCH", "/api/v1/cases/x", map[string]any{"note": "x"}), 400, "invalid id")
	problem(t, e.Call(ops, "PATCH", "/api/v1/cases/"+c.ID, "{"), 400, "invalid JSON body")
	problem(t, e.Call(viewer, "PATCH", "/api/v1/cases/"+c.ID, map[string]any{"status": "resolved"}), 403, "missing role")
	e.Must(http.StatusOK, e.Call(ops, "PATCH", "/api/v1/cases/"+c.ID, map[string]any{"status": "in_progress", "assignee": "ops@acme.test", "note": "Checking with carrier", "priority": 2}), &c)
	e.Must(http.StatusOK, e.Call(ops, "PATCH", "/api/v1/cases/"+c.ID, map[string]any{"note": "   "}), &c)
	if c.Status != "in_progress" || c.Assignee != "ops@acme.test" || c.Priority != 2 || len(c.Notes) != 1 || c.Notes[0]["by"] != "user:ops@acme.test" || c.Notes[0]["text"] != "Checking with carrier" {
		t.Fatalf("case update: %+v", c)
	}

	// Queue order: priority first.
	var page struct {
		Total int
		Items []wcase
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/cases", nil), &page)
	if page.Total != 2 || page.Items[0].ID != urgent.ID || page.Items[1].ID != c.ID {
		t.Fatalf("queue: %+v", page)
	}

	// A case opened from an alert resolves the alert with it.
	alertID := "00000000-0000-4000-8000-0000000000aa"
	alert(t, e, alertID, "verify.suspect", "error", "Suspect pack")
	var run struct{ Proposed int }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/exception-triage/run", nil), &run)
	triage := actions(t, e, "proposed")[0]
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/agents/actions/"+triage.ID+"/approve", nil), nil)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/cases", nil), &page)
	var fromAlert wcase
	for _, x := range page.Items {
		if x.Source == "alert" {
			fromAlert = x
		}
	}
	if fromAlert.SourceID != alertID || fromAlert.Priority != 1 {
		t.Fatalf("alert case: %+v", page.Items)
	}
	e.Must(http.StatusOK, e.Call(ops, "PATCH", "/api/v1/cases/"+fromAlert.ID, map[string]any{"status": "resolved"}), nil)
	var al struct{ Items []struct{ ID, Status string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=resolved", nil), &al)
	if len(al.Items) != 1 || al.Items[0].ID != alertID {
		t.Fatalf("alert not resolved with its case: %+v", al)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/cases?status=resolved", nil), &page)
	if page.Total != 1 || page.Items[0].ID != fromAlert.ID {
		t.Fatalf("resolved cases: %+v", page)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/cases?status=all", nil), &page)
	if page.Total != 3 {
		t.Fatalf("all cases: %+v", page)
	}

	// Control tower thresholds: one critical alert is a warning, five are critical; overdue cases count.
	monitor := func(id string) (float64, string) {
		var ct struct {
			Monitors []struct {
				ID, Status string
				Value      float64
			}
			PendingActions int
		}
		e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/control-tower", nil), &ct)
		for _, m := range ct.Monitors {
			if m.ID == id {
				return m.Value, m.Status
			}
		}
		t.Fatalf("monitor %s missing", id)
		return 0, ""
	}
	if v, s := monitor("alerts_critical"); v != 0 || s != "ok" {
		t.Fatalf("alerts_critical: %v %s", v, s)
	}
	alert(t, e, "00000000-0000-4000-8000-0000000000b1", "x", "error", "E1")
	if v, s := monitor("alerts_critical"); v != 1 || s != "warning" {
		t.Fatalf("alerts_critical warning: %v %s", v, s)
	}
	for _, id := range []string{"b2", "b3", "b4", "b5"} {
		alert(t, e, "00000000-0000-4000-8000-0000000000"+id, "x", "error", id)
	}
	if v, s := monitor("alerts_critical"); v != 5 || s != "critical" {
		t.Fatalf("alerts_critical critical: %v %s", v, s)
	}
	superExec(t, e, `update work_case set due_at = now() - interval '1 hour' where id=$1`, c.ID)
	if v, s := monitor("cases_overdue"); v != 1 || s != "warning" {
		t.Fatalf("cases_overdue: %v %s", v, s)
	}

	// Tenant isolation.
	_, gtok := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(gtok, "PATCH", "/api/v1/cases/"+c.ID, map[string]any{"status": "resolved"}), 404, "not found")
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/cases?status=all", nil), &page)
	if page.Total != 0 {
		t.Fatalf("cross-tenant cases: %+v", page)
	}
}

func TestFormsAndWorkflowDesigner(t *testing.T) {
	e := testkit.New(t)
	ops := login(t, e, "ops@acme.test", "Ops")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	serial := login(t, e, "serial@acme.test", "Serialization")
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	// Forms: designer validation; admin only; upsert by name.
	problem(t, e.Do("POST", "/api/v1/forms", map[string]any{"schema": obj(map[string]any{"a": map[string]any{"type": "string"}})}), 400, "name is required")
	problem(t, e.Do("POST", "/api/v1/forms", map[string]any{"name": "F", "schema": map[string]any{"type": "array"}}), 400, `schema.type must be "object"`)
	problem(t, e.Do("POST", "/api/v1/forms", map[string]any{"name": "F", "schema": obj(map[string]any{})}), 400, "at least one field")
	problem(t, e.Do("POST", "/api/v1/forms", map[string]any{"name": "F", "schema": obj(map[string]any{"a": map[string]any{"type": "date"}})}), 400, "field a needs a type")
	problem(t, e.Do("POST", "/api/v1/forms", map[string]any{"name": "F", "schema": obj(map[string]any{"a": "string"})}), 400, "field a needs a type")
	problem(t, e.Do("POST", "/api/v1/forms", "{"), 400, "invalid JSON body")
	problem(t, e.Call(ops, "POST", "/api/v1/forms", map[string]any{"name": "F", "schema": obj(map[string]any{"a": map[string]any{"type": "string"}})}), 403, "missing role")
	var f1, f2 struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/forms", map[string]any{"name": "Goods in", "schema": obj(map[string]any{"pallets": map[string]any{"type": "integer"}})}), &f1)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/forms", map[string]any{"name": "Goods in", "schema": obj(map[string]any{"pallets": map[string]any{"type": "integer"}, "dock": map[string]any{"type": "string"}})}), &f2)
	if f1.ID != f2.ID {
		t.Fatalf("form upsert created a second form: %s %s", f1.ID, f2.ID)
	}
	var forms struct {
		Total int
		Items []struct {
			Name   string
			Schema map[string]any
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/forms", nil), &forms)
	if forms.Total != 1 || len(forms.Items[0].Schema["properties"].(map[string]any)) != 2 {
		t.Fatalf("forms: %+v", forms)
	}
	problem(t, e.Call(ops, "POST", "/api/v1/forms/00000000-0000-0000-0000-000000000001/submit", map[string]any{"data": map[string]any{}}), 404, "not found")
	problem(t, e.Call(ops, "POST", "/api/v1/forms/x/submit", map[string]any{"data": map[string]any{}}), 400, "invalid id")
	problem(t, e.Call(ops, "POST", "/api/v1/forms/"+f1.ID+"/submit", "{"), 400, "invalid JSON body")
	var invalid struct{ Errors []string }
	r := e.Call(ops, "POST", "/api/v1/forms/"+f1.ID+"/submit", map[string]any{"data": map[string]any{"pallets": 2.5}})
	if r.Status != 422 || r.JSON(&invalid) != nil || len(invalid.Errors) != 1 || invalid.Errors[0] != "pallets must be an integer" {
		t.Fatalf("invalid submission: %d %s", r.Status, r.Body)
	}
	e.Must(http.StatusCreated, e.Call(ops, "POST", "/api/v1/forms/"+f1.ID+"/submit", map[string]any{"data": map[string]any{"pallets": 3, "dock": "D2"}}), nil)
	var subs struct {
		Total int
		Items []struct {
			SubmittedBy string
			Data        map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/forms/"+f1.ID+"/submissions", nil), &subs)
	if subs.Total != 1 || subs.Items[0].SubmittedBy != "user:ops@acme.test" || subs.Items[0].Data["dock"] != "D2" {
		t.Fatalf("submissions: %+v", subs)
	}
	problem(t, e.Do("GET", "/api/v1/forms/bad/submissions", nil), 400, "invalid id")

	// Workflow designer validation.
	step := func(key, name, role string) map[string]any {
		return map[string]any{"key": key, "name": name, "role": role}
	}
	problem(t, e.Do("POST", "/api/v1/workflows", "not json"), 400, "invalid JSON")
	problem(t, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Empty"}), 400, "name and at least one step are required")
	problem(t, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Dup", "steps": []any{step("a", "A", ""), step("a", "B", "")}}), 400, "step 2 needs a unique key and a name")
	problem(t, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Nameless", "steps": []any{step("a", "", "")}}), 400, "step 1 needs a unique key")
	problem(t, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "BadForm", "steps": []any{map[string]any{"key": "a", "name": "A", "form": map[string]any{"type": "object"}}}}), 400, "at least one field")
	problem(t, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Batch Release", "steps": []any{step("a", "A", "")}}), 409, "built-in process")
	problem(t, e.Call(ops, "POST", "/api/v1/workflows", map[string]any{"name": "Ops flow", "steps": []any{step("a", "A", "")}}), 403, "missing role")

	type wrun struct {
		ID, Key, Status string
		Step            int
		CurrentStep     *struct{ Key string }
		History         []map[string]any
		Context         map[string]any
	}
	var wf struct {
		ID, Key string
		Builtin bool
		Steps   []struct{ Key string }
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Cold Chain Excursion!", "description": "Temperature excursion handling",
		"steps": []any{map[string]any{"key": "assess", "name": "Assess", "role": "Ops", "form": obj(map[string]any{"tempC": map[string]any{"type": "number"}})}, step("close", "Close", "Compliance")}}), &wf)
	if wf.Key != "cold-chain-excursion-" || wf.Builtin || len(wf.Steps) != 2 {
		t.Fatalf("workflow: %+v", wf)
	}
	var custom struct{ ID, Key string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Returns", "key": "returns-v1", "steps": []any{step("inspect", "Inspect", "")}}), &custom)
	if custom.Key != "returns-v1" {
		t.Fatalf("explicit key: %+v", custom)
	}
	var wfs struct {
		Total int
		Items []struct {
			Key     string
			Builtin bool
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/workflows", nil), &wfs)
	if wfs.Total != 5 || !wfs.Items[0].Builtin || wfs.Items[4].Builtin {
		t.Fatalf("workflows: %+v", wfs)
	}

	// Runs: start by key or id, role and form checks per step, completion.
	problem(t, e.Call(ops, "POST", "/api/v1/workflows/nope/start", nil), 404, "unknown workflow")
	problem(t, e.Call(ops, "POST", "/api/v1/workflows/returns-v1/start", "{"), 400, "invalid JSON body")
	var rn wrun
	e.Must(http.StatusCreated, e.Call(ops, "POST", "/api/v1/workflows/"+wf.ID+"/start", map[string]any{"context": map[string]any{"shipment": "S-1"}}), &rn)
	if rn.Status != "running" || rn.CurrentStep == nil || rn.CurrentStep.Key != "assess" || rn.Context["shipment"] != "S-1" {
		t.Fatalf("start: %+v", rn)
	}
	var r2 wrun
	e.Must(http.StatusCreated, e.Call(ops, "POST", "/api/v1/workflows/returns-v1/start", nil), &r2)
	adv := func(token string, id string, data any) testkit.Response {
		return e.Call(token, "POST", "/api/v1/workflow-runs/"+id+"/advance", map[string]any{"data": data})
	}
	problem(t, adv(serial, rn.ID, map[string]any{"tempC": 9}), 403, "step Assess needs role Ops")
	// Viewer is strictly read-only: it can read runs but cannot start, advance or submit.
	problem(t, adv(viewer, rn.ID, map[string]any{"tempC": 9}), 403, "missing role")
	problem(t, e.Call(viewer, "POST", "/api/v1/workflows/returns-v1/start", nil), 403, "missing role")
	problem(t, e.Call(viewer, "POST", "/api/v1/forms/"+f1.ID+"/submit", map[string]any{"data": map[string]any{"pallets": 1}}), 403, "missing role")
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/workflow-runs/"+rn.ID, nil), nil)
	problem(t, adv(ops, rn.ID, map[string]any{"tempC": "hot"}), 422, "step data is invalid")
	e.Must(http.StatusOK, adv(ops, rn.ID, map[string]any{"tempC": 9.5}), &rn)
	if rn.Step != 1 || rn.CurrentStep.Key != "close" || rn.Context["tempC"] != 9.5 || len(rn.History) != 1 || rn.History[0]["by"] != "user:ops@acme.test" {
		t.Fatalf("after assess: %+v", rn)
	}
	problem(t, adv(ops, rn.ID, nil), 403, "needs role Compliance")
	rn0ID := rn.ID
	rn = wrun{}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/workflow-runs/"+rn0ID+"/advance", nil), &rn)
	if rn.Status != "completed" || rn.CurrentStep != nil {
		t.Fatalf("completed: %+v", rn)
	}
	problem(t, e.Do("POST", "/api/v1/workflow-runs/"+rn.ID+"/advance", nil), 409, "workflow run is completed")
	problem(t, e.Do("POST", "/api/v1/workflow-runs/00000000-0000-0000-0000-000000000001/advance", nil), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/workflow-runs/x/advance", nil), 400, "invalid id")
	problem(t, e.Do("POST", "/api/v1/workflow-runs/"+r2.ID+"/advance", "{"), 400, "invalid JSON body")

	var got wrun
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/workflow-runs/"+rn.ID, nil), &got)
	if got.Status != "completed" || got.Key != "cold-chain-excursion-" {
		t.Fatalf("get run: %+v", got)
	}
	problem(t, e.Do("GET", "/api/v1/workflow-runs/00000000-0000-0000-0000-000000000001", nil), 404, "not found")
	problem(t, e.Do("GET", "/api/v1/workflow-runs/x", nil), 400, "invalid id")
	var runs struct {
		Total int
		Items []wrun
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/workflow-runs?status=running", nil), &runs)
	if runs.Total != 1 || runs.Items[0].ID != r2.ID || runs.Items[0].CurrentStep == nil {
		t.Fatalf("running runs: %+v", runs)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/workflow-runs", nil), &runs)
	if runs.Total != 2 {
		t.Fatalf("all runs: %+v", runs)
	}

	// Saving the same key again updates the definition in place.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/workflows", map[string]any{"name": "Returns v1.1", "key": "returns-v1", "steps": []any{step("inspect", "Inspect", ""), step("refund", "Refund", "Admin")}}), &custom)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/workflows", nil), &wfs)
	if wfs.Total != 5 {
		t.Fatalf("upsert added a workflow: %+v", wfs)
	}

	// Another tenant cannot touch these runs or forms.
	_, gtok := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(gtok, "POST", "/api/v1/workflow-runs/"+r2.ID+"/advance", nil), 404, "not found")
	problem(t, e.Call(gtok, "POST", "/api/v1/forms/"+f1.ID+"/submit", map[string]any{"data": map[string]any{}}), 404, "not found")
	problem(t, e.Call(gtok, "POST", "/api/v1/workflows/returns-v1/start", nil), 404, "unknown workflow")
}

type scriptedLLM struct {
	reply string
	err   error
	msgs  int
}

func (s *scriptedLLM) Name() string { return "scripted" }
func (s *scriptedLLM) Answer(_ context.Context, _ string, msgs []act.ChatMessage) (string, error) {
	s.msgs = len(msgs)
	return s.reply, s.err
}

func TestAssistantEdgeCases(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	chat := func(token string, msgs ...act.ChatMessage) testkit.Response {
		return e.Call(token, "POST", "/api/v1/assistant/chat", map[string]any{"messages": msgs})
	}
	user := func(s string) act.ChatMessage { return act.ChatMessage{Role: "user", Content: s} }
	problem(t, chat(e.Token), 400, "messages must end with a user message")
	problem(t, chat(e.Token, user("hi"), act.ChatMessage{Role: "assistant", Content: "hello"}), 400, "messages must end with a user message")
	problem(t, e.Do("POST", "/api/v1/assistant/chat", "{"), 400, "invalid JSON body")

	// Offline answer: unknown identifiers, rejected reports, pending actions, alerts and lots.
	inst := "00000000-0000-4000-8000-0000000000e1"
	superExec(t, e, `insert into connector_instance (id, tenant_id, connector, name) values ($1,$2,'test-hub','Hub')`, inst, e.Tenant.ID)
	superExec(t, e, `insert into regulatory_report (id, tenant_id, instance_id, connector, kind, idem_key, payload, status, alert)
		values (gen_random_uuid(),$1,$2,'test-hub','decommission','k1','{}','rejected','{"reason":"bad GTIN","suggestedFix":"fix GTIN"}')`, e.Tenant.ID, inst)
	alert(t, e, "00000000-0000-4000-8000-0000000000a1", "verify.suspect", "error", "Suspect pack X9")
	var run struct{ Proposed int }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/exception-triage/run", nil), &run)
	var rep struct {
		Reply     string
		Model     string
		Citations []struct{ Title, Ref string }
	}
	e.Must(http.StatusOK, chat(viewer, user("Where is (01)09506000134352(21)NOPE and lot ABC-1?")), &rep)
	for _, want := range []string{"(01)09506000134352(21)NOPE: no record of this identifier.", "1 rejected regulatory reports (first: bad GTIN — fix GTIN)", "1 agent actions awaiting approval", "1 open alerts", "Most recent alert: Suspect pack X9."} {
		if !strings.Contains(rep.Reply, want) {
			t.Fatalf("offline reply missing %q:\n%s", want, rep.Reply)
		}
	}
	refs := map[string]bool{}
	for _, c := range rep.Citations {
		refs[c.Ref] = true
	}
	if rep.Model != "none" || !refs["lot/ABC-1"] || !refs["alert/00000000-0000-4000-8000-0000000000a1"] {
		t.Fatalf("citations: %+v", rep.Citations)
	}

	// Long conversations are trimmed to the last 40 turns; questions are truncated in the audit log.
	fake := &scriptedLLM{reply: "fine"}
	actOf(e).LLM = fake
	var msgs []act.ChatMessage
	for i := 0; i < 50; i++ {
		msgs = append(msgs, user("q"), act.ChatMessage{Role: "assistant", Content: "a"})
	}
	msgs = append(msgs, user(strings.Repeat("x", 600)))
	e.Must(http.StatusOK, chat(e.Token, msgs...), &rep)
	if fake.msgs != 40 || rep.Reply != "fine" || rep.Model != "scripted" {
		t.Fatalf("trim: %d %+v", fake.msgs, rep)
	}
	var aud struct {
		Items []struct{ After map[string]any }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=assistant.ask&limit=1", nil), &aud)
	if q, _ := aud.Items[0].After["question"].(string); len(q) != 500 {
		t.Fatalf("audited question length %d", len(q))
	}

	// A model failure falls back to the offline answer and says so.
	actOf(e).LLM = &scriptedLLM{err: errors.New("overloaded")}
	e.Must(http.StatusOK, chat(e.Token, user("status?")), &rep)
	if !strings.Contains(rep.Reply, "Workspace:") || !strings.HasSuffix(rep.Reply, "[Model unavailable: overloaded]") {
		t.Fatalf("fallback: %s", rep.Reply)
	}
}

func TestLanguageModelAdapter(t *testing.T) {
	if act.NewLLM(config.Config{AnthropicKey: "k"}) != nil || act.NewLLM(config.Config{AnthropicModel: "m"}) != nil {
		t.Fatal("adapter enabled without key and model")
	}
	var lastBody map[string]any
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &lastBody)
		w.Header().Set("Content-Type", "application/json")
		switch mode {
		case "error":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
		case "refusal":
			_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":0}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"Two "},{"type":"text","text":"alerts."}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`))
		}
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	llm := act.NewLLM(config.Config{AnthropicKey: "test-key", AnthropicModel: "test-model"})
	if llm == nil || llm.Name() != "test-model" {
		t.Fatalf("adapter: %v", llm)
	}
	ctx := context.Background()
	msgs := []act.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}, {Role: "system", Content: "ignored"}, {Role: "user", Content: "alerts?"}}
	out, err := llm.Answer(ctx, "CONTEXT", msgs)
	if err != nil || out != "Two alerts." {
		t.Fatalf("answer: %q %v", out, err)
	}
	sent, _ := lastBody["messages"].([]any)
	if len(sent) != 3 || lastBody["model"] != "test-model" {
		t.Fatalf("request: %+v", lastBody)
	}
	mode = "refusal"
	if _, err := llm.Answer(ctx, "CONTEXT", msgs); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("refusal: %v", err)
	}
	mode = "error"
	if _, err := llm.Answer(ctx, "CONTEXT", msgs); err == nil || err.Error() != "assistant model error (HTTP 400)" {
		t.Fatalf("api error: %v", err)
	}
	srv.Close()
	if _, err := llm.Answer(ctx, "CONTEXT", msgs); err == nil {
		t.Fatal("unreachable model returned no error")
	}
}
