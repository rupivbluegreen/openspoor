package as2_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/pkg/as2"
)

func TestCertAndKeyPEM(t *testing.T) {
	now := time.Now()
	c, k, err := as2.GenerateStation("PEM-1", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := as2.GenerateStation("bad id", time.Hour, now); err == nil {
		t.Fatal("invalid id accepted")
	}
	got, err := as2.ParseCertPEM(as2.CertPEM(c), now)
	if err != nil || !got.Equal(c) {
		t.Fatalf("cert round trip: %v", err)
	}
	if _, err := as2.ParseCertPEM(as2.CertPEM(c), now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired certificate accepted")
	}
	if _, err := as2.ParseCertPEM(as2.CertPEM(c), time.Time{}); err != nil {
		t.Fatalf("zero time skips the validity check: %v", err)
	}
	for name, in := range map[string]string{
		"junk":       "junk",
		"wrong type": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})),
		"bad der":    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2}})),
	} {
		if _, err := as2.ParseCertPEM(in, now); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// A 1024-bit key is below the profile minimum.
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	tpl := &x509.Certificate{SerialNumber: c.SerialNumber, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &small.PublicKey, small)
	if _, err := as2.ParseCertPEM(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), now); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("small key: %v", err)
	}

	kp, err := as2.EncodeKeyPEM(k)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := as2.ParseKeyPEM(string(kp))
	if err != nil || !k2.Equal(k) {
		t.Fatalf("pkcs8 round trip: %v", err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if k3, err := as2.ParseKeyPEM(string(pkcs1)); err != nil || !k3.Equal(k) {
		t.Fatalf("pkcs1: %v", err)
	}
	ec := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustECDER(t)})
	for name, in := range map[string]string{"junk": "junk", "bad der": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})), "ec key": string(ec)} {
		if _, err := as2.ParseKeyPEM(in); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func mustECDER(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
