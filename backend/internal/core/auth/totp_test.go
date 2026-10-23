package auth

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B: SHA-1 secret "12345678901234567890", 8 digits. The last six digits are the
// 6-digit code for the same step.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		unix int64
		code string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		if got := hotp(secret, TOTPStep(time.Unix(v.unix, 0)), 8); got != v.code {
			t.Errorf("t=%d: %s, want %s", v.unix, got, v.code)
		}
		if got := TOTPCode(secret, time.Unix(v.unix, 0)); got != v.code[2:] {
			t.Errorf("t=%d: 6-digit %s, want %s", v.unix, got, v.code[2:])
		}
	}
}

func TestVerifyTOTPWindowAndReplayStep(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil || len(secret) != 20 {
		t.Fatalf("secret: %v %d", err, len(secret))
	}
	now := time.Unix(1_700_000_000, 0)
	code := TOTPCode(secret, now)
	step, err := VerifyTOTP(secret, code, now)
	if err != nil || step != TOTPStep(now) {
		t.Fatalf("current step: %d %v", step, err)
	}
	// One step of drift either side is accepted, two are not.
	if s, err := VerifyTOTP(secret, code, now.Add(TOTPPeriod)); err != nil || s != TOTPStep(now) {
		t.Fatalf("previous step: %d %v", s, err)
	}
	if _, err := VerifyTOTP(secret, code, now.Add(-TOTPPeriod)); err != nil {
		t.Fatalf("next step: %v", err)
	}
	if _, err := VerifyTOTP(secret, code, now.Add(3*TOTPPeriod)); err == nil {
		t.Fatal("a code three steps old was accepted")
	}
	if _, err := VerifyTOTP(secret, " "+code[:3]+" "+code[3:]+" ", now); err != nil {
		t.Fatalf("spaces are ignored: %v", err)
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "000000x"} {
		if _, err := VerifyTOTP(secret, bad, now); err == nil && bad != "" {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := VerifyTOTP(secret, "", now); err == nil {
		t.Fatal("empty code accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	u := TOTPURI("Open Spoor", "a@b.test", []byte("12345678901234567890"))
	for _, want := range []string{"otpauth://totp/Open%20Spoor:a@b.test?", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "issuer=Open+Spoor", "digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(u, want) {
			t.Errorf("%s lacks %s", u, want)
		}
	}
}

func TestRecoveryCodes(t *testing.T) {
	c, err := NewRecoveryCode()
	if err != nil || len(c) != 14 || c[4] != '-' || c[9] != '-' {
		t.Fatalf("code %q %v", c, err)
	}
	if !IsRecoveryCode(c) || !IsRecoveryCode(strings.ToUpper(strings.ReplaceAll(c, "-", " "))) {
		t.Fatal("shape")
	}
	if NormalizeRecoveryCode(" AB-cd ") != "abcd" {
		t.Fatal("normalize")
	}
	for _, bad := range []string{"123456", "abcd-efgh-ijk", "abcd-efgh-ijk1", "abcd-efgh-ijk!"} {
		if IsRecoveryCode(bad) {
			t.Errorf("%q has the shape of a recovery code", bad)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c, _ := NewRecoveryCode()
		if seen[c] {
			t.Fatal("duplicate code")
		}
		seen[c] = true
	}
}
