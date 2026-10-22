package ecosystem_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

func TestMarketplaceSandboxColdChainUDI(t *testing.T) {
	e := testkit.New(t)

	// Marketplace: built-ins listed publicly; install grants a scoped key.
	var apps struct {
		Items []struct {
			ID        string
			Verified  bool
			Installed bool
		}
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/marketplace", nil), &apps)
	if len(apps.Items) != 6 || !apps.Items[0].Verified {
		t.Fatalf("marketplace: %+v", apps.Items)
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/apps", map[string]any{"id": "evil", "name": "Evil", "kind": "app", "publisher": "x", "version": "1", "roles": []string{"Admin"}}), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps", map[string]any{"id": "acme-erp-sync", "name": "ERP sync", "kind": "integration", "publisher": "Acme IT", "version": "0.1.0",
		"description": "Pushes shipments to an ERP", "roles": []string{"Integration"}}), nil)
	var inst struct{ APIKey string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps/acme-erp-sync/install", nil), &inst)
	e.Must(http.StatusOK, e.Call(inst.APIKey, "GET", "/api/v1/epcis/events", nil), nil)
	e.Must(http.StatusForbidden, e.Call(inst.APIKey, "POST", "/api/v1/users", map[string]any{"email": "x@y.z", "roles": []string{"Admin"}}), nil)
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/apps/acme-erp-sync/install", nil), nil)
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/apps/acme-erp-sync/install", nil), nil)
	e.Must(http.StatusUnauthorized, e.Call(inst.APIKey, "GET", "/api/v1/epcis/events", nil), nil)

	// Developer sandbox: an isolated tenant with sample data.
	var sb struct{ Tenant, APIKey string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps/sandbox", nil), &sb)
	var st struct{ Events int }
	e.Must(http.StatusOK, e.Call(sb.APIKey, "GET", "/api/v1/trace/stats", nil), &st)
	if !strings.HasPrefix(sb.Tenant, "sandbox-") || st.Events != 1 {
		t.Fatalf("sandbox: %+v events=%d", sb, st.Events)
	}

	// Cold chain: one excursion above 8 °C, recorded as an alert and an EPCIS sensor event.
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	var readings []map[string]any
	for i, c := range []float64{4, 5, 9.5, 11.2, 7, 5, 4} {
		readings = append(readings, map[string]any{"time": base.Add(time.Duration(i) * 10 * time.Minute), "tempC": c})
	}
	var ing struct {
		Stored     int
		Excursions []struct {
			PeakC   float64
			Minutes int
		}
	}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "logger-7", "epc": "urn:epc:id:sscc:9506000.0000000017", "readings": readings}), &ing)
	if ing.Stored != 7 || len(ing.Excursions) != 1 || ing.Excursions[0].PeakC != 11.2 || ing.Excursions[0].Minutes != 20 {
		t.Fatalf("excursions: %+v", ing)
	}
	e.Drain()
	var q struct {
		EpcisBody struct {
			QueryResults struct {
				ResultsBody struct{ EventList []map[string]any }
			}
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?EQ_bizStep=sensor_reporting", nil), &q)
	if len(q.EpcisBody.QueryResults.ResultsBody.EventList) != 1 || q.EpcisBody.QueryResults.ResultsBody.EventList[0]["sensorElementList"] == nil {
		t.Fatalf("sensor event: %+v", q)
	}

	// UDI: GUDID submission needs a GMDN code and model.
	var d struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/udi/devices", map[string]any{"di": "09506000134352", "brandName": "FlowSafe", "riskClass": "US-II", "labeler": "Acme Devices"}), &d)
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/udi/devices/"+d.ID+"/submit?target=gudid", nil), nil)
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/udi/devices/"+d.ID+"/submit?target=eudamed", nil), nil)
	var d2 struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/udi/devices", map[string]any{"di": "10614141073464", "brandName": "FlowSafe Pro", "model": "FS-200", "gmdnCode": "35151",
		"riskClass": "US-II", "labeler": "Acme Devices", "sterile": true}), &d2)
	var sub struct {
		Payload    string
		Submission map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/udi/devices/"+d2.ID+"/submit?target=gudid", nil), &sub)
	if !strings.Contains(sub.Payload, "<primaryDI>10614141073464</primaryDI>") || sub.Submission["status"] != "prepared_stub" {
		t.Fatalf("gudid: %+v", sub)
	}

	// Learning hub (public).
	var learn struct {
		Items []struct{ Slug, Title string }
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/learn", nil), &learn)
	if len(learn.Items) != 6 {
		t.Fatalf("learn: %+v", learn.Items)
	}
	var art struct{ Title, Body string }
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/learn/04-fsma-204", nil), &art)
	if art.Title == "" || !strings.Contains(art.Body, "24 hours") {
		t.Fatalf("article: %+v", art)
	}
	e.Must(http.StatusNotFound, e.Call("", "GET", "/api/v1/learn/..%2Fecosystem", nil), nil)

	// Benchmarks: operator records a measured run; tenants see KPIs with k-anonymity.
	_, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "ops", Name: "Ops", AdminEmail: "ops@x.test", AdminPassword: testkit.Password, Operator: true})
	if err != nil {
		t.Fatal(err)
	}
	var op struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "ops", "email": "ops@x.test", "password": testkit.Password}), &op)
	e.Must(http.StatusForbidden, e.Do("POST", "/api/v1/benchmarks/runs", map[string]any{}), nil)
	e.Must(http.StatusCreated, e.Call(op.Token, "POST", "/api/v1/benchmarks/runs", map[string]any{"measuredAt": time.Now().UTC(), "environment": "4 vCPU", "version": "test",
		"results": map[string]any{"captureEventsPerSec": 14000}}), nil)
	var sys struct{ Items []map[string]any }
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/benchmarks/system", nil), &sys)
	if len(sys.Items) != 1 {
		t.Fatalf("system benchmarks: %+v", sys)
	}
	var bm struct {
		KPIs []struct {
			Key        string
			Yours      *float64
			PeerMedian *float64
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/benchmarks", nil), &bm)
	if len(bm.KPIs) != 3 || bm.KPIs[0].PeerMedian != nil {
		t.Fatalf("peer KPIs must be hidden below 5 peers: %+v", bm.KPIs)
	}
}
