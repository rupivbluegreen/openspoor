package app_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/openspoor/openspoor/internal/testkit"
)

func TestGlobalSearch(t *testing.T) {
	e := testkit.New(t)
	other, otherToken := e.NewTenant("rival", "admin@rival.example")
	_ = other
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Amoxicillin 250mg"}), nil)
	e.Must(http.StatusCreated, e.Call(otherToken, "POST", "/api/v1/products", map[string]any{"gtin": "09506000134352", "name": "Amoxicillin (other tenant)"}), nil)

	type res struct {
		Items []struct{ Kind, ID, Title, Path string }
	}
	var r res
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/search?q=amoxi", nil), &r)
	if len(r.Items) != 1 || r.Items[0].Kind != "product" || r.Items[0].Title != "Amoxicillin 250mg" {
		t.Fatalf("name search (must not leak the other tenant): %+v", r.Items)
	}
	if r.Items[0].Path != "/settings/products/"+r.Items[0].ID {
		t.Fatalf("product path: %s", r.Items[0].Path)
	}

	// A scanned Digital Link opens serial lookup.
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/search?q="+url.QueryEscape("https://id.gs1.org/01/09506000134352/21/ABC123"), nil), &r)
	if len(r.Items) == 0 || r.Items[0].Kind != "serial" || r.Items[0].Title != "ABC123" {
		t.Fatalf("code search: %+v", r.Items)
	}

	// LIKE wildcards in the query are literal.
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/search?q=%25%25", nil), &r)
	if len(r.Items) != 0 {
		t.Fatalf("wildcards matched: %+v", r.Items)
	}
}
