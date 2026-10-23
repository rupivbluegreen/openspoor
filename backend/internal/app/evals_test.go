package app_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openspoor/openspoor/internal/testkit"
)

func TestEvalAgentsCommand(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	var out bytes.Buffer
	res, err := e.App.EvalAgents(ctx, &out, "", nil)
	if err != nil || len(res) != 6 {
		t.Fatalf("%v %+v", err, res)
	}
	for _, w := range []string{"AGENT", "exception-triage", "reg-watch", "not accuracy figures"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("table lacks %q:\n%s", w, out.String())
		}
	}
	// Recording for a tenant writes the scores the autonomy ladder reads, audited as a system actor.
	out.Reset()
	if _, err := e.App.EvalAgents(ctx, &out, "acme", []string{"mapping-agent"}); err != nil || !strings.Contains(out.String(), "Scores recorded for tenant acme") {
		t.Fatalf("record: %v\n%s", err, out.String())
	}
	var l struct {
		Items []struct {
			Agent  string
			Latest *struct {
				Score float64
				RunBy string
			}
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/evals", nil), &l)
	n := 0
	for _, i := range l.Items {
		if i.Latest != nil {
			n++
			if i.Agent != "mapping-agent" || i.Latest.RunBy != "system:eval-agents" || i.Latest.Score != 1 {
				t.Fatalf("recorded: %+v", i)
			}
		}
	}
	if n != 1 {
		t.Fatalf("recorded %d", n)
	}
	if _, err := e.App.EvalAgents(ctx, &out, "no-such-tenant", []string{"reg-watch"}); err == nil {
		t.Fatal("unknown tenant accepted")
	}
	if _, err := e.App.EvalAgents(ctx, &out, "", []string{"nope"}); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

// The semantic layer reaches the KPI library through the wiring in agents_wiring.go.
func TestSemanticLayerUsesTheKPILibrary(t *testing.T) {
	e := testkit.New(t)
	var out struct {
		Metric string
		Rows   []struct {
			Key   string
			Value *float64
		}
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/semantic/query", map[string]any{"metric": "kpi.otif_pct", "dimension": "day", "days": 5}), &out)
	if out.Metric != "kpi.otif_pct" || len(out.Rows) == 0 {
		t.Fatalf("kpi series: %+v", out)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/semantic/query", map[string]any{"metric": "kpi.otif_pct", "days": 5}), &out)
	if len(out.Rows) != 1 || out.Rows[0].Value != nil {
		t.Fatalf("a KPI with nothing to measure has no value: %+v", out)
	}
}
