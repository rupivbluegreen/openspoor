// Package secretsrc resolves secret configuration values from where they are kept.
//
// A configuration value is one of:
//
//	plain text                     used as is (environment variable; fine for development)
//	file:/path/to/secret           contents of the file, surrounding whitespace trimmed
//	vault:<path>#<field>           a field of a KV v2 secret in a HashiCorp Vault compatible server
//
// The Vault client uses net/http only. It authenticates with OPENSPOOR_VAULT_TOKEN or, when only
// OPENSPOOR_VAULT_ROLE is set, with the pod's Kubernetes service-account token (Kubernetes auth
// method). Resolution happens once at startup and fails closed: an unreadable or empty secret is an
// error, never an empty value. Error messages name the reference, never the value.
package secretsrc

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	filePrefix  = "file:"
	vaultPrefix = "vault:"

	maxSecretFile = 1 << 20
	maxVaultBody  = 1 << 20

	// DefaultK8sTokenFile is where Kubernetes mounts the pod's service-account token.
	DefaultK8sTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" // #nosec G101 -- a path, not a credential
)

// Options configure the Vault client. The zero value resolves plain and file references only.
type Options struct {
	VaultAddr      string // base URL, for example https://vault.internal:8200
	VaultToken     string // static token; takes precedence over Kubernetes login
	VaultRole      string // role for the Kubernetes auth method
	VaultAuthMount string // Kubernetes auth mount, default "kubernetes"
	VaultNamespace string // optional X-Vault-Namespace
	VaultCACert    string // optional PEM file with the CA that signed the Vault certificate
	K8sTokenFile   string // service-account token file, default DefaultK8sTokenFile
	HTTPClient     *http.Client
}

// OptionsFromEnv reads the OPENSPOOR_VAULT_* variables.
func OptionsFromEnv() Options {
	return Options{
		VaultAddr:      os.Getenv("OPENSPOOR_VAULT_ADDR"),
		VaultToken:     os.Getenv("OPENSPOOR_VAULT_TOKEN"),
		VaultRole:      os.Getenv("OPENSPOOR_VAULT_ROLE"),
		VaultAuthMount: os.Getenv("OPENSPOOR_VAULT_AUTH_MOUNT"),
		VaultNamespace: os.Getenv("OPENSPOOR_VAULT_NAMESPACE"),
		VaultCACert:    os.Getenv("OPENSPOOR_VAULT_CACERT"),
		K8sTokenFile:   os.Getenv("OPENSPOOR_VAULT_K8S_TOKEN_FILE"),
	}
}

// Resolver turns references into values. It caches the Vault token and each fetched secret for
// its own lifetime, so a startup that resolves several fields of one path makes one request.
type Resolver struct {
	opt Options

	mu    sync.Mutex
	token string
	cache map[string]map[string]any
}

// New returns a Resolver.
func New(opt Options) *Resolver {
	return &Resolver{opt: opt, cache: map[string]map[string]any{}}
}

// IsRef reports whether v is a file: or vault: reference rather than a literal value.
func IsRef(v string) bool {
	return strings.HasPrefix(v, filePrefix) || strings.HasPrefix(v, vaultPrefix)
}

// Resolve returns the secret v refers to. An empty v stays empty (the variable is unset).
func (r *Resolver) Resolve(ctx context.Context, v string) (string, error) {
	switch {
	case v == "":
		return "", nil
	case strings.HasPrefix(v, filePrefix):
		return readFile(strings.TrimPrefix(v, filePrefix))
	case strings.HasPrefix(v, vaultPrefix):
		return r.vault(ctx, strings.TrimPrefix(v, vaultPrefix))
	}
	return v, nil
}

func readFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("file reference has no path")
	}
	f, err := os.Open(path) // #nosec G304 -- the path is operator configuration
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, errors.Unwrap(err))
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFile+1))
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	if len(b) > maxSecretFile {
		return "", fmt.Errorf("secret file %s is larger than %d bytes", path, maxSecretFile)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return s, nil
}

// splitRef splits "<path>#<field>" and validates both parts.
func splitRef(ref string) (path, field string, err error) {
	path, field, ok := strings.Cut(ref, "#")
	path = strings.Trim(path, "/")
	if !ok || path == "" || field == "" {
		return "", "", errors.New("vault reference must look like vault:<mount>/<path>#<field>")
	}
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		return "", "", errors.New("vault reference needs a mount and a path: vault:<mount>/<path>#<field>")
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "?\\") {
			return "", "", errors.New("vault reference has an invalid path")
		}
	}
	return path, field, nil
}

func (r *Resolver) vault(ctx context.Context, ref string) (string, error) {
	path, field, err := splitRef(ref)
	if err != nil {
		return "", err
	}
	data, err := r.secret(ctx, path)
	if err != nil {
		return "", fmt.Errorf("vault %s: %w", path, err)
	}
	raw, ok := data[field]
	if !ok {
		return "", fmt.Errorf("vault %s has no field %q", path, field)
	}
	var s string
	switch t := raw.(type) {
	case string:
		s = t
	case json.Number:
		s = t.String()
	default:
		return "", fmt.Errorf("vault %s field %q is not a string", path, field)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("vault %s field %q is empty", path, field)
	}
	return s, nil
}

func (r *Resolver) base() (*url.URL, error) {
	if r.opt.VaultAddr == "" {
		return nil, errors.New("OPENSPOOR_VAULT_ADDR is not set")
	}
	u, err := url.Parse(r.opt.VaultAddr)
	if err != nil || u.Host == "" {
		return nil, errors.New("OPENSPOOR_VAULT_ADDR is not a valid URL")
	}
	// Tokens and secrets must not cross the network in clear text. Plain http is only for loopback.
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname())) {
		return nil, errors.New("OPENSPOOR_VAULT_ADDR must be https (http is allowed for loopback only)")
	}
	return u, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (r *Resolver) client() (*http.Client, error) {
	if r.opt.HTTPClient != nil {
		return r.opt.HTTPClient, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if r.opt.VaultCACert != "" {
		pem, err := os.ReadFile(r.opt.VaultCACert)
		if err != nil {
			return nil, fmt.Errorf("read OPENSPOOR_VAULT_CACERT: %w", errors.Unwrap(err))
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("OPENSPOOR_VAULT_CACERT holds no PEM certificate")
		}
		cfg.RootCAs = pool
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: cfg},
		// A redirect would carry the token to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// secret returns the data map of a KV v2 secret ("<mount>/<path>" is read from /v1/<mount>/data/<path>).
func (r *Resolver) secret(ctx context.Context, path string) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.cache[path]; ok {
		return d, nil
	}
	base, err := r.base()
	if err != nil {
		return nil, err
	}
	hc, err := r.client()
	if err != nil {
		return nil, err
	}
	token, err := r.login(ctx, base, hc)
	if err != nil {
		return nil, err
	}
	mount, rest, _ := strings.Cut(path, "/")
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + "/v1/" + mount + "/data/" + rest
	body, status, err := r.do(ctx, hc, http.MethodGet, u.String(), token, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("server answered HTTP %d", status)
	}
	var out struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil || out.Data.Data == nil {
		return nil, errors.New("response is not a KV v2 secret")
	}
	r.cache[path] = out.Data.Data
	return out.Data.Data, nil
}

// login returns the token to use: the static one, or a Kubernetes-auth login (cached).
func (r *Resolver) login(ctx context.Context, base *url.URL, hc *http.Client) (string, error) {
	if r.opt.VaultToken != "" {
		return r.opt.VaultToken, nil
	}
	if r.token != "" {
		return r.token, nil
	}
	if r.opt.VaultRole == "" {
		return "", errors.New("neither OPENSPOOR_VAULT_TOKEN nor OPENSPOOR_VAULT_ROLE is set")
	}
	tf := r.opt.K8sTokenFile
	if tf == "" {
		tf = DefaultK8sTokenFile
	}
	jwt, err := readFile(tf)
	if err != nil {
		return "", fmt.Errorf("kubernetes service-account token: %w", err)
	}
	mount := strings.Trim(r.opt.VaultAuthMount, "/")
	if mount == "" {
		mount = "kubernetes"
	}
	payload, _ := json.Marshal(map[string]string{"role": r.opt.VaultRole, "jwt": jwt})
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + "/v1/auth/" + mount + "/login"
	body, status, err := r.do(ctx, hc, http.MethodPost, u.String(), "", payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("kubernetes login answered HTTP %d", status)
	}
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Auth.ClientToken == "" {
		return "", errors.New("kubernetes login returned no token")
	}
	r.token = out.Auth.ClientToken
	return r.token, nil
}

func (r *Resolver) do(ctx context.Context, hc *http.Client, method, u, token string, payload []byte) ([]byte, int, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if r.opt.VaultNamespace != "" {
		req.Header.Set("X-Vault-Namespace", r.opt.VaultNamespace)
	}
	res, err := hc.Do(req)
	if err != nil {
		// url.Error carries the URL only; the token travels in a header and is never part of it.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, 0, fmt.Errorf("request failed: %w", ue.Err)
		}
		return nil, 0, errors.New("request failed")
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxVaultBody))
	if err != nil {
		return nil, 0, errors.New("read response failed")
	}
	return b, res.StatusCode, nil
}
