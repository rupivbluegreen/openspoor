package link_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/testkit"
)

type logEntry struct {
	ID, Kind, Direction, Type, PartnerName, Reference, Channel, Status, LastError string
	PartnerID                                                                     *string
	Attempts                                                                      int
	Retryable                                                                     bool
}

type logPage struct {
	Items  []logEntry
	Counts map[string]int
}

func alerts(t *testing.T, e *testkit.Env, kind, status string) []struct{ ID, Kind, Title, Status string } {
	t.Helper()
	var al struct {
		Items []struct{ ID, Kind, Title, Status string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind="+kind+"&status="+status, nil), &al)
	return al.Items
}

func opExec(t *testing.T, e *testkit.Env, sql string, args ...any) {
	t.Helper()
	if err := e.App.Deps.DB.OperatorTx(context.Background(), func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMessageTrackingDeadLetterAndRetry(t *testing.T) {
	e := testkit.New(t)
	l := linkOf(e)
	l.DeliveryAttempts = 2
	var up atomic.Bool
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer stub.Close()
	pub, _, _ := ed25519.GenerateKey(nil)
	var peer struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "Remote", "url": stub.URL, "remoteTenant": "acme",
		"did": "did:web:remote.test", "publicKey": base64.RawURLEncoding.EncodeToString(pub)}), &peer)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/federation/peers/"+peer.ID+"/approve", nil), nil)
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Remote Pharma", "endpoints": map[string]string{"federation": peer.ID}}), &p)

	var d doc
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(p.ID, "PO-DLQ")), &d)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), nil)
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": peer.ID, "type": "ping"}), nil)

	var page logPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages", nil), &page)
	if len(page.Items) != 2 || page.Counts["queued"] != 2 {
		t.Fatalf("queued log: %+v", page)
	}
	// Two failing attempts exhaust the retries: the delivery is dead-lettered and alerted.
	e.Drain()
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?status=retrying", nil), &page)
	if len(page.Items) != 2 || page.Items[0].Attempts != 1 || page.Items[0].LastError == "" || !page.Items[0].Retryable {
		t.Fatalf("retrying: %+v", page.Items)
	}
	if _, err := e.App.Deps.DB.Pool.Exec(context.Background(), `update job set run_at = now() where kind = 'federation.deliver'`); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?status=dead", nil), &page)
	if len(page.Items) != 2 || page.Counts["dead"] != 2 {
		t.Fatalf("dead letters: %+v", page)
	}
	var b2b logEntry
	for _, it := range page.Items {
		if it.Kind == "b2b" {
			b2b = it
		}
	}
	if b2b.ID != d.ID || b2b.Reference != "PO-DLQ" || b2b.Attempts != 2 || b2b.PartnerName != "Remote Pharma" || b2b.Channel != "federation" || !b2b.Retryable {
		t.Fatalf("dead b2b entry: %+v", b2b)
	}
	if a := alerts(t, e, "link.message_dead", "open"); len(a) != 2 {
		t.Fatalf("dead-letter alerts: %+v", a)
	}

	// Retry now: the partner is back; the document is delivered and acknowledged.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(viewer, "POST", "/api/v1/messages/"+d.ID+"/retry", nil), 403, "missing role")
	up.Store(true)
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/messages/"+d.ID+"/retry", nil), nil)
	e.Drain()
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?kind=b2b", nil), &page)
	if len(page.Items) != 1 || page.Items[0].Status != "acknowledged" || page.Items[0].Retryable {
		t.Fatalf("after retry: %+v", page.Items)
	}
	problem(t, e.Do("POST", "/api/v1/messages/"+d.ID+"/retry", nil), 409, "already delivered")
	if a := alerts(t, e, "link.message_dead", "open"); len(a) != 1 {
		t.Fatalf("dead-letter alert not resolved by retry: %+v", a)
	}
	var fed logEntry
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?kind=federation&q=ping", nil), &page)
	if len(page.Items) != 1 || page.Items[0].Status != "dead" || page.Items[0].PartnerID == nil || *page.Items[0].PartnerID != p.ID {
		t.Fatalf("federation entry: %+v", page.Items)
	}
	fed = page.Items[0]
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/messages/"+fed.ID+"/retry", nil), nil)
	e.Drain()
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?kind=federation", nil), &page)
	if page.Items[0].Status != "delivered" {
		t.Fatalf("federation retry: %+v", page.Items)
	}

	// Documents not delivered through the queue cannot be retried.
	var q struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "EDI Partner"}), &q)
	var d2 doc
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(q.ID, "PO-EDI")), &d2)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+d2.ID+"/send", nil), nil)
	problem(t, e.Do("POST", "/api/v1/messages/"+d2.ID+"/retry", nil), 409, "not delivered through the retry queue")
	problem(t, e.Do("POST", "/api/v1/messages/00000000-0000-0000-0000-000000000001/retry", nil), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/messages/x/retry", nil), 400, "invalid id")
	opExec(t, e, `update federation_message set job_id = null where id = $1`, fed.ID)
	problem(t, e.Do("POST", "/api/v1/messages/"+fed.ID+"/retry", nil), 409, "no delivery job")

	// TI/TS deliveries appear in the log (read from shipments).
	opExec(t, e, `insert into shipment (id, tenant_id, direction, partner_id, asn_number, status, response) values (gen_random_uuid(), $1, 'outbound', $2, 'ASN-7', 'send_failed', '{"error":"boom"}')`, e.Tenant.ID, q.ID)
	opExec(t, e, `insert into shipment (id, tenant_id, direction, po_number, status) values (gen_random_uuid(), $1, 'inbound', 'PO-IN', 'received')`, e.Tenant.ID)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?kind=tits", nil), &page)
	if len(page.Items) != 2 {
		t.Fatalf("tits: %+v", page.Items)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?kind=tits&direction=outbound&partnerId="+q.ID, nil), &page)
	if len(page.Items) != 1 || page.Items[0].Status != "failed" || page.Items[0].LastError != "boom" || page.Items[0].Reference != "ASN-7" {
		t.Fatalf("tits entry: %+v", page.Items)
	}

	// Tenant isolation.
	_, other := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/messages", nil), &page)
	if len(page.Items) != 0 {
		t.Fatalf("other tenant sees messages: %+v", page.Items)
	}
	problem(t, e.Call(other, "POST", "/api/v1/messages/"+d.ID+"/retry", nil), 404, "not found")

	var aud struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=message.retry", nil), &aud)
	if len(aud.Items) != 2 {
		t.Fatalf("retry audit: %d", len(aud.Items))
	}
}

func TestPartnerSLABreaches(t *testing.T) {
	e := testkit.New(t)
	l := linkOf(e)
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Slow Partner"}), &p)

	// SLA settings: validation, roles, upsert.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(viewer, "PUT", "/api/v1/messages/slas/"+p.ID, map[string]any{"ackHours": 2}), 403, "missing role")
	problem(t, e.Do("PUT", "/api/v1/messages/slas/"+p.ID, map[string]any{"ackHours": 0}), 400, "ackHours")
	problem(t, e.Do("PUT", "/api/v1/messages/slas/00000000-0000-0000-0000-000000000001", map[string]any{"ackHours": 2}), 404, "not found")
	problem(t, e.Do("PUT", "/api/v1/messages/slas/x", map[string]any{"ackHours": 2}), 400, "invalid partnerId")
	problem(t, e.Do("PUT", "/api/v1/messages/slas/"+p.ID, "{"), 400, "invalid JSON")
	var sla struct {
		PartnerName string
		AckHours    int
		Enabled     bool
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/messages/slas/"+p.ID, map[string]any{"ackHours": 4, "enabled": false}), &sla)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/messages/slas/"+p.ID, map[string]any{"ackHours": 2}), &sla)
	if sla.AckHours != 2 || !sla.Enabled || sla.PartnerName != "Slow Partner" {
		t.Fatalf("sla: %+v", sla)
	}
	var slas struct{ Items []any }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/messages/slas?partnerId="+p.ID, nil), &slas)
	if len(slas.Items) != 1 {
		t.Fatalf("slas: %+v", slas)
	}

	// An outbound document sent 3 hours ago without acknowledgement breaches the 2 h SLA.
	var d, fresh, draft doc
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(p.ID, "PO-SLA")), &d)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(p.ID, "PO-FRESH")), &fresh)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(p.ID, "PO-DRAFT")), &draft)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+fresh.ID+"/send", nil), nil)
	opExec(t, e, `update b2b_document set sent_at = now() - interval '3 hours' where id = $1`, d.ID)
	for i := 0; i < 2; i++ { // idempotent
		if err := l.SLABreaches(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	al := alerts(t, e, "link.sla_breach", "open")
	if len(al) != 1 || al[0].Title != "Slow Partner: order PO-SLA not acknowledged within 2 h" {
		t.Fatalf("sla alerts: %+v", al)
	}
	var page logPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?q=PO-SLA", nil), &page)
	if len(page.Items) != 1 || page.Items[0].Status != "sent" || page.Items[0].Channel != "edi-pickup" {
		t.Fatalf("sent entry: %+v", page.Items)
	}

	// The functional acknowledgement resolves the breach.
	problem(t, e.Call(viewer, "POST", "/api/v1/b2b/documents/"+d.ID+"/ack", nil), 403, "missing role")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/"+draft.ID+"/ack", nil), 409, "only sent outbound")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/00000000-0000-0000-0000-000000000001/ack", nil), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/x/ack", nil), 400, "invalid id")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/ack", "{"), 400, "invalid JSON")
	var acked doc
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/ack", map[string]any{"reference": "997-1"}), &acked)
	if a := alerts(t, e, "link.sla_breach", "open"); len(a) != 0 {
		t.Fatalf("breach not resolved: %+v", a)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/messages?status=acknowledged", nil), &page)
	if len(page.Items) != 1 || page.Items[0].ID != d.ID {
		t.Fatalf("acknowledged: %+v", page.Items)
	}

	// Federation messages count too: a ping not delivered within the SLA is a breach.
	pub, _, _ := ed25519.GenerateKey(nil)
	var peer struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "Remote", "url": "http://127.0.0.1:1", "remoteTenant": "acme",
		"did": "did:web:remote.test", "publicKey": base64.RawURLEncoding.EncodeToString(pub)}), &peer)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/federation/peers/"+peer.ID+"/approve", nil), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+p.ID, map[string]any{"name": "Slow Partner", "endpoints": map[string]string{"federation": peer.ID}}), nil)
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": peer.ID, "type": "ping"}), nil)
	opExec(t, e, `update federation_message set created_at = now() - interval '5 hours'`)
	if err := l.SLABreaches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := alerts(t, e, "link.sla_breach", "open"); len(a) != 1 {
		t.Fatalf("federation breach: %+v", a)
	}

	// Deleting the SLA.
	if r := e.Do("DELETE", "/api/v1/messages/slas/"+p.ID, nil); r.Status != http.StatusNoContent {
		t.Fatalf("delete sla: %d", r.Status)
	}
	problem(t, e.Do("DELETE", "/api/v1/messages/slas/"+p.ID, nil), 404, "not found")
	problem(t, e.Do("DELETE", "/api/v1/messages/slas/x", nil), 400, "invalid partnerId")
	for _, a := range []string{"partner.sla.set", "partner.sla.delete", "b2b.ack"} {
		var aud struct{ Items []any }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action="+a, nil), &aud)
		if len(aud.Items) == 0 {
			t.Errorf("no audit entry %s", a)
		}
	}
}
