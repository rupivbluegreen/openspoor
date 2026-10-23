package saas_test

import (
	"net/http"
	"testing"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/testkit"
)

func TestRegionPinning(t *testing.T) {
	e := testkit.New(t, func(c *config.Config) { c.Mode, c.Region = config.ModeSaaS, "me-central" })
	var meta struct{ Region string }
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/meta", nil), &meta)
	if meta.Region != "me-central" {
		t.Fatalf("meta region: %q", meta.Region)
	}
	signup := func(region string) int {
		return e.Call("", "POST", "/api/v1/saas/signup", map[string]any{"slug": "gulf-" + region, "companyName": "Gulf Pharma",
			"email": "admin@gulf.example", "password": testkit.Password, "region": region}).Status
	}
	if code := signup("eu"); code != http.StatusBadRequest {
		t.Fatalf("signup for another region: %d, want 400", code)
	}
	if code := signup("me-central"); code != http.StatusCreated && code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("signup in this region: %d", code)
	}
}
