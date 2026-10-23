package dscsa_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/dscsa"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// userToken creates a user with the given roles in tenant "acme" and logs in.
func userToken(t *testing.T, e *testkit.Env, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

// commissionPacks registers a product, allocates n serials and returns their SGTIN URNs.
func commissionPacks(t *testing.T, e *testkit.Env, n int) ([]string, []string) {
	t.Helper()
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
	return a.Serials, epcs
}

func capture(t *testing.T, e *testkit.Env, evs ...lib.Event) {
	t.Helper()
	b, _ := lib.WriteJSON(lib.NewDocument(evs))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()
}

func commission(epcs []string, lot, exp, disposition string, at time.Time) lib.Event {
	return lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: disposition, EventTime: at, EventTimeZoneOffset: "+00:00",
		EPCList: epcs, ILMD: map[string]any{"lotNumber": lot, "itemExpirationDate": exp}}
}

func createPartner(t *testing.T, e *testkit.Env, body map[string]any) string {
	t.Helper()
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", body), &p)
	return p.ID
}

type alertList struct {
	Items []struct {
		Kind, Title, EntityID string
		Detail                map[string]any
	}
}

func auditActions(t *testing.T, e *testkit.Env, prefix string) []string {
	t.Helper()
	var aud struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=500&action="+prefix, nil), &aud)
	var out []string
	for _, a := range aud.Items {
		out = append(out, a.Action)
	}
	return out
}

func TestShipmentValidationRolesAndTransports(t *testing.T) {
	e := testkit.New(t)
	viewer := userToken(t, e, "viewer@acme.test", "Viewer")
	serial := userToken(t, e, "ser@acme.test", "Serialization")

	// Role checks: Viewer reads, cannot write; a role outside the list is refused entirely.
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/shipments", nil), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/shipments", map[string]any{"poNumber": "x", "epcs": []string{"x"}}), nil)
	e.Must(http.StatusForbidden, e.Call(serial, "GET", "/api/v1/shipments", nil), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", "/api/v1/retention/epcis_events", map[string]any{"years": 10}), nil)
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/retention", nil), nil)
	e.Must(http.StatusUnauthorized, e.Call("", "GET", "/api/v1/shipments", nil), nil)

	_, epcs := commissionPacks(t, e, 4)
	capture(t, e, commission(epcs[:3], "L1", "2030-01-31", "active", time.Now().UTC().Add(-time.Hour)))

	// Partners with different endpoints.
	var mu sync.Mutex
	var got []*http.Request
	var gotBody []byte
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, gotBody = append(got, r), b
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer badSrv.Close()
	pOK := createPartner(t, e, map[string]any{"name": "Good Wholesale", "gln": "0614141000005", "endpoints": map[string]string{"epcisCapture": okSrv.URL + "/capture"}})
	pBad := createPartner(t, e, map[string]any{"name": "Broken Wholesale", "endpoints": map[string]string{"epcisCapture": badSrv.URL}})
	pNone := createPartner(t, e, map[string]any{"name": "No Endpoint"})
	pSusp := createPartner(t, e, map[string]any{"name": "Suspended", "endpoints": map[string]string{"epcisCapture": okSrv.URL}})
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+pSusp+"/suspend", nil), nil)

	// Validation errors are problem JSON.
	bad := e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pOK, "poNumber": "PO"})
	if bad.Status != http.StatusBadRequest || !strings.Contains(bad.Header.Get("Content-Type"), "problem+json") {
		t.Fatalf("missing epcs: %d %s %s", bad.Status, bad.Header.Get("Content-Type"), bad.Body)
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pOK, "epcs": epcs[:1]}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pOK, "poNumber": "PO", "epcs": epcs[:1], "transport": "ftp"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pOK, "poNumber": "PO", "epcs": epcs[:1], "shipDate": "01/02/2026"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": "00000000-0000-0000-0000-000000000001", "poNumber": "PO", "epcs": epcs[:1]}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shipments", "{not json"), nil)

	mk := func(partner, po, transport string, ep []string) string {
		t.Helper()
		var s struct{ ID, Status, Transport string }
		body := map[string]any{"partnerId": partner, "poNumber": po, "asnNumber": "ASN-" + po, "epcs": ep, "shipDate": "2026-05-01"}
		if transport != "" {
			body["transport"] = transport
		}
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", body), &s)
		if s.Status != "draft" || (transport == "" && s.Transport != "https") {
			t.Fatalf("new shipment: %+v", s)
		}
		return s.ID
	}

	// HTTPS delivery to a partner endpoint succeeds and records the outbound events.
	okID := mk(pOK, "PO-OK", "", epcs[:2])
	var sent struct {
		Status   string
		Response map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+okID+"/send", nil), &sent)
	if sent.Status != "sent" || sent.Response["status"] != float64(200) {
		t.Fatalf("https send: %+v", sent)
	}
	mu.Lock()
	if len(got) != 1 || got[0].Header.Get("X-OpenSpoor-Shipment") != okID || got[0].Header.Get("Content-Type") != "application/ld+json" {
		t.Fatalf("partner request: %+v", got)
	}
	doc, _, err := lib.Parse("application/ld+json", gotBody)
	mu.Unlock()
	if err != nil || len(doc.Body.EventList) != 1 {
		t.Fatalf("delivered doc: %v", err)
	}
	ship := doc.Body.EventList[0]
	if len(ship.BizTransactionList) != 2 || ship.DestinationList == nil || len(ship.EPCList) != 2 {
		t.Fatalf("shipping event: %+v", ship)
	}
	var hdr map[string]map[string]any
	_ = json.Unmarshal(doc.Header, &hdr)
	if ti := hdr["dscsa:transactionInformation"]; ti["shipmentDate"] != "2026-05-01" || ti["poNumber"] != "PO-OK" {
		t.Fatalf("transaction information: %+v", ti)
	}
	if ts := hdr["dscsa:transactionStatement"]; ts["affirmTransactionStatement"] != true {
		t.Fatalf("transaction statement: %+v", ts)
	}
	var full struct {
		Status string
		SentAt *time.Time
		TITS   json.RawMessage
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/shipments/"+okID, nil), &full)
	if full.Status != "sent" || full.SentAt == nil || len(full.TITS) == 0 {
		t.Fatalf("stored shipment: %+v", full)
	}
	// Viewer can read a single shipment.
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/shipments/"+okID, nil), nil)

	// Failing transports: partner 5xx, no endpoint, AS2 not configured. Each raises an alert.
	for _, c := range []struct{ partner, po, transport, wantErr string }{
		{pBad, "PO-BAD", "", "502"},
		{pNone, "PO-NONE", "", "no EPCIS capture endpoint"},
		{pOK, "PO-AS2", "as2", "partner has no AS2 URL and AS2 identifier"},
	} {
		id := mk(c.partner, c.po, c.transport, epcs[2:3])
		e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+id+"/send", nil), &sent)
		if sent.Status != "send_failed" || !strings.Contains(sent.Response["error"].(string), c.wantErr) {
			t.Fatalf("%s: %+v", c.po, sent)
		}
	}
	var al alertList
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=dscsa.send_failed", nil), &al)
	if len(al.Items) != 3 {
		t.Fatalf("send_failed alerts: %+v", al.Items)
	}

	// A suspended partner cannot receive.
	sid := mk(pSusp, "PO-SUSP", "", epcs[2:3])
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/shipments/"+sid+"/send", nil), nil)

	// Unknown identifiers and non-shippable product cannot be built into a TI/TS.
	unk := mk(pOK, "PO-UNK", "", epcs[3:4])
	r := e.Do("POST", "/api/v1/shipments/"+unk+"/tits", nil)
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "unknown identifier") {
		t.Fatalf("unknown epc: %d %s", r.Status, r.Body)
	}
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/shipments/"+unk+"/send", nil), nil)
	capture(t, e, lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "destroying", Disposition: "destroyed", EventTime: time.Now().UTC().Add(-time.Minute), EventTimeZoneOffset: "+00:00", EPCList: epcs[2:3]})
	dst := mk(pOK, "PO-DST", "", epcs[2:3])
	r = e.Do("POST", "/api/v1/shipments/"+dst+"/tits", nil)
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "cannot ship destroyed") {
		t.Fatalf("destroyed: %d %s", r.Status, r.Body)
	}

	// Reconcile without a TI/TS is a conflict; bad / unknown ids.
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/shipments/"+dst+"/reconcile", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/shipments/not-a-uuid", nil), nil)
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/shipments/00000000-0000-0000-0000-000000000001", nil), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/shipments/00000000-0000-0000-0000-000000000001/tits", nil), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/shipments/00000000-0000-0000-0000-000000000001/send", nil), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/shipments/00000000-0000-0000-0000-000000000001/reconcile", nil), nil)

	// Re-reconcile the sent shipment against its own ASN (no items → matched).
	var rec dscsa.Reconciliation
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+okID+"/reconcile", nil), &rec)
	if !rec.Matched {
		t.Fatalf("reconcile: %+v", rec)
	}

	var list struct {
		Items []struct {
			ID   string
			TITS json.RawMessage
		}
		Total int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/shipments?direction=outbound", nil), &list)
	if list.Total != 7 {
		t.Fatalf("outbound list: %d", list.Total)
	}
	for _, it := range list.Items {
		if len(it.TITS) != 0 {
			t.Fatal("list must not carry TI/TS documents")
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/shipments?direction=inbound", nil), &list)
	if list.Total != 0 {
		t.Fatalf("inbound list: %d", list.Total)
	}

	acts := strings.Join(auditActions(t, e, "shipment."), ",")
	for _, want := range []string{"shipment.create", "shipment.send", "shipment.reconcile"} {
		if !strings.Contains(acts, want) {
			t.Fatalf("audit missing %s: %s", want, acts)
		}
	}
}

func TestInboundTITS(t *testing.T) {
	e := testkit.New(t)
	sgtin := func(s string) string { u, _ := gs1.SGTINURN(gtin, s, 7); return u }
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	docOf := func(epcs ...string) json.RawMessage {
		ev := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", Disposition: "in_transit", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: epcs}
		b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{ev}))
		return b
	}

	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shipments/inbound", map[string]any{"poNumber": "PO"}), nil)
	r := e.Do("POST", "/api/v1/shipments/inbound", map[string]any{"poNumber": "PO", "document": map[string]any{"type": "EPCISDocument", "epcisBody": map[string]any{"eventList": []map[string]any{{"type": "ObjectEvent"}}}}})
	if r.Status < 400 || r.Status >= 500 {
		t.Fatalf("invalid document: %d %s", r.Status, r.Body)
	}

	pid := createPartner(t, e, map[string]any{"name": "Supplier"})
	// Matches an ASN that lists the EPCs explicitly.
	var s struct {
		ID, Status     string
		PartnerID      *string
		EPCs           []string
		Reconciliation dscsa.Reconciliation
	}
	asn := map[string]any{"number": "ASN-9", "items": []map[string]any{{"gtin": gtin, "quantity": 2, "epcs": []string{sgtin("A1"), sgtin("A2")}}}}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments/inbound", map[string]any{"poNumber": "PO-9", "partnerId": pid, "asn": asn, "document": docOf(sgtin("A1"), sgtin("A2"))}), &s)
	if s.Status != "received" || !s.Reconciliation.Matched || s.PartnerID == nil || *s.PartnerID != pid || len(s.EPCs) != 2 {
		t.Fatalf("matched inbound: %+v", s)
	}
	// A missing and an unexpected pack raise an exception and an alert.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments/inbound", map[string]any{"poNumber": "PO-10", "asn": asn, "document": docOf(sgtin("A1"), sgtin("B9"))}), &s)
	if s.Status != "exception" || s.Reconciliation.Matched || len(s.Reconciliation.Missing) != 1 || len(s.Reconciliation.Unexpected) != 1 {
		t.Fatalf("mismatched inbound: %+v", s)
	}
	var al alertList
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=dscsa.reconciliation", nil), &al)
	if len(al.Items) != 1 || al.Items[0].EntityID != s.ID || !strings.Contains(al.Items[0].Title, "PO-10") {
		t.Fatalf("reconciliation alert: %+v", al.Items)
	}
	// Re-reconciling against a corrected ASN clears the mismatch.
	var rec dscsa.Reconciliation
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+s.ID+"/reconcile", map[string]any{"asn": map[string]any{"items": []map[string]any{{"gtin": gtin, "quantity": 2}}}}), &rec)
	if !rec.Matched {
		t.Fatalf("corrected asn: %+v", rec)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/shipments/"+s.ID, nil), &s)
	if s.Status != "reconciled" {
		t.Fatalf("status after reconcile: %s", s.Status)
	}
	// XML passed as a JSON string is accepted.
	ev := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: []string{sgtin("X1")}}
	xml, err := lib.WriteXML(lib.NewDocument([]lib.Event{ev}))
	if err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments/inbound", map[string]any{"poNumber": "PO-XML", "document": string(xml)}), &s)
	if len(s.EPCs) != 1 || s.EPCs[0] != lib.Canonical(sgtin("X1")) {
		t.Fatalf("xml inbound: %+v", s)
	}
	// The stored TI/TS can be reconciled later (it is kept as JSON-LD).
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+s.ID+"/reconcile", map[string]any{"asn": map[string]any{"items": []map[string]any{{"gtin": gtin, "quantity": 1}}}}), &rec)
	if !rec.Matched {
		t.Fatalf("xml reconcile: %+v", rec)
	}
}

func TestReconcileDoc(t *testing.T) {
	sg := func(s string) string { u, _ := gs1.SGTINURN(gtin, s, 7); return u }
	doc := lib.NewDocument([]lib.Event{
		{Type: lib.AggregationEvent, ParentID: "urn:epc:id:sscc:9506000.0000000017", ChildEPCs: []string{sg("1"), sg("2")}},
		{Type: lib.ObjectEvent, EPCList: []string{"urn:epc:id:sscc:9506000.0000000017"}},
	})
	cases := []struct {
		name    string
		asn     dscsa.ASN
		matched bool
		missing int
		unexp   int
	}{
		{"empty asn", dscsa.ASN{}, true, 0, 0},
		{"quantity only", dscsa.ASN{Items: []dscsa.ASNItem{{GTIN: "9506000134352", Quantity: 2}}}, true, 0, 0},
		{"quantity mismatch", dscsa.ASN{Items: []dscsa.ASNItem{{GTIN: gtin, Quantity: 3}}}, false, 0, 0},
		{"listed exact", dscsa.ASN{Items: []dscsa.ASNItem{{GTIN: gtin, Quantity: 2, EPCs: []string{sg("1"), sg("2")}}}}, true, 0, 0},
		{"listed partial", dscsa.ASN{Items: []dscsa.ASNItem{{GTIN: gtin, Quantity: 2, EPCs: []string{sg("1"), sg("3")}}}}, false, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := dscsa.ReconcileDoc(doc, c.asn)
			if r.Matched != c.matched || len(r.Missing) != c.missing || len(r.Unexpected) != c.unexp {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestVRSResponderOutcomesAndDirectory(t *testing.T) {
	e := testkit.New(t)
	viewer := userToken(t, e, "viewer@acme.test", "Viewer")
	serials, epcs := commissionPacks(t, e, 6)
	past := time.Now().UTC().Add(-2 * time.Hour)
	capture(t, e,
		commission(epcs[0:1], "L1", "2030-01-31", "active", past),
		commission(epcs[1:2], "L1", "2030-01-31", "recalled", past),
		commission(epcs[2:3], "L1", "2030-01-31", "destroyed", past),
		commission(epcs[3:4], "L1", "2020-01-31", "active", past),
		commission(epcs[4:5], "L1", "2030-01-31", "active", past),
	)
	capture(t, e, lib.Event{Type: lib.ObjectEvent, Action: "DELETE", BizStep: "decommissioning", EventTime: past.Add(time.Minute), EventTimeZoneOffset: "+00:00", EPCList: epcs[4:5]})

	var key struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "router", "roles": []string{"Integration"}}), &key)
	ask := func(q map[string]any) (string, bool) {
		t.Helper()
		var resp dscsa.VRSResponse
		e.Must(http.StatusOK, e.Call(key.Secret, "POST", "/api/v1/vrs/verify", q), &resp)
		return resp.Data.VerificationFailureReason, resp.Data.Verified
	}
	for _, c := range []struct {
		q      map[string]any
		reason string
	}{
		{map[string]any{"gtin": gtin, "serialNumber": serials[0], "lotNumber": "L1", "expirationDate": "300131"}, ""},
		{map[string]any{"gtin": "09506000134369", "serialNumber": serials[0]}, "No_match_GTIN"},
		{map[string]any{"gtin": gtin, "serialNumber": serials[0], "expirationDate": "300130"}, "No_match_GTIN_Serial_Lot_Expiry"},
		{map[string]any{"gtin": gtin, "serialNumber": serials[0], "expirationDate": "garbage"}, "No_match_GTIN_Serial_Lot_Expiry"},
		{map[string]any{"gtin": gtin, "serialNumber": serials[1]}, "Recalled"},
		{map[string]any{"gtin": gtin, "serialNumber": serials[2]}, "Not_saleable"},
		{map[string]any{"gtin": gtin, "serialNumber": serials[3]}, "Expired"},
		{map[string]any{"gtin": gtin, "serialNumber": serials[4]}, "Not_saleable"},
		{map[string]any{"gtin": gtin, "serialNumber": serials[5]}, "No_match_GTIN_Serial"},
	} {
		reason, ok := ask(c.q)
		if reason != c.reason || ok != (c.reason == "") {
			t.Fatalf("%v: got %q verified=%v, want %q", c.q, reason, ok, c.reason)
		}
	}
	e.Must(http.StatusBadRequest, e.Call(key.Secret, "POST", "/api/v1/vrs/verify", map[string]any{"gtin": gtin}), nil)
	// A caller-supplied correlation id is echoed.
	var resp dscsa.VRSResponse
	corr := "6f1c2a4e-7b9d-4c3e-8a2f-1d5e6b7c8d9e"
	e.Must(http.StatusOK, e.Call(key.Secret, "POST", "/api/v1/vrs/verify", map[string]any{"gtin": gtin, "serialNumber": serials[0], "corrUUID": corr}), &resp)
	if resp.CorrUUID != corr {
		t.Fatalf("corrUUID: %+v", resp)
	}

	// Inbound requests are logged; Viewer may list.
	var rows struct {
		Items []struct{ Direction, Reason, Responder string }
		Total int
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/vrs/requests?direction=inbound", nil), &rows)
	if rows.Total != 10 || rows.Items[0].Responder != "self" {
		t.Fatalf("inbound vrs log: %d %+v", rows.Total, rows.Items)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506", "responderUrl": "https://x"}), nil)

	// Directory validation.
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "95", "responderUrl": "https://x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "95AB06", "responderUrl": "https://x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506", "responderUrl": "ftp://x"}), nil)

	// Requests without a directory entry → 404; invalid GTIN → 400.
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": "S"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": "09506000134353", "serial": "S"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin}), nil)

	// Remote responder over HTTP with a sealed API key; the longest prefix wins.
	var hits []string
	var hmu sync.Mutex
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q dscsa.VRSRequest
		_ = json.NewDecoder(r.Body).Decode(&q)
		hmu.Lock()
		hits = append(hits, r.Header.Get("X-API-Key"))
		hmu.Unlock()
		if q.SerialNumber == "BOOM" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(dscsa.VRSResponse{CorrUUID: q.CorrUUID, Data: dscsa.VRSData{Verified: q.Context == "dscsaSaleableReturn"}})
	}))
	defer remote.Close()
	var short, long struct{ ID string }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506", "responderUrl": "http://127.0.0.1:1/unused"}), &short)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506000", "responderUrl": remote.URL, "apiKey": "remote-key"}), &long)
	var v struct {
		Verified  bool
		Reason    string
		Responder string
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": "S1"}), &v)
	if !v.Verified || v.Responder != remote.URL {
		t.Fatalf("remote verify: %+v", v)
	}
	hmu.Lock()
	if len(hits) != 1 || hits[0] != "remote-key" {
		t.Fatalf("api key not sent: %v", hits)
	}
	hmu.Unlock()
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": "BOOM"}), &v)
	if v.Verified || !strings.Contains(v.Reason, "responder_error") || !strings.Contains(v.Reason, "503") {
		t.Fatalf("remote failure: %+v", v)
	}
	// Updating the entry without a key keeps the stored key.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506000", "responderUrl": remote.URL}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": "S2", "context": "other"}), &v)
	hmu.Lock()
	if v.Verified || hits[len(hits)-1] != "remote-key" {
		t.Fatalf("after update: %+v %v", v, hits)
	}
	hmu.Unlock()

	var dir struct {
		Items []struct{ ID, GTINPrefix string }
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/vrs/directory", nil), &dir)
	if len(dir.Items) != 2 || dir.Items[0].GTINPrefix != "9506" {
		t.Fatalf("directory: %+v", dir.Items)
	}
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/vrs/directory/"+long.ID, nil), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/vrs/directory/"+long.ID, nil), nil)
	e.Must(http.StatusBadRequest, e.Do("DELETE", "/api/v1/vrs/directory/xyz", nil), nil)
	// Now the short prefix routes to an unreachable responder.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": "S3"}), &v)
	if v.Verified || !strings.HasPrefix(v.Reason, "responder_error") {
		t.Fatalf("unreachable: %+v", v)
	}
	// A malformed local responder is a client error.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506", "responderUrl": "local:not-a-uuid"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": "S4"}), nil)

	var out struct{ Total int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/vrs/requests?direction=outbound", nil), &out)
	if out.Total != 4 {
		t.Fatalf("outbound vrs log: %d", out.Total)
	}
	acts := strings.Join(auditActions(t, e, "vrs."), ",")
	for _, want := range []string{"vrs.request", "vrs.directory.put", "vrs.directory.delete"} {
		if !strings.Contains(acts, want) {
			t.Fatalf("audit missing %s: %s", want, acts)
		}
	}
}

func TestRetentionPolicyAndEnforcement(t *testing.T) {
	e := testkit.New(t)
	var rs struct {
		Items []struct {
			DataClass string
			Years     int
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/retention", nil), &rs)
	if len(rs.Items) != 2 || rs.Items[0].Years != dscsa.RetentionYears {
		t.Fatalf("default retention: %+v", rs.Items)
	}
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/retention/vrs_requests", map[string]any{"years": 5}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/retention/vrs_requests", map[string]any{"years": 8, "basis": "policy"}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/retention/custom_logs", map[string]any{"years": 1, "basis": "ops"}), nil)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/retention", nil), &rs)
	got := map[string]int{}
	for _, it := range rs.Items {
		got[it.DataClass] = it.Years
	}
	if len(rs.Items) != 3 || got["vrs_requests"] != 8 || got["epcis_events"] != 6 || got["custom_logs"] != 1 {
		t.Fatalf("retention after put: %+v", rs.Items)
	}
	if acts := auditActions(t, e, "retention."); len(acts) != 2 {
		t.Fatalf("retention audit: %v", acts)
	}

	// Seed VRS records of different ages directly, then run the daily job.
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.DBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	opExec := func(sql string, args ...any) {
		t.Helper()
		tx, err := c.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "select set_config('app.operator','on',true)"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, age := range []string{"1 year", "7 years", "9 years"} {
		opExec(`insert into vrs_request (id, tenant_id, direction, corr_uuid, gtin, serial, lot, expiry, requestor_gln, context, verified, reason, responder, latency_ms, response, created_at)
			values (gen_random_uuid(), $1, 'inbound', gen_random_uuid(), $2, $3, '', '', '', '', true, '', 'self', 1, '{}', now() - $4::interval)`, e.Tenant.ID, gtin, age, age)
	}
	var mod *dscsa.Module
	for _, m := range e.App.Modules {
		if d, ok := m.(*dscsa.Module); ok {
			mod = d
		}
	}
	if mod == nil || mod.Name() != "dscsa" || len(mod.Schedules()) != 3 || mod.Schedules()[0].Name != "dscsa.retention" {
		t.Fatal("dscsa module or schedule missing")
	}
	if err := mod.Schedules()[0].Run(ctx); err != nil {
		t.Fatal(err)
	}
	var serialsLeft []string
	var rows struct{ Items []struct{ Serial string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/vrs/requests", nil), &rows)
	for _, r := range rows.Items {
		serialsLeft = append(serialsLeft, r.Serial)
	}
	// Tenant keeps 8 years: the 9-year-old record goes, the 7-year-old one stays.
	if strings.Join(serialsLeft, ",") != "1 year,7 years" {
		t.Fatalf("after retention: %v", serialsLeft)
	}
}

func TestTransportsDirect(t *testing.T) {
	var as2 dscsa.AS2Transport
	if _, err := as2.Send(context.Background(), [16]byte{}, partnersPartner(""), dscsa.Shipment{}, nil); err == nil {
		t.Fatal("AS2 must refuse without a module")
	}
	tr := &dscsa.HTTPSTransport{Client: http.DefaultClient}
	if _, err := tr.Send(context.Background(), [16]byte{}, partnersPartner("http://[::1"), dscsa.Shipment{}, nil); err == nil {
		t.Fatal("malformed endpoint must fail")
	}
	if _, err := tr.Send(context.Background(), [16]byte{}, partnersPartner("http://127.0.0.1:1/"), dscsa.Shipment{}, nil); err == nil {
		t.Fatal("unreachable endpoint must fail")
	}
}

func partnersPartner(capture string) partners.Partner {
	return partners.Partner{Name: "P", Endpoints: partners.Endpoints{EPCISCapture: capture}}
}
