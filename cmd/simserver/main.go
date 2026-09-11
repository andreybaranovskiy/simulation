// Command simserver is the platform's HTTP server: REST API, authentication,
// artifact streaming, report export and the built single-page app.
//
// It is designed to sit behind IIS as a reverse proxy on Windows Server, and
// to run standalone during development. See deploy/iis for the hosting setup.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/api"
	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/blobstore"
	"github.com/andreybaranovskiy/simulation/internal/config"
	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=1.0.0"
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "simserver:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", defaultConfigPath(), "path to config.yaml")
		migrateOnly = flag.Bool("migrate", false, "apply database migrations and exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	log, closeLog, err := newLogger(cfg.Log)
	if err != nil {
		return err
	}
	defer closeLog()

	api.Version = version
	log.Info("starting simserver", "version", version, "config", *configPath)

	// Signals cancel the root context, which unwinds startup as well as the
	// running server, so Ctrl+C during a slow migration still exits cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer database.Close()
	log.Info("connected to mysql", "host", cfg.Database.Host, "database", cfg.Database.Name)

	if cfg.Database.AutoMigrate || *migrateOnly {
		if err := database.Migrate(ctx, log); err != nil {
			return err
		}
	}
	if *migrateOnly {
		log.Info("migrations applied, exiting as requested")
		return nil
	}

	if err := os.MkdirAll(cfg.Storage.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	blobs, err := blobstore.New(cfg.Storage.DataDir)
	if err != nil {
		return err
	}

	st := store.New(database)
	authSvc := auth.NewService(st, cfg.Auth, log)

	if err := authSvc.BootstrapAdmin(ctx); err != nil {
		return err
	}
	warnIfNoUsers(ctx, st, cfg, log)

	srv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      api.NewServer(cfg, st, authSvc, blobs, log).Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	go purgeSessions(ctx, authSvc, log)

	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Server.Addr, "base_url", cfg.Server.BaseURL,
			"data_dir", cfg.Storage.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("listen on %s: %w", cfg.Server.Addr, err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Shutdown gets its own context: the root one is already cancelled, and
	// in-flight requests need a grace period to finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}

// purgeSessions removes expired session rows hourly. Resolve already ignores
// them, so this only keeps the table from growing without bound.
func purgeSessions(ctx context.Context, authSvc *auth.Service, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := authSvc.PurgeExpiredSessions(ctx)
			if err != nil {
				log.Warn("could not purge expired sessions", "error", err)
				continue
			}
			if n > 0 {
				log.Debug("purged expired sessions", "count", n)
			}
		}
	}
}

// warnIfNoUsers points the operator at the way in when the instance is empty
// and self-registration is closed, which would otherwise lock everyone out.
func warnIfNoUsers(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) {
	n, err := st.Users.Count(ctx)
	if err != nil || n > 0 {
		return
	}
	if cfg.Auth.AllowRegistration {
		log.Info("no accounts yet: the first account created will be an administrator")
		return
	}
	log.Warn("no accounts exist and registration is disabled",
		"fix", "set auth.bootstrap_admin_email and auth.bootstrap_admin_password, then restart")
}

func defaultConfigPath() string {
	if env := os.Getenv(config.EnvPrefix + "CONFIG"); env != "" {
		return env
	}
	// Prefer a config next to the executable, which is where a Windows service
	// install puts it, and fall back to the working directory for development.
	if exe, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(exe), "config.yaml")
		if _, err := os.Stat(beside); err == nil {
			return beside
		}
	}
	return "config.yaml"
}

func newLogger(cfg config.Log) (*slog.Logger, func(), error) {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	out := os.Stdout
	closeFn := func() {}

	if cfg.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0o750); err != nil {
			return nil, nil, fmt.Errorf("create log directory: %w", err)
		}
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}
		out = f
		closeFn = func() { f.Close() }
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.EqualFold(cfg.Format, "json") {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}

	return slog.New(handler), closeFn, nil
}
