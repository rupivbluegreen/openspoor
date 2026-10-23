package link_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/testkit"
)

// --- certificate helpers ---

type cert struct {
	x    *x509.Certificate
	key  *ecdsa.PrivateKey
	pair tls.Certificate
	pem  []byte
	keyP []byte
}

func (c cert) fp() string { return link.CertFingerprint(c.x.Raw) }

// mkCert creates a certificate for localhost / 127.0.0.1; signer nil means self-signed.
func mkCert(t *testing.T, cn string, signer *cert, isCA bool, notBefore, notAfter time.Time) cert {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: cn}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true, IsCA: isCA, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	if isCA {
		tpl.KeyUsage |= x509.KeyUsageCertSign
	}
	parent, pkey := tpl, key
	if signer != nil {
		parent, pkey = signer.x, signer.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, &key.PublicKey, pkey)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := x509.ParseCertificate(der)
	kb, _ := x509.MarshalECPrivateKey(key)
	c := cert{x: x, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyP: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})}
	c.pair, err = tls.X509KeyPair(c.pem, c.keyP)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func valid(t *testing.T, cn string, signer *cert) cert {
	return mkCert(t, cn, signer, false, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.pem")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// --- unit tests ---

func TestNormalizeFingerprint(t *testing.T) {
	const hexfp = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	colons := func(s string, upper bool) string {
		var parts []string
		for i := 0; i < len(s); i += 2 {
			parts = append(parts, s[i:i+2])
		}
		out := strings.Join(parts, ":")
		if upper {
			return strings.ToUpper(out)
		}
		return out
	}
	for _, in := range []string{hexfp, strings.ToUpper(hexfp), colons(hexfp, true), colons(hexfp, false), "  " + colons(hexfp, true) + "\n",
		"sha256:" + hexfp, "SHA256:" + colons(hexfp, true), "sha256 Fingerprint=" + colons(hexfp, true), "SHA-256 Fingerprint=" + hexfp} {
		got, err := link.NormalizeFingerprint(in)
		if err != nil || got != hexfp {
			t.Errorf("NormalizeFingerprint(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", hexfp[:62], hexfp + "00", strings.Repeat("zz", 32), "sha1:" + hexfp[:40]} {
		if _, err := link.NormalizeFingerprint(in); err == nil {
			t.Errorf("NormalizeFingerprint(%q) must fail", in)
		}
	}
}

func TestCheckClientCert(t *testing.T) {
	now := time.Now()
	good := valid(t, "good", nil)
	expired := mkCert(t, "old", nil, false, now.Add(-48*time.Hour), now.Add(-time.Hour))
	future := mkCert(t, "new", nil, false, now.Add(time.Hour), now.Add(2*time.Hour))
	other := valid(t, "other", nil)
	peer := func(pin string) link.Peer { return link.Peer{DID: "did:web:x", TLSFingerprint: pin} }
	upperColons := strings.ToUpper(strings.Join(splitPairs(good.fp()), ":"))

	for name, tc := range map[string]struct {
		leaf cert
		pin  string
		ok   bool
	}{
		"match":                 {good, good.fp(), true},
		"match with colons":     {good, upperColons, true},
		"mismatch":              {other, good.fp(), false},
		"no pin":                {good, "", false},
		"malformed pin":         {good, "not-a-fingerprint", false},
		"expired though pinned": {expired, expired.fp(), false},
		"not yet valid":         {future, future.fp(), false},
	} {
		err := link.CheckClientCert(tc.leaf.x, peer(tc.pin), now)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

func splitPairs(s string) []string {
	var out []string
	for i := 0; i < len(s); i += 2 {
		out = append(out, s[i:i+2])
	}
	return out
}

func TestClientForPinsTheLeafCertificate(t *testing.T) {
	ca := mkCert(t, "ca", nil, true, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	server := valid(t, "server", &ca)
	ours := valid(t, "us", &ca)
	var seenClient string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://127.0.0.1:1/", http.StatusFound)
			return
		}
		seenClient = ""
		if len(r.TLS.PeerCertificates) > 0 {
			seenClient = link.CertFingerprint(r.TLS.PeerCertificates[0].Raw)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{server.pair}, ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	defer srv.Close()

	newModule := func(tc config.TLS) *link.Module {
		m := &link.Module{Client: &http.Client{Timeout: 5 * time.Second}}
		m.Cfg.TLS = tc
		return m
	}
	trust := config.TLS{PeerCA: writeTemp(t, ca.pem)}
	get := func(m *link.Module, pin, path string) (int, error) {
		cl, err := m.ClientFor(pin)
		if err != nil {
			return 0, err
		}
		res, err := cl.Get(srv.URL + path)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode, nil
	}

	// Pin matches, with and without colon / case differences.
	m := newModule(trust)
	for _, pin := range []string{server.fp(), strings.ToUpper(strings.Join(splitPairs(server.fp()), ":")), "sha256:" + server.fp()} {
		if code, err := get(m, pin, "/"); err != nil || code != 200 {
			t.Fatalf("pin %q: %d %v", pin, code, err)
		}
	}
	// Pin mismatch must fail even though the chain is valid.
	if _, err := get(m, ours.fp(), "/"); err == nil || !strings.Contains(err.Error(), "pinned fingerprint") {
		t.Fatalf("mismatch must fail, got %v", err)
	}
	// An unusable pin fails closed.
	if _, err := get(m, "garbage", "/"); err == nil {
		t.Fatal("malformed pin must fail")
	}
	// A matching pin does not replace chain verification: without the CA, the handshake fails.
	if _, err := get(newModule(config.TLS{MinVersion: tls.VersionTLS13}), server.fp(), "/"); err == nil || !strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("an untrusted chain must fail even with the right pin: %v", err)
	}
	// No pin and nothing configured: the plain module client is used (system roots, so the test CA is unknown).
	if cl, _ := newModule(config.TLS{}).ClientFor(""); cl == nil || cl.Transport != nil {
		t.Fatal("the unconfigured, unpinned peer uses the module client")
	}
	// The client certificate is presented, and a missing or broken file fails the handshake.
	withCert := trust
	withCert.ClientCert, withCert.ClientKey = writeTemp(t, ours.pem), writeTemp(t, ours.keyP)
	if code, err := get(newModule(withCert), server.fp(), "/"); err != nil || code != 200 || seenClient != ours.fp() {
		t.Fatalf("client cert: %d %v seen %q want %q", code, err, seenClient, ours.fp())
	}
	if code, err := get(m, server.fp(), "/"); err != nil || code != 200 || seenClient != "" {
		t.Fatalf("no client cert configured: %d %v seen %q", code, err, seenClient)
	}
	broken := trust
	broken.ClientCert, broken.ClientKey = "/nonexistent.pem", "/nonexistent.key"
	if _, err := get(newModule(broken), server.fp(), "/"); err == nil {
		t.Fatal("unreadable client certificate must fail")
	}
	// Redirects are not followed.
	if code, err := get(m, server.fp(), "/redirect"); err != nil || code != http.StatusFound {
		t.Fatalf("redirect: %d %v", code, err)
	}
	// Unreadable peer CA fails.
	if _, err := newModule(config.TLS{PeerCA: "/nonexistent"}).ClientFor(server.fp()); err == nil {
		t.Fatal("unreadable peer CA must fail")
	}
}

func TestClientForRefusesOldTLSUnlessConfigured(t *testing.T) {
	ca := mkCert(t, "ca", nil, true, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	server := valid(t, "server", &ca)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{server.pair}, MaxVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	run := func(min uint16) error {
		m := &link.Module{Client: &http.Client{}}
		m.Cfg.TLS = config.TLS{PeerCA: writeTemp(t, ca.pem), MinVersion: min}
		cl, err := m.ClientFor(server.fp())
		if err != nil {
			return err
		}
		res, err := cl.Get(srv.URL)
		if err == nil {
			_ = res.Body.Close()
		}
		return err
	}
	if err := run(0); err == nil {
		t.Fatal("the default minimum is TLS 1.3: a TLS 1.2 peer must be refused")
	}
	if err := run(tls.VersionTLS13); err == nil {
		t.Fatal("explicit 1.3 must refuse a TLS 1.2 peer")
	}
	if err := run(tls.VersionTLS12); err != nil {
		t.Fatalf("1.2 explicitly allowed: %v", err)
	}
}

// --- integration: two nodes over TLS ---

type tlsNodes struct {
	a, b       *testkit.Env
	la, lb     *link.Module
	srvB       *httptest.Server
	ca         cert
	serverB    cert
	clientA    cert
	bPeerOfA   string // id of A's peer record on B
	aPeerOfB   string // id of B's peer record on A
	bDID       string
	aDID       string
	aPub, bPub string
}

func setupTLSNodes(t *testing.T, pinOfB string) *tlsNodes {
	t.Helper()
	n := &tlsNodes{a: testkit.New(t), b: testkit.New(t)}
	n.la, n.lb = linkOf(n.a), linkOf(n.b)
	n.ca = mkCert(t, "ca", nil, true, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	n.serverB = valid(t, "node-b", &n.ca)
	n.clientA = valid(t, "node-a", nil) // self-signed: the pin is the only trust anchor on B
	ctx := context.Background()
	na, err := n.la.Node(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := n.lb.Node(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n.aDID, n.bDID = na.DID, nb.DID
	n.aPub, n.bPub = base64.RawURLEncoding.EncodeToString(na.Pub), base64.RawURLEncoding.EncodeToString(nb.Pub)

	n.srvB = httptest.NewUnstartedServer(n.b.App.Router)
	n.srvB.TLS = &tls.Config{Certificates: []tls.Certificate{n.serverB.pair}, ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS13}
	n.srvB.StartTLS()
	t.Cleanup(n.srvB.Close)

	n.la.Cfg.TLS = config.TLS{PeerCA: writeTemp(t, n.ca.pem), ClientCert: writeTemp(t, n.clientA.pem), ClientKey: writeTemp(t, n.clientA.keyP)}
	n.lb.Cfg.TLS = config.TLS{RequireFederationClientCert: true}

	if pinOfB == "" {
		pinOfB = n.serverB.fp()
	}
	var pa, pb struct{ ID, TLSFingerprint string }
	// A knows B by its TLS address and pinned server certificate; the DID is fetched through the pinned client.
	n.a.Must(http.StatusCreated, n.a.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "node B", "url": n.srvB.URL, "remoteTenant": "acme", "tlsFingerprint": pinOfB}), &pa)
	n.a.Must(http.StatusOK, n.a.Do("POST", "/api/v1/federation/peers/"+pa.ID+"/approve", nil), nil)
	n.aPeerOfB = pa.ID
	// B knows A by the pinned client certificate (upper-case, colon-separated on purpose).
	upper := strings.ToUpper(strings.Join(splitPairs(n.clientA.fp()), ":"))
	n.b.Must(http.StatusCreated, n.b.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "node A", "url": "https://node-a.invalid", "remoteTenant": "acme",
		"publicKey": n.aPub, "did": n.aDID, "tlsFingerprint": upper}), &pb)
	if pb.TLSFingerprint != n.clientA.fp() {
		t.Fatalf("fingerprint must be stored normalised: %q", pb.TLSFingerprint)
	}
	n.b.Must(http.StatusOK, n.b.Do("POST", "/api/v1/federation/peers/"+pb.ID+"/approve", nil), nil)
	n.bPeerOfA = pb.ID
	return n
}

func (n *tlsNodes) lastStatus(t *testing.T) (status string, detail map[string]any) {
	t.Helper()
	var out struct {
		Items []struct {
			Status string
			Detail map[string]any
		}
	}
	n.a.Must(http.StatusOK, n.a.Do("GET", "/api/v1/federation/messages", nil), &out)
	if len(out.Items) == 0 {
		t.Fatal("no outbound message")
	}
	return out.Items[0].Status, out.Items[0].Detail
}

func TestFederationMutualTLSDelivery(t *testing.T) {
	n := setupTLSNodes(t, "")
	n.a.Must(http.StatusAccepted, n.a.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": n.aPeerOfB, "type": "ping"}), nil)
	n.a.Drain()
	if st, d := n.lastStatus(t); st != "delivered" {
		t.Fatalf("pinned delivery with a client certificate: %s %v", st, d)
	}
	var inbound struct {
		Items []struct{ Direction, Type string }
	}
	n.b.Must(http.StatusOK, n.b.Do("GET", "/api/v1/federation/messages", nil), &inbound)
	if len(inbound.Items) != 1 || inbound.Items[0].Direction != "inbound" {
		t.Fatalf("node B messages: %+v", inbound.Items)
	}
}

func TestFederationDeliveryFailsOnPinMismatch(t *testing.T) {
	// The peer record on A pins a different certificate than node B presents. Adding the peer
	// resolves the DID through the pinned client, so the mismatch is caught there too.
	a, b := testkit.New(t), testkit.New(t)
	la, lb := linkOf(a), linkOf(b)
	ca := mkCert(t, "ca", nil, true, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	serverB, wrong := valid(t, "node-b", &ca), valid(t, "impostor", &ca)
	srv := httptest.NewUnstartedServer(b.App.Router)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverB.pair}}
	srv.StartTLS()
	defer srv.Close()
	la.Cfg.TLS = config.TLS{PeerCA: writeTemp(t, ca.pem)}
	nb, _ := lb.Node(context.Background())
	pub := base64.RawURLEncoding.EncodeToString(nb.Pub)

	problem(t, a.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "B", "url": srv.URL, "remoteTenant": "acme", "tlsFingerprint": wrong.fp()}), 400, "cannot resolve peer DID")

	// With the keys given explicitly the peer can be stored; every delivery must then fail.
	var p struct{ ID string }
	a.Must(http.StatusCreated, a.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "B", "url": srv.URL, "remoteTenant": "acme", "publicKey": pub, "did": nb.DID, "tlsFingerprint": wrong.fp()}), &p)
	a.Must(http.StatusOK, a.Do("POST", "/api/v1/federation/peers/"+p.ID+"/approve", nil), nil)
	a.Must(http.StatusAccepted, a.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": p.ID, "type": "ping"}), nil)
	a.Drain()
	var out struct {
		Items []struct {
			Status string
			Detail map[string]any
		}
	}
	a.Must(http.StatusOK, a.Do("GET", "/api/v1/federation/messages", nil), &out)
	if len(out.Items) != 1 || out.Items[0].Status != "retrying" || !strings.Contains(out.Items[0].Detail["error"].(string), "pinned fingerprint") {
		t.Fatalf("mismatch must not deliver: %+v", out.Items)
	}
	var inbound struct{ Items []any }
	b.Must(http.StatusOK, b.Do("GET", "/api/v1/federation/messages", nil), &inbound)
	if len(inbound.Items) != 0 {
		t.Fatalf("nothing may arrive at the impostor-pinned node: %v", inbound.Items)
	}
}

func TestFederationRefusesPinnedPeerOverPlainHTTP(t *testing.T) {
	n := setupTLSNodes(t, "")
	problem(t, n.a.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "x", "url": "http://example.test", "remoteTenant": "acme", "publicKey": n.bPub, "did": n.bDID, "tlsFingerprint": n.serverB.fp()}), 400, "needs an https peer url")
	problem(t, n.a.Do("POST", "/api/v1/federation/peers", map[string]any{"name": "x", "url": n.srvB.URL, "remoteTenant": "acme", "publicKey": n.bPub, "did": n.bDID, "tlsFingerprint": "zz"}), 400, "tlsFingerprint")

	// Even if the stored URL is changed to http afterwards, delivery refuses to send unpinned.
	ctx := context.Background()
	c, err := pgx.Connect(ctx, n.a.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, `update federation_peer set url = replace(url, 'https://', 'http://') where id=$1`, n.aPeerOfB); err != nil {
		t.Fatal(err)
	}
	n.a.Must(http.StatusAccepted, n.a.Do("POST", "/api/v1/federation/send", map[string]any{"peerId": n.aPeerOfB, "type": "ping"}), nil)
	n.a.Drain()
	if st, d := n.lastStatus(t); st != "retrying" || !strings.Contains(d["error"].(string), "not https") {
		t.Fatalf("%s %v", st, d)
	}
}

func TestFederationInboxEnforcesClientCertificate(t *testing.T) {
	n := setupTLSNodes(t, "")
	ctx := context.Background()
	na, _ := n.la.Node(ctx)
	seq := 0
	post := func(c *tls.Certificate) (int, string) {
		seq++
		env := na.Sign(link.Envelope{ID: "mtls-" + string(rune('a'+seq)), Type: "ping", From: n.aDID, FromTenant: "acme", To: n.bDID, ToTenant: "acme", CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
		body, _ := json.Marshal(env)
		cfg := &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS13}
		cfg.RootCAs.AddCert(n.ca.x)
		if c != nil {
			cfg.Certificates = []tls.Certificate{*c}
		}
		cl := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		res, err := cl.Post(n.srvB.URL+"/api/v1/federation/inbox", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, body := post(nil); code != http.StatusUnauthorized || !strings.Contains(body, "client certificate required") {
		t.Fatalf("no client certificate: %d %s", code, body)
	}
	stranger := valid(t, "stranger", nil)
	if code, _ := post(&stranger.pair); code != http.StatusForbidden {
		t.Fatalf("certificate that is not pinned for the sender: %d", code)
	}
	// The same signed envelope with the pinned certificate is accepted.
	if code, body := post(&n.clientA.pair); code != http.StatusAccepted {
		t.Fatalf("pinned certificate: %d %s", code, body)
	}
	// Only the inbox is affected: the rest of the API needs no client certificate.
	cfg := &tls.Config{RootCAs: x509.NewCertPool()}
	cfg.RootCAs.AddCert(n.ca.x)
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	for _, p := range []string{"/healthz", "/.well-known/did.json"} {
		res, err := cl.Get(n.srvB.URL + p)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("%s without client certificate: %v %v", p, res, err)
		}
		_ = res.Body.Close()
	}

	// A peer record without a pin is refused when enforcement is on.
	sup, err := pgx.Connect(ctx, n.b.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close(ctx)
	if _, err := sup.Exec(ctx, `update federation_peer set tls_fingerprint=''`); err != nil {
		t.Fatal(err)
	}
	if code, _ := post(&n.clientA.pair); code != http.StatusForbidden {
		t.Fatalf("peer without pin: %d", code)
	}
	// An expired certificate is refused even when its fingerprint is the pinned one.
	expired := mkCert(t, "node-a-old", nil, false, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	if _, err := sup.Exec(ctx, `update federation_peer set tls_fingerprint=$1`, expired.fp()); err != nil {
		t.Fatal(err)
	}
	if code, _ := post(&expired.pair); code != http.StatusForbidden {
		t.Fatalf("expired certificate: %d", code)
	}
	// Enforcement off (a proxy terminates TLS): no certificate needed.
	n.lb.Cfg.TLS.RequireFederationClientCert = false
	if code, body := post(nil); code != http.StatusAccepted {
		t.Fatalf("enforcement off: %d %s", code, body)
	}
}

func TestFederationInboxEnforcementWithoutTLSFailsClosed(t *testing.T) {
	// Enforcement on, but the request arrived without TLS (for example a misconfigured proxy).
	e := testkit.New(t)
	l := linkOf(e)
	l.Cfg.TLS = config.TLS{RequireFederationClientCert: true}
	res, err := http.Post(e.Server.URL+"/api/v1/federation/inbox", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", res.StatusCode)
	}
}
