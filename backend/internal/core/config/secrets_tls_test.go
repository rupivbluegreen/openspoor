package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tmpFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	for _, k := range []string{"OPENSPOOR_DATABASE_PASSWORD", "OPENSPOOR_MIGRATE_DATABASE_URL", "OPENSPOOR_LAGO_API_KEY", "ANTHROPIC_API_KEY",
		"OPENSPOOR_SMTP_URL", "OPENSPOOR_TRUSTED_PROXIES", "OPENSPOOR_TLS_CERT", "OPENSPOOR_TLS_KEY", "OPENSPOOR_TLS_CLIENT_CA",
		"OPENSPOOR_TLS_MIN_VERSION", "OPENSPOOR_TLS_CLIENT_CERT", "OPENSPOOR_TLS_CLIENT_KEY", "OPENSPOOR_TLS_PEER_CA",
		"OPENSPOOR_TLS_REDIRECT_ADDR", "OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT", "OPENSPOOR_VAULT_ADDR", "OPENSPOOR_VAULT_TOKEN", "OPENSPOOR_VAULT_ROLE"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENSPOOR_JWT_SECRET", "0123456789abcdef")
	t.Setenv("OPENSPOOR_MASTER_KEY", "mk")
}

func TestSecretsFromFilesAndVault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" || r.URL.Path != "/v1/secret/data/openspoor" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"master":"vault-master-key","lago":"lago-1","dburl":"postgres://app@db:5432/os","pw":"p@ss/w:rd"}}}`))
	}))
	defer srv.Close()
	baseEnv(t)
	t.Setenv("OPENSPOOR_VAULT_ADDR", srv.URL)
	t.Setenv("OPENSPOOR_VAULT_TOKEN", "tok")
	t.Setenv("OPENSPOOR_MASTER_KEY", "vault:secret/openspoor#master")
	t.Setenv("OPENSPOOR_JWT_SECRET", "file:"+tmpFile(t, "jwt", "jwt-secret-from-file-0123\n"))
	t.Setenv("OPENSPOOR_LAGO_API_KEY", "vault:secret/openspoor#lago")
	t.Setenv("OPENSPOOR_DATABASE_URL", "vault:secret/openspoor#dburl")
	t.Setenv("OPENSPOOR_DATABASE_PASSWORD", "vault:secret/openspoor#pw")
	t.Setenv("OPENSPOOR_MIGRATE_DATABASE_URL", "file:"+tmpFile(t, "mig", "postgres://owner:x@db/os"))
	t.Setenv("ANTHROPIC_API_KEY", "file:"+tmpFile(t, "a", "model-key"))
	t.Setenv("OPENSPOOR_SMTP_URL", "smtp://u:p@h:587")
	t.Setenv("OPENSPOOR_S3_SECRET_KEY", "file:"+tmpFile(t, "s3", "s3-secret"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(c.MasterKey) != "vault-master-key" || string(c.JWTSecret) != "jwt-secret-from-file-0123" || c.LagoAPIKey != "lago-1" ||
		c.AnthropicKey != "model-key" || c.SMTPURL != "smtp://u:p@h:587" || c.S3SecretKey != "s3-secret" || c.MigrateDatabaseURL != "postgres://owner:x@db/os" {
		t.Fatalf("resolved: %+v", c)
	}
	if c.DatabaseURL != "postgres://app:p%40ss%2Fw%3Ard@db:5432/os" {
		t.Fatalf("database url: %q", c.DatabaseURL)
	}
}

func TestSecretResolutionFailsClosed(t *testing.T) {
	for _, name := range []string{"OPENSPOOR_MASTER_KEY", "OPENSPOOR_JWT_SECRET", "OPENSPOOR_DATABASE_URL", "OPENSPOOR_LAGO_API_KEY",
		"ANTHROPIC_API_KEY", "OPENSPOOR_SMTP_URL", "OPENSPOOR_S3_SECRET_KEY", "OPENSPOOR_MIGRATE_DATABASE_URL", "OPENSPOOR_DATABASE_PASSWORD"} {
		t.Run(name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("OPENSPOOR_DATABASE_URL", "postgres://u@h/db")
			t.Setenv(name, "file:/nonexistent/secret")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected an error naming %s, got %v", name, err)
			}
		})
	}
	t.Run("vault without address", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("OPENSPOOR_MASTER_KEY", "vault:secret/x#y")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENSPOOR_VAULT_ADDR") {
			t.Fatal(err)
		}
	})
	t.Run("password into a URL without host", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("OPENSPOOR_DATABASE_URL", "nonsense")
		t.Setenv("OPENSPOOR_DATABASE_PASSWORD", "pw")
		if _, err := Load(); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestPasswordWithoutUser(t *testing.T) {
	got, err := withPassword("postgres://db:5432/x", "pw")
	if err != nil || !strings.Contains(got, ":pw@db") {
		t.Fatal(got, err)
	}
}

// --- TLS ---

type pki struct {
	caPEM, certPEM, keyPEM []byte
}

func newPKI(t *testing.T, host string) pki {
	t.Helper()
	ca, caKey := mkCert(t, "test ca", nil, nil, true, "")
	leaf, leafKey := mkCert(t, host, ca, caKey, false, host)
	enc := func(c *x509.Certificate) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	kb, _ := x509.MarshalECPrivateKey(leafKey)
	return pki{caPEM: enc(ca), certPEM: enc(leaf), keyPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})}
}

func mkCert(t *testing.T, cn string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool, host string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true, IsCA: isCA,
	}
	if isCA {
		tpl.KeyUsage |= x509.KeyUsageCertSign
	}
	if host != "" {
		tpl.DNSNames = []string{host}
		tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	if parent == nil {
		parent, parentKey = tpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func tlsEnv(t *testing.T, p pki) {
	t.Helper()
	baseEnv(t)
	t.Setenv("OPENSPOOR_TLS_CERT", tmpFile(t, "c.pem", string(p.certPEM)))
	t.Setenv("OPENSPOOR_TLS_KEY", tmpFile(t, "k.pem", string(p.keyPEM)))
}

func TestTLSDefaultsOff(t *testing.T) {
	baseEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.TLS.Enabled() || c.TLS.RequireFederationClientCert || c.TLS.MinVersion != tls.VersionTLS13 {
		t.Fatalf("%+v", c.TLS)
	}
	if tc, err := c.ServerTLS(); tc != nil || err != nil {
		t.Fatal(tc, err)
	}
}

func TestServerTLSDefaultsTo13(t *testing.T) {
	p := newPKI(t, "localhost")
	tlsEnv(t, p)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.TLS.RequireFederationClientCert {
		t.Fatal("federation client certificate must be required by default when the API serves TLS")
	}
	tc, err := c.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	if tc.MinVersion != tls.VersionTLS13 || tc.ClientAuth != tls.RequestClientCert || tc.ClientCAs != nil {
		t.Fatalf("%+v", tc)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(config(r)))
	}))
	srv.TLS = tc
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.caPEM)
	get := func(max uint16) (string, error) {
		cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MaxVersion: max}}}
		// SNI (a host name, not an IP) makes the server use GetCertificate rather than httptest's built-in pair.
		res, err := cl.Get(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1))
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		return res.Proto + " " + tls.VersionName(res.TLS.Version), nil
	}
	if got, err := get(0); err != nil || !strings.HasSuffix(got, "TLS 1.3") {
		t.Fatal(got, err)
	}
	if _, err := get(tls.VersionTLS12); err == nil {
		t.Fatal("a TLS 1.2 client must be refused by default")
	}
}

func config(r *http.Request) string { return r.Proto }

func TestServerTLSMinVersion12OnlyWhenExplicit(t *testing.T) {
	p := newPKI(t, "localhost")
	tlsEnv(t, p)
	t.Setenv("OPENSPOOR_TLS_MIN_VERSION", "1.2")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	tc, err := c.ServerTLS()
	if err != nil || tc.MinVersion != tls.VersionTLS12 || len(tc.CipherSuites) == 0 {
		t.Fatal(tc, err)
	}
	for _, id := range tc.CipherSuites {
		for _, bad := range tls.InsecureCipherSuites() {
			if id == bad.ID {
				t.Fatalf("insecure suite %s", bad.Name)
			}
		}
	}
	if VersionName(tls.VersionTLS12) != "1.2" || VersionName(tls.VersionTLS13) != "1.3" {
		t.Fatal("VersionName")
	}
	t.Setenv("OPENSPOOR_TLS_MIN_VERSION", "1.1")
	if _, err := Load(); err == nil {
		t.Fatal("1.1 must be refused")
	}
}

func TestServerTLSClientCAVerifiesPresentedCerts(t *testing.T) {
	p := newPKI(t, "localhost")
	tlsEnv(t, p)
	t.Setenv("OPENSPOOR_TLS_CLIENT_CA", tmpFile(t, "ca.pem", string(p.caPEM)))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	tc, err := c.ServerTLS()
	if err != nil || tc.ClientAuth != tls.VerifyClientCertIfGiven || tc.ClientCAs == nil {
		t.Fatal(tc, err)
	}
	t.Setenv("OPENSPOOR_TLS_CLIENT_CA", tmpFile(t, "bad.pem", "nope"))
	c, _ = Load()
	if _, err := c.ServerTLS(); err == nil {
		t.Fatal("unreadable client CA must fail")
	}
	if _, err := LoadPool("/nonexistent"); err == nil {
		t.Fatal("missing pool")
	}
}

func TestServerTLSBadKeyPairFailsAtStartup(t *testing.T) {
	baseEnv(t)
	t.Setenv("OPENSPOOR_TLS_CERT", tmpFile(t, "c.pem", "x"))
	t.Setenv("OPENSPOOR_TLS_KEY", tmpFile(t, "k.pem", "y"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ServerTLS(); err == nil {
		t.Fatal("expected an error")
	}
	t.Setenv("OPENSPOOR_TLS_CERT", "/nonexistent/c.pem")
	c, _ = Load()
	if _, err := c.ServerTLS(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTLSConfigValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"cert without key":        {"OPENSPOOR_TLS_CERT": "/c"},
		"key without cert":        {"OPENSPOOR_TLS_KEY": "/k"},
		"client cert without key": {"OPENSPOOR_TLS_CLIENT_CERT": "/c"},
		"client ca without tls":   {"OPENSPOOR_TLS_CLIENT_CA": "/ca"},
		"redirect without tls":    {"OPENSPOOR_TLS_REDIRECT_ADDR": ":80"},
		"redirect to http":        {"OPENSPOOR_TLS_CERT": "/c", "OPENSPOOR_TLS_KEY": "/k", "OPENSPOOR_TLS_REDIRECT_ADDR": ":80"},
		"enforce without tls":     {"OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT": "true"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			baseEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	baseEnv(t)
	t.Setenv("OPENSPOOR_TLS_CERT", "/c")
	t.Setenv("OPENSPOOR_TLS_KEY", "/k")
	t.Setenv("OPENSPOOR_TLS_REDIRECT_ADDR", ":8081")
	t.Setenv("OPENSPOOR_PUBLIC_URL", "https://os.example.com")
	t.Setenv("OPENSPOOR_TLS_CLIENT_CERT", "/cc")
	t.Setenv("OPENSPOOR_TLS_CLIENT_KEY", "/ck")
	t.Setenv("OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT", "false")
	c, err := Load()
	if err != nil || c.TLS.RedirectAddr != ":8081" || c.TLS.RequireFederationClientCert || c.TLS.ClientCert != "/cc" {
		t.Fatal(c.TLS, err)
	}
}

func TestCertLoaderReloadsRotatedFiles(t *testing.T) {
	a, b := newPKI(t, "a.test"), newPKI(t, "b.test")
	dir := t.TempDir()
	cp, kp := filepath.Join(dir, "c"), filepath.Join(dir, "k")
	write := func(p pki, mod time.Time) {
		_ = os.WriteFile(cp, p.certPEM, 0o600)
		_ = os.WriteFile(kp, p.keyPEM, 0o600)
		_ = os.Chtimes(cp, mod, mod)
		_ = os.Chtimes(kp, mod, mod)
	}
	write(a, time.Now().Add(-time.Hour))
	l := &certLoader{cert: cp, key: kp}
	first, err := l.get()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := l.get(); again != first {
		t.Fatal("cached pair expected")
	}
	write(b, time.Now())
	l.lastStat = time.Time{} // let the stat interval elapse
	second, err := l.get()
	if err != nil || second == first {
		t.Fatal("rotated pair expected", err)
	}
	// Unchanged files after the interval keep the pair.
	l.lastStat = time.Time{}
	if same, _ := l.get(); same != second {
		t.Fatal("unchanged files keep the pair")
	}
	// A broken replacement keeps the last good pair; a missing file too.
	_ = os.WriteFile(cp, []byte("garbage"), 0o600)
	_ = os.Chtimes(cp, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	l.lastStat = time.Time{}
	if kept, err := l.get(); err != nil || kept != second {
		t.Fatal("last good pair expected", err)
	}
	_ = os.Remove(kp)
	l.lastStat = time.Time{}
	if kept, err := l.get(); err != nil || kept != second {
		t.Fatal("last good pair expected", err)
	}
}

func TestRedirectHandlerUsesConfiguredHost(t *testing.T) {
	h := RedirectHandler("https://os.example.com/")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://evil.test//x.test/a?b=1", nil)
	req.Host = "evil.test"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "https://os.example.com//x.test/a?b=1" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTrustedProxiesDefault(t *testing.T) {
	baseEnv(t)
	c, _ := Load()
	if c.TrustedProxies != 0 {
		t.Fatal(c.TrustedProxies)
	}
	t.Setenv("OPENSPOOR_TRUSTED_PROXIES", "2")
	if c, _ = Load(); c.TrustedProxies != 2 {
		t.Fatal(c.TrustedProxies)
	}
}
