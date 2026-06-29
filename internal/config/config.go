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

	// Cold-tier Parquet. Local dir by default (self-host friendly); S3/MinIO opt-in.
	ParquetDir  string
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3UseSSL    bool

	SessionKey         string
	CSRFKey            string
	SessionLifetime    time.Duration
	SessionIdleTimeout time.Duration
	DisableCSRF        bool

	RedisURL string
}

func (c Config) IsProduction() bool { return c.Environment == "production" }

func Load() (Config, error) {
	c := Config{
		Environment:        env("ENVIRONMENT", "development"),
		Port:               env("PORT", "8080"),
		BaseURL:            env("BASE_URL", "http://localhost:8080"),
		DatabaseURL:        env("DATABASE_URL", ""),
		DBMaxConns:         int32(envInt("DB_MAX_CONNS", 20)),
		DBMinConns:         int32(envInt("DB_MIN_CONNS", 2)),
		RetentionDays:      envInt("RETENTION_DAYS", 30),
		ParquetDir:         env("FLARE_PARQUET_DIR", "data/parquet"),
		S3Endpoint:         env("FLARE_PARQUET_S3_ENDPOINT", ""),
		S3Bucket:           env("FLARE_PARQUET_S3_BUCKET", "flare"),
		S3AccessKey:        env("FLARE_PARQUET_S3_ACCESS_KEY", ""),
		S3SecretKey:        env("FLARE_PARQUET_S3_SECRET_KEY", ""),
		S3Region:           env("FLARE_PARQUET_S3_REGION", "us-east-1"),
		S3UseSSL:           env("FLARE_PARQUET_S3_USE_SSL", "true") == "true",
		SessionKey:         env("SESSION_KEY", ""),
		CSRFKey:            env("CSRF_KEY", ""),
		SessionLifetime:    time.Duration(envInt("SESSION_LIFETIME_HOURS", 720)) * time.Hour,
		SessionIdleTimeout: time.Duration(envInt("SESSION_IDLE_HOURS", 168)) * time.Hour,
		DisableCSRF:        env("DISABLE_CSRF", "") == "true",
		RedisURL:           env("REDIS_URL", ""),
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
		if len(missing) > 0 {
			return c, fmt.Errorf("production requires: %s", strings.Join(missing, ", "))
		}
	} else {
		// Dev-only fallbacks so the service boots without ceremony.
		if c.SessionKey == "" {
			c.SessionKey = "dev-session-key-not-for-production"
		}
		if c.CSRFKey == "" {
			c.CSRFKey = "dev-csrf-key-32-bytes-long-000000"
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

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
