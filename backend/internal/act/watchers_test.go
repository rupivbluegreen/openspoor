package act_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

func TestWatchers(t *testing.T) {
	e := testkit.New(t)
	gtin := "09506000134352"
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Amoxicillin 500mg"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "US"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 2}, "Idempotency-Key", "w"), &a)
	u0, _ := gs1.SGTINURN(gtin, a.Serials[0], 7)
	u1, _ := gs1.SGTINURN(gtin, a.Serials[1], 7)
	t0 := time.Now().Add(-2 * time.Hour).UTC()
	ev := func(step, disp string, at time.Time, epcs ...string) lib.Event {
		return lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: step, Disposition: disp, EventTime: at, EventTimeZoneOffset: "+00:00", EPCList: epcs}
	}
	comm := ev("commissioning", "active", t0, u0, u1)
	comm.Action = "ADD"
	comm.ILMD = map[string]any{"lotNumber": "L1", "itemExpirationDate": "2030-01-31"}
	capture := func(evs ...lib.Event) {
		b, _ := lib.WriteJSON(lib.NewDocument(evs))
		e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
		e.Drain()
	}
	run := func(agent string) int {
		var r struct{ Proposed int }
		e.Must(http.StatusOK, e.Do("POST", "/api/v1/agents/"+agent+"/run", nil), &r)
		return r.Proposed
	}

	// Reappeared serial: dispensed, then shipped again.
	capture(comm, ev("dispensing", "dispensed", t0.Add(time.Minute), u0))
	if n := run("reappeared-serial"); n != 0 {
		t.Fatalf("no reappearance yet, proposed %d", n)
	}
	capture(ev("shipping", "in_transit", t0.Add(time.Hour), u0, u1))
	if n := run("reappeared-serial"); n != 1 {
		t.Fatalf("reappeared after dispensing: proposed %d, want 1 (only the dispensed pack)", n)
	}
	if n := run("reappeared-serial"); n != 0 {
		t.Fatalf("same signal proposed twice: %d", n)
	}

	// Verification spike: five public scans of a serial that was never issued.
	never := gs1.DigitalLink("https://brand.example", gs1.Pack{GTIN: gtin, Serial: "NEVERISSUED1"})
	for i := 0; i < 4; i++ {
		e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": never}), nil)
	}
	if n := run("verification-spike"); n != 0 {
		t.Fatalf("4 failed scans is below the threshold, proposed %d", n)
	}
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": never}), nil)
	if n := run("verification-spike"); n != 1 {
		t.Fatalf("spike: proposed %d, want 1", n)
	}

	// Duplicate verification: the same serial verified by two partners.
	var key struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "vrs", "roles": []string{"Integration"}}), &key)
	verify := func(gln string) {
		e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/vrs/verify", map[string]any{"gtin": gtin, "serialNumber": a.Serials[1],
			"lotNumber": "L1", "expirationDate": "300131", "requestorGLN": gln, "corrUUID": "b8a1f2c4-6f0e-4b8e-9d3a-2c1e5f7a9b0d"}, "X-API-Key", key.Secret), nil)
	}
	verify("0614141000005")
	verify("0614141000005")
	if n := run("duplicate-verification"); n != 0 {
		t.Fatalf("one partner verifying twice is not a duplicate, proposed %d", n)
	}
	verify("8712345000004")
	if n := run("duplicate-verification"); n != 1 {
		t.Fatalf("duplicate verification: proposed %d, want 1", n)
	}

	// Proposals wait for a person: approving opens the case.
	var acts struct {
		Items []struct{ ID, Agent, Title string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/agents/actions?status=proposed", nil), &acts)
	if len(acts.Items) != 3 {
		t.Fatalf("pending watcher proposals: %+v", acts.Items)
	}
}
