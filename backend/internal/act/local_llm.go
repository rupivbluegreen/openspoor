package act

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// localLLM calls an open-weight model served on the site through the OpenAI-compatible chat
// completions route that local inference servers (vLLM, Ollama and others) expose. It is
// meant for air-gapped hubs; no tools are passed, so the model can only answer.
type localLLM struct {
	url, model string
	client     *http.Client
}

func (l *localLLM) Name() string { return l.model }

func (l *localLLM) Answer(ctx context.Context, system string, msgs []ChatMessage) (string, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := struct {
		Model       string    `json:"model"`
		Messages    []message `json:"messages"`
		Temperature float64   `json:"temperature"`
		MaxTokens   int       `json:"max_tokens"`
	}{Model: l.model, Temperature: 0, MaxTokens: 1024, Messages: []message{{Role: "system", Content: system}}}
	for _, m := range msgs {
		if m.Role == "user" || m.Role == "assistant" {
			body.Messages = append(body.Messages, message{Role: m.Role, Content: m.Content})
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := l.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("local model: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("local model: HTTP %d: %.200s", res.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message      struct{ Content string } `json:"message"`
			FinishReason string                   `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("local model: unreadable response")
	}
	return out.Choices[0].Message.Content, nil
}
