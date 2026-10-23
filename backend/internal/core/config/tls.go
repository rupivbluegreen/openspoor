package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TLS holds transport security settings. All paths are PEM files.
//
// Normally a reverse proxy or ingress terminates TLS and these stay empty. Setting Cert and Key makes
// the API serve HTTPS itself, which federation mutual TLS needs (docs/runbooks/secrets.md, ADR-008).
type TLS struct {
	Cert       string // OPENSPOOR_TLS_CERT: server certificate chain
	Key        string // OPENSPOOR_TLS_KEY: its private key
	ClientCA   string // OPENSPOOR_TLS_CLIENT_CA: CA that signs peer client certificates (optional)
	MinVersion uint16 // OPENSPOOR_TLS_MIN_VERSION: 1.3 (default) or, only when set explicitly, 1.2

	// Outbound (federation delivery).
	ClientCert string // OPENSPOOR_TLS_CLIENT_CERT: certificate this node presents to peers
	ClientKey  string // OPENSPOOR_TLS_CLIENT_KEY
	PeerCA     string // OPENSPOOR_TLS_PEER_CA: extra CA roots for peers that use a private CA

	// RequireFederationClientCert makes /api/v1/federation/inbox demand a client certificate that
	// matches the sending peer's pinned fingerprint (OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT;
	// default on when the API serves TLS itself, off behind a TLS-terminating proxy).
	RequireFederationClientCert bool
	// RedirectAddr is an optional plain-HTTP listener that redirects to PublicURL (OPENSPOOR_TLS_REDIRECT_ADDR).
	RedirectAddr string
}

// Enabled reports whether the API serves HTTPS itself.
func (t TLS) Enabled() bool { return t.Cert != "" }

func (c *Config) loadTLS() error {
	t := TLS{
		Cert: env("OPENSPOOR_TLS_CERT", ""), Key: env("OPENSPOOR_TLS_KEY", ""), ClientCA: env("OPENSPOOR_TLS_CLIENT_CA", ""),
		ClientCert: env("OPENSPOOR_TLS_CLIENT_CERT", ""), ClientKey: env("OPENSPOOR_TLS_CLIENT_KEY", ""), PeerCA: env("OPENSPOOR_TLS_PEER_CA", ""),
		RedirectAddr: env("OPENSPOOR_TLS_REDIRECT_ADDR", ""),
	}
	switch v := env("OPENSPOOR_TLS_MIN_VERSION", "1.3"); v {
	case "1.3":
		t.MinVersion = tls.VersionTLS13
	case "1.2":
		t.MinVersion = tls.VersionTLS12
	default:
		return fmt.Errorf("OPENSPOOR_TLS_MIN_VERSION must be 1.3 or 1.2, got %q", v)
	}
	if (t.Cert == "") != (t.Key == "") {
		return errors.New("OPENSPOOR_TLS_CERT and OPENSPOOR_TLS_KEY must be set together")
	}
	if (t.ClientCert == "") != (t.ClientKey == "") {
		return errors.New("OPENSPOOR_TLS_CLIENT_CERT and OPENSPOOR_TLS_CLIENT_KEY must be set together")
	}
	if t.ClientCA != "" && t.Cert == "" {
		return errors.New("OPENSPOOR_TLS_CLIENT_CA needs OPENSPOOR_TLS_CERT and OPENSPOOR_TLS_KEY")
	}
	if t.RedirectAddr != "" {
		if t.Cert == "" {
			return errors.New("OPENSPOOR_TLS_REDIRECT_ADDR needs OPENSPOOR_TLS_CERT and OPENSPOOR_TLS_KEY")
		}
		if u, err := url.Parse(c.PublicURL); err != nil || u.Scheme != "https" {
			return errors.New("OPENSPOOR_TLS_REDIRECT_ADDR needs an https OPENSPOOR_PUBLIC_URL (the redirect target)")
		}
	}
	t.RequireFederationClientCert = envBool("OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT", t.Cert != "")
	if t.RequireFederationClientCert && t.Cert == "" {
		return errors.New("OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT needs the API to serve TLS (OPENSPOOR_TLS_CERT); behind a proxy, enforce mutual TLS at the proxy")
	}
	c.TLS = t
	return nil
}

// VersionName renders a tls version constant.
func VersionName(v uint16) string { return "1." + strconv.Itoa(int(v-tls.VersionTLS10)) }

// ServerTLS builds the server's tls.Config, or nil when the API does not serve TLS itself.
//
// Client certificates are never demanded for the whole API: browsers and integrations do not have
// one. When federation enforcement is on, the inbox handler requires a certificate and compares its
// fingerprint to the sending peer's pinned one. With a client CA the handshake also verifies the
// chain of any certificate that is presented; without one the pinned fingerprint is the only trust anchor.
func (c Config) ServerTLS() (*tls.Config, error) {
	t := c.TLS
	if !t.Enabled() {
		return nil, nil
	}
	ld := &certLoader{cert: t.Cert, key: t.Key}
	if _, err := ld.get(); err != nil { // fail at startup, not on the first request
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:     t.MinVersion,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return ld.get() },
		ClientAuth:     tls.RequestClientCert,
		// Go fixes the TLS 1.3 suites; for an explicitly allowed TLS 1.2 only forward-secret AEAD suites remain.
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
	}
	if t.ClientCA != "" {
		pool, err := LoadPool(t.ClientCA)
		if err != nil {
			return nil, fmt.Errorf("OPENSPOOR_TLS_CLIENT_CA: %w", err)
		}
		cfg.ClientCAs, cfg.ClientAuth = pool, tls.VerifyClientCertIfGiven
	}
	return cfg, nil
}

// LoadPool reads a PEM bundle of CA certificates.
func LoadPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) // #nosec G304 -- operator configuration
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return pool, nil
}

// certLoader serves a key pair from files and reloads it when a file changes, so a renewed
// certificate (cert-manager, a rotated secret mount) needs no restart.
type certLoader struct {
	cert, key string

	mu       sync.Mutex
	pair     *tls.Certificate
	loaded   [2]time.Time
	lastStat time.Time
}

func (l *certLoader) get() (*tls.Certificate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pair != nil && time.Since(l.lastStat) < 30*time.Second {
		return l.pair, nil
	}
	var mt [2]time.Time
	for i, p := range []string{l.cert, l.key} {
		st, err := os.Stat(p)
		if err != nil {
			if l.pair != nil { // keep serving the last good pair
				return l.pair, nil
			}
			return nil, fmt.Errorf("TLS key pair: %w", err)
		}
		mt[i] = st.ModTime()
	}
	l.lastStat = time.Now()
	if l.pair != nil && mt == l.loaded {
		return l.pair, nil
	}
	pair, err := tls.LoadX509KeyPair(l.cert, l.key)
	if err != nil {
		if l.pair != nil {
			return l.pair, nil
		}
		return nil, fmt.Errorf("TLS key pair: %w", err)
	}
	l.pair, l.loaded = &pair, mt
	return l.pair, nil
}

// RedirectHandler answers every request with a permanent redirect to the same path on publicURL.
// The target host is fixed configuration, never taken from the request (no open redirect, no Host injection).
func RedirectHandler(publicURL string) http.Handler {
	base := strings.TrimSuffix(publicURL, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The target is the configured https base URL plus the request path, never a request-supplied host.
		http.Redirect(w, r, base+r.URL.RequestURI(), http.StatusPermanentRedirect) // #nosec G710
	})
}
