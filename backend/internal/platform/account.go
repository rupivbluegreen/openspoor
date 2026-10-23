package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// Account security: password change and recovery, TOTP second factor, session revocation and the
// tenant's second-factor policy (OWASP ASVS V2.1, V2.5, V2.8, V3.3). Credentials, secrets and codes
// never appear in audit entries, logs or API responses other than where a value is shown once.

const (
	resetTTL           = 30 * time.Minute
	recoveryCodeCount  = 10
	resetAccountLimit  = 3  // recovery requests per account and window before back-off
	resetAddressLimit  = 20 // per client address
	resetConfirmLimit  = 10 // wrong recovery tokens per client address
	mailTimeout        = 20 * time.Second
	mfaIssuer          = "Open Spoor"
	policyRequireAdmin = "requireMfaForAdmin"
)

// ErrMFARequired answers a correct password for an account that has a second factor but was given no code.
var ErrMFARequired = &httpx.Error{Status: http.StatusUnauthorized, Msg: "verification code required", Extra: map[string]any{"mfaRequired": true}}

// userSec is the security state of a user row read during sign-in.
type userSec struct {
	mfaEnabled bool
	secret     []byte
	lastStep   int64
}

// SessionValid implements auth.SessionChecker: the user exists, is active and the token carries the current version.
func (m *Module) SessionValid(ctx context.Context, tenant, user uuid.UUID, version int) (bool, error) {
	var cur int
	var status string
	err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select token_version, status from app_user where id=$1`, user).Scan(&cur, &status)
	})
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	return err == nil && cur == version && status == "active", err
}

// VerifyPassword re-authenticates a user by password (throttled like a sign-in); it does not check a second factor.
func (m *Module) VerifyPassword(ctx context.Context, tenant uuid.UUID, email, password string) error {
	_, err := m.checkPassword(ctx, tenant, email, password)
	return err
}

func (m *Module) keyedHash(purpose string, parts ...string) string {
	h := hmac.New(sha256.New, append([]byte(purpose+"|"), m.Cfg.MasterKey...))
	h.Write([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Module) recoveryHash(user uuid.UUID, code string) string {
	return m.keyedHash("recovery-code", user.String(), auth.NormalizeRecoveryCode(code))
}

func mfaAAD(user uuid.UUID) []byte { return []byte("mfa:" + user.String()) }

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// notify sends a notification e-mail in the background; failures are logged, never returned, so the
// response does not depend on the relay (and cannot reveal whether a message was sent).
func (m *Module) notify(to, subject, body string) {
	if m.Mail == nil || to == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), mailTimeout)
		defer cancel()
		if err := m.Mail.Send(ctx, to, subject, body); err != nil {
			slog.Warn("account notification not sent", "err", err)
		}
	}()
}

// requireMFA reports the tenant's policy that administrators must use a second factor.
func requireMFA(ctx context.Context, tx pgx.Tx) (bool, error) {
	var v bool
	err := tx.QueryRow(ctx, `select coalesce((settings->>'`+policyRequireAdmin+`')::boolean, false) from tenant where id=app_tenant()`).Scan(&v)
	return v, err
}

// issueFresh signs a new token for p after a change that raised its token version.
func (m *Module) issueFresh(w http.ResponseWriter, status int, p auth.Principal, version int, extra map[string]any) error {
	p.TokenVersion = version
	tok, exp, err := m.Auth.Issue(p)
	if err != nil {
		return err
	}
	out := map[string]any{"token": tok, "expiresAt": exp, "principal": p}
	for k, v := range extra {
		out[k] = v
	}
	httpx.JSON(w, status, out)
	return nil
}

// --- password change ---

func (m *Module) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	if p.Kind != "user" || p.Unbound {
		return httpx.Forbidden("only a signed-in user can change a password")
	}
	ctx := WithClientIP(r.Context(), ClientIP(r, m.Cfg.TrustedProxies))
	_, _, g, err := m.verifyPassword(ctx, p.TenantID, p.Email, in.CurrentPassword)
	if err != nil {
		return err
	}
	if err := ValidatePassword(in.NewPassword, p.Email); err != nil {
		return err
	}
	if in.NewPassword == in.CurrentPassword {
		return httpx.BadRequest("the new password must differ from the current one")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var version int
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `update app_user set password_hash=$2, password_changed_at=now(), token_version=token_version+1 where id=$1 returning token_version`,
			p.UserID, string(hash)).Scan(&version); err != nil {
			return err
		}
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "user.password.change", Entity: "user", EntityID: p.UserID.String()})
		return err
	})
	if err != nil {
		return err
	}
	m.loginSucceeded(ctx, g)
	m.notify(p.Email, "Your Open Spoor password was changed",
		"The password of your Open Spoor account was just changed and all other sessions were signed out.\n\nIf this was not you, contact your administrator at once.\n")
	return m.issueFresh(w, http.StatusOK, p, version, nil)
}

// --- password recovery ---

// forgotPassword always answers 202: whether the account exists, has a local password or is active is
// never revealed. Requests are throttled per account and per client address by the login guard (a
// refusal is the same for existing and non-existing accounts, because the counters key on the request).
func (m *Module) forgotPassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Tenant string `json:"tenant"`
		Email  string `json:"email"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Tenant == "" && m.HostTenant != nil {
		if slug, ok := m.HostTenant(r.Context(), r.Host); ok {
			in.Tenant = slug
		}
	}
	ctx := WithClientIP(r.Context(), ClientIP(r, m.Cfg.TrustedProxies))
	email := normEmail(in.Email)
	g := guardKeys{account: m.guardKey("r", strings.ToLower(strings.TrimSpace(in.Tenant)), email)}
	if ip := clientIPFrom(ctx); ip != "" {
		g.address = m.guardKey("ri", ip)
	}
	if err := m.guardCheck(ctx, g); err != nil {
		if secs, ok := RetryAfter(err); ok {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		return err
	}
	for key, limit := range map[string]int{g.account: resetAccountLimit, g.address: resetAddressLimit} {
		if key == "" {
			continue
		}
		if _, _, err := m.guardFail(ctx, key, limit); err != nil {
			slog.ErrorContext(ctx, "password recovery throttle", "err", err)
		}
	}
	m.startRecovery(ctx, in.Tenant, email)
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	return nil
}

// startRecovery creates and mails a token when the account exists. Both outcomes do comparable work
// (a token is always generated and hashed) and the mail goes out in the background.
func (m *Module) startRecovery(ctx context.Context, tenantSlug, email string) {
	token := ids.Secret(32)
	_ = tokenHash(token)
	if m.Mail == nil {
		slog.WarnContext(ctx, "password recovery requested but no SMTP relay is configured (OPENSPOOR_SMTP_URL)")
		return
	}
	t, err := m.TenantBySlug(ctx, tenantSlug)
	if err != nil || t.Status != "active" || !strings.Contains(email, "@") {
		return
	}
	token = t.ID.String() + "." + token
	var found bool
	err = m.DB.Tx(ctx, t.ID, func(tx pgx.Tx) error {
		var uid uuid.UUID
		err := tx.QueryRow(ctx, `select id from app_user where email=$1 and status='active' and password_hash is not null`, email).Scan(&uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		// A new request replaces earlier unused tokens.
		if _, err := tx.Exec(ctx, `delete from password_reset where user_id=$1 and used_at is null`, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into password_reset (id, tenant_id, user_id, token_hash, expires_at) values ($1, app_tenant(), $2, $3, now() + make_interval(secs => $4))`,
			ids.New(), uid, tokenHash(token), resetTTL.Seconds()); err != nil {
			return err
		}
		_, err = audit.WriteAs(ctx, tx, t.ID, "anonymous", audit.Record{Action: "user.password.reset_requested", Entity: "user", EntityID: uid.String()})
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "password recovery: cannot create token", "err", err)
		return
	}
	if found {
		link := strings.TrimSuffix(m.Cfg.PublicURL, "/") + "/reset-password?token=" + token
		m.notify(email, "Reset your Open Spoor password",
			"Someone asked to reset the password of this Open Spoor account.\n\nOpen this link within "+strconv.Itoa(int(resetTTL.Minutes()))+
				" minutes to choose a new password. It works once.\n\n"+link+"\n\nIf you did not ask for this, ignore this message: your password has not changed.\n")
	}
}

func (m *Module) resetPassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := WithClientIP(r.Context(), ClientIP(r, m.Cfg.TrustedProxies))
	g := guardKeys{account: m.guardKey("rc", clientIPFrom(ctx))}
	if err := m.guardCheck(ctx, g); err != nil {
		if secs, ok := RetryAfter(err); ok {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		return err
	}
	invalid := func() error {
		if _, _, err := m.guardFail(ctx, g.account, resetConfirmLimit); err != nil {
			slog.ErrorContext(ctx, "password recovery throttle", "err", err)
		}
		return httpx.BadRequest("the link is invalid or has expired; request a new one")
	}
	tid, _, ok := strings.Cut(in.Token, ".")
	tenant, err := uuid.Parse(tid)
	if !ok || err != nil {
		return invalid()
	}
	var email string
	var userID uuid.UUID
	err = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		var prID uuid.UUID
		err := tx.QueryRow(ctx, `select pr.id, u.id, u.email from password_reset pr join app_user u on u.id=pr.user_id
			where pr.token_hash=$1 and pr.used_at is null and pr.expires_at > now() and u.status='active' for update of pr`, tokenHash(in.Token)).Scan(&prID, &userID, &email)
		if err != nil {
			return err
		}
		// A weak password leaves the token usable so the person can try again.
		if err := ValidatePassword(in.NewPassword, email); err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update app_user set password_hash=$2, password_changed_at=now(), token_version=token_version+1 where id=$1`, userID, string(hash)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update password_reset set used_at=now() where id=$1`, prID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from password_reset where user_id=$1 and used_at is null`, userID); err != nil {
			return err
		}
		_, err = audit.WriteAs(ctx, tx, tenant, "user:"+userID.String(), audit.Record{Action: "user.password.reset", Entity: "user", EntityID: userID.String()})
		return err
	})
	if errors.Is(err, db.ErrNotFound) {
		return invalid()
	}
	if err != nil {
		return err
	}
	m.notify(email, "Your Open Spoor password was changed",
		"The password of your Open Spoor account was just reset with an e-mailed link and all sessions were signed out.\n\nIf this was not you, contact your administrator at once.\n")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- sign-out ---

// logout ends every session of the user: all earlier local tokens stop working.
func (m *Module) logout(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFrom(r.Context())
	if p.Kind != "user" || p.Unbound {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		ct, err := tx.Exec(r.Context(), `update app_user set token_version=token_version+1 where id=$1`, p.UserID)
		if err != nil || ct.RowsAffected() == 0 {
			return err // an identity-provider user has no row here: nothing to revoke
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "auth.logout", Entity: "user", EntityID: p.UserID.String()})
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- second factor ---

type mfaStatus struct {
	Enabled           bool `json:"enabled"`
	Pending           bool `json:"pending"`
	RecoveryRemaining int  `json:"recoveryCodesRemaining"`
	RequiredByPolicy  bool `json:"requiredByPolicy"`
}

func (m *Module) userOnly(p auth.Principal) error {
	if p.Kind != "user" || p.Unbound {
		return httpx.Forbidden("only a signed-in user has a second factor")
	}
	return nil
}

func (m *Module) mfaStatus(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFrom(r.Context())
	if err := m.userOnly(p); err != nil {
		return err
	}
	var s mfaStatus
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var secret []byte
		if err := tx.QueryRow(r.Context(), `select mfa_enabled, mfa_secret from app_user where id=$1`, p.UserID).Scan(&s.Enabled, &secret); err != nil {
			return err
		}
		s.Pending = !s.Enabled && len(secret) > 0
		if err := tx.QueryRow(r.Context(), `select count(*) from user_recovery_code where user_id=$1 and used_at is null`, p.UserID).Scan(&s.RecoveryRemaining); err != nil {
			return err
		}
		req, err := requireMFA(r.Context(), tx)
		s.RequiredByPolicy = req && p.Has(auth.RoleAdmin)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, s)
	return nil
}

// mfaEnrol creates a pending secret. It is not in force until confirmed with a valid code.
func (m *Module) mfaEnrol(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFrom(r.Context())
	if err := m.userOnly(p); err != nil {
		return err
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		return err
	}
	sealed, err := m.Keys.Seal(r.Context(), p.TenantID, secret, mfaAAD(p.UserID))
	if err != nil {
		return err
	}
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		ct, err := tx.Exec(r.Context(), `update app_user set mfa_secret=$2 where id=$1 and not mfa_enabled`, p.UserID, sealed)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.Conflict("a second factor is already enabled; disable it first")
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "mfa.enrol", Entity: "user", EntityID: p.UserID.String()})
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"secret": auth.EncodeTOTPSecret(secret), "otpauthUri": auth.TOTPURI(mfaIssuer, p.Email, secret)})
	return nil
}

// newRecoveryCodes replaces the user's recovery codes and returns the new ones in clear (shown once).
func (m *Module) newRecoveryCodes(ctx context.Context, tx pgx.Tx, user uuid.UUID) ([]string, error) {
	if _, err := tx.Exec(ctx, `delete from user_recovery_code where user_id=$1`, user); err != nil {
		return nil, err
	}
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		c, err := auth.NewRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes[i] = c
		if _, err := tx.Exec(ctx, `insert into user_recovery_code (id, tenant_id, user_id, code_hash) values ($1, app_tenant(), $2, $3)`, ids.New(), user, m.recoveryHash(user, c)); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

func (m *Module) mfaConfirm(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	if err := m.userOnly(p); err != nil {
		return err
	}
	ctx := WithClientIP(r.Context(), ClientIP(r, m.Cfg.TrustedProxies))
	g := m.keysFor(ctx, p.TenantID.String(), normEmail(p.Email))
	if err := m.guardCheck(ctx, g); err != nil {
		return err
	}
	var codes []string
	var version int
	bad := false
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var sealed []byte
		var enabled bool
		if err := tx.QueryRow(r.Context(), `select mfa_secret, mfa_enabled from app_user where id=$1 for update`, p.UserID).Scan(&sealed, &enabled); err != nil {
			return err
		}
		if enabled {
			return httpx.Conflict("a second factor is already enabled")
		}
		if len(sealed) == 0 {
			return httpx.BadRequest("start enrolment first")
		}
		secret, err := m.Keys.Open(r.Context(), p.TenantID, sealed, mfaAAD(p.UserID))
		if err != nil {
			return err
		}
		step, err := auth.VerifyTOTP(secret, in.Code, time.Now())
		if err != nil {
			bad = true
			return nil
		}
		if codes, err = m.newRecoveryCodes(r.Context(), tx, p.UserID); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `update app_user set mfa_enabled=true, mfa_enabled_at=now(), mfa_last_step=$2, token_version=token_version+1 where id=$1 returning token_version`,
			p.UserID, step).Scan(&version); err != nil {
			return err
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "mfa.enable", Entity: "user", EntityID: p.UserID.String(), After: map[string]any{"recoveryCodes": len(codes)}})
		return err
	})
	if err != nil {
		return err
	}
	if bad {
		m.loginFailed(ctx, g, p.TenantID, p.UserID)
		return httpx.BadRequest("invalid verification code")
	}
	m.loginSucceeded(ctx, g)
	p.MFA, p.MFASetupRequired = true, false
	return m.issueFresh(w, http.StatusOK, p, version, map[string]any{"recoveryCodes": codes})
}

// requireSecondFactor re-authenticates for a sensitive second-factor change: current password and a
// valid code (authenticator or recovery code). Failures count against the sign-in throttle.
func (m *Module) requireSecondFactor(r *http.Request, p auth.Principal, password, code string) (context.Context, guardKeys, error) {
	ctx := WithClientIP(r.Context(), ClientIP(r, m.Cfg.TrustedProxies))
	_, u, g, err := m.verifyPassword(ctx, p.TenantID, p.Email, password)
	if err != nil {
		return ctx, g, err
	}
	if !u.mfaEnabled {
		return ctx, g, httpx.Conflict("no second factor is enabled")
	}
	if err := m.verifySecondFactor(ctx, p.TenantID, p.UserID, u, code); err != nil {
		m.loginFailed(ctx, g, p.TenantID, p.UserID)
		return ctx, g, err
	}
	return ctx, g, nil
}

func (m *Module) mfaDisable(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	if err := m.userOnly(p); err != nil {
		return err
	}
	ctx, g, err := m.requireSecondFactor(r, p, in.Password, in.Code)
	if err != nil {
		return err
	}
	var version int
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		if req, err := requireMFA(r.Context(), tx); err != nil {
			return err
		} else if req && p.Has(auth.RoleAdmin) {
			return httpx.Forbidden("this tenant requires administrators to use a second factor")
		}
		if err := m.clearMFA(r.Context(), tx, p.UserID, &version); err != nil {
			return err
		}
		_, err := audit.Write(r.Context(), tx, audit.Record{Action: "mfa.disable", Entity: "user", EntityID: p.UserID.String()})
		return err
	})
	if err != nil {
		return err
	}
	m.loginSucceeded(ctx, g)
	m.notify(p.Email, "Two-factor authentication was turned off", "Two-factor authentication was turned off for your Open Spoor account and all other sessions were signed out.\n\nIf this was not you, contact your administrator at once.\n")
	p.MFA = false
	return m.issueFresh(w, http.StatusOK, p, version, nil)
}

// clearMFA removes the second factor and raises the token version.
func (m *Module) clearMFA(ctx context.Context, tx pgx.Tx, user uuid.UUID, version *int) error {
	if _, err := tx.Exec(ctx, `delete from user_recovery_code where user_id=$1`, user); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `update app_user set mfa_enabled=false, mfa_secret=null, mfa_enabled_at=null, mfa_last_step=0, token_version=token_version+1 where id=$1 returning token_version`, user).Scan(version)
}

func (m *Module) mfaRecoveryCodes(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	if err := m.userOnly(p); err != nil {
		return err
	}
	ctx, g, err := m.requireSecondFactor(r, p, in.Password, in.Code)
	if err != nil {
		return err
	}
	var codes []string
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if codes, err = m.newRecoveryCodes(r.Context(), tx, p.UserID); err != nil {
			return err
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "mfa.recovery_codes.regenerate", Entity: "user", EntityID: p.UserID.String(), After: map[string]any{"recoveryCodes": len(codes)}})
		return err
	})
	if err != nil {
		return err
	}
	m.loginSucceeded(ctx, g)
	httpx.JSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
	return nil
}

// resetUserMFA lets an administrator remove another user's second factor (lost device and recovery codes).
func (m *Module) resetUserMFA(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var email string
		var enabled bool
		if err := tx.QueryRow(r.Context(), `select email, mfa_enabled from app_user where id=$1`, id).Scan(&email, &enabled); err != nil {
			return err
		}
		if !enabled {
			return httpx.Conflict("the user has no second factor")
		}
		var version int
		if err := m.clearMFA(r.Context(), tx, id, &version); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "mfa.reset", Entity: "user", EntityID: id.String(), Reason: in.Reason}); err != nil {
			return err
		}
		m.notify(email, "Two-factor authentication was reset", "An administrator reset two-factor authentication for your Open Spoor account. Sign in again"+
			" and set it up again if your organisation requires it.\n")
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// verifySecondFactor accepts a current authenticator code (each time step once) or an unused recovery code.
func (m *Module) verifySecondFactor(ctx context.Context, tenant, user uuid.UUID, u userSec, code string) error {
	bad := httpx.Unauthorized("invalid verification code")
	if auth.IsRecoveryCode(code) {
		err := m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `update user_recovery_code set used_at=now() where user_id=$1 and code_hash=$2 and used_at is null returning id`,
				user, m.recoveryHash(user, code)).Scan(&id); err != nil {
				return err
			}
			_, err := audit.WriteAs(ctx, tx, tenant, "user:"+user.String(), audit.Record{Action: "mfa.recovery_code.use", Entity: "user", EntityID: user.String()})
			return err
		})
		if errors.Is(err, db.ErrNotFound) {
			return bad
		}
		return err
	}
	secret, err := m.Keys.Open(ctx, tenant, u.secret, mfaAAD(user))
	if err != nil {
		return err
	}
	step, err := auth.VerifyTOTP(secret, code, time.Now())
	if err != nil {
		return bad
	}
	// The step is recorded atomically, so a code (or an earlier one) cannot be used twice.
	var ok bool
	err = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `update app_user set mfa_last_step=$2 where id=$1 and mfa_last_step < $2`, user, int64(step))
		ok = err == nil && ct.RowsAffected() == 1
		return err
	})
	if err != nil {
		return err
	}
	if !ok {
		return bad
	}
	return nil
}

// --- tenant policy ---

type securityPolicy struct {
	RequireMFAForAdmin bool `json:"requireMfaForAdmin"`
}

func (m *Module) getSecurityPolicy(w http.ResponseWriter, r *http.Request) error {
	var s securityPolicy
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		s.RequireMFAForAdmin, err = requireMFA(r.Context(), tx)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, s)
	return nil
}

// putSecurityPolicy sets the tenant policy. Turning the second-factor requirement on needs the caller
// to have a second factor already (no lock-out), and ends the sessions of administrators without one so
// that their next sign-in is limited to enrolment.
func (m *Module) putSecurityPolicy(w http.ResponseWriter, r *http.Request) error {
	var in securityPolicy
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := requireMFA(r.Context(), tx)
		if err != nil {
			return err
		}
		if in.RequireMFAForAdmin && !before {
			var mine bool
			if p.Kind != "user" || tx.QueryRow(r.Context(), `select mfa_enabled from app_user where id=$1`, p.UserID).Scan(&mine) != nil || !mine {
				return httpx.Conflict("enable a second factor for your own account before requiring it for administrators")
			}
		}
		if _, err := tx.Exec(r.Context(), `update tenant set settings = jsonb_set(settings, '{`+policyRequireAdmin+`}', to_jsonb($1::boolean)) where id=app_tenant()`, in.RequireMFAForAdmin); err != nil {
			return err
		}
		if in.RequireMFAForAdmin && !before {
			if _, err := tx.Exec(r.Context(), `update app_user set token_version=token_version+1 where 'Admin' = any(roles) and not mfa_enabled`); err != nil {
				return err
			}
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "tenant.security_policy.update", Entity: "tenant", EntityID: p.TenantID.String(),
			Before: securityPolicy{RequireMFAForAdmin: before}, After: in}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, in)
		return nil
	})
}

// accountRoutes mounts the self-service credential routes (any signed-in user, Viewer included: they
// change only the caller's own credentials, not tenant data) and the tenant policy.
func (m *Module) accountRoutes(r chi.Router) {
	r.Post("/auth/password", httpx.Handler(m.changePassword))
	r.Post("/auth/logout", httpx.Handler(m.logout))
	r.Get("/auth/mfa", httpx.Handler(m.mfaStatus))
	r.Post("/auth/mfa/enrol", httpx.Handler(m.mfaEnrol))
	r.Post("/auth/mfa/confirm", httpx.Handler(m.mfaConfirm))
	r.Post("/auth/mfa/disable", httpx.Handler(m.mfaDisable))
	r.Post("/auth/mfa/recovery-codes", httpx.Handler(m.mfaRecoveryCodes))
	r.Route("/tenant/security", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin))
		r.Get("/", httpx.Handler(m.getSecurityPolicy))
		r.Put("/", httpx.Handler(m.putSecurityPolicy))
	})
}
