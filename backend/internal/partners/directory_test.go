package partners_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

type profile struct {
	TenantID, Name, Contact, Website string
	GLNs, CompanyPrefixes            []string
	Verified                         bool
	VerifiedBy, VerificationNote     string
	Endpoints                        map[string]string
	Licenses                         []struct {
		ID, Kind, Number string
		Verified         bool
	}
}

func operatorToken(t *testing.T, e *testkit.Env) string {
	t.Helper()
	if _, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "platform", Name: "Platform", AdminEmail: "ops@openspoor.test",
		AdminPassword: testkit.Password, Operator: true}); err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "platform", "email": "ops@openspoor.test", "password": testkit.Password}), &tok)
	return tok.Token
}

func TestNetworkDirectory(t *testing.T) {
	e := testkit.New(t)
	in := map[string]any{"name": "Acme Pharma", "kind": "manufacturer", "gln": "9506000000008", "glns": []string{"9506000000008", "0614141000005"},
		"companyPrefixes": []string{"9506000"}, "country": "NL", "description": "Generic medicines", "website": "https://acme.example",
		"contact": "partners@acme.example", "endpoints": map[string]string{"epcisCapture": "https://acme.example/epcis"},
		"licenses": []map[string]any{{"kind": "manufacturer", "number": "M-1", "authority": "IGJ", "country": "NL", "validTo": "2030-01-01"}}, "published": true}

	// Validation.
	for field, val := range map[string]any{"glns": []string{"123"}, "companyPrefixes": []string{"12"}, "website": "ftp://x", "contact": "not a contact",
		"licenses": []map[string]any{{"kind": "x"}}, "name": " "} {
		bad := map[string]any{}
		for k, v := range in {
			bad[k] = v
		}
		bad[field] = val
		if r := e.Do("PUT", "/api/v1/partners/listing", bad); r.Status != http.StatusBadRequest {
			t.Errorf("%s=%v accepted: %d %s", field, val, r.Status, r.Body)
		}
	}
	bad := map[string]any{"name": "x", "kind": "x", "licenses": []map[string]any{{"kind": "x", "number": "1", "authority": "a", "validTo": "01-01-2030"}}}
	problem(t, e.Do("PUT", "/api/v1/partners/listing", bad), 400, "YYYY-MM-DD")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(viewer, "PUT", "/api/v1/partners/listing", in), 403, "missing role")

	var mine profile
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/listing", nil), &mine)
	if mine.Name != "" || mine.GLNs == nil {
		t.Fatalf("empty listing: %+v", mine)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", in), &mine)
	if len(mine.Licenses) != 1 || mine.Licenses[0].ID == "" || mine.Licenses[0].Verified || len(mine.GLNs) != 2 {
		t.Fatalf("listing: %+v", mine)
	}

	// Public search (no authentication): published fields only, no integration endpoints.
	var pub struct{ Items []profile }
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/directory?q=0614141000005", nil), &pub)
	if len(pub.Items) != 1 || pub.Items[0].Name != "Acme Pharma" || pub.Items[0].Endpoints != nil || pub.Items[0].Contact != "partners@acme.example" {
		t.Fatalf("public search: %+v", pub)
	}
	for _, q := range []string{"q=9506000", "q=acme", "country=nl", "kind=manufacturer"} {
		e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/directory?"+q, nil), &pub)
		if len(pub.Items) != 1 {
			t.Errorf("%s: %+v", q, pub)
		}
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/directory?verified=true", nil), &pub)
	if len(pub.Items) != 0 {
		t.Fatalf("unverified listed as verified: %+v", pub)
	}
	var one profile
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/directory/"+mine.TenantID, nil), &one)
	problem(t, e.Call("", "GET", "/api/v1/directory/00000000-0000-0000-0000-000000000001", nil), 404, "not found")
	problem(t, e.Call("", "GET", "/api/v1/directory/x", nil), 400, "invalid tenantId")

	// Operator verification marks; tenants cannot set them.
	op := operatorToken(t, e)
	problem(t, e.Do("POST", "/api/v1/directory/"+mine.TenantID+"/verification", map[string]any{"verified": true}), 403, "operator only")
	problem(t, e.Call(op, "POST", "/api/v1/directory/"+mine.TenantID+"/verification", map[string]any{"verified": true, "licenses": map[string]bool{"nope": true}}), 400, "unknown license")
	problem(t, e.Call(op, "POST", "/api/v1/directory/00000000-0000-0000-0000-000000000001/verification", map[string]any{"verified": true}), 404, "not found")
	problem(t, e.Call(op, "POST", "/api/v1/directory/x/verification", map[string]any{}), 400, "invalid tenantId")
	problem(t, e.Call(op, "POST", "/api/v1/directory/"+mine.TenantID+"/verification", "{"), 400, "invalid JSON")
	var v profile
	e.Must(http.StatusOK, e.Call(op, "POST", "/api/v1/directory/"+mine.TenantID+"/verification", map[string]any{"verified": true, "note": "checked against register",
		"licenses": map[string]bool{mine.Licenses[0].ID: true}}), &v)
	if !v.Verified || !v.Licenses[0].Verified || v.VerifiedBy != "user:ops@openspoor.test" {
		t.Fatalf("verified: %+v", v)
	}
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/directory?verified=true", nil), &pub)
	if len(pub.Items) != 1 || !pub.Items[0].Licenses[0].Verified {
		t.Fatalf("verified search: %+v", pub)
	}

	// Re-publishing unchanged facts keeps the marks; changed identity or license facts clear them.
	lic := map[string]any{"id": mine.Licenses[0].ID, "kind": "manufacturer", "number": "M-1", "authority": "IGJ", "country": "NL", "validTo": "2030-01-01", "verified": true}
	in["licenses"] = []map[string]any{lic}
	in["description"] = "Updated"
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", in), &mine)
	if !mine.Verified || !mine.Licenses[0].Verified {
		t.Fatalf("marks lost on an unrelated edit: %+v", mine)
	}
	lic["number"] = "M-2"
	in["name"] = "Acme Pharma BV"
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", in), &mine)
	if mine.Verified || mine.Licenses[0].Verified {
		t.Fatalf("marks kept after identity change: %+v", mine)
	}

	// Connection requests land in the published tenant's alert inbox.
	_, other := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(other, "POST", "/api/v1/partners/directory/00000000-0000-0000-0000-000000000001/connect", map[string]any{}), 404, "no published profile")
	problem(t, e.Do("POST", "/api/v1/partners/directory/"+mine.TenantID+"/connect", map[string]any{}), 400, "yourself")
	problem(t, e.Call(other, "POST", "/api/v1/partners/directory/x/connect", map[string]any{}), 400, "invalid tenantId")
	problem(t, e.Call(other, "POST", "/api/v1/partners/directory/"+mine.TenantID+"/connect", "{"), 400, "invalid JSON")
	long := make([]byte, 2001)
	for i := range long {
		long[i] = 'a'
	}
	problem(t, e.Call(other, "POST", "/api/v1/partners/directory/"+mine.TenantID+"/connect", map[string]any{"message": string(long)}), 400, "2000")
	e.Must(http.StatusCreated, e.Call(other, "POST", "/api/v1/partners/directory/"+mine.TenantID+"/connect", map[string]any{"message": "Let's exchange TI/TS"}), nil)
	var reqs struct {
		Items []struct{ FromName, Message string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/directory/requests", nil), &reqs)
	if len(reqs.Items) != 1 || reqs.Items[0].FromName != "GLOBEX" {
		t.Fatalf("requests: %+v", reqs)
	}
	var al struct{ Items []struct{ Title string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=directory.", nil), &al)
	if len(al.Items) != 1 || al.Items[0].Title != "GLOBEX asks to connect" {
		t.Fatalf("connection alert: %+v", al)
	}
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/partners/directory/requests", nil), &reqs)
	if len(reqs.Items) != 0 {
		t.Fatalf("requests leak to sender: %+v", reqs)
	}

	// Unpublished profiles disappear from the public directory.
	in["published"] = false
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", in), nil)
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/directory", nil), &pub)
	if len(pub.Items) != 0 {
		t.Fatalf("unpublished profile listed: %+v", pub)
	}
	problem(t, e.Call("", "GET", "/api/v1/directory/"+mine.TenantID, nil), 404, "not found")

	for _, a := range []string{"directory.publish", "directory.verify", "directory.connection_request"} {
		var aud struct{ Items []any }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action="+a, nil), &aud)
		if len(aud.Items) == 0 {
			t.Errorf("no audit entry %s", a)
		}
	}

	// The public directory is rate limited per client.
	limited := false
	for i := 0; i < 80 && !limited; i++ {
		limited = e.Call("", "GET", "/api/v1/directory", nil).Status == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("public directory is not rate limited")
	}
}
