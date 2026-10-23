package act

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
)

// Autonomy ladder per action type (tool). A tenant's quality owner decides, for each tool:
//
//	suggest  the agent only suggests; a person makes the change in the console themselves
//	approve  applied after a person approves (the default; high-risk tools also need a signature)
//	auto     applied automatically with an audit entry and an undo (revert); only low-risk
//	         reversible tools can be set to auto, and a tool that stops qualifying falls back to approve
//
// A tenant that has not set a tool's mode keeps the per-agent setting (propose / auto_low_risk).
const (
	ModeSuggest = "suggest"
	ModeApprove = "approve"
	ModeAuto    = "auto"

	autoSuffix = " (auto, low risk)"
)

// AutoEligible reports whether a tool may be applied without a person: low risk and reversible.
func AutoEligible(t Tool) bool { return t.Risk() == "low" && t.Reversible() }

// toolModes returns the tenant's stored mode per tool.
func (m *Module) toolModes(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	out := map[string]string{}
	rows, err := tx.Query(ctx, `select tool, mode from tool_autonomy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t, md string
		if err := rows.Scan(&t, &md); err != nil {
			return nil, err
		}
		out[t] = md
	}
	return out, rows.Err()
}

// effectiveMode resolves what happens to a proposal of tool t: the tenant's tool setting, else
// the agent's own setting. Auto never applies to a tool that is not low-risk and reversible.
func effectiveMode(t Tool, modes map[string]string, agentAuto bool) string {
	mode, ok := modes[t.Name()]
	if !ok {
		mode = ModeApprove
		if agentAuto {
			mode = ModeAuto
		}
	}
	if mode == ModeAuto && !AutoEligible(t) {
		return ModeApprove
	}
	return mode
}

// withModes fills the autonomy of listed actions.
func (m *Module) withModes(ctx context.Context, tx pgx.Tx, items []Action) error {
	modes, err := m.toolModes(ctx, tx)
	if err != nil {
		return err
	}
	st, err := m.settings(ctx, tx)
	if err != nil {
		return err
	}
	for i := range items {
		if t := m.tools[items[i].Tool]; t != nil {
			items[i].Autonomy = effectiveMode(t, modes, st[items[i].Agent][1] == "auto_low_risk")
		}
	}
	return nil
}

type toolInfo struct {
	Name        string   `json:"name"`
	Risk        string   `json:"risk"`
	Reversible  bool     `json:"reversible"`
	Roles       []string `json:"approverRoles"`
	Agents      []string `json:"agents"`
	Mode        string   `json:"mode"`        // effective: suggest | approve | auto
	Configured  bool     `json:"configured"`  // a tenant setting exists
	AutoAllowed bool     `json:"autoAllowed"` // low risk and reversible
}

func (m *Module) listTools(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		modes, err := m.toolModes(r.Context(), tx)
		if err != nil {
			return err
		}
		users := map[string][]string{}
		for ag, tools := range agentTools {
			for _, t := range tools {
				users[t] = append(users[t], ag)
			}
		}
		out := []toolInfo{}
		for name, t := range m.tools {
			sort.Strings(users[name])
			_, conf := modes[name]
			out = append(out, toolInfo{Name: name, Risk: t.Risk(), Reversible: t.Reversible(), Roles: t.Scope(), Agents: users[name],
				Mode: effectiveMode(t, modes, false), Configured: conf, AutoAllowed: AutoEligible(t)})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		httpx.JSON(w, http.StatusOK, httpx.Page[toolInfo]{Items: out, Total: len(out)})
		return nil
	})
}

func (m *Module) setToolMode(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "name")
	t := m.tools[name]
	if t == nil {
		return httpx.NotFound("unknown tool")
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Mode = strings.TrimSpace(in.Mode)
	switch in.Mode {
	case ModeSuggest, ModeApprove:
	case ModeAuto:
		if !AutoEligible(t) {
			return httpx.BadRequest("auto is only allowed for low-risk reversible tools; " + name + " is " + t.Risk() + " risk and " + map[bool]string{true: "reversible", false: "not reversible"}[t.Reversible()])
		}
	default:
		return httpx.BadRequest("mode must be suggest, approve or auto")
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		modes, err := m.toolModes(r.Context(), tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into tool_autonomy (tenant_id, tool, mode, updated_by, updated_at) values (app_tenant(), $1,$2,$3, now())
			on conflict (tenant_id, tool) do update set mode=excluded.mode, updated_by=excluded.updated_by, updated_at=now()`, name, in.Mode, p.Actor()); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "agent.tool.autonomy", Entity: "agent_tool", EntityID: name,
			Before: map[string]string{"mode": modes[name]}, After: map[string]string{"mode": in.Mode}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"name": name, "mode": in.Mode, "updatedAt": time.Now().UTC()})
		return nil
	})
}

// --- optional wording refinement ---

// polished marks agents whose proposal summaries a configured language model may reword. The model
// never decides: its output replaces the summary only, the deterministic reasoning stays, and the
// prompt, output and model name go into the audit entry of the proposal.
type polished interface{ polished() }

const polishSystem = `You reword a proposal summary for a compliance team. Use one or two plain sentences. Do not add facts, numbers or recommendations that are not in the input. Output only the reworded summary.`

func (m *Module) polish(ctx context.Context, p Proposal) (Proposal, map[string]any) {
	if m.LLM == nil {
		return p, nil
	}
	prompt := "Title: " + p.Title + "\nSummary: " + p.Summary + "\nReasoning:\n- " + strings.Join(p.Reasoning, "\n- ")
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := m.LLM.Answer(cctx, polishSystem, []ChatMessage{{Role: "user", Content: prompt}})
	out = strings.TrimSpace(out)
	ev := map[string]any{"model": m.LLM.Name(), "system": polishSystem, "prompt": prompt, "output": out}
	if err != nil {
		ev["error"] = err.Error()
		return p, ev
	}
	if out == "" || len(out) > 600 {
		ev["rejected"] = "empty or too long; the deterministic summary is kept"
		return p, ev
	}
	ev["original"] = p.Summary
	p.Summary = out
	p.Reasoning = append(append([]string{}, p.Reasoning...), "Summary wording refined by model "+m.LLM.Name()+"; the facts above are deterministic.")
	return p, ev
}

func (m *Module) agentRoutes(r chi.Router) {
	r.Get("/agents/tools", httpx.Handler(m.listTools))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance)).Put("/agents/tools/{name}", httpx.Handler(m.setToolMode))
	m.aiRoutes(r)
}
