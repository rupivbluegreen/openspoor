package epcis_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

type queryDoc struct {
	EpcisBody struct {
		QueryResults struct {
			QueryName   string
			ResultsBody struct{ EventList []lib.Event }
		}
	}
}

func (q queryDoc) events() []lib.Event { return q.EpcisBody.QueryResults.ResultsBody.EventList }

func module(e *testkit.Env) *epcis.Module {
	for _, m := range e.App.Modules {
		if x, ok := m.(*epcis.Module); ok {
			return x
		}
	}
	return nil
}

const (
	ssccA = "urn:epc:id:sscc:9506000.0000000017"
	ssccB = "urn:epc:id:sscc:9506000.0000000024"
	ssccC = "urn:epc:id:sscc:9506000.0000000031"
	glnA  = "urn:epc:id:sgln:9506000.13435.0"
	glnB  = "urn:epc:id:sgln:9506000.13442.0"
)

// seedQueryEvents stores four observation events that need no master data.
func seedQueryEvents(t *testing.T, e *testkit.Env, t0 time.Time) epcis.Capture {
	t.Helper()
	ship := ev(lib.ObjectEvent, "OBSERVE", "shipping", "in_transit", t0)
	ship.EPCList = []string{ssccA}
	ship.BizLocation = &lib.Location{ID: glnA}
	recv := ev(lib.ObjectEvent, "OBSERVE", "receiving", "in_progress", t0.Add(time.Hour))
	recv.EPCList = []string{ssccB}
	recv.ReadPoint = &lib.Location{ID: glnB}
	pack := ev(lib.AggregationEvent, "OBSERVE", "packing", "in_progress", t0.Add(2*time.Hour))
	pack.ParentID, pack.ChildEPCs = ssccC, []string{ssccB}
	qty := ev(lib.ObjectEvent, "OBSERVE", "storing", "", t0.Add(3*time.Hour))
	qty.QuantityList = []lib.QuantityElement{{EPCClass: "urn:epc:class:lgtin:9506000.013435.LOT1"}}
	var c epcis.Capture
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", doc(ship, recv, pack, qty), "X-Source", "test"), &c)
	if c.EventCount != 4 || c.Source != "test" || c.Timing["total"] <= 0 {
		t.Fatalf("seed capture: %+v", c)
	}
	return c
}

func TestQueryFilters(t *testing.T) {
	e := testkit.New(t)
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	c := seedQueryEvents(t, e, t0)

	count := func(qs string) int {
		t.Helper()
		var q queryDoc
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?"+qs, nil), &q)
		return len(q.events())
	}
	esc := url.QueryEscape
	cases := map[string]int{
		"":                           4,
		"eventType=AggregationEvent": 1,
		"eventTypes=ObjectEvent,AggregationEvent":                     4,
		"GE_eventTime=" + esc(t0.Add(time.Hour).Format(time.RFC3339)): 3,
		"LT_eventTime=" + esc(t0.Add(time.Hour).Format(time.RFC3339)): 1,
		"GE_recordTime=" + esc(t0.Format(time.RFC3339)):               4,
		"LT_recordTime=" + esc(t0.Format(time.RFC3339)):               0,
		"EQ_action=OBSERVE":    4,
		"EQ_action=ADD,DELETE": 0,
		"EQ_bizStep=" + esc("urn:epcglobal:cbv:bizstep:receiving"):         1,
		"EQ_disposition=in_progress":                                       2,
		"EQ_disposition=" + esc("https://ref.gs1.org/cbv/Disp-in_transit"): 1,
		"EQ_readPoint=" + esc(glnB):                                        1,
		"EQ_bizLocation=" + esc(glnA):                                      1,
		"EQ_bizLocation=" + esc(glnB):                                      0,
		"MATCH_parentID=" + esc(ssccC):                                     1,
		"MATCH_epc=" + esc(ssccB):                                          2,
		"MATCH_anyEPC=" + esc(ssccA) + "," + esc(ssccC):                    2,
		"MATCH_epcClass=" + esc("https://id.gs1.org/01/*"):                 1,
		"MATCH_epc=" + esc("https://id.gs1.org/00/*"):                      3,
		"MATCH_inputEPC=" + esc("urn:none:*"):                              0,
		"EQ_captureID=" + c.ID.String():                                    4,
		"EQ_bizStep=shipping&EQ_disposition=in_progress":                   0,
		"EQ_action=": 4, // empty values are ignored
	}
	for qs, want := range cases {
		if got := count(qs); got != want {
			t.Errorf("%s: %d events, want %d", qs, got, want)
		}
	}
	for _, qs := range []string{"GE_eventTime=yesterday", "LT_recordTime=1", "bogus=1", "nextPageToken=%25%25", "nextPageToken=" + esc("bm90IGpzb24")} {
		if r := e.Do("GET", "/api/v1/epcis/events?"+qs, nil); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d", qs, r.Status)
		}
	}

	// Single event by ID (IDs are URIs, so they are path-escaped).
	var all queryDoc
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?EQ_bizStep=shipping", nil), &all)
	id := all.events()[0].EventID
	if !strings.HasPrefix(id, "ni:///sha-256;") {
		t.Fatalf("event id: %s", id)
	}
	if n := count("EQ_eventID=" + esc(id)); n != 1 {
		t.Fatalf("EQ_eventID: %d", n)
	}
	r := e.Do("GET", "/api/v1/epcis/events/"+url.PathEscape(id), nil)
	var one lib.Event
	e.Must(http.StatusOK, r, &one)
	if r.Header.Get("Content-Type") != "application/ld+json" || one.EventID != id || one.BizStep != "shipping" || one.RecordTime == nil {
		t.Fatalf("get event: %s %+v", r.Header.Get("Content-Type"), one)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/epcis/events/"+url.PathEscape("urn:uuid:missing"), nil), nil)

	// Tenants never see each other's events.
	_, tok := e.NewTenant("globex", "admin@globex.test")
	var other queryDoc
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/epcis/events", nil), &other)
	if len(other.events()) != 0 {
		t.Fatalf("cross-tenant leak: %d events", len(other.events()))
	}
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/epcis/events/"+url.PathEscape(id), nil), nil)
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/epcis/capture/"+c.ID.String(), nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/epcis/capture/nope", nil), nil)
}

func TestQueryPagination(t *testing.T) {
	e := testkit.New(t)
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	seedQueryEvents(t, e, t0)

	walk := func(base string) []time.Time {
		t.Helper()
		var times []time.Time
		path := "/api/v1/epcis/events?" + base
		for pages := 0; pages < 10; pages++ {
			r := e.Do("GET", path, nil)
			var q queryDoc
			e.Must(http.StatusOK, r, &q)
			for _, ev := range q.events() {
				times = append(times, ev.EventTime)
			}
			tok := r.Header.Get("GS1-Next-Page-Token")
			if tok == "" {
				if r.Header.Get("Link") != "" {
					t.Fatal("Link header on last page")
				}
				return times
			}
			if !strings.Contains(r.Header.Get("Link"), `rel="next"`) {
				t.Fatalf("Link header: %q", r.Header.Get("Link"))
			}
			path = "/api/v1/epcis/events?" + base + "&nextPageToken=" + url.QueryEscape(tok)
		}
		t.Fatal("pagination did not terminate")
		return nil
	}
	asc := walk("perPage=3")
	if len(asc) != 4 || !asc[0].Equal(t0) || !asc[3].Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("ascending: %v", asc)
	}
	desc := walk("perPage=1&orderDirection=desc")
	if len(desc) != 4 || !desc[0].Equal(t0.Add(3*time.Hour)) || !desc[3].Equal(t0) {
		t.Fatalf("descending: %v", desc)
	}
	byRecord := walk("perPage=2&orderBy=recordTime")
	if len(byRecord) != 4 {
		t.Fatalf("by record time: %v", byRecord)
	}
	// Out-of-range or junk perPage falls back to the default page size.
	for _, p := range []string{"0", "5000", "abc"} {
		r := e.Do("GET", "/api/v1/epcis/events?perPage="+p, nil)
		var q queryDoc
		e.Must(http.StatusOK, r, &q)
		if len(q.events()) != 4 || r.Header.Get("GS1-Next-Page-Token") != "" {
			t.Errorf("perPage=%s: %d events", p, len(q.events()))
		}
	}
}

func TestNamedQueries(t *testing.T) {
	e := testkit.New(t)
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	seedQueryEvents(t, e, t0)

	for name, body := range map[string]map[string]any{
		"no name":     {"query": map[string]string{}},
		"space":       {"name": "a b", "query": map[string]string{}},
		"slash":       {"name": "a/b", "query": map[string]string{}},
		"unsupported": {"name": "x", "query": map[string]string{"EQ_color": "red"}},
		"bad time":    {"name": "x", "query": map[string]string{"GE_eventTime": "now"}},
	} {
		if r := e.Do("POST", "/api/v1/epcis/queries", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/epcis/queries", "{"), nil)
	var nq struct {
		ID    string
		Name  string
		Query map[string]string
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/queries", map[string]any{"name": "in-progress", "query": map[string]string{"EQ_disposition": "in_progress"}}), &nq)
	if nq.Name != "in-progress" || nq.Query["EQ_disposition"] != "in_progress" {
		t.Fatalf("created: %+v", nq)
	}
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/epcis/queries", map[string]any{"name": "in-progress", "query": map[string]string{}}), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/queries", map[string]any{"name": "all", "query": map[string]string{}}), nil)

	var list struct {
		Items []struct{ Name string }
		Total int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/queries", nil), &list)
	if list.Total != 2 || list.Items[0].Name != "all" || list.Items[1].Name != "in-progress" {
		t.Fatalf("list: %+v", list)
	}

	var q queryDoc
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/queries/in-progress/events", nil), &q)
	if q.EpcisBody.QueryResults.QueryName != "in-progress" || len(q.events()) != 2 {
		t.Fatalf("run named: %+v", q)
	}
	r := e.Do("GET", "/api/v1/epcis/queries/all/events?perPage=3", nil)
	e.Must(http.StatusOK, r, &q)
	tok := r.Header.Get("GS1-Next-Page-Token")
	if len(q.events()) != 3 || tok == "" {
		t.Fatalf("named query paging: %d events, token %q", len(q.events()), tok)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/queries/all/events?perPage=3&nextPageToken="+url.QueryEscape(tok), nil), &q)
	if len(q.events()) != 1 {
		t.Fatalf("named query page 2: %d", len(q.events()))
	}
	xr := e.Do("GET", "/api/v1/epcis/queries/in-progress/events", nil, "Accept", "application/xml")
	if xr.Status != http.StatusOK || xr.Header.Get("Content-Type") != "application/xml" || strings.Count(string(xr.Body), "<disposition>") != 2 {
		t.Fatalf("named query xml: %d %s", xr.Status, xr.Body)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/epcis/queries/missing/events", nil), nil)
}

func TestSubscriptionLifecycle(t *testing.T) {
	e := testkit.New(t)
	var hits atomic.Int32
	var fail atomic.Bool
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer hook.Close()

	for name, body := range map[string]map[string]any{
		"no dest":    {},
		"ftp":        {"dest": "ftp://x/y"},
		"no host":    {"dest": "https://"},
		"bad query":  {"dest": hook.URL, "query": map[string]string{"LT_eventTime": "x"}},
		"not object": {"dest": hook.URL, "query": "x"},
	} {
		if r := e.Do("POST", "/api/v1/epcis/subscriptions", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}

	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/queries", map[string]any{"name": "shipping", "query": map[string]string{"EQ_bizStep": "shipping"}}), nil)
	var named, plain, failing struct {
		ID, Secret string
		QueryName  *string
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": hook.URL + "/named", "queryName": "shipping", "secret": "s3cret"}), &named)
	if named.Secret != "s3cret" || named.QueryName == nil || *named.QueryName != "shipping" {
		t.Fatalf("named sub: %+v", named)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": hook.URL + "/plain"}), &plain)
	if len(plain.Secret) < 16 {
		t.Fatalf("generated secret too short: %q", plain.Secret)
	}
	var subs struct {
		Items []struct {
			ID             string
			Active         bool
			DeliveredCount int
			LastError      string
			Secret         string
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/subscriptions", nil), &subs)
	if len(subs.Items) != 2 || subs.Items[0].Secret != "" {
		t.Fatalf("list must not expose secrets: %+v", subs)
	}

	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	seedQueryEvents(t, e, t0)
	e.Drain()
	if hits.Load() != 2 {
		t.Fatalf("deliveries: %d, want 2", hits.Load())
	}
	delivered := map[string]int{}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/subscriptions", nil), &subs)
	for _, s := range subs.Items {
		delivered[s.ID] = s.DeliveredCount
	}
	if delivered[named.ID] != 1 || delivered[plain.ID] != 4 {
		t.Fatalf("delivered counts: %v", delivered)
	}

	// A failing endpoint records the error; a removed subscription gets nothing.
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/epcis/subscriptions/"+named.ID, nil), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/epcis/subscriptions/"+uuid.NewString(), nil), nil)
	e.Must(http.StatusBadRequest, e.Do("DELETE", "/api/v1/epcis/subscriptions/x", nil), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": "http://127.0.0.1:1/unreachable"}), &failing)
	fail.Store(true)
	before := hits.Load()
	seedQueryEvents(t, e, t0.Add(24*time.Hour))
	e.Drain()
	if hits.Load() != before+1 {
		t.Fatalf("deliveries after delete: %d, want %d", hits.Load(), before+1)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/subscriptions", nil), &subs)
	errs := map[string]string{}
	active := map[string]bool{}
	for _, s := range subs.Items {
		errs[s.ID], active[s.ID] = s.LastError, s.Active
	}
	if active[named.ID] || !strings.Contains(errs[plain.ID], "status=502") || !strings.Contains(errs[failing.ID], "status=0") {
		t.Fatalf("errors after failed delivery: %v active=%v", errs, active)
	}
}

func TestJobPayloadErrorsArePermanent(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	tid := e.Tenant.ID
	var ids []int64
	err := e.App.Deps.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		for _, kind := range []string{epcis.JobNotify, "epcis.deliver"} {
			id, err := jobs.Enqueue(ctx, tx, &tid, kind, "not an object")
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		// A delivery for a subscription that no longer exists is a no-op.
		id, err := jobs.Enqueue(ctx, tx, &tid, "epcis.deliver", map[string]string{"captureId": uuid.NewString(), "subscriptionId": uuid.NewString()})
		ids = append(ids, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Drain()
	want := []string{"failed", "failed", "done"}
	for i, id := range ids {
		var status string
		if err := e.App.Deps.DB.Pool.QueryRow(ctx, `select status from job where id=$1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != want[i] {
			t.Errorf("job %d: %s, want %s", i, status, want[i])
		}
	}
}

func TestCaptureInputErrors(t *testing.T) {
	e := testkit.New(t)
	for name, body := range map[string]string{
		"not json":  "{",
		"not xml":   "<EPCISDocument>",
		"no events": `{"type":"EPCISDocument","schemaVersion":"2.0","epcisBody":{"eventList":[]}}`,
	} {
		if r := e.Do("POST", "/api/v1/epcis/capture", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}

	// EPCIS 1.2 XML from the shared fixtures: structurally valid, but the GTINs are not in master data.
	x, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "epcis", "epcis12-object-aggregation.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var rej struct {
		Title      string
		CaptureID  string
		EventCount int
		Errors     []lib.FieldError
	}
	r := e.Do("POST", "/api/v1/epcis/capture", x, "Content-Type", "application/xml")
	e.Must(http.StatusUnprocessableEntity, r, &rej)
	if r.Header.Get("Content-Type") != "application/problem+json" || rej.EventCount != 3 || len(rej.Errors) != 2 {
		t.Fatalf("rejection: %s %+v", r.Header.Get("Content-Type"), rej)
	}
	for _, fe := range rej.Errors {
		if fe.Index != 0 || !strings.Contains(fe.Message, "unknown GTIN") {
			t.Errorf("error: %+v", fe)
		}
	}
	var stored epcis.Capture
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/capture/"+rej.CaptureID, nil), &stored)
	if stored.Status != "rejected" || stored.Format != "xml" || stored.ErrorCount != 2 || stored.FinishedAt == nil {
		t.Fatalf("stored rejection: %+v", stored)
	}

	// The shared invalid fixture reports every structural problem with its event index.
	inv, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "epcis", "invalid-events.jsonld"))
	if err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/epcis/capture", inv), &rej)
	idx := map[string]int{}
	for _, fe := range rej.Errors {
		idx[fe.Field] = fe.Index
	}
	if idx["action"] != 1 || idx["bizStep"] != 1 || idx["parentID"] != 2 {
		t.Fatalf("structural errors: %+v", rej.Errors)
	}
	var caps struct{ Items []epcis.Capture }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/capture?limit=1", nil), &caps)
	if len(caps.Items) != 1 || caps.Items[0].Format != "json" {
		t.Fatalf("latest capture: %+v", caps.Items)
	}
}

func TestCaptureGateAndInDocumentConflicts(t *testing.T) {
	e := testkit.New(t)
	mod := module(e)
	errQuota := errors.New("quota exceeded")
	var seen int
	mod.Gate = func(_ context.Context, tenant uuid.UUID, events int) error {
		seen = events
		if events > 2 {
			return errQuota
		}
		return nil
	}
	t.Cleanup(func() { mod.Gate = nil })
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	big := []lib.Event{}
	for i := 0; i < 3; i++ {
		o := ev(lib.ObjectEvent, "OBSERVE", "storing", "", t0.Add(time.Duration(i)*time.Minute))
		o.EPCList = []string{ssccA}
		big = append(big, o)
	}
	if r := e.Do("POST", "/api/v1/epcis/capture", doc(big...)); r.Status < 400 || seen != 3 {
		t.Fatalf("gate did not refuse: %d seen=%d", r.Status, seen)
	}

	// Two aggregation events in one document put the same child into two parents.
	p1 := ev(lib.AggregationEvent, "ADD", "packing", "", t0)
	p1.ParentID, p1.ChildEPCs = ssccA, []string{ssccC}
	p2 := ev(lib.AggregationEvent, "ADD", "packing", "", t0.Add(time.Minute))
	p2.ParentID, p2.ChildEPCs = ssccB, []string{ssccC}
	var rej struct{ Errors []lib.FieldError }
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/epcis/capture", doc(p1, p2)), &rej)
	if len(rej.Errors) != 1 || rej.Errors[0].Index != 1 || !strings.Contains(rej.Errors[0].Message, "two parents") {
		t.Fatalf("in-document conflict: %+v", rej.Errors)
	}
}

func TestCaptureErrorMessageAndModuleBasics(t *testing.T) {
	ce := &epcis.CaptureError{Capture: epcis.Capture{ErrorCount: 3}}
	if ce.Error() != "document rejected with 3 errors" {
		t.Fatalf("CaptureError: %q", ce.Error())
	}
	e := testkit.New(t)
	mod := module(e)
	if mod.Name() != "epcis" {
		t.Fatal("name")
	}
	if mod.Archive != nil {
		t.Fatal("no S3 endpoint configured, archive must be nil")
	}
	if s := mod.Schedules(); s != nil {
		t.Fatalf("schedules without archive: %v", s)
	}
	if err := mod.ArchiveOld(context.Background()); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write([]byte("body"))
	if got := epcis.Sign("k", []byte("body")); got != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature: %s", got)
	}
	b, _ := json.Marshal(epcis.Query{"EQ_action": "ADD"})
	if string(b) != `{"EQ_action":"ADD"}` {
		t.Fatalf("query json: %s", b)
	}
}
