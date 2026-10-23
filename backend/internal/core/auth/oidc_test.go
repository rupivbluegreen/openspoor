package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestOIDCTokenChecks(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
		"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
	}}})
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/openspoor/protocol/openid-connect/certs" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(jwks)
	}))
	defer idp.Close()
	issuer := idp.URL + "/realms/openspoor"

	a, err := New(context.Background(), []byte("local-secret-0123456789"), time.Hour, nil, issuer, "openspoor")
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	sign := func(iss string, aud []string, tid string) string {
		c := Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: iss, Audience: aud, Subject: uuid.NewString(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}, Tenant: tid, Roles: []string{RoleAdmin}}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	p, err := a.Verify(sign(issuer, []string{"account", "openspoor"}, tenant.String()))
	if err != nil || p.TenantID != tenant || !p.Has(RoleAdmin) {
		t.Fatalf("valid token rejected: %v %+v", err, p)
	}
	for name, tok := range map[string]string{
		"other issuer":   sign("https://evil.example/realms/openspoor", []string{"openspoor"}, tenant.String()),
		"other audience": sign(issuer, []string{"account"}, tenant.String()),
		"no tenant":      sign(issuer, []string{"openspoor"}, ""),
	} {
		if _, err := a.Verify(tok); err == nil {
			t.Errorf("%s: token accepted", name)
		}
	}
	// Local session tokens keep working alongside the identity provider.
	local, _, err := a.Issue(Principal{TenantID: tenant, UserID: uuid.New(), Roles: []string{RoleViewer}, Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Verify(local); err != nil {
		t.Fatalf("local token rejected: %v", err)
	}
}
