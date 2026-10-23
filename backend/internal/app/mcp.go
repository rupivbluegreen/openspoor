package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// The MCP server lets a customer's own AI assistant work with Open Spoor through the same typed
// operations as the UI. Every tool replays the caller's credentials against the REST routes,
// so roles, tenant isolation and validation are exactly the API's; every call is audited.
// Tools read, or open a case for a person to work: nothing an assistant does becomes a
// regulated record without a human.

type (
	searchIn struct {
		Query string `json:"query" jsonschema:"text to find: a product name or GTIN, PO or ASN number, partner, lot code, case title, or a scanned GS1 code"`
	}
	codeIn struct {
		Code string `json:"code" jsonschema:"a GS1 Digital Link, DataMatrix element string, human-readable (01)...(21)... text or an SGTIN URN"`
	}
	alertsIn struct {
		Status string `json:"status,omitempty" jsonschema:"open or resolved; omit for all"`
		Kind   string `json:"kind,omitempty" jsonschema:"alert kind prefix, e.g. regulatory. or verify."`
	}
	eventsIn struct {
		Params map[string]string `json:"params,omitempty" jsonschema:"EPCIS 2.0 query parameters, e.g. EQ_bizStep=shipping, GE_eventTime=2026-01-01T00:00:00Z, MATCH_epc=<epc>, perPage=50"`
	}
	shipmentsIn struct {
		Direction string `json:"direction,omitempty" jsonschema:"outbound or inbound; omit for both"`
	}
	caseIn struct {
		Title    string `json:"title" jsonschema:"what a person needs to look at"`
		Kind     string `json:"kind,omitempty" jsonschema:"case kind, default assistant"`
		Priority int    `json:"priority,omitempty" jsonschema:"1 (urgent) to 4 (low), default 3"`
		Entity   string `json:"entity,omitempty" jsonschema:"related record type, e.g. serial, shipment, lot"`
		EntityID string `json:"entityId,omitempty" jsonschema:"related record id"`
	}
)

func (a *App) mcpServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "openspoor", Title: "Open Spoor", Version: Version},
		&mcp.ServerOptions{Instructions: "Tools over this tenant's Open Spoor data. All tools are read-only except open_case, " +
			"which creates a case for a person to review. Results are JSON from the Open Spoor API."})
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "search", Description: "Find products, shipments, partners, lots and cases, or open a scanned code.", Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
			return asTool(a.mcpCall(ctx, req, "search", http.MethodGet, "/api/v1/search?q="+url.QueryEscape(in.Query), nil))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "serial_history", Description: "Current state, full event history and aggregation tree of one serialized pack.", Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in codeIn) (*mcp.CallToolResult, any, error) {
			p, err := serialization.ParseCode(in.Code)
			if err != nil || p.Serial == "" {
				return toolError("could not read a serialized GS1 code: " + in.Code), nil, nil
			}
			id := url.PathEscape(gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: p.GTIN, Serial: p.Serial}))
			hist, err := a.mcpCall(ctx, req, "serial_history", http.MethodGet, "/api/v1/trace/serials/"+id, nil)
			if err != nil || hist.IsError {
				return hist, nil, err
			}
			tree, err := a.mcpCall(ctx, req, "", http.MethodGet, "/api/v1/trace/tree/"+id, nil)
			if err != nil {
				return nil, nil, err
			}
			hist.Content = append(hist.Content, tree.Content...)
			return hist, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "verify_pack", Description: "Verify a pack against the manufacturer's record, as a pharmacist's scan would.", Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in codeIn) (*mcp.CallToolResult, any, error) {
			return asTool(a.mcpCall(ctx, req, "verify_pack", http.MethodPost, "/api/v1/verify/scan", map[string]any{"code": in.Code}))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_alerts", Description: "Regulatory and operational alerts: hub rejections, suspect scans, pool exhaustion.", Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in alertsIn) (*mcp.CallToolResult, any, error) {
			q := url.Values{}
			if in.Status != "" {
				q.Set("status", in.Status)
			}
			if in.Kind != "" {
				q.Set("kind", in.Kind)
			}
			return asTool(a.mcpCall(ctx, req, "list_alerts", http.MethodGet, "/api/v1/alerts?"+q.Encode(), nil))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "query_events", Description: "EPCIS 2.0 event query with the standard query parameters.", Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in eventsIn) (*mcp.CallToolResult, any, error) {
			q := url.Values{}
			for k, v := range in.Params {
				q.Set(k, v)
			}
			if q.Get("perPage") == "" {
				q.Set("perPage", "50")
			}
			return asTool(a.mcpCall(ctx, req, "query_events", http.MethodGet, "/api/v1/epcis/events?"+q.Encode(), nil))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_shipments", Description: "Shipments with their partner, status and DSCSA transaction documents.", Annotations: ro},
		func(ctx context.Context, req *mcp.CallToolRequest, in shipmentsIn) (*mcp.CallToolResult, any, error) {
			path := "/api/v1/shipments"
			if in.Direction != "" {
				path += "?direction=" + url.QueryEscape(in.Direction)
			}
			return asTool(a.mcpCall(ctx, req, "list_shipments", http.MethodGet, path, nil))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "open_case", Description: "Open a case in the work queue for a person to review. The only tool that writes; it changes no regulated record."},
		func(ctx context.Context, req *mcp.CallToolRequest, in caseIn) (*mcp.CallToolResult, any, error) {
			if strings.TrimSpace(in.Title) == "" {
				return toolError("title is required"), nil, nil
			}
			if in.Kind == "" {
				in.Kind = "assistant"
			}
			return asTool(a.mcpCall(ctx, req, "open_case", http.MethodPost, "/api/v1/cases",
				map[string]any{"title": in.Title, "kind": in.Kind, "priority": in.Priority, "entity": in.Entity, "entityId": in.EntityID}))
		})
	return s
}

// asTool adapts mcpCall to a tool handler's results (no structured output).
func asTool(r *mcp.CallToolResult, err error) (*mcp.CallToolResult, any, error) { return r, nil, err }

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// mcpCall replays the caller's credentials against one API route and returns the JSON body as
// text content; API errors (4xx/5xx) become tool errors the assistant can read. A non-empty
// tool name records the call in the audit trail.
func (a *App) mcpCall(ctx context.Context, req *mcp.CallToolRequest, tool, method, path string, body any) (*mcp.CallToolResult, error) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	// A fresh routing context: the outer MCP request's chi context would make the router
	// treat this as a sub-route and miss.
	r := httptest.NewRequestWithContext(context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil)), method, path, rd)
	r.Header.Set("Content-Type", "application/json")
	if req.Extra != nil {
		for _, h := range []string{"Authorization", "X-API-Key", "Cookie"} {
			if v := req.Extra.Header.Get(h); v != "" {
				r.Header.Set(h, v)
			}
		}
	}
	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, r)
	if tool != "" {
		if err := a.auditTool(ctx, tool, path, rec.Code); err != nil {
			return nil, err
		}
	}
	res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: rec.Body.String()}}}
	if rec.Code >= 400 {
		res.IsError = true
		res.Content = []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("HTTP %d: %s", rec.Code, rec.Body.String())}}
	}
	return res, nil
}

func (a *App) auditTool(ctx context.Context, tool, path string, status int) error {
	p, ok := auth.From(ctx)
	if !ok {
		return nil
	}
	return a.Deps.DB.Tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		_, err := audit.Write(auth.With(ctx, p), tx, audit.Record{Action: "mcp.tool", Entity: "mcp_tool", EntityID: tool,
			After: map[string]any{"path": path, "status": status}})
		return err
	})
}
