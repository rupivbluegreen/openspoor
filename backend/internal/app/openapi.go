package app

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// publicPaths are served without authentication.
var publicPaths = map[string]bool{"/api/v1/meta": true, "/api/v1/openapi.yaml": true, "/api/v1/asyncapi.yaml": true, "/api/v1/auth/login": true, "/api/v1/saas/signup": true,
	"/api/v1/saas/branding": true, "/api/v1/saas/plans": true, "/api/v1/saas/tls-ask": true, "/api/v1/verify/scan": true, "/api/v1/vrs/verify": true,
	"/api/v1/playground/validate": true, "/api/v1/playground/convert": true, "/api/v1/playground/visualize": true, "/api/v1/federation/inbox": true,
	"/api/v1/learn": true, "/api/v1/learn/{slug}": true, "/api/v1/benchmarks/system": true, "/api/v1/marketplace": true}

// bodies documents request schemas of the core endpoints ("METHOD path" -> schema).
var bodies = map[string]map[string]any{
	"POST /api/v1/auth/login": obj(map[string]any{"tenant": str(), "email": str(), "password": str()}, "email", "password"),
	"POST /api/v1/products": obj(map[string]any{"gtin": str(), "name": str(), "ndc": str(), "companyPrefixLength": integer(), "targetMarkets": arr(str()),
		"dosageForm": str(), "strength": str(), "packSize": str(), "vertical": enum("pharma", "food", "device")}, "gtin", "name"),
	"POST /api/v1/serial-pools": obj(map[string]any{"productId": uuidS(), "market": str(), "mode": enum("random", "sequential"), "alphabet": str(),
		"length": integer(), "prefix": str(), "lowWatermarkPct": integer()}, "productId", "market"),
	"POST /api/v1/serial-pools/{id}/allocate": obj(map[string]any{"quantity": integer()}, "quantity"),
	"POST /api/v1/codes/encode":               obj(map[string]any{"gtin": str(), "serial": str(), "lot": str(), "expiry": str(), "format": enum("png", "text")}, "gtin"),
	"POST /api/v1/codes/decode":               obj(map[string]any{"code": str()}, "code"),
	"POST /api/v1/verify/scan":                obj(map[string]any{"code": str()}, "code"),
	"POST /api/v1/vrs/verify": obj(map[string]any{"gtin": str(), "serialNumber": str(), "lotNumber": str(), "expirationDate": str(),
		"requestorGLN": str(), "context": str(), "corrUUID": str()}, "gtin", "serialNumber"),
	"POST /api/v1/esignatures": obj(map[string]any{"password": str(), "meaning": str(), "reason": str(), "entity": str(), "entityId": str()},
		"password", "meaning", "reason", "entity", "entityId"),
	"POST /api/v1/shipments": obj(map[string]any{"partnerId": uuidS(), "poNumber": str(), "asnNumber": str(), "shipDate": str(), "epcs": arr(str()),
		"transport": enum("https", "as2"), "asn": map[string]any{"type": "object"}}, "partnerId", "poNumber", "epcs"),
	"POST /api/v1/playground/validate": obj(map[string]any{"document": str()}, "document"),
	"POST /api/v1/playground/convert":  obj(map[string]any{"document": str(), "to": enum("json", "xml")}, "document"),
	"POST /api/v1/sensors/readings": obj(map[string]any{"deviceId": str(), "epc": str(), "shipmentId": uuidS(), "minC": num(), "maxC": num(),
		"readings": arr(obj(map[string]any{"time": map[string]any{"type": "string", "format": "date-time"}, "tempC": num(), "humidity": num()}, "time"))}, "deviceId", "readings"),
	"POST /api/v1/assistant/chat": obj(map[string]any{"messages": arr(obj(map[string]any{"role": enum("user", "assistant"), "content": str()}, "role", "content"))}, "messages"),
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}
func str() map[string]any             { return map[string]any{"type": "string"} }
func num() map[string]any             { return map[string]any{"type": "number"} }
func integer() map[string]any         { return map[string]any{"type": "integer"} }
func uuidS() map[string]any           { return map[string]any{"type": "string", "format": "uuid"} }
func arr(items any) map[string]any    { return map[string]any{"type": "array", "items": items} }
func enum(v ...string) map[string]any { return map[string]any{"type": "string", "enum": v} }

// standardMethods are the operations OpenAPI 3.1 allows; chi's Handle also registers CONNECT, TRACE and QUERY.
var standardMethods = map[string]bool{http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true,
	http.MethodDelete: true, http.MethodHead: true, http.MethodOptions: true}

var paramRe = regexp.MustCompile(`\{([a-zA-Z]+)\}`)

// OpenAPI builds the OpenAPI 3.1 document from the live router, so every mounted
// endpoint is documented; core request bodies carry schemas.
func (a *App) OpenAPI() ([]byte, error) {
	paths := map[string]map[string]any{}
	err := chi.Walk(a.Router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !standardMethods[method] {
			return nil
		}
		route = strings.TrimSuffix(strings.ReplaceAll(route, "/*", "/{id}"), "/")
		if !strings.HasPrefix(route, "/api/v1") {
			if route != "/healthz" && route != "/readyz" && route != "/metrics" && route != "/.well-known/did.json" {
				return nil
			}
		}
		if paths[route] == nil {
			paths[route] = map[string]any{}
		}
		parts := strings.Split(strings.TrimPrefix(route, "/api/v1/"), "/")
		tag := parts[0]
		op := map[string]any{
			"operationId": opID(method, route),
			"tags":        []string{tag},
			"responses": map[string]any{
				"2XX":     map[string]any{"description": "Success", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}},
				"4XX":     map[string]any{"$ref": "#/components/responses/Problem"},
				"default": map[string]any{"$ref": "#/components/responses/Problem"},
			},
		}
		var params []any
		for _, m := range paramRe.FindAllStringSubmatch(route, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": str()})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if publicPaths[route] || !strings.HasPrefix(route, "/api/v1") {
			op["security"] = []any{}
		}
		if b, ok := bodies[method+" "+route]; ok {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": b}}}
		}
		if route == "/api/v1/epcis/capture" && method == http.MethodPost {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{
				"application/ld+json": map[string]any{"schema": map[string]any{"type": "object",
					"description": "EPCISDocument per https://ref.gs1.org/standards/epcis/2.0.0/epcis-json-schema.json"}},
				"application/xml": map[string]any{"schema": str()}}}
			op["description"] = "EPCIS 2.0 capture (JSON-LD, XML 2.0, XML 1.2). Atomic: either all events are stored or the document is rejected with per-event errors (422)."
		}
		if route == "/api/v1/serial-pools/{id}/allocate" {
			op["parameters"] = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": str()})
		}
		paths[route][strings.ToLower(method)] = op
		return nil
	})
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{"title": "Open Spoor API", "version": "1.0.0", "license": map[string]any{"name": "Apache-2.0", "identifier": "Apache-2.0"},
			"description": "Open Agentic Supply Chain OS. Generated from the router (`go run ./cmd/openspoor openapi`); every endpoint is listed."},
		"servers":  []any{map[string]any{"url": "/"}},
		"security": []any{map[string]any{"bearer": []any{}}, map[string]any{"apiKey": []any{}}},
		"paths":    paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearer": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"},
				"apiKey": map[string]any{"type": "apiKey", "in": "header", "name": "X-API-Key"},
			},
			"responses": map[string]any{"Problem": map[string]any{"description": "RFC 9457 problem document",
				"content": map[string]any{"application/problem+json": map[string]any{"schema": obj(map[string]any{"title": str(), "status": integer(), "errors": map[string]any{}}, "title", "status")}}}},
		},
	}
	return yaml.Marshal(doc)
}

func opID(method, route string) string {
	r := strings.NewReplacer("/api/v1/", "", "{", "", "}", "", ".", "", "-", "_").Replace(route)
	parts := strings.FieldsFunc(r, func(c rune) bool { return c == '/' || c == '_' })
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// RouteCount returns how many operations the spec documents.
func RouteCount(spec []byte) (int, error) {
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return 0, err
	}
	n := 0
	keys := make([]string, 0, len(doc.Paths))
	for k := range doc.Paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n += len(doc.Paths[k])
	}
	return n, nil
}

var _ = fmt.Sprint
