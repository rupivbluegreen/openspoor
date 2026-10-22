package dscsa_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

const gtin = "09506000134352"

func TestDSCSAManufacturerToWholesaler(t *testing.T) {
	e := testkit.New(t) // tenant "acme" = manufacturer
	_, whTok := e.NewTenant("globex", "admin@globex.test")

	// Identities in the directory.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme Pharma", "gln": "9506000000008", "kind": "manufacturer", "country": "US", "published": true}), nil)
	e.Must(http.StatusOK, e.Call(whTok, "PUT", "/api/v1/partners/listing", map[string]any{"name": "Globex Wholesale", "gln": "0614141000005", "kind": "wholesaler", "country": "US", "published": true}), nil)
	var dir struct{ Items []map[string]any }
	e.Must(http.StatusOK, e.Call(whTok, "GET", "/api/v1/partners/directory?q=acme", nil), &dir)
	if len(dir.Items) != 1 {
		t.Fatalf("directory: %v", dir.Items)
	}

	// Invitation → accept → approve.
	var inv struct{ Code string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners/invitations", map[string]any{"email": "admin@globex.test", "name": "Globex"}), &inv)
	e.Must(http.StatusCreated, e.Call(whTok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": inv.Code}), nil)
	e.Must(http.StatusConflict, e.Call(whTok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": inv.Code}), nil)
	var ps struct {
		Items []struct {
			ID, Status, Name string
			GLN              *string
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners", nil), &ps)
	if len(ps.Items) != 1 || ps.Items[0].Status != "pending" || ps.Items[0].GLN == nil {
		t.Fatalf("inviter view: %+v", ps.Items)
	}
	pid := ps.Items[0].ID
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+pid+"/approve", nil), nil)

	// Manufacturer commissions 4 packs and packs them in a case.
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Metformin 850mg", "ndc": "50090-1234-01", "dosageForm": "TABLET", "strength": "850 mg"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "US"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 5}, "Idempotency-Key", "x"), &a)
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var epcs []string
	for _, s := range a.Serials[:4] {
		u, _ := gs1.SGTINURN(gtin, s, 7)
		epcs = append(epcs, u)
	}
	sscc := "urn:epc:id:sscc:9506000.0000000017"
	comm := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: epcs,
		ILMD: map[string]any{"lotNumber": "M1", "itemExpirationDate": "2029-06-30"}}
	pack := lib.Event{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", EventTime: t0.Add(time.Minute), EventTimeZoneOffset: "+00:00", ParentID: sscc, ChildEPCs: epcs}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{comm, pack}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()

	// Outbound shipment with ASN; TI/TS built and delivered to the partner tenant on this node.
	var sh struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pid, "poNumber": "PO-1001", "asnNumber": "ASN-1", "epcs": []string{sscc},
		"asn": map[string]any{"items": []map[string]any{{"gtin": gtin, "lot": "M1", "quantity": 4}}}}), &sh)
	tits := e.Do("POST", "/api/v1/shipments/"+sh.ID+"/tits", nil)
	e.Must(http.StatusOK, tits, nil)
	doc, _, err := lib.Parse("application/ld+json", tits.Body)
	if err != nil || len(doc.Body.EventList) != 2 || len(doc.Header) == 0 {
		t.Fatalf("tits: %v events=%d header=%s", err, len(doc.Body.EventList), doc.Header)
	}
	var sent struct {
		Status   string
		Response map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+sh.ID+"/send", nil), &sent)
	if sent.Status != "sent" || sent.Response["delivered"] != "local" {
		t.Fatalf("send: %+v", sent)
	}
	e.Drain()

	// Wholesaler sees the inbound shipment, reconciled against the ASN quantity.
	var in struct {
		Items []struct {
			ID, Status, PONumber string
			Reconciliation      struct{ Matched bool }
		}
	}
	e.Must(http.StatusOK, e.Call(whTok, "GET", "/api/v1/shipments?direction=inbound", nil), &in)
	if len(in.Items) != 1 || in.Items[0].PONumber != "PO-1001" {
		t.Fatalf("inbound: %+v", in.Items)
	}
	var full struct {
		Reconciliation struct {
			Matched    bool
			Quantities []struct{ Expected, Received int }
		}
	}
	e.Must(http.StatusOK, e.Call(whTok, "GET", "/api/v1/shipments/"+in.Items[0].ID, nil), &full)
	if !full.Reconciliation.Matched || full.Reconciliation.Quantities[0].Received != 4 {
		t.Fatalf("reconciliation: %+v", full.Reconciliation)
	}
	// Wholesaler's repository knows the case and its children.
	var tree struct{ Children []any }
	e.Must(http.StatusOK, e.Call(whTok, "GET", "/api/v1/trace/tree/?id="+sscc, nil), &tree)
	if len(tree.Children) != 4 {
		t.Fatalf("wholesaler tree: %d children", len(tree.Children))
	}
	// Mismatch with a different ASN raises an exception.
	var rec struct{ Matched bool }
	e.Must(http.StatusOK, e.Call(whTok, "POST", "/api/v1/shipments/"+in.Items[0].ID+"/reconcile",
		map[string]any{"asn": map[string]any{"items": []map[string]any{{"gtin": gtin, "quantity": 5}}}}), &rec)
	if rec.Matched {
		t.Fatal("quantity mismatch not detected")
	}

	// VRS: wholesaler verifies a saleable return against the manufacturer via the directory.
	e.Must(http.StatusOK, e.Call(whTok, "POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506000", "responderUrl": "local:" + e.Tenant.ID.String()}), nil)
	var v struct {
		Verified bool
		Reason   string
	}
	e.Must(http.StatusOK, e.Call(whTok, "POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": a.Serials[0], "lot": "M1", "expiry": "290630"}), &v)
	if !v.Verified {
		t.Fatalf("vrs should verify: %+v", v)
	}
	e.Must(http.StatusOK, e.Call(whTok, "POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": a.Serials[4]}), &v)
	if v.Verified || v.Reason != "No_match_GTIN_Serial" {
		t.Fatalf("uncommissioned serial: %+v", v)
	}
	e.Must(http.StatusOK, e.Call(whTok, "POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": a.Serials[0], "lot": "WRONG"}), &v)
	if v.Reason != "No_match_GTIN_Serial_Lot" {
		t.Fatalf("lot mismatch: %+v", v)
	}

	// VRS responder over HTTP with an API key.
	var key struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "vrs-router", "roles": []string{"Integration"}}), &key)
	var resp struct {
		CorrUUID string
		Data     struct{ Verified bool }
	}
	e.Must(http.StatusOK, e.Call(key.Secret, "POST", "/api/v1/vrs/verify", map[string]any{"gtin": gtin, "serialNumber": a.Serials[1], "requestorGLN": "0614141000005", "context": "dscsaSaleableReturn"}), &resp)
	if !resp.Data.Verified || resp.CorrUUID == "" {
		t.Fatalf("http vrs: %+v", resp)
	}
	e.Must(http.StatusUnauthorized, e.Call("", "POST", "/api/v1/vrs/verify", map[string]any{"gtin": gtin, "serialNumber": "x"}), nil)

	// Retention cannot go below six years.
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/retention/epcis_events", map[string]any{"years": 2}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/retention/epcis_events", map[string]any{"years": 10, "basis": "company policy"}), nil)
}
