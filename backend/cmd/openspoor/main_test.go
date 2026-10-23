package main

import (
	"bytes"
	"context"
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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/testkit"
)

func TestMigrateURL(t *testing.T) {
	c := config.Config{DatabaseURL: "postgres://app@h/db"}
	if migrateURL(c) != "postgres://app@h/db" {
		t.Fatal("falls back to the application URL")
	}
	c.MigrateDatabaseURL = "postgres://owner@h/db"
	if migrateURL(c) != "postgres://owner@h/db" {
		t.Fatal("explicit migration URL wins")
	}
}

func TestNewServerPlain(t *testing.T) {
	s, err := newServer(http.NotFoundHandler(), config.Config{HTTPAddr: ":0"})
	if err != nil || s.TLSConfig != nil || s.ReadHeaderTimeout == 0 {
		t.Fatal(s, err)
	}
}

func writeKeyPair(t *testing.T) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	_ = os.WriteFile(certPath, cp, 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	pool = x509.NewCertPool()
	pool.AppendCertsFromPEM(cp)
	return
}

func TestNewServerServesTLS13(t *testing.T) {
	cert, key, pool := writeKeyPair(t)
	cfg := config.Config{HTTPAddr: "127.0.0.1:0", TLS: config.TLS{Cert: cert, Key: key, MinVersion: tls.VersionTLS13}}
	s, err := newServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), cfg)
	if err != nil || s.TLSConfig == nil {
		t.Fatal(s, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.ServeTLS(ln, "", "") }()
	defer s.Close()
	url := "https://localhost:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) + "/"
	get := func(max uint16) (*http.Response, error) {
		cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MaxVersion: max}}}
		return cl.Get(url)
	}
	res, err := get(0)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated %x", res.TLS.Version)
	}
	if old, err := get(tls.VersionTLS12); err == nil {
		_ = old.Body.Close()
		t.Fatal("TLS 1.2 must be refused by default")
	}
}

func TestNewServerBadKeyPair(t *testing.T) {
	if _, err := newServer(http.NotFoundHandler(), config.Config{TLS: config.TLS{Cert: "/nonexistent", Key: "/nonexistent"}}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRunRotateKeys(t *testing.T) {
	e := testkit.New(t)
	var out bytes.Buffer
	if _, err := e.App.Deps.Keys.For(context.Background(), e.Tenant.ID); err != nil { // creates the tenant's data key
		t.Fatal(err)
	}
	cfg := e.App.Deps.Cfg
	if err := runRotateKeys(context.Background(), e.App, cfg, false, &out); err == nil || !strings.Contains(err.Error(), "OPENSPOOR_NEW_MASTER_KEY") {
		t.Fatalf("without a new key: %v", err)
	}
	cfg.NewMasterKey = []byte("a-brand-new-master-key-0123456789")
	if err := runRotateKeys(context.Background(), e.App, cfg, true, &out); err != nil || !strings.Contains(out.String(), "would re-seal (dry run, nothing changed)") || strings.Contains(out.String(), "Next:") {
		t.Fatalf("dry run: %v %q", err, out.String())
	}
	out.Reset()
	if err := runRotateKeys(context.Background(), e.App, cfg, false, &out); err != nil || !strings.Contains(out.String(), "re-sealed:") || !strings.Contains(out.String(), "Next: set OPENSPOOR_MASTER_KEY") {
		t.Fatalf("rotate: %v %q", err, out.String())
	}
	if err := runRotateKeys(context.Background(), e.App, cfg, false, &out); err == nil {
		t.Fatal("the process still holds the old key: a second rotation must fail")
	}
}

func TestNewMasterKeyFromEnvironment(t *testing.T) {
	t.Setenv("OPENSPOOR_DEV_AUTH", "true")
	t.Setenv("OPENSPOOR_NEW_MASTER_KEY", "replacement-master-key-0123456789")
	c, err := config.Load()
	if err != nil || string(c.NewMasterKey) != "replacement-master-key-0123456789" {
		t.Fatalf("%v %q", err, c.NewMasterKey)
	}
}
