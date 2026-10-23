package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openspoor/openspoor/internal/testkit"
)

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func mcpSession(t *testing.T, e *testkit.Env, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: e.Server.URL + "/api/v1/mcp",
		HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCPServer(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Amoxicillin 250mg"}), nil)
	s := mcpSession(t, e, e.Token)

	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
		if tl.Name != "open_case" && (tl.Annotations == nil || !tl.Annotations.ReadOnlyHint) {
			t.Errorf("%s should be marked read-only", tl.Name)
		}
	}
	for _, n := range []string{"search", "serial_history", "verify_pack", "list_alerts", "query_events", "list_shipments", "open_case"} {
		if !names[n] {
			t.Errorf("missing tool %s", n)
		}
	}

	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"query": "amoxi"}})
	if err != nil || r.IsError || !strings.Contains(text(r), "Amoxicillin 250mg") {
		t.Fatalf("search: %v %v %s", err, r.IsError, text(r))
	}
	r, _ = s.CallTool(ctx, &mcp.CallToolParams{Name: "serial_history", Arguments: map[string]any{"code": "not a code"}})
	if !r.IsError {
		t.Fatalf("bad code accepted: %s", text(r))
	}
	r, _ = s.CallTool(ctx, &mcp.CallToolParams{Name: "open_case", Arguments: map[string]any{"title": "Check batch A7 rejections", "priority": 2}})
	if r.IsError || !strings.Contains(text(r), "Check batch A7 rejections") {
		t.Fatalf("open_case: %s", text(r))
	}

	// A Viewer can read but not open cases: the API's role checks apply to the assistant too.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "viewer@acme.example", "roles": []string{"Viewer"}, "password": testkit.Password}), nil)
	var login struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "acme", "email": "viewer@acme.example", "password": testkit.Password}), &login)
	vs := mcpSession(t, e, login.Token)
	r, _ = vs.CallTool(ctx, &mcp.CallToolParams{Name: "open_case", Arguments: map[string]any{"title": "x"}})
	if !r.IsError || !strings.Contains(text(r), "403") {
		t.Fatalf("viewer opened a case: %s", text(r))
	}
	r, _ = vs.CallTool(ctx, &mcp.CallToolParams{Name: "list_alerts", Arguments: map[string]any{}})
	if r.IsError {
		t.Fatalf("viewer list_alerts: %s", text(r))
	}

	// Every tool call is evidence in the audit trail.
	var audit struct{ Items []struct{ EntityID string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=mcp.tool", nil), &audit)
	if len(audit.Items) < 4 {
		t.Fatalf("audited tool calls: %d", len(audit.Items))
	}

	// No credentials, no MCP.
	res, err := http.Post(e.Server.URL+"/api/v1/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous MCP: %d", res.StatusCode)
	}
}
