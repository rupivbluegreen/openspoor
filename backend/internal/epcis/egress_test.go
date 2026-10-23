package epcis_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/egress/egresstest"
	"github.com/openspoor/openspoor/internal/testkit"
)

// Webhook destinations: a literal internal address is refused when the subscription is saved, and a
// host name that resolves to one is refused when the delivery connects.
func TestWebhookDeliveryRefusesInternalDestinations(t *testing.T) {
	e := testkit.New(t)
	var hits atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer hook.Close()
	egresstest.Strict(t)

	for _, dest := range []string{hook.URL, "http://169.254.169.254/latest/meta-data", "http://[::1]:8080/x", "http://10.0.0.7/hook", "https://user:pw@example.com/"} {
		r := e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": dest})
		if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "dest") {
			t.Fatalf("%s accepted: %d %s", dest, r.Status, r.Body)
		}
	}

	// "localhost" is a host name: it passes validation and is refused at connect time.
	u, _ := url.Parse(hook.URL)
	var sub struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": "http://localhost:" + u.Port() + "/hook"}), &sub)
	seedQueryEvents(t, e, time.Now().UTC().Add(-time.Hour))
	e.Drain()
	var subs struct {
		Items []struct {
			ID        string
			LastError string
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/subscriptions", nil), &subs)
	if len(subs.Items) != 1 || !strings.Contains(subs.Items[0].LastError, "refused") || hits.Load() != 0 {
		t.Fatalf("delivery to a name resolving to loopback: %+v (server hit %d times)", subs.Items, hits.Load())
	}
}
