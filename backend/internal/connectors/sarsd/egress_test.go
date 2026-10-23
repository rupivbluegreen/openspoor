package sarsd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/sarsd"
	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
)

func TestHubEndpointOnInternalAddressIsRefused(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	egresstest.Strict(t)
	c := sarsd.New()
	cfg := connector.Config{"baseUrl": srv.URL, "apiKey": "k"}
	if _, err := c.Submit(context.Background(), cfg, connector.Payload{Body: []byte(`{}`)}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("submit: %v", err)
	}
	if _, err := c.PollStatus(context.Background(), cfg, connector.Submission{Ref: "T1"}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("poll: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the internal server was contacted")
	}
}
