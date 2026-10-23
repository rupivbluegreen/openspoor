package hubjson_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/egeda"
	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
)

func TestJSONHubEndpointOnInternalAddressIsRefused(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	egresstest.Strict(t)
	c := egeda.New()
	cfg := connector.Config{"baseUrl": srv.URL, "clientId": "c", "clientSecret": "s", "participantId": "P"}
	if _, err := c.Submit(context.Background(), cfg, connector.Payload{Key: "k", Body: []byte(`{}`)}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("submit: %v", err)
	}
	if _, err := c.PollStatus(context.Background(), cfg, connector.Submission{Ref: "R1"}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("poll: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the internal server was contacted")
	}
}
