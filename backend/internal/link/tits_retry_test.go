package link_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// shippablePacks registers a product, commissions n packs and returns their SGTIN URNs.
func shippablePacks(t *testing.T, e *testkit.Env, n int) []string {
	t.Helper()
	const gtin = "09506000134352"
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Metformin 850mg", "ndc": "50090-1234-01"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "US"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": n}, "Idempotency-Key", "k"), &a)
	var epcs []string
	for _, s := range a.Serials {
		u, _ := gs1.SGTINURN(gtin, s, 7)
		epcs = append(epcs, u)
	}
	ev := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: time.Now().UTC().Add(-time.Hour), EventTimeZoneOffset: "+00:00",
		EPCList: epcs, ILMD: map[string]any{"lotNumber": "L1", "itemExpirationDate": "2030-01-31"}}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{ev}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()
	return epcs
}

func titsRows(t *testing.T, e *testkit.Env, token string) []logEntry {
	t.Helper()
	var page logPage
	e.Must(http.StatusOK, e.Call(token, "GET", "/api/v1/messages?kind=tits&direction=outbound", nil), &page)
	return page.Items
}

func TestTITSRetryThroughOutboundPath(t *testing.T) {
	e := testkit.New(t)
	epcs := shippablePacks(t, e, 3)
	var calls atomic.Int32
	var failing atomic.Bool
	failing.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failing.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	var pt struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Wholesaler", "endpoints": map[string]string{"epcisCapture": srv.URL}}), &pt)
	ship := func(po string) string {
		var s struct{ ID string }
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pt.ID, "poNumber": po, "epcs": epcs[:1]}), &s)
		return s.ID
	}
	id := ship("PO-RETRY-1")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+id+"/send", nil), nil)

	rows := titsRows(t, e, e.Token)
	if len(rows) != 1 || rows[0].ID != id || rows[0].Status != "failed" || !rows[0].Retryable || rows[0].Attempts != 1 || !strings.Contains(rows[0].LastError, "502") {
		t.Fatalf("failed delivery row: %+v", rows)
	}

	// Roles and tenancy.
	var viewer struct{ Token string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "v@acme.test", "roles": []string{"Viewer"}, "password": testkit.Password}), nil)
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "v@acme.test", "password": testkit.Password}), &viewer)
	problem(t, e.Call(viewer.Token, "POST", "/api/v1/messages/"+id+"/retry", nil), 403, "missing role")
	_, other := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(other, "POST", "/api/v1/messages/"+id+"/retry", nil), 404, "not found")
	if calls.Load() != 1 {
		t.Fatalf("refused retries must not send: %d calls", calls.Load())
	}

	// Still down: the retry runs the outbound path, fails again and stays retryable.
	var res struct {
		Status   string
		Response map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/messages/"+id+"/retry", nil), &res)
	if res.Status != "send_failed" || calls.Load() != 2 {
		t.Fatalf("second failure: %+v calls=%d", res, calls.Load())
	}
	rows = titsRows(t, e, e.Token)
	if rows[0].Status != "failed" || !rows[0].Retryable || rows[0].Attempts != 2 {
		t.Fatalf("after failed retry: %+v", rows)
	}
	if got := len(alerts(t, e, "dscsa.send_failed", "open")); got != 2 {
		t.Fatalf("open send_failed alerts: %d", got)
	}

	// Partner back: the retry succeeds, resolves the alerts and is audited.
	failing.Store(false)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/messages/"+id+"/retry", nil), &res)
	if res.Status != "sent" || res.Response["status"] != float64(200) {
		t.Fatalf("retry: %+v", res)
	}
	rows = titsRows(t, e, e.Token)
	if rows[0].Status != "sent" || rows[0].Retryable || rows[0].Attempts != 3 || rows[0].LastError != "" {
		t.Fatalf("after retry: %+v", rows)
	}
	if got := len(alerts(t, e, "dscsa.send_failed", "open")); got != 0 {
		t.Fatalf("alerts not resolved: %d", got)
	}
	problem(t, e.Do("POST", "/api/v1/messages/"+id+"/retry", nil), 409, "only a failed TI/TS delivery")
	var aud struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=message.retry", nil), &aud)
	if len(aud.Items) != 2 {
		t.Fatalf("message.retry audit entries: %d", len(aud.Items))
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=shipment.send", nil), &aud)
	if len(aud.Items) != 3 {
		t.Fatalf("shipment.send audit entries (initial + two retries): %d", len(aud.Items))
	}

	// A retry that another retry holds ("sending") is refused until its claim goes stale.
	failing.Store(true)
	id2 := ship("PO-RETRY-2")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+id2+"/send", nil), nil)
	opExec(t, e, `update shipment set status='sending', response=jsonb_build_object('claimedAt', to_char(now() at time zone 'utc','YYYY-MM-DD"T"HH24:MI:SS"Z"')) where id=$1`, id2)
	for _, r := range titsRows(t, e, e.Token) {
		if r.ID == id2 && (r.Status != "retrying" || r.Retryable) {
			t.Fatalf("claimed row: %+v", r)
		}
	}
	problem(t, e.Do("POST", "/api/v1/messages/"+id2+"/retry", nil), 409, "another retry")
	opExec(t, e, `update shipment set response=jsonb_build_object('claimedAt', to_char((now() - interval '10 minutes') at time zone 'utc','YYYY-MM-DD"T"HH24:MI:SS"Z"')) where id=$1`, id2)
	for _, r := range titsRows(t, e, e.Token) {
		if r.ID == id2 && !r.Retryable {
			t.Fatalf("stale claim must be retryable: %+v", r)
		}
	}
	failing.Store(false)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/messages/"+id2+"/retry", nil), &res)
	if res.Status != "sent" {
		t.Fatalf("takeover of stale claim: %+v", res)
	}

	// A suspended partner cannot be retried; an inbound TI/TS is not retryable at all.
	failing.Store(true)
	id3 := ship("PO-RETRY-3")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+id3+"/send", nil), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+pt.ID+"/suspend", nil), nil)
	problem(t, e.Do("POST", "/api/v1/messages/"+id3+"/retry", nil), 409, "suspended")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+pt.ID+"/approve", nil), nil)
	opExec(t, e, `insert into shipment (id, tenant_id, direction, po_number, status) values ('00000000-0000-0000-0000-0000000000aa', $1, 'inbound', 'PO-IN', 'received')`, e.Tenant.ID)
	problem(t, e.Do("POST", "/api/v1/messages/00000000-0000-0000-0000-0000000000aa/retry", nil), 409, "not delivered through the retry queue")

	// Without the dscsa wiring the retry is unavailable rather than silently ignored.
	l := linkOf(e)
	keep := l.RetryShipment
	l.RetryShipment = nil
	problem(t, e.Do("POST", "/api/v1/messages/"+id3+"/retry", nil), 409, "not available")
	l.RetryShipment = keep
}
