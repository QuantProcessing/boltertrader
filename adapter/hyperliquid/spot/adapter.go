package spot

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

type Config struct {
	PrivateKey     string
	AccountAddress string
	VaultAddress   string
	Environment    sdk.Environment
	RESTBaseURL    string
	WSURL          string
	HTTPClient     *http.Client
	Clock          clock.Clock
}

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instruments.Registry
	rest     *sdkspot.Client
	ws       *sdkspot.WebsocketClient
	exec     *executionClient
	acct     *accountClient
	clk      clock.Clock
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}

	base := sdk.NewClient().WithEnvironment(cfg.Environment)
	if cfg.PrivateKey != "" || cfg.VaultAddress != "" {
		vault := cfg.VaultAddress
		base.WithCredentials(cfg.PrivateKey, &vault)
	}
	if cfg.AccountAddress != "" {
		base.WithAccount(cfg.AccountAddress)
	}
	if cfg.RESTBaseURL != "" {
		base.BaseURL = cfg.RESTBaseURL
	}
	if cfg.HTTPClient != nil {
		base.Http = cfg.HTTPClient
	}
	rest := sdkspot.NewClient(base)

	meta, err := rest.GetSpotMeta(ctx)
	if err != nil {
		return nil, err
	}
	insts, err := instruments.BuildSpotInstruments(meta)
	if err != nil {
		return nil, err
	}
	provider := instruments.NewRegistry(insts...)

	wsBase := sdk.NewWebsocketClient(ctx).WithEnvironment(cfg.Environment)
	if cfg.WSURL != "" {
		wsBase.WithURL(cfg.WSURL)
	}
	ws := sdkspot.NewWebsocketClient(wsBase)

	exec := newExecutionClient(rest, provider, clk)
	acct := newAccountClient(rest, clk)
	market := newMarketDataClient(rest, provider, clk)

	return &Adapter{
		Market:    market,
		Execution: exec,
		Account:   acct,
		provider:  provider,
		rest:      rest,
		ws:        ws,
		exec:      exec,
		acct:      acct,
		clk:       clk,
	}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	return nil
}

func (a *Adapter) Close() error {
	if a.ws != nil {
		a.ws.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
