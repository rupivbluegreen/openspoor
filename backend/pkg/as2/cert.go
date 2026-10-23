package as2

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

// MinRSABits is the smallest RSA key accepted for signing and encryption.
const MinRSABits = 2048

// ParseCertPEM parses one PEM certificate and checks it can serve this profile (an RSA key of at
// least MinRSABits, valid at now; a zero now skips the validity check).
func ParseCertPEM(s string, now time.Time) (*x509.Certificate, error) {
	blk, _ := pem.Decode([]byte(s))
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New("as2: expected a PEM CERTIFICATE")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, errors.New("as2: cannot parse certificate")
	}
	pub, ok := c.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() < MinRSABits {
		return nil, errors.New("as2: certificate must carry an RSA key of at least 2048 bits")
	}
	if !now.IsZero() && (now.Before(c.NotBefore) || now.After(c.NotAfter)) {
		return nil, errors.New("as2: certificate is not valid now")
	}
	return c, nil
}

// ParseKeyPEM parses a PEM RSA private key (PKCS#8 or PKCS#1).
func ParseKeyPEM(s string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(s))
	if blk == nil {
		return nil, errors.New("as2: expected a PEM private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("as2: cannot parse private key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("as2: private key must be RSA")
	}
	return rk, nil
}

// EncodeKeyPEM encodes an RSA private key as PKCS#8 PEM.
func EncodeKeyPEM(k *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// CertPEM encodes a certificate as PEM.
func CertPEM(c *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
}

// GenerateStation makes a self-signed RSA-2048 certificate and key for id, valid for the given
// duration. Partners pin the certificate, so a self-signed one is as good as any other.
func GenerateStation(id string, valid time.Duration, now time.Time) (*x509.Certificate, *rsa.PrivateKey, error) {
	if !ValidID(id) {
		return nil, nil, errors.New("as2: invalid AS2 identifier")
	}
	k, err := rsa.GenerateKey(rand.Reader, MinRSABits)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: id, Organization: []string{"Open Spoor AS2"}},
		NotBefore: now.Add(-time.Minute).UTC(), NotAfter: now.Add(valid).UTC(),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageContentCommitment}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		return nil, nil, err
	}
	c, err := x509.ParseCertificate(der)
	return c, k, err
}
