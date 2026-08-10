package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/app"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
)

var (
	version = "dev"
	build   = "local"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadFromEnv()
	if err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if cfg.StartupMode == config.StartupMigrate {
		report, err := app.RunCutover(ctx, cfg, logger)
		if err != nil {
			logger.Error("cutover failed", "errorType", errorType(err))
			os.Exit(1)
		}
		logger.Info(
			"cutover complete",
			"schemaVersion", report.SchemaVersion,
			"sessionsRevoked", report.SessionsRevoked,
			"derivedRowsCleared", report.DerivedRowsCleared,
		)
		return
	}

	var application *app.App
	switch cfg.StartupMode {
	case config.StartupServe:
		application, err = app.New(ctx, cfg, version, build, logger)
	case config.StartupMaintenance:
		application, err = app.NewMaintenance(cfg, version, build, logger)
	default:
		err = app.ErrStartupMode
	}
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting", "config", cfg.String())
		errCh <- application.Server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := application.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}
