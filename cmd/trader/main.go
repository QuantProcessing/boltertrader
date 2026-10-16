// Command trader serves the manual-trading web app and forwards its API calls
// to the gateway after password authentication.
//
//	trader            run the server
//	trader passwd     set a new password (reads it from stdin)
//
// Environment:
//
//	TRADER_ADDR              listen address           (default 127.0.0.1:8080)
//	TRADER_DATA_DIR          state directory          (default ./data/trader)
//	TRADER_WEB_DIR           built web app            (default ./web/dist)
//	TRADER_SECURE_COOKIE     "1" when served via HTTPS
//	GATEWAY_URL              gateway base URL         (default http://127.0.0.1:8081)
//	GATEWAY_TOKEN_FILE       gateway bearer token     (default ./data/gateway/token)
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/QuantProcessing/boltertrader/trader"
)

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	var err error
	if len(os.Args) > 1 && os.Args[1] == "passwd" {
		err = passwd()
	} else {
		err = run()
	}
	if err != nil {
		slog.Error("trader stopped", "error", err)
		os.Exit(1)
	}
}

func passwordPath() string {
	return filepath.Join(env("TRADER_DATA_DIR", "data/trader"), "password")
}

func passwd() error {
	fmt.Fprint(os.Stderr, "New password (min 12 characters): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	password := strings.TrimRight(line, "\r\n")
	if len(password) < 12 {
		return fmt.Errorf("password must be at least 12 characters")
	}
	hash, err := trader.HashPassword(password)
	if err != nil {
		return err
	}
	if err := trader.WritePassword(passwordPath(), hash); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Password updated. Restart trader to sign out existing sessions.")
	return nil
}

func run() error {
	hash, generated, err := trader.LoadOrCreatePassword(passwordPath())
	if err != nil {
		return err
	}
	if generated != "" {
		fmt.Fprintf(os.Stderr, "\n  Generated trader password (shown once): %s\n  Change it with: trader passwd\n\n", generated)
	}
	gatewayURL, err := url.Parse(env("GATEWAY_URL", "http://127.0.0.1:8081"))
	if err != nil || gatewayURL.Host == "" {
		return fmt.Errorf("GATEWAY_URL is invalid")
	}
	tokenPath := env("GATEWAY_TOKEN_FILE", "data/gateway/token")
	token, err := waitForToken(tokenPath, 30*time.Second)
	if err != nil {
		return err
	}
	webDir := env("TRADER_WEB_DIR", "web/dist")
	if _, err := os.Stat(filepath.Join(webDir, "index.html")); err != nil {
		slog.Warn("web app not found; serving API only", "dir", webDir)
		webDir = ""
	}
	server := &http.Server{
		Addr: env("TRADER_ADDR", "127.0.0.1:8080"),
		Handler: trader.NewServer(trader.Config{
			GatewayURL: gatewayURL, GatewayToken: token, Password: hash, WebDir: webDir,
			SecureCookie: env("TRADER_SECURE_COOKIE", "") == "1",
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 1)
	go func() {
		slog.Info("trader listening", "addr", server.Addr, "gateway", gatewayURL.String())
		errs <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// waitForToken tolerates the gateway creating its token on first start.
func waitForToken(path string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if token := strings.TrimSpace(string(data)); len(token) >= 32 {
				return token, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("gateway token %s is unavailable: %v", path, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
