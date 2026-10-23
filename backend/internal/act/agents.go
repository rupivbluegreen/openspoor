// Package act is the governed-agent layer: an agent framework with scoped tools, human
// approval gates (e-signature for high-risk actions), a full audit trail and reversible
// actions; the agentic control tower (monitors and a prioritized work queue); a process
// library and no-code forms and workflows; and an assistant over the tenant's own data.
//
// Agents never write directly. They propose actions; a tool executes an action only after
// approval (or automatically, for low-risk reversible tools, when the tenant opts in).
package act

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
)

// Module is the Act bounded context.
type Module struct {
	kernel.Deps
	tools  map[string]Tool
	agents []Agent
	LLM    LLM
}

// New creates the module with the built-in tools and agents.
func New(d kernel.Deps) *Module {
	m := &Module{Deps: d, tools: map[string]Tool{}}
	for _, t := range []Tool{caseOpenTool{}, reportResubmitTool{}, serialStatusTool{}} {
		m.tools[t.Name()] = t
	}
	m.agents = []Agent{exceptionTriage{}, regulatoryResubmitter{}, expiryQuarantine{}, partnerWatch{}}
	m.LLM = NewLLM(d.Cfg)
	return m
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "act" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Get("/agents", httpx.Handler(m.listAgents))
	r.With(auth.Require(auth.RoleAdmin)).Put("/agents/{name}", httpx.Handler(m.configureAgent))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleCompliance)).Post("/agents/{name}/run", httpx.Handler(m.runAgent))
	r.Get("/agents/actions", httpx.Handler(m.listActions))
	r.Get("/agents/actions/{id}", httpx.Handler(m.getAction))
	r.Post("/agents/actions/{id}/approve", httpx.Handler(m.approve))
	r.Post("/agents/actions/{id}/reject", httpx.Handler(m.reject))
	r.Post("/agents/actions/{id}/revert", httpx.Handler(m.revert))
	r.Get("/control-tower", httpx.Handler(m.controlTower))
	r.Get("/cases", httpx.Handler(m.listCases))
	// Viewer is read-only: any other role may open and update cases.
	writer := auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleCompliance, auth.RoleOps, auth.RoleIntegration)
	r.With(writer).Post("/cases", httpx.Handler(m.createCase))
	r.With(writer).Patch("/cases/{id}", httpx.Handler(m.updateCase))
	m.workflowRoutes(r)
	r.Post("/assistant/chat", httpx.Handler(m.chat))
}

// Schedules implements kernel.Scheduled: every enabled agent runs every 10 minutes for every tenant.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: "act.agents", Every: 10 * time.Minute, Run: m.runAll}}
}

// --- tools ---

// Tool is a capability an agent may use. Tools declare who may approve them (Scope),
// their Risk and whether they can be reverted.
type Tool interface {
	Name() string
	Scope() []string
	Risk() string // low | high (high requires an e-signature on approval)
	Reversible() bool
	// Execute performs the action inside the tenant transaction and returns before/after state.
	Execute(ctx context.Context, tx pgx.Tx, input json.RawMessage) (before, after any, err error)
	// Revert undoes an executed action using its recorded before state.
	Revert(ctx context.Context, tx pgx.Tx, input, before json.RawMessage) error
}

// Proposal is what an agent suggests.
type Proposal struct {
	Tool       string
	Title      string
	Summary    string
	Reasoning  []string
	Input      any
	Preview    any // the expected "after" shown in the approval diff
	Confidence float64
	Entity     string
	EntityID   string
	DedupeKey  string
}

// Agent observes tenant data (read-only) and proposes actions.
type Agent interface {
	Name() string
	Description() string
	Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error)
}

type agentInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Autonomy    string   `json:"autonomy"`
	Tools       []string `json:"tools"`
}

var agentTools = map[string][]string{"exception-triage": {"case.open"}, "regulatory-resubmitter": {"report.resubmit"}, "expiry-quarantine": {"serials.set_status"}, "partner-watch": {"case.open"}}

func (m *Module) settings(ctx context.Context, tx pgx.Tx) (map[string][2]string, error) {
	out := map[string][2]string{}
	rows, err := tx.Query(ctx, `select agent, enabled::text, autonomy from agent_setting`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, e, au string
		if err := rows.Scan(&a, &e, &au); err != nil {
			return nil, err
		}
		out[a] = [2]string{e, au}
	}
	return out, rows.Err()
}

func (m *Module) listAgents(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		st, err := m.settings(r.Context(), tx)
		if err != nil {
			return err
		}
		var out []agentInfo
		for _, a := range m.agents {
			ai := agentInfo{Name: a.Name(), Description: a.Description(), Enabled: true, Autonomy: "propose", Tools: agentTools[a.Name()]}
			if s, ok := st[a.Name()]; ok {
				ai.Enabled, ai.Autonomy = s[0] == "true", s[1]
			}
			out = append(out, ai)
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[agentInfo]{Items: out, Total: len(out)})
		return nil
	})
}

func (m *Module) agent(name string) Agent {
	for _, a := range m.agents {
		if a.Name() == name {
			return a
		}
	}
	return nil
}

func (m *Module) configureAgent(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "name")
	if m.agent(name) == nil {
		return httpx.NotFound("unknown agent")
	}
	var in struct {
		Enabled  bool   `json:"enabled"`
		Autonomy string `json:"autonomy"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Autonomy == "" {
		in.Autonomy = "propose"
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `insert into agent_setting (tenant_id, agent, enabled, autonomy) values (app_tenant(), $1,$2,$3)
			on conflict (tenant_id, agent) do update set enabled=excluded.enabled, autonomy=excluded.autonomy`, name, in.Enabled, in.Autonomy); err != nil {
			return httpx.BadRequest("autonomy must be propose or auto_low_risk")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "agent.configure", Entity: "agent", EntityID: name, After: in}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, in)
		return nil
	})
}

// Run executes one agent for the caller's tenant and stores its proposals.
func (m *Module) Run(ctx context.Context, tenant uuid.UUID, a Agent) (int, error) {
	n := 0
	var autoIDs []uuid.UUID
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		st, err := m.settings(ctx, tx)
		if err != nil {
			return err
		}
		if s, ok := st[a.Name()]; ok && s[0] == "false" {
			return nil
		}
		auto := st[a.Name()][1] == "auto_low_risk"
		props, err := a.Observe(ctx, tx)
		if err != nil {
			return err
		}
		for _, p := range props {
			tool := m.tools[p.Tool]
			if tool == nil {
				return fmt.Errorf("agent %s proposed unknown tool %s", a.Name(), p.Tool)
			}
			id := ids.New()
			ct, err := tx.Exec(ctx, `insert into agent_action (id, tenant_id, agent, tool, title, summary, reasoning, input, after, risk, confidence, entity, entity_id, dedupe_key)
				values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,nullif($13,'')) on conflict do nothing`,
				id, a.Name(), p.Tool, p.Title, p.Summary, p.Reasoning, p.Input, p.Preview, tool.Risk(), p.Confidence, p.Entity, p.EntityID, p.DedupeKey)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				continue
			}
			n++
			if _, err := audit.WriteAs(ctx, tx, tenant, "agent:"+a.Name(), audit.Record{Action: "agent.propose", Entity: "agent_action", EntityID: id.String(),
				After: map[string]any{"tool": p.Tool, "title": p.Title, "input": p.Input}}); err != nil {
				return err
			}
			if auto && tool.Risk() == "low" && tool.Reversible() {
				autoIDs = append(autoIDs, id)
			}
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	for _, id := range autoIDs {
		_, _ = m.execute(auth.With(ctx, auth.System(tenant)), tenant, id, "agent:"+a.Name()+" (auto, low risk)")
	}
	return n, nil
}

func (m *Module) runAgent(w http.ResponseWriter, r *http.Request) error {
	a := m.agent(chi.URLParam(r, "name"))
	if a == nil {
		return httpx.NotFound("unknown agent")
	}
	n, err := m.Run(r.Context(), kernel.TenantOf(r.Context()), a)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"agent": a.Name(), "proposed": n})
	return nil
}

func (m *Module) runAll(ctx context.Context) error {
	var tenants []uuid.UUID
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id from tenant where status='active'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			tenants = append(tenants, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, t := range tenants {
		for _, a := range m.agents {
			if _, err := m.Run(auth.With(ctx, auth.System(t)), t, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// Action is a stored agent action.
type Action struct {
	ID            uuid.UUID       `json:"id" db:"id"`
	Agent         string          `json:"agent" db:"agent"`
	Tool          string          `json:"kind" db:"tool"`
	Title         string          `json:"title" db:"title"`
	Summary       string          `json:"summary" db:"summary"`
	Reasoning     []string        `json:"reasoning" db:"reasoning"`
	Input         json.RawMessage `json:"input" db:"input"`
	Before        json.RawMessage `json:"before,omitempty" db:"before"`
	After         json.RawMessage `json:"after,omitempty" db:"after"`
	Risk          string          `json:"risk" db:"risk"`
	Confidence    float64         `json:"confidence" db:"confidence"`
	Status        string          `json:"status" db:"status"`
	Entity        string          `json:"entity" db:"entity"`
	EntityID      string          `json:"entityId" db:"entity_id"`
	DecidedBy     *string         `json:"decidedBy,omitempty" db:"decided_by"`
	DecidedAt     *time.Time      `json:"decidedAt,omitempty" db:"decided_at"`
	ExecutedAt    *time.Time      `json:"executedAt,omitempty" db:"executed_at"`
	RevertedAt    *time.Time      `json:"revertedAt,omitempty" db:"reverted_at"`
	Result        json.RawMessage `json:"result,omitempty" db:"result"`
	CreatedAt     time.Time       `json:"createdAt" db:"created_at"`
	Reversible    bool            `json:"reversible" db:"-"`
	ApproverRoles []string        `json:"approverRoles" db:"-"`
}

const actionCols = `id, agent, tool, title, summary, reasoning, input, before, after, risk, confidence, status, entity, entity_id, decided_by, decided_at, executed_at, reverted_at, result, created_at`

func (m *Module) decorate(a *Action) {
	if t := m.tools[a.Tool]; t != nil {
		a.Reversible, a.ApproverRoles = t.Reversible(), t.Scope()
	}
}

func (m *Module) listActions(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Action](r.Context(), tx, `select `+actionCols+` from agent_action where ($1='' or status=$1 or ($1='pending' and status='proposed')) order by created_at desc limit 500`, status)
		for i := range items {
			m.decorate(&items[i])
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Action]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getAction(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		a, err := db.One[Action](r.Context(), tx, `select `+actionCols+` from agent_action where id=$1`, id)
		m.decorate(&a)
		if err == nil {
			httpx.JSON(w, http.StatusOK, a)
		}
		return err
	})
}

// approve is the human gate: the approver needs one of the tool's roles, and high-risk
// tools need an e-signature recorded against this action (re-authentication + reason).
func (m *Module) approve(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	p := auth.MustFrom(ctx)
	var tool string
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select tool from agent_action where id=$1 and status='proposed'`, id).Scan(&tool)
	})
	if err != nil {
		return httpx.Conflict("action is not awaiting approval")
	}
	t := m.tools[tool]
	if !p.Has(t.Scope()...) {
		return httpx.Forbidden("approving this action needs one of: " + fmt.Sprint(t.Scope()))
	}
	if t.Risk() == "high" {
		var n int
		_ = m.Tx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from esignature where entity='agent_action' and entity_id=$1 and user_id=$2`, id.String(), p.UserID).Scan(&n)
		})
		if n == 0 {
			return &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "high-risk action: sign it first (POST /esignatures with entity agent_action)"}
		}
	}
	a, err := m.execute(ctx, p.TenantID, id, p.Actor())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, a)
	return nil
}

func (m *Module) execute(ctx context.Context, tenant, id uuid.UUID, actor string) (Action, error) {
	var a Action
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		a, err = db.One[Action](ctx, tx, `select `+actionCols+` from agent_action where id=$1 and status='proposed' for update`, id)
		if err != nil {
			return httpx.Conflict("action is not awaiting approval")
		}
		t := m.tools[a.Tool]
		before, after, execErr := t.Execute(ctx, tx, a.Input)
		status, result := "executed", map[string]any{"ok": true}
		if execErr != nil {
			status, result = "failed", map[string]any{"error": execErr.Error()}
		}
		a, err = db.One[Action](ctx, tx, `update agent_action set status=$2, decided_by=$3, decided_at=now(), executed_at=case when $2='executed' then now() end,
			before=$4, after=coalesce($5, after), result=$6 where id=$1 returning `+actionCols, id, status, actor, before, after, result)
		if err != nil {
			return err
		}
		_, err = audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "agent.action." + status, Entity: "agent_action", EntityID: id.String(),
			Before: before, After: map[string]any{"tool": a.Tool, "after": after, "result": result}})
		return err
	})
	m.decorate(&a)
	return a, err
}

func (m *Module) reject(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		_ = httpx.Decode(r, &in)
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		// Rejecting is a decision like approving: it needs one of the tool's approver roles.
		var tool string
		if err := tx.QueryRow(r.Context(), `select tool from agent_action where id=$1 and status='proposed'`, id).Scan(&tool); err != nil {
			return httpx.Conflict("action is not awaiting approval")
		}
		if t := m.tools[tool]; t != nil && !p.Has(t.Scope()...) {
			return httpx.Forbidden("rejecting this action needs one of: " + fmt.Sprint(t.Scope()))
		}
		a, err := db.One[Action](r.Context(), tx, `update agent_action set status='rejected', decided_by=$2, decided_at=now() where id=$1 and status='proposed' returning `+actionCols, id, p.Actor())
		if err != nil {
			return httpx.Conflict("action is not awaiting approval")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "agent.action.rejected", Entity: "agent_action", EntityID: id.String(), Reason: in.Reason}); err != nil {
			return err
		}
		m.decorate(&a)
		httpx.JSON(w, http.StatusOK, a)
		return nil
	})
}

func (m *Module) revert(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		a, err := db.One[Action](r.Context(), tx, `select `+actionCols+` from agent_action where id=$1 and status='executed' for update`, id)
		if err != nil {
			return httpx.Conflict("only executed actions can be reverted")
		}
		t := m.tools[a.Tool]
		if !t.Reversible() {
			return httpx.Conflict("this action cannot be reverted")
		}
		if !p.Has(t.Scope()...) {
			return httpx.Forbidden("reverting needs one of: " + fmt.Sprint(t.Scope()))
		}
		if err := t.Revert(r.Context(), tx, a.Input, a.Before); err != nil {
			return err
		}
		a, err = db.One[Action](r.Context(), tx, `update agent_action set status='reverted', reverted_at=now() where id=$1 returning `+actionCols, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "agent.action.reverted", Entity: "agent_action", EntityID: id.String(), After: a.Before}); err != nil {
			return err
		}
		m.decorate(&a)
		httpx.JSON(w, http.StatusOK, a)
		return nil
	})
}

// --- built-in tools ---

type caseInput struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Entity   string `json:"entity"`
	EntityID string `json:"entityId"`
	Source   string `json:"source"`
	SourceID string `json:"sourceId"`
}

type caseOpenTool struct{}

func (caseOpenTool) Name() string     { return "case.open" }
func (caseOpenTool) Scope() []string  { return []string{auth.RoleOps, auth.RoleCompliance} }
func (caseOpenTool) Risk() string     { return "low" }
func (caseOpenTool) Reversible() bool { return true }
func (caseOpenTool) Execute(ctx context.Context, tx pgx.Tx, input json.RawMessage) (any, any, error) {
	var in caseInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, nil, err
	}
	c, err := OpenCase(ctx, tx, in)
	return nil, c, err
}
func (caseOpenTool) Revert(ctx context.Context, tx pgx.Tx, input, _ json.RawMessage) error {
	var in caseInput
	_ = json.Unmarshal(input, &in)
	_, err := tx.Exec(ctx, `update work_case set status='resolved', notes = notes || '["reverted by user"]'::jsonb, updated_at=now()
		where source=$1 and source_id=$2 and status <> 'resolved'`, in.Source, in.SourceID)
	return err
}

type reportResubmitTool struct{}

func (reportResubmitTool) Name() string     { return "report.resubmit" }
func (reportResubmitTool) Scope() []string  { return []string{auth.RoleCompliance} }
func (reportResubmitTool) Risk() string     { return "high" }
func (reportResubmitTool) Reversible() bool { return false }
func (reportResubmitTool) Execute(ctx context.Context, tx pgx.Tx, input json.RawMessage) (any, any, error) {
	var in struct {
		ReportID uuid.UUID `json:"reportId"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, nil, err
	}
	var status string
	_ = tx.QueryRow(ctx, `select status from regulatory_report where id=$1`, in.ReportID).Scan(&status)
	rep, err := connectors.Resubmit(ctx, tx, kernel.TenantOf(ctx), in.ReportID)
	return map[string]string{"status": status}, map[string]string{"status": rep.Status}, err
}
func (reportResubmitTool) Revert(context.Context, pgx.Tx, json.RawMessage, json.RawMessage) error {
	return httpx.Conflict("a regulatory submission cannot be reverted")
}

type serialStatusTool struct{}

func (serialStatusTool) Name() string { return "serials.set_status" }
func (serialStatusTool) Scope() []string {
	return []string{auth.RoleSerialization, auth.RoleCompliance}
}
func (serialStatusTool) Risk() string     { return "high" }
func (serialStatusTool) Reversible() bool { return true }
func (serialStatusTool) Execute(ctx context.Context, tx pgx.Tx, input json.RawMessage) (any, any, error) {
	var in struct {
		EPCs   []string `json:"epcs"`
		Status string   `json:"status"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, nil, err
	}
	if in.Status != "expired" && in.Status != "quarantined" {
		return nil, nil, fmt.Errorf("agents may only set expired or quarantined")
	}
	rows, err := tx.Query(ctx, `select epc, status from serial_state where tenant_id = app_tenant() and epc = any($1) for update`, in.EPCs)
	if err != nil {
		return nil, nil, err
	}
	before := map[string]string{}
	for rows.Next() {
		var e, s string
		if err := rows.Scan(&e, &s); err != nil {
			rows.Close()
			return nil, nil, err
		}
		before[e] = s
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `update serial_state set status=$2, updated_at=now() where epc = any($1)`, in.EPCs, in.Status); err != nil {
		return nil, nil, err
	}
	after := map[string]string{}
	for e := range before {
		after[e] = in.Status
	}
	return before, after, nil
}
func (serialStatusTool) Revert(ctx context.Context, tx pgx.Tx, _, before json.RawMessage) error {
	var prev map[string]string
	if err := json.Unmarshal(before, &prev); err != nil {
		return err
	}
	keys := make([]string, 0, len(prev))
	for k := range prev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, e := range keys {
		if _, err := tx.Exec(ctx, `update serial_state set status=$2, updated_at=now() where epc=$1`, e, prev[e]); err != nil {
			return err
		}
	}
	return nil
}

// --- built-in agents (deterministic observers; summaries may be refined by the LLM) ---

type exceptionTriage struct{}

func (exceptionTriage) Name() string { return "exception-triage" }
func (exceptionTriage) Description() string {
	return "Turns open alerts into prioritized cases in the work queue (regulatory rejections and suspect products first)."
}
func (exceptionTriage) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `select a.id, a.kind, a.severity, a.title, a.entity, a.entity_id, extract(epoch from now()-a.created_at)/3600 from alert a
		where a.status='open' and not exists (select 1 from work_case c where c.source='alert' and c.source_id=a.id::text) order by a.created_at limit 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var id uuid.UUID
		var kind, sev, title, ent, eid string
		var ageH float64
		if err := rows.Scan(&id, &kind, &sev, &title, &ent, &eid, &ageH); err != nil {
			return nil, err
		}
		prio := 3
		reason := []string{fmt.Sprintf("Alert %q (%s, %s) has been open %.1f h without a case.", title, kind, sev, ageH)}
		switch {
		case sev == "error" && (kind == "regulatory.rejection" || kind == "recall.open" || kind == "recall.received" || len(kind) > 7 && kind[:7] == "verify."):
			prio = 1
			reason = append(reason, "Regulatory or patient-safety impact: priority 1.")
		case sev == "error":
			prio = 2
			reason = append(reason, "Error severity: priority 2.")
		}
		out = append(out, Proposal{Tool: "case.open", Title: "Open case: " + title, Summary: "Track and resolve this exception in the work queue.", Reasoning: reason,
			Input:   caseInput{Kind: kind, Title: title, Priority: prio, Entity: ent, EntityID: eid, Source: "alert", SourceID: id.String()},
			Preview: map[string]any{"case": title, "priority": prio}, Confidence: 0.9, Entity: "alert", EntityID: id.String(), DedupeKey: "case:alert:" + id.String()})
	}
	return out, rows.Err()
}

type regulatoryResubmitter struct{}

func (regulatoryResubmitter) Name() string { return "regulatory-resubmitter" }
func (regulatoryResubmitter) Description() string {
	return "Proposes resubmitting rejected regulatory reports whose rejection is retryable, once the cause looks fixed."
}
func (regulatoryResubmitter) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `select r.id, r.connector, r.kind, coalesce(r.alert->>'reason',''), coalesce(r.alert->>'suggestedFix',''), jsonb_array_length(r.packs)
		from regulatory_report r where r.status in ('rejected','failed') and coalesce((r.alert->>'retryable')::boolean, true) order by r.updated_at limit 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var id uuid.UUID
		var conn, kind, reason, fix string
		var packs int
		if err := rows.Scan(&id, &conn, &kind, &reason, &fix, &packs); err != nil {
			return nil, err
		}
		out = append(out, Proposal{Tool: "report.resubmit", Title: fmt.Sprintf("Resubmit %s %s (%d packs)", conn, kind, packs),
			Summary:   "The hub rejected this report. If the suggested fix has been applied, resubmit it.",
			Reasoning: []string{"Hub reason: " + reason, "Suggested fix: " + fix, "The rejection is marked retryable; resubmission is idempotent at the hub (same idempotency key)."},
			Input:     map[string]any{"reportId": id}, Preview: map[string]string{"status": "queued"}, Confidence: 0.6, Entity: "regulatory_report", EntityID: id.String(), DedupeKey: "resubmit:" + id.String()})
	}
	return out, rows.Err()
}

type expiryQuarantine struct{}

func (expiryQuarantine) Name() string { return "expiry-quarantine" }
func (expiryQuarantine) Description() string {
	return "Finds saleable packs past their expiry date and proposes marking them expired so they cannot be shipped or verified as saleable."
}
func (expiryQuarantine) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `select coalesce(gtin,''), coalesce(lot,''), min(expiry), array_agg(epc order by epc) from serial_state
		where expiry < current_date and status in ('active','in_progress','sellable_accessible','sellable_not_accessible','observed','commissioned')
		group by gtin, lot limit 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var gtin, lot string
		var exp time.Time
		var epcs []string
		if err := rows.Scan(&gtin, &lot, &exp, &epcs); err != nil {
			return nil, err
		}
		if len(epcs) > 5000 {
			epcs = epcs[:5000]
		}
		out = append(out, Proposal{Tool: "serials.set_status", Title: fmt.Sprintf("Mark %d packs of GTIN %s lot %s expired", len(epcs), gtin, lot),
			Summary:   "These packs passed their expiry date but are still in a saleable state.",
			Reasoning: []string{fmt.Sprintf("Expiry %s is in the past.", exp.Format("2006-01-02")), "Expired product must not be distributed; status 'expired' blocks verification as saleable.", "Reversible: previous statuses are kept."},
			Input:     map[string]any{"epcs": epcs, "status": "expired"}, Preview: map[string]any{"status": "expired", "packs": len(epcs)}, Confidence: 0.95,
			Entity: "lot", EntityID: gtin + "/" + lot, DedupeKey: "expiry:" + gtin + "/" + lot})
	}
	return out, rows.Err()
}

type partnerWatch struct{}

func (partnerWatch) Name() string { return "partner-watch" }
func (partnerWatch) Description() string {
	return "Opens a case when an active trading partner's endpoint is down."
}
func (partnerWatch) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `select id, name, coalesce(health->>'detail','') from partner where status='active' and health->>'status' = 'down'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var id uuid.UUID
		var name, detail string
		if err := rows.Scan(&id, &name, &detail); err != nil {
			return nil, err
		}
		out = append(out, Proposal{Tool: "case.open", Title: "Partner endpoint down: " + name, Summary: "TI/TS and VRS exchanges with this partner will fail until the endpoint recovers.",
			Reasoning: []string{"Last health probe failed: " + detail},
			Input:     caseInput{Kind: "partner.down", Title: "Partner endpoint down: " + name, Priority: 2, Entity: "partner", EntityID: id.String(), Source: "partner", SourceID: id.String()},
			Preview:   map[string]any{"priority": 2}, Confidence: 0.8, Entity: "partner", EntityID: id.String(), DedupeKey: "partner-down:" + id.String()})
	}
	return out, rows.Err()
}
