package act_test

import (
	"net/http"
	"testing"

	"github.com/openspoor/openspoor/internal/testkit"
)

func TestAssistantAnswersAreAudited(t *testing.T) {
	e := testkit.New(t)
	var r struct{ Reply, Model string }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/assistant/chat", map[string]any{"messages": []map[string]any{{"role": "user", "content": "How many open alerts?"}}}), &r)
	var a struct {
		Items []struct {
			Action string
			After  map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=assistant.", nil), &a)
	var asked, answered bool
	for _, it := range a.Items {
		switch it.Action {
		case "assistant.ask":
			asked = true
		case "assistant.answer":
			answered = it.After["model"] == r.Model && it.After["reply"] != ""
		}
	}
	if !asked || !answered {
		t.Fatalf("audit trail of the exchange: %+v", a.Items)
	}
}
