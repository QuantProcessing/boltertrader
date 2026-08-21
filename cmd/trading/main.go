// Command trading runs the trading service as its own process.
//
// The same assembly serves here and inside the auth edge — services/trading/app
// builds it either way. What differs is only how the two questions at the edge
// get answered: identity arrives as a service token plus an asserted principal
// rather than a session cookie, and the durable state is trading's own database
// rather than the platform's.
//
// The listener binds loopback unless BT_TRADING_ALLOW_NON_LOOPBACK_LISTEN says
// otherwise. Nothing here authenticates a browser, so a routable address would
// publish the trading surface to anyone who could reach the host. The one
// deployment that needs the opt-in is a container, where the port is reachable
// only through a mapping the host controls and the process must bind the
// interface its runtime routes to rather than its own namespace's loopback.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/signal"
	"syscall"
	"time"

	tradingapp "github.com/QuantProcessing/boltertrader/services/trading/app"
	tradinggateway "github.com/QuantProcessing/boltertrader/services/trading/gateway"
	"github.com/QuantProcessing/boltertrader/services/trading/servicetoken"
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

	database, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	defer database.Close()

	gatewayClient, err := tradinggateway.NewClient(cfg.GatewayURL, cfg.AllowNonLoopbackUpstream, cfg.GatewayToken, cfg.LivePrivateWritesEnabled, nil,
		tradinggateway.WithAuthorityPlane(cfg.GatewayInternalURL, cfg.AllowNonLoopbackUpstream, cfg.GatewayInternalToken))
	if err != nil {
		return err
	}
	authenticator, err := servicetoken.New(cfg.ServiceToken)
	if err != nil {
		return err
	}

	service, err := tradingapp.New(tradingapp.Config{
		LivePrivateWritesEnabled: cfg.LivePrivateWritesEnabled,
		ManualPreviewSigningKey:  cfg.ManualPreviewSigningKey,
	}, tradingapp.Dependencies{
		Database:              database,
		CatalogSource:         gatewayClient,
		MarketDataSource:      gatewayClient.MarketData(),
		FreshMarketData:       gatewayClient.FreshMarketData(),
		CandleSource:          gatewayClient.Candles(),
		FundingBoardStream:    gatewayClient.FundingBoard(),
		FundingHistorySource:  gatewayClient.FundingHistory(),
		MarketDataProbe:       gatewayClient,
		RuntimeProbe:          gatewayClient,
		RuntimeProjection:     gatewayClient,
		BalanceProjection:     gatewayClient,
		PositionProjection:    gatewayClient,
		ManualGateway:         gatewayClient,
		ManualAccounts:        gatewayClient.Accounts(),
		ReconciliationGateway: gatewayClient,
		Principal:             authenticator.Principal,
		Authenticate:          authenticator.Authenticate,
		AuthorizeMutation:     authenticator.AuthorizeMutation,
	})
	if err != nil {
		return err
	}
	cfg.ManualPreviewSigningKey = nil

	// The assembly does not report "database" — which database has to be up is a
	// fact about the deployment. Here it is the one just opened and pinged.
	service.Readiness().Update("database", readyComponent())

	mux := http.NewServeMux()
	if _, err := service.Register(mux); err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           authenticator.Wrap(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return serve(ctx, server, service)
}

func serve(ctx context.Context, server *http.Server, service *tradingapp.Service) error {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on trading address: %w", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	reconcileCtx, stopReconcile := context.WithCancel(ctx)
	defer stopReconcile()
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		service.Run(reconcileCtx)
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			// The reconciler outlives a dead listener otherwise, and would keep
			// issuing Gateway writes for a service nobody can reach or stop.
			stopReconcile()
			<-reconcileDone
			return err
		}
	case <-ctx.Done():
	}

	stopReconcile()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	select {
	case <-reconcileDone:
	case <-shutdownCtx.Done():
	}
	return shutdownErr
}

func openDatabase(ctx context.Context, cfg config) (*sql.DB, error) {
	// No password is assembled: the connection authenticates by peer or by what
	// the environment already provides, so this process never holds a database
	// credential it could log.
	connectionURL := &url.URL{Scheme: "postgres", User: url.User(cfg.DatabaseRole), Host: cfg.DatabaseHost, Path: cfg.DatabaseName, RawQuery: "sslmode=disable"}
	database, err := sql.Open("pgx", connectionURL.String())
	if err != nil {
		return nil, fmt.Errorf("open trading database: %w", err)
	}
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping trading database: %w", err)
	}
	return database, nil
}
