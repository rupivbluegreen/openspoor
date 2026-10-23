package config

import (
	"strings"
	"testing"
	"time"
)

var allVars = []string{
	"OPENSPOOR_MODE", "OPENSPOOR_HTTP_ADDR", "OPENSPOOR_WEB_ROOT", "OPENSPOOR_DATABASE_URL", "OPENSPOOR_JWT_SECRET",
	"OPENSPOOR_JWT_TTL", "OPENSPOOR_DEV_AUTH", "OPENSPOOR_OIDC_ISSUER", "OPENSPOOR_OIDC_AUDIENCE", "OPENSPOOR_KAFKA_BROKERS",
	"OPENSPOOR_S3_ENDPOINT", "OPENSPOOR_S3_BUCKET", "OPENSPOOR_S3_REGION", "OPENSPOOR_S3_ACCESS_KEY", "OPENSPOOR_S3_SECRET_KEY",
	"OPENSPOOR_ARCHIVE_AFTER_DAYS", "OPENSPOOR_BASE_DOMAIN", "OPENSPOOR_PUBLIC_URL", "OPENSPOOR_BILLING",
	"OPENSPOOR_ASSISTANT_MODEL", "OPENSPOOR_WORKER_CONCURRENCY",
	"OPENSPOOR_CORS_ORIGINS", "OPENSPOOR_LOG_LEVEL", "OPENSPOOR_MASTER_KEY",
}

// clearEnv blanks every variable Load reads; an empty value falls back to the default.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allVars {
		t.Setenv(k, "")
	}
}

func TestLoadDevDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENSPOOR_DEV_AUTH", "true")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeSingle || c.HTTPAddr != ":8080" || c.JWTTTL != 12*time.Hour || c.ArchiveAfterDays != 90 ||
		c.WorkerConcurrency != 4 || c.BillingDriver != "none" || c.LogLevel != "info" || c.OIDCAudience != "openspoor" ||
		c.S3Bucket != "openspoor-archive" || c.S3Region != "us-east-1" || c.BaseDomain != "localhost" ||
		c.PublicURL != "http://localhost:8080" || c.WebRoot != "" || c.KafkaBrokers != nil || c.CORSOrigins != nil {
		t.Fatalf("defaults: %+v", c)
	}
	if !strings.HasPrefix(c.DatabaseURL, "postgres://") {
		t.Fatalf("database url default: %q", c.DatabaseURL)
	}
	if string(c.JWTSecret) != "dev-only-insecure-secret-change-me" {
		t.Fatalf("dev secret: %q", c.JWTSecret)
	}
	if string(c.MasterKey) != "dev-master:dev-only-insecure-secret-change-me" {
		t.Fatalf("dev master key: %q", c.MasterKey)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENSPOOR_MODE", "hub")
	t.Setenv("OPENSPOOR_JWT_SECRET", "0123456789abcdef")
	t.Setenv("OPENSPOOR_MASTER_KEY", "mk")
	t.Setenv("OPENSPOOR_JWT_TTL", "30m")
	t.Setenv("OPENSPOOR_ARCHIVE_AFTER_DAYS", "7")
	t.Setenv("OPENSPOOR_WORKER_CONCURRENCY", "9")
	t.Setenv("OPENSPOOR_KAFKA_BROKERS", " a:9092 , ,b:9092,")
	t.Setenv("OPENSPOOR_CORS_ORIGINS", "https://x.test")
	t.Setenv("OPENSPOOR_BILLING", "custom")
	t.Setenv("OPENSPOOR_WEB_ROOT", "/srv/web")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeHub || c.DevAuth || c.JWTTTL != 30*time.Minute || c.ArchiveAfterDays != 7 || c.WorkerConcurrency != 9 ||
		c.BillingDriver != "custom" || c.WebRoot != "/srv/web" || string(c.MasterKey) != "mk" {
		t.Fatalf("overrides: %+v", c)
	}
	if len(c.KafkaBrokers) != 2 || c.KafkaBrokers[0] != "a:9092" || c.KafkaBrokers[1] != "b:9092" {
		t.Fatalf("brokers: %q", c.KafkaBrokers)
	}
	if len(c.CORSOrigins) != 1 || c.CORSOrigins[0] != "https://x.test" {
		t.Fatalf("cors: %q", c.CORSOrigins)
	}
}

func TestLoadUnparsableValuesFallBack(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENSPOOR_DEV_AUTH", "yes please")
	t.Setenv("OPENSPOOR_JWT_SECRET", "0123456789abcdef")
	t.Setenv("OPENSPOOR_MASTER_KEY", "mk")
	t.Setenv("OPENSPOOR_JWT_TTL", "forever")
	t.Setenv("OPENSPOOR_ARCHIVE_AFTER_DAYS", "ninety")
	t.Setenv("OPENSPOOR_WORKER_CONCURRENCY", "4.5")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DevAuth || c.JWTTTL != 12*time.Hour || c.ArchiveAfterDays != 90 || c.WorkerConcurrency != 4 {
		t.Fatalf("fallbacks: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"invalid mode", map[string]string{"OPENSPOOR_MODE": "cluster", "OPENSPOOR_DEV_AUTH": "true"}, "OPENSPOOR_MODE"},
		{"missing secret", map[string]string{}, "OPENSPOOR_JWT_SECRET is required"},
		{"short secret", map[string]string{"OPENSPOOR_JWT_SECRET": "short", "OPENSPOOR_MASTER_KEY": "mk"}, "at least 16 bytes"},
		{"missing master key", map[string]string{"OPENSPOOR_JWT_SECRET": "0123456789abcdef"}, "OPENSPOOR_MASTER_KEY is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadShortSecretAllowedWithDevAuth(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENSPOOR_DEV_AUTH", "1")
	t.Setenv("OPENSPOOR_JWT_SECRET", "short")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(c.JWTSecret) != "short" || string(c.MasterKey) != "dev-master:short" {
		t.Fatalf("got %+v", c)
	}
	for _, m := range []string{"saas", "single", "hub"} {
		t.Setenv("OPENSPOOR_MODE", m)
		if c, err := Load(); err != nil || string(c.Mode) != m {
			t.Fatalf("mode %s: %v %v", m, c.Mode, err)
		}
	}
}
