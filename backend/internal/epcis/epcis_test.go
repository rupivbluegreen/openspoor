package epcis_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

const gtin = "09506000134352"

func setupProduct(t *testing.T, e *testkit.Env) (poolID string) {
	t.Helper()
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Paracetamol 500mg", "companyPrefixLength": 7, "targetMarkets": []string{"EU"}}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU", "mode": "random", "length": 12}), &pool)
	return pool.ID
}

func allocate(t *testing.T, e *testkit.Env, poolID string, n int, key string) []string {
	t.Helper()
	var a struct{ Serials []string }
	r := e.Do("POST", "/api/v1/serial-pools/"+poolID+"/allocate", map[string]any{"quantity": n}, "Idempotency-Key", key)
	if r.Status != http.StatusCreated && r.Status != http.StatusOK {
		t.Fatalf("allocate: %d %s", r.Status, r.Body)
	}
	_ = r.JSON(&a)
	return a.Serials
}

func dl(serial string) string {
	return gs1.DigitalLink("https://brand.example", gs1.Pack{GTIN: gtin, Serial: serial})
}

func urn(serial string) string {
	u, _ := gs1.SGTINURN(gtin, serial, 7)
	return u
}

const sscc = "urn:epc:id:sscc:9506000.0000000017"

func doc(events ...lib.Event) []byte {
	b, _ := lib.WriteJSON(lib.NewDocument(events))
	return b
}

func ev(typ, action, step, disp string, at time.Time) lib.Event {
	return lib.Event{Type: typ, Action: action, BizStep: step, Disposition: disp, EventTime: at, EventTimeZoneOffset: "+00:00",
		ReadPoint: &lib.Location{ID: "urn:epc:id:sgln:9506000.13435.0"}}
}

func TestCaptureTraceLifecycle(t *testing.T) {
	e := testkit.New(t)
	poolID := setupProduct(t, e)

	// Idempotent allocation: same key, same serials.
	s1 := allocate(t, e, poolID, 5, "order-1")
	s2 := allocate(t, e, poolID, 5, "order-1")
	if strings.Join(s1, ",") != strings.Join(s2, ",") {
		t.Fatal("idempotent replay returned different serials")
	}
	s3 := allocate(t, e, poolID, 5, "order-2")
	if s3[0] == s1[0] {
		t.Fatal("new allocation reissued a serial")
	}
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/serial-pools/"+poolID+"/allocate", map[string]any{"quantity": 6}, "Idempotency-Key", "order-1"), nil)

	t0 := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
	comm := ev(lib.ObjectEvent, "ADD", "commissioning", "active", t0)
	// mix of URN and Digital Link forms for the same item space
	comm.EPCList = []string{urn(s1[0]), dl(s1[1]), urn(s1[2])}
	comm.ILMD = map[string]any{"lotNumber": "LOT-1", "itemExpirationDate": "2028-01-31"}
	pack := ev(lib.AggregationEvent, "ADD", "packing", "in_progress", t0.Add(time.Minute))
	pack.ParentID, pack.ChildEPCs = sscc, []string{dl(s1[0]), urn(s1[1])}
	ship := ev(lib.ObjectEvent, "OBSERVE", "shipping", "in_transit", t0.Add(time.Hour))
	ship.EPCList = []string{sscc}
	ship.BizTransactionList = []lib.BizTransaction{{Type: "po", BizTransaction: "urn:epcglobal:cbv:bt:9506000134352:PO-77"}}

	var c epcis.Capture
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", doc(comm, pack, ship)), &c)
	if c.EventCount != 3 || c.Status != "accepted" {
		t.Fatalf("capture: %+v", c)
	}
	e.Drain()
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/capture/"+c.ID.String(), nil), &c)
	if c.Status != "completed" {
		t.Fatalf("projection did not complete: %s", c.Status)
	}

	// Re-capture is idempotent (same event IDs).
	var again epcis.Capture
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", doc(comm, pack, ship)), &again)
	if again.Duplicates != 3 || again.EventCount != 0 {
		t.Fatalf("duplicates not skipped: %+v", again)
	}

	// Serial lookup works with either identifier form; parent is the SSCC.
	var view struct {
		State struct {
			Status    string
			Lot       *string
			ParentEPC *string
		}
		Ancestors []struct{ EPC, Status string }
		History   []lib.Event
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/?id="+url.QueryEscape(urn(s1[0])), nil), &view)
	if view.State.ParentEPC == nil || !strings.HasSuffix(*view.State.ParentEPC, "/00/095060000000000172") && !strings.Contains(*view.State.ParentEPC, "/00/") {
		t.Fatalf("parent not set: %+v", view.State)
	}
	if view.State.Lot == nil || *view.State.Lot != "LOT-1" || len(view.History) != 2 {
		t.Fatalf("state/history wrong: %+v history=%d", view.State, len(view.History))
	}
	if len(view.Ancestors) != 1 || view.Ancestors[0].Status != "in_transit" {
		t.Fatalf("ancestor (shipped case) should be in_transit: %+v", view.Ancestors)
	}

	var tree struct {
		EPC      string
		Children []struct{ EPC string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/tree/?id="+url.QueryEscape(sscc), nil), &tree)
	if len(tree.Children) != 2 {
		t.Fatalf("tree children: %+v", tree)
	}

	// Query interface.
	var qd struct {
		EpcisBody struct {
			QueryResults struct {
				ResultsBody struct{ EventList []lib.Event }
			}
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?EQ_bizStep=shipping&MATCH_anyEPC="+url.QueryEscape(lib.Canonical(sscc)), nil), &qd)
	if n := len(qd.EpcisBody.QueryResults.ResultsBody.EventList); n != 1 {
		t.Fatalf("query returned %d events", n)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?MATCH_epc="+url.QueryEscape(dl(s1[1])), nil), &qd)
	if n := len(qd.EpcisBody.QueryResults.ResultsBody.EventList); n != 2 {
		t.Fatalf("digital-link query returned %d events, want 2 (commission+pack)", n)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?perPage=1&eventType=ObjectEvent", nil), nil)
	r := e.Do("GET", "/api/v1/epcis/events?perPage=1", nil)
	if tok := r.Header.Get("GS1-Next-Page-Token"); tok == "" {
		t.Fatal("expected pagination token")
	}
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/epcis/events?bogus=1", nil), nil)

	// Unpack and decommission.
	unpack := ev(lib.AggregationEvent, "DELETE", "unpacking", "", t0.Add(2*time.Hour))
	unpack.ParentID = sscc
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", doc(unpack)), nil)
	e.Drain()
	var after struct{ State struct{ ParentEPC *string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/?id="+url.QueryEscape(urn(s1[0])), nil), &after)
	if after.State.ParentEPC != nil {
		t.Fatalf("unpack-all did not clear parent: %v", *after.State.ParentEPC)
	}

	var stats struct {
		Events, Serials int
		ByStatus        map[string]int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/stats", nil), &stats)
	if stats.Events != 4 || stats.Serials != 4 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestCaptureRejections(t *testing.T) {
	e := testkit.New(t)
	poolID := setupProduct(t, e)
	s := allocate(t, e, poolID, 3, "k1")
	t0 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	ok := ev(lib.ObjectEvent, "ADD", "commissioning", "active", t0)
	ok.EPCList = []string{urn(s[0])}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", doc(ok)), nil)

	unknown := ev(lib.ObjectEvent, "ADD", "commissioning", "active", t0)
	unknown.EPCList = []string{"urn:epc:id:sgtin:0614141.107346.1"}
	never := ev(lib.ObjectEvent, "ADD", "commissioning", "active", t0)
	never.EPCList = []string{urn("ZZZZZZZZZZZZ")}
	twice := ev(lib.ObjectEvent, "ADD", "commissioning", "active", t0.Add(time.Second))
	twice.EPCList = []string{urn(s[0])}
	self := ev(lib.AggregationEvent, "ADD", "packing", "", t0)
	self.ParentID, self.ChildEPCs = sscc, []string{sscc}
	bad := ev(lib.ObjectEvent, "ADD", "teleporting", "", t0)
	bad.EPCList = []string{urn(s[1])}

	var rej struct {
		CaptureID string
		Errors    []lib.FieldError
	}
	// Structural errors are reported first.
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/epcis/capture", doc(bad)), &rej)
	if len(rej.Errors) != 1 || rej.Errors[0].Field != "bizStep" {
		t.Fatalf("structural: %+v", rej.Errors)
	}
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/epcis/capture", doc(unknown, never, twice, self)), &rej)
	byIdx := map[int]string{}
	for _, fe := range rej.Errors {
		byIdx[fe.Index] = fe.Message
	}
	for i, want := range []string{"unknown GTIN", "never allocated", "already commissioned", "cannot contain itself"} {
		if !strings.Contains(byIdx[i], want) {
			t.Errorf("event %d: want %q, got %q", i, want, byIdx[i])
		}
	}
	// Nothing from the rejected document was stored; the rejection is in the inbox.
	var stats struct{ Events int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/stats", nil), &stats)
	if stats.Events != 1 {
		t.Fatalf("rejected events were stored: %d", stats.Events)
	}
	var caps struct{ Items []epcis.Capture }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/capture?status=rejected", nil), &caps)
	if len(caps.Items) != 2 {
		t.Fatalf("rejections not recorded: %d", len(caps.Items))
	}

	// Packing a child already in another case is rejected.
	ok2 := ev(lib.ObjectEvent, "ADD", "commissioning", "active", t0)
	ok2.EPCList = []string{urn(s[1])}
	p1 := ev(lib.AggregationEvent, "ADD", "packing", "", t0.Add(time.Minute))
	p1.ParentID, p1.ChildEPCs = sscc, []string{urn(s[1])}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", doc(ok2, p1)), nil)
	e.Drain()
	p2 := ev(lib.AggregationEvent, "ADD", "packing", "", t0.Add(2*time.Minute))
	p2.ParentID, p2.ChildEPCs = "urn:epc:id:sscc:9506000.0000000024", []string{dl(s[1])}
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/epcis/capture", doc(p2)), &rej)
	if !strings.Contains(rej.Errors[0].Message, "already packed") {
		t.Fatalf("aggregation: %+v", rej.Errors)
	}
}

func TestXMLCaptureAndSubscription(t *testing.T) {
	e := testkit.New(t)
	var mu sync.Mutex
	var got [][]byte
	var sig string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, b)
		sig = r.Header.Get("X-OpenSpoor-Signature")
		mu.Unlock()
	}))
	defer hook.Close()
	var sub struct{ ID, Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": hook.URL, "query": map[string]string{"EQ_bizStep": "shipping"}}), &sub)

	x := `<?xml version="1.0"?><epcis:EPCISDocument xmlns:epcis="urn:epcglobal:epcis:xsd:1" schemaVersion="1.2" creationDate="2026-01-01T00:00:00Z"><EPCISBody><EventList>
	<ObjectEvent><eventTime>2026-01-01T00:00:00Z</eventTime><eventTimeZoneOffset>+00:00</eventTimeZoneOffset><epcList><epc>urn:epc:id:sscc:9506000.0000000017</epc></epcList>
	<action>OBSERVE</action><bizStep>urn:epcglobal:cbv:bizstep:shipping</bizStep></ObjectEvent>
	<ObjectEvent><eventTime>2026-01-01T00:00:01Z</eventTime><eventTimeZoneOffset>+00:00</eventTimeZoneOffset><epcList><epc>urn:epc:id:sscc:9506000.0000000017</epc></epcList>
	<action>OBSERVE</action><bizStep>urn:epcglobal:cbv:bizstep:receiving</bizStep></ObjectEvent>
	</EventList></EPCISBody></epcis:EPCISDocument>`
	r := e.Do("POST", "/api/v1/epcis/capture", x, "Content-Type", "application/xml")
	e.Must(http.StatusAccepted, r, nil)
	e.Drain()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("webhook deliveries: %d", len(got))
	}
	if sig != epcis.Sign(sub.Secret, got[0]) {
		t.Fatal("bad webhook signature")
	}
	var qd map[string]any
	_ = json.Unmarshal(got[0], &qd)
	evs := qd["epcisBody"].(map[string]any)["queryResults"].(map[string]any)["resultsBody"].(map[string]any)["eventList"].([]any)
	if len(evs) != 1 {
		t.Fatalf("subscription filter: %d events", len(evs))
	}
	// XML output for the query interface.
	xr := e.Do("GET", "/api/v1/epcis/events", nil, "Accept", "application/xml")
	if !strings.Contains(string(xr.Body), "<ObjectEvent>") {
		t.Fatalf("xml query: %s", xr.Body)
	}
}

func TestProductCSVAndPackaging(t *testing.T) {
	e := testkit.New(t)
	csv := "gtin,name,ndc,target_markets\n09506000134352,Aspirin,1234-5678-90,US;EU\n09506000134353,Bad,,\n0614141000012,Ibuprofen,,SA\n"
	var res struct {
		Created int
		Errors  []struct {
			Line    int
			Message string
		}
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/products/import", csv, "Content-Type", "text/csv"), &res)
	if res.Created != 2 || len(res.Errors) != 1 || res.Errors[0].Line != 3 {
		t.Fatalf("import: %+v", res)
	}
	var page struct {
		Items []struct{ ID, GTIN string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/products?q=Aspirin", nil), &page)
	if len(page.Items) != 1 {
		t.Fatal("search failed")
	}
	levels := map[string]any{"levels": []map[string]any{{"level": "unit", "quantity": 1}, {"level": "case", "quantity": 24}, {"level": "pallet", "quantity": 40}}}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/products/"+page.Items[0].ID+"/packaging/us", levels), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/products/"+page.Items[0].ID+"/packaging/eu",
		map[string]any{"levels": []map[string]any{{"level": "case", "quantity": 1}, {"level": "unit", "quantity": 1}}}), nil)

	var enc struct{ ElementString, DigitalLink, URN, Image string }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/codes/encode", map[string]any{"gtin": "09506000134352", "serial": "ABC", "lot": "L1", "expiry": "281231"}), &enc)
	if !strings.HasPrefix(enc.Image, "data:image/png;base64,") || enc.URN == "" {
		t.Fatalf("encode: %+v", enc)
	}
	var dec struct{ GTIN, Serial, Lot, Expiry string }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/codes/decode", map[string]any{"code": enc.ElementString}), &dec)
	if dec.Serial != "ABC" || dec.Lot != "L1" || dec.Expiry != "281231" {
		t.Fatalf("decode: %+v", dec)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/codes/decode", map[string]any{"code": enc.DigitalLink}), &dec)
	if dec.Serial != "ABC" {
		t.Fatalf("decode dl: %+v", dec)
	}
	_ = fmt.Sprint
}
