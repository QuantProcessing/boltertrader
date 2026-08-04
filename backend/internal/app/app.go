package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
	"github.com/QuantProcessing/boltertrader/backend/internal/workapi"
	backendcontract "github.com/QuantProcessing/boltertrader/internal/productplatformcontract"
)

type App struct {
	cfg          config.Config
	contract     backendcontract.Contract
	browser      *http.Server
	work         *http.Server
	browserLn    net.Listener
	workLn       net.Listener
	startOnce    sync.Once
	shutdownOnce sync.Once
}

func New(cfg config.Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	contract, err := loadContract()
	if err != nil {
		return nil, err
	}
	sessionService, err := session.New(session.Options{
		Username:     cfg.BootstrapUsername,
		PasswordHash: cfg.BootstrapPasswordHash,
		Mode:         cfg.Mode,
		TTL:          24 * time.Hour,
	})
	if err != nil {
		return nil, err
	}
	browserHandler, err := httpapi.New(httpapi.Options{Sessions: sessionService, Mode: cfg.Mode, ContractVersion: httpapi.ContractVersion})
	if err != nil {
		return nil, err
	}
	workHandler, err := workapi.New(workapi.Options{TokenDigest: cfg.FundingWorkerTokenDigest, Mode: cfg.Mode, ContractVersion: workapi.ContractVersion})
	if err != nil {
		return nil, err
	}
	return &App{
		cfg:      cfg,
		contract: contract,
		browser:  &http.Server{Addr: cfg.BrowserListenAddr, Handler: browserHandler},
		work:     &http.Server{Addr: cfg.WorkListenAddr, Handler: workHandler},
	}, nil
}

func (a *App) Start(ctx context.Context) error {
	var err error
	a.startOnce.Do(func() {
		a.browserLn, err = net.Listen("tcp", a.cfg.BrowserListenAddr)
		if err != nil {
			return
		}
		a.workLn, err = net.Listen("tcp", a.cfg.WorkListenAddr)
		if err != nil {
			_ = a.browserLn.Close()
			return
		}
		go func() { _ = a.browser.Serve(a.browserLn) }()
		go func() { _ = a.work.Serve(a.workLn) }()
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	return a.Shutdown(context.Background())
}

func (a *App) Shutdown(ctx context.Context) error {
	var first error
	a.shutdownOnce.Do(func() {
		if a.work != nil {
			if err := a.work.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		if a.browser != nil {
			if err := a.browser.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		if a.workLn != nil {
			_ = a.workLn.Close()
		}
		if a.browserLn != nil {
			_ = a.browserLn.Close()
		}
	})
	return first
}

func Run(ctx context.Context, cfg config.Config) error {
	app, err := New(cfg)
	if err != nil {
		return err
	}
	return app.Start(ctx)
}

func loadContract() (backendcontract.Contract, error) {
	contract, err := frozencontract.Load()
	if err != nil {
		return backendcontract.Contract{}, fmt.Errorf("load frozen contract: %w", err)
	}
	return contract, nil
}
