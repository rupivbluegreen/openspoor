package platform_test

import (
	"context"
	"encoding/base32"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/testkit"
)

type sentMail struct{ To, Subject, Body string }

type fakeMail struct {
	mu   sync.Mutex
	msgs []sentMail
}

func (f *fakeMail) Send(_ context.Context, to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, sentMail{to, subject, body})
	return nil
}

// waitFor returns the messages once n have arrived (mail goes out in the background).
func (f *fakeMail) waitFor(t *testing.T, n int) []sentMail {
	t.Helper()
	for i := 0; i < 200; i++ {
		f.mu.Lock()
		got := append([]sentMail(nil), f.msgs...)
		f.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d messages", n)
	return nil
}

func (f *fakeMail) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

func withMail(e *testkit.Env) *fakeMail {
	f := &fakeMail{}
	e.App.Platform.Mail = f
	return f
}

func queryRow(t *testing.T, e *testkit.Env, q string, dest ...any) {
	t.Helper()
	c, err := pgx.Connect(context.Background(), e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if err := c.QueryRow(context.Background(), q).Scan(dest...); err != nil {
		t.Fatal(err)
	}
}

func auditActions(t *testing.T, e *testkit.Env) string {
	t.Helper()
	var a auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=500", nil), &a)
	var names []string
	for _, it := range a.Items {
		names = append(names, it.Action)
	}
	return strings.Join(names, " ")
}

type tokenOut struct {
	Token         string
	Principal     auth.Principal
	RecoveryCodes []string
}

func signIn(t *testing.T, e *testkit.Env, email, password, mfaCode string) testkit.Response {
	t.Helper()
	return e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": email, "password": password, "mfaCode": mfaCode})
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// enrol runs the whole enrolment and returns the secret, a fresh token and the recovery codes.
func enrol(t *testing.T, e *testkit.Env, token string) ([]byte, tokenOut) {
	t.Helper()
	var st struct{ Secret, OtpauthUri string }
	e.Must(http.StatusCreated, e.Call(token, "POST", "/api/v1/auth/mfa/enrol", nil), &st)
	secret, err := b32.DecodeString(st.Secret)
	if err != nil || !strings.HasPrefix(st.OtpauthUri, "otpauth://totp/") {
		t.Fatalf("secret %q uri %q: %v", st.Secret, st.OtpauthUri, err)
	}
	var out tokenOut
	e.Must(http.StatusOK, e.Call(token, "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": auth.TOTPCode(secret, time.Now())}), &out)
	return secret, out
}

func TestPasswordChange(t *testing.T) {
	e := testkit.New(t)
	mails := withMail(e)
	tok := login(t, e, "viewer@acme.test", "Viewer") // a Viewer may change its own password; it is not tenant data
	change := func(token, cur, next string) testkit.Response {
		return e.Call(token, "POST", "/api/v1/auth/password", map[string]string{"currentPassword": cur, "newPassword": next})
	}
	problem(t, change(tok, "not-the-password", "a-new-passphrase-1"), 401, "invalid credentials")
	problem(t, change(tok, testkit.Password, "password123"), 400, "too common")
	problem(t, change(tok, testkit.Password, "viewer-is-the-name"), 400, "e-mail")
	problem(t, change(tok, testkit.Password, testkit.Password), 400, "must differ")
	problem(t, e.Call("", "POST", "/api/v1/auth/password", map[string]string{}), 401, "authentication required")

	var out tokenOut
	e.Must(http.StatusOK, change(tok, testkit.Password, "a-new-passphrase-1"), &out)
	if out.Token == "" || out.Token == tok {
		t.Fatal("a fresh token is issued")
	}
	// The old token and the old password are dead; the new ones work.
	problem(t, e.Call(tok, "GET", "/api/v1/me", nil), 401, "authentication required")
	e.Must(http.StatusOK, e.Call(out.Token, "GET", "/api/v1/me", nil), nil)
	problem(t, signIn(t, e, "viewer@acme.test", testkit.Password, ""), 401, "invalid credentials")
	e.Must(http.StatusOK, signIn(t, e, "viewer@acme.test", "a-new-passphrase-1", ""), nil)
	if m := mails.waitFor(t, 1); m[0].To != "viewer@acme.test" || strings.Contains(m[0].Body, "a-new-passphrase") {
		t.Fatalf("notification: %+v", m[0])
	}
	if got := auditActions(t, e); !strings.Contains(got, "user.password.change") {
		t.Fatalf("audit: %s", got)
	}
	var a auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=user.password.change", nil), &a)
	if a.Total != 1 || a.Items[0].After != nil || a.Items[0].Before != nil {
		t.Fatalf("the audit entry holds no credentials: %+v", a.Items)
	}
	// Repeated wrong current passwords are throttled like sign-ins.
	var last testkit.Response
	for i := 0; i < 6; i++ {
		last = change(out.Token, "wrong-current-pw", "another-passphrase-2")
	}
	problem(t, last, 429, "too many")

	// API keys and operator-bound tokens have no password.
	var key struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "roles": []string{"Admin"}}), &key)
	problem(t, e.Call(key.Secret, "POST", "/api/v1/auth/password", map[string]string{"currentPassword": "x", "newPassword": "y"}), 403, "signed-in user")
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9._-]+)`)

func TestPasswordRecovery(t *testing.T) {
	e := testkit.New(t)
	mails := withMail(e)
	login(t, e, "ops@acme.test", "Ops")
	forgot := func(tenant, email string) testkit.Response {
		return e.Call("", "POST", "/api/v1/auth/password/forgot", map[string]string{"tenant": tenant, "email": email})
	}
	reset := func(token, pw string) testkit.Response {
		return e.Call("", "POST", "/api/v1/auth/password/reset", map[string]string{"token": token, "newPassword": pw})
	}
	// Uniform answer whether or not the account, the tenant or the e-mail address exists.
	for _, c := range [][2]string{{"acme", "nobody@acme.test"}, {"no-such-tenant", "ops@acme.test"}, {"acme", "not-an-address"}} {
		r := forgot(c[0], c[1])
		e.Must(http.StatusAccepted, r, nil)
		if string(r.Body) != "{\"status\":\"accepted\"}\n" {
			t.Fatalf("body %q", r.Body)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if mails.count() != 0 {
		t.Fatal("mail went to an unknown account")
	}
	e.Must(http.StatusAccepted, forgot("acme", "OPS@acme.test"), nil)
	msgs := mails.waitFor(t, 1)
	if msgs[0].To != "ops@acme.test" || !strings.Contains(msgs[0].Body, "http://localhost/reset-password?token=") {
		t.Fatalf("mail: %+v", msgs[0])
	}
	token := tokenRe.FindStringSubmatch(msgs[0].Body)[1]
	var stored string
	queryRow(t, e, `select token_hash from password_reset`, &stored)
	if strings.Contains(stored, token) || len(stored) != 64 {
		t.Fatal("only a hash of the token is stored")
	}

	// A weak password does not use up the token; garbage and foreign tokens are refused.
	problem(t, reset(token, "password123"), 400, "too common")
	problem(t, reset(token+"x", "a-fresh-passphrase-9"), 400, "invalid or has expired")
	problem(t, reset("garbage", "a-fresh-passphrase-9"), 400, "invalid or has expired")
	problem(t, reset("00000000-0000-0000-0000-000000000000.abc", "a-fresh-passphrase-9"), 400, "invalid or has expired")
	oldTok := func() string {
		var tk tokenOut
		e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", testkit.Password, ""), &tk)
		return tk.Token
	}()
	r := reset(token, "a-fresh-passphrase-9")
	if r.Status != http.StatusNoContent {
		t.Fatalf("reset: %d %s", r.Status, r.Body)
	}
	problem(t, reset(token, "another-fresh-pass-8"), 400, "invalid or has expired") // single use
	problem(t, e.Call(oldTok, "GET", "/api/v1/me", nil), 401, "authentication required")
	problem(t, signIn(t, e, "ops@acme.test", testkit.Password, ""), 401, "invalid credentials")
	e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", "a-fresh-passphrase-9", ""), nil)
	mails.waitFor(t, 2)
	if got := auditActions(t, e); !strings.Contains(got, "user.password.reset_requested") || !strings.Contains(got, "user.password.reset") {
		t.Fatalf("audit: %s", got)
	}

	// An expired token is refused, and a new request replaces an earlier one.
	e.Must(http.StatusAccepted, forgot("acme", "ops@acme.test"), nil)
	first := tokenRe.FindStringSubmatch(mails.waitFor(t, 3)[2].Body)[1]
	sql(t, e, `update password_reset set expires_at = now() - interval '1 minute'`)
	problem(t, reset(first, "a-fresh-passphrase-7"), 400, "invalid or has expired")
	e.Must(http.StatusAccepted, forgot("acme", "ops@acme.test"), nil)
	second := tokenRe.FindStringSubmatch(mails.waitFor(t, 4)[3].Body)[1]
	var n int
	queryRow(t, e, `select count(*) from password_reset where used_at is null`, &n)
	if n != 1 || first == second {
		t.Fatalf("unused tokens: %d", n)
	}
	// Disabled accounts get nothing.
	var users struct{ Items []struct{ ID, Email string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/users", nil), &users)
	for _, u := range users.Items {
		if u.Email == "ops@acme.test" {
			e.Must(http.StatusOK, e.Do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"status": "disabled"}), nil)
		}
	}
	problem(t, reset(second, "a-fresh-passphrase-6"), 400, "invalid or has expired")
}

func TestPasswordRecoveryThrottleAndNoRelay(t *testing.T) {
	e := testkit.New(t)
	forgot := func(email string) testkit.Response {
		return e.Call("", "POST", "/api/v1/auth/password/forgot", map[string]string{"tenant": "acme", "email": email})
	}
	// Without an SMTP relay the answer is the same and nothing is stored.
	e.Must(http.StatusAccepted, forgot("someone@acme.test"), nil)
	var n int
	queryRow(t, e, `select count(*) from password_reset`, &n)
	if n != 0 {
		t.Fatal("no token without a relay")
	}
	mails := withMail(e)
	// Existing and non-existing accounts are throttled identically: three requests, then back-off.
	for _, email := range []string{"admin@acme.test", "ghost@acme.test"} {
		for i := 0; i < 3; i++ {
			e.Must(http.StatusAccepted, forgot(email), nil)
		}
		r := forgot(email)
		problem(t, r, 429, "too many")
		if r.Header.Get("Retry-After") == "" {
			t.Fatal("Retry-After missing")
		}
	}
	mails.waitFor(t, 3) // only the existing account's requests sent mail (the first one without a relay sent none)
	// Wrong tokens from one address are throttled too.
	var last testkit.Response
	for i := 0; i < 11; i++ {
		last = e.Call("", "POST", "/api/v1/auth/password/reset", map[string]string{"token": "bad", "newPassword": "a-fresh-passphrase-9"})
	}
	problem(t, last, 429, "too many")
}

func TestSessionRevocation(t *testing.T) {
	e := testkit.New(t)
	tok := login(t, e, "ops@acme.test", "Ops")
	e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/me", nil), nil)
	// Logout ends every session of the user; the admin's own are untouched.
	tok2 := func() string {
		var tk tokenOut
		e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", testkit.Password, ""), &tk)
		return tk.Token
	}()
	if r := e.Call(tok, "POST", "/api/v1/auth/logout", nil); r.Status != http.StatusNoContent {
		t.Fatalf("logout: %d %s", r.Status, r.Body)
	}
	problem(t, e.Call(tok, "GET", "/api/v1/me", nil), 401, "authentication required")
	problem(t, e.Call(tok2, "GET", "/api/v1/me", nil), 401, "authentication required")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/me", nil), nil)
	if got := auditActions(t, e); !strings.Contains(got, "auth.logout") {
		t.Fatalf("audit: %s", got)
	}

	// An administrator changing roles or status ends the user's sessions; other edits do not.
	var tk tokenOut
	e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", testkit.Password, ""), &tk)
	var users struct{ Items []struct{ ID, Email string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/users", nil), &users)
	var id string
	for _, u := range users.Items {
		if u.Email == "ops@acme.test" {
			id = u.ID
		}
	}
	e.Must(http.StatusOK, e.Do("PATCH", "/api/v1/users/"+id, map[string]any{"name": "Ops Person"}), nil)
	e.Must(http.StatusOK, e.Call(tk.Token, "GET", "/api/v1/me", nil), nil)
	e.Must(http.StatusOK, e.Do("PATCH", "/api/v1/users/"+id, map[string]any{"roles": []string{"Viewer"}}), nil)
	problem(t, e.Call(tk.Token, "GET", "/api/v1/me", nil), 401, "authentication required")
	e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", testkit.Password, ""), &tk)
	e.Must(http.StatusOK, e.Do("PATCH", "/api/v1/users/"+id, map[string]any{"status": "disabled"}), nil)
	problem(t, e.Call(tk.Token, "GET", "/api/v1/me", nil), 401, "authentication required")

	// Tokens issued before versions existed (no "ver" claim) stay valid at version 0 until they expire.
	var p auth.Principal
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/me", nil), &p)
	old, _, err := e.App.Deps.Auth.Issue(p)
	if err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusOK, e.Call(old, "GET", "/api/v1/me", nil), nil)
	// A token for a user that does not exist is refused.
	p.UserID = [16]byte{1}
	ghost, _, _ := e.App.Deps.Auth.Issue(p)
	problem(t, e.Call(ghost, "GET", "/api/v1/me", nil), 401, "authentication required")
}

func TestMFAEnrolmentLoginAndRecovery(t *testing.T) {
	e := testkit.New(t)
	mails := withMail(e)
	tok := login(t, e, "ops@acme.test", "Ops")
	status := func(token string) (s struct {
		Enabled, Pending, RequiredByPolicy bool
		RecoveryCodesRemaining             int
	}) {
		e.Must(http.StatusOK, e.Call(token, "GET", "/api/v1/auth/mfa", nil), &s)
		return
	}
	if s := status(tok); s.Enabled || s.Pending {
		t.Fatalf("%+v", s)
	}
	problem(t, e.Call(tok, "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": "123456"}), 400, "start enrolment")
	var st struct{ Secret string }
	e.Must(http.StatusCreated, e.Call(tok, "POST", "/api/v1/auth/mfa/enrol", nil), &st)
	if s := status(tok); s.Enabled || !s.Pending {
		t.Fatalf("pending: %+v", s)
	}
	secret, _ := b32.DecodeString(st.Secret)
	problem(t, e.Call(tok, "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": "000000"}), 400, "invalid verification code")
	var out tokenOut
	e.Must(http.StatusOK, e.Call(tok, "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": auth.TOTPCode(secret, time.Now())}), &out)
	if len(out.RecoveryCodes) != 10 || !out.Principal.MFA {
		t.Fatalf("%+v", out)
	}
	// Enabling revokes the earlier token; the new one works.
	problem(t, e.Call(tok, "GET", "/api/v1/me", nil), 401, "authentication required")
	e.Must(http.StatusOK, e.Call(out.Token, "GET", "/api/v1/me", nil), nil)
	if s := status(out.Token); !s.Enabled || s.RecoveryCodesRemaining != 10 {
		t.Fatalf("%+v", s)
	}
	problem(t, e.Call(out.Token, "POST", "/api/v1/auth/mfa/enrol", nil), 409, "already enabled")
	problem(t, e.Call(out.Token, "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": "000000"}), 409, "already enabled")

	// At rest: the secret is sealed and the recovery codes are hashed.
	var sealed []byte
	queryRow(t, e, `select mfa_secret from app_user where email='ops@acme.test'`, &sealed)
	if len(sealed) <= 20 || strings.Contains(string(sealed), string(secret)) {
		t.Fatal("secret is not sealed")
	}
	var hashes string
	queryRow(t, e, `select string_agg(code_hash, ' ') from user_recovery_code`, &hashes)
	for _, c := range out.RecoveryCodes {
		if strings.Contains(hashes, c) || strings.Contains(hashes, strings.ReplaceAll(c, "-", "")) {
			t.Fatal("recovery code stored in clear")
		}
	}

	// Sign-in now needs the code; the prompt appears only after the password was right.
	r := signIn(t, e, "ops@acme.test", testkit.Password, "")
	problem(t, r, 401, "verification code required")
	if !strings.Contains(string(r.Body), `"mfaRequired":true`) {
		t.Fatalf("no machine-readable marker: %s", r.Body)
	}
	problem(t, signIn(t, e, "ops@acme.test", "wrong-password-xx", ""), 401, "invalid credentials")
	problem(t, signIn(t, e, "ops@acme.test", testkit.Password, "000000"), 401, "invalid verification code")
	// The code used to confirm cannot be replayed; the next step's code is accepted once.
	problem(t, signIn(t, e, "ops@acme.test", testkit.Password, auth.TOTPCode(secret, time.Now())), 401, "invalid verification code")
	next := auth.TOTPCode(secret, time.Now().Add(auth.TOTPPeriod))
	var li tokenOut
	e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", testkit.Password, next), &li)
	if !li.Principal.MFA {
		t.Fatal("session should record the second factor")
	}
	problem(t, signIn(t, e, "ops@acme.test", testkit.Password, next), 401, "invalid verification code")
	// Recovery codes work once, in any case and with or without dashes.
	rc := out.RecoveryCodes[0]
	e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", testkit.Password, strings.ToUpper(strings.ReplaceAll(rc, "-", " "))), nil)
	problem(t, signIn(t, e, "ops@acme.test", testkit.Password, rc), 401, "invalid verification code")
	if s := status(li.Token); s.RecoveryCodesRemaining != 9 {
		t.Fatalf("%+v", s)
	}

	// Disabling and regenerating need the password and a code.
	body := func(pw, code string) map[string]string { return map[string]string{"password": pw, "code": code} }
	problem(t, e.Call(li.Token, "POST", "/api/v1/auth/mfa/disable", body("wrong-password-xx", out.RecoveryCodes[1])), 401, "invalid credentials")
	problem(t, e.Call(li.Token, "POST", "/api/v1/auth/mfa/disable", body(testkit.Password, "000000")), 401, "invalid verification code")
	var regen tokenOut
	e.Must(http.StatusOK, e.Call(li.Token, "POST", "/api/v1/auth/mfa/recovery-codes", body(testkit.Password, out.RecoveryCodes[1])), &regen)
	if len(regen.RecoveryCodes) != 10 || regen.RecoveryCodes[0] == out.RecoveryCodes[0] {
		t.Fatal("new codes")
	}
	problem(t, signIn(t, e, "ops@acme.test", testkit.Password, out.RecoveryCodes[2]), 401, "invalid verification code") // old set is void
	var dis tokenOut
	e.Must(http.StatusOK, e.Call(li.Token, "POST", "/api/v1/auth/mfa/disable", body(testkit.Password, regen.RecoveryCodes[0])), &dis)
	problem(t, e.Call(li.Token, "GET", "/api/v1/me", nil), 401, "authentication required")
	e.Must(http.StatusOK, signIn(t, e, "ops@acme.test", testkit.Password, ""), nil)
	problem(t, e.Call(dis.Token, "POST", "/api/v1/auth/mfa/disable", body(testkit.Password, "000000")), 409, "no second factor")
	var n int
	queryRow(t, e, `select count(*) from user_recovery_code`, &n)
	if n != 0 {
		t.Fatalf("%d recovery codes left", n)
	}
	mails.waitFor(t, 1) // disabling is notified
	got := auditActions(t, e)
	for _, want := range []string{"mfa.enrol", "mfa.enable", "mfa.recovery_code.use", "mfa.recovery_codes.regenerate", "mfa.disable"} {
		if !strings.Contains(got, want) {
			t.Errorf("audit lacks %s: %s", want, got)
		}
	}
	var a auditPage
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=mfa.", nil), &a)
	for _, it := range a.Items {
		if strings.Contains(fmt.Sprint(it.After, it.Before), st.Secret) {
			t.Fatal("audit holds the secret")
		}
	}
	// Service credentials have no second factor.
	var key struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "roles": []string{"Admin"}}), &key)
	problem(t, e.Call(key.Secret, "POST", "/api/v1/auth/mfa/enrol", nil), 403, "signed-in user")
	problem(t, e.Call(key.Secret, "GET", "/api/v1/auth/mfa", nil), 403, "signed-in user")
}

func TestMFALockoutOnWrongCodes(t *testing.T) {
	e := testkit.New(t)
	tok := login(t, e, "ops@acme.test", "Ops")
	_, out := enrol(t, e, tok)
	_ = out
	// A correct password does not reset the counter while codes are wrong: guessing is locked out.
	var last testkit.Response
	for i := 0; i < 6; i++ {
		last = signIn(t, e, "ops@acme.test", testkit.Password, "000000")
	}
	problem(t, last, 429, "too many")
	if last.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After")
	}
}

func TestMFAPolicyForAdministrators(t *testing.T) {
	e := testkit.New(t)
	put := func(token string, on bool) testkit.Response {
		return e.Call(token, "PUT", "/api/v1/tenant/security", map[string]bool{"requireMfaForAdmin": on})
	}
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	var pol struct{ RequireMfaForAdmin bool }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/tenant/security", nil), &pol)
	problem(t, put(viewer, true), 403, "missing role")
	// An administrator cannot lock the tenant out by requiring what they do not have themselves.
	problem(t, put(e.Token, true), 409, "own account")

	second := login(t, e, "second@acme.test", "Admin") // a second administrator without a factor
	ops := login(t, e, "ops@acme.test", "Ops")
	secret, out := enrol(t, e, e.Token)
	adminTok := out.Token
	e.Token = adminTok
	e.Must(http.StatusOK, put(adminTok, true), &pol)
	if !pol.RequireMfaForAdmin {
		t.Fatal("policy not set")
	}
	// The other administrator's session ended; their next sign-in is limited to enrolment.
	problem(t, e.Call(second, "GET", "/api/v1/me", nil), 401, "authentication required")
	var si tokenOut
	e.Must(http.StatusOK, signIn(t, e, "second@acme.test", testkit.Password, ""), &si)
	if !si.Principal.MFASetupRequired {
		t.Fatalf("principal %+v", si.Principal)
	}
	problem(t, e.Call(si.Token, "GET", "/api/v1/orgs", nil), 403, "enrolment required")
	problem(t, e.Call(si.Token, "GET", "/api/v1/audit", nil), 403, "enrolment required")
	e.Must(http.StatusOK, e.Call(si.Token, "GET", "/api/v1/me", nil), nil)
	var ms struct{ RequiredByPolicy bool }
	e.Must(http.StatusOK, e.Call(si.Token, "GET", "/api/v1/auth/mfa", nil), &ms)
	if !ms.RequiredByPolicy {
		t.Fatal("status should say the policy applies")
	}
	_, done := enrol(t, e, si.Token)
	if done.Principal.MFASetupRequired {
		t.Fatal("enrolment lifts the limit")
	}
	e.Must(http.StatusOK, e.Call(done.Token, "GET", "/api/v1/orgs", nil), nil)
	// While the policy applies an administrator cannot turn their factor off.
	problem(t, e.Call(done.Token, "POST", "/api/v1/auth/mfa/disable", map[string]string{"password": testkit.Password, "code": done.RecoveryCodes[0]}), 403, "requires administrators")
	// Non-administrators are not affected.
	e.Must(http.StatusOK, e.Call(ops, "GET", "/api/v1/me", nil), nil)

	// An administrator can remove another user's factor (lost device); the user's sessions end.
	var users struct{ Items []struct{ ID, Email string } }
	e.Must(http.StatusOK, e.Call(adminTok, "GET", "/api/v1/users", nil), &users)
	var secondID, opsID string
	for _, u := range users.Items {
		switch u.Email {
		case "second@acme.test":
			secondID = u.ID
		case "ops@acme.test":
			opsID = u.ID
		}
	}
	problem(t, e.Call(adminTok, "POST", "/api/v1/users/"+opsID+"/mfa/reset", nil), 409, "no second factor")
	problem(t, e.Call(ops, "POST", "/api/v1/users/"+secondID+"/mfa/reset", nil), 403, "missing role")
	r := e.Call(adminTok, "POST", "/api/v1/users/"+secondID+"/mfa/reset", map[string]string{"reason": "lost phone"})
	if r.Status != http.StatusNoContent {
		t.Fatalf("reset: %d %s", r.Status, r.Body)
	}
	problem(t, e.Call(done.Token, "GET", "/api/v1/me", nil), 401, "authentication required")
	e.Must(http.StatusOK, signIn(t, e, "second@acme.test", testkit.Password, ""), &si)
	if !si.Principal.MFASetupRequired {
		t.Fatal("after a reset the policy asks for enrolment again")
	}
	// Switching the policy off.
	e.Must(http.StatusOK, put(adminTok, false), &pol)
	if pol.RequireMfaForAdmin {
		t.Fatal("policy not cleared")
	}
	si = tokenOut{}
	e.Must(http.StatusOK, signIn(t, e, "second@acme.test", testkit.Password, ""), &si)
	if si.Principal.MFASetupRequired {
		t.Fatal("limit lifted with the policy")
	}
	got := auditActions(t, e)
	for _, want := range []string{"tenant.security_policy.update", "mfa.reset"} {
		if !strings.Contains(got, want) {
			t.Errorf("audit lacks %s", want)
		}
	}
	_ = secret
}

func TestTenantSwitchHonoursSecondFactor(t *testing.T) {
	e := testkit.New(t)
	beta, betaTok := e.NewTenant("beta", "admin@acme.test") // the same person is an administrator in two tenants
	_, betaOut := enrol(t, e, betaTok)
	_ = betaOut
	switchTo := func(token string) testkit.Response {
		return e.Call(token, "POST", "/api/v1/auth/switch", map[string]string{"tenantId": beta.ID.String()})
	}
	problem(t, switchTo(e.Token), 403, "second factor")
	// A session that passed a second factor may switch.
	secret, out := enrol(t, e, e.Token)
	_ = secret
	e.Must(http.StatusOK, switchTo(out.Token), nil)
}

func TestSecurityPolicyTurnOnRequiresHumanAdmin(t *testing.T) {
	e := testkit.New(t)
	var key struct{ Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "roles": []string{"Admin"}}), &key)
	problem(t, e.Call(key.Secret, "PUT", "/api/v1/tenant/security", map[string]bool{"requireMfaForAdmin": true}), 409, "own account")
	e.Must(http.StatusOK, e.Call(key.Secret, "PUT", "/api/v1/tenant/security", map[string]bool{"requireMfaForAdmin": false}), nil)
}

func TestNoCacheAndNoCookies(t *testing.T) {
	e := testkit.New(t)
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/me"}, {"GET", "/api/v1/orgs"}, {"GET", "/api/v1/audit"}, {"POST", "/api/v1/auth/logout"}} {
		r := e.Do(c.method, c.path, nil)
		if cc := r.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s %s: Cache-Control %q", c.method, c.path, cc)
		}
		if r.Header.Get("Set-Cookie") != "" || r.Header.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s %s sets a cookie or allows credentialed cross-site requests", c.method, c.path)
		}
	}
	// Errors, sign-in and recovery responses carry it too.
	if r := e.Call("", "GET", "/api/v1/me", nil); r.Status != 401 || r.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("401: %d %q", r.Status, r.Header.Get("Cache-Control"))
	}
	for _, p := range []string{"/api/v1/auth/login", "/api/v1/auth/password/forgot"} {
		r := e.Call("", "POST", p, map[string]string{"tenant": "acme", "email": "admin@acme.test", "password": testkit.Password})
		if r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("Set-Cookie") != "" {
			t.Errorf("%s: %q %q", p, r.Header.Get("Cache-Control"), r.Header.Get("Set-Cookie"))
		}
	}
	// Public endpoints that are not about accounts stay cacheable by their own rules (no forced header).
	if r := e.Call("", "GET", "/healthz", nil); r.Header.Get("Cache-Control") == "no-store" {
		t.Error("health check should not be affected")
	}
}

// TestCookieIsNotACredential documents and enforces that the API is authenticated by headers only: no
// route issues a cookie and a cookie is never read as a credential, so cross-site requests carry nothing
// a browser would add on its own (there is no CSRF surface to protect).
func TestCookieIsNotACredential(t *testing.T) {
	e := testkit.New(t)
	for _, name := range []string{"openspoor_token", "token", "session"} {
		req, _ := http.NewRequest("GET", e.Server.URL+"/api/v1/me", nil)
		req.AddCookie(&http.Cookie{Name: name, Value: e.Token})
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("cookie %q authenticated the request: %d", name, res.StatusCode)
		}
	}
	// A state-changing request with only a cookie is refused too.
	req, _ := http.NewRequest("POST", e.Server.URL+"/api/v1/orgs", strings.NewReader(`{"name":"x","kind":"manufacturer"}`))
	req.AddCookie(&http.Cookie{Name: "openspoor_token", Value: e.Token})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST with cookie: %d", res.StatusCode)
	}
}
