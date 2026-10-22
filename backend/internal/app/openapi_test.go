package app_test

import (
	"os"
	"testing"

	"github.com/openspoor/openspoor/internal/app"
	"github.com/openspoor/openspoor/internal/testkit"
)

// TestOpenAPIUpToDate fails when a route is added without regenerating the committed spec:
//
//	go run ./cmd/openspoor openapi > internal/app/openapi.yaml   (with OPENSPOOR_DEV_AUTH=true)
func TestOpenAPIUpToDate(t *testing.T) {
	e := testkit.New(t)
	gen, err := e.App.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := app.RouteCount(gen)
	if n < 200 {
		t.Fatalf("only %d operations documented", n)
	}
	if string(gen) != string(committed) {
		if os.Getenv("UPDATE_OPENAPI") == "1" {
			_ = os.WriteFile("openapi.yaml", gen, 0o644)
			return
		}
		t.Fatal("internal/app/openapi.yaml is stale: run UPDATE_OPENAPI=1 go test ./internal/app -run TestOpenAPIUpToDate")
	}
}
