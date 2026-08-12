// Command authedge runs the auth edge: the browser's entry point.
//
// It is the only process that terminates a session cookie, and the only one a
// browser talks to. Everything past the four auth routes is forwarded to the
// trading service or the gateway with a service token and an asserted
// principal, so this process holds two credentials that must never be reachable
// from the browser side of the boundary.
//
// The listener binds loopback only. This edge has no TLS of its own; putting it
// on a routable address would publish a plaintext session cookie.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os/signal"
	"syscall"

	authedgeapp "github.com/QuantProcessing/boltertrader/services/authedge/app"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	cfg, err := load()
	if err != nil {
		return err
	}
	defer cfg.clear()

	database, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	defer database.Close()

	app, err := authedgeapp.New(authedgeapp.Config{
		Mode:                  cfg.Mode,
		ListenAddr:            cfg.ListenAddr,
		BootstrapUsername:     cfg.BootstrapUsername,
		BootstrapPasswordHash: cfg.BootstrapPasswordHash,
		TradingURL:            cfg.TradingURL,
		TradingToken:          cfg.TradingToken,
		GatewayURL:            cfg.GatewayURL,
		GatewayToken:          cfg.GatewayToken,
	}, database)
	if err != nil {
		return err
	}
	return app.Start(ctx)
}

func openDatabase(ctx context.Context, cfg config) (*sql.DB, error) {
	// No password is assembled: the connection authenticates by peer or by what
	// the environment already provides, so this process never holds a database
	// credential it could log.
	connectionURL := &url.URL{Scheme: "postgres", User: url.User(cfg.DatabaseRole), Host: cfg.DatabaseHost, Path: cfg.DatabaseName, RawQuery: "sslmode=disable"}
	database, err := sql.Open("pgx", connectionURL.String())
	if err != nil {
		return nil, fmt.Errorf("open auth edge database: %w", err)
	}
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping auth edge database: %w", err)
	}
	return database, nil
}
