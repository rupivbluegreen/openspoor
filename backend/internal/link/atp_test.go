package link_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/testkit"
)

func TestCanonicalizeRFC8785(t *testing.T) {
	// RFC 8785 section 3.2.2 sample.
	in := `{"numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001], "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/", "literals": [null, true, false]}`
	want := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	got, err := link.Canonicalize([]byte(in))
	if err != nil || string(got) != want {
		t.Fatalf("got %s (%v)\nwant %s", got, err, want)
	}
	// Property sorting by UTF-16 code units (RFC 8785 section 3.2.3).
	got, err = link.Canonicalize([]byte(`{"€":"Euro Sign","\r":"Carriage Return","דּ":"Hebrew Letter Dalet With Dagesh","1":"One","😀":"Emoji: Grinning Face","\u0080":"Control","ö":"Latin Small Letter O With Diaeresis"}`))
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"\r", "1", "\u0080", "\u00f6", "\u20ac", "\U0001F600", "\ufb33"}
	pos := -1
	for _, k := range order {
		i := strings.Index(string(got), `"`+strings.ReplaceAll(k, "\r", `\r`)+`"`)
		if i <= pos {
			t.Fatalf("key %q out of order in %s", k, got)
		}
		pos = i
	}
	// Numbers (ECMAScript serialisation).
	for in, want := range map[string]string{
		"0": "0", "-0": "0", "1e21": "1e+21", "1e20": "100000000000000000000", "1e-7": "1e-7", "0.000001": "0.000001", "-1.5": "-1.5",
		"9007199254740992": "9007199254740992", "123e-20": "1.23e-18", "5e-324": "5e-324", "1.7976931348623157e308": "1.7976931348623157e+308",
		"[1,{}]": "[1,{}]",
	} {
		got, err := link.Canonicalize([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %s (%v), want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"{", `1 2`, "1e999"} {
		if _, err := link.Canonicalize([]byte(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := link.CanonicalizeValue(map[string]any{"x": make(chan int)}); err == nil {
		t.Error("unmarshalable value accepted")
	}
}

type credential struct {
	ID          string
	StatusIndex int
	Credential  map[string]any
	RevokedAt   *time.Time
}

type verification struct {
	ID, Issuer, Subject, CredentialID string
	Verified                          bool
	PartnerID                         *string
	Checks                            []struct {
		Name, Detail string
		OK           bool
	}
}

func (v verification) check(name string) (bool, string) {
	for _, c := range v.Checks {
		if c.Name == name {
			return c.OK, c.Detail
		}
	}
	return false, "missing check " + name
}

func licenseFor(t *testing.T, e *testkit.Env, validTo string) string {
	t.Helper()
	var org struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "Acme Pharma Inc", "kind": "wholesaler", "companyPrefix": "9506000", "country": "US"}), &org)
	var site struct{ ID string }
	gln := "9506000000008"
	if strings.HasPrefix(validTo, "2020") {
		gln = "0614141000005"
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": gln, "name": "DC", "kind": "warehouse"}), &site)
	var lic struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/licenses", map[string]any{"orgId": org.ID, "siteId": site.ID, "kind": "wholesale-distributor", "number": "WD-1",
		"authority": "State Board of Pharmacy", "country": "US", "validFrom": "2025-01-01T00:00:00Z", "validTo": validTo}), &lic)
	return lic.ID
}

func TestATPCredentialLifecycle(t *testing.T) {
	e := testkit.New(t)
	l := linkOf(e)
	l.InsecureDIDWeb = true
	future := time.Now().AddDate(1, 0, 0).Format("2006-01-02") + "T00:00:00Z"
	licID := licenseFor(t, e, future)

	// Roles: only admin and compliance issue; viewers only read.
	ops := login(t, e, "ops@acme.test", "Ops")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(ops, "POST", "/api/v1/credentials", map[string]any{"licenseId": licID}), 403, "missing role")
	problem(t, e.Call(viewer, "POST", "/api/v1/credentials", map[string]any{"licenseId": licID}), 403, "missing role")
	problem(t, e.Do("POST", "/api/v1/credentials", map[string]any{"licenseId": "00000000-0000-0000-0000-000000000001"}), 400, "unknown licenseId")
	problem(t, e.Do("POST", "/api/v1/credentials", "{"), 400, "invalid JSON")

	var c credential
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/credentials", map[string]any{"licenseId": licID}), &c)
	vc := c.Credential
	var did map[string]any
	e.Must(http.StatusOK, e.Call("", "GET", "/.well-known/did.json", nil), &did)
	proof, _ := vc["proof"].(map[string]any)
	subj, _ := vc["credentialSubject"].(map[string]any)
	status, _ := vc["credentialStatus"].(map[string]any)
	if vc["issuer"] != did["id"] || proof["cryptosuite"] != "eddsa-jcs-2022" || proof["type"] != "DataIntegrityProof" ||
		!strings.HasPrefix(proof["proofValue"].(string), "z") || subj["name"] != "Acme Pharma Inc" || subj["gln"] != "9506000000008" ||
		status["type"] != "BitstringStatusListEntry" || vc["validUntil"] == nil || !strings.HasSuffix(proof["verificationMethod"].(string), "#key-2") {
		t.Fatalf("credential: %+v", vc)
	}
	vms, _ := did["verificationMethod"].([]any)
	if len(vms) != 2 || !strings.HasPrefix(vms[1].(map[string]any)["publicKeyMultibase"].(string), "z6Mk") {
		t.Fatalf("DID document Multikey: %+v", did)
	}

	// A partner presents the credential: verified, and recorded on the partner.
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Presenter"}), &p)
	problem(t, e.Call(viewer, "POST", "/api/v1/credentials/verify", map[string]any{"credential": vc}), 403, "missing role")
	problem(t, e.Do("POST", "/api/v1/credentials/verify", map[string]any{}), 400, "credential is required")
	problem(t, e.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": vc, "partnerId": "00000000-0000-0000-0000-000000000001"}), 400, "unknown partnerId")
	var v verification
	e.Must(http.StatusOK, e.Call(ops, "POST", "/api/v1/credentials/verify", map[string]any{"credential": vc, "partnerId": p.ID}), &v)
	if !v.Verified || v.Issuer != did["id"] || v.Subject != "Acme Pharma Inc" || len(v.Checks) != 5 {
		t.Fatalf("verify: %+v", v)
	}

	// Tampering with any signed field breaks the proof.
	tampered := cloneMap(t, vc)
	tampered["credentialSubject"].(map[string]any)["name"] = "Evil Corp"
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": tampered}), &v)
	if ok, detail := v.check("proof"); v.Verified || ok || !strings.Contains(detail, "signature does not match") {
		t.Fatalf("tampered: %+v", v)
	}
	tampered = cloneMap(t, vc)
	tampered["validUntil"] = "2099-01-01T00:00:00Z"
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": tampered}), &v)
	if v.Verified {
		t.Fatal("extended validity accepted")
	}
	// Not a VC 2.0 credential.
	tampered = cloneMap(t, vc)
	tampered["@context"] = []any{"https://www.w3.org/2018/credentials/v1"}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": tampered}), &v)
	if ok, _ := v.check("format"); v.Verified || ok || len(v.Checks) != 1 {
		t.Fatalf("format: %+v", v)
	}
	// Unsupported issuer method.
	tampered = cloneMap(t, vc)
	tampered["issuer"] = map[string]any{"id": "did:key:z6MkhaXgBZDvotDkL5257faiztiGiC2QtKLGpbnnEGta2doK"}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": tampered}), &v)
	if ok, _ := v.check("proof"); ok || v.Verified {
		t.Fatalf("did:key issuer: %+v", v)
	}

	// The public status list is a signed BitstringStatusListCredential.
	var list map[string]any
	e.Must(http.StatusOK, e.Call("", "GET", "/api/v1/credentials/status/revocation", nil), &list)
	if ls, _ := list["credentialSubject"].(map[string]any); ls == nil || !strings.HasPrefix(ls["encodedList"].(string), "u") || list["proof"] == nil {
		t.Fatalf("status list: %+v", list)
	}

	// Revocation: the issuer revokes, and verification then fails on the status check.
	problem(t, e.Call(ops, "POST", "/api/v1/credentials/"+c.ID+"/revoke", map[string]any{"reason": "x"}), 403, "missing role")
	problem(t, e.Do("POST", "/api/v1/credentials/"+c.ID+"/revoke", map[string]any{"reason": " "}), 400, "reason is required")
	problem(t, e.Do("POST", "/api/v1/credentials/"+c.ID+"/revoke", "{"), 400, "invalid JSON")
	problem(t, e.Do("POST", "/api/v1/credentials/x/revoke", map[string]any{"reason": "x"}), 400, "invalid id")
	problem(t, e.Do("POST", "/api/v1/credentials/00000000-0000-0000-0000-000000000001/revoke", map[string]any{"reason": "x"}), 404, "not found")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/credentials/"+c.ID+"/revoke", map[string]any{"reason": "license suspended"}), &c)
	if c.RevokedAt == nil {
		t.Fatalf("revoke: %+v", c)
	}
	problem(t, e.Do("POST", "/api/v1/credentials/"+c.ID+"/revoke", map[string]any{"reason": "again"}), 409, "already revoked")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": vc, "partnerId": p.ID}), &v)
	if ok, detail := v.check("status"); v.Verified || ok || !strings.Contains(detail, "revoked") {
		t.Fatalf("revoked credential verified: %+v", v)
	}
	if ok, _ := v.check("proof"); !ok {
		t.Fatalf("revoked credential's signature is still valid: %+v", v)
	}

	// Issuance refuses expired or not-yet-valid licenses.
	expired := licenseFor(t, e, "2020-01-01T00:00:00Z")
	problem(t, e.Do("POST", "/api/v1/credentials", map[string]any{"licenseId": expired}), 409, "expired")

	// Listing, reading, verification history on the partner.
	var creds struct{ Items []credential }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/credentials", nil), &creds)
	if len(creds.Items) != 1 {
		t.Fatalf("credentials: %+v", creds)
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/credentials/"+c.ID, nil), nil)
	problem(t, e.Do("GET", "/api/v1/credentials/x", nil), 400, "invalid id")
	var vers struct{ Items []verification }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/credentials/verifications?partnerId="+p.ID, nil), &vers)
	if len(vers.Items) != 2 || vers.Items[0].Verified || !vers.Items[1].Verified {
		t.Fatalf("partner verifications: %+v", vers.Items)
	}

	// Tenant isolation.
	_, other := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/credentials", nil), &creds)
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/credentials/verifications", nil), &vers)
	if len(creds.Items) != 0 || len(vers.Items) != 0 {
		t.Fatalf("other tenant sees credentials: %+v %+v", creds, vers)
	}
	problem(t, e.Call(other, "GET", "/api/v1/credentials/"+c.ID, nil), 404, "not found")
	problem(t, e.Call(other, "POST", "/api/v1/credentials/"+c.ID+"/revoke", map[string]any{"reason": "x"}), 404, "not found")

	for _, a := range []string{"atp.credential.issue", "atp.credential.revoke", "atp.credential.verify"} {
		var aud struct{ Items []any }
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action="+a, nil), &aud)
		if len(aud.Items) == 0 {
			t.Errorf("no audit entry %s", a)
		}
	}
}

func TestATPCredentialAcrossNodes(t *testing.T) {
	a, b := testkit.New(t), testkit.New(t)
	linkOf(a).InsecureDIDWeb = true
	linkOf(b).InsecureDIDWeb = true
	licID := licenseFor(t, a, time.Now().AddDate(0, 6, 0).Format("2006-01-02")+"T00:00:00Z")
	var c credential
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/credentials", map[string]any{"licenseId": licID}), &c)
	var v verification
	b.Must(http.StatusOK, b.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": c.Credential}), &v)
	if !v.Verified {
		t.Fatalf("node B verifying node A's credential: %+v", v)
	}
	// The status list must come from the issuer's host.
	moved := cloneMap(t, c.Credential)
	moved["credentialStatus"].(map[string]any)["statusListCredential"] = b.Server.URL + "/api/v1/credentials/status/revocation"
	b.Must(http.StatusOK, b.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": moved}), &v)
	if ok, _ := v.check("status"); ok {
		t.Fatalf("foreign status list accepted: %+v", v)
	}
	// Without https (and not in insecure mode) did:web is resolved over https only.
	linkOf(b).InsecureDIDWeb = false
	b.Must(http.StatusOK, b.Do("POST", "/api/v1/credentials/verify", map[string]any{"credential": c.Credential}), &v)
	if v.Verified {
		t.Fatalf("http did:web resolved in secure mode: %+v", v)
	}
}

func cloneMap(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(m)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
