// Package ids generates identifiers and validates GS1 keys.
package ids

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/core/httpx"
)

// New returns a time-ordered UUIDv7.
func New() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// Secret returns a random URL-safe secret with n bytes of entropy.
func Secret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Param parses a UUID path parameter.
func Param(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid " + name)
	}
	return id, nil
}
