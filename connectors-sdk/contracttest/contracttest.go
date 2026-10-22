// Package contracttest checks that a connector honours the SDK contract. Connector authors
// call Run from their own tests with a config pointing at a mock or sandbox hub.
package contracttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	connector "github.com/openspoor/connectors-sdk"
)

// Case is one scenario.
type Case struct {
	Name  string
	Event connector.Event
	// WantPayloads is the expected number of payloads from Map (-1 = any ≥ 1).
	WantPayloads int
	// WantState is the terminal state expected after Submit + PollStatus.
	WantState connector.State
}

// Run executes the contract checks.
func Run(t *testing.T, c connector.Connector, cfg connector.Config, cases []Case) {
	t.Helper()
	info := c.Info()
	if info.ID == "" || info.Name == "" || info.Country == "" || len(info.Triggers) == 0 {
		t.Fatalf("Info must set id, name, country and triggers: %+v", info)
	}
	for _, f := range info.Config {
		if f.Required && f.Default == "" && cfg[f.Key] == "" {
			t.Fatalf("required config %q missing in test config", f.Key)
		}
	}
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			p1, err := c.Map(ctx, tc.Event)
			if err != nil {
				t.Fatalf("Map: %v", err)
			}
			p2, _ := c.Map(ctx, tc.Event)
			a, _ := json.Marshal(p1)
			b, _ := json.Marshal(p2)
			if string(a) != string(b) {
				t.Fatal("Map must be deterministic (same event, same payloads and keys)")
			}
			if tc.WantPayloads >= 0 && len(p1) != tc.WantPayloads {
				t.Fatalf("Map produced %d payloads, want %d", len(p1), tc.WantPayloads)
			}
			if tc.WantPayloads < 0 && len(p1) == 0 {
				t.Fatal("Map produced no payloads")
			}
			seen := map[string]bool{}
			for _, p := range p1 {
				if p.Key == "" || p.Kind == "" || !json.Valid(p.Body) {
					t.Fatalf("payload needs kind, key and a JSON body: %+v", p)
				}
				if seen[p.Key] {
					t.Fatalf("duplicate payload key %s", p.Key)
				}
				seen[p.Key] = true
			}
			if tc.WantState == "" {
				return
			}
			for _, p := range p1 {
				sub, err := c.Submit(ctx, cfg, p)
				if err != nil {
					t.Fatalf("Submit: %v", err)
				}
				if sub.Ref == "" {
					t.Fatal("Submit must return a reference")
				}
				var st connector.Status
				deadline := time.Now().Add(10 * time.Second)
				for {
					st, err = c.PollStatus(ctx, cfg, sub)
					if err != nil {
						t.Fatalf("PollStatus: %v", err)
					}
					if st.State != connector.Pending || time.Now().After(deadline) {
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
				if st.State != tc.WantState {
					t.Fatalf("state %s, want %s (%+v)", st.State, tc.WantState, st.Rejection)
				}
				if st.State == connector.Rejected {
					if st.Rejection == nil {
						t.Fatal("rejected status must carry a Rejection")
					}
					al := c.HandleRejection(ctx, *st.Rejection)
					if al.Title == "" || al.Reason == "" || al.SuggestedFix == "" {
						t.Fatalf("HandleRejection must give title, reason and a suggested fix: %+v", al)
					}
				}
			}
		})
	}
}
