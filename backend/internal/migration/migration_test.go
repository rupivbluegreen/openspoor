package migration_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

func TestMigrationAndShadow(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Legacy product"}), &p)
	// A pool exists for the GTIN; legacy serials still import (they were issued by the old system).
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}), nil)
	csv := "gtin,serial,lot,expiry,status,parent\n" +
		gtin + ",LEG0001,L1,2030-01-31,active,urn:epc:id:sscc:9506000.0000000017\n" +
		gtin + ",LEG0002,L1,2030-01-31,active,urn:epc:id:sscc:9506000.0000000017\n" +
		gtin + ",LEG0003,L1,2030-01-31,dispensed,\n" +
		gtin + ",LEG0004,L1,2030-01-31,decommissioned,\n" +
		"09506000134353,BAD,,,,\n"
	var res struct {
		Rows, Events int
		Errors       []string
		CaptureIDs   []string
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/migration/serials", csv, "Content-Type", "text/csv"), &res)
	if res.Rows != 4 || len(res.Errors) != 1 || len(res.CaptureIDs) != 1 {
		t.Fatalf("import: %+v", res)
	}
	e.Drain()
	var st struct {
		State struct {
			Status    string
			ParentEPC *string
		}
	}
	u3, _ := gs1.SGTINURN(gtin, "LEG0003", 7)
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/serials/?id="+u3, nil), &st)
	if st.State.Status != "dispensed" {
		t.Fatalf("legacy status: %+v", st)
	}
	// Re-importing the same serials is refused: serials are never reissued.
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/migration/serials", "gtin,serial,lot\n"+gtin+",LEG0001,L2\n", "Content-Type", "text/csv"), nil)

	// Shadow mode: the legacy system says LEG0002 was destroyed and LEG0099 exists; we disagree.
	t0 := time.Now().UTC().Add(time.Hour)
	u2, _ := gs1.SGTINURN(gtin, "LEG0002", 7)
	u1, _ := gs1.SGTINURN(gtin, "LEG0001", 7)
	u99, _ := gs1.SGTINURN(gtin, "LEG0099", 7)
	legacy := lib.NewDocument([]lib.Event{
		{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "destroying", Disposition: "destroyed", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: []string{u2}},
		{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "storing", Disposition: "active", EventTime: t0, EventTimeZoneOffset: "+00:00", EPCList: []string{u1, u99}},
	})
	b, _ := lib.WriteXML(legacy)
	var run struct {
		ID           string
		Compared     int
		Matched      int
		AgreementPct float64
		Mismatches   []struct{ EPC, Field, Legacy, OpenSpoor string }
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shadow/runs", map[string]any{"label": "week 1", "legacyDocument": string(b)}), &run)
	if run.Compared != 3 || run.Matched != 1 || len(run.Mismatches) != 2 {
		t.Fatalf("shadow: %+v", run)
	}
	found := map[string]bool{}
	for _, m := range run.Mismatches {
		found[m.Field] = true
		if m.Field == "status" && (m.Legacy != "destroyed" || m.OpenSpoor != "active") {
			t.Fatalf("status mismatch: %+v", m)
		}
	}
	if !found["status"] || !found["exists"] || !strings.Contains(run.Mismatches[0].EPC, "id.gs1.org") {
		t.Fatalf("mismatch kinds: %+v", run.Mismatches)
	}
	var al struct{ Items []any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=migration.", nil), &al)
	if len(al.Items) != 1 {
		t.Fatal("no shadow alert")
	}
}
