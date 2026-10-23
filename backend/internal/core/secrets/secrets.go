// Package secrets seals small secrets (pool FF1 keys, webhook secrets, per-tenant keys)
// with AES-256-GCM under the installation master key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// Sealer encrypts and decrypts with one master key.
type Sealer struct {
	aead cipher.AEAD
	mac  []byte // derived key for Pseudonym
}

// New derives a 256-bit key from master (use 32 random bytes from OPENSPOOR_MASTER_KEY in production).
func New(master []byte) (*Sealer, error) {
	if len(master) == 0 {
		return nil, errors.New("empty master key")
	}
	k := sha256.Sum256(master)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	mac := sha256.Sum256(append([]byte("pseudonym|"), k[:]...))
	return &Sealer{aead: aead, mac: mac[:]}, nil
}

// Seal returns nonce||ciphertext; aad binds the ciphertext to its owner (e.g. tenant+pool id).
func (s *Sealer) Seal(plain, aad []byte) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return s.aead.Seal(nonce, nonce, plain, aad)
}

// Open reverses Seal.
func (s *Sealer) Open(sealed, aad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed value too short")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], aad)
}

// Pseudonym returns a stable, non-reversible token for a personal value (HMAC-SHA256 under a key
// derived from this sealer's key, normalised to lower case). For a tenant sealer the key dies
// with the tenant's data key, after which the token can no longer be linked to the value.
func (s *Sealer) Pseudonym(value string) string {
	m := hmac.New(sha256.New, s.mac)
	m.Write([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(m.Sum(nil))[:16]
}
