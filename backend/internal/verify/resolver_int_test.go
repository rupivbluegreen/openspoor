package verify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

const rgtin = "09506000134352"

// get calls without following redirects.
func get(t *testing.T, e *testkit.Env, token, path string, headers ...string) (int, http.Header, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.Server.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

func tokenFor(t *testing.T, e *testkit.Env, tenant, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": tenant, "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

func addLink(t *testing.T, e *testkit.Env, in map[string]any) string {
	t.Helper()
	var l struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/resolver/links", in), &l)
	return l.ID
}

type linkset struct {
	Linkset []map[string]json.RawMessage
}

func (l linkset) rels() map[string]bool {
	out := map[string]bool{}
	for k := range l.Linkset[0] {
		if k != "anchor" {
			out[k] = true
		}
	}
	return out
}

func TestDigitalLinkResolver(t *testing.T) {
	e := testkit.New(t)
	// Pack data: two commissioned serials of lot A7.
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": rgtin, "name": "Amoxicillin 500mg"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 2}, "Idempotency-Key", "k"), &a)
	u0, _ := gs1.SGTINURN(rgtin, a.Serials[0], 7)
	u1, _ := gs1.SGTINURN(rgtin, a.Serials[1], 7)
	comm := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: time.Now().Add(-time.Hour).UTC(), EventTimeZoneOffset: "+00:00",
		EPCList: []string{u0, u1}, ILMD: map[string]any{"lotNumber": "A7", "itemExpirationDate": "2030-01-31"}}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{comm}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()

	viewer := tokenFor(t, e, "acme", "viewer@acme.test", "Viewer")
	compliance := tokenFor(t, e, "acme", "comp@acme.test", "Compliance")
	ops := tokenFor(t, e, "acme", "ops@acme.test", "Ops")
	_, other := e.NewTenant("globex", "admin@globex.test")
	third, third2 := e.NewTenant("initech", "admin@initech.test")
	_ = third

	// Management: vocabulary, validation, roles.
	var types struct {
		Items     []struct{ ID, Vocabulary string }
		Audiences []string
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/resolver/link-types", nil), &types)
	if len(types.Items) < 5 || len(types.Audiences) != 3 {
		t.Fatalf("types: %+v", types)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/resolver/links", map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "https://brand.example/pip"}), nil)
	e.Must(http.StatusForbidden, e.Call(ops, "POST", "/api/v1/resolver/links", map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "https://brand.example/pip"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/resolver/links", map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "http://brand.example/pip"}), nil)

	pip := addLink(t, e, map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "https://brand.example/pip", "title": "Product page", "isDefault": true})
	addLink(t, e, map[string]any{"gtin": rgtin, "linkType": "gs1:safetyInfo", "href": "https://brand.example/safety", "audiences": []string{"authority"}, "mimeType": "application/pdf"})
	addLink(t, e, map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "https://brand.example/partner", "audiences": []string{"trading_partner"}})
	addLink(t, e, map[string]any{"gtin": rgtin, "lot": "A7", "linkType": "gs1:recallStatus", "href": "https://brand.example/recall-a7", "lang": "en"})
	inactive := addLink(t, e, map[string]any{"gtin": rgtin, "linkType": "custom:old", "href": "https://brand.example/old", "active": false})
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/consumer-info/"+rgtin+"/en", map[string]any{"leafletUrl": "https://brand.example/leaflet-en.html"}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/consumer-info/"+rgtin+"/nl", map[string]any{"leafletUrl": "https://brand.example/leaflet-nl.html"}), nil)

	var list struct{ Items []struct{ ID string } }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/resolver/links?gtin="+rgtin, nil), &list)
	if len(list.Items) != 5 {
		t.Fatalf("links: %+v", list)
	}

	// Anonymous: default redirect, linkset, content negotiation.
	root := "/01/" + rgtin
	st, h, _ := get(t, e, "", root)
	if st != http.StatusTemporaryRedirect || h.Get("Location") != "https://brand.example/pip" || !strings.Contains(strings.Join(h.Values("Link"), "\n"), `rel="linkset"`) || !strings.Contains(h.Get("Cache-Control"), "public") {
		t.Fatalf("default: %d %v", st, h)
	}
	st, h, body := get(t, e, "", root+"?linkType=all")
	var ls linkset
	if st != 200 || h.Get("Content-Type") != "application/linkset+json" || json.Unmarshal(body, &ls) != nil {
		t.Fatalf("linkset: %d %v %s", st, h, body)
	}
	rels := ls.rels()
	if !rels["https://gs1.org/voc/pip"] || !rels["openspoor:leaflet"] || rels["https://gs1.org/voc/safetyInfo"] || rels["https://gs1.org/voc/recallStatus"] || rels["custom:old"] || len(rels) != 2 {
		t.Fatalf("consumer linkset: %v", rels)
	}
	if strings.Contains(string(body), "partner") {
		t.Fatalf("partner link leaked to consumers: %s", body)
	}
	_, _, body = get(t, e, "", root, "Accept", "application/linkset+json")
	if json.Unmarshal(body, &ls) != nil || len(ls.Linkset) != 1 {
		t.Fatalf("accept linkset: %s", body)
	}
	st, _, body = get(t, e, "", root, "Accept", "application/json")
	var summary struct {
		Audience string
		Recalled bool
		Links    []struct{ LinkType, Source string }
	}
	if st != 200 || json.Unmarshal(body, &summary) != nil || summary.Audience != "consumer" || len(summary.Links) != 3 {
		t.Fatalf("json: %d %s", st, body)
	}
	st, h, _ = get(t, e, "", root+"?linkType=openspoor:leaflet", "Accept-Language", "nl-NL,nl;q=0.9,en;q=0.5")
	if st != 307 || h.Get("Location") != "https://brand.example/leaflet-nl.html" {
		t.Fatalf("nl leaflet: %d %v", st, h)
	}
	st, h, _ = get(t, e, "", root+"?linkType=openspoor:leaflet", "Accept-Language", "de")
	if st != 307 || h.Get("Location") != "https://brand.example/leaflet-en.html" {
		t.Fatalf("fallback leaflet: %d %v", st, h)
	}
	st, h, _ = get(t, e, "", root+"?linkType=https://gs1.org/voc/pip")
	if st != 307 || h.Get("Location") != "https://brand.example/pip" {
		t.Fatalf("full IRI: %d %v", st, h)
	}
	for path, want := range map[string]int{root + "?linkType=gs1:safetyInfo": 404, root + "?linkType=gs1:nonsense": 400, root + "?audience=public": 400,
		"/01/12345678901234": 400, "/01/09524000059109": 404, "/api/v1/resolve/00/095060001234567890": 404, root + "?audience=authority": 401} {
		if st, _, body := get(t, e, "", path); st != want {
			t.Errorf("%s: %d, want %d: %s", path, st, want, body)
		}
	}

	// A serial adds the consumer verification link and makes it the default when no link is flagged.
	serialPath := root + "/10/A7/21/" + url.PathEscape(a.Serials[0])
	st, h, _ = get(t, e, "", serialPath+"?linkType=openspoor:consumerVerification")
	if st != 307 || !strings.Contains(h.Get("Location"), "/verify/?code=") || !strings.Contains(h.Get("Location"), url.QueryEscape("/01/"+rgtin)) {
		t.Fatalf("verification: %d %v", st, h)
	}
	// The lot-specific recall link only exists for its lot.
	st, _, body = get(t, e, "", "/api/v1/resolve/01/"+rgtin+"/10/A7?linkType=all")
	_ = json.Unmarshal(body, &ls)
	if st != 200 || !ls.rels()["https://gs1.org/voc/recallStatus"] {
		t.Fatalf("lot link: %d %s", st, body)
	}
	// ... and through the serial, whose lot comes from the projection.
	_, _, body = get(t, e, "", serialPath[:strings.Index(serialPath, "/10/")]+"/21/"+url.PathEscape(a.Serials[1])+"?linkType=all")
	_ = json.Unmarshal(body, &ls)
	if !ls.rels()["https://gs1.org/voc/recallStatus"] || !ls.rels()["openspoor:consumerVerification"] {
		t.Fatalf("serial link: %s", body)
	}

	// Other audiences need credentials.
	authority := root + "?linkType=all&audience=authority"
	if st, _, _ := get(t, e, viewer, authority); st != http.StatusForbidden {
		t.Fatalf("viewer as authority: %d", st)
	}
	if st, _, _ := get(t, e, ops, authority); st != http.StatusForbidden {
		t.Fatalf("ops as authority: %d", st)
	}
	st, h, body = get(t, e, compliance, authority)
	_ = json.Unmarshal(body, &ls)
	if st != 200 || !ls.rels()["https://gs1.org/voc/safetyInfo"] || !ls.rels()["https://gs1.org/voc/pip"] || !strings.Contains(h.Get("Cache-Control"), "no-store") {
		t.Fatalf("authority: %d %s %v", st, body, h)
	}
	if st, _, _ := get(t, e, "bad-token", authority); st != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", st)
	}

	// Trading partner: the owner's own users, and only linked partners from outside.
	partnerURL := root + "?linkType=all&audience=trading_partner"
	if st, _, body = get(t, e, viewer, partnerURL); st != 200 || !strings.Contains(string(body), "brand.example/partner") || strings.Contains(string(body), "safety") {
		t.Fatalf("owner as partner: %d %s", st, body)
	}
	if st, _, _ = get(t, e, other, partnerURL); st != http.StatusForbidden {
		t.Fatalf("unlinked tenant: %d", st)
	}
	var partner struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Globex Wholesale", "gln": "0614141000029", "kind": "wholesaler"}), &partner)
	superExec(t, e, `update partner set linked_tenant=(select id from tenant where slug='globex') where id=$1`, partner.ID)
	if st, _, body = get(t, e, other, partnerURL); st != 200 || !strings.Contains(string(body), "brand.example/partner") {
		t.Fatalf("linked partner: %d %s", st, body)
	}
	if st, _, _ = get(t, e, other, root+"?audience=authority"); st != http.StatusForbidden {
		t.Fatalf("partner must not be an authority: %d", st)
	}
	if st, _, _ = get(t, e, third2, partnerURL); st != http.StatusForbidden {
		t.Fatalf("third tenant: %d", st)
	}

	// An open recall adds the recall status link for the pack, derived from the recall.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/trace/recalls", map[string]any{"gtin": rgtin, "lot": "A7", "reason": "test"}), nil)
	st, _, body = get(t, e, "", serialPath, "Accept", "application/json")
	if st != 200 || json.Unmarshal(body, &summary) != nil || !summary.Recalled {
		t.Fatalf("recalled flag: %d %s", st, body)
	}

	// Update, delete, isolation and audit.
	e.Must(http.StatusNotFound, e.Call(other, "PUT", "/api/v1/resolver/links/"+pip, map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "https://evil.example/"}), nil)
	e.Must(http.StatusNotFound, e.Call(other, "DELETE", "/api/v1/resolver/links/"+pip, nil), nil)
	var other2 struct{ Items []any }
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/resolver/links", nil), &other2)
	if len(other2.Items) != 0 {
		t.Fatalf("leak: %+v", other2)
	}
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/resolver/links/"+pip, map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "ftp://x"}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/resolver/links/"+pip, map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "https://brand.example/pip2", "isDefault": true}), nil)
	if _, h, _ = get(t, e, "", root); h.Get("Location") != "https://brand.example/pip2" {
		t.Fatalf("updated link: %v", h)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/resolver/links/"+inactive, map[string]any{"gtin": rgtin, "linkType": "custom:old", "href": "https://brand.example/old", "active": true}), nil)
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/resolver/links/"+pip, nil), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/resolver/links/"+pip, nil), nil)
	e.Must(http.StatusNotFound, e.Do("PUT", "/api/v1/resolver/links/"+pip, map[string]any{"gtin": rgtin, "linkType": "gs1:pip", "href": "https://brand.example/x"}), nil)
	// Without the flagged default the verification link (with a serial) or the first link is used.
	if st, h, _ = get(t, e, "", serialPath); st != 307 || !strings.Contains(h.Get("Location"), "/verify/?code=") {
		t.Fatalf("no default: %d %v", st, h)
	}
	var au struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=500", nil), &au)
	n := map[string]int{}
	for _, x := range au.Items {
		n[x.Action]++
	}
	if n["resolver.link.create"] != 5 || n["resolver.link.update"] != 2 || n["resolver.link.delete"] != 1 {
		t.Fatalf("audit: %v", n)
	}
}

func TestResolverRateLimit(t *testing.T) {
	e := testkit.New(t)
	limited := false
	for i := 0; i < 60 && !limited; i++ {
		st, _, _ := get(t, e, "", "/01/09524000059109")
		limited = st == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("the public resolver must be rate limited")
	}
}

func superExec(t *testing.T, e *testkit.Env, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
