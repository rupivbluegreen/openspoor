package ecosystem_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

type appListing struct {
	ID        string
	Kind      string
	Verified  bool
	Installed bool
	Version   string
}

func TestMarketplacePublishInstallWebhook(t *testing.T) {
	e := testkit.New(t)
	var page struct {
		Items []appListing
		Total int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/apps?kind=agent", nil), &page)
	if page.Total != 2 || page.Items[0].Kind != "agent" {
		t.Fatalf("kind filter: %+v", page)
	}

	// Built-in ids are protected; community ids can be republished (new version).
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/apps", map[string]any{"id": "sa-rsd", "name": "Fake", "kind": "connector", "publisher": "x", "version": "9"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/apps", "{"), nil)
	for _, v := range []string{"0.1.0", "0.2.0"} {
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps", map[string]any{"id": "acme-hook", "name": "Hooked", "kind": "app", "publisher": "Acme",
			"version": v, "roles": []string{"Viewer"}, "webhook": "https://hooks.example/epcis"}), nil)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/apps?kind=app", nil), &page)
	if page.Total != 1 || page.Items[0].Version != "0.2.0" || page.Items[0].Verified {
		t.Fatalf("community listing: %+v", page)
	}

	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/apps/nope/install", nil), nil)
	var inst struct {
		APIKey string
		App    struct{ ID string }
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps/acme-hook/install", nil), &inst)
	if !strings.HasPrefix(inst.APIKey, "osk_") || inst.App.ID != "acme-hook" {
		t.Fatalf("install: %+v", inst)
	}
	// A Viewer key can read but not write.
	e.Must(http.StatusOK, e.Call(inst.APIKey, "GET", "/api/v1/apps/installed", nil), nil)
	e.Must(http.StatusForbidden, e.Call(inst.APIKey, "POST", "/api/v1/apps/acme-hook/install", nil), nil)

	var subs struct{ Items []struct{ Dest string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/subscriptions", nil), &subs)
	if len(subs.Items) != 1 || subs.Items[0].Dest != "https://hooks.example/epcis" {
		t.Fatalf("webhook subscription: %+v", subs)
	}
	var installed struct {
		Items []struct{ AppID, InstalledBy string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/apps/installed", nil), &installed)
	if len(installed.Items) != 1 || installed.Items[0].AppID != "acme-hook" || installed.Items[0].InstalledBy == "" {
		t.Fatalf("installed: %+v", installed)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/apps?kind=app", nil), &page)
	if !page.Items[0].Installed {
		t.Fatal("listing does not show installed state")
	}
	// The public catalogue has no tenant, so nothing is marked installed.
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/marketplace?kind=app", nil), &page)
	if page.Items[0].Installed {
		t.Fatal("anonymous catalogue shows install state")
	}
	// Installation is per tenant.
	_, tok := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/apps?kind=app", nil), &page)
	if page.Items[0].Installed {
		t.Fatal("install leaked to another tenant")
	}

	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/apps/acme-hook/install", nil), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/apps/acme-hook/install", nil), nil)
	e.Must(http.StatusUnauthorized, e.Call(inst.APIKey, "GET", "/api/v1/apps/installed", nil), nil)
	var audit struct{ Items []map[string]any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=app.", nil), &audit)
	if len(audit.Items) != 2 {
		t.Fatalf("install/uninstall audit entries: %d", len(audit.Items))
	}
}

func TestSensorIngestRules(t *testing.T) {
	e := testkit.New(t)
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	for name, body := range map[string]any{
		"bad json":    "{",
		"no device":   map[string]any{"readings": []map[string]any{{"time": base, "tempC": 5}}},
		"no readings": map[string]any{"deviceId": "d1"},
		"inverted":    map[string]any{"deviceId": "d1", "minC": 10, "maxC": 2, "readings": []map[string]any{{"time": base, "tempC": 5}}},
	} {
		if r := e.Do("POST", "/api/v1/sensors/readings", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}

	// Frozen-food range, no monitored object: an excursion is stored but no EPCIS event.
	var res struct {
		Stored     int
		Excursions []struct {
			DeviceID string
			EPC      *string
			PeakC    float64
			MinC     float64
			MaxC     float64
			Status   string
		}
	}
	readings := []map[string]any{{"time": base, "tempC": -20}, {"time": base.Add(10 * time.Minute), "tempC": -12}, {"time": base.Add(20 * time.Minute), "tempC": -19}, {"time": base.Add(30 * time.Minute), "humidity": 40}}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "freezer-1", "minC": -25, "maxC": -15, "readings": readings}), &res)
	if res.Stored != 4 || len(res.Excursions) != 1 || res.Excursions[0].PeakC != -12 || res.Excursions[0].EPC != nil || res.Excursions[0].MinC != -25 || res.Excursions[0].Status != "open" {
		t.Fatalf("frozen excursion: %+v", res)
	}
	// Re-sending the same readings is idempotent and yields no new events; in range means no excursion.
	ok := []map[string]any{{"time": base.Add(time.Hour), "tempC": 5}}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "fridge-2", "epc": "urn:epc:id:sscc:9506000.0000000024", "readings": ok}), &res)
	if res.Excursions == nil || len(res.Excursions) != 0 {
		t.Fatalf("in-range: %+v", res)
	}
	// Humidity-only readings on a monitored object: no temperatures, so no sensor event.
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/sensors/readings", map[string]any{"deviceId": "hygro", "epc": "urn:epc:id:sscc:9506000.0000000031",
		"readings": []map[string]any{{"time": base, "humidity": 55}}}), nil)
	e.Drain()

	var list struct {
		Items []struct{ DeviceID string }
		Total int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/sensors/excursions", nil), &list)
	if list.Total != 1 || list.Items[0].DeviceID != "freezer-1" {
		t.Fatalf("excursions: %+v", list)
	}
	var q struct {
		EpcisBody struct {
			QueryResults struct {
				ResultsBody struct{ EventList []lib.Event }
			}
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/epcis/events?EQ_bizStep=sensor_reporting", nil), &q)
	evs := q.EpcisBody.QueryResults.ResultsBody.EventList
	if len(evs) != 1 || !strings.Contains(string(evs[0].SensorElementList), `"meanValue":5`) {
		t.Fatalf("sensor events: %+v", evs)
	}
	var alerts struct{ Items []struct{ Title string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=coldchain.", nil), &alerts)
	if len(alerts.Items) != 1 || !strings.Contains(alerts.Items[0].Title, "freezer-1") || !strings.Contains(alerts.Items[0].Title, "-12.0") {
		t.Fatalf("alerts: %+v", alerts)
	}
}

func TestUDIDevices(t *testing.T) {
	e := testkit.New(t)
	for name, body := range map[string]any{
		"bad json":   "{",
		"bad di":     map[string]any{"di": "124", "brandName": "B", "labeler": "L", "riskClass": "EU-I"},
		"no brand":   map[string]any{"di": "09506000134352", "labeler": "L", "riskClass": "EU-I"},
		"no labeler": map[string]any{"di": "09506000134352", "brandName": "B", "riskClass": "EU-I"},
		"bad class":  map[string]any{"di": "09506000134352", "brandName": "B", "labeler": "L", "riskClass": "EU-IV"},
	} {
		if r := e.Do("POST", "/api/v1/udi/devices", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}
	var eu, partial struct{ ID, DI string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/udi/devices", map[string]any{"di": "9506000134352", "brandName": "Stent X", "model": "SX-1", "riskClass": "EU-III",
		"labeler": "Acme Med", "basicUdiDi": "0950600013435XYZ", "emdnCode": "P0704", "singleUse": true}), &eu)
	if eu.DI != "09506000134352" {
		t.Fatalf("di not normalized: %+v", eu)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/udi/devices", map[string]any{"di": "10614141073464", "brandName": "Bandage", "riskClass": "EU-I", "labeler": "Acme Med"}), &partial)

	var sub struct {
		Payload    string
		Submission map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/udi/devices/"+eu.ID+"/submit?target=EUDAMED", nil), &sub)
	for _, frag := range []string{"<eudamedDeviceRegistration>", "<basicUdiDi>0950600013435XYZ</basicUdiDi>", "<udiDi>09506000134352</udiDi>", "<riskClass>EU-III</riskClass>", "<singleUse>true</singleUse>"} {
		if !strings.Contains(sub.Payload, frag) {
			t.Errorf("eudamed payload lacks %s:\n%s", frag, sub.Payload)
		}
	}
	if sub.Submission["status"] != "prepared_stub" || !strings.HasPrefix(fmt.Sprint(sub.Submission["ref"]), "LOCAL-EUDAMED-") {
		t.Fatalf("submission: %+v", sub.Submission)
	}

	var prob struct {
		Errors []string
	}
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/udi/devices/"+partial.ID+"/submit?target=eudamed", nil), &prob)
	if strings.Join(prob.Errors, ",") != "basicUdiDi,emdnCode" {
		t.Fatalf("eudamed missing fields: %+v", prob)
	}
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/udi/devices/"+eu.ID+"/submit?target=gudid", nil), &prob)
	if len(prob.Errors) != 2 || !strings.Contains(prob.Errors[0], "US class") || prob.Errors[1] != "gmdnCode" {
		t.Fatalf("gudid on EU device: %+v", prob)
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/udi/devices/"+eu.ID+"/submit?target=pmda", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/udi/devices/x/submit?target=gudid", nil), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/udi/devices/00000000-0000-0000-0000-000000000001/submit?target=gudid", nil), nil)

	var list struct {
		Items []struct {
			BrandName   string
			Submissions map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/udi/devices", nil), &list)
	if len(list.Items) != 2 || list.Items[0].BrandName != "Bandage" || list.Items[1].Submissions["eudamed"] == nil || list.Items[1].Submissions["gudid"] != nil {
		t.Fatalf("devices: %+v", list)
	}
}

func TestLearnTagFilter(t *testing.T) {
	e := testkit.New(t)
	var learn struct {
		Items []struct {
			Slug string
			Tags []string
			Body string
		}
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/learn?tag=food", nil), &learn)
	if len(learn.Items) == 0 {
		t.Fatal("no food articles")
	}
	for _, a := range learn.Items {
		if a.Body != "" {
			t.Errorf("%s: list includes body", a.Slug)
		}
		found := false
		for _, tg := range a.Tags {
			found = found || tg == "food"
		}
		if !found {
			t.Errorf("%s lacks tag food: %v", a.Slug, a.Tags)
		}
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/learn?tag=nonexistent", nil), &learn)
	if len(learn.Items) != 0 {
		t.Fatalf("unknown tag: %+v", learn.Items)
	}
	e.Must(http.StatusNotFound, e.Call("", "GET", "/api/v1/learn/missing", nil), nil)
}

// With at least five other tenants reporting, peer figures are published.
func TestBenchmarkPeersAboveKAnonymityThreshold(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	if _, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "ops", Name: "Ops", AdminEmail: "ops@x.test", AdminPassword: testkit.Password, Operator: true}); err != nil {
		t.Fatal(err)
	}
	var op struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]any{"tenant": "ops", "email": "ops@x.test", "password": testkit.Password}), &op)
	for name, body := range map[string]any{
		"bad json":       "{",
		"no time":        map[string]any{"environment": "x", "results": map[string]any{"a": 1}},
		"no environment": map[string]any{"measuredAt": time.Now(), "results": map[string]any{"a": 1}},
		"no results":     map[string]any{"measuredAt": time.Now(), "environment": "x"},
	} {
		if r := e.Call(op.Token, "POST", "/api/v1/benchmarks/runs", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}

	obs := func(i int) []byte {
		ev := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "storing", EventTime: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC), EventTimeZoneOffset: "+00:00",
			EPCList: []string{"urn:epc:id:sscc:9506000.0000000017"}}
		b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{ev}))
		return b
	}
	bad := `{"type":"EPCISDocument","schemaVersion":"2.0","epcisBody":{"eventList":[{"type":"ObjectEvent","action":"JUMP","eventTime":"2026-01-01T00:00:00Z","eventTimeZoneOffset":"+00:00","epcList":["urn:x:1"]}]}}`
	// Peer i: one accepted capture and i rejected ones → 0, 50, 66.7, 75, 80 % rejected.
	for i := 0; i < 5; i++ {
		_, tok := e.NewTenant(fmt.Sprintf("peer%d", i), fmt.Sprintf("a@peer%d.test", i))
		e.Must(http.StatusAccepted, e.Call(tok, "POST", "/api/v1/epcis/capture", obs(i)), nil)
		for j := 0; j < i; j++ {
			e.Must(http.StatusUnprocessableEntity, e.Call(tok, "POST", "/api/v1/epcis/capture", bad), nil)
		}
	}
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", obs(9)), nil)

	var bm struct {
		MinPeers int
		KPIs     []struct {
			Key        string
			Yours      *float64
			PeerMedian *float64
			PeerP90    *float64
			Peers      int
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/benchmarks", nil), &bm)
	if bm.MinPeers != 5 || len(bm.KPIs) != 3 {
		t.Fatalf("benchmarks: %+v", bm)
	}
	k := bm.KPIs[0]
	if k.Key != "capture_rejection_pct" || k.Peers != 5 || k.Yours == nil || *k.Yours != 0 || k.PeerMedian == nil || k.PeerP90 == nil {
		t.Fatalf("rejection KPI: %+v", k)
	}
	if *k.PeerMedian < 66.6 || *k.PeerMedian > 66.7 || *k.PeerP90 < 77.9 || *k.PeerP90 > 78.1 {
		t.Fatalf("peer median %.2f p90 %.2f", *k.PeerMedian, *k.PeerP90)
	}
	if bm.KPIs[1].PeerMedian != nil || bm.KPIs[2].PeerMedian != nil {
		t.Fatalf("KPIs without peer data must stay hidden: %+v", bm.KPIs[1:])
	}
}

func TestMarketplaceOptionalAuthAndCreatedAt(t *testing.T) {
	e := testkit.New(t)
	var created struct {
		ID        string
		CreatedAt time.Time
		Verified  bool
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps", map[string]any{"id": "acme-ts", "name": "TS", "kind": "app", "publisher": "Acme", "version": "1"}), &created)
	if created.CreatedAt.Year() < 2000 || time.Since(created.CreatedAt) > time.Hour || created.Verified {
		t.Fatalf("created listing: %+v", created)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps", map[string]any{"id": "acme-ts", "name": "TS", "kind": "app", "publisher": "Acme", "version": "2", "roles": []string{"Viewer"}}), &created)
	if created.CreatedAt.Year() < 2000 {
		t.Fatalf("republished listing: %+v", created)
	}
	var key struct{ APIKey string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps/acme-ts/install", nil), &key)

	installed := func(r testkit.Response) bool {
		t.Helper()
		var page struct{ Items []appListing }
		e.Must(http.StatusOK, r, &page)
		for _, a := range page.Items {
			if a.ID == "acme-ts" {
				return a.Installed
			}
		}
		t.Fatal("listing missing")
		return false
	}
	if installed(e.Call("", "GET", "/api/v1/marketplace", nil)) {
		t.Fatal("anonymous request must not see installs")
	}
	if !installed(e.Do("GET", "/api/v1/marketplace", nil)) {
		t.Fatal("bearer token should report installs")
	}
	if !installed(e.Call(key.APIKey, "GET", "/api/v1/marketplace", nil)) {
		t.Fatal("API key should report installs")
	}
	// Bad credentials do not break the public catalogue; another tenant sees no installs.
	if installed(e.Call("garbage", "GET", "/api/v1/marketplace", nil)) {
		t.Fatal("invalid token is anonymous")
	}
	_, other := e.NewTenant("other-mkt", "a@other-mkt.test")
	if installed(e.Call(other, "GET", "/api/v1/marketplace", nil)) {
		t.Fatal("installs are per tenant")
	}
}

func TestInstallAppWithoutRoles(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps", map[string]any{"id": "no-roles", "name": "Reader", "kind": "app", "publisher": "Acme", "version": "1"}), nil)
	var inst struct{ APIKey string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/apps/no-roles/install", nil), &inst)
	// The key exists but carries no role: it cannot read the tenant's data.
	e.Must(http.StatusForbidden, e.Call(inst.APIKey, "GET", "/api/v1/products", nil), nil)
}
