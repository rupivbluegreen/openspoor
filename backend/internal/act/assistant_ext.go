package act

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The assistant's two extra sources: the semantic layer (metric by dimension over a period) and the
// extracted text of uploaded documents. Both are read-only and RLS-scoped; the model never writes SQL.

const semanticPickSystem = `You choose one metric from a catalogue to answer a question about a company's supply-chain data.
Reply with only a JSON object: {"metric": "<id>", "dimension": "<id or empty>", "days": <number of days back, 1-1100>}.
Use only ids from the catalogue below; if no metric fits, reply {"metric": ""}. You never write SQL.

CATALOGUE:
`

var quantCues = []string{"how many", "number of", "count", "total", "per ", " by ", "trend", "average", "rate", "percent", "%", "over the", "last ", "past ", "since ", "this month", "this week", "this year", "today", "yesterday"}

// looksQuantitative is a cheap check that a question asks for a figure, so a model is not called to
// pick a metric for every chat message.
func looksQuantitative(q string) bool {
	q = strings.ToLower(q)
	for _, c := range quantCues {
		if strings.Contains(q, c) {
			return true
		}
	}
	return false
}

// semanticChoice reads the question into a catalogue query. The deterministic reader (names and
// synonyms from the catalogue) comes first; only when it finds no metric, and the question asks for a
// figure, a configured model picks from the catalogue. Either way the planner validates the choice, so
// a model can name a metric and a dimension but never contribute SQL.
func (m *Module) semanticChoice(ctx context.Context, question string) *SemanticQuery {
	cat, err := m.catalog()
	if err != nil {
		return nil
	}
	now := time.Now().UTC()
	if det, ok := cat.ParseQuestion(question, now); ok {
		if _, err := cat.Plan(det); err == nil {
			return &det
		}
	}
	if m.LLM == nil || !looksQuantitative(question) {
		return nil
	}
	var b strings.Builder
	for _, mt := range cat.Metrics() {
		fmt.Fprintf(&b, "- %s (%s): %s dimensions: %s\n", mt.ID, mt.Name, mt.Description, strings.Join(cat.dimsOf(mt), ", "))
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := m.LLM.Answer(cctx, semanticPickSystem+b.String(), []ChatMessage{{Role: "user", Content: truncate(question, 1000)}})
	if err != nil {
		return nil
	}
	i, j := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if i < 0 || j < i {
		return nil
	}
	var pick struct {
		Metric    string `json:"metric"`
		Dimension string `json:"dimension"`
		Days      int    `json:"days"`
	}
	if json.Unmarshal([]byte(out[i:j+1]), &pick) != nil || pick.Metric == "" {
		return nil
	}
	if pick.Days < 1 || pick.Days > semanticMaxDays {
		pick.Days = semanticDefaultDays
	}
	q := SemanticQuery{Metric: pick.Metric, Dimension: pick.Dimension, To: now, From: now.AddDate(0, 0, -pick.Days)}
	if _, err := cat.Plan(q); err != nil {
		return nil
	}
	return &q
}

// enrich adds semantic results and document passages to the assistant's context.
func (m *Module) enrich(ctx context.Context, tx pgx.Tx, question string, sq *SemanticQuery, data map[string]any, cites []Citation) ([]Citation, error) {
	if sq != nil {
		if res, err := m.RunSemantic(ctx, tx, *sq); err == nil {
			data["semantic"] = res
			cites = append(cites, Citation{Title: res.Name + " (" + res.Description + ")", Ref: "metric/" + res.Metric})
		}
	}
	if q := questionQuery(question); q != "" {
		ps, err := searchDocuments(ctx, tx, q, 3)
		if err != nil {
			return cites, err
		}
		if len(ps) > 0 {
			data["documentPassages"] = ps
			for _, p := range ps {
				cites = append(cites, Citation{Title: p.Document, Ref: p.Ref})
			}
		}
	}
	return cites, nil
}

// offlineExtra renders the semantic result and cited passages in the model-free answer.
func offlineExtra(data map[string]any) string {
	var b strings.Builder
	if res, ok := data["semantic"].(SemanticResult); ok {
		fmt.Fprintf(&b, " %s:", res.Description)
		if res.Dimension == "" && len(res.Rows) == 1 {
			fmt.Fprintf(&b, " %s.", fmtValue(res.Rows[0].Value, res.Unit))
		} else if len(res.Rows) == 0 {
			b.WriteString(" no data in the period.")
		} else {
			for i, r := range res.Rows {
				if i == 8 {
					fmt.Fprintf(&b, " (+%d more)", len(res.Rows)-8)
					break
				}
				fmt.Fprintf(&b, " %s %s;", r.Key, fmtValue(r.Value, res.Unit))
			}
		}
	}
	if ps, ok := data["documentPassages"].([]Passage); ok && len(ps) > 0 {
		fmt.Fprintf(&b, " From your documents: %q (%s, passage %d).", truncate(ps[0].Text, 200), ps[0].Document, ps[0].Seq)
	}
	return b.String()
}

func fmtValue(v *float64, unit string) string {
	if v == nil {
		return "no value"
	}
	if unit == "count" || *v == float64(int64(*v)) {
		return fmt.Sprintf("%d", int64(*v))
	}
	return fmt.Sprintf("%.2f", *v)
}
