package partners_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
	"github.com/openspoor/openspoor/internal/testkit"
)

// A partner's health URL is tenant input: the probe must not reach internal addresses.
func TestHealthProbeRefusesInternalEndpoint(t *testing.T) {
	e := testkit.New(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	var p partner
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Internal", "endpoints": map[string]string{"health": srv.URL}}), &p)

	egresstest.Strict(t)
	var h map[string]any
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+p.ID+"/health", nil), &h)
	if h["status"] != "down" || !strings.Contains(stringOf(h["detail"]), "refused") || hits.Load() != 0 {
		t.Fatalf("probe: %+v (server hit %d times)", h, hits.Load())
	}
}
