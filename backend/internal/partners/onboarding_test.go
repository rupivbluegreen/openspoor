package partners_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/testkit"
)

type onboarding struct {
	Started  bool
	Template string
	Score    int
	Items    []struct {
		ID, Key, Kind, Note, DoneBy string
		Done                        bool
	}
	Tests []struct {
		Channel string
		Passed  bool
	}
}

func TestOnboardingScore(t *testing.T) {
	items := []partners.OnboardingItem{{Kind: "manual", Done: true}, {Kind: "manual"}, {Kind: "test", Channel: "federation"}, {Kind: "test"}}
	if s := partners.Score(nil, nil); s != 0 {
		t.Fatalf("empty: %d", s)
	}
	if s := partners.Score(items, nil); s != 25 {
		t.Fatalf("one manual: %d", s)
	}
	tests := []partners.TestRun{{Channel: "federation", Passed: true}, {Channel: "local", Passed: false}}
	if s := partners.Score(items, tests); s != 75 {
		t.Fatalf("latest federation test passed: %d", s)
	}
	// The latest run on the channel decides.
	tests = []partners.TestRun{{Channel: "federation", Passed: false}, {Channel: "federation", Passed: true}}
	if s := partners.Score(items, tests); s != 25 {
		t.Fatalf("latest federation test failed: %d", s)
	}
	if len(partners.Templates()) < 3 {
		t.Fatal("templates")
	}
}

func TestOnboardingChecklist(t *testing.T) {
	e := testkit.New(t)
	var p partner
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "New Wholesaler"}), &p)
	var tpls struct {
		Items []struct {
			Key   string
			Items []any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/onboarding/templates", nil), &tpls)
	if len(tpls.Items) != 3 || tpls.Items[0].Key != "dscsa-wholesaler" {
		t.Fatalf("templates: %+v", tpls)
	}
	var o onboarding
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+p.ID+"/onboarding", nil), &o)
	if o.Started || o.Score != 0 || len(o.Items) != 0 {
		t.Fatalf("not started: %+v", o)
	}
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(viewer, "POST", "/api/v1/partners/"+p.ID+"/onboarding", map[string]any{"template": "b2b-edi"}), 403, "missing role")
	problem(t, e.Do("POST", "/api/v1/partners/"+p.ID+"/onboarding", map[string]any{"template": "nope"}), 400, "unknown template")
	problem(t, e.Do("POST", "/api/v1/partners/"+p.ID+"/onboarding", "{"), 400, "invalid JSON")
	problem(t, e.Do("POST", "/api/v1/partners/00000000-0000-0000-0000-000000000001/onboarding", map[string]any{"template": "b2b-edi"}), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/partners/x/onboarding", map[string]any{"template": "b2b-edi"}), 400, "invalid id")
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners/"+p.ID+"/onboarding", map[string]any{"template": "b2b-edi"}), &o)
	if !o.Started || o.Template != "b2b-edi" || len(o.Items) != 5 || o.Score != 0 {
		t.Fatalf("started: %+v", o)
	}
	problem(t, e.Do("POST", "/api/v1/partners/"+p.ID+"/onboarding", map[string]any{"template": "b2b-edi"}), 409, "already started")

	// Manual items are ticked off by people; test items only by test runs.
	var manual, test string
	for _, it := range o.Items {
		if it.Kind == "manual" && manual == "" {
			manual = it.ID
		}
		if it.Kind == "test" {
			test = it.ID
		}
	}
	path := "/api/v1/partners/" + p.ID + "/onboarding/items/"
	problem(t, e.Call(viewer, "PATCH", path+manual, map[string]any{"done": true}), 403, "missing role")
	problem(t, e.Do("PATCH", path+test, map[string]any{"done": true}), 409, "test items")
	problem(t, e.Do("PATCH", path+"00000000-0000-0000-0000-000000000001", map[string]any{"done": true}), 404, "not found")
	problem(t, e.Do("PATCH", path+"x", map[string]any{"done": true}), 400, "invalid itemId")
	problem(t, e.Do("PATCH", "/api/v1/partners/x/onboarding/items/"+manual, map[string]any{"done": true}), 400, "invalid id")
	problem(t, e.Do("PATCH", path+manual, "{"), 400, "invalid JSON")
	e.Must(http.StatusOK, e.Do("PATCH", path+manual, map[string]any{"done": true, "note": "GLNs exchanged by e-mail"}), &o)
	if o.Score != 20 || !o.Items[0].Done || o.Items[0].Note != "GLNs exchanged by e-mail" || o.Items[0].DoneBy != "user:admin@acme.test" {
		t.Fatalf("ticked: %+v", o)
	}
	e.Must(http.StatusOK, e.Do("PATCH", path+manual, map[string]any{"done": false}), &o)
	if o.Score != 0 || o.Items[0].Note != "GLNs exchanged by e-mail" || o.Items[0].DoneBy != "" {
		t.Fatalf("unticked: %+v", o)
	}

	// A passing test run (recorded by the link test harness) satisfies the test item.
	pid := uuid.MustParse(p.ID)
	sys := auth.With(context.Background(), auth.System(e.Tenant.ID))
	if err := e.App.Deps.DB.Tx(sys, e.Tenant.ID, func(tx pgx.Tx) error {
		_, err := partners.RecordTest(sys, tx, pid, "local", true, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/partners/"+p.ID+"/onboarding", nil), &o)
	if o.Score != 20 || len(o.Tests) != 1 || !o.Items[4].Done {
		t.Fatalf("after test: %+v", o)
	}
	problem(t, e.Do("GET", "/api/v1/partners/00000000-0000-0000-0000-000000000001/onboarding", nil), 404, "not found")
	problem(t, e.Do("GET", "/api/v1/partners/x/onboarding", nil), 400, "invalid id")

	// Tenant isolation.
	_, other := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(other, "GET", "/api/v1/partners/"+p.ID+"/onboarding", nil), 404, "not found")
	problem(t, e.Call(other, "PATCH", path+manual, map[string]any{"done": true}), 404, "not found")

	for _, a := range []string{"partner.onboarding.start", "partner.onboarding.item", "partner.onboarding.test"} {
		var aud struct{ Items []any }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action="+a, nil), &aud)
		if len(aud.Items) == 0 {
			t.Errorf("no audit entry %s", a)
		}
	}
}
