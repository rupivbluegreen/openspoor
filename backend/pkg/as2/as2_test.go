package as2_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/pkg/as2"
)

func newStation(t *testing.T, id string) as2.Station {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: id},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return as2.Station{ID: id, Cert: c, Key: k}
}

func peerOf(s as2.Station) as2.Peer { return as2.Peer{ID: s.ID, Cert: s.Cert} }

func hdr(w *as2.Wire) http.Header {
	h := http.Header{}
	for k, v := range w.Header {
		h.Set(k, v)
	}
	return h
}

var payload = []byte(`{"@context":"x","note":"line one\nline two"}` + "\r\n")

func send(t *testing.T, a, b as2.Station, sign, enc bool) (*as2.Wire, *as2.Received) {
	t.Helper()
	w, err := as2.Build(a, peerOf(b), as2.Outgoing{MessageID: as2.NewMessageID(a.ID), Subject: "TI/TS", Filename: "tits.json", ContentType: "application/ld+json",
		Payload: payload, Sign: sign, Encrypt: enc, MDN: as2.MDNSync})
	if err != nil {
		t.Fatal(err)
	}
	r, err := as2.Open(hdr(w), w.Body, b, peerOf(a), as2.Policy{}, time.Now())
	if err != nil {
		t.Fatalf("open (sign=%v enc=%v): %v", sign, enc, err)
	}
	return w, r
}

func TestRoundTripAllModes(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	for _, m := range []struct{ sign, enc bool }{{true, true}, {true, false}, {false, true}, {false, false}} {
		w, r := send(t, a, b, m.sign, m.enc)
		if string(r.Payload) != string(payload) || r.ContentType != "application/ld+json" || r.Filename != "tits.json" {
			t.Fatalf("payload mismatch %+v: %q %q %q", m, r.Payload, r.ContentType, r.Filename)
		}
		if r.Signed != m.sign || r.Encrypted != m.enc || r.MIC != w.MIC || r.From != "NODE-A" || r.To != "NODE-B" || r.MDN != as2.MDNSync {
			t.Fatalf("flags %+v: %+v (want mic %s)", m, r, w.MIC)
		}
		if m.enc && strings.Contains(string(w.Body), "line one") {
			t.Fatal("encrypted body leaks the payload")
		}
	}
}

func TestPolicy(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	w, _ := send(t, a, b, false, false)
	for _, pol := range []as2.Policy{{RequireSigned: true}, {RequireEncrypted: true}} {
		_, err := as2.Open(hdr(w), w.Body, b, peerOf(a), pol, time.Now())
		var f *as2.Failure
		if !errors.As(err, &f) || f.Code != as2.ErrInsufficient || f.Error() == "" || f.Unwrap() == nil {
			t.Fatalf("policy %+v: %v", pol, err)
		}
	}
}

func openErr(t *testing.T, w *as2.Wire, body []byte, b as2.Station, peer as2.Peer, now time.Time) string {
	t.Helper()
	_, err := as2.Open(hdr(w), body, b, peer, as2.Policy{}, now)
	var f *as2.Failure
	if !errors.As(err, &f) {
		t.Fatalf("expected a failure, got %v", err)
	}
	return f.Code
}

func TestBadSignatures(t *testing.T) {
	a, b, evil := newStation(t, "NODE-A"), newStation(t, "NODE-B"), newStation(t, "NODE-A")
	w, _ := send(t, a, b, true, false)

	// Payload tampered after signing.
	tampered := []byte(strings.Replace(string(w.Body), "line one", "line ONE", 1))
	if c := openErr(t, w, tampered, b, peerOf(a), time.Now()); c != as2.ErrIntegrity {
		t.Fatalf("tampered: %s", c)
	}
	// Signed by someone else (same AS2 id, different certificate): authentication fails.
	w2, _ := send(t, evil, b, true, false)
	if c := openErr(t, w2, w2.Body, b, peerOf(a), time.Now()); c != as2.ErrAuthentication {
		t.Fatalf("wrong signer: %s", c)
	}
	// Partner certificate expired by the time of receipt.
	if c := openErr(t, w, w.Body, b, peerOf(a), time.Now().Add(48*time.Hour)); c != as2.ErrAuthentication {
		t.Fatalf("expired: %s", c)
	}
	// No certificate on file.
	if c := openErr(t, w, w.Body, b, as2.Peer{ID: "NODE-A"}, time.Now()); c != as2.ErrAuthentication {
		t.Fatalf("no cert: %s", c)
	}
	// Same, inside an encrypted envelope.
	we, _ := send(t, evil, b, true, true)
	if c := openErr(t, we, we.Body, b, peerOf(a), time.Now()); c != as2.ErrAuthentication {
		t.Fatalf("encrypted wrong signer: %s", c)
	}
}

func TestDecryptionAndMalformed(t *testing.T) {
	a, b, c := newStation(t, "NODE-A"), newStation(t, "NODE-B"), newStation(t, "NODE-C")
	w, _ := send(t, a, b, true, true)
	if code := openErr(t, w, w.Body, c, peerOf(a), time.Now()); code != as2.ErrDecryption {
		t.Fatalf("wrong key: %s", code)
	}
	if code := openErr(t, w, []byte("junk"), b, peerOf(a), time.Now()); code != as2.ErrDecryption {
		t.Fatalf("junk: %s", code)
	}
	if code := openErr(t, w, w.Body, as2.Station{ID: "NODE-B"}, peerOf(a), time.Now()); code != as2.ErrDecryption {
		t.Fatalf("no key: %s", code)
	}
	ws, _ := send(t, a, b, true, false)
	for name, mut := range map[string]func(*as2.Wire){
		"missing from":    func(w *as2.Wire) { delete(w.Header, "AS2-From") },
		"bad message id":  func(w *as2.Wire) { w.Header["Message-ID"] = "<a b>" },
		"bad receipt url": func(w *as2.Wire) { w.Header["Receipt-Delivery-Option"] = "http://x y" },
		"micalg": func(w *as2.Wire) {
			w.Header["Content-Type"] = strings.Replace(w.Header["Content-Type"], "sha-256", "sha1", 1)
		},
		"not multipart": func(w *as2.Wire) { w.Header["Content-Type"] = "multipart/signed; boundary=nope" },
		"bad cte": func(w *as2.Wire) {
			w.Header["Content-Type"] = `application/pkcs7-mime; smime-type=enveloped-data`
			w.Header["Content-Transfer-Encoding"] = "quoted-printable"
		},
	} {
		cp := &as2.Wire{Header: map[string]string{}, Body: ws.Body}
		for k, v := range ws.Header {
			cp.Header[k] = v
		}
		mut(cp)
		if _, err := as2.Open(hdr(cp), cp.Body, b, peerOf(a), as2.Policy{}, time.Now()); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// A single-part "signed" message, a missing payload type and a non-signature second part.
	for name, body := range map[string]string{
		"one part":   "--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B--\r\n",
		"not p7s":    "--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B\r\nContent-Type: text/plain\r\n\r\ny\r\n--B--\r\n",
		"bad p7s":    "--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B\r\nContent-Type: application/pkcs7-signature\r\n\r\ny\r\n--B--\r\n",
		"b64 p7s":    "--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B\r\nContent-Type: application/pkcs7-signature\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!\r\n--B--\r\n",
		"no headers": "--B\r\n\r\nx\r\n--B\r\nContent-Type: application/pkcs7-signature\r\n\r\ny\r\n--B--\r\n",
	} {
		h := http.Header{}
		h.Set("AS2-From", "NODE-A")
		h.Set("AS2-To", "NODE-B")
		h.Set("Message-ID", "<m1@x>")
		h.Set("Content-Type", `multipart/signed; micalg=sha-256; boundary=B; protocol="application/pkcs7-signature"`)
		if _, err := as2.Open(h, []byte(body), b, peerOf(a), as2.Policy{}, time.Now()); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// Payload without Content-Type.
	h := http.Header{}
	h.Set("AS2-From", "NODE-A")
	h.Set("AS2-To", "NODE-B")
	h.Set("Message-ID", "<m2@x>")
	if _, err := as2.Open(h, []byte("x"), b, peerOf(a), as2.Policy{}, time.Now()); err == nil {
		t.Fatal("accepted a message without content type")
	}
}

func TestBuildValidation(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	ok := as2.Outgoing{MessageID: "m1@x", ContentType: "text/plain", Payload: []byte("x")}
	cases := map[string]func(*as2.Outgoing, *as2.Station, *as2.Peer){
		"bad station id": func(_ *as2.Outgoing, s *as2.Station, _ *as2.Peer) { s.ID = "a b" },
		"bad peer id":    func(_ *as2.Outgoing, _ *as2.Station, p *as2.Peer) { p.ID = "" },
		"bad message id": func(o *as2.Outgoing, _ *as2.Station, _ *as2.Peer) { o.MessageID = "a\r\nb" },
		"bad type":       func(o *as2.Outgoing, _ *as2.Station, _ *as2.Peer) { o.ContentType = "" },
		"bad subject":    func(o *as2.Outgoing, _ *as2.Station, _ *as2.Peer) { o.Subject = "a\nb" },
		"async no url":   func(o *as2.Outgoing, _ *as2.Station, _ *as2.Peer) { o.MDN = as2.MDNAsync },
		"encrypt no cert": func(o *as2.Outgoing, _ *as2.Station, p *as2.Peer) {
			o.Encrypt = true
			p.Cert = nil
		},
		"sign no key": func(o *as2.Outgoing, s *as2.Station, _ *as2.Peer) {
			o.Sign = true
			s.Key = nil
		},
		"ec key": func(o *as2.Outgoing, s *as2.Station, _ *as2.Peer) {
			o.Sign = true
			s.Key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		},
	}
	for name, mut := range cases {
		o, s, p := ok, a, peerOf(b)
		mut(&o, &s, &p)
		if name == "ec key" {
			// ECDSA signing works; only RSA decryption is supported. Not an error here.
			if _, err := as2.Build(s, p, o); err != nil {
				t.Fatalf("ec: %v", err)
			}
			continue
		}
		if _, err := as2.Build(s, p, o); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	w, err := as2.Build(a, peerOf(b), as2.Outgoing{MessageID: "m1@x", ContentType: "text/plain", Payload: []byte("x"), MDN: as2.MDNAsync, AsyncURL: "https://a.example/rx"})
	if err != nil {
		t.Fatal(err)
	}
	hd, err := as2.ParseHeader(hdr(w))
	if err != nil || hd.MDN != as2.MDNAsync || hd.ReceiptURL != "https://a.example/rx" || hd.MessageID != "m1@x" {
		t.Fatalf("%+v %v", hd, err)
	}
	if !as2.ValidID("A-1.b:c@d") || as2.ValidID("") || as2.ValidID(strings.Repeat("a", 129)) {
		t.Fatal("ValidID")
	}
	if as2.Fingerprint(a.Cert) == as2.Fingerprint(b.Cert) || len(as2.Fingerprint(a.Cert)) != 64 {
		t.Fatal("Fingerprint")
	}
}

func TestMDN(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	w, r := send(t, a, b, true, true)
	mdn, err := as2.BuildMDN(b, as2.MDNInput{MessageID: "r1@b", OriginalMessageID: r.MessageID, To: r.From, MIC: r.MIC, Sign: true})
	if err != nil {
		t.Fatal(err)
	}
	if !as2.IsMDN(hdr(mdn), mdn.Body) || as2.IsMDN(hdr(w), w.Body) {
		t.Fatal("IsMDN")
	}
	m, err := as2.ParseMDN(hdr(mdn), mdn.Body, peerOf(b), true, time.Now())
	if err != nil || !m.Processed || !m.Signed || m.MIC != w.MIC || m.MICAlg != "sha-256" || m.OriginalMessageID != r.MessageID || m.From != "NODE-B" || m.To != "NODE-A" {
		t.Fatalf("%+v %v", m, err)
	}

	// Tampered receipt body, wrong signer and unsigned receipt where a signed one is required.
	bad := []byte(strings.Replace(string(mdn.Body), "processed", "processeD", 1))
	if _, err := as2.ParseMDN(hdr(mdn), bad, peerOf(b), true, time.Now()); err == nil {
		t.Fatal("tampered receipt accepted")
	}
	if _, err := as2.ParseMDN(hdr(mdn), mdn.Body, peerOf(a), true, time.Now()); err == nil {
		t.Fatal("receipt signed by another party accepted")
	}
	plain, _ := as2.BuildMDN(b, as2.MDNInput{MessageID: "r2@b", OriginalMessageID: r.MessageID, To: r.From, MIC: r.MIC})
	if !as2.IsMDN(hdr(plain), plain.Body) {
		t.Fatal("IsMDN unsigned")
	}
	if _, err := as2.ParseMDN(hdr(plain), plain.Body, peerOf(b), true, time.Now()); err == nil {
		t.Fatal("unsigned receipt accepted when signed required")
	}
	if m, err := as2.ParseMDN(hdr(plain), plain.Body, peerOf(b), false, time.Now()); err != nil || m.Signed || !m.Processed {
		t.Fatalf("unsigned ok: %+v %v", m, err)
	}

	// Error receipts.
	er, _ := as2.BuildMDN(b, as2.MDNInput{MessageID: "r3@b", OriginalMessageID: "x@y", To: "NODE-A", ErrorCode: as2.ErrAuthentication, Text: "bad\r\nsig", Sign: true})
	m, err = as2.ParseMDN(hdr(er), er.Body, peerOf(b), true, time.Now())
	if err != nil || m.Processed || m.ErrorCode != as2.ErrAuthentication || m.MIC != "" {
		t.Fatalf("error mdn: %+v %v", m, err)
	}

	for name, in := range map[string]as2.MDNInput{
		"bad to":   {MessageID: "a@b", OriginalMessageID: "c@d", To: "a b"},
		"bad orig": {MessageID: "a@b", OriginalMessageID: "", To: "NODE-A"},
	} {
		if _, err := as2.BuildMDN(b, in); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := as2.BuildMDN(as2.Station{ID: "NODE-B"}, as2.MDNInput{MessageID: "a@b", OriginalMessageID: "c@d", To: "NODE-A", Sign: true}); err == nil {
		t.Fatal("unsigned station signed")
	}

	// Malformed receipts.
	for name, c := range map[string]struct{ ct, body string }{
		"not report":  {"text/plain", "x"},
		"one part":    {`multipart/report; boundary=B`, "--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B--\r\n"},
		"no fields":   {`multipart/report; boundary=B`, "--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B\r\nContent-Type: text/plain\r\n\r\ny\r\n--B--\r\n"},
		"no orig id":  {`multipart/report; boundary=B`, "--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B\r\nContent-Type: message/disposition-notification\r\n\r\nDisposition: a; processed\r\n\r\n--B--\r\n"},
		"signed junk": {`multipart/signed; boundary=B`, "--B\r\n\r\nx\r\n--B--\r\n"},
	} {
		h := http.Header{}
		h.Set("Content-Type", c.ct)
		if _, err := as2.ParseMDN(h, []byte(c.body), peerOf(b), false, time.Now()); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	for _, ct := range []string{"text/plain", "multipart/signed; boundary=B", "multipart/signed"} {
		h := http.Header{}
		h.Set("Content-Type", ct)
		if as2.IsMDN(h, []byte("--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B--\r\n")) {
			t.Fatalf("IsMDN(%s)", ct)
		}
	}
}
