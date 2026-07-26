// Package config loads Flare's runtime configuration from the environment.
// Production refuses to start when a security-critical secret is missing or
// left at its insecure default, per the repo security rules.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment string
	Port        string
	BaseURL     string

	DatabaseURL   string
	DBMaxConns    int32
	DBMinConns    int32
	RetentionDays int

	// IngestRatePerMin caps events per minute per DSN key (or per IP when no
	// key), so a captured DSN cannot flood the write path.
	IngestRatePerMin int

	// Cold-tier Parquet. Local dir by default (self-host friendly); S3/MinIO opt-in.
	ParquetDir string

	// AllowDropWithoutExport lets retention drop aged partitions even when the
	// cold tier is unreachable. Default false, which is fail-closed: partitions
	// pile up (disk grows, loudly logged) rather than telemetry being destroyed
	// without ever being archived. An operator running deliberately without a
	// cold tier sets FLARE_ALLOW_DROP_WITHOUT_EXPORT=true to restore pruning.
	// It cannot be inferred: FLARE_PARQUET_DIR always has a default value, so
	// "cold tier configured" is indistinguishable from "cold tier defaulted".
	AllowDropWithoutExport bool

	// AllowPrivateAIEndpoint disables the SSRF guard on the tenant-supplied
	// BYOAI base_url. The guard used to be tied to ENVIRONMENT=production, which
	// is backwards: development is the DEFAULT for a self-hosted deployment, so
	// the protection was off exactly where it was least likely to be noticed.
	// It is now on everywhere unless this is explicitly set, which is what a
	// developer pointing at a local Ollama/vLLM needs.
	AllowPrivateAIEndpoint bool
	S3Endpoint             string
	S3Bucket               string
	S3AccessKey            string
	S3SecretKey            string
	S3Region               string
	S3UseSSL               bool

	SessionKey         string
	CSRFKey            string
	SessionLifetime    time.Duration
	SessionIdleTimeout time.Duration
	DisableCSRF        bool

	RedisURL string

	// SMTP for transactional email (alert delivery, password reset). Optional:
	// when SMTPHost/SMTPFrom are unset, email features quietly no-op so a
	// self-host without a mail server still runs.
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPFrom     string
	SMTPFromName string
	SMTPTLS      string // "tls" (implicit 465) | "starttls" (587) | "none" (dev)

	// SecretKey (FLARE_SECRET_KEY) encrypts integration secrets at rest (BYOAI
	// key, OIDC client secret, GitHub token, channel webhook URLs). Optional and
	// fail-safe: when unset those secrets are stored plaintext (current behavior)
	// rather than the service failing. Provisioned by the deploy pipeline.
	SecretKey string
}

func (c Config) IsProduction() bool { return c.Environment == "production" }

// EmailEnabled reports whether transactional email can be sent.
func (c Config) EmailEnabled() bool { return c.SMTPHost != "" && c.SMTPFrom != "" }

func Load() (Config, error) {
	c := Config{
		Environment:      env("ENVIRONMENT", "development"),
		Port:             env("PORT", "8080"),
		BaseURL:          env("BASE_URL", "http://localhost:8080"),
		DatabaseURL:      env("DATABASE_URL", ""),
		DBMaxConns:       int32(envInt("DB_MAX_CONNS", 20)),
		DBMinConns:       int32(envInt("DB_MIN_CONNS", 2)),
		RetentionDays:    envInt("RETENTION_DAYS", 30),
		IngestRatePerMin: envInt("INGEST_RATE_PER_MIN", 1200),
		ParquetDir:       env("FLARE_PARQUET_DIR", "data/parquet"),
		S3Endpoint:       env("FLARE_PARQUET_S3_ENDPOINT", ""),
		S3Bucket:         env("FLARE_PARQUET_S3_BUCKET", "flare"),
		S3AccessKey:      env("FLARE_PARQUET_S3_ACCESS_KEY", ""),
		S3SecretKey:      env("FLARE_PARQUET_S3_SECRET_KEY", ""),
		S3Region:         env("FLARE_PARQUET_S3_REGION", "us-east-1"),
		// Fail CLOSED: any spelling other than an explicit false keeps TLS on.
		// Comparing against "true" meant "TRUE", "1" or a typo silently sent
		// object-storage credentials in the clear.
		S3UseSSL:               !isFalse(env("FLARE_PARQUET_S3_USE_SSL", "true")),
		AllowPrivateAIEndpoint: isTrue(env("FLARE_ALLOW_PRIVATE_AI_ENDPOINT", "")),
		SessionKey:             env("SESSION_KEY", ""),
		CSRFKey:                env("CSRF_KEY", ""),
		SessionLifetime:        time.Duration(envInt("SESSION_LIFETIME_HOURS", 720)) * time.Hour,
		SessionIdleTimeout:     time.Duration(envInt("SESSION_IDLE_HOURS", 168)) * time.Hour,
		DisableCSRF:            env("DISABLE_CSRF", "") == "true",
		RedisURL:               env("REDIS_URL", ""),
		SMTPHost:               env("SMTP_HOST", ""),
		SMTPPort:               envInt("SMTP_PORT", 587),
		SMTPUser:               env("SMTP_USER", ""),
		SMTPPass:               env("SMTP_PASS", ""),
		SMTPFrom:               env("SMTP_FROM", ""),
		SMTPFromName:           env("SMTP_FROM_NAME", "Flare"),
		SMTPTLS:                env("SMTP_TLS", "starttls"),
		SecretKey:              env("FLARE_SECRET_KEY", ""),
		AllowDropWithoutExport: env("FLARE_ALLOW_DROP_WITHOUT_EXPORT", "") == "true",
	}

	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}

	// In production every secret must be explicitly set. Auth is never optional.
	if c.IsProduction() {
		var missing []string
		if c.SessionKey == "" {
			missing = append(missing, "SESSION_KEY")
		}
		if c.CSRFKey == "" {
			missing = append(missing, "CSRF_KEY")
		}
		// FLARE_SECRET_KEY encrypts integration secrets (BYOAI key, OIDC client
		// secret, GitHub token, webhook URLs) at rest. Without it those land in
		// the DB as plaintext, so a DB dump or backup leak exposes live
		// third-party credentials. Fail closed in production, never silently.
		if c.SecretKey == "" {
			missing = append(missing, "FLARE_SECRET_KEY")
		}
		if len(missing) > 0 {
			return c, fmt.Errorf("production requires: %s", strings.Join(missing, ", "))
		}
	} else {
		// Dev-only fallbacks so the service boots without ceremony.
		if c.SessionKey == "" {
			c.SessionKey = "dev-session-key-not-for-production"
		}
		if c.CSRFKey == "" {
			// gorilla/csrf requires exactly 32 bytes or it panics at startup.
			c.CSRFKey = "dev-csrf-key-not-for-production0"
		}
	}

	return c, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// isTrue / isFalse accept the spellings operators actually type, so a boolean
// flag never silently takes the unsafe branch on a capitalisation difference.
func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

func isFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no", "off":
		return true
	}
	return false
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
