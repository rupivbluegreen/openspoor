package dscsa_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
	"github.com/openspoor/openspoor/internal/testkit"
)

// With the default (strict) outbound policy a partner endpoint on a loopback address is never
// contacted, on either transport; the send fails with a recorded error and an alert.
func TestHTTPSTransportRefusesInternalPartnerEndpoint(t *testing.T) {
	e := testkit.New(t)
	_, epcs := commissionPacks(t, e, 2)
	capture(t, e, commission(epcs, "L1", "2030-01-31", "active", time.Now().UTC().Add(-time.Hour)))
	var hits atomic.Int32
	partner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer partner.Close()
	pid := createPartner(t, e, map[string]any{"name": "Internal", "endpoints": map[string]string{"epcisCapture": partner.URL}})
	var s struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pid, "poNumber": "PO-SSRF", "epcs": epcs[:1]}), &s)

	egresstest.Strict(t)
	var sent struct {
		Status   string
		Response map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+s.ID+"/send", nil), &sent)
	msg, _ := sent.Response["error"].(string)
	if sent.Status != "send_failed" || !strings.Contains(msg, "egress to 127.0.0.1 refused") || hits.Load() != 0 {
		t.Fatalf("strict policy: %+v (partner hit %d times)", sent, hits.Load())
	}
}

func TestAS2TransportRefusesInternalPartnerEndpoint(t *testing.T) {
	p := newAS2Pair(t)
	id := p.shipment(t, "PO-AS2-SSRF", p.epcs[:2])
	egresstest.Strict(t)
	var r sendResult
	p.e.Must(http.StatusOK, p.e.Do("POST", "/api/v1/shipments/"+id+"/send", nil), &r)
	msg, _ := r.Response["error"].(string)
	if r.Status != "send_failed" || !strings.Contains(msg, "egress to 127.0.0.1 refused") {
		t.Fatalf("strict policy: %+v", r)
	}
	if in := p.shipments(t, p.globex.token, "inbound"); len(in) != 0 {
		t.Fatalf("the receiving node got a message: %+v", in)
	}
}
