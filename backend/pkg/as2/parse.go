package as2

import (
	"bytes"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/smallstep/pkcs7"
)

// Failure is a message that cannot be accepted. Code is the MDN disposition modifier.
type Failure struct {
	Code string
	Err  error
}

func (f *Failure) Error() string { return f.Code + ": " + f.Err.Error() }
func (f *Failure) Unwrap() error { return f.Err }

func fail(code, msg string) *Failure { return &Failure{Code: code, Err: errors.New(msg)} }

// Header is what a receiver needs from the HTTP headers before it can look up the sender.
type Header struct {
	MessageID string
	From, To  string
	Subject   string
	// MDN is the receipt mode the sender asked for.
	MDN string
	// ReceiptURL is the sender's Receipt-Delivery-Option (async receipts).
	ReceiptURL string
	// MICAlg is the digest the sender asked for in the receipt (signed-receipt-micalg); empty
	// when it did not say.
	MICAlg string
}

// requestedMICAlg reads the first supported digest after signed-receipt-micalg in a
// Disposition-Notification-Options header.
func requestedMICAlg(opts string) string {
	_, rest, ok := strings.Cut(strings.ToLower(opts), "signed-receipt-micalg")
	if !ok {
		return ""
	}
	rest = strings.TrimPrefix(strings.TrimSpace(rest), "=")
	for _, tok := range strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if tok == "required" || tok == "optional" {
			continue
		}
		return NormalizeDigest(tok) // the first algorithm named is the preferred one
	}
	return ""
}

func stripAngles(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
}

// ParseHeader reads the AS2 headers of a received message.
func ParseHeader(h http.Header) (*Header, error) {
	hd := &Header{
		MessageID: stripAngles(h.Get("Message-ID")), From: strings.Trim(strings.TrimSpace(h.Get("AS2-From")), `"`),
		To: strings.Trim(strings.TrimSpace(h.Get("AS2-To")), `"`), Subject: h.Get("Subject"), MDN: MDNNone,
	}
	if !ValidID(hd.From) || !ValidID(hd.To) {
		return nil, errors.New("as2: missing or invalid AS2-From / AS2-To")
	}
	if !ValidID(hd.MessageID) {
		return nil, errors.New("as2: missing or invalid Message-ID")
	}
	if h.Get("Disposition-Notification-To") != "" {
		hd.MDN = MDNSync
		hd.MICAlg = requestedMICAlg(h.Get("Disposition-Notification-Options"))
		if u := strings.TrimSpace(h.Get("Receipt-Delivery-Option")); u != "" {
			if strings.ContainsAny(u, " <>\r\n") {
				return nil, errors.New("as2: invalid Receipt-Delivery-Option")
			}
			hd.MDN, hd.ReceiptURL = MDNAsync, u
		}
	}
	return hd, nil
}

// Received is a verified, decrypted message.
type Received struct {
	Header
	Payload     []byte
	ContentType string
	Filename    string
	Signed      bool
	Encrypted   bool
	// MIC is the base64 message integrity check to return in the receipt, computed with MICAlg.
	MIC    string
	MICAlg string
	// Compressed is true when the payload arrived as CMS compressed data.
	Compressed bool
}

// Open decrypts and verifies a received message from peer according to pol. Every rejection is
// a *Failure whose Code belongs in the error MDN.
func Open(h http.Header, body []byte, st Station, peer Peer, pol Policy, now time.Time) (*Received, error) {
	hd, err := ParseHeader(h)
	if err != nil {
		return nil, fail(ErrUnexpected, err.Error())
	}
	r := &Received{Header: *hd}
	ct := h.Get("Content-Type")
	mt, _ := mediaType(ct)
	// Raw is the MIME entity (headers and body) the rest of the processing works on.
	var raw []byte
	switch {
	case (mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime") && !isCompressed(ct):
		der, err := decodeCTE(h.Get("Content-Transfer-Encoding"), body)
		if err != nil {
			return nil, fail(ErrDecryption, err.Error())
		}
		p7, err := pkcs7.Parse(der)
		if err != nil {
			return nil, fail(ErrDecryption, "not a CMS enveloped message")
		}
		if st.Cert == nil || st.Key == nil {
			return nil, fail(ErrDecryption, "station has no decryption key")
		}
		if alg := contentEncryptionOf(der); !encryptionAllowed(alg, pol.MinEncryption) {
			if alg == "" {
				alg = "unknown"
			}
			return nil, fail(ErrInsufficient, "content encryption "+alg+" is not accepted for this partner")
		}
		if raw, err = p7.Decrypt(st.Cert, st.Key); err != nil && st.Other != nil && st.OtherKey != nil {
			raw, err = p7.Decrypt(st.Other, st.OtherKey)
		}
		if err != nil {
			return nil, fail(ErrDecryption, "cannot decrypt message")
		}
		r.Encrypted = true
	default:
		raw = append([]byte("Content-Type: "+ct+"\r\n"), headerLine(h, "Content-Transfer-Encoding")...)
		raw = append(raw, headerLine(h, "Content-Disposition")...)
		raw = append(raw, "\r\n"...)
		raw = append(raw, body...)
	}
	if pol.RequireEncrypted && !r.Encrypted {
		return nil, fail(ErrInsufficient, "partner profile requires encryption")
	}
	eh, ebody, err := splitEntity(raw)
	if err != nil {
		return nil, fail(ErrUnexpected, err.Error())
	}
	emt, eparams := mediaType(eh.Get("Content-Type"))
	payloadEntity := raw
	floor := floorDigest(pol.MinDigest, pol.Legacy)
	micAlg := DigestSHA256
	if emt == "multipart/signed" {
		parts, err := splitMultipart(ebody, eparams["boundary"])
		if err != nil || len(parts) != 2 {
			return nil, fail(ErrUnexpected, "malformed multipart/signed message")
		}
		micAlg = NormalizeDigest(eparams["micalg"])
		if micAlg == "" || digestRank(micAlg) < digestRank(floor) {
			return nil, fail(ErrInsufficient, "unsupported micalg "+strings.ToLower(eparams["micalg"]))
		}
		if err := verifyDetached(parts[0], parts[1], peer, now, floor); err != nil {
			return nil, err
		}
		r.Signed = true
		payloadEntity = parts[0]
	} else if pol.RequireSigned {
		return nil, fail(ErrInsufficient, "partner profile requires a signature")
	}
	ph, pbody, err := splitEntity(payloadEntity)
	if err != nil {
		return nil, fail(ErrUnexpected, err.Error())
	}
	if isCompressed(ph.Get("Content-Type")) {
		if !pol.AllowCompressed {
			return nil, fail(ErrUnexpected, "compressed messages are not enabled for this partner")
		}
		if !r.Signed && !r.Encrypted {
			return nil, fail(ErrInsufficient, "a compressed message must be signed or encrypted")
		}
		cder, err := decodeCTE(ph.Get("Content-Transfer-Encoding"), pbody)
		if err != nil {
			return nil, fail(ErrUnexpected, err.Error())
		}
		inner, err := decompressEntity(cder)
		if err != nil {
			return nil, fail(ErrUnexpected, err.Error())
		}
		r.Compressed = true
		if ph, pbody, err = splitEntity(inner); err != nil {
			return nil, fail(ErrUnexpected, err.Error())
		}
	}
	if r.Payload, err = decodeCTE(ph.Get("Content-Transfer-Encoding"), pbody); err != nil {
		return nil, fail(ErrUnexpected, err.Error())
	}
	r.ContentType = ph.Get("Content-Type")
	if _, p, err := mimeParams(ph.Get("Content-Disposition")); err == nil {
		r.Filename = p["filename"]
	}
	// An unsigned message has no micalg: use the digest the sender asked the receipt to carry,
	// when it meets the policy, else SHA-256.
	if !r.Signed {
		micAlg = DigestSHA256
		if a := hd.MICAlg; a != "" && digestRank(a) >= digestRank(floor) {
			micAlg = a
		}
	}
	r.MICAlg = micAlg
	switch {
	case r.Signed:
		r.MIC = micOf(payloadEntity, micAlg)
	case r.Encrypted:
		r.MIC = micOf(raw, micAlg)
	default:
		r.MIC = micOf(entityBytes(r.ContentType, r.Filename, r.Payload), micAlg)
	}
	if r.ContentType == "" {
		return nil, fail(ErrUnexpected, "payload has no Content-Type")
	}
	return r, nil
}

func headerLine(h http.Header, k string) []byte {
	if v := h.Get(k); v != "" {
		return []byte(k + ": " + v + "\r\n")
	}
	return nil
}

// verifyDetached checks a detached signature over signed with the peer's pinned certificate (or
// its second certificate during a rotation). The signature digest must be at least floor.
func verifyDetached(signed, sigPart []byte, peer Peer, now time.Time, floor string) *Failure {
	if peer.Cert == nil {
		return fail(ErrAuthentication, "no certificate on file for the sender")
	}
	sh, sbody, err := splitEntity(sigPart)
	if err != nil {
		return fail(ErrUnexpected, "malformed signature part")
	}
	if m, _ := mediaType(sh.Get("Content-Type")); m != "application/pkcs7-signature" && m != "application/x-pkcs7-signature" {
		return fail(ErrUnexpected, "second part is not a signature")
	}
	der, err := decodeCTE(sh.Get("Content-Transfer-Encoding"), sbody)
	if err != nil {
		return fail(ErrUnexpected, err.Error())
	}
	p7, err := pkcs7.Parse(der)
	if err != nil {
		return fail(ErrAuthentication, "unreadable signature")
	}
	p7.Content = signed
	if len(p7.Signers) != 1 {
		return fail(ErrAuthentication, "exactly one signer is required")
	}
	d := digestOfOID(p7.Signers[0].DigestAlgorithm.Algorithm)
	if d == "" || digestRank(d) < digestRank(floor) {
		return fail(ErrInsufficient, "signature digest is not accepted for this partner")
	}
	if err := p7.Verify(); err != nil {
		var mm *pkcs7.MessageDigestMismatchError
		if errors.As(err, &mm) {
			return fail(ErrIntegrity, "content digest mismatch")
		}
		return fail(ErrAuthentication, "signature does not verify")
	}
	// Pin: the signer must be the certificate exchanged with this partner, or, during a rotation,
	// the next one.
	// TODO(spec): chain building, CA trust anchors and revocation checks (CRL, OCSP) are not
	// performed; trust comes from the certificate exchange during onboarding only.
	s := p7.GetOnlySigner()
	var match *x509.Certificate
	switch {
	case s == nil:
	case bytes.Equal(s.Raw, peer.Cert.Raw):
		match = peer.Cert
	case peer.Other != nil && bytes.Equal(s.Raw, peer.Other.Raw):
		match = peer.Other
	}
	if match == nil {
		return fail(ErrAuthentication, "signer is not the partner's certificate")
	}
	if now.Before(match.NotBefore) || now.After(match.NotAfter) {
		return fail(ErrAuthentication, "partner certificate is not valid now")
	}
	return nil
}
