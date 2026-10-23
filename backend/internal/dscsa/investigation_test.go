package dscsa_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

type invResp struct {
	ID, Kind, Status, Title, Decision, RequesterType, RequesterName string
	EPCs, NotFound, Serials                                         []string
	DueAt, AnsweredAt, ClosedAt                                     *time.Time
	SignatureID, SourceAlertID                                      *string
	Held, Overdue                                                   bool
	Findings                                                        []struct{ Kind, Summary, By string }
}

type invDetail struct {
	Investigation invResp
	Timeline      []struct {
		Kind, FromStatus, ToStatus, Actor, Note string
		Data                                    map[string]any
	}
	AllowedTransitions []string
}

// setupSupply commissions 4 packs (lot L1) packed into one case and returns serials, pack EPCs and the case.
func setupSupply(t *testing.T, e *testkit.Env) ([]string, []string, string) {
	t.Helper()
	serials, epcs := commissionPacks(t, e, 6)
	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	sscc := "urn:epc:id:sscc:9506000.0000000024"
	pack := lib.Event{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", EventTime: t0.Add(time.Minute), EventTimeZoneOffset: "+00:00", ParentID: sscc, ChildEPCs: epcs[:4]}
	capture(t, e, commission(epcs[:5], "L1", "2030-01-31", "active", t0), pack)
	return serials, epcs, sscc
}

func serialStatus(t *testing.T, e *testkit.Env, epc string) string {
	t.Helper()
	var s struct{ State struct{ Status string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/?id="+epc, nil), &s)
	return s.State.Status
}

func sign(t *testing.T, e *testkit.Env, tok, entity, id string) string {
	t.Helper()
	var sig struct{ ID string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/esignatures", map[string]any{"password": testkit.Password, "meaning": "approval", "reason": "investigation outcome", "entity": entity, "entityId": id}), &sig)
	return sig.ID
}

func TestInvestigationSuspectLifecycle(t *testing.T) {
	e := testkit.New(t)
	viewer := userToken(t, e, "viewer@acme.test", "Viewer")
	ops := userToken(t, e, "ops@acme.test", "Ops")
	qa := userToken(t, e, "qa@acme.test", "Compliance")
	serials, epcs, sscc := setupSupply(t, e)
	pid := createPartner(t, e, map[string]any{"name": "Globex Wholesale", "gln": "0614141000005"})

	// Roles: Viewer reads but never writes; Ops has no access.
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/investigations", nil), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/investigations", map[string]any{"kind": "suspect", "gtin": gtin, "serials": serials[:1]}), nil)
	e.Must(http.StatusForbidden, e.Call(ops, "POST", "/api/v1/investigations", map[string]any{"kind": "suspect", "gtin": gtin, "serials": serials[:1]}), nil)
	e.Must(http.StatusForbidden, e.Call(ops, "GET", "/api/v1/investigations", nil), nil)

	// Validation.
	for _, body := range []map[string]any{
		{"kind": "recall", "gtin": gtin, "serials": serials[:1]},
		{"kind": "suspect"},
		{"kind": "suspect", "serials": serials[:1]},
		{"kind": "suspect", "gtin": "09506000134353", "epcs": epcs[:1]},
		{"kind": "suspect", "epcs": epcs[:1], "requester": map[string]any{"type": "neighbour"}},
		{"kind": "suspect", "shipmentId": "00000000-0000-0000-0000-000000000001"},
		{"kind": "tracing_request", "epcs": epcs[:1]},
		{"kind": "tracing_request", "epcs": epcs[:1], "requester": map[string]any{"type": "partner"}},
		{"kind": "tracing_request", "epcs": epcs[:1], "requester": map[string]any{"type": "authority"}},
		{"kind": "tracing_request", "epcs": epcs[:1], "requester": map[string]any{"type": "partner", "partnerId": "00000000-0000-0000-0000-000000000001"}},
	} {
		e.Must(http.StatusBadRequest, e.Call(qa, "POST", "/api/v1/investigations", body), nil)
	}
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", "/api/v1/investigations", "{"), nil)

	// Open: two known packs plus one never commissioned.
	var inv invResp
	e.Must(http.StatusCreated, e.Call(qa, "POST", "/api/v1/investigations", map[string]any{"kind": "suspect", "gtin": gtin, "serials": []string{serials[0], serials[1], "NOPE1", serials[0]}, "note": "pharmacy report"}), &inv)
	if inv.Status != "open" || len(inv.EPCs) != 2 || len(inv.NotFound) != 1 || inv.DueAt != nil || inv.Title == "" || len(inv.Serials) != 3 {
		t.Fatalf("created: %+v", inv)
	}
	id := inv.ID
	base := "/api/v1/investigations/" + id

	// Invalid transitions are 409, unknown investigation 404.
	e.Must(http.StatusConflict, e.Call(qa, "POST", base+"/transition", map[string]any{"to": "cleared"}), nil)
	e.Must(http.StatusConflict, e.Call(qa, "POST", base+"/transition", map[string]any{"to": "closed", "decision": "x"}), nil)
	e.Must(http.StatusConflict, e.Call(qa, "POST", base+"/transition", map[string]any{"to": "bogus"}), nil)
	e.Must(http.StatusNotFound, e.Call(qa, "POST", "/api/v1/investigations/00000000-0000-0000-0000-000000000001/transition", map[string]any{"to": "quarantined"}), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", base+"/transition", map[string]any{"to": "quarantined"}), nil)
	for _, path := range []string{"/transition", "/close", "/findings", "/tracing-answer"} {
		e.Must(http.StatusBadRequest, e.Call(qa, "POST", "/api/v1/investigations/not-a-uuid"+path, map[string]any{}), nil)
		if path != "/tracing-answer" {
			e.Must(http.StatusBadRequest, e.Call(qa, "POST", base+path, "{"), nil)
		}
	}
	e.Must(http.StatusBadRequest, e.Call(qa, "GET", "/api/v1/investigations/not-a-uuid", nil), nil)
	e.Must(http.StatusBadRequest, e.Call(qa, "GET", "/api/v1/investigations/not-a-uuid/tracing-answer", nil), nil)
	e.Must(http.StatusNotFound, e.Call(qa, "GET", "/api/v1/investigations/00000000-0000-0000-0000-000000000001", nil), nil)

	// Quarantine captures a holding event; the trace module projects the disposition.
	e.Must(http.StatusOK, e.Call(qa, "POST", base+"/transition", map[string]any{"to": "quarantined", "note": "moved to quarantine cage"}), &inv)
	if inv.Status != "quarantined" || !inv.Held {
		t.Fatalf("quarantined: %+v", inv)
	}
	e.Drain()
	if s := serialStatus(t, e, epcs[0]); s != "non_sellable_other" {
		t.Fatalf("quarantined serial status: %s", s)
	}
	if s := serialStatus(t, e, epcs[2]); s != "active" {
		t.Fatalf("untouched serial status: %s", s)
	}
	// Held product cannot be shipped.
	var sh struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pid, "poNumber": "PO-9", "epcs": []string{sscc}}), &sh)
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/shipments/"+sh.ID+"/tits", nil), nil)

	e.Must(http.StatusOK, e.Call(qa, "POST", base+"/transition", map[string]any{"to": "investigating"}), &inv)

	// Findings.
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", base+"/findings", map[string]any{"summary": "x"}), nil)
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", base+"/findings", map[string]any{"kind": "gossip", "summary": "x"}), nil)
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", base+"/findings", map[string]any{"summary": " "}), nil)
	e.Must(http.StatusNotFound, e.Call(qa, "POST", "/api/v1/investigations/00000000-0000-0000-0000-000000000001/findings", map[string]any{"summary": "x"}), nil)
	e.Must(http.StatusCreated, e.Call(qa, "POST", base+"/findings", map[string]any{"summary": "Print quality differs from reference"}), &inv)
	e.Must(http.StatusCreated, e.Call(qa, "POST", base+"/findings", map[string]any{"kind": "evidence", "summary": "Photos attached", "detail": map[string]any{"files": 3}}), &inv)
	if len(inv.Findings) != 2 || inv.Findings[0].Kind != "note" || inv.Findings[1].Kind != "evidence" || inv.Findings[1].By == "" {
		t.Fatalf("findings: %+v", inv.Findings)
	}

	// Determination: the investigation becomes an illegitimate product investigation with a due date.
	e.Must(http.StatusOK, e.Call(qa, "POST", base+"/transition", map[string]any{"to": "confirmed_illegitimate"}), &inv)
	if inv.Kind != "illegitimate" || inv.DueAt == nil || time.Until(*inv.DueAt) < 23*time.Hour || time.Until(*inv.DueAt) > 25*time.Hour {
		t.Fatalf("confirmed: %+v", inv)
	}
	e.Must(http.StatusCreated, e.Call(qa, "POST", base+"/findings", map[string]any{"kind": "authority_notification", "summary": "Notified the authority", "detail": map[string]any{"reference": "REF-1"}}), &inv)

	// Closing needs a decision and the caller's own signature on this investigation.
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", base+"/close", map[string]any{}), nil)
	e.Must(http.StatusPreconditionRequired, e.Call(qa, "POST", base+"/close", map[string]any{"decision": "destroy"}), nil)
	otherSig := sign(t, e, qa, "investigation", "00000000-0000-0000-0000-000000000009")
	e.Must(http.StatusPreconditionRequired, e.Call(qa, "POST", base+"/close", map[string]any{"decision": "destroy", "signatureId": otherSig}), nil)
	adminSig := sign(t, e, e.Token, "investigation", id)
	e.Must(http.StatusPreconditionRequired, e.Call(qa, "POST", base+"/close", map[string]any{"decision": "destroy", "signatureId": adminSig}), nil)
	e.Must(http.StatusUnauthorized, e.Call(qa, "POST", "/api/v1/esignatures", map[string]any{"password": "wrong", "meaning": "approval", "reason": "r", "entity": "investigation", "entityId": id}), nil)
	sig := sign(t, e, qa, "investigation", id)
	e.Must(http.StatusOK, e.Call(qa, "POST", base+"/close", map[string]any{"decision": "Counterfeit; product destroyed", "signatureId": sig, "note": "closing"}), &inv)
	if inv.Status != "closed" || inv.SignatureID == nil || *inv.SignatureID != sig || inv.ClosedAt == nil || inv.Decision == "" || !inv.Held || inv.Overdue {
		t.Fatalf("closed: %+v", inv)
	}

	// Closed is terminal.
	e.Must(http.StatusConflict, e.Call(qa, "POST", base+"/findings", map[string]any{"summary": "late"}), nil)
	e.Must(http.StatusConflict, e.Call(qa, "POST", base+"/transition", map[string]any{"to": "investigating"}), nil)
	e.Must(http.StatusConflict, e.Call(qa, "POST", base+"/tracing-answer", nil), nil)

	// Timeline and audit.
	var d invDetail
	e.Must(http.StatusOK, e.Call(viewer, "GET", base, nil), &d)
	var kinds []string
	for _, ev := range d.Timeline {
		kinds = append(kinds, ev.Kind+":"+ev.ToStatus)
	}
	want := []string{"created:open", "transition:quarantined", "transition:investigating", "finding:investigating", "finding:investigating",
		"transition:confirmed_illegitimate", "finding:confirmed_illegitimate", "transition:closed"}
	if !slices.Equal(kinds, want) || len(d.AllowedTransitions) != 0 {
		t.Fatalf("timeline: %v allowed=%v", kinds, d.AllowedTransitions)
	}
	if d.Timeline[1].Data["captureId"] == nil || d.Timeline[7].Data["signatureId"] != sig || d.Timeline[0].Note != "pharmacy report" {
		t.Fatalf("timeline data: %+v", d.Timeline)
	}
	acts := auditActions(t, e, "investigation.")
	for _, a := range []string{"investigation.create", "investigation.quarantined", "investigation.investigating", "investigation.finding", "investigation.confirmed_illegitimate", "investigation.closed"} {
		if !slices.Contains(acts, a) {
			t.Fatalf("audit %s missing: %v", a, acts)
		}
	}

	// Cleared path by lot: release puts the packs back in the saleable state; no signature needed.
	var lot invResp
	e.Must(http.StatusCreated, e.Call(qa, "POST", "/api/v1/investigations", map[string]any{"kind": "suspect", "gtin": gtin, "lots": []string{"L1"}, "title": "Lot L1 temperature excursion"}), &lot)
	if len(lot.EPCs) != 5 || lot.Title != "Lot L1 temperature excursion" {
		t.Fatalf("lot scope: %+v", lot)
	}
	lb := "/api/v1/investigations/" + lot.ID
	// Overlapping holds: the first investigation still holds two packs.
	e.Must(http.StatusOK, e.Call(qa, "POST", lb+"/transition", map[string]any{"to": "quarantined"}), nil)
	e.Must(http.StatusOK, e.Call(qa, "POST", lb+"/transition", map[string]any{"to": "investigating"}), nil)
	e.Must(http.StatusOK, e.Call(qa, "POST", lb+"/transition", map[string]any{"to": "cleared", "note": "within limits"}), &lot)
	if lot.Held || lot.Status != "cleared" {
		t.Fatalf("cleared: %+v", lot)
	}
	e.Drain()
	if s := serialStatus(t, e, epcs[3]); s != "sellable_accessible" {
		t.Fatalf("released serial status: %s", s)
	}
	if s := serialStatus(t, e, epcs[0]); s != "non_sellable_other" {
		t.Fatalf("pack held by the other investigation was released: %s", s)
	}
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", lb+"/close", map[string]any{}), nil)
	e.Must(http.StatusOK, e.Call(qa, "POST", lb+"/close", map[string]any{"decision": "Released to stock"}), &lot)
	if lot.Status != "closed" || lot.SignatureID != nil {
		t.Fatalf("closed cleared: %+v", lot)
	}
	// An unknown lot resolves to nothing.
	var none invResp
	e.Must(http.StatusCreated, e.Call(qa, "POST", "/api/v1/investigations", map[string]any{"kind": "suspect", "gtin": gtin, "lots": []string{"ZZ"}}), &none)
	if len(none.EPCs) != 0 || len(none.NotFound) != 1 {
		t.Fatalf("unknown lot: %+v", none)
	}
	// Quarantine with nothing in the repository is a physical-only quarantine.
	e.Must(http.StatusOK, e.Call(qa, "POST", "/api/v1/investigations/"+none.ID+"/transition", map[string]any{"to": "quarantined"}), &none)
	if none.Held {
		t.Fatalf("physical quarantine marked held: %+v", none)
	}

	// Directly opened illegitimate investigations also need a signature to close, even when cleared.
	var il invResp
	e.Must(http.StatusCreated, e.Call(qa, "POST", "/api/v1/investigations", map[string]any{"kind": "illegitimate", "epcs": []string{epcs[5]}, "requester": map[string]any{"type": "authority", "name": "State board"}}), &il)
	if il.DueAt == nil || il.RequesterType != "authority" {
		t.Fatalf("illegitimate: %+v", il)
	}
	ib := "/api/v1/investigations/" + il.ID
	e.Must(http.StatusOK, e.Call(qa, "POST", ib+"/transition", map[string]any{"to": "investigating"}), nil)
	e.Must(http.StatusOK, e.Call(qa, "POST", ib+"/transition", map[string]any{"to": "cleared"}), nil)
	e.Must(http.StatusPreconditionRequired, e.Call(qa, "POST", ib+"/close", map[string]any{"decision": "false alarm"}), nil)

	// Filters.
	var list struct{ Items []invResp }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/investigations?status=closed", nil), &list)
	if len(list.Items) != 2 {
		t.Fatalf("closed filter: %d", len(list.Items))
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/investigations?status=active&kind=suspect", nil), &list)
	if len(list.Items) != 1 || list.Items[0].ID != none.ID {
		t.Fatalf("active filter: %+v", list.Items)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/investigations?q=excursion&gtin="+gtin, nil), &list)
	if len(list.Items) != 1 || list.Items[0].ID != lot.ID {
		t.Fatalf("search: %+v", list.Items)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/investigations?q="+serials[1], nil), &list)
	if len(list.Items) != 1 || list.Items[0].ID != id {
		t.Fatalf("serial search: %+v", list.Items)
	}

	// Tenant isolation.
	_, other := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusNotFound, e.Call(other, "GET", base, nil), nil)
	e.Must(http.StatusNotFound, e.Call(other, "POST", base+"/findings", map[string]any{"summary": "x"}), nil)
	e.Must(http.StatusNotFound, e.Call(other, "POST", ib+"/transition", map[string]any{"to": "closed", "decision": "x"}), nil)
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/investigations", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("other tenant sees %d investigations", len(list.Items))
	}
	// Another tenant cannot pass this tenant's signature either.
	e.Must(http.StatusNotFound, e.Call(other, "POST", ib+"/close", map[string]any{"decision": "x", "signatureId": sig}), nil)
}

func TestTracingRequestAnswer(t *testing.T) {
	e := testkit.New(t)
	viewer := userToken(t, e, "viewer@acme.test", "Viewer")
	_, epcs, sscc := setupSupply(t, e)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme Pharma", "gln": "9506000000008", "kind": "manufacturer", "country": "US", "published": true}), nil)
	pid := createPartner(t, e, map[string]any{"name": "Globex Wholesale", "gln": "0614141000005"})

	// Shipment 1 has stored TI/TS; shipment 2 (a single pack outside the case) never had one built.
	var s1, s2 struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pid, "poNumber": "PO-1", "asnNumber": "ASN-1", "epcs": []string{sscc}, "shipDate": "2026-05-01"}), &s1)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+s1.ID+"/tits", nil), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": pid, "poNumber": "PO-2", "epcs": epcs[4:5]}), &s2)

	// Policies: defaults, then a tenant window.
	var pol struct {
		Items []struct {
			Kind      string
			DueHours  int
			Basis     string
			IsDefault bool
		}
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/investigations/policies", nil), &pol)
	if len(pol.Items) != 3 || pol.Items[0].Kind != "tracing_request" || !pol.Items[0].IsDefault || !strings.Contains(pol.Items[0].Basis, "TODO(spec)") {
		t.Fatalf("policies: %+v", pol.Items)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", "/api/v1/investigations/policies/tracing_request", map[string]any{"dueHours": 24, "basis": "SOP"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/investigations/policies/recall", map[string]any{"dueHours": 24, "basis": "SOP"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/investigations/policies/tracing_request", map[string]any{"dueHours": -1, "basis": "SOP"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/investigations/policies/tracing_request", map[string]any{"dueHours": 24}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/investigations/policies/tracing_request", "{"), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/investigations/policies/tracing_request", map[string]any{"dueHours": 72, "basis": "Company SOP-17"}), nil)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/investigations/policies", nil), &pol)
	if pol.Items[0].DueHours != 72 || pol.Items[0].IsDefault {
		t.Fatalf("policy update: %+v", pol.Items[0])
	}

	// A partner asks for the history of the case and of the loose pack.
	var inv invResp
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/investigations", map[string]any{"kind": "tracing_request", "epcs": []string{sscc, epcs[4]},
		"requester": map[string]any{"type": "partner", "partnerId": pid}, "requestRef": "TR-2026-001"}), &inv)
	if len(inv.EPCs) != 6 || inv.RequesterName != "Globex Wholesale" || inv.DueAt == nil || time.Until(*inv.DueAt) < 71*time.Hour {
		t.Fatalf("tracing request: %+v", inv)
	}
	base := "/api/v1/investigations/" + inv.ID
	var d invDetail
	e.Must(http.StatusOK, e.Do("GET", base, nil), &d)
	if !slices.Equal(d.AllowedTransitions, []string{"investigating"}) {
		t.Fatalf("allowed: %v", d.AllowedTransitions)
	}
	// Tracing requests are not quarantined, and are not closed before an answer exists.
	e.Must(http.StatusConflict, e.Do("POST", base+"/transition", map[string]any{"to": "quarantined"}), nil)
	e.Must(http.StatusOK, e.Do("POST", base+"/transition", map[string]any{"to": "investigating"}), nil)
	e.Must(http.StatusConflict, e.Do("POST", base+"/close", map[string]any{"decision": "answered"}), nil)
	e.Must(http.StatusNotFound, e.Do("GET", base+"/tracing-answer", nil), nil)
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/investigations/00000000-0000-0000-0000-000000000001/tracing-answer", nil), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/investigations/00000000-0000-0000-0000-000000000001/tracing-answer", nil), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", base+"/tracing-answer", nil), nil)

	var ans struct {
		InvestigationID, RequestRef string
		Requester                   map[string]any
		Products                    []map[string]any
		Items                       []struct {
			ID        string
			State     struct{ Status string }
			Ancestors []string
			History   []json.RawMessage
			Shipments []string
		}
		Transactions []struct {
			ShipmentID, Direction, PONumber, TITSSource, Note string
			Partner                                           struct{ Name, GLN string }
			TransactionInformation                            map[string]any
			TransactionStatement                              map[string]any
			EventCount                                        int
		}
		NotFound  []string
		Truncated bool
	}
	e.Must(http.StatusOK, e.Do("POST", base+"/tracing-answer", nil), &ans)
	if ans.InvestigationID != inv.ID || ans.RequestRef != "TR-2026-001" || ans.Requester["type"] != "partner" || len(ans.Items) != 6 || len(ans.NotFound) != 0 || ans.Truncated {
		t.Fatalf("answer: %+v", ans)
	}
	if len(ans.Products) != 1 || ans.Products[0]["name"] != "Metformin 850mg" || ans.Products[0]["ndc"] == nil {
		t.Fatalf("products: %+v", ans.Products)
	}
	if len(ans.Transactions) != 2 {
		t.Fatalf("transactions: %+v", ans.Transactions)
	}
	byShip := map[string]int{}
	for i, tr := range ans.Transactions {
		byShip[tr.ShipmentID] = i
	}
	t1, t2 := ans.Transactions[byShip[s1.ID]], ans.Transactions[byShip[s2.ID]]
	if t1.TITSSource != "stored" || t1.PONumber != "PO-1" || t1.Partner.GLN != "0614141000005" || t1.TransactionStatement["affirmTransactionStatement"] != true ||
		t1.TransactionInformation["poNumber"] != "PO-1" || t1.EventCount != 2 {
		t.Fatalf("stored TI/TS transaction: %+v", t1)
	}
	if t2.TITSSource != "rebuilt" || t2.Note == "" || t2.TransactionInformation == nil {
		t.Fatalf("rebuilt TI/TS transaction: %+v", t2)
	}
	packs := 0
	for _, it := range ans.Items {
		if len(it.History) == 0 || len(it.Shipments) != 1 {
			t.Fatalf("item %s: history=%d shipments=%v", it.ID, len(it.History), it.Shipments)
		}
		if len(it.Ancestors) == 1 {
			packs++
			if it.Shipments[0] != s1.ID {
				t.Fatalf("pack in case linked to %v", it.Shipments)
			}
		}
	}
	if packs != 4 {
		t.Fatalf("packs inside the case: %d", packs)
	}
	// The stored answer is downloadable (Viewer may read).
	dl := e.Call(viewer, "GET", base+"/tracing-answer", nil)
	if dl.Status != http.StatusOK || !strings.Contains(dl.Header.Get("Content-Disposition"), "tracing-answer-"+inv.ID) || !strings.Contains(string(dl.Body), "TR-2026-001") {
		t.Fatalf("download: %d %v", dl.Status, dl.Header)
	}
	e.Must(http.StatusOK, e.Do("POST", base+"/close", map[string]any{"decision": "Answer sent to requester"}), &inv)
	if inv.Status != "closed" || inv.AnsweredAt == nil {
		t.Fatalf("closed: %+v", inv)
	}
	if acts := auditActions(t, e, "investigation."); !slices.Contains(acts, "investigation.tracing_answer") || !slices.Contains(acts, "investigation.policy.put") {
		t.Fatalf("audit: %v", acts)
	}

	// A request about a shipment resolves the shipment's contents; an authority requester is named.
	var byShipment invResp
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/investigations", map[string]any{"kind": "tracing_request", "shipmentId": s1.ID,
		"requester": map[string]any{"type": "authority", "name": "FDA"}, "dueAt": time.Now().Add(-time.Hour).Format(time.RFC3339)}), &byShipment)
	if len(byShipment.EPCs) != 5 || byShipment.RequesterName != "FDA" || !byShipment.Overdue {
		t.Fatalf("shipment scope: %+v", byShipment)
	}

	// Overdue investigations raise one alert each (deduplicated).
	var list struct{ Items []invResp }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/investigations?overdue=true", nil), &list)
	if len(list.Items) != 1 || list.Items[0].ID != byShipment.ID {
		t.Fatalf("overdue filter: %+v", list.Items)
	}
	run := scheduleRunner(t, e, "dscsa.investigations_overdue")
	run()
	run()
	var al alertList
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=dscsa.investigation_overdue", nil), &al)
	if len(al.Items) != 1 || al.Items[0].EntityID != byShipment.ID {
		t.Fatalf("overdue alerts: %+v", al.Items)
	}
}

func scheduleRunner(t *testing.T, e *testkit.Env, name string) func() {
	t.Helper()
	for _, m := range e.App.Modules {
		if s, ok := m.(kernel.Scheduled); ok {
			for _, sc := range s.Schedules() {
				if sc.Name == name {
					return func() {
						if err := sc.Run(context.Background()); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
	}
	t.Fatalf("no schedule %s", name)
	return nil
}

func TestInvestigationFromAlerts(t *testing.T) {
	e := testkit.New(t)
	qa := userToken(t, e, "qa@acme.test", "Compliance")
	serials, _, _ := setupSupply(t, e)

	// A public scan of a never-issued serial raises a verify.* alert.
	var scan struct{ Status string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": gs1.HRI(gs1.Pack{GTIN: gtin, Serial: "FAKE123"}.AIs())}), &scan)
	if scan.Status != "not_found" {
		t.Fatalf("scan: %+v", scan)
	}
	// A VRS answer of "not verified" raises a dscsa.vrs_not_verified alert.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/directory", map[string]any{"gtinPrefix": "9506000", "responderUrl": "local:" + e.Tenant.ID.String()}), nil)
	var v struct{ Verified bool }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": serials[0], "lot": "WRONG"}), &v)
	if v.Verified {
		t.Fatal("wrong lot verified")
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/vrs/requests", map[string]any{"gtin": gtin, "serial": serials[0]}), &v)
	if !v.Verified {
		t.Fatal("genuine pack not verified")
	}

	type alertRow struct {
		ID, Kind, Status string
		Detail           map[string]any
	}
	var al struct{ Items []alertRow }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts", nil), &al)
	byKind := map[string]alertRow{}
	for _, a := range al.Items {
		byKind[a.Kind] = a
	}
	scanAlert, vrsAlert := byKind["verify.not_found"], byKind["dscsa.vrs_not_verified"]
	if scanAlert.ID == "" || vrsAlert.ID == "" || len(al.Items) != 2 {
		t.Fatalf("alerts: %+v", al.Items)
	}

	e.Must(http.StatusBadRequest, e.Call(qa, "POST", "/api/v1/investigations/from-alert", map[string]any{"alertId": scanAlert.ID, "kind": "tracing_request"}), nil)
	e.Must(http.StatusNotFound, e.Call(qa, "POST", "/api/v1/investigations/from-alert", map[string]any{"alertId": "00000000-0000-0000-0000-000000000001"}), nil)
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", "/api/v1/investigations/from-alert", "{"), nil)

	var inv invResp
	e.Must(http.StatusCreated, e.Call(qa, "POST", "/api/v1/investigations/from-alert", map[string]any{"alertId": scanAlert.ID}), &inv)
	if inv.Kind != "suspect" || inv.SourceAlertID == nil || *inv.SourceAlertID != scanAlert.ID || len(inv.EPCs) != 0 || len(inv.NotFound) != 1 || inv.Serials[0] != "FAKE123" {
		t.Fatalf("from scan alert: %+v", inv)
	}
	e.Must(http.StatusConflict, e.Call(qa, "POST", "/api/v1/investigations/from-alert", map[string]any{"alertId": scanAlert.ID}), nil)

	var vinv invResp
	e.Must(http.StatusCreated, e.Call(qa, "POST", "/api/v1/investigations/from-alert", map[string]any{"alertId": vrsAlert.ID, "title": "VRS mismatch"}), &vinv)
	if len(vinv.EPCs) != 1 || vinv.Title != "VRS mismatch" {
		t.Fatalf("from vrs alert: %+v", vinv)
	}
	var d invDetail
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/investigations/"+vinv.ID, nil), &d)
	if !strings.Contains(d.Timeline[0].Note, "WRONG") {
		t.Fatalf("lot note: %+v", d.Timeline[0])
	}

	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=resolved", nil), &al)
	if len(al.Items) != 2 {
		t.Fatalf("resolved alerts: %+v", al.Items)
	}
	for _, a := range al.Items {
		if a.Detail["investigationId"] == nil {
			t.Fatalf("alert not linked: %+v", a)
		}
	}
	if acts := auditActions(t, e, "alert."); !slices.Contains(acts, "alert.resolve") {
		t.Fatalf("audit: %v", acts)
	}

	// Other alert kinds are refused.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/investigations", map[string]any{"kind": "tracing_request", "epcs": []string{"x"}, "requester": map[string]any{"type": "authority", "name": "FDA"}, "dueAt": time.Now().Add(-time.Minute).Format(time.RFC3339)}), nil)
	scheduleRunner(t, e, "dscsa.investigations_overdue")()
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=dscsa.investigation_overdue", nil), &al)
	if len(al.Items) != 1 {
		t.Fatalf("overdue alert: %+v", al.Items)
	}
	e.Must(http.StatusBadRequest, e.Call(qa, "POST", "/api/v1/investigations/from-alert", map[string]any{"alertId": al.Items[0].ID}), nil)
}
