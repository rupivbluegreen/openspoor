package link

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBase58AndMultikey(t *testing.T) {
	for _, b := range [][]byte{{}, {0}, {0, 0, 1}, []byte("hello world"), bytes.Repeat([]byte{0xff}, 40)} {
		got, err := unbase58btc(base58btc(b))
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("round trip %x: %x %v", b, got, err)
		}
	}
	if base58btc([]byte("hello world")) != "StV1DL6CwTryKyV" {
		t.Fatal("base58btc vector")
	}
	if _, err := unbase58btc("0OIl"); err == nil {
		t.Fatal("invalid base58 accepted")
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	mk := multikeyEd25519(pub)
	if !strings.HasPrefix(mk, "z6Mk") {
		t.Fatalf("multikey prefix: %s", mk)
	}
	if k, err := parseMultikeyEd25519(mk); err != nil || !bytes.Equal(k, pub) {
		t.Fatal("multikey round trip")
	}
	for _, bad := range []string{"m123", "z0", "z" + base58btc([]byte{1, 2, 3})} {
		if _, err := parseMultikeyEd25519(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestDIDWebURL(t *testing.T) {
	m := &Module{}
	for did, want := range map[string]string{
		"did:web:example.com":            "https://example.com/.well-known/did.json",
		"did:web:localhost%3A8443":       "https://localhost:8443/.well-known/did.json",
		"did:web:example.com:user:alice": "https://example.com/user/alice/did.json",
		"did:web:example.com:a%20b":      "https://example.com/a%20b/did.json",
		"did:web:":                       "",
		"did:key:z6Mk":                   "",
		"did:web:ex%2Fample.com":         "",
		"did:web:example.com::x":         "",
		"did:web:example.com:%zz":        "",
		"did:web:user@example.com":       "",
		"did:web:example.com:a%2Fb":      "",
		"did:web:%zz":                    "",
	} {
		got, err := m.didWebURL(did)
		if (want == "") != (err != nil) || got != want {
			t.Errorf("%s: %q %v", did, got, err)
		}
	}
	m.InsecureDIDWeb = true
	if u, _ := m.didWebURL("did:web:example.com"); !strings.HasPrefix(u, "http://") {
		t.Fatal("insecure scheme")
	}
}

// fixture serves a DID document and a status list for a test issuer.
type fixture struct {
	srv    *httptest.Server
	m      *Module
	n      *nodeIdentity
	didDoc func(n *nodeIdentity) map[string]any
	list   func() map[string]any
	status int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			return
		}
		var v any
		switch r.URL.Path {
		case "/.well-known/did.json":
			v = f.didDoc(f.n)
		case statusPath:
			v = f.list()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(f.srv.Close)
	pub, priv, _ := ed25519.GenerateKey(nil)
	f.n = &nodeIdentity{DID: DIDFor(f.srv.URL), Pub: pub, priv: priv}
	f.m = &Module{Client: f.srv.Client(), InsecureDIDWeb: true}
	f.m.Cfg.PublicURL = f.srv.URL
	f.didDoc = didDocument
	f.list = func() map[string]any { return f.statusList(t, -1) }
	return f
}

func (f *fixture) statusList(t *testing.T, revoked int) map[string]any {
	bits := make([]byte, statusListBits/8)
	if revoked >= 0 {
		bits[revoked/8] |= 0x80 >> (revoked % 8)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(bits)
	_ = zw.Close()
	vc := map[string]any{"@context": []any{vcContextV2}, "id": f.m.statusListURL(), "type": []any{"VerifiableCredential", "BitstringStatusListCredential"},
		"issuer": f.n.DID, "credentialSubject": map[string]any{"type": "BitstringStatusList", "statusPurpose": "revocation",
			"encodedList": "u" + base64.RawURLEncoding.EncodeToString(gz.Bytes())}}
	s, err := f.n.signDataIntegrity(vc, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixture) credential(t *testing.T, mut func(map[string]any)) map[string]any {
	t.Helper()
	vc := map[string]any{"@context": []any{vcContextV2}, "type": []any{"VerifiableCredential", atpType}, "issuer": f.n.DID,
		"validFrom": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "credentialSubject": map[string]any{"name": "X"},
		"credentialStatus": map[string]any{"type": "BitstringStatusListEntry", "statusPurpose": "revocation", "statusListIndex": "7", "statusListCredential": f.m.statusListURL()}}
	if mut != nil {
		mut(vc)
	}
	s, err := f.n.signDataIntegrity(vc, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func failed(checks []Check, name string) string {
	for _, c := range checks {
		if c.Name == name && !c.OK {
			return c.Detail
		}
	}
	return ""
}

func TestVerifyCredentialChecks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ok := f.credential(t, nil)
	for _, c := range f.m.VerifyCredential(ctx, ok) {
		if !c.OK {
			t.Fatalf("valid credential: %+v", c)
		}
	}

	// Validity window and status entry problems (signed by the issuer, so only that check fails).
	cases := map[string]struct {
		mut   func(map[string]any)
		check string
		want  string
	}{
		"not yet valid": {func(v map[string]any) { v["validFrom"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }, "validity", "not valid before"},
		"expired":       {func(v map[string]any) { v["validUntil"] = "2020-01-01T00:00:00Z" }, "validity", "expired"},
		"bad date":      {func(v map[string]any) { v["validUntil"] = "tomorrow" }, "validity", "dateTimeStamp"},
		"bad from":      {func(v map[string]any) { v["validFrom"] = 5 }, "validity", "date-time string"},
		"no status":     {func(v map[string]any) { delete(v, "credentialStatus") }, "status", "no credentialStatus"},
		"other status":  {func(v map[string]any) { v["credentialStatus"] = map[string]any{"type": "StatusList2021Entry"} }, "status", "unsupported"},
		"bad index": {func(v map[string]any) {
			v["credentialStatus"].(map[string]any)["statusListIndex"] = "x"
		}, "status", "statusListIndex"},
		"big index": {func(v map[string]any) {
			v["credentialStatus"].(map[string]any)["statusListIndex"] = "99999999"
		}, "status", "outside"},
		"ftp list": {func(v map[string]any) {
			v["credentialStatus"].(map[string]any)["statusListCredential"] = "ftp://x/y"
		}, "status", "https"},
		"not ATP": {func(v map[string]any) { v["type"] = "VerifiableCredential" }, "type", "not an"},
	}
	for name, c := range cases {
		got := failed(f.m.VerifyCredential(ctx, f.credential(t, c.mut)), c.check)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %s check %q", name, c.check, got)
		}
	}

	// Proof problems.
	proofCases := map[string]struct {
		mut  func(map[string]any)
		want string
	}{
		"no proof":      {func(v map[string]any) { delete(v, "proof") }, "no single proof"},
		"suite":         {func(v map[string]any) { v["proof"].(map[string]any)["cryptosuite"] = "ecdsa-rdfc-2019" }, "unsupported proof"},
		"purpose":       {func(v map[string]any) { v["proof"].(map[string]any)["proofPurpose"] = "authentication" }, "assertionMethod"},
		"value prefix":  {func(v map[string]any) { v["proof"].(map[string]any)["proofValue"] = "uabc" }, "base58btc"},
		"value length":  {func(v map[string]any) { v["proof"].(map[string]any)["proofValue"] = "z2" }, "not an Ed25519 signature"},
		"foreign vm":    {func(v map[string]any) { v["proof"].(map[string]any)["verificationMethod"] = "did:web:evil#key-2" }, "not controlled"},
		"unknown vm":    {func(v map[string]any) { v["proof"].(map[string]any)["verificationMethod"] = f.n.DID + "#key-9" }, "not an assertion method"},
		"proof context": {func(v map[string]any) { v["proof"].(map[string]any)["@context"] = []any{"https://example.com/other"} }, "@context does not match"},
	}
	for name, c := range proofCases {
		vc := f.credential(t, nil)
		c.mut(vc)
		if got := failed(f.m.VerifyCredential(ctx, vc), "proof"); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q", name, got)
		}
	}
	// A credential without @context fails the format check before anything else.
	noCtx := f.credential(t, func(v map[string]any) { delete(v, "@context") })
	if got := failed(f.m.VerifyCredential(ctx, noCtx), "format"); got == "" {
		t.Error("missing @context accepted")
	}
	for _, mut := range []func(map[string]any){func(v map[string]any) { v["type"] = []any{"X"} }, func(v map[string]any) { delete(v, "credentialSubject") }} {
		if got := failed(f.m.VerifyCredential(ctx, f.credential(t, mut)), "format"); got == "" {
			t.Error("bad format accepted")
		}
	}

	// DID document problems.
	f.didDoc = func(n *nodeIdentity) map[string]any { d := didDocument(n); d["id"] = "did:web:other"; return d }
	if got := failed(f.m.VerifyCredential(ctx, ok), "proof"); !strings.Contains(got, "does not match the issuer") {
		t.Errorf("DID id: %q", got)
	}
	f.didDoc = func(n *nodeIdentity) map[string]any {
		d := didDocument(n)
		d["verificationMethod"] = []map[string]any{{"id": n.DID + "#key-2", "type": "Multikey", "publicKeyJwk": map[string]string{"kty": "EC"}}}
		return d
	}
	if got := failed(f.m.VerifyCredential(ctx, ok), "proof"); !strings.Contains(got, "no Ed25519 key") {
		t.Errorf("no key: %q", got)
	}
	f.didDoc = func(n *nodeIdentity) map[string]any {
		d := didDocument(n)
		d["verificationMethod"] = []map[string]any{{"id": "#key-2", "type": "JsonWebKey2020",
			"publicKeyJwk": map[string]string{"kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(n.Pub)}}}
		d["assertionMethod"] = []string{"#key-2"}
		return d
	}
	if got := failed(f.m.VerifyCredential(ctx, ok), "proof"); got != "" {
		t.Errorf("relative JWK method: %q", got)
	}
	f.didDoc = func(n *nodeIdentity) map[string]any {
		d := didDocument(n)
		d["verificationMethod"] = []map[string]any{}
		return d
	}
	if got := failed(f.m.VerifyCredential(ctx, ok), "proof"); !strings.Contains(got, "not found") {
		t.Errorf("missing method: %q", got)
	}
	f.didDoc = didDocument

	// Status list problems.
	f.list = func() map[string]any { return f.statusList(t, 7) }
	if got := failed(f.m.VerifyCredential(ctx, ok), "status"); !strings.Contains(got, "revoked") {
		t.Errorf("revoked: %q", got)
	}
	listCases := map[string]struct {
		mut  func(map[string]any)
		want string
	}{
		"issuer":   {func(l map[string]any) { l["issuer"] = "did:web:other" }, "not a BitstringStatusListCredential"},
		"proof":    {func(l map[string]any) { l["credentialSubject"].(map[string]any)["encodedList"] = "uAAAA" }, "status list proof"},
		"expired":  {func(l map[string]any) { l["validUntil"] = "2020-01-01T00:00:00Z" }, "expired"},
		"purpose":  {func(l map[string]any) { l["credentialSubject"].(map[string]any)["statusPurpose"] = "suspension" }, "purpose"},
		"encoding": {func(l map[string]any) { l["credentialSubject"].(map[string]any)["encodedList"] = "zabc" }, "multibase"},
		"base64":   {func(l map[string]any) { l["credentialSubject"].(map[string]any)["encodedList"] = "u!!" }, "base64url"},
		"gzip":     {func(l map[string]any) { l["credentialSubject"].(map[string]any)["encodedList"] = "uAAAA" }, "GZIP"},
	}
	for name, c := range listCases {
		resign := name != "proof"
		f.list = func() map[string]any {
			l := f.statusList(t, -1)
			delete(l, "proof")
			c.mut(l)
			if !resign {
				l2 := f.statusList(t, -1)
				l["proof"] = l2["proof"]
				return l
			}
			s, _ := f.n.signDataIntegrity(l, time.Now())
			return s
		}
		if got := failed(f.m.VerifyCredential(ctx, ok), "status"); !strings.Contains(got, c.want) {
			t.Errorf("list %s: %q", name, got)
		}
	}
	f.status = http.StatusInternalServerError
	if got := failed(f.m.VerifyCredential(ctx, ok), "proof"); !strings.Contains(got, "HTTP 500") {
		t.Errorf("unreachable issuer: %q", got)
	}
}
