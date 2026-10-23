package secretsrc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type fakeVault struct {
	*httptest.Server
	reads, logins atomic.Int32
}

// newVault serves KV v2 under "secret" and the Kubernetes login method.
func newVault(t *testing.T) *fakeVault {
	t.Helper()
	fv := &fakeVault{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/kubernetes/login", func(w http.ResponseWriter, r *http.Request) {
		fv.logins.Add(1)
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["role"] != "openspoor" || in["jwt"] != "sa-jwt" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"auth":{"client_token":"login-token"}}`))
	})
	mux.HandleFunc("/v1/secret/data/openspoor/app", func(w http.ResponseWriter, r *http.Request) {
		fv.reads.Add(1)
		if tok := r.Header.Get("X-Vault-Token"); tok != "static-token" && tok != "login-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("X-Vault-Namespace") != "" && r.Header.Get("X-Vault-Namespace") != "team" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"master":"m-secret","blank":"  ","num":42,"obj":{"a":1}},"metadata":{"version":3}}}`))
	})
	mux.HandleFunc("/v1/secret/data/openspoor/bad", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"nope":1}`)) })
	mux.HandleFunc("/v1/secret/data/openspoor/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
	})
	fv.Server = httptest.NewServer(mux)
	t.Cleanup(fv.Close)
	return fv
}

func TestResolvePlainAndEmpty(t *testing.T) {
	r := New(Options{})
	ctx := context.Background()
	for in, want := range map[string]string{"": "", "plain": "plain", "postgres://u:p@h/db": "postgres://u:p@h/db"} {
		got, err := r.Resolve(ctx, in)
		if err != nil || got != want {
			t.Fatalf("Resolve(%q) = %q, %v", in, got, err)
		}
	}
	if IsRef("plain") || !IsRef("file:/x") || !IsRef("vault:a/b#c") {
		t.Fatal("IsRef")
	}
}

func TestResolveFile(t *testing.T) {
	r := New(Options{})
	ctx := context.Background()
	got, err := r.Resolve(ctx, "file:"+writeFile(t, "  s3cr3t-value\n"))
	if err != nil || got != "s3cr3t-value" {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, ref := range map[string]string{
		"missing": "file:/nonexistent/secret",
		"empty":   "file:" + writeFile(t, " \n"),
		"no path": "file:",
		"huge":    "file:" + writeFile(t, strings.Repeat("a", maxSecretFile+1)),
	} {
		if _, err := r.Resolve(ctx, ref); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestResolveVaultStaticToken(t *testing.T) {
	fv := newVault(t)
	r := New(Options{VaultAddr: fv.URL, VaultToken: "static-token", VaultNamespace: "team"})
	ctx := context.Background()
	got, err := r.Resolve(ctx, "vault:secret/openspoor/app#master")
	if err != nil || got != "m-secret" {
		t.Fatalf("got %q, %v", got, err)
	}
	if n, err := r.Resolve(ctx, "vault:secret/openspoor/app#num"); err != nil || n != "42" {
		t.Fatalf("number field: %q, %v", n, err)
	}
	if fv.reads.Load() != 1 || fv.logins.Load() != 0 {
		t.Fatalf("one read, no login expected, got reads=%d logins=%d", fv.reads.Load(), fv.logins.Load())
	}
}

func TestResolveVaultKubernetesLogin(t *testing.T) {
	fv := newVault(t)
	tok := writeFile(t, "sa-jwt\n")
	r := New(Options{VaultAddr: fv.URL, VaultRole: "openspoor", K8sTokenFile: tok})
	ctx := context.Background()
	got, err := r.Resolve(ctx, "vault:secret/openspoor/app#master")
	if err != nil || got != "m-secret" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := r.Resolve(ctx, "vault:secret/openspoor/app#num"); err != nil {
		t.Fatal(err)
	}
	if fv.logins.Load() != 1 {
		t.Fatalf("token must be cached, logins=%d", fv.logins.Load())
	}
	// A different secret path reuses the login token.
	r2 := New(Options{VaultAddr: fv.URL, VaultRole: "wrong", K8sTokenFile: tok})
	if _, err := r2.Resolve(ctx, "vault:secret/openspoor/app#master"); err == nil || !strings.Contains(err.Error(), "login answered HTTP 403") {
		t.Fatalf("wrong role must fail: %v", err)
	}
	r3 := New(Options{VaultAddr: fv.URL, VaultRole: "openspoor", K8sTokenFile: "/nonexistent/token"})
	if _, err := r3.Resolve(ctx, "vault:secret/openspoor/app#master"); err == nil {
		t.Fatal("missing service-account token must fail")
	}
}

func TestResolveVaultFailsClosed(t *testing.T) {
	fv := newVault(t)
	ctx := context.Background()
	ok := Options{VaultAddr: fv.URL, VaultToken: "static-token"}
	cases := map[string]struct {
		opt Options
		ref string
	}{
		"missing field":     {ok, "vault:secret/openspoor/app#absent"},
		"blank field":       {ok, "vault:secret/openspoor/app#blank"},
		"non-string field":  {ok, "vault:secret/openspoor/app#obj"},
		"unknown path":      {ok, "vault:secret/openspoor/other#x"},
		"bad shape":         {ok, "vault:secret/openspoor/bad#x"},
		"redirect":          {ok, "vault:secret/openspoor/redirect#x"},
		"wrong token":       {Options{VaultAddr: fv.URL, VaultToken: "nope"}, "vault:secret/openspoor/app#master"},
		"no auth":           {Options{VaultAddr: fv.URL}, "vault:secret/openspoor/app#master"},
		"no addr":           {Options{VaultToken: "t"}, "vault:secret/openspoor/app#master"},
		"bad addr":          {Options{VaultAddr: "://x", VaultToken: "t"}, "vault:secret/openspoor/app#master"},
		"clear text remote": {Options{VaultAddr: "http://vault.example.com:8200", VaultToken: "t"}, "vault:secret/openspoor/app#master"},
		"no field":          {ok, "vault:secret/openspoor/app"},
		"no mount":          {ok, "vault:app#master"},
		"traversal":         {ok, "vault:secret/../sys/x#master"},
		"unreachable":       {Options{VaultAddr: "http://127.0.0.1:1", VaultToken: "t"}, "vault:secret/openspoor/app#master"},
		"bad ca file":       {Options{VaultAddr: "https://127.0.0.1:1", VaultToken: "t", VaultCACert: "/nonexistent.pem"}, "vault:secret/openspoor/app#master"},
		"ca not pem":        {Options{VaultAddr: "https://127.0.0.1:1", VaultToken: "t", VaultCACert: writeFile(t, "not pem")}, "vault:secret/openspoor/app#master"},
	}
	for name, c := range cases {
		got, err := New(c.opt).Resolve(ctx, c.ref)
		if err == nil || got != "" {
			t.Errorf("%s: expected an error and no value, got %q, %v", name, got, err)
			continue
		}
		for _, leak := range []string{"static-token", "m-secret", "login-token", "sa-jwt"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: error leaks a secret: %v", name, err)
			}
		}
	}
}

func TestOptionsFromEnv(t *testing.T) {
	t.Setenv("OPENSPOOR_VAULT_ADDR", "https://v")
	t.Setenv("OPENSPOOR_VAULT_TOKEN", "t")
	t.Setenv("OPENSPOOR_VAULT_ROLE", "r")
	t.Setenv("OPENSPOOR_VAULT_AUTH_MOUNT", "k8s")
	t.Setenv("OPENSPOOR_VAULT_NAMESPACE", "ns")
	t.Setenv("OPENSPOOR_VAULT_CACERT", "/ca")
	t.Setenv("OPENSPOOR_VAULT_K8S_TOKEN_FILE", "/tok")
	o := OptionsFromEnv()
	if o.VaultAddr != "https://v" || o.VaultToken != "t" || o.VaultRole != "r" || o.VaultAuthMount != "k8s" ||
		o.VaultNamespace != "ns" || o.VaultCACert != "/ca" || o.K8sTokenFile != "/tok" {
		t.Fatalf("%+v", o)
	}
}

func TestCustomAuthMountAndBasePath(t *testing.T) {
	var gotLogin, gotRead string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			gotLogin = r.URL.Path
			_, _ = w.Write([]byte(`{"auth":{"client_token":"t"}}`))
			return
		}
		gotRead = r.URL.Path
		_, _ = w.Write([]byte(`{"data":{"data":{"f":"v"}}}`))
	}))
	defer srv.Close()
	r := New(Options{VaultAddr: srv.URL + "/prefix/", VaultRole: "x", VaultAuthMount: "/k8s-prod/", K8sTokenFile: writeFile(t, "jwt")})
	if v, err := r.Resolve(context.Background(), "vault:kv/a/b#f"); err != nil || v != "v" {
		t.Fatal(v, err)
	}
	if gotLogin != "/prefix/v1/auth/k8s-prod/login" || gotRead != "/prefix/v1/kv/data/a/b" {
		t.Fatalf("login %q read %q", gotLogin, gotRead)
	}
}
