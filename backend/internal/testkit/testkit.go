// Package testkit spins up a real PostgreSQL database and the full Open Spoor app
// for integration tests. Set OPENSPOOR_TEST_ADMIN_URL and OPENSPOOR_TEST_DATABASE_URL
// (scripts/testdb.sh prints them, CI sets them via a Postgres service container).
package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/app"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/migrations"
)

// Password used for every test admin.
const Password = "correct-horse-battery"

// Env is a running test environment.
type Env struct {
	T      testing.TB
	App    *app.App
	Server *httptest.Server
	DBURL  string
	Token  string // admin token for the default tenant "acme"
	Tenant platform.Tenant
}

// New creates a fresh database, migrates it, starts the API and logs in as admin of tenant "acme".
func New(t testing.TB, opts ...func(*config.Config)) *Env {
	t.Helper()
	admin, appURL := os.Getenv("OPENSPOOR_TEST_ADMIN_URL"), os.Getenv("OPENSPOOR_TEST_DATABASE_URL")
	if admin == "" || appURL == "" {
		t.Skip("integration test: set OPENSPOOR_TEST_ADMIN_URL and OPENSPOOR_TEST_DATABASE_URL (see scripts/testdb.sh)")
	}
	ctx := context.Background()
	name := "openspoor_test_" + strings.ToLower(strings.ReplaceAll(ids.Secret(6), "-", "_"))
	name = strings.ReplaceAll(name, "_", "x")
	ac, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(appURL)
	if _, err := ac.Exec(ctx, fmt.Sprintf("create database %s owner %s", name, u.User.Username())); err != nil {
		t.Fatal(err)
	}
	_ = ac.Close(ctx)
	u.Path = "/" + name
	dbURL := u.String()
	if err := migrations.Up(ctx, dbURL); err != nil {
		t.Fatal("migrate: ", err)
	}
	// OPENSPOOR_TEST_MODE runs the whole suite in saas, single (default) or hub mode; CI runs all three.
	mode := config.Mode(os.Getenv("OPENSPOOR_TEST_MODE"))
	if mode == "" {
		mode = config.ModeSingle
	}
	cfg := config.Config{Mode: mode, DatabaseURL: dbURL, JWTSecret: []byte("test-secret-0123456789"), JWTTTL: time.Hour,
		DevAuth: true, PublicURL: "http://localhost", BaseDomain: "localhost", BillingDriver: "none", WorkerConcurrency: 2, MasterKey: []byte("test-master-key")}
	for _, o := range opts {
		o(&cfg)
	}
	a, err := app.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Router)
	e := &Env{T: t, App: a, Server: srv, DBURL: dbURL}
	t.Cleanup(func() {
		srv.Close()
		a.Close()
		if c, err := pgx.Connect(context.Background(), admin); err == nil {
			_, _ = c.Exec(context.Background(), "drop database if exists "+name+" with (force)")
			_ = c.Close(context.Background())
		}
	})
	e.Tenant, e.Token = e.NewTenant("acme", "admin@acme.test")
	return e
}

// NewTenant creates a tenant and returns an admin token for it.
func (e *Env) NewTenant(slug, email string) (platform.Tenant, string) {
	e.T.Helper()
	t, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: slug, Name: strings.ToUpper(slug), AdminEmail: email, AdminPassword: Password})
	if err != nil {
		e.T.Fatal(err)
	}
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": slug, "email": email, "password": Password}), &tok)
	return t, tok.Token
}

// Response is an HTTP result.
type Response struct {
	Status int
	Body   []byte
	Header http.Header
}

// JSON decodes the body.
func (r Response) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Call performs a request with the given bearer token. body may be []byte, string or any JSON value.
func (e *Env) Call(token, method, path string, body any, headers ...string) Response {
	e.T.Helper()
	var rd io.Reader
	ct := "application/json"
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		buf, _ := json.Marshal(b)
		rd = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, e.Server.URL+path, rd)
	req.Header.Set("Content-Type", ct)
	if token != "" {
		if strings.HasPrefix(token, "osk_") {
			req.Header.Set("X-API-Key", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return Response{Status: res.StatusCode, Body: b, Header: res.Header}
}

// Do calls as the default admin.
func (e *Env) Do(method, path string, body any, headers ...string) Response {
	e.T.Helper()
	return e.Call(e.Token, method, path, body, headers...)
}

// Must asserts the status and decodes into v (if non-nil).
func (e *Env) Must(status int, r Response, v any) {
	e.T.Helper()
	if r.Status != status {
		e.T.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Body)
	}
	if v != nil {
		if err := r.JSON(v); err != nil {
			e.T.Fatalf("decode: %v: %s", err, r.Body)
		}
	}
}

// Drain runs all queued background jobs.
func (e *Env) Drain() {
	e.T.Helper()
	if err := e.App.Deps.Jobs.Drain(context.Background()); err != nil {
		e.T.Fatal(err)
	}
}
