package link_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openspoor/openspoor/internal/testkit"
)

type harnessResult struct {
	Run struct {
		Channel string
		Passed  bool
		Detail  map[string]any
	}
	Onboarding struct{ Score int }
}

func runTest(t *testing.T, e *testkit.Env, partnerID, channel string) harnessResult {
	t.Helper()
	var res harnessResult
	body := map[string]any{"partnerId": partnerID}
	if channel != "" {
		body["channel"] = channel
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/onboarding/tests", body), &res)
	return res
}

func TestOnboardingTestHarness(t *testing.T) {
	e := testkit.New(t)
	tok, local := linkTenants(t, e, "globex")
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners/"+local+"/onboarding", map[string]any{"template": "b2b-edi"}), nil)
	res := runTest(t, e, local, "")
	if !res.Run.Passed || res.Run.Channel != "local" || res.Onboarding.Score != 20 {
		t.Fatalf("local: %+v", res)
	}
	// The other side suspends the connection: the test fails and the score drops.
	var theirs struct{ Items []struct{ ID string } }
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/partners", nil), &theirs)
	e.Must(http.StatusOK, e.Call(tok, "POST", "/api/v1/partners/"+theirs.Items[0].ID+"/suspend", nil), nil)
	res = runTest(t, e, local, "local")
	if res.Run.Passed || !strings.Contains(res.Run.Detail["error"].(string), "suspended") || res.Onboarding.Score != 0 {
		t.Fatalf("suspended: %+v", res)
	}

	// EPCIS endpoint over HTTP.
	status := http.StatusAccepted
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer stub.Close()
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "HTTP Partner", "endpoints": map[string]string{"epcisCapture": stub.URL}}), &p)
	if res = runTest(t, e, p.ID, ""); !res.Run.Passed || res.Run.Channel != "epcis" {
		t.Fatalf("epcis: %+v", res)
	}
	status = http.StatusBadRequest
	if res = runTest(t, e, p.ID, ""); res.Run.Passed {
		t.Fatalf("epcis rejected: %+v", res)
	}
	stub.Close()
	if res = runTest(t, e, p.ID, "epcis"); res.Run.Passed {
		t.Fatalf("epcis down: %+v", res)
	}

	// No channel, AS2, and a non-local partner on the local channel.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Nothing"}), &p)
	if res = runTest(t, e, p.ID, ""); res.Run.Passed || res.Run.Channel != "none" {
		t.Fatalf("none: %+v", res)
	}
	if res = runTest(t, e, p.ID, "local"); res.Run.Passed {
		t.Fatalf("local without linked tenant: %+v", res)
	}
	if res = runTest(t, e, p.ID, "federation"); res.Run.Passed {
		t.Fatalf("federation without peer: %+v", res)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "AS2", "endpoints": map[string]string{"as2Url": "https://as2.example", "as2Id": "X"}}), &p)
	if res = runTest(t, e, p.ID, ""); res.Run.Passed || res.Run.Channel != "as2" {
		t.Fatalf("as2: %+v", res)
	}

	// Validation and roles.
	problem(t, e.Do("POST", "/api/v1/onboarding/tests", map[string]any{"partnerId": p.ID, "channel": "fax"}), 400, "channel must be")
	problem(t, e.Do("POST", "/api/v1/onboarding/tests", map[string]any{"partnerId": "00000000-0000-0000-0000-000000000001"}), 400, "unknown partnerId")
	problem(t, e.Do("POST", "/api/v1/onboarding/tests", "{"), 400, "invalid JSON")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(viewer, "POST", "/api/v1/onboarding/tests", map[string]any{"partnerId": p.ID}), 403, "missing role")
	problem(t, e.Call(tok, "POST", "/api/v1/onboarding/tests", map[string]any{"partnerId": p.ID}), 400, "unknown partnerId")
}

func TestOnboardingFederationTest(t *testing.T) {
	a, b := testkit.New(t), testkit.New(t)
	linkOf(a)
	linkOf(b)
	aToB := peer(t, a, b)
	var p struct{ ID string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/partners", map[string]any{"name": "Node B", "endpoints": map[string]string{"federation": aToB}}), &p)
	// Node B has not approved node A yet: the ping is refused.
	if res := runTest(t, a, p.ID, ""); res.Run.Passed || res.Run.Channel != "federation" {
		t.Fatalf("unapproved: %+v", res)
	}
	_ = peer(t, b, a)
	if res := runTest(t, a, p.ID, ""); !res.Run.Passed {
		t.Fatalf("federation: %+v", res)
	}
	var msgs struct {
		Items []struct{ Type, Status string }
	}
	a.Must(http.StatusOK, a.Do("GET", "/api/v1/federation/messages", nil), &msgs)
	if len(msgs.Items) != 2 || msgs.Items[0].Status != "delivered" {
		t.Fatalf("test pings in the message log: %+v", msgs)
	}
	// A pending peer.
	var pend struct{ ID string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "pending", "url": b.Server.URL, "remoteTenant": "other"}), &pend)
	a.Must(http.StatusOK, a.Do("PUT", "/api/v1/partners/"+p.ID, map[string]any{"name": "Node B", "endpoints": map[string]string{"federation": pend.ID}}), nil)
	if res := runTest(t, a, p.ID, ""); res.Run.Passed {
		t.Fatalf("pending peer: %+v", res)
	}
	a.Must(http.StatusOK, a.Do("PUT", "/api/v1/partners/"+p.ID, map[string]any{"name": "Node B", "endpoints": map[string]string{"federation": "not-a-uuid"}}), nil)
	if res := runTest(t, a, p.ID, ""); res.Run.Passed {
		t.Fatalf("bad peer id: %+v", res)
	}
}
