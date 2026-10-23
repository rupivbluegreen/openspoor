package connectors_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
	"github.com/openspoor/openspoor/internal/core/kernel"
)

// The reference connectors share one client: mock hubs are answered in process and everything
// else goes through the egress guard, so a tenant-configured hub URL cannot reach internal addresses.
func TestConnectorClientsRefuseInternalAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	m, err := connectors.New(kernel.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	egresstest.Strict(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if res, err := m.Mocks.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "refused") {
		if res != nil {
			_ = res.Body.Close()
		}
		t.Fatalf("transport reached a loopback server: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the internal server was contacted")
	}
}
