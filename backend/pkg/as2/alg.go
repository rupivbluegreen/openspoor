package as2

import (
	"bytes"
	"crypto"
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is offered only for partners explicitly flagged legacy
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"strings"
	"sync"

	"github.com/smallstep/pkcs7"
)

// Message digest algorithms for signatures, the message integrity check (micalg) and receipts.
// TODO(spec): which digests DSCSA trading partners accept is not defined in any document we
// hold; SHA-256 is the default, SHA-384 and SHA-512 are offered, SHA-1 only for a partner flagged
// legacy. The micalg token for SHA-1 is written "sha1" (the spelling common in AS2 practice) and
// both spellings are read; confirm against partner profiles.
const (
	DigestSHA1   = "sha-1"
	DigestSHA256 = "sha-256"
	DigestSHA384 = "sha-384"
	DigestSHA512 = "sha-512"
)

// Content encryption algorithms (CBC mode, RSA key transport).
// TODO(spec): AES-256 is the default, AES-128 is allowed per partner; the set partners require
// is not defined in any document we hold.
const (
	EncAES256 = "aes-256-cbc"
	EncAES128 = "aes-128-cbc"
)

type digestInfo struct {
	hash crypto.Hash
	oid  asn1.ObjectIdentifier
	rank int
}

var digestTable = map[string]digestInfo{
	DigestSHA1:   {crypto.SHA1, pkcs7.OIDDigestAlgorithmSHA1, 1},
	DigestSHA256: {crypto.SHA256, pkcs7.OIDDigestAlgorithmSHA256, 2},
	DigestSHA384: {crypto.SHA384, pkcs7.OIDDigestAlgorithmSHA384, 3},
	DigestSHA512: {crypto.SHA512, pkcs7.OIDDigestAlgorithmSHA512, 4},
}

// NormalizeDigest maps a digest name or micalg token (sha1, sha-1, SHA-256, ...) to its canonical
// name, or "" when it is not one of the supported digests.
func NormalizeDigest(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sha1", "sha-1":
		return DigestSHA1
	case "sha256", "sha-256":
		return DigestSHA256
	case "sha384", "sha-384":
		return DigestSHA384
	case "sha512", "sha-512":
		return DigestSHA512
	}
	return ""
}

// DigestToken is the micalg / Received-Content-MIC spelling of a canonical digest name.
func DigestToken(d string) string {
	if d == DigestSHA1 {
		return "sha1"
	}
	return d
}

// ValidDigest reports whether d is a canonical supported digest name.
func ValidDigest(d string) bool { _, ok := digestTable[d]; return ok }

// ValidEncryption reports whether e is a supported content encryption algorithm.
func ValidEncryption(e string) bool { return e == EncAES256 || e == EncAES128 }

func digestRank(d string) int { return digestTable[d].rank }

// digestOr returns the canonical digest for s, or def when s is empty.
func digestOr(s, def string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return def, nil
	}
	d := NormalizeDigest(s)
	if d == "" {
		return "", errors.New("as2: unsupported digest algorithm " + s)
	}
	return d, nil
}

// sendDigest validates the digest chosen for an outgoing message or receipt.
func sendDigest(s string, legacy bool) (string, error) {
	d, err := digestOr(s, DigestSHA256)
	if err != nil {
		return "", err
	}
	if d == DigestSHA1 && !legacy {
		return "", errors.New("as2: SHA-1 is only available for a partner flagged legacy")
	}
	return d, nil
}

// floorDigest is the weakest digest a receiver accepts: SHA-256 unless the policy is legacy and
// asks for SHA-1.
func floorDigest(min string, legacy bool) string {
	d := NormalizeDigest(min)
	if d == "" || (d == DigestSHA1 && !legacy) {
		return DigestSHA256
	}
	return d
}

func digestOfOID(oid asn1.ObjectIdentifier) string {
	for name, di := range digestTable {
		if di.oid.Equal(oid) {
			return name
		}
	}
	return ""
}

func hashBytes(d string, b []byte) []byte {
	switch d {
	case DigestSHA1:
		s := sha1.Sum(b) //nolint:gosec // G401: legacy partners only
		return s[:]
	case DigestSHA384:
		s := sha512.Sum384(b)
		return s[:]
	case DigestSHA512:
		s := sha512.Sum512(b)
		return s[:]
	}
	s := sha256.Sum256(b)
	return s[:]
}

// micOf is the base64 message integrity check of content under digest d.
func micOf(content []byte, d string) string {
	return base64.StdEncoding.EncodeToString(hashBytes(d, content))
}

// --- content encryption ---

// encMu serialises message encryption: the CMS library selects the content cipher through a
// package variable.
var encMu sync.Mutex

func encryptFor(content []byte, cert *x509.Certificate, enc string) ([]byte, error) {
	encMu.Lock()
	defer encMu.Unlock()
	defer func() { pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC }()
	switch enc {
	case EncAES128:
		pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES128CBC
	default:
		pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
	}
	return pkcs7.Encrypt(content, []*x509.Certificate{cert})
}

var encOIDs = map[string]asn1.ObjectIdentifier{
	EncAES256:  pkcs7.OIDEncryptionAlgorithmAES256CBC,
	EncAES128:  pkcs7.OIDEncryptionAlgorithmAES128CBC,
	"aes-192":  {2, 16, 840, 1, 101, 3, 4, 1, 22},
	"3des-cbc": {1, 2, 840, 113549, 3, 7},
	"des-cbc":  {1, 3, 14, 3, 2, 7},
	"rc2-cbc":  {1, 2, 840, 113549, 3, 2},
	"aes-gcm":  pkcs7.OIDEncryptionAlgorithmAES128GCM,
	"aes-gcm2": pkcs7.OIDEncryptionAlgorithmAES256GCM,
}

// contentEncryptionOf names the content encryption algorithm of an enveloped-data message by
// looking for the algorithm identifier in the encoding (works for BER and DER). It returns "" when
// none or several known algorithms appear.
func contentEncryptionOf(der []byte) string {
	found := ""
	for name, oid := range encOIDs {
		enc, err := asn1.Marshal(oid)
		if err != nil || !bytes.Contains(der, enc) {
			continue
		}
		if found != "" {
			return ""
		}
		found = name
	}
	return found
}

// encryptionAllowed reports whether an enveloped message's cipher meets the policy floor: AES-256
// always, AES-128 only when the floor is AES-128.
func encryptionAllowed(alg, min string) bool {
	return alg == EncAES256 || (alg == EncAES128 && min == EncAES128)
}
