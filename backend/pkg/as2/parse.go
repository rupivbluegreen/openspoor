package as2

import (
	"bytes"
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
	// MIC is the base64 message integrity check to return in the receipt.
	MIC string
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
	switch mt {
	case "application/pkcs7-mime", "application/x-pkcs7-mime":
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
		if raw, err = p7.Decrypt(st.Cert, st.Key); err != nil {
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
	if emt == "multipart/signed" {
		parts, err := splitMultipart(ebody, eparams["boundary"])
		if err != nil || len(parts) != 2 {
			return nil, fail(ErrUnexpected, "malformed multipart/signed message")
		}
		if m := strings.ToLower(eparams["micalg"]); m != MICAlgorithm {
			return nil, fail(ErrInsufficient, "unsupported micalg "+m)
		}
		if err := verifyDetached(parts[0], parts[1], peer, now); err != nil {
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
	if r.Payload, err = decodeCTE(ph.Get("Content-Transfer-Encoding"), pbody); err != nil {
		return nil, fail(ErrUnexpected, err.Error())
	}
	r.ContentType = ph.Get("Content-Type")
	if _, p, err := mimeParams(ph.Get("Content-Disposition")); err == nil {
		r.Filename = p["filename"]
	}
	switch {
	case r.Signed:
		r.MIC = micOf(payloadEntity)
	case r.Encrypted:
		r.MIC = micOf(raw)
	default:
		r.MIC = micOf(entityBytes(r.ContentType, r.Filename, r.Payload))
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

// verifyDetached checks a detached signature over signed with the peer's pinned certificate.
func verifyDetached(signed, sigPart []byte, peer Peer, now time.Time) *Failure {
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
	if !p7.Signers[0].DigestAlgorithm.Algorithm.Equal(pkcs7.OIDDigestAlgorithmSHA256) {
		return fail(ErrInsufficient, "signature digest is not SHA-256")
	}
	if err := p7.Verify(); err != nil {
		var mm *pkcs7.MessageDigestMismatchError
		if errors.As(err, &mm) {
			return fail(ErrIntegrity, "content digest mismatch")
		}
		return fail(ErrAuthentication, "signature does not verify")
	}
	// Pin: the signer must be the certificate exchanged with this partner.
	// TODO(spec): chain building, CA trust anchors and revocation checks (CRL, OCSP) are not
	// performed; trust comes from the certificate exchange during onboarding only.
	if s := p7.GetOnlySigner(); s == nil || !bytes.Equal(s.Raw, peer.Cert.Raw) {
		return fail(ErrAuthentication, "signer is not the partner's certificate")
	}
	if now.Before(peer.Cert.NotBefore) || now.After(peer.Cert.NotAfter) {
		return fail(ErrAuthentication, "partner certificate is not valid now")
	}
	return nil
}
