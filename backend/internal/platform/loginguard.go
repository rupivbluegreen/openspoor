package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// Brute-force protection for password logins (OWASP ASVS V2.2). Failures are counted per account
// (tenant + e-mail address) and per client address in the login_attempt table, so every node of a
// cluster shares the counters. After the threshold, further attempts are refused without checking
// the password, for a time that doubles with each further failure up to a ceiling. The account key
// exists whether or not the user does, so the refusal reveals nothing about which accounts exist.
const (
	accountThreshold = 5  // failures per account before the first lockout
	addressThreshold = 30 // failures per client address; higher because offices share one address
	lockBase         = 30 * time.Second
	lockMax          = 15 * time.Minute
	failureWindow    = 30 * time.Minute // a failure older than this no longer counts
)

// lockDuration is the lockout after the n-th failure when the threshold is t (zero below it).
func lockDuration(n, t int) time.Duration {
	if n < t {
		return 0
	}
	d := time.Duration(float64(lockBase) * math.Pow(2, float64(n-t)))
	if d > lockMax || d <= 0 {
		return lockMax
	}
	return d
}

type ipCtxKey struct{}

// WithClientIP records the client address for login throttling.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ipCtxKey{}, ip)
}

func clientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(ipCtxKey{}).(string)
	return ip
}

// ClientIP is the address of the request's client. trustedProxies is how many reverse proxies in
// front of this server append to X-Forwarded-For; with 0 the TCP peer is used and the header is ignored,
// because anyone could forge it. With n proxies the n-th entry from the right is the client.
func ClientIP(r *http.Request, trustedProxies int) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if trustedProxies <= 0 {
		return peer
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				hops = append(hops, p)
			}
		}
	}
	if len(hops) == 0 {
		return peer
	}
	i := len(hops) - trustedProxies
	if i < 0 {
		i = 0
	}
	if ip := net.ParseIP(hops[i]); ip != nil {
		return ip.String()
	}
	return peer
}

// lockedError is the refusal while an account or address is locked out.
type lockedError struct {
	err     error
	seconds int
}

func (e *lockedError) Error() string { return e.err.Error() }
func (e *lockedError) Unwrap() error { return e.err }

func tooManyAttempts(wait time.Duration) error {
	return &lockedError{err: httpx.TooMany("too many failed sign-in attempts; try again later"), seconds: int(math.Ceil(wait.Seconds()))}
}

// RetryAfter returns the Retry-After seconds when err is a lockout refusal.
func RetryAfter(err error) (int, bool) {
	var le *lockedError
	if errors.As(err, &le) {
		return le.seconds, true
	}
	return 0, false
}

// guardKey is a keyed hash, so the table reveals neither e-mail addresses nor client addresses.
func (m *Module) guardKey(kind string, parts ...string) string {
	h := hmac.New(sha256.New, append([]byte("login-attempt|"), m.Cfg.MasterKey...))
	h.Write([]byte(strings.Join(parts, "\x00")))
	return kind + ":" + hex.EncodeToString(h.Sum(nil))[:32]
}

type guardKeys struct{ account, address string }

func (g guardKeys) all() []string {
	if g.address == "" {
		return []string{g.account}
	}
	return []string{g.account, g.address}
}

// keysFor derives the throttling keys; account is a tenant id (or the unknown tenant slug) plus the e-mail.
func (m *Module) keysFor(ctx context.Context, tenantRef, email string) guardKeys {
	g := guardKeys{account: m.guardKey("a", tenantRef, email)}
	if ip := clientIPFrom(ctx); ip != "" {
		g.address = m.guardKey("i", ip)
	}
	return g
}

// guardCheck returns a lockout refusal when any key is locked. Database errors fail closed.
func (m *Module) guardCheck(ctx context.Context, g guardKeys) error {
	var secs float64
	if err := m.DB.Pool.QueryRow(ctx, `select coalesce(extract(epoch from max(locked_until) - now()), 0)::float8 from login_attempt where key = any($1) and locked_until > now()`,
		g.all()).Scan(&secs); err != nil {
		return err
	}
	if secs > 0 {
		return tooManyAttempts(time.Duration(secs * float64(time.Second)))
	}
	return nil
}

// guardFail records a failed attempt on one key and returns the new failure count and any new lockout.
func (m *Module) guardFail(ctx context.Context, key string, threshold int) (failures int, locked time.Duration, err error) {
	err = m.DB.Pool.QueryRow(ctx, `insert into login_attempt (key, failures, last_failed_at) values ($1, 1, now())
		on conflict (key) do update set
		  failures = case when login_attempt.last_failed_at < now() - make_interval(secs => $2) then 1 else login_attempt.failures + 1 end,
		  last_failed_at = now()
		returning failures`, key, failureWindow.Seconds()).Scan(&failures)
	if err != nil {
		return 0, 0, err
	}
	if locked = lockDuration(failures, threshold); locked > 0 {
		_, err = m.DB.Pool.Exec(ctx, `update login_attempt set locked_until = now() + make_interval(secs => $2) where key = $1`, key, locked.Seconds())
	}
	// Opportunistic cleanup of stale rows keeps the table small without a scheduled job.
	_, _ = m.DB.Pool.Exec(ctx, `delete from login_attempt where last_failed_at < now() - interval '1 day'`)
	return failures, locked, err
}

// loginFailed counts one failed attempt against the account and the client address. tenant and
// userID are zero when the tenant or the user is unknown. A burst that triggers a lockout of a known
// tenant's account raises an `auth.login_failures` alert and an audit entry; neither holds the
// attempted password or e-mail address (the user is referenced by id, when it exists).
func (m *Module) loginFailed(ctx context.Context, g guardKeys, tenant, userID uuid.UUID) {
	failures, locked, err := m.guardFail(ctx, g.account, accountThreshold)
	if err != nil {
		slog.ErrorContext(ctx, "login throttle: cannot record failure", "err", err)
	}
	if g.address != "" {
		if _, l, err := m.guardFail(ctx, g.address, addressThreshold); err != nil {
			slog.ErrorContext(ctx, "login throttle: cannot record failure", "err", err)
		} else if l > 0 {
			slog.WarnContext(ctx, "login throttle: client address locked out", "ip", clientIPFrom(ctx), "seconds", int(l.Seconds()))
		}
	}
	// One alert per burst: raised when the account first locks, again only after the window passes.
	if locked == 0 || failures != accountThreshold || tenant == uuid.Nil {
		return
	}
	detail, entityID := map[string]any{"failures": failures, "lockedSeconds": int(locked.Seconds())}, ""
	if userID != uuid.Nil {
		entityID = userID.String()
		detail["userId"] = entityID
	}
	err = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			values ($1, app_tenant(), 'auth.login_failures', 'warning', $2, $3, 'user', $4, $5) on conflict do nothing`,
			ids.New(), "Repeated failed sign-ins; the account is temporarily locked", detail, entityID, "login-failures:"+g.account); err != nil {
			return err
		}
		_, err := audit.WriteAs(ctx, tx, tenant, "system", audit.Record{Action: "auth.login_failures", Entity: "user", EntityID: entityID, After: detail})
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "login throttle: cannot raise alert", "err", err)
	}
}

// loginSucceeded forgets the account's failures. The address counter is kept on purpose: otherwise
// one valid login between guesses would reset it.
func (m *Module) loginSucceeded(ctx context.Context, g guardKeys) {
	if _, err := m.DB.Pool.Exec(ctx, `delete from login_attempt where key = $1`, g.account); err != nil {
		slog.ErrorContext(ctx, "login throttle: cannot reset counter", "err", err)
	}
}

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// burnPassword spends the time of one password check when there is nothing to compare against, so
// an unknown user or tenant cannot be told from a wrong password by the response time.
func burnPassword(password string) {
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("openspoor-timing-equaliser"), bcrypt.DefaultCost)
	})
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}
