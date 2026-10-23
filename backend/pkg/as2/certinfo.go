package as2

import (
	"crypto/x509"
	"errors"
	"strconv"
	"time"
)

// CertInfo is what is stored and shown about an AS2 certificate.
type CertInfo struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	Serial      string    `json:"serial"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Fingerprint string    `json:"fingerprint"`
}

// Describe reads the identifying fields of a certificate.
func Describe(c *x509.Certificate) CertInfo {
	return CertInfo{Subject: c.Subject.String(), Issuer: c.Issuer.String(), Serial: c.SerialNumber.Text(16),
		NotBefore: c.NotBefore.UTC(), NotAfter: c.NotAfter.UTC(), Fingerprint: Fingerprint(c)}
}

// ExpiryThresholds are the days before expiry at which an alert is raised (and again on expiry).
// TODO(spec): 60, 30 and 7 days are the requested working values, not a requirement of any
// trading-partner agreement we hold.
var ExpiryThresholds = []int{60, 30, 7}

// Expiry states.
const (
	ExpiryOK      = "ok"
	ExpiryExpired = "expired"
)

// ExpiryStage classifies a certificate's notAfter at now: ExpiryExpired once passed, else the
// smallest threshold (in days) that the remaining time is within, as "60d", "30d" or "7d", else
// ExpiryOK. Days is the whole days remaining (negative once expired).
func ExpiryStage(notAfter, now time.Time) (stage string, days int) {
	left := notAfter.Sub(now)
	days = int(left.Hours() / 24)
	if left < 0 {
		return ExpiryExpired, -int((-left).Hours() / 24)
	}
	stage = ExpiryOK
	// ExpiryThresholds is descending; the last one the remaining time falls within wins.
	for _, t := range ExpiryThresholds {
		if left <= time.Duration(t)*24*time.Hour {
			stage = strconv.Itoa(t) + "d"
		}
	}
	return stage, days
}

// CheckRotation checks that next can take over from current on switchAt: it is a different
// certificate, it is not yet expired at now and it is valid on the switch date.
func CheckRotation(current, next *x509.Certificate, switchAt, now time.Time) error {
	switch {
	case current != nil && current.Equal(next):
		return errors.New("as2: the next certificate is the current certificate")
	case now.After(next.NotAfter):
		return errors.New("as2: the next certificate has expired")
	case switchAt.Before(next.NotBefore):
		return errors.New("as2: the next certificate is not valid yet on the switch date")
	case switchAt.After(next.NotAfter):
		return errors.New("as2: the next certificate expires before the switch date")
	}
	return nil
}
