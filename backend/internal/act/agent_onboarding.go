package act

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// onboarding-helper finds partners whose onboarding checklist has stalled and proposes the next
// concrete step as a case (the case is the reminder, with a due time from its priority). It cites
// the checklist item or failing test that blocks progress. Thresholds are fixed:
//
//	stalled        no progress (an item ticked, a test run recorded) for onboardingStallDays
//	failing test   the latest run on an incomplete test item's channel failed, and nothing has
//	               happened for onboardingFailingDays
//
// TODO(spec): the two thresholds are product defaults, not requirements; make them per-tenant
// settings if customers need different cadences.
const (
	onboardingStallDays   = 7
	onboardingFailingDays = 2
)

type onboardingHelper struct{}

func (onboardingHelper) polished()    {}
func (onboardingHelper) Name() string { return "onboarding-helper" }
func (onboardingHelper) Description() string {
	return fmt.Sprintf("Opens a case with the next concrete step when a partner's onboarding checklist has had no progress for %d days, or a test has been failing for %d days.",
		onboardingStallDays, onboardingFailingDays)
}

type obItem struct {
	key, title, kind, channel string
	done                      bool
}

type obTest struct {
	channel string
	passed  bool
	detail  map[string]any
	at      time.Time
}

func (onboardingHelper) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	type cand struct {
		id       uuid.UUID
		name     string
		template string
		last     time.Time
	}
	rows, err := tx.Query(ctx, `select p.id, p.name, o.template,
			greatest(o.started_at,
			  coalesce((select max(i.done_at) from partner_onboarding_item i where i.partner_id = p.id), o.started_at),
			  coalesce((select max(t.created_at) from partner_test_run t where t.partner_id = p.id), o.started_at))
		from partner_onboarding o join partner p on p.id = o.partner_id
		where p.status <> 'rejected' order by o.started_at limit 200`)
	if err != nil {
		return nil, err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.name, &c.template, &c.last); err != nil {
			rows.Close()
			return nil, err
		}
		if time.Since(c.last) >= time.Duration(onboardingFailingDays)*24*time.Hour {
			cands = append(cands, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []Proposal
	for _, c := range cands {
		items, err := onboardingItems(ctx, tx, c.id)
		if err != nil {
			return nil, err
		}
		tests, err := onboardingTests(ctx, tx, c.id)
		if err != nil {
			return nil, err
		}
		var open []obItem
		var failing *obTest
		var failingItem obItem
		for _, it := range items {
			ok, latest := itemSatisfied(it, tests)
			if ok {
				continue
			}
			open = append(open, it)
			if latest != nil && !latest.passed && failing == nil {
				failing, failingItem = latest, it
			}
		}
		if len(open) == 0 {
			continue
		}
		idle := time.Since(c.last)
		stalled := idle >= time.Duration(onboardingStallDays)*24*time.Hour
		if failing == nil && !stalled {
			continue
		}
		done := len(items) - len(open)
		reasoning := []string{fmt.Sprintf("Onboarding template %q: %d of %d checklist items complete.", c.template, done, len(items)),
			fmt.Sprintf("No progress for %d days (last activity %s).", int(idle.Hours()/24), c.last.UTC().Format(time.DateOnly))}
		in := caseInput{Entity: "partner", EntityID: c.id.String(), Source: "onboarding-helper"}
		var title, step string
		if failing != nil {
			step = fmt.Sprintf("Fix the %s channel and re-run the test (checklist item %q).", channelName(failing.channel), failingItem.title)
			title = fmt.Sprintf("Onboarding blocked: %s - %s test failing", c.name, channelName(failing.channel))
			reasoning = append(reasoning, fmt.Sprintf("Failing test: latest run on channel %s failed on %s%s.", channelName(failing.channel),
				failing.at.UTC().Format(time.DateOnly), failureDetail(failing.detail)))
			in.Kind, in.Priority, in.SourceID = "partner.onboarding_test_failing", 2, c.id.String()+"/test/"+failing.channel
		} else {
			next := open[0]
			step = "Complete the checklist item: " + next.title + "."
			if next.kind == "test" {
				step = fmt.Sprintf("Send the test message on the %s channel (checklist item %q); no passing run is recorded.", channelName(next.channel), next.title)
			}
			title = fmt.Sprintf("Onboarding reminder: %s - %s", c.name, strings.TrimSuffix(next.title, "."))
			in.Kind, in.Priority, in.SourceID = "partner.onboarding_stalled", 3, c.id.String()+"/"+next.key
		}
		reasoning = append(reasoning, "Proposed next step: "+step)
		if len(open) > 1 {
			reasoning = append(reasoning, fmt.Sprintf("%d more checklist items remain after this one.", len(open)-1))
		}
		in.Title, in.Notes = title, reasoning
		out = append(out, Proposal{Tool: "case.open", Title: title, Summary: step, Reasoning: reasoning, Input: in,
			Preview:    map[string]any{"priority": in.Priority, "nextStep": step},
			Confidence: 0.8, Entity: "partner", EntityID: c.id.String(), DedupeKey: "onboarding:" + in.SourceID})
	}
	return out, nil
}

func channelName(c string) string {
	if c == "" {
		return "configured"
	}
	return c
}

func failureDetail(d map[string]any) string {
	for _, k := range []string{"error", "message", "detail", "reason"} {
		if v, ok := d[k]; ok && fmt.Sprint(v) != "" {
			s := fmt.Sprint(v)
			if len(s) > 200 {
				s = s[:200] + "..."
			}
			return ": " + s
		}
	}
	return ""
}

func onboardingItems(ctx context.Context, tx pgx.Tx, partner uuid.UUID) ([]obItem, error) {
	rows, err := tx.Query(ctx, `select key, title, kind, channel, done from partner_onboarding_item where partner_id=$1 order by position`, partner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []obItem
	for rows.Next() {
		var it obItem
		if err := rows.Scan(&it.key, &it.title, &it.kind, &it.channel, &it.done); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// onboardingTests returns the partner's test runs, newest first.
func onboardingTests(ctx context.Context, tx pgx.Tx, partner uuid.UUID) ([]obTest, error) {
	rows, err := tx.Query(ctx, `select channel, passed, detail, created_at from partner_test_run where partner_id=$1 order by created_at desc, id desc limit 100`, partner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []obTest
	for rows.Next() {
		var t obTest
		var raw []byte
		if err := rows.Scan(&t.channel, &t.passed, &raw, &t.at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &t.detail)
		out = append(out, t)
	}
	return out, rows.Err()
}

// itemSatisfied mirrors the partners module's rule: a manual item is done when ticked; a test item
// when the latest run on its channel ("" = any) passed. latest is that run, if any.
func itemSatisfied(it obItem, tests []obTest) (bool, *obTest) {
	if it.kind != "test" {
		return it.done, nil
	}
	for i := range tests {
		if it.channel == "" || tests[i].channel == it.channel {
			return tests[i].passed, &tests[i]
		}
	}
	return false, nil
}
