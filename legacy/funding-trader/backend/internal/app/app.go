package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionmonitor"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionpreview"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionruntime"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/httpapi"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerreconcile"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgersync"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgersyncadapter"
	"github.com/QuantProcessing/funding-trader/backend/internal/maintenance"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/QuantProcessing/funding-trader/backend/internal/opportunityengine"
	"github.com/QuantProcessing/funding-trader/backend/internal/positioneconomics"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/store/postgres"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	Config             config.Config
	Pool               *pgxpool.Pool
	Server             *http.Server
	GatewayClient      *boltertrader.GatewayClient
	MarketData         *marketdata.Provider
	ReadModel          *postgres.ReadModelStore
	PositionEconomics  *positioneconomics.Service
	Stream             *uistream.Service
	OpportunityEngine  *opportunityengine.Engine
	ExecutionStore     *postgres.ExecutionStore
	ExecutionProjector *executionprojection.Projector
	ExecutionService   *executionservice.Service
	ExecutionMonitor   *executionmonitor.Monitor
	LedgerFacts        *ledgerfacts.PostgresStore
	LedgerProjection   *ledgerprojection.PostgresStore
	LedgerSync         *ledgersync.Service
	LedgerReconciler   *ledgerreconcile.Service
	LedgerCoordinator  *LedgerReconciliationCoordinator
	LedgerQuery        *httpapi.LedgerQueryService
	MaintenanceGate    *maintenance.Gate

	runtimeCancel context.CancelFunc
	engineDone    <-chan error
	monitorDone   <-chan error
}

var (
	ErrStartupMode            = errors.New("startup mode does not match application entrypoint")
	ErrServeMigrationRequired = postgres.ErrProductModeMigrationRequired
)

func New(ctx context.Context, cfg config.Config, version, build string, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.StartupMode != config.StartupServe {
		return nil, ErrStartupMode
	}
	pool, err := postgres.OpenApplicationPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	if err := postgres.NewMigrationRunner(pool).Up(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	bootID, readCursorKey, streamCursorKey, err := newRuntimeIdentity()
	if err != nil {
		pool.Close()
		return nil, err
	}
	store := postgres.NewStore(pool, postgres.StoreOptions{
		BootID: bootID, Mode: string(cfg.Mode),
	})
	authService := auth.NewService(store, string(cfg.Mode), cfg.SessionTTL, cfg.CookieSecure)
	if err := authService.Bootstrap(ctx, cfg.BootstrapEmail, cfg.BootstrapPassword); err != nil {
		pool.Close()
		return nil, err
	}
	gatewayClient, err := boltertrader.NewGatewayClient(boltertrader.GatewayClientOptions{
		BaseURL: cfg.GatewayBaseURL, Token: cfg.GatewayToken, Mode: cfg.Mode,
		ProductionWritesEnabled: cfg.ProductionWritesEnabled,
	})
	if err != nil {
		pool.Close()
		return nil, err
	}
	publicMarketClient, err := boltertrader.NewPublicMarketClient(boltertrader.PublicMarketClientOptions{
		BaseURL: cfg.GatewayBaseURL, Token: cfg.GatewayToken, Mode: cfg.Mode,
	})
	if err != nil {
		pool.Close()
		return nil, err
	}
	marketData, err := boltertrader.NewMarketDataProvider(gatewayClient, publicMarketClient, cfg.Mode)
	if err != nil {
		pool.Close()
		return nil, err
	}
	readModelStore, err := postgres.NewReadModelStore(pool, readCursorKey)
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	positionEconomics, err := positioneconomics.New(positioneconomics.ServiceOptions{
		Reader: readModelStore, Gateway: gatewayClient, ReportingAsset: "USDT",
	})
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	principalScope := readmodel.Scope{
		PrincipalID: auth.DefaultPrincipalID,
		Mode:        string(cfg.Mode),
	}
	metricsReader, err := postgres.NewMetricsReader(pool)
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	eventStore, err := postgres.NewUIStreamEventStore(readModelStore, bootID, principalScope)
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	stream, err := uistream.NewService(uistream.ServiceConfig{
		BootID:         bootID,
		PrincipalID:    principalScope.PrincipalID,
		Mode:           principalScope.Mode,
		SigningKey:     streamCursorKey,
		Store:          eventStore,
		SnapshotSource: readModelSnapshotSource{reader: positionEconomics, scope: principalScope},
	})
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	ledgerCursorCodec, err := readmodel.NewCursorCodec(readCursorKey)
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	ledgerFacts := ledgerfacts.NewPostgresStore(pool)
	ledgerProjection := ledgerprojection.NewPostgresStore(pool)
	ledgerPublisher, err := ledgersyncadapter.NewPublisher(ledgersyncadapter.PublisherConfig{
		Stream:      stream,
		PrincipalID: principalScope.PrincipalID,
		Mode:        principalScope.Mode,
	})
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	ledgerResolver, err := ledgersyncadapter.NewFundingFactResolver(ledgerFacts)
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	ledgerSync := ledgersync.NewService(gatewayClient, ledgerFacts, ledgersync.ServiceOptions{
		Publisher: ledgerPublisher,
		Resolver:  ledgerResolver,
	})
	ledgerReconciler := ledgerreconcile.NewService(
		ledgerSync,
		ledgerfacts.NewJournal(ledgerFacts),
		ledgerProjection,
	)
	ledgerCoordinator := NewLedgerReconciliationCoordinator(
		ledgerReconciler,
		stream,
		principalScope.PrincipalID,
		principalScope.Mode,
	)
	store.SetEventBroadcaster(stream)
	engine, err := opportunityengine.New(opportunityengine.Config{
		Mode:          cfg.Mode,
		BootID:        bootID,
		Source:        marketData,
		Store:         readModelStore,
		Publisher:     stream,
		MarketSymbols: cfg.MarketSymbols,
	})
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	executionStore := postgres.NewExecutionStore(pool, bootID)
	projectionStore := executionruntime.NewProjectionStore(
		executionStore, readModelStore, stream, bootID,
	)
	executionProjector := executionprojection.NewProjector(projectionStore, bootID)
	accountDiscovery, discoveryErr := gatewayClient.DiscoverAccounts(ctx)
	openAccountIDs := make([]string, 0)
	if discoveryErr == nil {
		writeAcceptance := boltertrader.EvaluateTwoLegWriteAcceptance(accountDiscovery)
		openAccountIDs = append(openAccountIDs, writeAcceptance.CompatibleAccountIDs...)
	}
	previewSource, err := executionpreview.NewSource(executionpreview.SourceOptions{
		Reader: readModelStore, Refresher: engine,
		Gateway: gatewayClient, Market: marketData,
	})
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	executionPublisher := executionruntime.NewProjectingPublisher(
		executionruntime.NewPublisher(stream),
		projectionStore,
	)
	executionService, err := executionservice.New(executionservice.Options{
		Store:            executionruntime.NewStoreAdapter(executionStore),
		Source:           previewSource,
		Gateway:          executionruntime.NewGatewayAdapter(gatewayClient),
		Publisher:        executionPublisher,
		RecoveryRepairer: executionPublisher,
		Ready:            false,
	})
	if err != nil {
		_ = marketData.Close()
		pool.Close()
		return nil, err
	}
	if _, err := executionService.Recover(ctx, executionservice.Scope{
		PrincipalID: principalScope.PrincipalID,
		Mode:        principalScope.Mode,
	}, "startup-"+bootID); err != nil {
		logger.Error("execution_recovery_failed", "errorType", errorType(err))
	} else {
		result, ledgerErr := RecoverClosedPositionLedger(
			ctx,
			executionStore,
			principalScope,
			ledgerCoordinator,
			"USDT",
		)
		if ledgerErr != nil {
			logger.Error("ledger_recovery_failed", "errorType", errorType(ledgerErr))
		} else if result.Incomplete > 0 {
			logger.Warn("ledger_recovery_incomplete", "eligible", result.Eligible, "reconciled", result.Reconciled, "incomplete", result.Incomplete)
		} else if result.Eligible > 0 {
			logger.Info("ledger_recovery_completed", "eligible", result.Eligible, "reconciled", result.Reconciled)
		}
	}
	var executionMonitor *executionmonitor.Monitor
	if discoveryErr != nil {
		logger.Error("private_event_account_discovery_failed", "errorType", errorType(discoveryErr))
	} else {
		accountIDs := append([]string(nil), openAccountIDs...)
		if len(accountIDs) == 0 {
			logger.Error("private_event_accounts_unavailable")
		} else {
			executionReconciler := executionruntime.NewExecutionReconciler(
				executionService,
				executionservice.Scope{
					PrincipalID: principalScope.PrincipalID,
					Mode:        principalScope.Mode,
				},
				bootID,
			)
			executionMonitor = executionmonitor.New(executionmonitor.Options{
				Gateway: executionruntime.NewPrivateEventGateway(gatewayClient),
				Reconciler: LedgerRecoveryReconciler{
					Execution:      executionReconciler,
					Source:         executionStore,
					Scope:          principalScope,
					Ledger:         ledgerCoordinator,
					ReportingAsset: "USDT",
					OnLedgerError: func(err error) {
						logger.Error("ledger_recovery_failed", "errorType", errorType(err))
					},
				},
				ResnapshotPublisher: executionruntime.NewPrivateEventResnapshotPublisher(stream),
				Accounts:            accountIDs,
			})
		}
	}
	twoLegAcceptance := gatewayTwoLegAcceptance{discoverer: gatewayClient}
	tradingReadiness := tradingReadinessChecker{
		mode:                    cfg.Mode,
		principal:               principalScope.PrincipalID,
		productionWritesEnabled: cfg.ProductionWritesEnabled,
		execution:               executionService,
		continuity:              executionMonitor,
		executions:              executionStore,
		discoverer:              gatewayClient,
	}
	ledgerQuery := httpapi.NewLedgerQueryService(httpapi.LedgerQueryServiceConfig{
		Funding:           ledgerFacts,
		Projection:        ledgerProjection,
		PositionReader:    readModelStore,
		PositionEconomics: positionEconomics,
		TradingReadiness:  tradingReadiness,
		ReportingAsset:    "USDT",
		CursorCodec:       ledgerCursorCodec,
	})
	maintenanceGate := maintenance.NewGate()
	readinessChecker := readiness.WithDatabasePingerAndComponents(pool, map[string]readiness.ComponentCheck{
		"maintenance": func(context.Context) readiness.Component {
			state := maintenanceGate.Snapshot()
			if state.Enabled {
				return readiness.Component{Ready: false, Reason: state.Reason}
			}
			return readiness.Component{Ready: true}
		},
		"gateway":    boltertrader.GatewayComponent(gatewayClient),
		"accounts":   boltertrader.AccountsComponent(gatewayClient),
		"publicData": boltertrader.PublicDataComponent(marketData),
		"execution": func(ctx context.Context) readiness.Component {
			status := executionService.Readiness()
			privateEvents := executionmonitor.Readiness{Reason: "private-events-not-configured"}
			if executionMonitor != nil {
				privateEvents = executionMonitor.Readiness()
			}
			ready := status.Ready && privateEvents.Ready
			reason := status.Reason
			if status.Ready && !privateEvents.Ready {
				reason = privateEvents.Reason
			}
			writeAcceptance := twoLegAcceptance.TwoLegWriteAcceptance(ctx)
			return readiness.Component{
				Ready:  ready,
				Reason: reason,
				Details: map[string]string{
					"recovering":         boolString(status.Recovering),
					"degraded":           boolString(status.Degraded),
					"privateEventsReady": boolString(privateEvents.Ready),
					"twoLegWriteReady":   boolString(writeAcceptance.Ready),
					"twoLegWriteReason":  writeAcceptance.Reason,
				},
			}
		},
		"twoLegWrite": func(ctx context.Context) readiness.Component {
			return twoLegAcceptance.TwoLegWriteAcceptance(ctx).ReadinessComponent()
		},
	})
	handler := httpapi.New(httpapi.ServerConfig{
		Mode:            string(cfg.Mode),
		Version:         version,
		Build:           build,
		MaintenanceGate: maintenanceGate,
		Auth:            authService,
		ReadModel:       positionEconomics,
		Ledger:          ledgerQuery,
		Execution: ledgerExecutionAPI{
			inner: monitoredExecutionAPI{
				service: executionService,
				trading: tradingReadiness,
			},
			reconciler:     ledgerCoordinator,
			reportingAsset: "USDT",
		},
		Metrics:          metricsReader,
		Stream:           stream,
		Readiness:        readinessChecker,
		TradingReadiness: tradingReadiness,
		Logger:           logger,
	})
	runtimeContext, runtimeCancel := context.WithCancel(context.Background())
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return runtimeContext
		},
	}
	engineDone := make(chan error, 1)
	go func() {
		runErr := engine.Run(runtimeContext)
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			logger.Error("opportunity_engine_stopped", "error", runErr)
		}
		engineDone <- runErr
		close(engineDone)
	}()
	var monitorDone <-chan error
	if executionMonitor != nil {
		done := make(chan error, 1)
		monitorDone = done
		go func() {
			runErr := executionMonitor.Run(runtimeContext)
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				logger.Error("private_event_monitor_stopped", "errorType", errorType(runErr))
			}
			done <- runErr
			close(done)
		}()
	}
	return &App{
		Config: cfg, Pool: pool, Server: server,
		GatewayClient: gatewayClient, MarketData: marketData,
		ReadModel: readModelStore, PositionEconomics: positionEconomics,
		Stream: stream, OpportunityEngine: engine,
		ExecutionStore: executionStore, ExecutionProjector: executionProjector,
		ExecutionService: executionService, ExecutionMonitor: executionMonitor,
		LedgerFacts: ledgerFacts, LedgerProjection: ledgerProjection,
		LedgerSync: ledgerSync, LedgerReconciler: ledgerReconciler,
		LedgerCoordinator: ledgerCoordinator, LedgerQuery: ledgerQuery,
		MaintenanceGate: maintenanceGate,
		runtimeCancel:   runtimeCancel, engineDone: engineDone, monitorDone: monitorDone,
	}, nil
}

func NewMaintenance(cfg config.Config, version, build string, logger *slog.Logger) (*App, error) {
	if cfg.StartupMode != config.StartupMaintenance {
		return nil, ErrStartupMode
	}
	if logger == nil {
		logger = slog.Default()
	}
	handler := httpapi.NewMaintenance(httpapi.MaintenanceConfig{
		Mode:    string(cfg.Mode),
		Version: version,
		Build:   build,
		Reason:  cfg.MaintenanceReason,
		Logger:  logger,
	})
	return &App{
		Config: cfg,
		Server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}, nil
}

func RunCutover(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
) (postgres.CutoverReport, error) {
	if cfg.StartupMode != config.StartupMigrate {
		return postgres.CutoverReport{}, ErrStartupMode
	}
	if logger == nil {
		logger = slog.Default()
	}
	pool, err := postgres.OpenCutoverPool(ctx, cfg.CutoverDatabaseURL)
	if err != nil {
		return postgres.CutoverReport{}, err
	}
	defer pool.Close()
	report, err := postgres.NewCutoverRunner(pool, cfg.CutoverProof).Run(ctx)
	if err != nil {
		return postgres.CutoverReport{}, err
	}
	logger.Info(
		"product_mode_cutover_validated",
		"schemaVersion", report.SchemaVersion,
		"sessionsRevoked", report.SessionsRevoked,
		"derivedRowsCleared", report.DerivedRowsCleared,
		"duration", report.Duration.String(),
	)
	return report, nil
}

func (a *App) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var shutdownErrors []error
	if a.MaintenanceGate != nil {
		a.MaintenanceGate.Enter("process_shutdown")
		if _, err := a.MaintenanceGate.Drain(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if a.runtimeCancel != nil {
		a.runtimeCancel()
	}
	if a.Server != nil {
		if err := a.Server.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if a.engineDone != nil {
		select {
		case err := <-a.engineDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				shutdownErrors = append(shutdownErrors, err)
			}
		case <-ctx.Done():
			shutdownErrors = append(shutdownErrors, ctx.Err())
		}
	}
	if a.monitorDone != nil {
		select {
		case err := <-a.monitorDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				shutdownErrors = append(shutdownErrors, err)
			}
		case <-ctx.Done():
			shutdownErrors = append(shutdownErrors, ctx.Err())
		}
	}
	if a.MarketData != nil {
		if err := a.MarketData.Close(); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if a.Pool != nil {
		a.Pool.Close()
	}
	return errors.Join(shutdownErrors...)
}

type readModelSnapshotSource struct {
	reader readmodel.Reader
	scope  readmodel.Scope
}

func (source readModelSnapshotSource) Snapshot(
	ctx context.Context,
	request uistream.Request,
) (uistream.Snapshot, error) {
	kinds := make([]readmodel.EventKind, 0, len(request.Kinds))
	for _, kind := range request.Kinds {
		kinds = append(kinds, readmodel.EventKind(kind))
	}
	snapshot, err := source.reader.Snapshot(ctx, source.scope, kinds)
	if err != nil {
		return uistream.Snapshot{}, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return uistream.Snapshot{}, err
	}
	return uistream.Snapshot{Scope: request.Scope, Data: payload}, nil
}

func newRuntimeIdentity() (string, []byte, []byte, error) {
	var bootID [16]byte
	if _, err := rand.Read(bootID[:]); err != nil {
		return "", nil, nil, err
	}
	readCursorKey := make([]byte, 32)
	if _, err := rand.Read(readCursorKey); err != nil {
		return "", nil, nil, err
	}
	streamCursorKey := make([]byte, 32)
	if _, err := rand.Read(streamCursorKey); err != nil {
		return "", nil, nil, err
	}
	return hex.EncodeToString(bootID[:]), readCursorKey, streamCursorKey, nil
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
