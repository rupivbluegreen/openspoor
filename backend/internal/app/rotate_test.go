package app_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/app"
	"github.com/openspoor/openspoor/internal/core/secrets"
	"github.com/openspoor/openspoor/internal/testkit"
)

func fetch(t *testing.T, url, q string, dest ...any) {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if err := c.QueryRow(context.Background(), q).Scan(dest...); err != nil {
		t.Fatal(err)
	}
}

func TestRotateKeys(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	// Material under the master key: the acme data key (created on first use), a second tenant, the node key.
	e.NewTenant("beta", "admin@beta.test")
	var sub struct{ ID, Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": "http://127.0.0.1:1/x", "secret": "hook-secret"}), &sub)
	var did1 map[string]any
	e.Must(http.StatusOK, e.Call("", "GET", "/.well-known/did.json", nil), &did1)
	var wrapped []byte
	fetch(t, e.SuperURL, `select wrapped_dek from tenant_key where tenant_id='`+e.Tenant.ID.String()+`'`, &wrapped)

	if _, err := e.App.RotateKeys(ctx, nil, false); err == nil {
		t.Fatal("an empty new key must be refused")
	}
	if _, err := e.App.RotateKeys(ctx, []byte("test-master-key"), false); err == nil || !strings.Contains(err.Error(), "equals") {
		t.Fatalf("the same key: %v", err)
	}

	newKey := []byte("a-brand-new-master-key-0123456789")
	dry, err := e.App.RotateKeys(ctx, newKey, true)
	if err != nil || !dry.DryRun || dry.TenantKeys < 1 || dry.NodeKeys != 1 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	var after []byte
	fetch(t, e.SuperURL, `select wrapped_dek from tenant_key where tenant_id='`+e.Tenant.ID.String()+`'`, &after)
	if !bytes.Equal(wrapped, after) {
		t.Fatal("a dry run must change nothing")
	}

	rep, err := e.App.RotateKeys(ctx, newKey, false)
	if err != nil || rep.DryRun || rep.TenantKeys != dry.TenantKeys || rep.NodeKeys != 1 {
		t.Fatalf("rotate: %+v %v", rep, err)
	}
	fetch(t, e.SuperURL, `select wrapped_dek from tenant_key where tenant_id='`+e.Tenant.ID.String()+`'`, &after)
	if bytes.Equal(wrapped, after) {
		t.Fatal("the data key was not re-wrapped")
	}

	// The old key no longer opens the wrapped keys; the new one does, and data sealed under the unchanged data keys still opens.
	oldS, _ := secrets.New([]byte("test-master-key"))
	newS, _ := secrets.New(newKey)
	aad := []byte("dek:" + e.Tenant.ID.String())
	if _, err := oldS.Open(after, aad); err == nil {
		t.Fatal("old master key still opens the data key")
	}
	if _, err := newS.Open(after, aad); err != nil {
		t.Fatal(err)
	}

	// A second process started with the new key reads everything: secrets, node key, sign-in.
	cfg := e.App.Deps.Cfg
	cfg.MasterKey = newKey
	a2, err := app.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	srv := httptest.NewServer(a2.Router)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/.well-known/did.json")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), did1["id"].(string)) {
		t.Fatalf("node identity after rotation: %d %s", res.StatusCode, b)
	}
	sealed, err := a2.Deps.Keys.Seal(ctx, e.Tenant.ID, []byte("x"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a2.Deps.Keys.Open(ctx, e.Tenant.ID, sealed, []byte("a")); err != nil || string(got) != "x" {
		t.Fatalf("%q %v", got, err)
	}
	var secretCipher []byte
	fetch(t, e.SuperURL, `select secret_sealed from epcis_subscription where id='`+sub.ID+`'`, &secretCipher)
	if got, err := a2.Deps.Keys.Open(ctx, e.Tenant.ID, secretCipher, []byte("epcis-subscription:"+sub.ID)); err != nil || string(got) != "hook-secret" {
		t.Fatalf("webhook secret after rotation: %q %v", got, err)
	}

	// Rotating again with the wrong current key fails and changes nothing.
	if _, err := a2.RotateKeys(ctx, []byte("yet-another-master-key-0123456789"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.App.RotateKeys(ctx, []byte("a-third-master-key-0123456789abc"), false); err == nil || !strings.Contains(err.Error(), "cannot open") {
		t.Fatalf("stale current key: %v", err)
	}
}
