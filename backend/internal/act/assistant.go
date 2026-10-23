package act

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/trace"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// ChatMessage is one turn of the assistant conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLM answers a question given grounding context. Implementations must not have tools
// with side effects: the assistant is read-only over the tenant's own data.
type LLM interface {
	Name() string
	Answer(ctx context.Context, system string, msgs []ChatMessage) (string, error)
}

// NewLLM returns the Claude-backed model when ANTHROPIC_API_KEY and OPENSPOOR_ASSISTANT_MODEL
// are set, else nil (the assistant then answers from the gathered context without a model).
func NewLLM(cfg config.Config) LLM {
	// A local open-weight model (air-gapped hubs) takes precedence: no data leaves the site.
	if cfg.LocalLLMURL != "" && cfg.LocalLLMModel != "" {
		return &localLLM{url: strings.TrimSuffix(cfg.LocalLLMURL, "/") + "/v1/chat/completions", model: cfg.LocalLLMModel,
			client: &http.Client{Timeout: 120 * time.Second}}
	}
	if cfg.AnthropicKey == "" || cfg.AnthropicModel == "" {
		return nil
	}
	return &claudeLLM{client: anthropic.NewClient(option.WithAPIKey(cfg.AnthropicKey)), model: cfg.AnthropicModel}
}

type claudeLLM struct {
	client anthropic.Client
	model  string
}

func (c *claudeLLM) Name() string { return c.model }

// Answer calls the Messages API. Refusals are re-served by a fallback model chosen by the
// server ("default" fallbacks), and a remaining refusal is reported, never shown as an answer.
func (c *claudeLLM) Answer(ctx context.Context, system string, msgs []ChatMessage) (string, error) {
	var ms []anthropic.BetaMessageParam
	for _, m := range msgs {
		switch m.Role {
		case "user":
			ms = append(ms, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(m.Content)))
		case "assistant":
			ms = append(ms, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(m.Content)}})
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := c.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(c.model),
		MaxTokens:    4096,
		System:       []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:     ms,
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow},
		Fallbacks:    anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) {
			return "", fmt.Errorf("assistant model error (HTTP %d)", apierr.StatusCode)
		}
		return "", err
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return "", errors.New("the model declined to answer this request")
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), nil
}

var _ = param.NewOpt[string]

// Citation points at a record the answer is grounded in.
type Citation struct {
	Title string `json:"title"`
	Ref   string `json:"ref"`
}

var (
	idPattern   = regexp.MustCompile(`(urn:epc:id:[a-z]+:[^\s,;]+|https?://[^\s]+/(?:01|00)/[^\s]+|\(01\)\d{14}\(21\)[^\s(]+)`)
	tlcPattern  = regexp.MustCompile(`(?i)\b(?:lot|tlc|batch)\s+([A-Za-z0-9-]{2,40})`)
	maxContext  = 12000
	systemIntro = `You are the Open Spoor assistant for one company's supply-chain workspace (EPCIS traceability, serialization, DSCSA/EU FMD/RSD compliance, food traceability).
Answer only from the CONTEXT below, which was read from this company's own data at %s. If the context does not contain the answer, say what is missing and which screen to check.
Be concise and concrete: name identifiers, counts and dates. Never invent regulatory requirements. You cannot change data; suggest the screen or action instead.`
)

// gather reads the tenant's data relevant to the question (read-only, RLS-scoped).
func gather(ctx context.Context, tx pgx.Tx, question string) (map[string]any, []Citation, error) {
	data := map[string]any{}
	var cites []Citation
	var events, serials int64
	if err := tx.QueryRow(ctx, `select (select count(*) from epcis_event), (select count(*) from serial_state)`).Scan(&events, &serials); err != nil {
		return nil, nil, err
	}
	data["totals"] = map[string]int64{"epcisEvents": events, "serials": serials}
	type kv = map[string]any
	q := func(name, sql string, args ...any) error {
		rows, err := tx.Query(ctx, `select to_jsonb(x) from (`+sql+`) x`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var out []kv
		for rows.Next() {
			var v kv
			if err := rows.Scan(&v); err != nil {
				return err
			}
			out = append(out, v)
		}
		data[name] = out
		return rows.Err()
	}
	steps := []struct{ name, sql string }{
		{"openAlerts", `select id, kind, severity, title, created_at from alert where status='open' order by created_at desc limit 15`},
		{"recentCaptures", `select id, status, event_count, error_count, created_at from epcis_capture order by created_at desc limit 5`},
		{"rejectedReports", `select id, connector, kind, alert->>'reason' as reason, alert->>'suggestedFix' as fix from regulatory_report where status in ('rejected','failed') order by updated_at desc limit 10`},
		{"pendingAgentActions", `select id, agent, title from agent_action where status='proposed' order by created_at desc limit 10`},
		{"openCases", `select id, title, priority, due_at from work_case where status <> 'resolved' order by priority, created_at limit 10`},
		{"recentShipments", `select id, direction, po_number, status, created_at from shipment order by created_at desc limit 5`},
		{"serialStatusCounts", `select status, count(*) as n from serial_state group by status order by n desc limit 12`},
	}
	for _, s := range steps {
		if err := q(s.name, s.sql); err != nil {
			return nil, nil, err
		}
	}
	for _, a := range data["openAlerts"].([]kv) {
		cites = append(cites, Citation{Title: fmt.Sprint(a["title"]), Ref: "alert/" + fmt.Sprint(a["id"])})
	}
	// Identifiers mentioned in the question: look them up.
	for _, raw := range idPattern.FindAllString(question, 3) {
		id := raw
		if strings.HasPrefix(raw, "(01)") {
			if ais, err := gs1.ParseElementString(raw); err == nil {
				if p, err := gs1.PackFromAIs(ais); err == nil {
					id = gs1.DigitalLink(gs1.DefaultResolver, p)
				}
			}
		}
		st, err := trace.Lookup(ctx, tx, id)
		if err != nil {
			data["lookup:"+raw] = "no record of this identifier"
			continue
		}
		hist, _ := trace.History(ctx, tx, id, 20)
		var steps []string
		for _, e := range hist {
			steps = append(steps, fmt.Sprintf("%s %s %s", e.EventTime.Format(time.RFC3339), e.Type, e.BizStep))
		}
		data["lookup:"+raw] = map[string]any{"state": st, "history": steps}
		cites = append(cites, Citation{Title: "Serial " + raw, Ref: "trace/serials/" + lib.Canonical(id)})
	}
	if m := tlcPattern.FindStringSubmatch(question); m != nil {
		if err := q("lot:"+m[1], `select tlc, description, status, quantity, uom from food_lot where tlc=$1
			union all select coalesce(lot,''), coalesce(gtin,''), status, count(*)::numeric, 'packs' from serial_state where tenant_id = app_tenant() and lot=$1 group by lot, gtin, status`, m[1]); err != nil {
			return nil, nil, err
		}
		cites = append(cites, Citation{Title: "Lot " + m[1], Ref: "lot/" + m[1]})
	}
	return data, cites, nil
}

// offline answers without a model: a deterministic summary of the gathered context.
func offline(question string, data map[string]any) string {
	var b strings.Builder
	t := data["totals"].(map[string]int64)
	for k, v := range data {
		if strings.HasPrefix(k, "lookup:") {
			fmt.Fprintf(&b, "%s: ", strings.TrimPrefix(k, "lookup:"))
			if m, ok := v.(map[string]any); ok {
				st := m["state"].(trace.State)
				fmt.Fprintf(&b, "status %s, last event %s", st.Status, st.LastEventTime.Format("2006-01-02 15:04"))
				if st.ParentEPC != nil {
					fmt.Fprintf(&b, ", inside %s", *st.ParentEPC)
				}
				fmt.Fprintf(&b, "; %d events in its history.\n", len(m["history"].([]string)))
			} else {
				fmt.Fprintf(&b, "%v.\n", v)
			}
		}
	}
	alerts, _ := data["openAlerts"].([]map[string]any)
	fmt.Fprintf(&b, "Workspace: %d EPCIS events, %d serials, %d open alerts", t["epcisEvents"], t["serials"], len(alerts))
	if rr, _ := data["rejectedReports"].([]map[string]any); len(rr) > 0 {
		fmt.Fprintf(&b, ", %d rejected regulatory reports (first: %v — %v)", len(rr), rr[0]["reason"], rr[0]["fix"])
	}
	if pa, _ := data["pendingAgentActions"].([]map[string]any); len(pa) > 0 {
		fmt.Fprintf(&b, ", %d agent actions awaiting approval", len(pa))
	}
	b.WriteString(".")
	if len(alerts) > 0 {
		fmt.Fprintf(&b, " Most recent alert: %v.", alerts[0]["title"])
	}
	b.WriteString(" (Answer generated without a language model; set ANTHROPIC_API_KEY to enable the conversational assistant.)")
	return b.String()
}

func (m *Module) chat(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Messages) == 0 || in.Messages[len(in.Messages)-1].Role != "user" {
		return httpx.BadRequest("messages must end with a user message")
	}
	if len(in.Messages) > 40 {
		in.Messages = in.Messages[len(in.Messages)-40:]
	}
	question := in.Messages[len(in.Messages)-1].Content
	var data map[string]any
	var cites []Citation
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		data, cites, err = gather(r.Context(), tx, question)
		if err != nil {
			return err
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "assistant.ask", Entity: "assistant", EntityID: "chat", After: map[string]any{"question": truncate(question, 500)}})
		return err
	})
	if err != nil {
		return err
	}
	ctxJSON, _ := json.Marshal(data)
	if len(ctxJSON) > maxContext {
		ctxJSON = ctxJSON[:maxContext]
	}
	reply, model := "", "none"
	if m.LLM != nil {
		system := fmt.Sprintf(systemIntro, time.Now().UTC().Format(time.RFC3339)) + "\n\nCONTEXT (JSON):\n" + string(ctxJSON)
		reply, err = m.LLM.Answer(r.Context(), system, in.Messages)
		model = m.LLM.Name()
		if err != nil {
			reply = offline(question, data) + " [Model unavailable: " + err.Error() + "]"
		}
	} else {
		reply = offline(question, data)
	}
	if cites == nil {
		cites = []Citation{}
	}
	// The answer is evidence too: which model produced what, from how many sources.
	if err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "assistant.answer", Entity: "assistant", EntityID: "chat",
			After: map[string]any{"model": model, "reply": truncate(reply, 2000), "citations": len(cites)}})
		return err
	}); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"reply": reply, "citations": cites, "model": model})
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
