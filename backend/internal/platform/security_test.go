package platform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

func TestValidatePassword(t *testing.T) {
	ok := []struct{ pw, email string }{
		{"correct-horse-battery", "admin@acme.test"},
		{"openspoor-demo", "ops@openspoor.example"}, // local part "ops" is too short to compare
		{"a-long passphrase with spaces", ""},
		{"Tr0ub4dor&3xyz", "someone@example.com"},
		{strings.Repeat("kQ3v-Lm8x", 8), ""}, // exactly 72 bytes
		{"ünïcödé-pässwörd-ok", ""},          // 10+ characters, counted as characters
	}
	for _, c := range ok {
		if err := platform.ValidatePassword(c.pw, c.email); err != nil {
			t.Errorf("%q / %q rejected: %v", c.pw, c.email, err)
		}
	}
	bad := []struct{ pw, email, want string }{
		{"short", "", "at least 10"},
		{"ünïcödé9", "", "at least 10"},
		{strings.Repeat("a1", 37), "", "at most 72"},
		{"password123", "", "too common"},
		{"Password123", "", "too common"},
		{"P@ssw0rd123", "", "too common"},
		{"qwertyuiop", "", "too common"},
		{"1234567890", "", "too common"},
		{"p-a-s-s-w-o-r-d-1-2-3", "", "too common"},
		{"password2031", "", "too common"}, // listed word plus digits
		{"aaaaaaaaaaaa", "", "too common"},
		{"abababababab", "", "too common"},
		{"123123123123", "", "too common"},
		{"my-alice-secret-1", "alice@example.com", "e-mail"},
		{"MY-ALICE-secret-1", "Alice@example.com", "e-mail"},
		{"xx-alice-yy-zz", "alice+shop@example.com", "e-mail"},
	}
	for _, c := range bad {
		err := platform.ValidatePassword(c.pw, c.email)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q / %q: err = %v, want %q", c.pw, c.email, err, c.want)
		}
	}
}

func TestPasswordPolicyOnEndpoints(t *testing.T) {
	e := testkit.New(t)
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "bob@acme.test", "roles": []string{"Ops"}, "password": "password123"}), 400, "too common")
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "robert@acme.test", "roles": []string{"Ops"}, "password": "xx-robert-secret"}), 400, "e-mail")
	problem(t, e.Do("POST", "/api/v1/users", map[string]any{"email": "bob@acme.test", "roles": []string{"Ops"}, "password": strings.Repeat("a1", 40)}), 400, "at most 72")
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "bob@acme.test", "roles": []string{"Ops"}, "password": "a-fine-passphrase"}), nil)
	// A user without a local password (SSO only) is still allowed.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "sso@acme.test", "roles": []string{"Ops"}}), nil)

	ctx := context.Background()
	_, _, err := e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "weak", Name: "W", AdminEmail: "carol@weak.test", AdminPassword: "welcome123"})
	if err == nil || !strings.Contains(err.Error(), "too common") {
		t.Fatalf("weak admin password: %v", err)
	}
	_, _, err = e.App.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "weak", Name: "W", AdminEmail: "carol@weak.test", AdminPassword: "carol-is-my-name"})
	if err == nil || !strings.Contains(err.Error(), "e-mail") {
		t.Fatalf("admin password with the local part: %v", err)
	}
}

func TestClientIP(t *testing.T) {
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	for name, tc := range map[string]struct {
		r       *http.Request
		proxies int
		want    string
	}{
		"direct": {req("203.0.113.9:5555"), 0, "203.0.113.9"},
		"header ignored when no proxy is trusted": {req("10.0.0.1:1", "198.51.100.7"), 0, "10.0.0.1"},
		"one proxy":                  {req("10.0.0.1:1", "198.51.100.7"), 1, "198.51.100.7"},
		"forged left entry ignored":  {req("10.0.0.1:1", "1.2.3.4, 198.51.100.7"), 1, "198.51.100.7"},
		"two proxies":                {req("10.0.0.1:1", "198.51.100.7, 10.0.0.2"), 2, "198.51.100.7"},
		"split headers":              {req("10.0.0.1:1", "198.51.100.7", "10.0.0.2"), 2, "198.51.100.7"},
		"fewer entries than proxies": {req("10.0.0.1:1", "198.51.100.7"), 3, "198.51.100.7"},
		"no header":                  {req("10.0.0.1:1"), 1, "10.0.0.1"},
		"garbage entry":              {req("10.0.0.1:1", "not-an-ip"), 1, "10.0.0.1"},
		"remote without port":        {req("10.0.0.1"), 0, "10.0.0.1"},
		"ipv6":                       {req("[2001:db8::1]:443"), 0, "2001:db8::1"},
	} {
		if got := platform.ClientIP(tc.r, tc.proxies); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func loginReq(e *testkit.Env, tenant, email, password string, headers ...string) testkit.Response {
	return e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": tenant, "email": email, "password": password}, headers...)
}

func attempts(t *testing.T, e *testkit.Env) (n int, keys []string) {
	t.Helper()
	c, err := pgx.Connect(context.Background(), e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	rows, err := c.Query(context.Background(), `select key from login_attempt`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	return len(keys), keys
}

func sql(t *testing.T, e *testkit.Env, q string, args ...any) {
	t.Helper()
	c, err := pgx.Connect(context.Background(), e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err := c.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestLoginLockoutAndUniformErrors(t *testing.T) {
	e := testkit.New(t)
	const email = "admin@acme.test"

	// Uniform answer: wrong password, unknown user and unknown tenant look the same.
	wrong := loginReq(e, "acme", email, "wrong-password-1")
	unknownUser := loginReq(e, "acme", "nobody@acme.test", "wrong-password-1")
	unknownTenant := loginReq(e, "nosuchtenant", email, "wrong-password-1")
	for name, r := range map[string]testkit.Response{"wrong password": wrong, "unknown user": unknownUser, "unknown tenant": unknownTenant} {
		problem(t, r, 401, "invalid credentials")
		if string(r.Body) != string(wrong.Body) || r.Header.Get("Retry-After") != "" {
			t.Errorf("%s answers differently: %s", name, r.Body)
		}
	}

	// 2 failures so far on this account; two more, then a success resets the counter.
	loginReq(e, "acme", email, "wrong-password-2")
	loginReq(e, "acme", email, "wrong-password-3")
	e.Must(http.StatusOK, loginReq(e, "acme", email, testkit.Password), nil)
	for i := 0; i < 4; i++ {
		problem(t, loginReq(e, "acme", email, "wrong-again"), 401, "invalid credentials")
	}
	e.Must(http.StatusOK, loginReq(e, "acme", email, testkit.Password), nil) // 4 failures are below the threshold

	// The fifth consecutive failure locks the account: even the right password is refused now.
	for i := 0; i < 5; i++ {
		problem(t, loginReq(e, "acme", email, "wrong-again"), 401, "invalid credentials")
	}
	r := loginReq(e, "acme", email, testkit.Password)
	problem(t, r, 429, "too many failed sign-in attempts")
	if ra := r.Header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q", ra)
	}
	// Another account in the same tenant is not locked (the address is far below its threshold).
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "other@acme.test", "roles": []string{"Ops"}, "password": "a-different-passphrase"}), nil)
	e.Must(http.StatusOK, loginReq(e, "acme", "other@acme.test", "a-different-passphrase"), nil)

	// The lockout answers the same whether or not the account exists.
	for i := 0; i < 5; i++ {
		loginReq(e, "acme", "ghost@acme.test", "wrong-again")
	}
	ghost := loginReq(e, "acme", "ghost@acme.test", "whatever-else")
	problem(t, ghost, 429, "too many failed sign-in attempts")
	if ghost.Header.Get("Retry-After") == "" {
		t.Fatal("unknown account must get Retry-After too")
	}
	for i := 0; i < 5; i++ {
		loginReq(e, "nosuchtenant", "x@y.test", "wrong-again")
	}
	problem(t, loginReq(e, "nosuchtenant", "x@y.test", "whatever-else"), 429, "too many failed sign-in attempts")

	// The alert for the tenant exists once, with no password and no e-mail address in it.
	var alerts struct {
		Items []struct {
			Kind, Severity, Title string
			Detail                map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts", nil), &alerts)
	var n, withUser int
	for _, a := range alerts.Items {
		if a.Kind == "auth.login_failures" {
			n++
			raw, _ := json.Marshal(a)
			if strings.Contains(string(raw), "wrong-again") || strings.Contains(string(raw), email) || strings.Contains(string(raw), "ghost") {
				t.Errorf("alert content: %s", raw)
			}
			if a.Detail["userId"] != nil {
				withUser++
			}
		}
	}
	// One alert per burst: the real account (referenced by user id) and the made-up one in the same tenant.
	if n != 2 || withUser != 1 {
		t.Fatalf("want 2 auth.login_failures alerts (1 with a user id), got %d/%d in %+v", n, withUser, alerts.Items)
	}
	var entries struct {
		Items []struct{ Action, Actor string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=auth.login_failures", nil), &entries)
	found := false
	for _, it := range entries.Items {
		found = found || it.Action == "auth.login_failures"
	}
	if !found {
		t.Fatalf("audit entry missing: %+v", entries.Items)
	}

	// The table stores keyed hashes only.
	count, keys := attempts(t, e)
	if count == 0 {
		t.Fatal("no counters stored")
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "a:") && !strings.HasPrefix(k, "i:") || strings.ContainsAny(k, "@.") || strings.Contains(k, "acme") {
			t.Errorf("key %q is not a keyed hash", k)
		}
	}

	// When the lockout has run out, the right password works and clears the counter.
	sql(t, e, `update login_attempt set locked_until = now() - interval '1 second'`)
	e.Must(http.StatusOK, loginReq(e, "acme", email, testkit.Password), nil)
}

func TestLoginBackoffDoubles(t *testing.T) {
	e := testkit.New(t)
	const email = "admin@acme.test"
	retry := func() int {
		r := loginReq(e, "acme", email, testkit.Password)
		if r.Status != 429 {
			t.Fatalf("status %d", r.Status)
		}
		var n int
		for _, c := range r.Header.Get("Retry-After") {
			n = n*10 + int(c-'0')
		}
		return n
	}
	for i := 0; i < 5; i++ {
		loginReq(e, "acme", email, "wrong-again")
	}
	first := retry()
	if first < 25 || first > 30 {
		t.Fatalf("first lockout %ds, want about 30", first)
	}
	// Lockout over, one more failure: the lockout doubles.
	sql(t, e, `update login_attempt set locked_until = now() - interval '1 second' where key like 'a:%'`)
	loginReq(e, "acme", email, "wrong-again")
	second := retry()
	if second < 55 || second > 60 {
		t.Fatalf("second lockout %ds, want about 60", second)
	}
	// And it is capped at 15 minutes.
	sql(t, e, `update login_attempt set failures = 40, locked_until = null where key like 'a:%'`)
	loginReq(e, "acme", email, "wrong-again")
	if capped := retry(); capped < 890 || capped > 900 {
		t.Fatalf("capped lockout %ds, want 900", capped)
	}
	// Failures older than the window do not count any more.
	sql(t, e, `update login_attempt set locked_until = null, last_failed_at = now() - interval '2 hours' where key like 'a:%'`)
	problem(t, loginReq(e, "acme", email, "wrong-again"), 401, "invalid credentials")
	sql(t, e, `update login_attempt set last_failed_at = now() - interval '2 days'`)
	loginReq(e, "acme", email, "wrong-again") // triggers the stale-row cleanup for other rows
}

func TestLoginAddressThrottle(t *testing.T) {
	e := testkit.New(t)
	// Thirty failures from one address, each against a different account, lock the address.
	for i := 0; i < 30; i++ {
		r := loginReq(e, "acme", "user"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@acme.test", "wrong-again")
		if r.Status != 401 {
			t.Fatalf("attempt %d: %d", i, r.Status)
		}
	}
	// Now even a legitimate login from that address waits.
	problem(t, loginReq(e, "acme", "admin@acme.test", testkit.Password), 429, "too many failed sign-in attempts")
}

func TestLoginAddressFromTrustedProxy(t *testing.T) {
	e := testkit.New(t)
	e.App.Platform.Cfg.TrustedProxies = 1
	for i := 0; i < 30; i++ {
		loginReq(e, "acme", "u"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@acme.test", "wrong-again", "X-Forwarded-For", "198.51.100.7")
	}
	// A different client behind the same proxy is not affected; the attacker's address is.
	e.Must(http.StatusOK, loginReq(e, "acme", "admin@acme.test", testkit.Password, "X-Forwarded-For", "198.51.100.8"), nil)
	problem(t, loginReq(e, "acme", "admin@acme.test", testkit.Password, "X-Forwarded-For", "198.51.100.7"), 429, "too many")
	// A forged left-most entry does not help the attacker.
	problem(t, loginReq(e, "acme", "admin@acme.test", testkit.Password, "X-Forwarded-For", "203.0.113.1, 198.51.100.7"), 429, "too many")
}

func TestLoginUnknownTenantAndSuspended(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	if _, err := e.App.Platform.Login(ctx, "acme", "admin@acme.test", testkit.Password); err != nil {
		t.Fatal(err)
	}
	// Direct callers (sign-up, erasure confirmation) have no client address: only the account is throttled.
	for i := 0; i < 5; i++ {
		_, _ = e.App.Platform.Login(ctx, "acme", "admin@acme.test", "wrong-again")
	}
	if _, err := e.App.Platform.Login(ctx, "acme", "admin@acme.test", testkit.Password); err == nil {
		t.Fatal("locked out account must be refused")
	} else if secs, ok := platform.RetryAfter(err); !ok || secs <= 0 {
		t.Fatalf("RetryAfter(%v) = %d, %v", err, secs, ok)
	}
	if _, ok := platform.RetryAfter(context.Canceled); ok {
		t.Fatal("not a lockout")
	}
}
