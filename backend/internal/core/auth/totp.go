package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238/4226 default HMAC-SHA-1; the secret is 160 bits and the code is short-lived
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Time-based one-time passwords (RFC 6238 over RFC 4226 HOTP), HMAC-SHA-1, 6 digits, 30 s steps:
// the parameters every authenticator app supports.
const (
	TOTPDigits = 6
	TOTPPeriod = 30 * time.Second
	// totpSkew is how many steps either side of the current one are accepted (clock drift).
	totpSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret.
func NewTOTPSecret() ([]byte, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// EncodeTOTPSecret is the base32 form shown to the user and put into the otpauth URI.
func EncodeTOTPSecret(secret []byte) string { return b32.EncodeToString(secret) }

// TOTPURI is the otpauth:// URI that authenticator apps import (usually as a QR code).
func TOTPURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", EncodeTOTPSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(int(TOTPPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// hotp is RFC 4226 section 5.3 with dynamic truncation.
func hotp(secret []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// TOTPStep is the time step number of t.
func TOTPStep(t time.Time) uint64 { return uint64(t.Unix()) / uint64(TOTPPeriod.Seconds()) }

// TOTPCode is the code for the step containing t.
func TOTPCode(secret []byte, t time.Time) string { return hotp(secret, TOTPStep(t), TOTPDigits) }

// ErrBadTOTP is returned for a wrong or malformed code.
var ErrBadTOTP = errors.New("invalid verification code")

// VerifyTOTP checks code against the steps around now and returns the matching step so the caller
// can refuse to accept the same step twice (replay). Comparison is constant-time per candidate.
func VerifyTOTP(secret []byte, code string, now time.Time) (uint64, error) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, ErrBadTOTP
	}
	cur := TOTPStep(now)
	var hit uint64
	found := 0
	for d := -totpSkew; d <= totpSkew; d++ {
		step := uint64(int64(cur) + int64(d))
		if subtle.ConstantTimeCompare([]byte(hotp(secret, step, TOTPDigits)), []byte(code)) == 1 {
			hit, found = step, 1
		}
	}
	if found == 0 {
		return 0, ErrBadTOTP
	}
	return hit, nil
}

// NewRecoveryCode returns a random single-use recovery code such as "k3m9-x2ab-7qrt" (about 60 bits).
func NewRecoveryCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := strings.ToLower(b32.EncodeToString(b))[:12]
	return s[:4] + "-" + s[4:8] + "-" + s[8:], nil
}

// NormalizeRecoveryCode lower-cases a code and drops separators, so "K3M9 X2AB-7QRT" matches.
func NormalizeRecoveryCode(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	return strings.NewReplacer("-", "", " ", "").Replace(c)
}

// IsRecoveryCode reports whether c has the shape of a recovery code (12 base32 characters).
func IsRecoveryCode(c string) bool {
	n := NormalizeRecoveryCode(c)
	if len(n) != 12 {
		return false
	}
	for _, r := range n {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return false
		}
	}
	return true
}
