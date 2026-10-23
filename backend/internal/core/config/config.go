// Package config loads Open Spoor runtime configuration from the environment.
package config

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openspoor/openspoor/internal/core/secretsrc"
)

// Mode selects how one binary behaves; it is configuration, never a fork.
type Mode string

const (
	ModeSaaS   Mode = "saas"
	ModeSingle Mode = "single"
	ModeHub    Mode = "hub"
)

// Config is the full runtime configuration.
type Config struct {
	Mode              Mode
	HTTPAddr          string
	WebRoot           string // directory holding the console, verify and operator builds; empty disables static serving
	DatabaseURL       string
	JWTSecret         []byte
	JWTTTL            time.Duration
	DevAuth           bool
	OIDCIssuer        string
	OIDCAudience      string
	KafkaBrokers      []string
	S3Endpoint        string
	S3Bucket          string
	S3Region          string
	S3AccessKey       string
	S3SecretKey       string
	ArchiveAfterDays  int // captures older than this are archived to S3
	SMTPURL           string
	LocalLLMURL       string // OpenAI-compatible chat endpoint of a local model server (air-gapped hubs)
	Region            string // the data-residency region this installation serves; tenants are pinned to it
	LocalLLMModel     string
	MailFrom          string
	BaseDomain        string
	PublicURL         string
	BillingDriver     string // "none", "lago", "stripe"
	LagoURL           string
	LagoAPIKey        string
	AnthropicKey      string
	AnthropicModel    string
	WorkerConcurrency int
	CORSOrigins       []string
	LogLevel          string
	MasterKey         []byte

	// MigrateDatabaseURL is the role used by `migrate` (OPENSPOOR_MIGRATE_DATABASE_URL); empty means DatabaseURL.
	MigrateDatabaseURL string
	// TrustedProxies is how many reverse proxies in front of the API append to X-Forwarded-For
	// (OPENSPOOR_TRUSTED_PROXIES, default 0: the TCP peer address is the client address).
	TrustedProxies int

	TLS TLS
}

// Load reads configuration from environment variables with safe defaults for development.
//
// Secret values (master key, JWT secret, database URL, Lago and model API keys, SMTP URL, S3 secret
// key) may be plain text, `file:/path` or `vault:<mount>/<path>#<field>`; see package secretsrc.
// They are resolved here once, and any failure to resolve a configured reference is an error.
func Load() (Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return LoadWith(ctx, secretsrc.New(secretsrc.OptionsFromEnv()))
}

// LoadWith is Load with an explicit secret resolver.
func LoadWith(ctx context.Context, res *secretsrc.Resolver) (Config, error) {
	c := Config{
		Mode:              Mode(env("OPENSPOOR_MODE", "single")),
		HTTPAddr:          env("OPENSPOOR_HTTP_ADDR", ":8080"),
		WebRoot:           os.Getenv("OPENSPOOR_WEB_ROOT"),
		DatabaseURL:       env("OPENSPOOR_DATABASE_URL", "postgres://openspoor:openspoor@localhost:5432/openspoor?sslmode=disable"),
		JWTTTL:            envDuration("OPENSPOOR_JWT_TTL", 12*time.Hour),
		DevAuth:           envBool("OPENSPOOR_DEV_AUTH", false),
		OIDCIssuer:        env("OPENSPOOR_OIDC_ISSUER", ""),
		OIDCAudience:      env("OPENSPOOR_OIDC_AUDIENCE", "openspoor"),
		KafkaBrokers:      envList("OPENSPOOR_KAFKA_BROKERS"),
		S3Endpoint:        env("OPENSPOOR_S3_ENDPOINT", ""),
		S3Bucket:          env("OPENSPOOR_S3_BUCKET", "openspoor-archive"),
		S3Region:          env("OPENSPOOR_S3_REGION", "us-east-1"),
		S3AccessKey:       env("OPENSPOOR_S3_ACCESS_KEY", ""),
		S3SecretKey:       env("OPENSPOOR_S3_SECRET_KEY", ""),
		ArchiveAfterDays:  envInt("OPENSPOOR_ARCHIVE_AFTER_DAYS", 90),
		SMTPURL:           env("OPENSPOOR_SMTP_URL", ""),
		LocalLLMURL:       env("OPENSPOOR_LOCAL_LLM_URL", ""),
		Region:            env("OPENSPOOR_REGION", "eu"),
		LocalLLMModel:     env("OPENSPOOR_LOCAL_LLM_MODEL", ""),
		MailFrom:          env("OPENSPOOR_MAIL_FROM", ""),
		BaseDomain:        env("OPENSPOOR_BASE_DOMAIN", "localhost"),
		PublicURL:         env("OPENSPOOR_PUBLIC_URL", "http://localhost:8080"),
		BillingDriver:     env("OPENSPOOR_BILLING", "none"),
		LagoURL:           env("OPENSPOOR_LAGO_URL", ""),
		LagoAPIKey:        env("OPENSPOOR_LAGO_API_KEY", ""),
		AnthropicKey:      env("ANTHROPIC_API_KEY", ""),
		AnthropicModel:    env("OPENSPOOR_ASSISTANT_MODEL", ""),
		WorkerConcurrency: envInt("OPENSPOOR_WORKER_CONCURRENCY", 4),
		CORSOrigins:       envList("OPENSPOOR_CORS_ORIGINS"),
		LogLevel:          env("OPENSPOOR_LOG_LEVEL", "info"),
		TrustedProxies:    envInt("OPENSPOOR_TRUSTED_PROXIES", 0),
	}
	switch c.Mode {
	case ModeSaaS, ModeSingle, ModeHub:
	default:
		return c, fmt.Errorf("OPENSPOOR_MODE must be saas, single or hub, got %q", c.Mode)
	}
	if err := c.resolveSecrets(ctx, res); err != nil {
		return c, err
	}
	if err := c.loadTLS(); err != nil {
		return c, err
	}
	if len(c.JWTSecret) == 0 {
		if !c.DevAuth {
			return c, fmt.Errorf("OPENSPOOR_JWT_SECRET is required unless OPENSPOOR_DEV_AUTH=true")
		}
		c.JWTSecret = []byte("dev-only-insecure-secret-change-me")
	}
	if len(c.JWTSecret) < 16 && !c.DevAuth {
		return c, fmt.Errorf("OPENSPOOR_JWT_SECRET must be at least 16 bytes")
	}
	if len(c.MasterKey) == 0 {
		if !c.DevAuth {
			return c, fmt.Errorf("OPENSPOOR_MASTER_KEY is required unless OPENSPOOR_DEV_AUTH=true")
		}
		c.MasterKey = append([]byte("dev-master:"), c.JWTSecret...)
	}
	if c.BillingDriver == "" {
		c.BillingDriver = "none"
	}
	return c, nil
}

// resolveSecrets replaces file:/vault: references in the secret settings by their values and
// fills JWTSecret, MasterKey and MigrateDatabaseURL, which have no plain-text field of their own.
func (c *Config) resolveSecrets(ctx context.Context, res *secretsrc.Resolver) error {
	one := func(name string, dst *string) error {
		v, err := res.Resolve(ctx, *dst)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = v
		return nil
	}
	for _, s := range []struct {
		name string
		dst  *string
	}{
		{"OPENSPOOR_DATABASE_URL", &c.DatabaseURL}, {"OPENSPOOR_LAGO_API_KEY", &c.LagoAPIKey},
		{"ANTHROPIC_API_KEY", &c.AnthropicKey}, {"OPENSPOOR_SMTP_URL", &c.SMTPURL}, {"OPENSPOOR_S3_SECRET_KEY", &c.S3SecretKey},
	} {
		if err := one(s.name, s.dst); err != nil {
			return err
		}
	}
	if pw := os.Getenv("OPENSPOOR_DATABASE_PASSWORD"); pw != "" {
		v, err := res.Resolve(ctx, pw)
		if err != nil {
			return fmt.Errorf("OPENSPOOR_DATABASE_PASSWORD: %w", err)
		}
		if c.DatabaseURL, err = withPassword(c.DatabaseURL, v); err != nil {
			return fmt.Errorf("OPENSPOOR_DATABASE_PASSWORD: %w", err)
		}
	}
	jwt, master, mig := os.Getenv("OPENSPOOR_JWT_SECRET"), os.Getenv("OPENSPOOR_MASTER_KEY"), os.Getenv("OPENSPOOR_MIGRATE_DATABASE_URL")
	for _, s := range []struct {
		name string
		src  string
		dst  func(string)
	}{
		{"OPENSPOOR_JWT_SECRET", jwt, func(v string) { c.JWTSecret = []byte(v) }},
		{"OPENSPOOR_MASTER_KEY", master, func(v string) { c.MasterKey = []byte(v) }},
		{"OPENSPOOR_MIGRATE_DATABASE_URL", mig, func(v string) { c.MigrateDatabaseURL = v }},
	} {
		v := s.src
		if err := one(s.name, &v); err != nil {
			return err
		}
		s.dst(v)
	}
	return nil
}

// withPassword sets the password of a database URL; the user name is kept.
func withPassword(dbURL, password string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the database URL is not a URL with a host")
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v, err := strconv.ParseBool(env(k, strconv.FormatBool(def)))
	if err != nil {
		return def
	}
	return v
}

func envInt(k string, def int) int {
	v, err := strconv.Atoi(env(k, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return v
}

func envDuration(k string, def time.Duration) time.Duration {
	v, err := time.ParseDuration(env(k, def.String()))
	if err != nil {
		return def
	}
	return v
}

func envList(k string) []string {
	v := env(k, "")
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
