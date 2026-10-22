package link_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

func linkOf(e *testkit.Env) *link.Module {
	for _, m := range e.App.Modules {
		if l, ok := m.(*link.Module); ok {
			l.Cfg.PublicURL = e.Server.URL
			return l
		}
	}
	e.T.Fatal("link module missing")
	return nil
}

func peer(t *testing.T, from, to *testkit.Env) string {
	var p struct{ ID, DID, Status string }
	from.Must(http.StatusCreated, from.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "peer", "url": to.Server.URL, "remoteTenant": "acme"}), &p)
	if !strings.HasPrefix(p.DID, "did:web:127.0.0.1%3A") || p.Status != "pending" {
		t.Fatalf("peer: %+v", p)
	}
	from.Must(http.StatusOK, from.Do("POST", "/api/v1/federation/peers/"+p.ID+"/approve", nil), nil)
	return p.ID
}

func TestFederationTwoNodes(t *testing.T) {
	a, b := testkit.New(t), testkit.New(t)
	la, lb := linkOf(a), linkOf(b)
	_, _ = la, lb
	var did map[string]any
	a.Must(http.StatusOK, a.Call("", "GET", "/.well-known/did.json", nil), &did)
	if did["id"] == nil {
		t.Fatal("no DID document")
	}
	aToB := peer(t, a, b)
	_ = peer(t, b, a)

	// Signed EPCIS document from node A lands in node B's repository.
	ev := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", EventTime: time.Now().UTC().Add(-time.Minute), EventTimeZoneOffset: "+00:00",
		EPCList: []string{"urn:epc:id:sscc:9506000.0000000017"}}
	doc, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{ev}))
	var sent struct{ MessageID string }
	a.Must(http.StatusAccepted, a.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": aToB, "type": "epcis.capture", "payload": json.RawMessage(doc)}), &sent)
	a.Drain()
	b.Drain()
	var st struct{ Events int }
	b.Must(http.StatusOK, b.Do("GET", "/api/v1/trace/stats", nil), &st)
	if st.Events != 1 {
		r := a.Do("GET", "/api/v1/federation/messages", nil)
		t.Fatalf("node B events: %d; messages %s", st.Events, r.Body)
	}
	var msgs struct {
		Items []struct{ Status, Direction string }
	}
	a.Must(http.StatusOK, a.Do("GET", "/api/v1/federation/messages", nil), &msgs)
	if len(msgs.Items) != 1 || msgs.Items[0].Status != "delivered" {
		t.Fatalf("outbound status: %+v", msgs.Items)
	}

	// Replays and tampered messages are refused.
	n, err := la.Node(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var bdid map[string]any
	b.Must(http.StatusOK, b.Call("", "GET", "/.well-known/did.json", nil), &bdid)
	env := n.Sign(link.Envelope{ID: "fixed-1", Type: "ping", From: n.DID, FromTenant: "acme", To: bdid["id"].(string), ToTenant: "acme", CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	post := func(e link.Envelope) int {
		buf, _ := json.Marshal(e)
		res, err := http.Post(b.Server.URL+"/api/v1/federation/inbox", "application/json", bytes.NewReader(buf))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if s := post(env); s != http.StatusAccepted {
		t.Fatalf("ping: %d", s)
	}
	if s := post(env); s != http.StatusConflict {
		t.Fatalf("replay: %d", s)
	}
	tampered := env
	tampered.ID, tampered.Payload = "fixed-2", json.RawMessage(`{"evil":true}`)
	if s := post(tampered); s != http.StatusUnauthorized {
		t.Fatalf("tampered: %d", s)
	}
	stale := n.Sign(link.Envelope{ID: "fixed-3", Type: "ping", From: n.DID, FromTenant: "acme", To: env.To, ToTenant: "acme", CreatedAt: time.Now().Add(-time.Hour), Payload: json.RawMessage(`{}`)})
	if s := post(stale); s != http.StatusBadRequest {
		t.Fatalf("stale: %d", s)
	}
	b.NewTenant("globex", "admin@globex.test")
	stranger := n.Sign(link.Envelope{ID: "fixed-4", Type: "ping", From: n.DID, FromTenant: "acme", To: env.To, ToTenant: "globex", CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	if s := post(stranger); s != http.StatusForbidden {
		t.Fatalf("unapproved sender: %d", s)
	}

	// B2B order sent over federation; EDI renderings.
	var p struct{ ID string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/partners", map[string]any{"name": "Node B Pharma", "kind": "wholesaler", "endpoints": map[string]string{"federation": aToB}}), &p)
	order := map[string]any{"type": "order", "partnerId": p.ID, "payload": map[string]any{"number": "PO-9", "buyer": map[string]string{"name": "A", "gln": "9506000000008"},
		"seller": map[string]string{"name": "B", "gln": "0614141000005"}, "lines": []map[string]any{{"gtin": "09506000134352", "quantity": 10, "unitPrice": 2.5}}}}
	var d struct{ ID string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/b2b/documents", order), &d)
	for _, f := range []string{"x12", "edifact", "idoc"} {
		r := a.Do("GET", "/api/v1/b2b/documents/"+d.ID+"/edi?format="+f, nil)
		if body := string(r.Body); r.Status != 200 || !strings.Contains(body, "PO1*1*10*EA*2.5") && !strings.Contains(body, "QTY+21:10:EA") && !strings.Contains(body, "<MENGE>10</MENGE>") {
			t.Fatalf("%s rendering: %d %s", f, r.Status, r.Body)
		}
	}
	a.Must(http.StatusOK, a.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), nil)
	a.Drain()
	var inb struct {
		Items []struct{ Number, Direction string }
	}
	b.Must(http.StatusOK, b.Do("GET", "/api/v1/b2b/documents?direction=inbound", nil), &inb)
	if len(inb.Items) != 1 || inb.Items[0].Number != "PO-9" {
		t.Fatalf("inbound b2b on node B: %+v", inb.Items)
	}

	// Inbound X12 import.
	x12 := a.Do("GET", "/api/v1/b2b/documents/"+d.ID+"/edi?format=x12", nil)
	var imp struct{ Number, Direction string }
	b.Must(http.StatusCreated, b.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "content": string(x12.Body)}), &imp)
	if imp.Number != "PO-9" || imp.Direction != "inbound" {
		t.Fatalf("import: %+v", imp)
	}

	// Supplier certificate expiring soon raises an alert.
	soon := time.Now().AddDate(0, 0, 10).Format("2006-01-02")
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"partnerId": p.ID, "kind": "GDP", "number": "GDP-1", "issuer": "IGJ", "validTo": soon}), nil)
	if err := la.ExpiryAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := la.ExpiryAlerts(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
	var al struct{ Items []any }
	a.Must(http.StatusOK, a.Do("GET", "/api/v1/alerts?kind=supplier.", nil), &al)
	if len(al.Items) != 1 {
		t.Fatalf("expiry alerts: %d", len(al.Items))
	}

	// Document exchange: upload and download with integrity hash.
	var f struct{ ID, SHA256 string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/documents", map[string]any{"title": "CoA batch M1.pdf", "contentType": "application/pdf", "content": base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 test"))}), &f)
	got := a.Do("GET", "/api/v1/documents/"+f.ID, nil)
	if string(got.Body) != "%PDF-1.7 test" || len(f.SHA256) != 64 {
		t.Fatalf("download: %q", got.Body)
	}
}
