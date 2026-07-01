package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/gorilla/csrf"

	"github.com/bright-interaction/flare/internal/analytics"
	"github.com/bright-interaction/flare/internal/api"
	"github.com/bright-interaction/flare/internal/auth"
	"github.com/bright-interaction/flare/internal/config"
	"github.com/bright-interaction/flare/internal/db"
	"github.com/bright-interaction/flare/internal/partition"
)

//go:embed all:frontend/build
var frontendFiles embed.FS

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	level := slog.LevelInfo
	if !cfg.IsProduction() {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	if err := db.RunMigrations(ctx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	slog.Info("migrations applied")

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	slog.Info("database connected", "max_conns", cfg.DBMaxConns)

	// Analytics: embedded DuckDB attached read-only to Postgres + a Parquet cold
	// tier. Non-fatal: if it fails to open, the server still serves every read
	// via the hot tier; only the analytics endpoints + cold export are disabled.
	var analyticsMgr *analytics.Manager
	var exporter partition.Exporter
	if mgr, aerr := analytics.Open(ctx, analytics.Config{
		PostgresDSN: cfg.DatabaseURL,
		ParquetDir:  cfg.ParquetDir,
		S3Endpoint:  cfg.S3Endpoint, S3Bucket: cfg.S3Bucket,
		S3AccessKey: cfg.S3AccessKey, S3SecretKey: cfg.S3SecretKey,
		S3Region: cfg.S3Region, S3UseSSL: cfg.S3UseSSL,
	}); aerr != nil {
		slog.Warn("analytics disabled (DuckDB open failed)", "error", aerr)
	} else {
		analyticsMgr = mgr
		exporter = analytics.NewExporter(mgr, pool)
		defer mgr.Close()
	}

	// Partition manager: pre-create daily telemetry partitions, export aged ones
	// to the Parquet cold tier, then drop (retention = export-then-DROP).
	go partition.New(pool, cfg.RetentionDays, exporter).Run(ctx)
	slog.Info("partition manager started", "retention_days", cfg.RetentionDays)

	sessions := auth.NewSessionManager(cfg.SessionLifetime, cfg.SessionIdleTimeout, cfg.IsProduction())
	sessions.Store = pgxstore.New(pool)

	var csrfMW func(http.Handler) http.Handler
	if cfg.DisableCSRF && !cfg.IsProduction() {
		slog.Warn("CSRF protection DISABLED via DISABLE_CSRF (dev only)")
		csrfMW = func(next http.Handler) http.Handler { return next }
	} else {
		// gorilla/csrf enforces an Origin/Referer check on state-changing
		// requests and validates against trusted origins. Trust the BASE_URL
		// host so the same-origin SPA passes.
		//
		// NOTE (GO-2025-3884): gorilla/csrf has an unfixed advisory about
		// TrustedOrigins validation. It is not reachable here: TrustedOrigins is
		// a single static host derived from BASE_URL (never attacker-supplied),
		// and the session cookie is SameSite=Strict, which structurally blocks
		// cross-site cookie-bearing POSTs on its own. Tracked for a future move
		// to a maintained CSRF implementation.
		host := cfg.BaseURL
		if u, err := url.Parse(cfg.BaseURL); err == nil && u.Host != "" {
			host = u.Host
		}
		csrfMW = csrf.Protect(
			[]byte(cfg.CSRFKey),
			csrf.Secure(cfg.IsProduction()),
			csrf.Path("/"),
			csrf.SameSite(csrf.SameSiteStrictMode),
			csrf.HttpOnly(true),
			csrf.TrustedOrigins([]string{host}),
		)
	}

	build, err := fs.Sub(frontendFiles, "frontend/build")
	if err != nil {
		return fmt.Errorf("frontend fs: %w", err)
	}

	srv := api.NewServer(pool, sessions, cfg, analyticsMgr)
	handler := srv.Routes(build, csrfMW)

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown", "error", err)
		}
	}()

	slog.Info("flare listening", "addr", httpServer.Addr, "env", cfg.Environment)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
