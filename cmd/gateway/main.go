// Command gateway serves the venue-neutral exchange API (/v1) over HTTP and
// WebSocket. Its only durable state is the encrypted account file.
//
// Environment:
//
//	GATEWAY_ADDR        listen address            (default 127.0.0.1:8081)
//	GATEWAY_DATA_DIR    state directory           (default ./data/gateway)
//	GATEWAY_TOKEN_FILE  bearer token file, 0600   (default $DATA_DIR/token; created if missing)
//	GATEWAY_KEY_FILE    64-hex master key, 0600   (default $DATA_DIR/master.key; created if missing)
//	GATEWAY_LOG_LEVEL   debug|info|warn|error     (default info)
//
// Outbound HTTP honours HTTPS_PROXY / HTTP_PROXY / NO_PROXY.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/api"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func run() error {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(env("GATEWAY_LOG_LEVEL", "info")))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	dataDir, err := filepath.Abs(env("GATEWAY_DATA_DIR", "data/gateway"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	tokenPath := env("GATEWAY_TOKEN_FILE", filepath.Join(dataDir, "token"))
	keyPath := env("GATEWAY_KEY_FILE", filepath.Join(dataDir, "master.key"))

	token, err := ensureSecretFile(tokenPath, 32)
	if err != nil {
		return fmt.Errorf("gateway token: %w", err)
	}
	if _, err := ensureSecretFile(keyPath, 32); err != nil {
		return fmt.Errorf("master key: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	key, err := secretstore.FileKeySource{Path: keyPath}.Load(ctx)
	if err != nil {
		return fmt.Errorf("master key %s: %w", keyPath, err)
	}
	cipher, err := secretstore.NewCipher(key)
	clear(key)
	if err != nil {
		return err
	}
	accounts, err := store.Open(filepath.Join(dataDir, "accounts.enc"), cipher)
	if err != nil {
		return err
	}

	// A TLS handshake to a venue can cost seconds on some networks. Keep enough
	// warm connections per host that an order never waits for a new one.
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConns = 256
	base.MaxIdleConnsPerHost = 16
	base.IdleConnTimeout = 5 * time.Minute
	transport, err := ratelimit.NewTransport(ratelimit.NewCoordinator(time.Now), base)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Transport: transport, Timeout: 20 * time.Second}

	catalog, err := market.NewCatalog(httpClient)
	if err != nil {
		return err
	}
	defer catalog.Close()
	started := time.Now()
	catalog.Refresh(ctx)
	slog.Info("instrument catalog loaded", "elapsed", time.Since(started).Round(time.Millisecond))
	for _, status := range catalog.Status() {
		if status.Error != "" {
			slog.Warn("market unavailable", "market", status.Market, "error", status.Error)
		}
	}
	go catalog.Run(ctx, 15*time.Minute)

	manager := account.NewManager(accounts, catalog, httpClient)
	manager.Start()
	defer manager.Close()

	feeds := market.NewFeeds(catalog)
	publicHub := hub.New(feeds.Source, 30*time.Second)
	defer publicHub.Close()
	privateHub := hub.New(manager.Source, 30*time.Second)
	defer privateHub.Close()
	go market.Warm(ctx, catalog, publicHub)

	server := &http.Server{
		Addr:              env("GATEWAY_ADDR", "127.0.0.1:8081"),
		Handler:           api.NewServer(token, catalog, manager, feeds, publicHub, privateHub),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		slog.Info("gateway listening", "addr", server.Addr, "accounts", len(manager.List()))
		errs <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// ensureSecretFile returns the trimmed contents of path, creating it with a
// random hex secret of size bytes (mode 0600) when it does not exist.
func ensureSecretFile(path string, size int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr == nil && info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s must not be readable by group or others (chmod 600)", path)
		}
		value := []byte(strings.TrimSpace(string(data)))
		if len(value) < 32 {
			return nil, fmt.Errorf("%s is too short", path)
		}
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	value := []byte(hex.EncodeToString(raw))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, append(value, '\n'), 0o600); err != nil {
		return nil, err
	}
	slog.Info("generated secret file", "path", path)
	return value, nil
}
