// Package as2 implements the AS2 (RFC 4130) message layer in process: S/MIME signing and
// encryption of a payload, parsing and verification of received messages, and message
// disposition notifications (MDN, RFC 3798) with signed receipts. CMS handling is delegated to
// github.com/smallstep/pkcs7 (MIT); everything else here is MIME framing and policy.
//
// The package is transport- and storage-agnostic: it builds and parses HTTP headers and bodies.
// Partner certificates are pinned: a signature verifies only when the signer certificate is
// byte-identical to the certificate exchanged with the partner (no chain building).
//
// Algorithms are fixed to one profile: SHA-256 signatures with RSA keys (PKCS#1 v1.5) and
// AES-256-CBC content encryption with RSA key transport.
// TODO(spec): the algorithm sets that DSCSA trading partners require (SHA-1 and 3DES for legacy
// partners, AES-GCM, RSA-OAEP, ECC keys, AS2 1.1 compression) are not defined in any document we
// hold; see docs/OPEN_QUESTIONS.md.
package as2

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/smallstep/pkcs7"
)

// Profile constants.
const (
	// MICAlgorithm is the only message integrity check algorithm (the micalg parameter value).
	MICAlgorithm = "sha-256"
	// Version is the AS2-Version header value sent.
	Version = "1.0"
	// MaxBody bounds the message and receipt sizes read from a peer.
	MaxBody = 32 << 20

	// MDN delivery modes requested by a sender.
	MDNNone  = "none"
	MDNSync  = "sync"
	MDNAsync = "async"
)

// Disposition modifier codes (RFC 4130 section 7.5).
const (
	ErrAuthentication = "authentication-failed"
	ErrDecryption     = "decryption-failed"
	ErrInsufficient   = "insufficient-message-security"
	ErrIntegrity      = "integrity-check-failed"
	ErrUnexpected     = "unexpected-processing-error"
)

func init() {
	// The library defaults to SHA-1 digests and DES content encryption; replace both.
	_ = pkcs7.SetDefaultDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
}

// Station is this node's AS2 identity.
type Station struct {
	ID   string
	Cert *x509.Certificate
	Key  crypto.PrivateKey
}

// Peer is a trading partner: its AS2 identifier and the certificate exchanged with it, used to
// verify its signatures and to encrypt to it.
type Peer struct {
	ID   string
	Cert *x509.Certificate
}

// Policy states what a receiver demands of messages from one peer.
type Policy struct {
	RequireSigned    bool
	RequireEncrypted bool
}

// Wire is an HTTP message to send: headers and body. MIC is the base64 message integrity check
// the receiver is expected to return in its receipt.
type Wire struct {
	Header map[string]string
	Body   []byte
	MIC    string
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$`)

// ValidID reports whether s is acceptable as an AS2 identifier or Message-ID (no whitespace,
// quotes or control characters, so it can be placed in a header unquoted).
func ValidID(s string) bool { return idRe.MatchString(s) }

// NewMessageID returns a globally unique Message-ID (without angle brackets).
func NewMessageID(stationID string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x@%s", b, stationID)
}

func micOf(content []byte) string {
	sum := sha256.Sum256(content)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Fingerprint is the SHA-256 fingerprint of a certificate, hex encoded.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return fmt.Sprintf("%x", sum[:])
}

func boundary() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return "=_os_" + base64.RawURLEncoding.EncodeToString(b)
}

func wrap64(b []byte) string {
	s := base64.StdEncoding.EncodeToString(b)
	var sb strings.Builder
	for len(s) > 76 {
		sb.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	sb.WriteString(s)
	return sb.String()
}

// signDetached returns a detached CMS SignedData over content.
func signDetached(content []byte, st Station) ([]byte, error) {
	if st.Cert == nil || st.Key == nil {
		return nil, errors.New("as2: station has no signing key")
	}
	sd, err := pkcs7.NewSignedData(content)
	if err != nil {
		return nil, err
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := sd.AddSigner(st.Cert, st.Key, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, err
	}
	sd.Detach()
	return sd.Finish()
}

// signedMultipart wraps entity (headers and body) in a multipart/signed body. It returns the
// Content-Type value and the body.
func signedMultipart(entity []byte, st Station) (string, []byte, error) {
	sig, err := signDetached(entity, st)
	if err != nil {
		return "", nil, err
	}
	b := boundary()
	var body []byte
	body = append(body, "--"+b+"\r\n"...)
	body = append(body, entity...)
	body = append(body, "\r\n--"+b+"\r\nContent-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"smime.p7s\"\r\n\r\n"...)
	body = append(body, wrap64(sig)...)
	body = append(body, "\r\n--"+b+"--\r\n"...)
	ct := fmt.Sprintf(`multipart/signed; protocol="application/pkcs7-signature"; micalg=%s; boundary="%s"`, MICAlgorithm, b)
	return ct, body, nil
}
