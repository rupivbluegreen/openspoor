package link_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/testkit"
)

func problem(t *testing.T, r testkit.Response, status int, want string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Body)
	}
	var p struct {
		Title  string
		Status int
	}
	if err := r.JSON(&p); err != nil || p.Status != status || !strings.Contains(p.Title, want) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("problem %+v (want %q): %s", p, want, r.Body)
	}
}

func login(t *testing.T, e *testkit.Env, email string, roles ...string) string {
	t.Helper()
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": email, "roles": roles, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": email, "password": testkit.Password}), &tok)
	return tok.Token
}

// linkTenants connects acme with a second tenant on the same node through an invitation;
// it returns the second tenant's token and acme's (approved) partner id for it.
func linkTenants(t *testing.T, e *testkit.Env, slug string) (string, string) {
	t.Helper()
	_, tok := e.NewTenant(slug, "admin@"+slug+".test")
	var inv struct{ Code string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners/invitations", map[string]any{"email": "admin@" + slug + ".test", "name": slug}), &inv)
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/partners/invitations/accept", map[string]any{"code": inv.Code}), nil)
	var list struct{ Items []struct{ ID string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners?status=pending", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("pending partners: %+v", list)
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/partners/"+list.Items[0].ID+"/approve", nil), nil)
	return tok, list.Items[0].ID
}

func order(partnerID, number string) map[string]any {
	o := map[string]any{"type": "order", "payload": map[string]any{"number": number, "buyer": map[string]string{"name": "A", "gln": "9506000000008"},
		"seller": map[string]string{"name": "B", "gln": "0614141000005"}, "lines": []map[string]any{{"gtin": "09506000134352", "quantity": 4, "unitPrice": 1.25}}}}
	if partnerID != "" {
		o["partnerId"] = partnerID
	}
	return o
}

type doc struct {
	ID, Type, Direction, Number, Status string
	PartnerID                           *string
	Total                               *float64
	Delivery                            map[string]any
	SentAt                              *time.Time
}

func TestModuleMetadataAndDID(t *testing.T) {
	m := link.New(kernel.Deps{}, nil)
	if m.Name() != "link" || m.Client.Timeout != 15*time.Second {
		t.Fatalf("module: %+v", m)
	}
	if s := m.Schedules(); len(s) != 2 || s[0].Name != "link.cert_expiry" || s[0].Every != 6*time.Hour || s[1].Name != "link.sla" {
		t.Fatalf("schedules: %+v", s)
	}
	for in, want := range map[string]string{
		"https://node.example":            "did:web:node.example",
		"http://127.0.0.1:8080/":          "did:web:127.0.0.1%3A8080",
		"https://node.example/tenants/a/": "did:web:node.example:tenants:a",
		"":                                "did:web:localhost",
		"://bad":                          "did:web:localhost",
	} {
		if got := link.DIDFor(in); got != want {
			t.Errorf("DIDFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVerifyEnvelopeRejectsBadSignatures(t *testing.T) {
	e := testkit.New(t)
	n, err := linkOf(e).Node(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	env := n.Sign(link.Envelope{ID: "1", Type: "ping", From: n.DID, CreatedAt: time.Now(), Payload: json.RawMessage(`{"b":1,"a":[1.50,2]}`)})
	if !link.VerifyEnvelope(n.Pub, env) {
		t.Fatal("valid signature rejected")
	}
	// Re-ordered keys and a non-UTC timestamp verify the same (canonical signing bytes).
	reordered := env
	reordered.Payload = json.RawMessage(`{ "a": [1.50, 2], "b": 1 }`)
	reordered.CreatedAt = env.CreatedAt.In(time.FixedZone("x", 3600))
	if !link.VerifyEnvelope(n.Pub, reordered) {
		t.Fatal("canonicalisation broken")
	}
	bad := env
	bad.Sig = "!!!"
	other, _, _ := ed25519.GenerateKey(nil)
	changed := env
	changed.ToTenant = "someone-else"
	for name, ok := range map[string]bool{
		"bad base64":    link.VerifyEnvelope(n.Pub, bad),
		"short key":     link.VerifyEnvelope(n.Pub[:10], env),
		"other key":     link.VerifyEnvelope(other, env),
		"changed field": link.VerifyEnvelope(n.Pub, changed),
	} {
		if ok {
			t.Errorf("%s: signature accepted", name)
		}
	}
	// The node identity is created once and reused.
	n2, _ := linkOf(e).Node(context.Background())
	if n2 != n {
		t.Fatal("node identity not cached")
	}
	var node map[string]any
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/federation/node", nil), &node)
	if node["id"] != n.DID {
		t.Fatalf("node document: %+v", node)
	}
}

func TestB2BDocuments(t *testing.T) {
	e := testkit.New(t)
	problem(t, e.Do("POST", "/api/v1/b2b/documents", order("00000000-0000-0000-0000-000000000001", "PO-0")), 400, "unknown partnerId")
	problem(t, e.Do("POST", "/api/v1/b2b/documents", map[string]any{"type": "telegram", "payload": map[string]any{"number": "1"}}), 400, "unknown document type")
	problem(t, e.Do("POST", "/api/v1/b2b/documents", map[string]any{"type": "order", "payload": map[string]any{"number": "1"}}), 400, "at least one line")
	problem(t, e.Do("POST", "/api/v1/b2b/documents", "{"), 400, "invalid JSON body")

	// A document without a partner can be rendered but not sent.
	var d doc
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order("", "PO-1")), &d)
	if d.Status != "draft" || d.Direction != "outbound" || d.Total == nil || *d.Total != 5 {
		t.Fatalf("draft: %+v", d)
	}
	problem(t, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), 400, "document has no partner")
	r := e.Do("GET", "/api/v1/b2b/documents/"+d.ID+"/edi", nil)
	if r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/edi-x12") || !strings.Contains(string(r.Body), "BEG*00*SA*PO-1") {
		t.Fatalf("default x12: %d %s %s", r.Status, r.Header.Get("Content-Type"), r.Body)
	}
	problem(t, e.Do("GET", "/api/v1/b2b/documents/"+d.ID+"/edi?format=pdf", nil), 400, "format must be x12, edifact or idoc")
	problem(t, e.Do("GET", "/api/v1/b2b/documents/00000000-0000-0000-0000-000000000001/edi", nil), 404, "not found")
	problem(t, e.Do("GET", "/api/v1/b2b/documents/nope/edi", nil), 400, "invalid id")
	var got doc
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents/"+d.ID, nil), &got)
	if got.Number != "PO-1" {
		t.Fatalf("get: %+v", got)
	}
	problem(t, e.Do("GET", "/api/v1/b2b/documents/00000000-0000-0000-0000-000000000001", nil), 404, "not found")
	problem(t, e.Do("GET", "/api/v1/b2b/documents/x", nil), 400, "invalid id")

	// Import: format and parse errors; EDIFACT round trip creates a received inbound document.
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "pdf", "content": "a,b"}), 400, "format must be x12, edifact")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "content": "garbage"}), 400, "cannot parse")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12"}), 400, "cannot parse")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/import", "{"), 400, "invalid JSON body")
	ef := e.Do("GET", "/api/v1/b2b/documents/"+d.ID+"/edi?format=EDIFACT", nil)
	var imp doc
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "EDIFACT", "content": string(ef.Body)}), &imp)
	if imp.Number != "PO-1" || imp.Direction != "inbound" || imp.Status != "received" {
		t.Fatalf("edifact import: %+v", imp)
	}
	// Inbound documents cannot be sent.
	problem(t, e.Do("POST", "/api/v1/b2b/documents/"+imp.ID+"/send", nil), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/b2b/documents/bad/send", nil), 400, "invalid id")

	var list struct {
		Total int
		Items []doc
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents?direction=inbound", nil), &list)
	if list.Total != 1 || list.Items[0].ID != imp.ID {
		t.Fatalf("inbound list: %+v", list)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents?type=invoice", nil), &list)
	if list.Total != 0 {
		t.Fatalf("type filter: %+v", list)
	}

	// EDI pickup: a partner without a linked tenant or federation peer.
	var plain struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Van Partner"}), &plain)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(plain.ID, "PO-2")), &d)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), &d)
	if d.Status != "sent" || d.SentAt == nil || d.Delivery["channel"] != "edi-pickup" {
		t.Fatalf("edi pickup: %+v", d)
	}

	// A misconfigured federation endpoint is a client error, not a server error.
	var badFed struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Fed", "endpoints": map[string]string{"federation": "not-a-peer"}}), &badFed)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(badFed.ID, "PO-3")), &d)
	problem(t, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), 409, "not a federation peer id")
	var unknownPeer struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Fed2", "endpoints": map[string]string{"federation": "00000000-0000-0000-0000-000000000001"}}), &unknownPeer)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(unknownPeer.ID, "PO-4")), &d)
	problem(t, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), 404, "not found")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents/"+d.ID, nil), &d)
	if d.Status != "draft" {
		t.Fatalf("failed send changed status: %+v", d)
	}

	// Local delivery to a linked tenant on this node, with the back-reference to the sender.
	gtok, gp := linkTenants(t, e, "globex")
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(gp, "PO-5")), &d)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), &d)
	if d.Delivery["channel"] != "local" || d.Delivery["remoteId"] == nil {
		t.Fatalf("local delivery: %+v", d)
	}
	var remote doc
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/b2b/documents/"+d.Delivery["remoteId"].(string), nil), &remote)
	if remote.Number != "PO-5" || remote.Direction != "inbound" || remote.PartnerID == nil {
		t.Fatalf("remote copy: %+v", remote)
	}
	var aud struct {
		Items []struct{ Actor string }
	}
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/audit?action=b2b.order.inbound", nil), &aud)
	if len(aud.Items) != 1 || aud.Items[0].Actor != "system" {
		t.Fatalf("remote audit: %+v", aud)
	}

	// Roles and tenant isolation.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ser := login(t, e, "ser@acme.test", "Serialization")
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/b2b/documents", nil), nil)
	problem(t, e.Call(viewer, "POST", "/api/v1/b2b/documents", order("", "PO-V")), 403, "missing role")
	problem(t, e.Call(viewer, "POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), 403, "missing role")
	problem(t, e.Call(ser, "GET", "/api/v1/b2b/documents", nil), 403, "missing role")
	problem(t, e.Call(gtok, "GET", "/api/v1/b2b/documents/"+d.ID, nil), 404, "not found")
	problem(t, e.Call(gtok, "POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), 404, "not found")
}

func TestSupplierCertificates(t *testing.T) {
	e := testkit.New(t)
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Farm Co"}), &p)
	problem(t, e.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"partnerId": p.ID}), 400, "kind is required")
	problem(t, e.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"kind": "GMP", "validTo": "31/12/2030"}), 400, "dates must be YYYY-MM-DD")
	problem(t, e.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"kind": "GMP", "validFrom": "yesterday"}), 400, "dates must be YYYY-MM-DD")
	problem(t, e.Do("POST", "/api/v1/suppliers/certificates", "{"), 400, "invalid JSON body")

	day := func(d int) string { return time.Now().AddDate(0, 0, d).Format("2006-01-02") }
	type cert struct {
		ID, Supplier, Category, Kind, Status string
		ValidTo                              *time.Time
	}
	var expired, expiring, valid, audit cert
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"partnerId": p.ID, "kind": "GMP", "number": "G-1", "validFrom": day(-400), "validTo": day(-1)}), &expired)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"partnerId": p.ID, "kind": "organic", "validTo": day(5) + "T00:00:00Z"}), &expiring)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"kind": "ISO 22000", "validTo": day(365)}), &valid)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/suppliers/certificates", map[string]any{"partnerId": p.ID, "kind": "audit", "category": "audit", "result": "pass"}), &audit)
	if expired.Status != "expired" || expired.Supplier != "Farm Co" || expired.Category != "certificate" || expiring.Status != "expiring" ||
		expiring.ValidTo == nil || expiring.ValidTo.Format("2006-01-02") != day(5) || valid.Status != "valid" || valid.Supplier != "" || audit.Status != "valid" {
		t.Fatalf("statuses: %+v %+v %+v %+v", expired, expiring, valid, audit)
	}
	var list struct {
		Total int
		Items []cert
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/suppliers/certificates", nil), &list)
	if list.Total != 4 || list.Items[0].ID != expired.ID || list.Items[3].ID != audit.ID {
		t.Fatalf("ordered by validity: %+v", list)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/suppliers/certificates?category=audit", nil), &list)
	if list.Total != 1 || list.Items[0].Kind != "audit" {
		t.Fatalf("category filter: %+v", list)
	}

	// Expiry alerts: expired → error, expiring → warning, far-off → none; the schedule runs the same job.
	if err := linkOf(e).Schedules()[0].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var al struct {
		Items []struct{ Severity, Title, EntityID string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=supplier.certificate_expiring", nil), &al)
	sev := map[string]string{}
	for _, a := range al.Items {
		sev[a.EntityID] = a.Severity
	}
	if len(al.Items) != 2 || sev[expired.ID] != "error" || sev[expiring.ID] != "warning" {
		t.Fatalf("expiry alerts: %+v", al.Items)
	}
	_, other := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/suppliers/certificates", nil), &list)
	if list.Total != 0 {
		t.Fatalf("cross-tenant certificates: %+v", list)
	}
}

func TestDocumentExchange(t *testing.T) {
	e := testkit.New(t)
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	problem(t, e.Do("POST", "/api/v1/documents", map[string]any{"title": "x", "content": "not base64!"}), 400, "content must be base64")
	problem(t, e.Do("POST", "/api/v1/documents", map[string]any{"title": "x", "content": ""}), 400, "content must be base64")
	problem(t, e.Do("POST", "/api/v1/documents", map[string]any{"content": b64("hi")}), 400, "title is required")
	problem(t, e.Do("POST", "/api/v1/documents", map[string]any{"title": "big", "content": base64.StdEncoding.EncodeToString(make([]byte, 10<<20+1))}), 400, "limited to 10 MiB")
	problem(t, e.Do("POST", "/api/v1/documents", "{"), 400, "invalid JSON body")

	// Content type is sniffed when omitted; quotes are stripped from the download file name.
	var f struct{ ID, ContentType, SHA256, Direction string }
	sum := sha256.Sum256([]byte("plain text spec"))
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/documents", map[string]any{"title": `spec "v2".txt`, "content": b64("plain text spec")}), &f)
	if !strings.HasPrefix(f.ContentType, "text/plain") || f.Direction != "internal" || f.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("upload: %+v", f)
	}
	r := e.Do("GET", "/api/v1/documents/"+f.ID, nil)
	if r.Status != 200 || string(r.Body) != "plain text spec" || r.Header.Get("Content-Disposition") != `attachment; filename="spec v2.txt"` || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v %s", r.Status, r.Header, r.Body)
	}
	problem(t, e.Do("GET", "/api/v1/documents/00000000-0000-0000-0000-000000000001", nil), 404, "not found")
	problem(t, e.Do("GET", "/api/v1/documents/zz", nil), 400, "invalid id")
	var files struct {
		Total int
		Items []struct{ ID string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/documents", nil), &files)
	if files.Total != 1 || files.Items[0].ID != f.ID {
		t.Fatalf("files: %+v", files)
	}

	// Sharing needs a partner on this node.
	var plain struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Elsewhere"}), &plain)
	problem(t, e.Do("POST", "/api/v1/documents/"+f.ID+"/share", map[string]any{"partnerId": plain.ID}), 409, "partner is not on this node")
	problem(t, e.Do("POST", "/api/v1/documents/"+f.ID+"/share", map[string]any{"partnerId": "00000000-0000-0000-0000-000000000001"}), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/documents/00000000-0000-0000-0000-000000000001/share", map[string]any{"partnerId": plain.ID}), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/documents/bad/share", map[string]any{"partnerId": plain.ID}), 400, "invalid id")
	problem(t, e.Do("POST", "/api/v1/documents/"+f.ID+"/share", "{"), 400, "invalid JSON body")

	gtok, gp := linkTenants(t, e, "globex")
	var shared struct {
		Shared bool
		SHA256 string
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/documents/"+f.ID+"/share", map[string]any{"partnerId": gp}), &shared)
	if !shared.Shared || shared.SHA256 != f.SHA256 {
		t.Fatalf("share: %+v", shared)
	}
	var theirs struct {
		Items []struct {
			ID, Direction, SHA256 string
			PartnerID             *string
		}
	}
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/documents", nil), &theirs)
	if len(theirs.Items) != 1 || theirs.Items[0].Direction != "inbound" || theirs.Items[0].SHA256 != f.SHA256 || theirs.Items[0].PartnerID == nil {
		t.Fatalf("partner copy: %+v", theirs)
	}
	got := e.Call(gtok, "GET", "/api/v1/documents/"+theirs.Items[0].ID, nil)
	if string(got.Body) != "plain text spec" {
		t.Fatalf("partner download: %s", got.Body)
	}
	// The original stays private to its owner.
	problem(t, e.Call(gtok, "GET", "/api/v1/documents/"+f.ID, nil), 404, "not found")
	problem(t, e.Call(gtok, "POST", "/api/v1/documents/"+f.ID+"/share", map[string]any{"partnerId": gp}), 404, "not found")
	var aud struct{ Total int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=document.share&entityId="+f.ID, nil), &aud)
	if aud.Total != 1 {
		t.Fatalf("share audit: %+v", aud)
	}
}

func TestFederationPeersAndInbox(t *testing.T) {
	e := testkit.New(t)
	la := linkOf(e)
	ctx := context.Background()
	n, err := la.Node(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, gtok := e.NewTenant("globex", "admin@globex.test")

	// Peer registration validation and DID resolution failures.
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "p", "url": "ftp://x", "remoteTenant": "a"}), 400, "url must be the peer's http(s) base URL")
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"url": e.Server.URL, "remoteTenant": "a"}), 400, "name and remoteTenant")
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "p", "url": e.Server.URL}), 400, "name and remoteTenant")
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "p", "url": e.Server.URL, "remoteTenant": "a", "publicKey": "short", "did": "did:web:x"}), 400, "publicKey must be a base64url Ed25519 key")
	pub := base64.RawURLEncoding.EncodeToString(n.Pub)
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "p", "url": e.Server.URL, "remoteTenant": "a", "publicKey": pub}), 400, "did is required")
	problem(t, e.Do("POST", "/api/v1/federation/peers", "{"), 400, "invalid JSON body")
	notDID := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer notDID.Close()
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "p", "url": notDID.URL, "remoteTenant": "a"}), 400, "no DID document")
	wrongCurve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"did:web:x","verificationMethod":[{"publicKeyJwk":{"crv":"P-256","x":"` + pub + `"}}]}`))
	}))
	defer wrongCurve.Close()
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "p", "url": wrongCurve.URL, "remoteTenant": "a"}), 400, "no Ed25519 key")
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	problem(t, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "p", "url": closed.URL, "remoteTenant": "a"}), 400, "cannot resolve peer DID")

	// Two tenants on the same node peer with each other (keys given explicitly on one side).
	var ab, ba struct{ ID, DID, Status, PublicKey string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "Globex node", "url": e.Server.URL + "/", "remoteTenant": "globex"}), &ab)
	e.Must(http.StatusCreated, e.Call(gtok, "POST", "/api/v1/federation/peers", map[string]any{"name": "Acme node", "url": e.Server.URL, "remoteTenant": "acme", "publicKey": pub, "did": n.DID}), &ba)
	if ab.DID != n.DID || ab.PublicKey != pub || ab.Status != "pending" {
		t.Fatalf("peer: %+v", ab)
	}

	// Sending needs an active peer and a supported type.
	problem(t, e.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": ab.ID, "type": "ping"}), 409, "peer is pending")
	problem(t, e.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": ab.ID, "type": "shell"}), 400, "type must be ping")
	problem(t, e.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": "00000000-0000-0000-0000-000000000001", "type": "ping"}), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/federation/send", "{"), 400, "invalid JSON body")
	problem(t, e.Do("POST", "/api/v1/federation/peers/00000000-0000-0000-0000-000000000001/approve", nil), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/federation/peers/x/approve", nil), 400, "invalid id")
	problem(t, e.Call(gtok, "POST", "/api/v1/federation/peers/"+ab.ID+"/approve", nil), 404, "not found")
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/federation/peers/"+ab.ID+"/approve", nil), &ab)
	if ab.Status != "active" {
		t.Fatalf("approve: %+v", ab)
	}

	// Globex has not approved acme yet: the inbox refuses and the delivery is marked rejected.
	type msgs struct {
		Items []struct {
			Type, Status, Direction, MessageID string
			Detail                             map[string]any
		}
	}
	var sent struct{ MessageID string }
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": ab.ID, "type": "ping"}), &sent)
	e.Drain()
	var out msgs
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/federation/messages", nil), &out)
	if len(out.Items) != 1 || out.Items[0].Status != "rejected" || out.Items[0].MessageID != sent.MessageID || out.Items[0].Detail["status"] != float64(403) {
		t.Fatalf("rejected delivery: %+v", out)
	}

	// Once approved, a B2B document travels over federation and lands in globex.
	e.Must(http.StatusOK, e.Call(gtok, "POST", "/api/v1/federation/peers/"+ba.ID+"/approve", nil), nil)
	var fp struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Globex via federation", "endpoints": map[string]string{"federation": ab.ID}}), &fp)
	var d doc
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", order(fp.ID, "PO-FED")), &d)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+d.ID+"/send", nil), &d)
	if d.Delivery["channel"] != "federation" || d.Delivery["messageId"] == "" {
		t.Fatalf("federation send: %+v", d)
	}
	e.Drain()
	var inb struct{ Items []doc }
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/b2b/documents?direction=inbound", nil), &inb)
	if len(inb.Items) != 1 || inb.Items[0].Number != "PO-FED" {
		t.Fatalf("federated document: %+v", inb)
	}
	var in msgs
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/federation/messages", nil), &in)
	if len(in.Items) != 1 || in.Items[0].Direction != "inbound" || in.Items[0].Type != "b2b.document" {
		t.Fatalf("inbound messages: %+v", in)
	}
	var peers struct {
		Total int
		Items []struct{ ID, Status string }
	}
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/federation/peers", nil), &peers)
	if peers.Total != 1 || peers.Items[0].Status != "active" {
		t.Fatalf("peers: %+v", peers)
	}

	// Inbox error paths for signed envelopes from an approved peer.
	post := func(body []byte) testkit.Response {
		return e.Call("", "POST", "/api/v1/federation/inbox", body)
	}
	envFor := func(id, typ, toTenant string, payload string) []byte {
		b, _ := json.Marshal(n.Sign(link.Envelope{ID: id, Type: typ, From: n.DID, FromTenant: "acme", To: n.DID, ToTenant: toTenant, CreatedAt: time.Now().UTC(), Payload: json.RawMessage(payload)}))
		return b
	}
	problem(t, post([]byte("not json")), 400, "invalid envelope")
	wrongTo, _ := json.Marshal(n.Sign(link.Envelope{ID: "w", Type: "ping", From: n.DID, To: "did:web:elsewhere", ToTenant: "globex", CreatedAt: time.Now(), Payload: json.RawMessage(`{}`)}))
	problem(t, post(wrongTo), 400, "not addressed to this node")
	problem(t, post(envFor("u1", "ping", "nobody", `{}`)), 404, "unknown recipient tenant")
	problem(t, post(envFor("u2", "launch", "globex", `{}`)), 400, "unsupported message type launch")
	problem(t, post(envFor("u3", "b2b.document", "globex", `{"lines":"many"}`)), 400, "invalid b2b payload")
	problem(t, post(envFor("u4", "b2b.document", "globex", `{"type":"order","number":""}`)), 400, "number is required")
	r := post(envFor("u5", "epcis.capture", "globex", `{"type":"EPCISDocument","epcisBody":{"eventList":[]}}`))
	if r.Status < 400 || r.Status >= 500 {
		t.Fatalf("empty EPCIS capture over federation: %d %s", r.Status, r.Body)
	}
	var ok map[string]any
	e.Must(http.StatusAccepted, post(envFor("u6", "ping", "globex", `{}`)), &ok)
	if ok["accepted"] != true || ok["id"] != "u6" {
		t.Fatalf("ping: %+v", ok)
	}
}

func TestFederationDeliveryRetries(t *testing.T) {
	e := testkit.New(t)
	la := linkOf(e)
	n, err := la.Node(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.RawURLEncoding.EncodeToString(n.Pub)
	var hits int
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		var env link.Envelope
		if json.NewDecoder(r.Body).Decode(&env) != nil || !link.VerifyEnvelope(n.Pub, env) {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	replayed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) }))
	defer replayed.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	status := map[string]string{}
	for name, u := range map[string]string{"failing": failing.URL, "replayed": replayed.URL, "closed": closedURL} {
		var p struct{ ID string }
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/federation/peers", map[string]any{"name": name, "url": u, "remoteTenant": "x", "publicKey": pub, "did": "did:web:" + name}), &p)
		e.Must(http.StatusOK, e.Do("POST", "/api/v1/federation/peers/"+p.ID+"/approve", nil), nil)
		var sent struct{ MessageID string }
		e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": p.ID, "type": "epcis.capture", "payload": map[string]any{"b": 1, "a": 2}}), &sent)
		status[sent.MessageID] = name
	}
	e.Drain()
	var out struct {
		Items []struct {
			MessageID, Status string
			Detail            map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/federation/messages", nil), &out)
	want := map[string]string{"failing": "retrying", "replayed": "delivered", "closed": "retrying"}
	for _, m := range out.Items {
		name := status[m.MessageID]
		if m.Status != want[name] {
			t.Errorf("%s: status %s, detail %v", name, m.Status, m.Detail)
		}
		if name == "closed" && m.Detail["error"] == nil {
			t.Errorf("closed: no error detail: %v", m.Detail)
		}
	}
	if len(out.Items) != 3 || hits != 1 {
		t.Fatalf("messages %d, hits %d", len(out.Items), hits)
	}
	// Retries are re-queued with backoff instead of being dropped.
	var queued int
	if err := e.App.Deps.DB.Pool.QueryRow(context.Background(), `select count(*) from job where kind=$1 and status='queued' and attempts=1`, link.JobDeliver).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("queued retries: %d", queued)
	}
}
