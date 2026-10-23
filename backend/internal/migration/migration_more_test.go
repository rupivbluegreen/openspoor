package migration_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/migration"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

type importResult struct {
	Rows, Events int
	Errors       []string
	CaptureIDs   []string
}

type shadowRun struct {
	ID           string
	Label        string
	Compared     int
	Matched      int
	AgreementPct float64
	Mismatches   []migration.Mismatch
}

func TestSerialCSVValidation(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Legacy"}), nil)

	for name, body := range map[string]string{"empty": "", "no gtin": "serial\nA\n", "no serial": "gtin\n" + gtin + "\n"} {
		if r := e.Do("POST", "/api/v1/migration/serials", body, "Content-Type", "text/csv"); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}

	// Every row invalid: nothing to capture, still a 200 with the line errors.
	csv := "\xef\xbb\xbfGTIN,Serial,expiry,event_time\n" +
		gtin + ",,,\n" +
		gtin + ",ABCDEFGHIJKLMNOPQRSTU,,\n" +
		gtin + ",S1,31/01/2030,\n" +
		gtin + ",S2,,yesterday\n" +
		"\"broken,\n"
	var res importResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/migration/serials", csv, "Content-Type", "text/csv"), &res)
	if res.Rows != 0 || res.Events != 0 || len(res.CaptureIDs) != 0 || len(res.Errors) != 5 {
		t.Fatalf("all-invalid import: %+v", res)
	}
	for i, frag := range []string{"line 2: invalid gtin or serial", "line 3: invalid gtin or serial", "line 4: expiry", "line 5: event_time", "line 6:"} {
		if !strings.HasPrefix(res.Errors[i], frag) && !strings.Contains(res.Errors[i], frag) {
			t.Errorf("error %d = %q, want %q", i, res.Errors[i], frag)
		}
	}

	// Unknown GTIN: the capture is rejected as a whole and reported as 422.
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/migration/serials", "gtin,serial\n00614141000012,X1\n", "Content-Type", "text/csv"), &res)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "unknown GTIN") {
		t.Fatalf("unknown gtin: %+v", res)
	}
}

func TestSerialCSVStatusesAndTimes(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Legacy"}), nil)
	at := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	csv := "gtin,serial,lot,expiry,status,parent,event_time\n" +
		gtin + ",R1,L9,,recalled,," + at.Format(time.RFC3339) + "\n" +
		gtin + ",X1,,2031-12-31,expired,," + at.Format(time.RFC3339) + "\n" +
		gtin + ",D1,,,destroyed,," + at.Add(time.Hour).Format(time.RFC3339) + "\n" +
		gtin + ",A1,,,,,\n"
	var res importResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/migration/serials", csv, "Content-Type", "text/csv"), &res)
	// Commissioning groups: (L9, at), (expiry, at), (at+1h), (now) plus three final-status events.
	if res.Rows != 4 || res.Events != 7 || len(res.Errors) != 0 || len(res.CaptureIDs) != 1 {
		t.Fatalf("import: %+v", res)
	}
	e.Drain()
	want := map[string]string{"R1": "recalled", "X1": "expired", "D1": "destroyed", "A1": "active"}
	for serial, status := range want {
		u, _ := gs1.SGTINURN(gtin, serial, 7)
		var st struct{ State struct{ Status string } }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/?id="+u, nil), &st)
		if st.State.Status != status {
			t.Errorf("%s: status %q, want %q", serial, st.State.Status, status)
		}
	}
	// Migrated events carry the marker extension and the lot from the CSV.
	var q struct {
		EpcisBody struct {
			QueryResults struct {
				ResultsBody struct{ EventList []lib.Event }
			}
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?EQ_bizStep=commissioning", nil), &q)
	evs := q.EpcisBody.QueryResults.ResultsBody.EventList
	if len(evs) != 4 {
		t.Fatalf("commissioning events: %d", len(evs))
	}
	lots := 0
	for _, ev := range evs {
		if ev.Extensions["openspoor:migrated"] != true {
			t.Fatalf("missing migration marker: %+v", ev)
		}
		if ev.ILMD["lotNumber"] == "L9" {
			lots++
		}
	}
	if lots != 1 {
		t.Fatalf("lot ILMD on %d events", lots)
	}
}

func TestEPCISImportAndShadowRuns(t *testing.T) {
	e := testkit.New(t)
	// The 1.2 fixture commissions two serials of this GTIN and packs them into an SSCC.
	gtin := "10614141073464"
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Fixture product"}), nil)
	x, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "epcis", "epcis12-object-aggregation.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		CaptureID  string
		EventCount int
		Source     string
	}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/migration/epcis", x, "Content-Type", "application/xml"), &c)
	if c.EventCount != 3 || c.Source != "migration:epcis" {
		t.Fatalf("epcis import: %+v", c)
	}
	e.Drain()

	// Importing the same serials as fresh commissioning again is refused (never reissued).
	again := strings.Replace(string(x), "2024-03-01T09:00:00.000+01:00", "2024-03-01T09:30:00.000+01:00", 1)
	var rej struct {
		Title  string
		Errors []lib.FieldError
	}
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/migration/epcis", again, "Content-Type", "application/xml"), &rej)
	if len(rej.Errors) != 2 || !strings.Contains(rej.Errors[0].Message, "already exists") {
		t.Fatalf("re-import: %+v", rej)
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/migration/epcis", "<nope", "Content-Type", "application/xml"), nil)

	// Shadow run: legacy says serial 1001 sits in another case and 1002 was unpacked.
	u1, _ := gs1.SGTINURN(gtin, "1001", 7)
	u2, _ := gs1.SGTINURN(gtin, "1002", 7)
	t0 := time.Date(2024, 3, 3, 0, 0, 0, 0, time.UTC)
	legacy := lib.NewDocument([]lib.Event{
		{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", EventTime: t0, EventTimeZoneOffset: "+00:00", ParentID: "urn:epc:id:sscc:0614141.9999999999", ChildEPCs: []string{u1}},
		{Type: lib.AggregationEvent, Action: "DELETE", BizStep: "unpacking", EventTime: t0, EventTimeZoneOffset: "+00:00", ParentID: "urn:epc:id:sscc:0614141.1234567890", ChildEPCs: []string{u2}},
	})
	b, _ := lib.WriteJSON(legacy)
	var run shadowRun
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shadow/runs", map[string]any{"legacyDocument": string(b)}), &run)
	// Compared: both serials plus both SSCCs; only the original case agrees.
	if !strings.HasPrefix(run.Label, "shadow ") || run.Compared != 4 || run.Matched != 1 || run.AgreementPct != 25 || len(run.Mismatches) != 3 {
		t.Fatalf("parent shadow run: %+v", run)
	}
	ours := lib.Canonical("urn:epc:id:sscc:0614141.1234567890")
	byEPC := map[string]migration.Mismatch{}
	for _, m := range run.Mismatches {
		byEPC[m.EPC] = m
	}
	if m := byEPC[lib.Canonical(u1)]; m.Field != "parent" || m.Legacy != lib.Canonical("urn:epc:id:sscc:0614141.9999999999") || m.Ours != ours {
		t.Fatalf("moved child: %+v", m)
	}
	if m := byEPC[lib.Canonical(u2)]; m.Field != "parent" || m.Legacy != "" || m.Ours != ours {
		t.Fatalf("unpacked child: %+v", m)
	}
	if m := byEPC[lib.Canonical("urn:epc:id:sscc:0614141.9999999999")]; m.Field != "exists" {
		t.Fatalf("unknown case: %+v", m)
	}

	// A legacy document that agrees with us: 100% agreement, no alert.
	agree := lib.NewDocument([]lib.Event{{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "storing", Disposition: "active", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: []string{u1, u2}}})
	b, _ = lib.WriteJSON(agree)
	var ok shadowRun
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shadow/runs", map[string]any{"label": "clean", "legacyDocument": string(b)}), &ok)
	if ok.Compared != 2 || ok.Matched != 2 || ok.AgreementPct != 100 || ok.Mismatches == nil || len(ok.Mismatches) != 0 {
		t.Fatalf("agreeing run: %+v", ok)
	}
	var al struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=migration.", nil), &al)
	if len(al.Items) != 1 {
		t.Fatalf("alerts: %d, want 1 (only the mismatching run)", len(al.Items))
	}

	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shadow/runs", map[string]any{"legacyDocument": "not a document"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/shadow/runs", "{"), nil)

	var list struct {
		Items []shadowRun
		Total int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/shadow/runs", nil), &list)
	if list.Total != 2 || list.Items[0].Label != "clean" || list.Items[0].AgreementPct != 100 || len(list.Items[1].Mismatches) != 0 {
		t.Fatalf("list (mismatches omitted): %+v", list)
	}
	var got shadowRun
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/shadow/runs/"+run.ID, nil), &got)
	if got.ID != run.ID || len(got.Mismatches) != 3 || got.Compared != 4 {
		t.Fatalf("get run: %+v", got)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/shadow/runs/00000000-0000-0000-0000-000000000001", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/shadow/runs/abc", nil), nil)
	if (&migration.Module{}).Name() != "migration" {
		t.Fatal("name")
	}
}
