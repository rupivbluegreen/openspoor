// Package auth authenticates requests (local JWT, Keycloak OIDC, scoped API keys)
// and carries the resulting Principal through the request context.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/core/httpx"
)

// Role names (M1).
const (
	RoleAdmin         = "Admin"
	RoleSerialization = "Serialization"
	RoleCompliance    = "Compliance"
	RoleOps           = "Ops"
	RoleIntegration   = "Integration"
	RoleViewer        = "Viewer"
)

// AllRoles lists every tenant role.
var AllRoles = []string{RoleAdmin, RoleSerialization, RoleCompliance, RoleOps, RoleIntegration, RoleViewer}

// Principal is the authenticated caller.
type Principal struct {
	TenantID uuid.UUID `json:"tenantId"`
	UserID   uuid.UUID `json:"userId"`
	Email    string    `json:"email,omitempty"`
	Name     string    `json:"name,omitempty"`
	Roles    []string  `json:"roles"`
	Kind     string    `json:"kind"` // "user", "apikey", "system"
	Operator bool      `json:"operator,omitempty"`
	KeyID    uuid.UUID `json:"keyId,omitempty"`

	// TokenVersion is the user's session generation when the token was issued. Changing the
	// password or the second factor, signing out, or an administrator changing roles or status
	// raises the stored version and so invalidates every earlier local token (see SessionChecker).
	TokenVersion int `json:"-"`
	// Unbound marks a token that is not tied to a stored user session (operator support access;
	// short-lived by its grant). Never set for tenant users.
	Unbound bool `json:"-"`
	// MFA says whether this session passed a second factor.
	MFA bool `json:"mfa,omitempty"`
	// MFASetupRequired limits the session to enrolling a second factor: the tenant requires one
	// for administrators and this user has none yet.
	MFASetupRequired bool `json:"mfaSetupRequired,omitempty"`
}

// Actor is the audit label for the principal.
// AuditActor identifies the principal in the audit trail without personal data: users by id
// (resolvable while the tenant exists), never by e-mail address.
func (p Principal) AuditActor() string {
	if p.Kind == "user" && p.UserID != uuid.Nil {
		return "user:" + p.UserID.String()
	}
	return p.Actor()
}

func (p Principal) Actor() string {
	switch p.Kind {
	case "apikey":
		return "apikey:" + p.KeyID.String()
	case "system":
		return "system"
	}
	if p.Email != "" {
		return "user:" + p.Email
	}
	return "user:" + p.UserID.String()
}

// Has reports whether the principal holds any of the roles (Admin holds all).
func (p Principal) Has(roles ...string) bool {
	if slices.Contains(p.Roles, RoleAdmin) {
		return true
	}
	for _, r := range roles {
		if slices.Contains(p.Roles, r) {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// With stores p in ctx.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal in ctx.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// MustFrom returns the principal or panics; use only behind Middleware.
func MustFrom(ctx context.Context) Principal {
	p, ok := From(ctx)
	if !ok {
		panic("auth: no principal in context")
	}
	return p
}

// System returns a principal for background work inside one tenant.
func System(tenant uuid.UUID) Principal {
	return Principal{TenantID: tenant, Kind: "system", Roles: []string{RoleAdmin}}
}

// APIKeyResolver looks up a hashed API key. Implemented by the platform module.
type APIKeyResolver interface {
	ResolveAPIKey(ctx context.Context, hash string) (Principal, error)
}

// Claims are the local JWT claims.
type Claims struct {
	jwt.RegisteredClaims
	Tenant   string   `json:"tid"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Roles    []string `json:"roles"`
	Operator bool     `json:"op,omitempty"`
	// Ver is the user's token version at issue time; Unbound tokens carry "nb" instead.
	Ver      int  `json:"ver"`
	Unbound  bool `json:"nb,omitempty"`
	MFA      bool `json:"mfa,omitempty"`
	MFASetup bool `json:"mfa_setup,omitempty"`
}

// SessionChecker confirms that a local token still belongs to an active session of its user.
// Implemented by the platform module, which holds the per-user token version.
type SessionChecker interface {
	// SessionValid reports whether the user is active and version is still its current token version.
	SessionValid(ctx context.Context, tenant, user uuid.UUID, version int) (bool, error)
}

// Authenticator validates credentials.
type Authenticator struct {
	secret   []byte
	ttl      time.Duration
	keys     APIKeyResolver
	oidc     keyfunc.Keyfunc
	issuer   string
	audience string
	sessions SessionChecker
}

// SetSessionChecker enables revocation of local tokens. Without it only expiry ends a token.
func (a *Authenticator) SetSessionChecker(c SessionChecker) { a.sessions = c }

// New builds an Authenticator. oidcIssuer may be empty.
func New(ctx context.Context, secret []byte, ttl time.Duration, keys APIKeyResolver, oidcIssuer, audience string) (*Authenticator, error) {
	a := &Authenticator{secret: secret, ttl: ttl, keys: keys, issuer: oidcIssuer, audience: audience}
	if oidcIssuer != "" {
		kf, err := keyfunc.NewDefaultCtx(ctx, []string{strings.TrimSuffix(oidcIssuer, "/") + "/protocol/openid-connect/certs"})
		if err != nil {
			return nil, fmt.Errorf("oidc jwks: %w", err)
		}
		a.oidc = kf
	}
	return a, nil
}

// Issue signs a local token for p.
func (a *Authenticator) Issue(p Principal) (string, time.Time, error) {
	exp := time.Now().Add(a.ttl)
	c := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   p.UserID.String(),
			Issuer:    "openspoor",
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Tenant: p.TenantID.String(), Email: p.Email, Name: p.Name, Roles: p.Roles, Operator: p.Operator,
		Ver: p.TokenVersion, Unbound: p.Unbound, MFA: p.MFA, MFASetup: p.MFASetupRequired,
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(a.secret)
	return s, exp, err
}

// HashKey hashes an API key secret for storage and lookup.
func HashKey(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// Authenticate resolves the request's principal.
func (a *Authenticator) Authenticate(r *http.Request) (Principal, error) {
	if k := r.Header.Get("X-API-Key"); k != "" {
		if a.keys == nil {
			return Principal{}, errors.New("api keys disabled")
		}
		return a.keys.ResolveAPIKey(r.Context(), HashKey(k))
	}
	h := r.Header.Get("Authorization")
	// Credentials travel only in headers. No cookie is ever read as a credential, so a browser
	// cannot be made to attach one to a cross-site request: there is nothing for CSRF to ride on.
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return Principal{}, errors.New("missing credentials")
	}
	p, local, err := a.verify(tok)
	if err != nil {
		return Principal{}, err
	}
	if local && !p.Unbound && a.sessions != nil {
		ok, err := a.sessions.SessionValid(r.Context(), p.TenantID, p.UserID, p.TokenVersion)
		if err != nil {
			return Principal{}, err
		}
		if !ok {
			return Principal{}, errors.New("session ended")
		}
	}
	return p, nil
}

// Verify validates a bearer token (local HS256 or OIDC RS256).
func (a *Authenticator) Verify(tok string) (Principal, error) {
	p, _, err := a.verify(tok)
	return p, err
}

// verify is Verify that also says whether the token is a local one (checked against sessions).
func (a *Authenticator) verify(tok string) (Principal, bool, error) {
	var c Claims
	t, err := jwt.ParseWithClaims(tok, &c, func(t *jwt.Token) (any, error) {
		switch t.Method.(type) {
		case *jwt.SigningMethodHMAC:
			return a.secret, nil
		default:
			if a.oidc == nil {
				return nil, errors.New("unexpected signing method")
			}
			return a.oidc.Keyfunc(t)
		}
	}, jwt.WithValidMethods([]string{"HS256", "RS256", "ES256"}))
	if err != nil {
		return Principal{}, false, err
	}
	// Tokens from the identity provider must come from the configured realm and be meant for
	// this API: any other client in the same realm can obtain validly signed tokens too.
	if _, local := t.Method.(*jwt.SigningMethodHMAC); !local {
		if c.Issuer != strings.TrimSuffix(a.issuer, "/") {
			return Principal{}, false, errors.New("token issuer is not the configured identity provider")
		}
		if a.audience != "" && !slices.Contains(c.Audience, a.audience) {
			return Principal{}, false, errors.New("token audience does not include " + a.audience)
		}
	}
	tid, err := uuid.Parse(c.Tenant)
	if err != nil {
		return Principal{}, false, errors.New("token has no tenant")
	}
	uid, _ := uuid.Parse(c.Subject)
	_, local := t.Method.(*jwt.SigningMethodHMAC)
	return Principal{TenantID: tid, UserID: uid, Email: c.Email, Name: c.Name, Roles: c.Roles, Kind: "user", Operator: c.Operator,
		TokenVersion: c.Ver, Unbound: c.Unbound, MFA: c.MFA, MFASetupRequired: c.MFASetup}, local, nil
}

// Middleware requires an authenticated principal.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Authenticate(r)
		if err != nil {
			httpx.Fail(w, r, httpx.Unauthorized("authentication required"))
			return
		}
		if p.MFASetupRequired && !mfaSetupPath(r.URL.Path) {
			httpx.Fail(w, r, httpx.Forbidden("multi-factor enrolment required: set up a second factor before using the API"))
			return
		}
		next.ServeHTTP(w, r.WithContext(With(r.Context(), p)))
	})
}

// mfaSetupPath lists what a session that must still enrol a second factor may call: who it is,
// the enrolment itself, password change and sign-out.
func mfaSetupPath(path string) bool {
	path = strings.TrimPrefix(path, "/api/v1")
	return path == "/me" || path == "/auth/logout" || path == "/auth/password" || path == "/auth/mfa" || strings.HasPrefix(path, "/auth/mfa/")
}

// Require allows the request when the principal has any role. Viewer may only read.
func Require(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := From(r.Context())
			if !ok {
				httpx.Fail(w, r, httpx.Unauthorized("authentication required"))
				return
			}
			if r.Method == http.MethodGet && p.Has(append([]string{RoleViewer}, roles...)...) {
				next.ServeHTTP(w, r)
				return
			}
			if !p.Has(roles...) {
				httpx.Fail(w, r, httpx.Forbidden("missing role: one of "+strings.Join(roles, ", ")))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireStrict allows the request only when the principal has one of roles, whatever the
// method: no read access for Viewer. For bulk or sensitive reads such as the tenant export.
func RequireStrict(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := From(r.Context())
			if !ok {
				httpx.Fail(w, r, httpx.Unauthorized("authentication required"))
				return
			}
			if !p.Has(roles...) {
				httpx.Fail(w, r, httpx.Forbidden("missing role: one of "+strings.Join(roles, ", ")))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireOperator allows only platform operators.
func RequireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := From(r.Context())
		if !ok || !p.Operator {
			httpx.Fail(w, r, httpx.Forbidden("operator only"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
