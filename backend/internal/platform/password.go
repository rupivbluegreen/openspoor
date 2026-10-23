package platform

import (
	_ "embed" // common_passwords.txt
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/openspoor/openspoor/internal/core/httpx"
)

// Password policy (OWASP ASVS V2.1): at least 10 characters, at most 72 bytes (bcrypt hashes only
// the first 72 bytes, so a longer password would silently be a shorter one), not a very common
// password, and not containing the e-mail address' local part. There are no composition rules.
const (
	minPasswordLen = 10
	maxPasswordLen = 72
	// A local part shorter than this ("ops", "jo") would match innocent passwords by chance.
	minLocalPartLen = 4
)

//go:embed common_passwords.txt
var commonPasswordsRaw string

var (
	commonOnce sync.Once
	common     map[string]struct{}
)

func commonPasswords() map[string]struct{} {
	commonOnce.Do(func() {
		common = map[string]struct{}{}
		for _, l := range strings.Split(commonPasswordsRaw, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				common[l] = struct{}{}
			}
		}
	})
	return common
}

// alnumLower keeps letters and digits, lower-cased: "P@ssw0rd!" and "p-a-s-s-w-0-r-d" compare alike.
func alnumLower(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isCommon reports whether pw is on the deny list, alone or as a common word followed only by digits.
func isCommon(pw string) bool {
	c := commonPasswords()
	n := alnumLower(pw)
	if _, ok := c[n]; ok {
		return true
	}
	if _, ok := c[strings.ToLower(pw)]; ok {
		return true
	}
	// "password2031": a listed word of six or more letters with only digits appended.
	base := strings.TrimRightFunc(n, unicode.IsDigit)
	if len(base) >= 6 && len(base) < len(n) {
		if _, ok := c[base]; ok {
			return true
		}
	}
	// One character repeated, or a single short unit repeated ("abababababab").
	return repeated(n)
}

func repeated(s string) bool {
	for unit := 1; unit <= 3 && unit*3 <= len(s); unit++ {
		if len(s)%unit == 0 && strings.Repeat(s[:unit], len(s)/unit) == s {
			return true
		}
	}
	return false
}

// ValidatePassword applies the password policy. email may be empty.
func ValidatePassword(password, email string) error {
	if utf8.RuneCountInString(password) < minPasswordLen {
		return httpx.BadRequest("password must be at least 10 characters")
	}
	if len(password) > maxPasswordLen {
		return httpx.BadRequest("password must be at most 72 bytes")
	}
	if isCommon(password) {
		return httpx.BadRequest("password is too common; choose a longer, less predictable one")
	}
	if local, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@"); ok {
		local, _, _ = strings.Cut(local, "+") // user+tag@host: the user part is what people reuse
		if len(local) >= minLocalPartLen && strings.Contains(strings.ToLower(password), local) {
			return httpx.BadRequest("password must not contain the e-mail address")
		}
	}
	return nil
}
