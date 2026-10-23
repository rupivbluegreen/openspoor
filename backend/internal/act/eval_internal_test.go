package act

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDatasetsAreSyntheticAndWellFormed(t *testing.T) {
	sets, err := LoadEvalDatasets()
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 6 {
		t.Fatalf("%d datasets", len(sets))
	}
	agents := map[string]bool{}
	for _, d := range sets {
		agents[d.Agent] = true
		if !d.Synthetic || d.Hash == "" || len(d.Cases) < 5 || len(d.Cases) > 20 {
			t.Errorf("%s: synthetic=%v cases=%d", d.Agent, d.Synthetic, len(d.Cases))
		}
		names := map[string]bool{}
		for _, c := range d.Cases {
			if c.Name == "" || names[c.Name] {
				t.Errorf("%s: case name %q is empty or repeated", d.Agent, c.Name)
			}
			names[c.Name] = true
			for _, r := range c.Rows {
				if _, ok := evalTables[r.Table]; !ok {
					t.Errorf("%s/%s: table %s", d.Agent, c.Name, r.Table)
				}
			}
		}
	}
	for _, a := range []string{"exception-triage", "onboarding-helper", "investigation-assistant", "inspection-agent", "mapping-agent", "reg-watch"} {
		if !agents[a] {
			t.Errorf("no dataset for %s", a)
		}
	}
	if got := EvalAgents(); len(got) != 6 {
		t.Errorf("EvalAgents: %v", got)
	}
}

func TestScoring(t *testing.T) {
	resolve := func(s string) string { return s }
	want := []EvalExpect{{Tool: "case.open", Entity: "a", Priority: 1}, {Tool: "case.open", Entity: "b", TitleContains: "x"}, {Tool: "mapping.save", Entity: "c"}}
	got := []observed{
		{Tool: "case.open", Entity: "a", Priority: 1},
		{Tool: "case.open", Entity: "b", Title: "y"}, // wrong title: false positive and false negative
		{Tool: "case.open", Entity: "z", Title: "extra"},
	}
	c, why := scoreCase(want, got, resolve)
	if c.tp != 1 || c.fp != 2 || c.fn != 2 || len(why) != 3 {
		t.Fatalf("counts %+v why %v", c, why)
	}
	if p, r := c.precision(), c.recall(); p < 0.33 || p > 0.34 || r < 0.33 || r > 0.34 {
		t.Errorf("precision %v recall %v", p, r)
	}
	if f := (counts{tp: 3}).f1(); f != 1 {
		t.Errorf("perfect f1 %v", f)
	}
	if f := (counts{}).f1(); f != 1 {
		t.Errorf("empty case is perfect: %v", f)
	}
	if f := (counts{fp: 2, fn: 2}).f1(); f != 0 {
		t.Errorf("all wrong f1 %v", f)
	}
	for _, bad := range []struct {
		w EvalExpect
		g observed
	}{
		{EvalExpect{ReasoningContains: []string{"need"}}, observed{Reasoning: []string{"other"}}},
		{EvalExpect{Fields: map[string]string{"t": "s"}}, observed{Fields: map[string]string{"t": "other"}}},
		{EvalExpect{Checklist: map[string]string{"k": "ok"}}, observed{Checklist: map[string]string{"k": "warn"}}},
	} {
		if len(checkExpect(bad.w, bad.g)) != 1 {
			t.Errorf("expectation %+v should fail", bad.w)
		}
	}
}

func TestFixtureTokens(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	r := &evalResolver{caseName: "c", now: now, server: "http://127.0.0.1:1"}
	a, b := r.ref("@x"), (&evalResolver{caseName: "d"}).ref("@x")
	if a == b || a != r.ref("@x") || r.ref("plain") != "plain" {
		t.Errorf("refs: %s %s", a, b)
	}
	got := r.resolve(map[string]any{"t": "now-72h", "u": []any{"now+30m", "now-3d", "@y", "{server}/p"}, "n": 4.0}).(map[string]any)
	u := got["u"].([]any)
	if got["t"] != "2026-04-28T12:00:00Z" || u[0] != "2026-05-01T12:30:00Z" || u[1] != "2026-04-28T12:00:00Z" || u[3] != "http://127.0.0.1:1/p" || got["n"] != 4.0 {
		t.Errorf("resolved: %v", got)
	}
	for _, s := range []string{"now", "nowhere", "now-xh", "now-5s", "now*3h", "now-h"} {
		if _, ok := parseRelative(s, now); ok {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestFormatEvalTable(t *testing.T) {
	var b bytes.Buffer
	FormatEvalTable(&b, []AgentEval{
		{Agent: "a", Cases: 2, Passed: 2, Precision: 1, Recall: 1, Score: 1, Threshold: 0.9, Pass: true},
		{Agent: "b", Cases: 2, Passed: 1, Score: 0.5, Threshold: 0.9, Failures: []EvalFailure{{Case: "c1", Detail: "wrong"}}},
		{Agent: "c", Error: "boom"},
	})
	out := b.String()
	for _, w := range []string{"AGENT", "pass", "FAIL", "error: boom", "b / c1: wrong", "not accuracy figures"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
}

func TestInsertEvalRowRefusals(t *testing.T) {
	r := &evalResolver{caseName: "c", now: time.Now()}
	for _, row := range []EvalRow{
		{Table: "app_user", Row: map[string]any{"email": "x"}},
		{Table: "alert", Row: map[string]any{"tenant_id": "x"}},
		{Table: "alert", Row: map[string]any{"kind; drop table alert": "x"}},
	} {
		if err := insertEvalRow(nil, nil, r, 0, row); err == nil { //nolint:staticcheck // refusals happen before the transaction is used
			t.Errorf("row %+v accepted", row)
		}
	}
}
