package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubSessions struct {
	version int
	err     error
	calls   int
}

func (s *stubSessions) SessionValid(_ context.Context, _, _ uuid.UUID, v int) (bool, error) {
	s.calls++
	return s.err == nil && v == s.version, s.err
}

func authn(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New(context.Background(), []byte("0123456789abcdef"), time.Hour, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func bearer(tok string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func TestAuthenticateChecksTokenVersion(t *testing.T) {
	a := authn(t)
	s := &stubSessions{version: 3}
	a.SetSessionChecker(s)
	p := Principal{TenantID: uuid.New(), UserID: uuid.New(), Roles: []string{RoleOps}, Kind: "user", TokenVersion: 3, MFA: true}
	tok, _, _ := a.Issue(p)
	got, err := a.Authenticate(bearer(tok))
	if err != nil || got.TokenVersion != 3 || !got.MFA || s.calls != 1 {
		t.Fatalf("%+v %v calls=%d", got, err, s.calls)
	}
	s.version = 4 // the user's version moved on: the token is revoked
	if _, err := a.Authenticate(bearer(tok)); err == nil || err.Error() != "session ended" {
		t.Fatalf("revoked token: %v", err)
	}
	s.err = errors.New("db down") // fail closed
	if _, err := a.Authenticate(bearer(tok)); err == nil {
		t.Fatal("a failing session store must not authenticate")
	}
	// Unbound tokens (operator support access) are not tied to a stored session.
	s.err, s.calls = nil, 0
	p.Unbound = true
	tok, _, _ = a.Issue(p)
	if got, err := a.Authenticate(bearer(tok)); err != nil || s.calls != 0 || !got.Unbound {
		t.Fatalf("%+v %v calls=%d", got, err, s.calls)
	}
}

func TestAuthenticateIgnoresCookies(t *testing.T) {
	a := authn(t)
	tok, _, _ := a.Issue(Principal{TenantID: uuid.New(), UserID: uuid.New(), Roles: []string{RoleOps}, Kind: "user"})
	r := httptest.NewRequest("GET", "/api/v1/x", nil)
	r.AddCookie(&http.Cookie{Name: "openspoor_token", Value: tok})
	if _, err := a.Authenticate(r); err == nil || err.Error() != "missing credentials" {
		t.Fatalf("a cookie is not a credential: %v", err)
	}
	if _, err := a.Authenticate(bearer(tok)); err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareLimitsSessionsThatMustEnrol(t *testing.T) {
	a := authn(t)
	tok, _, _ := a.Issue(Principal{TenantID: uuid.New(), UserID: uuid.New(), Roles: []string{RoleAdmin}, Kind: "user", MFASetupRequired: true})
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for path, want := range map[string]int{
		"/api/v1/me": 204, "/api/v1/auth/mfa": 204, "/api/v1/auth/mfa/enrol": 204, "/api/v1/auth/mfa/confirm": 204, "/api/v1/auth/logout": 204, "/api/v1/auth/password": 204,
		"/api/v1/orgs": 403, "/api/v1/users": 403, "/api/v1/auth/mfa-evil": 403, "/api/v1/me/tenants": 403, "/api/v1/auth/switch": 403, "/api/v1/mcp": 403,
	} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
	}
}
