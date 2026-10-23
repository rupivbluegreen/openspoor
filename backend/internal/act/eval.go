package act

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// Agent evaluation. Each governed agent has a small golden dataset (testdata/evals/<agent>.json):
// input fixtures (rows in a throwaway tenant) and the proposals the agent is expected to make.
// The harness runs the agent's own Observe (or, for the request-driven inspection agent, its
// package builder) against each fixture and scores precision and recall over expected proposals;
// the agent's score is the F1 of the two.
//
// The datasets are synthetic and small. A score says the agent behaves as the dataset expects, and
// nothing about accuracy on real data. The latest score per agent is recorded per tenant
// (agent_eval_score) and consulted by the autonomy ladder: a tool cannot be raised above "approve"
// unless every evaluated agent that uses it has a recorded score at or above the threshold.

// TODO(spec): the pass thresholds and the tenant minimum score are engineering defaults; whether a score
// should expire or be bound to the dataset version and agent code it was measured on is not decided
// (docs/OPEN_QUESTIONS.md).

//go:embed testdata/evals/*.json
var evalFS embed.FS

// DefaultEvalMinScore is the tenant-wide minimum score for raising a tool above "approve" when the
// tenant has not set its own (PUT /agents/evals/policy). The effective bar for an agent is the higher
// of this value and the pass threshold its dataset declares.
const DefaultEvalMinScore = 0.9

// EvalDataset is one agent's golden dataset.
type EvalDataset struct {
	Agent       string     `json:"agent"`
	Description string     `json:"description"`
	Synthetic   bool       `json:"synthetic"`
	Threshold   float64    `json:"threshold"` // pass threshold for the agent's score
	Cases       []EvalCase `json:"cases"`
	Hash        string     `json:"-"`
}

// EvalCase is one fixture and what the agent should do with it.
type EvalCase struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Rows        []EvalRow           `json:"rows"`
	Pages       map[string][]string `json:"pages,omitempty"`  // reg-watch: path to successive page versions
	Rounds      int                 `json:"rounds,omitempty"` // reg-watch: observations; the last one is scored
	Request     *EvalRequest        `json:"request,omitempty"`
	Expect      []EvalExpect        `json:"expect"`
	ExpectError string              `json:"expectError,omitempty"`
}

// EvalRow inserts one row into a (tenant-scoped) table; omitted columns take their defaults.
type EvalRow struct {
	Table string         `json:"table"`
	Row   map[string]any `json:"row"`
}

// EvalRequest is an inspection-agent request with the stubbed outputs of the modules it calls.
type EvalRequest struct {
	Kind     string         `json:"kind"`
	Scope    map[string]any `json:"scope"`
	FSMARows int            `json:"fsmaRows"`
	Tracing  map[string]any `json:"tracing"`
}

// EvalExpect is an expected proposal. Tool and Entity identify it; the other fields are checked
// when set, and a proposal whose checks fail counts as both a false positive and a false negative.
type EvalExpect struct {
	Tool              string            `json:"tool"`
	Entity            string            `json:"entity"`
	Priority          int               `json:"priority,omitempty"`
	TitleContains     string            `json:"titleContains,omitempty"`
	ReasoningContains []string          `json:"reasoningContains,omitempty"`
	Fields            map[string]string `json:"fields,omitempty"`    // mapping: target -> source
	Checklist         map[string]string `json:"checklist,omitempty"` // inspection: item key -> status
}

// EvalFailure explains one mismatch.
type EvalFailure struct {
	Case   string `json:"case"`
	Detail string `json:"detail"`
}

// AgentEval is the outcome of one agent's dataset.
type AgentEval struct {
	Agent     string        `json:"agent"`
	Cases     int           `json:"cases"`
	Passed    int           `json:"passedCases"`
	TP        int           `json:"truePositives"`
	FP        int           `json:"falsePositives"`
	FN        int           `json:"falseNegatives"`
	Precision float64       `json:"precision"`
	Recall    float64       `json:"recall"`
	Score     float64       `json:"score"`
	Threshold float64       `json:"threshold"`
	Pass      bool          `json:"pass"`
	Dataset   string        `json:"dataset"`
	Synthetic bool          `json:"synthetic"`
	Failures  []EvalFailure `json:"failures"`
	Error     string        `json:"error,omitempty"`
}

// EvalAgents lists the agents that have a dataset, in a stable order.
func EvalAgents() []string {
	ds, _ := LoadEvalDatasets()
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Agent)
	}
	return out
}

var (
	evalOnce sync.Once
	evalSets []EvalDataset
	evalErr  error
)

// LoadEvalDatasets reads the embedded datasets and refuses anything that is not marked synthetic.
func LoadEvalDatasets() ([]EvalDataset, error) {
	evalOnce.Do(func() {
		files, err := fs.Glob(evalFS, "testdata/evals/*.json")
		if err != nil {
			evalErr = err
			return
		}
		sort.Strings(files)
		for _, f := range files {
			b, err := evalFS.ReadFile(f)
			if err != nil {
				evalErr = err
				return
			}
			var d EvalDataset
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&d); err != nil {
				evalErr = fmt.Errorf("%s: %w", f, err)
				return
			}
			if !d.Synthetic {
				evalErr = fmt.Errorf("%s: a dataset must be marked synthetic", f)
				return
			}
			if d.Agent == "" || len(d.Cases) == 0 || d.Threshold <= 0 || d.Threshold > 1 {
				evalErr = fmt.Errorf("%s: agent, cases and a threshold in (0, 1] are required", f)
				return
			}
			sum := sha256.Sum256(b)
			d.Hash = hex.EncodeToString(sum[:])[:12]
			evalSets = append(evalSets, d)
		}
	})
	return evalSets, evalErr
}

// --- scoring ---

type counts struct{ tp, fp, fn int }

func (c counts) precision() float64 {
	if c.tp+c.fp == 0 {
		return 1
	}
	return float64(c.tp) / float64(c.tp+c.fp)
}

func (c counts) recall() float64 {
	if c.tp+c.fn == 0 {
		return 1
	}
	return float64(c.tp) / float64(c.tp+c.fn)
}

func (c counts) f1() float64 {
	p, r := c.precision(), c.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// observed is what an agent produced for a case, reduced to what the expectations can check.
type observed struct {
	Tool, Entity, Title string
	Priority            int
	Reasoning           []string
	Fields              map[string]string
	Checklist           map[string]string
}

func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }

// scoreCase matches observed proposals to expectations (by tool and entity) and returns counts and
// the reasons for each mismatch.
func scoreCase(want []EvalExpect, got []observed, resolve func(string) string) (c counts, why []string) {
	used := make([]bool, len(got))
	for _, w := range want {
		entity := resolve(w.Entity)
		idx := -1
		for i, g := range got {
			if !used[i] && g.Tool == w.Tool && g.Entity == entity {
				idx = i
				break
			}
		}
		if idx < 0 {
			c.fn++
			why = append(why, fmt.Sprintf("expected %s for %s, not proposed", w.Tool, w.Entity))
			continue
		}
		used[idx] = true
		if bad := checkExpect(w, got[idx]); len(bad) > 0 {
			c.fp++
			c.fn++
			why = append(why, fmt.Sprintf("%s for %s proposed but wrong: %s", w.Tool, w.Entity, strings.Join(bad, "; ")))
			continue
		}
		c.tp++
	}
	for i, g := range got {
		if !used[i] {
			c.fp++
			why = append(why, fmt.Sprintf("unexpected %s for %s (%q)", g.Tool, g.Entity, g.Title))
		}
	}
	return c, why
}

func checkExpect(w EvalExpect, g observed) (bad []string) {
	if w.Priority != 0 && g.Priority != w.Priority {
		bad = append(bad, fmt.Sprintf("priority %d, want %d", g.Priority, w.Priority))
	}
	if w.TitleContains != "" && !strings.Contains(g.Title, w.TitleContains) {
		bad = append(bad, fmt.Sprintf("title %q lacks %q", g.Title, w.TitleContains))
	}
	text := strings.Join(g.Reasoning, "\n")
	for _, s := range w.ReasoningContains {
		if !strings.Contains(text, s) {
			bad = append(bad, fmt.Sprintf("reasoning lacks %q", s))
		}
	}
	for t, s := range w.Fields {
		if g.Fields[t] != s {
			bad = append(bad, fmt.Sprintf("field %s maps %q, want %q", t, g.Fields[t], s))
		}
	}
	for k, st := range w.Checklist {
		if g.Checklist[k] != st {
			bad = append(bad, fmt.Sprintf("checklist %s is %q, want %q", k, g.Checklist[k], st))
		}
	}
	return bad
}

// --- running ---

var evalNS = uuid.MustParse("5b0b3f0e-7f0a-4a52-9a53-0a1d5e1a6e11")

// RunEvals runs the datasets of the named agents (all when none are named) against a throwaway tenant
// inside a transaction that is always rolled back: nothing is written, and the caller's own data is
// never seen by the agents under test.
func (m *Module) RunEvals(ctx context.Context, only ...string) ([]AgentEval, error) {
	sets, err := LoadEvalDatasets()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, a := range only {
		want[a] = true
	}
	all := len(want) == 0
	var todo []EvalDataset
	for _, d := range sets {
		if all || want[d.Agent] {
			todo = append(todo, d)
			delete(want, d.Agent)
		}
	}
	if len(want) > 0 {
		var unknown []string
		for a := range want {
			unknown = append(unknown, a)
		}
		sort.Strings(unknown)
		return nil, httpx.BadRequest("no evaluation dataset for: " + strings.Join(unknown, ", "))
	}
	out := make([]AgentEval, 0, len(todo))
	tx, err := m.DB.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tenant := ids.New()
	for _, s := range []string{`select set_config('app.operator', 'on', true)`} {
		if _, err := tx.Exec(ctx, s); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `insert into tenant (id, slug, name) values ($1, $2, 'Agent evaluation (synthetic)')`, tenant, "eval-"+strings.ReplaceAll(tenant.String(), "-", "")[:12]); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `select set_config('app.operator', 'off', true), set_config('app.tenant_id', $1, true)`, tenant.String()); err != nil {
		return nil, err
	}
	ev := &Module{Deps: m.Deps, tools: m.tools, agents: m.agents, Mapping: m.Mapping, InsecureWatch: true}
	// Agents that audit their own observations (reg-watch) write into the throwaway tenant, which is rolled back.
	ectx := auth.With(ctx, auth.System(tenant))
	for _, d := range todo {
		out = append(out, ev.evalDataset(ectx, tx, d))
	}
	return out, nil
}

func (m *Module) evalDataset(ctx context.Context, tx pgx.Tx, d EvalDataset) AgentEval {
	res := AgentEval{Agent: d.Agent, Cases: len(d.Cases), Threshold: d.Threshold, Dataset: d.Hash, Synthetic: true, Failures: []EvalFailure{}}
	var total counts
	for _, c := range d.Cases {
		cc, why, err := m.evalCase(ctx, tx, d.Agent, c)
		if err != nil {
			res.Error = fmt.Sprintf("case %q: %v", c.Name, err)
			return res
		}
		total.tp, total.fp, total.fn = total.tp+cc.tp, total.fp+cc.fp, total.fn+cc.fn
		if cc.fp == 0 && cc.fn == 0 {
			res.Passed++
		}
		for _, w := range why {
			res.Failures = append(res.Failures, EvalFailure{Case: c.Name, Detail: w})
		}
	}
	res.TP, res.FP, res.FN = total.tp, total.fp, total.fn
	res.Precision, res.Recall, res.Score = round4(total.precision()), round4(total.recall()), round4(total.f1())
	res.Pass = res.Score >= d.Threshold
	return res
}

// evalCase loads one fixture in a savepoint, runs the agent and rolls the savepoint back.
func (m *Module) evalCase(ctx context.Context, tx pgx.Tx, agent string, c EvalCase) (counts, []string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return counts{}, nil, err
	}
	defer func() { _ = sp.Rollback(ctx) }()
	r := &evalResolver{caseName: c.Name, now: time.Now().UTC()}
	if len(c.Pages) > 0 {
		srv := evalServer(c.Pages, &r.round)
		defer srv.Close()
		r.server = srv.URL
	}
	for i, row := range c.Rows {
		if err := insertEvalRow(ctx, sp, r, i, row); err != nil {
			return counts{}, nil, fmt.Errorf("fixture row %d (%s): %w", i, row.Table, err)
		}
	}
	var got []observed
	var runErr error
	switch agent {
	case "inspection-agent":
		got, runErr = m.evalInspection(ctx, sp, r, c)
	default:
		a, ok := m.evalAgent(agent)
		if !ok {
			return counts{}, nil, fmt.Errorf("unknown agent %q", agent)
		}
		if agent == "mapping-agent" && m.Mapping == nil {
			return counts{}, nil, errors.New("the mapping engine is not wired")
		}
		rounds := max(c.Rounds, 1)
		for i := 0; i < rounds; i++ {
			r.round.Store(int32(i))
			props, err := a.Observe(ctx, sp)
			if err != nil {
				return counts{}, nil, err
			}
			if i < rounds-1 {
				if _, err := sp.Exec(ctx, `update watch_source set last_checked_at = now() - interval '7 hours' where last_checked_at is not null`); err != nil {
					return counts{}, nil, err
				}
				continue
			}
			for _, p := range props {
				got = append(got, observe(p))
			}
		}
	}
	cnt, why := scoreCase(c.Expect, got, r.ref)
	if c.ExpectError != "" {
		if runErr != nil && strings.Contains(runErr.Error(), c.ExpectError) {
			cnt.tp++
		} else {
			cnt.fn++
			why = append(why, fmt.Sprintf("expected a refusal containing %q, got %v", c.ExpectError, runErr))
		}
	} else if runErr != nil {
		return counts{}, nil, runErr
	}
	return cnt, why, nil
}

func (m *Module) evalAgent(name string) (Agent, bool) {
	switch name {
	case "exception-triage":
		return exceptionTriage{}, true
	case "onboarding-helper":
		return onboardingHelper{}, true
	case "investigation-assistant":
		return investigationAssistant{}, true
	case "mapping-agent":
		return mappingAgent{m}, true
	case "reg-watch":
		return regWatch{m}, true
	}
	return nil, false
}

// observe reduces a proposal to what expectations can check. Priority comes from the case input;
// mapping fields from the mapping input.
func observe(p Proposal) observed {
	o := observed{Tool: p.Tool, Entity: p.EntityID, Title: p.Title, Reasoning: p.Reasoning}
	switch in := p.Input.(type) {
	case caseInput:
		o.Priority = in.Priority
	case mappingSaveInput:
		o.Fields = map[string]string{}
		for _, f := range in.Mapping.Fields {
			o.Fields[f.Target] = f.Source
		}
	}
	return o
}

// --- inspection agent (request driven) ---

type evalFSMA struct{ rows int }

func (e evalFSMA) CSV(context.Context, pgx.Tx, time.Time, time.Time, string) (string, int, error) {
	return "event_id\n" + strings.Repeat("e\n", e.rows), e.rows, nil
}

type evalTracing struct{ summary map[string]any }

func (e evalTracing) Assemble(ctx context.Context, tx pgx.Tx, id uuid.UUID) (json.RawMessage, map[string]any, error) {
	return e.Preview(ctx, tx, id)
}

func (e evalTracing) Preview(context.Context, pgx.Tx, uuid.UUID) (json.RawMessage, map[string]any, error) {
	s := map[string]any{}
	for k, v := range e.summary {
		if f, ok := v.(float64); ok {
			v = int(f) // JSON numbers; the real module reports counts as int
		}
		s[k] = v
	}
	return json.RawMessage(`{"items":[]}`), s, nil
}

func (m *Module) evalInspection(ctx context.Context, tx pgx.Tx, r *evalResolver, c EvalCase) ([]observed, error) {
	if c.Request == nil {
		return nil, errors.New("an inspection case needs a request")
	}
	ev := &Module{Deps: m.Deps, FSMA: evalFSMA{c.Request.FSMARows}, Tracing: evalTracing{r.resolve(c.Request.Tracing).(map[string]any)}}
	b, _ := json.Marshal(map[string]any{"kind": c.Request.Kind, "scope": r.resolve(c.Request.Scope)})
	var in inspectionInput
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	if err := in.validate(ctx, tx); err != nil {
		return nil, err
	}
	title, _, _, _, list, err := ev.buildPackage(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	o := observed{Tool: "inspection.assemble", Entity: in.Kind, Title: title, Checklist: map[string]string{}}
	for _, it := range list {
		o.Checklist[it.Key] = it.Status
	}
	return []observed{o}, nil
}

// --- fixtures ---

type evalResolver struct {
	caseName string
	now      time.Time
	server   string
	round    atomic.Int32
}

// ref maps "@name" to a stable uuid within the case; other strings are returned as they are.
func (r *evalResolver) ref(s string) string {
	if strings.HasPrefix(s, "@") {
		return uuid.NewSHA1(evalNS, []byte(r.caseName+"/"+s)).String()
	}
	return s
}

// resolve replaces fixture tokens in any JSON value: "@name" (a uuid), "now-72h" / "now+30m" / "now-3d"
// (a timestamp) and "{server}" inside a string (the local page server of a reg-watch case).
func (r *evalResolver) resolve(v any) any {
	switch t := v.(type) {
	case string:
		if strings.HasPrefix(t, "@") {
			return r.ref(t)
		}
		if ts, ok := parseRelative(t, r.now); ok {
			return ts.Format(time.RFC3339Nano)
		}
		return strings.ReplaceAll(t, "{server}", r.server)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = r.resolve(x)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = r.resolve(x)
		}
		return out
	}
	return v
}

func parseRelative(s string, now time.Time) (time.Time, bool) {
	if !strings.HasPrefix(s, "now") || len(s) < 6 {
		return time.Time{}, false
	}
	sign := 1
	switch s[3] {
	case '-':
		sign = -1
	case '+':
	default:
		return time.Time{}, false
	}
	unit := s[len(s)-1]
	n := 0
	for _, ch := range s[4 : len(s)-1] {
		if ch < '0' || ch > '9' {
			return time.Time{}, false
		}
		n = n*10 + int(ch-'0')
	}
	var d time.Duration
	switch unit {
	case 'm':
		d = time.Duration(n) * time.Minute
	case 'h':
		d = time.Duration(n) * time.Hour
	case 'd':
		d = time.Duration(n) * 24 * time.Hour
	default:
		return time.Time{}, false
	}
	return now.Add(time.Duration(sign) * d), true
}

// evalTables are the tables fixtures may fill; the value says whether the table has a uuid id.
var evalTables = map[string]bool{"alert": true, "work_case": true, "partner": true, "partner_onboarding": false, "partner_onboarding_item": true,
	"partner_test_run": true, "investigation": true, "watch_source": true, "mapping_sample": true, "product": true}

func insertEvalRow(ctx context.Context, tx pgx.Tx, r *evalResolver, i int, row EvalRow) error {
	hasID, ok := evalTables[row.Table]
	if !ok {
		return fmt.Errorf("table %q is not allowed in a fixture", row.Table)
	}
	vals := r.resolve(row.Row).(map[string]any)
	if hasID && vals["id"] == nil {
		vals["id"] = uuid.NewSHA1(evalNS, []byte(fmt.Sprintf("%s/%s/%d", r.caseName, row.Table, i))).String()
	}
	cols := make([]string, 0, len(vals))
	for k := range vals {
		if !reIdent.MatchString(k) || k == "tenant_id" {
			return fmt.Errorf("column %q is not allowed", k)
		}
		cols = append(cols, k)
	}
	sort.Strings(cols)
	sel := make([]string, len(cols))
	for j, c := range cols {
		sel[j] = "r." + c
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return err
	}
	// jsonb_populate_record types each value by the column, so uuids, timestamps, arrays and jsonb need no per-column code.
	q := fmt.Sprintf(`insert into %s (tenant_id, %s) select app_tenant(), %s from jsonb_populate_record(null::%s, $1::jsonb) r`,
		row.Table, strings.Join(cols, ", "), strings.Join(sel, ", "), row.Table)
	_, err = tx.Exec(ctx, q, string(b))
	return err
}

// evalServer serves the case's pages: page version number round (the last one repeats).
func evalServer(pages map[string][]string, round *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		vs, ok := pages[req.URL.Path]
		if !ok || len(vs) == 0 {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, vs[min(int(round.Load()), len(vs)-1)])
	}))
}

// --- recorded scores and the autonomy gate ---

// EvalScore is the latest recorded score of an agent.
type EvalScore struct {
	Agent     string    `json:"agent"`
	Score     float64   `json:"score"`
	Precision float64   `json:"precision"`
	Recall    float64   `json:"recall"`
	Cases     int       `json:"cases"`
	Passed    int       `json:"passedCases"`
	PassScore float64   `json:"passThreshold"`
	Dataset   string    `json:"dataset"`
	Synthetic bool      `json:"synthetic"`
	RunBy     string    `json:"runBy"`
	RunAt     time.Time `json:"runAt"`
	// Required is the bar this agent must meet for the autonomy ladder; Meets says whether it does.
	Required float64 `json:"required"`
	Meets    bool    `json:"meets"`
}

// evalMinScore returns the tenant's minimum score (the default when unset).
func evalMinScore(ctx context.Context, tx pgx.Tx) (min float64, custom bool, err error) {
	err = tx.QueryRow(ctx, `select min_score::float8 from agent_eval_policy`).Scan(&min)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultEvalMinScore, false, nil
	}
	return min, err == nil, err
}

// latestScores returns the latest recorded score per evaluated agent, judged against the tenant's bar.
func latestScores(ctx context.Context, tx pgx.Tx) (map[string]EvalScore, float64, error) {
	minScore, _, err := evalMinScore(ctx, tx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `select distinct on (agent) agent, score::float8, precision::float8, recall::float8, cases, passed_cases, pass_score::float8, dataset, synthetic, run_by, run_at
		from agent_eval_score order by agent, run_at desc, id desc`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[string]EvalScore{}
	for rows.Next() {
		var s EvalScore
		if err := rows.Scan(&s.Agent, &s.Score, &s.Precision, &s.Recall, &s.Cases, &s.Passed, &s.PassScore, &s.Dataset, &s.Synthetic, &s.RunBy, &s.RunAt); err != nil {
			return nil, 0, err
		}
		s.Required = max(minScore, s.PassScore)
		s.Meets = s.Score >= s.Required
		out[s.Agent] = s
	}
	return out, minScore, rows.Err()
}

// evalBlockers lists, for the given agents, why each cannot be trusted with more than "approve":
// no recorded score, or a score below the bar. Agents without a dataset are not gated.
func evalBlockers(ctx context.Context, tx pgx.Tx, agents []string) ([]string, error) {
	evaluated := map[string]bool{}
	for _, a := range EvalAgents() {
		evaluated[a] = true
	}
	scores, _, err := latestScores(ctx, tx)
	if err != nil {
		return nil, err
	}
	var out []string
	sort.Strings(agents)
	for _, a := range agents {
		if !evaluated[a] {
			continue
		}
		s, ok := scores[a]
		switch {
		case !ok:
			out = append(out, a+": no evaluation score recorded")
		case !s.Meets:
			out = append(out, fmt.Sprintf("%s: score %.2f is below the required %.2f", a, s.Score, s.Required))
		}
	}
	return out, nil
}

// blockedAgents returns the evaluated agents that do not currently meet the bar.
func blockedAgents(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
	scores, _, err := latestScores(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, a := range EvalAgents() {
		if s, ok := scores[a]; !ok || !s.Meets {
			out[a] = true
		}
	}
	return out, nil
}

// RecordEvals stores the results as the tenant's latest scores and writes one audit entry per agent.
// Agents whose run failed (Error set) are not recorded.
func (m *Module) RecordEvals(ctx context.Context, tenant uuid.UUID, actor string, results []AgentEval) error {
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		for _, r := range results {
			if r.Error != "" {
				continue
			}
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into agent_eval_score (id, tenant_id, agent, score, precision, recall, cases, passed_cases, pass_score, dataset, synthetic, run_by)
				values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,true,$10)`,
				id, r.Agent, r.Score, r.Precision, r.Recall, r.Cases, r.Passed, r.Threshold, r.Dataset, actor); err != nil {
				return err
			}
			if _, err := audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "agent.eval.record", Entity: "agent_eval", EntityID: r.Agent,
				After: map[string]any{"score": r.Score, "precision": r.Precision, "recall": r.Recall, "cases": r.Cases, "passedCases": r.Passed,
					"passThreshold": r.Threshold, "dataset": r.Dataset, "synthetic": true}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// FormatEvalTable renders results as the table `openspoor eval-agents` prints.
func FormatEvalTable(w io.Writer, results []AgentEval) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tCASES\tPASSED\tPRECISION\tRECALL\tSCORE\tTHRESHOLD\tRESULT")
	for _, r := range results {
		res := "pass"
		switch {
		case r.Error != "":
			res = "error: " + r.Error
		case !r.Pass:
			res = "FAIL"
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.4f\t%.4f\t%.4f\t%.2f\t%s\n", r.Agent, r.Cases, r.Passed, r.Precision, r.Recall, r.Score, r.Threshold, res)
	}
	_ = tw.Flush()
	fmt.Fprintln(w, "\nScores are measured on small synthetic datasets (internal/act/testdata/evals). They are not accuracy figures for real data.")
	for _, r := range results {
		for _, f := range r.Failures {
			fmt.Fprintf(w, "  %s / %s: %s\n", r.Agent, f.Case, f.Detail)
		}
	}
}

// --- endpoints ---

func (m *Module) evalRoutes(r chi.Router) {
	r.Get("/agents/evals", httpx.Handler(m.listEvals))
	gate := auth.Require(auth.RoleAdmin, auth.RoleCompliance)
	r.With(gate).Post("/agents/evals/run", httpx.Handler(m.runEvals))
	r.With(gate).Put("/agents/evals/policy", httpx.Handler(m.setEvalPolicy))
}

func (m *Module) listEvals(w http.ResponseWriter, r *http.Request) error {
	sets, err := LoadEvalDatasets()
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		scores, minScore, err := latestScores(r.Context(), tx)
		if err != nil {
			return err
		}
		_, custom, err := evalMinScore(r.Context(), tx)
		if err != nil {
			return err
		}
		type item struct {
			Agent       string     `json:"agent"`
			Description string     `json:"description"`
			Cases       int        `json:"cases"`
			Threshold   float64    `json:"passThreshold"`
			Dataset     string     `json:"dataset"`
			Latest      *EvalScore `json:"latest"`
		}
		items := []item{}
		for _, d := range sets {
			it := item{Agent: d.Agent, Description: d.Description, Cases: len(d.Cases), Threshold: d.Threshold, Dataset: d.Hash}
			if s, ok := scores[d.Agent]; ok {
				s := s
				it.Latest = &s
			}
			items = append(items, it)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "minScore": minScore, "minScoreIsDefault": !custom,
			"note": "Scores are measured on small synthetic datasets and say nothing about accuracy on real data."})
		return nil
	})
}

func (m *Module) runEvals(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Agents []string `json:"agents"`
	}
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	results, err := m.RunEvals(r.Context(), in.Agents...)
	if err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	if err := m.RecordEvals(r.Context(), p.TenantID, p.AuditActor(), results); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[AgentEval]{Items: results, Total: len(results)})
	return nil
}

func (m *Module) setEvalPolicy(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		MinScore float64 `json:"minScore"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.MinScore < 0.5 || in.MinScore > 1 {
		return httpx.BadRequest("minScore must be between 0.5 and 1")
	}
	in.MinScore = round4(in.MinScore)
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, _, err := evalMinScore(r.Context(), tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into agent_eval_policy (tenant_id, min_score, updated_by, updated_at) values (app_tenant(), $1, $2, now())
			on conflict (tenant_id) do update set min_score=excluded.min_score, updated_by=excluded.updated_by, updated_at=now()`, in.MinScore, p.Actor()); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "agent.eval.policy", Entity: "agent_eval_policy", EntityID: "min_score",
			Before: map[string]float64{"minScore": before}, After: map[string]float64{"minScore": in.MinScore}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"minScore": in.MinScore})
		return nil
	})
}
