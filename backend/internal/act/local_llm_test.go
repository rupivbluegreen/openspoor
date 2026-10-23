package act

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/config"
)

func TestLocalLLM(t *testing.T) {
	var got struct {
		Model    string
		Messages []struct{ Role, Content string }
		Tools    any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if strings.Contains(got.Messages[len(got.Messages)-1].Content, "fail") {
			http.Error(w, "model not loaded", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Lot L1 is recalled."},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	// The local model wins over a configured cloud model: nothing leaves the site.
	l := NewLLM(config.Config{LocalLLMURL: srv.URL + "/", LocalLLMModel: "open-weight-8b", AnthropicKey: "k", AnthropicModel: "m"})
	if l == nil || l.Name() != "open-weight-8b" {
		t.Fatalf("local model not selected: %v", l)
	}
	ans, err := l.Answer(context.Background(), "SYSTEM", []ChatMessage{{Role: "user", Content: "Is L1 recalled?"}, {Role: "tool", Content: "dropped"}})
	if err != nil || ans != "Lot L1 is recalled." {
		t.Fatalf("answer: %q %v", ans, err)
	}
	if got.Model != "open-weight-8b" || len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[0].Content != "SYSTEM" || got.Tools != nil {
		t.Fatalf("request: %+v", got)
	}
	if _, err := l.Answer(context.Background(), "S", []ChatMessage{{Role: "user", Content: "fail"}}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("server error not reported: %v", err)
	}
	unreachable := &localLLM{url: "http://127.0.0.1:1/v1/chat/completions", model: "x", client: &http.Client{Timeout: time.Second}}
	if _, err := unreachable.Answer(context.Background(), "S", []ChatMessage{{Role: "user", Content: "q"}}); err == nil {
		t.Fatal("unreachable server: no error")
	}
	if NewLLM(config.Config{}) != nil {
		t.Fatal("no model configured should mean no LLM")
	}
}
