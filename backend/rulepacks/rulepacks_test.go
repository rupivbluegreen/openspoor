package rulepacks

import (
	"strings"
	"testing"
)

func TestLoadAndTodoRules(t *testing.T) {
	packs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]Pack{}
	for _, p := range packs {
		ids[p.ID] = p
	}
	for _, id := range []string{"ae-tatmeen", "eg-eda"} {
		p, ok := ids[id]
		if !ok || p.Reporting.Connector != id {
			t.Fatalf("%s missing: %+v", id, p)
		}
		todo := 0
		for _, r := range p.Rules {
			if !r.Enforced() {
				todo++
				if r.Check != nil || !strings.HasPrefix(r.Todo, "TODO(spec)") {
					t.Fatalf("%s/%s", id, r.ID)
				}
			}
		}
		if todo == 0 {
			t.Fatalf("%s: unknowns must be listed as TODO(spec)", id)
		}
	}
	for _, r := range []Rule{{ID: "a"}, {ID: "b", Todo: "TODO(spec): x", Check: &Check{}}} {
		if r.validate() == nil {
			t.Fatalf("%s must be invalid", r.ID)
		}
	}
}
