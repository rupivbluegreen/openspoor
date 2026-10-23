package as2_test

import (
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/smallstep/pkcs7"

	"github.com/openspoor/openspoor/pkg/as2"
)

var bigPayload = []byte(strings.Repeat(`{"epc":"urn:epc:id:sgtin:0614141.107346.2018"},`, 400))

func build(t *testing.T, a, b as2.Station, o as2.Outgoing) *as2.Wire {
	t.Helper()
	o.MessageID, o.ContentType, o.Payload, o.MDN = as2.NewMessageID(a.ID), "application/ld+json", bigPayload, as2.MDNSync
	w, err := as2.Build(a, peerOf(b), o)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func failCode(t *testing.T, err error) string {
	t.Helper()
	var f *as2.Failure
	if !errors.As(err, &f) {
		t.Fatalf("expected a failure, got %v", err)
	}
	return f.Code
}

func TestDigestChoice(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	for _, d := range []string{as2.DigestSHA256, as2.DigestSHA384, as2.DigestSHA512} {
		for _, enc := range []bool{false, true} {
			w := build(t, a, b, as2.Outgoing{Sign: true, Encrypt: enc, Digest: d})
			if !enc && !strings.Contains(w.Header["Content-Type"], "micalg="+d) {
				t.Fatalf("%s: micalg missing in %s", d, w.Header["Content-Type"])
			}
			if !strings.Contains(w.Header["Disposition-Notification-Options"], d) {
				t.Fatalf("%s: receipt micalg not requested: %s", d, w.Header["Disposition-Notification-Options"])
			}
			r, err := as2.Open(hdr(w), w.Body, b, peerOf(a), as2.Policy{MinDigest: d}, time.Now())
			if err != nil || r.MICAlg != d || r.MIC != w.MIC || !r.Signed {
				t.Fatalf("%s enc=%v: %+v %v", d, enc, r, err)
			}
			// The receipt carries the MIC under the same digest, signed with the chosen receipt digest.
			mdn, err := as2.BuildMDN(b, as2.MDNInput{MessageID: "r@b", OriginalMessageID: r.MessageID, To: r.From, MIC: r.MIC, MICAlg: r.MICAlg, Sign: true, Digest: d})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(mdn.Header["Content-Type"], "micalg="+d) {
				t.Fatalf("receipt not signed with %s: %s", d, mdn.Header["Content-Type"])
			}
			m, err := as2.ParseMDNMin(hdr(mdn), mdn.Body, peerOf(b), true, time.Now(), d, false)
			if err != nil || m.MICAlg != d || m.MIC != w.MIC {
				t.Fatalf("%s receipt: %+v %v", d, m, err)
			}
		}
	}
}

func TestDigestFloor(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	w := build(t, a, b, as2.Outgoing{Sign: true, Digest: as2.DigestSHA256})
	// A partner configured for SHA-384 does not accept SHA-256 signatures.
	if _, err := as2.Open(hdr(w), w.Body, b, peerOf(a), as2.Policy{MinDigest: as2.DigestSHA384}, time.Now()); failCode(t, err) != as2.ErrInsufficient {
		t.Fatalf("below floor: %v", err)
	}
	// Stronger than the floor is accepted.
	w512 := build(t, a, b, as2.Outgoing{Sign: true, Digest: as2.DigestSHA512})
	if _, err := as2.Open(hdr(w512), w512.Body, b, peerOf(a), as2.Policy{MinDigest: as2.DigestSHA384}, time.Now()); err != nil {
		t.Fatalf("above floor: %v", err)
	}
	// An unknown micalg is refused.
	h := hdr(w)
	h.Set("Content-Type", strings.Replace(h.Get("Content-Type"), "micalg=sha-256", "micalg=md5", 1))
	if _, err := as2.Open(h, w.Body, b, peerOf(a), as2.Policy{}, time.Now()); failCode(t, err) != as2.ErrInsufficient {
		t.Fatalf("md5 micalg: %v", err)
	}
	// Unknown digests cannot be chosen.
	for _, d := range []string{"md5", "sha-224", "nonsense"} {
		if _, err := as2.Build(a, peerOf(b), as2.Outgoing{MessageID: "m@x", ContentType: "text/plain", Payload: []byte("x"), Sign: true, Digest: d}); err == nil {
			t.Fatalf("digest %q accepted", d)
		}
		if _, err := as2.BuildMDN(a, as2.MDNInput{MessageID: "m@x", OriginalMessageID: "o@x", To: "NODE-B", Digest: d}); err == nil {
			t.Fatalf("receipt digest %q accepted", d)
		}
		if _, err := as2.BuildMDN(a, as2.MDNInput{MessageID: "m@x", OriginalMessageID: "o@x", To: "NODE-B", MICAlg: d}); err == nil {
			t.Fatalf("receipt mic digest %q accepted", d)
		}
	}
}

func TestSHA1OnlyForLegacy(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	out := as2.Outgoing{MessageID: "m@x", ContentType: "text/plain", Payload: []byte("x"), Sign: true, Digest: as2.DigestSHA1}
	if _, err := as2.Build(a, peerOf(b), out); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("SHA-1 without legacy: %v", err)
	}
	if _, err := as2.BuildMDN(a, as2.MDNInput{MessageID: "m@x", OriginalMessageID: "o@x", To: "NODE-B", Digest: as2.DigestSHA1, Sign: true}); err == nil {
		t.Fatal("SHA-1 receipt without legacy")
	}

	w := build(t, a, b, as2.Outgoing{Sign: true, Digest: as2.DigestSHA1, Legacy: true})
	if !strings.Contains(w.Header["Content-Type"], "micalg=sha1") {
		t.Fatalf("legacy micalg token: %s", w.Header["Content-Type"])
	}
	// Default policy, and a SHA-1 floor without the legacy flag, both refuse it.
	for _, pol := range []as2.Policy{{}, {MinDigest: as2.DigestSHA1}, {Legacy: true, MinDigest: as2.DigestSHA256}} {
		if _, err := as2.Open(hdr(w), w.Body, b, peerOf(a), pol, time.Now()); failCode(t, err) != as2.ErrInsufficient {
			t.Fatalf("policy %+v accepted SHA-1: %v", pol, err)
		}
	}
	r, err := as2.Open(hdr(w), w.Body, b, peerOf(a), as2.Policy{Legacy: true, MinDigest: as2.DigestSHA1}, time.Now())
	if err != nil || r.MICAlg != as2.DigestSHA1 || r.MIC != w.MIC {
		t.Fatalf("legacy open: %+v %v", r, err)
	}

	// A SHA-1 signed receipt is refused unless the partner is legacy.
	mdn, err := as2.BuildMDN(b, as2.MDNInput{MessageID: "r@b", OriginalMessageID: r.MessageID, To: r.From, MIC: r.MIC, MICAlg: r.MICAlg, Sign: true, Digest: as2.DigestSHA1, Legacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as2.ParseMDN(hdr(mdn), mdn.Body, peerOf(b), true, time.Now()); err == nil {
		t.Fatal("SHA-1 receipt accepted by default")
	}
	if _, err := as2.ParseMDNMin(hdr(mdn), mdn.Body, peerOf(b), true, time.Now(), as2.DigestSHA1, false); err == nil {
		t.Fatal("SHA-1 floor without legacy accepted a SHA-1 receipt")
	}
	m, err := as2.ParseMDNMin(hdr(mdn), mdn.Body, peerOf(b), true, time.Now(), as2.DigestSHA1, true)
	if err != nil || m.MICAlg != as2.DigestSHA1 || m.MIC != w.MIC {
		t.Fatalf("legacy receipt: %+v %v", m, err)
	}
}

func TestUnsignedUsesRequestedDigest(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	w := build(t, a, b, as2.Outgoing{Encrypt: true, Digest: as2.DigestSHA384})
	r, err := as2.Open(hdr(w), w.Body, b, peerOf(a), as2.Policy{}, time.Now())
	if err != nil || r.Signed || r.MICAlg != as2.DigestSHA384 || r.MIC != w.MIC {
		t.Fatalf("%+v %v", r, err)
	}
	// A request below the receiver's floor falls back to SHA-256, so the sender sees a mismatch.
	wl := build(t, a, b, as2.Outgoing{Encrypt: true, Digest: as2.DigestSHA1, Legacy: true})
	r, err = as2.Open(hdr(wl), wl.Body, b, peerOf(a), as2.Policy{}, time.Now())
	if err != nil || r.MICAlg != as2.DigestSHA256 || r.MIC == wl.MIC {
		t.Fatalf("%+v %v", r, err)
	}
	// Plain (neither signed nor encrypted) also follows the requested digest.
	wp := build(t, a, b, as2.Outgoing{Digest: as2.DigestSHA512})
	if r, err = as2.Open(hdr(wp), wp.Body, b, peerOf(a), as2.Policy{}, time.Now()); err != nil || r.MICAlg != as2.DigestSHA512 || r.MIC != wp.MIC {
		t.Fatalf("plain: %+v %v", r, err)
	}
}

func TestRequestedMICAlgHeader(t *testing.T) {
	cases := map[string]string{
		"signed-receipt-protocol=required, pkcs7-signature; signed-receipt-micalg=required, sha-512":   as2.DigestSHA512,
		"signed-receipt-protocol=optional, pkcs7-signature; signed-receipt-micalg=optional, sha1, md5": as2.DigestSHA1,
		"signed-receipt-protocol=required, pkcs7-signature; signed-receipt-micalg=required, SHA256":    as2.DigestSHA256,
		"signed-receipt-protocol=required, pkcs7-signature":                                            "",
		"signed-receipt-micalg=required":                                                               "",
		"signed-receipt-micalg=required, md5":                                                          "",
	}
	for opts, want := range cases {
		h := http.Header{}
		h.Set("AS2-From", "A")
		h.Set("AS2-To", "B")
		h.Set("Message-ID", "<m@x>")
		h.Set("Disposition-Notification-To", "A")
		h.Set("Disposition-Notification-Options", opts)
		hd, err := as2.ParseHeader(h)
		if err != nil || hd.MICAlg != want {
			t.Fatalf("%q: %q %v", opts, hd.MICAlg, err)
		}
	}
}

func TestEncryptionChoice(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	for _, enc := range []string{"", as2.EncAES256, as2.EncAES128} {
		for _, sign := range []bool{false, true} {
			w := build(t, a, b, as2.Outgoing{Sign: sign, Encrypt: true, Encryption: enc})
			r, err := as2.Open(hdr(w), w.Body, b, peerOf(a), as2.Policy{MinEncryption: as2.EncAES128}, time.Now())
			if err != nil || !r.Encrypted || string(r.Payload) != string(bigPayload) {
				t.Fatalf("enc %q sign=%v: %v", enc, sign, err)
			}
		}
	}
	// The default floor accepts AES-256 only: AES-128 is refused, AES-256 passes.
	w128 := build(t, a, b, as2.Outgoing{Encrypt: true, Encryption: as2.EncAES128})
	if _, err := as2.Open(hdr(w128), w128.Body, b, peerOf(a), as2.Policy{}, time.Now()); failCode(t, err) != as2.ErrInsufficient {
		t.Fatalf("AES-128 under the default policy: %v", err)
	}
	w256 := build(t, a, b, as2.Outgoing{Encrypt: true})
	if _, err := as2.Open(hdr(w256), w256.Body, b, peerOf(a), as2.Policy{}, time.Now()); err != nil {
		t.Fatalf("AES-256: %v", err)
	}
	// An unsupported cipher cannot be chosen.
	if _, err := as2.Build(a, peerOf(b), as2.Outgoing{MessageID: "m@x", ContentType: "text/plain", Payload: []byte("x"), Encrypt: true, Encryption: "3des-cbc"}); err == nil {
		t.Fatal("3DES accepted")
	}
	// A message encrypted with a legacy cipher is refused even under the loosest floor.
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmDESCBC
	env, err := pkcs7.Encrypt([]byte("Content-Type: text/plain\r\n\r\nhello"), []*x509.Certificate{b.Cert})
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("AS2-From", "NODE-A")
	h.Set("AS2-To", "NODE-B")
	h.Set("Message-ID", "<des@x>")
	h.Set("Content-Type", `application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"`)
	if _, err := as2.Open(h, env, b, peerOf(a), as2.Policy{MinEncryption: as2.EncAES128}, time.Now()); failCode(t, err) != as2.ErrInsufficient {
		t.Fatalf("DES: %v", err)
	}
}

func TestCompression(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	pol := as2.Policy{AllowCompressed: true}
	plain := build(t, a, b, as2.Outgoing{Sign: true})
	for _, m := range []struct{ sign, enc bool }{{true, true}, {true, false}, {false, true}} {
		w := build(t, a, b, as2.Outgoing{Sign: m.sign, Encrypt: m.enc, Compress: true})
		if m.sign && !m.enc && len(w.Body) >= len(plain.Body)/2 {
			t.Fatalf("compressed body is not smaller: %d vs %d", len(w.Body), len(plain.Body))
		}
		if strings.Contains(string(w.Body), "sgtin") {
			t.Fatal("compressed body carries the payload in clear")
		}
		r, err := as2.Open(hdr(w), w.Body, b, peerOf(a), pol, time.Now())
		if err != nil || !r.Compressed || string(r.Payload) != string(bigPayload) || r.ContentType != "application/ld+json" || r.MIC != w.MIC {
			t.Fatalf("%+v: %+v %v", m, r, err)
		}
		// Not enabled for this partner: refused.
		if _, err := as2.Open(hdr(w), w.Body, b, peerOf(a), as2.Policy{}, time.Now()); failCode(t, err) != as2.ErrUnexpected {
			t.Fatalf("%+v: compressed accepted without the option: %v", m, err)
		}
	}
	// Uncompressed messages are unaffected by the option.
	if r, err := as2.Open(hdr(plain), plain.Body, b, peerOf(a), pol, time.Now()); err != nil || r.Compressed {
		t.Fatalf("plain with option: %+v %v", r, err)
	}
	// Compression needs a signature or encryption.
	if _, err := as2.Build(a, peerOf(b), as2.Outgoing{MessageID: "m@x", ContentType: "text/plain", Payload: []byte("x"), Compress: true}); err == nil {
		t.Fatal("bare compressed message built")
	}
}

func TestCompressedWithoutSecurityRefused(t *testing.T) {
	a, b := newStation(t, "NODE-A"), newStation(t, "NODE-B")
	w := build(t, a, b, as2.Outgoing{Sign: true, Compress: true})
	// Rebuild the wire as a bare compressed entity: extract the signed first part.
	h := hdr(w)
	_, params, _ := strings.Cut(h.Get("Content-Type"), "boundary=")
	bnd := strings.Trim(params, `"`)
	parts := strings.Split(string(w.Body), "--"+bnd)
	_, entity, _ := strings.Cut(strings.TrimPrefix(parts[1], "\r\n"), "\r\n\r\n")
	bare := http.Header{}
	bare.Set("AS2-From", "NODE-A")
	bare.Set("AS2-To", "NODE-B")
	bare.Set("Message-ID", "<bare@x>")
	bare.Set("Content-Type", `application/pkcs7-mime; smime-type=compressed-data; name="smime.p7z"`)
	bare.Set("Content-Transfer-Encoding", "binary")
	if _, err := as2.Open(bare, []byte(strings.TrimSuffix(entity, "\r\n")), b, peerOf(a), as2.Policy{AllowCompressed: true}, time.Now()); failCode(t, err) != as2.ErrInsufficient {
		t.Fatalf("bare compressed: %v", err)
	}
	// Garbage inside an encrypted compressed entity is an unexpected-processing-error, not a panic.
	env, err := pkcs7.Encrypt([]byte("Content-Type: application/pkcs7-mime; smime-type=compressed-data\r\nContent-Transfer-Encoding: binary\r\n\r\nnot cms"), []*x509.Certificate{b.Cert})
	if err != nil {
		t.Fatal(err)
	}
	enc := http.Header{}
	enc.Set("AS2-From", "NODE-A")
	enc.Set("AS2-To", "NODE-B")
	enc.Set("Message-ID", "<junk@x>")
	enc.Set("Content-Type", `application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"`)
	if _, err := as2.Open(enc, env, b, peerOf(a), as2.Policy{AllowCompressed: true}, time.Now()); failCode(t, err) != as2.ErrUnexpected {
		t.Fatalf("garbage compressed data: %v", err)
	}
}

func TestRotationVerifyAndDecrypt(t *testing.T) {
	a, aNext, b, bNext := newStation(t, "NODE-A"), newStation(t, "NODE-A"), newStation(t, "NODE-B"), newStation(t, "NODE-B")
	// Signed with the partner's next certificate: accepted only while it is on file as the second one.
	w := build(t, aNext, b, as2.Outgoing{Sign: true})
	if _, err := as2.Open(hdr(w), w.Body, b, as2.Peer{ID: "NODE-A", Cert: a.Cert}, as2.Policy{}, time.Now()); failCode(t, err) != as2.ErrAuthentication {
		t.Fatalf("next certificate not on file: %v", err)
	}
	if _, err := as2.Open(hdr(w), w.Body, b, as2.Peer{ID: "NODE-A", Cert: a.Cert, Other: aNext.Cert}, as2.Policy{}, time.Now()); err != nil {
		t.Fatalf("next certificate on file: %v", err)
	}
	// ... and the current certificate keeps working during the overlap.
	wc := build(t, a, b, as2.Outgoing{Sign: true})
	if _, err := as2.Open(hdr(wc), wc.Body, b, as2.Peer{ID: "NODE-A", Cert: a.Cert, Other: aNext.Cert}, as2.Policy{}, time.Now()); err != nil {
		t.Fatalf("current certificate during overlap: %v", err)
	}
	// An unrelated certificate is still refused, and an expired second certificate too.
	other := newStation(t, "NODE-A")
	wo := build(t, other, b, as2.Outgoing{Sign: true})
	if _, err := as2.Open(hdr(wo), wo.Body, b, as2.Peer{ID: "NODE-A", Cert: a.Cert, Other: aNext.Cert}, as2.Policy{}, time.Now()); failCode(t, err) != as2.ErrAuthentication {
		t.Fatalf("unrelated certificate: %v", err)
	}
	if _, err := as2.Open(hdr(w), w.Body, b, as2.Peer{ID: "NODE-A", Cert: a.Cert, Other: aNext.Cert}, as2.Policy{}, time.Now().Add(48*time.Hour)); failCode(t, err) != as2.ErrAuthentication {
		t.Fatalf("expired second certificate: %v", err)
	}

	// Encrypted to the station's next certificate: decrypted with the second key while both are held.
	we := build(t, a, bNext, as2.Outgoing{Sign: true, Encrypt: true})
	if _, err := as2.Open(hdr(we), we.Body, b, peerOf(a), as2.Policy{}, time.Now()); failCode(t, err) != as2.ErrDecryption {
		t.Fatalf("no second key: %v", err)
	}
	both := b
	both.Other, both.OtherKey = bNext.Cert, bNext.Key
	if _, err := as2.Open(hdr(we), we.Body, both, peerOf(a), as2.Policy{}, time.Now()); err != nil {
		t.Fatalf("second key: %v", err)
	}
	// Messages for the current certificate still decrypt with both held.
	wcur := build(t, a, b, as2.Outgoing{Sign: true, Encrypt: true})
	if _, err := as2.Open(hdr(wcur), wcur.Body, both, peerOf(a), as2.Policy{}, time.Now()); err != nil {
		t.Fatalf("current key with both held: %v", err)
	}

	// Receipts signed with the next certificate verify too.
	mdn, err := as2.BuildMDN(aNext, as2.MDNInput{MessageID: "r@a", OriginalMessageID: "o@x", To: "NODE-B", Sign: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as2.ParseMDN(hdr(mdn), mdn.Body, as2.Peer{ID: "NODE-A", Cert: a.Cert}, true, time.Now()); err == nil {
		t.Fatal("receipt from an unknown next certificate accepted")
	}
	if _, err := as2.ParseMDN(hdr(mdn), mdn.Body, as2.Peer{ID: "NODE-A", Cert: a.Cert, Other: aNext.Cert}, true, time.Now()); err != nil {
		t.Fatalf("receipt from next certificate: %v", err)
	}
}

func TestDescribeAndExpiryStage(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	c, _, err := as2.GenerateStation("DESC-1", 100*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	info := as2.Describe(c)
	if info.Fingerprint != as2.Fingerprint(c) || !strings.Contains(info.Subject, "CN=DESC-1") || info.Serial == "" || info.Issuer == "" ||
		!info.NotAfter.Equal(c.NotAfter) || !info.NotBefore.Equal(c.NotBefore) {
		t.Fatalf("%+v", info)
	}
	day := 24 * time.Hour
	for _, tc := range []struct {
		left  time.Duration
		stage string
	}{
		{100 * day, as2.ExpiryOK}, {61 * day, as2.ExpiryOK}, {60 * day, "60d"}, {45 * day, "60d"}, {30 * day, "30d"}, {10 * day, "30d"},
		{7 * day, "7d"}, {time.Hour, "7d"}, {-time.Second, as2.ExpiryExpired}, {-40 * day, as2.ExpiryExpired},
	} {
		if got, _ := as2.ExpiryStage(now.Add(tc.left), now); got != tc.stage {
			t.Fatalf("left %v: %s, want %s", tc.left, got, tc.stage)
		}
	}
	if _, d := as2.ExpiryStage(now.Add(10*day+time.Hour), now); d != 10 {
		t.Fatalf("days %d", d)
	}
	if st, d := as2.ExpiryStage(now.Add(-3*day-time.Hour), now); st != as2.ExpiryExpired || d != -3 {
		t.Fatalf("expired days %s %d", st, d)
	}
}

func TestDigestHelpers(t *testing.T) {
	for in, want := range map[string]string{"sha1": as2.DigestSHA1, " SHA-1 ": as2.DigestSHA1, "sha256": as2.DigestSHA256, "SHA-384": as2.DigestSHA384, "sha512": as2.DigestSHA512, "md5": "", "": ""} {
		if got := as2.NormalizeDigest(in); got != want {
			t.Fatalf("%q: %q want %q", in, got, want)
		}
	}
	if as2.DigestToken(as2.DigestSHA1) != "sha1" || as2.DigestToken(as2.DigestSHA384) != "sha-384" {
		t.Fatal("DigestToken")
	}
	if !as2.ValidDigest("sha-512") || as2.ValidDigest("sha512") || !as2.ValidEncryption("aes-128-cbc") || as2.ValidEncryption("3des-cbc") {
		t.Fatal("Valid*")
	}
}
