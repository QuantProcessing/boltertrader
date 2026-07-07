package perp

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

// Config configures a live Binance USD-M perpetual adapter.
type Config struct {
	APIKey    string
	APISecret string
	// AccountID is the logical runtime account id. Product scope (USD-M perp)
	// remains modeled separately in AccountState.
	AccountID string
	// Environment selects Binance production or Demo endpoints. Zero value is
	// LIVE; Demo is retained below as a compatibility shortcut.
	Environment sdkperp.Environment
	// Demo switches REST, public/market WS, account WS, and SDK API clients
	// to Binance USD-M Futures Demo endpoints.
	Demo          bool
	DemoAPIKey    string
	DemoAPISecret string
	// HTTPClient overrides the SDK REST HTTP client; tests use it to keep
	// ExchangeInfo loading offline.
	HTTPClient *http.Client
	// Clock is the time source; defaults to a RealClock when nil.
	Clock clock.Clock
}

// Adapter bundles the three venue-neutral clients for Binance USD-M perps,
// sharing one REST client and a single resolved instrument registry.
type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instrumentProvider

	// concrete handles for wiring the user-data stream
	rest *sdkperp.Client
	exec *executionClient
	acct *accountClient
	clk  clock.Clock

	wsAccount *sdkperp.WsAccountClient
	apiKey    string
	apiSecret string
	profile   sdkperp.EndpointProfile
}

// New constructs a live Binance perp adapter: it builds the REST and WebSocket
// clients, loads the instrument registry via ExchangeInfo, and returns the
// three contract clients ready to use. Call Start to begin the private
// user-data stream (order/fill/balance/position pushes).
func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}

	env := cfg.Environment
	if env == "" && cfg.Demo {
		env = sdkperp.EnvironmentDemo
	}
	profile, err := sdkperp.EndpointProfileForEnvironment(env)
	if err != nil {
		return nil, err
	}
	apiKey, apiSecret := cfg.APIKey, cfg.APISecret
	if sdkperp.DefaultEnvironment(env) == sdkperp.EnvironmentDemo {
		apiKey, apiSecret = cfg.DemoAPIKey, cfg.DemoAPISecret
	}
	rest := sdkperp.NewClient().WithEndpointProfile(profile)
	rest.WithCredentials(apiKey, apiSecret)
	if cfg.HTTPClient != nil {
		rest.WithHTTPClient(cfg.HTTPClient)
	}
	accountID := cfg.AccountID
	if accountID == "" {
		accountID = model.AccountIDBinanceDefault
	}

	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest); err != nil {
		return nil, err
	}

	wsMarket := sdkperp.NewWsMarketClientWithEndpointProfile(ctx, profile)

	exec := newExecutionClient(rest, provider, clk, accountID)
	acct := newAccountClient(rest, provider, clk, accountID)
	market := newMarketDataClient(rest, wsMarket, provider, clk)

	return &Adapter{
		Market:    market,
		Execution: exec,
		Account:   acct,
		provider:  provider,
		rest:      rest,
		exec:      exec,
		acct:      acct,
		clk:       clk,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		profile:   profile,
	}, nil
}

// Start opens the Binance user-data WebSocket and routes its order and account
// pushes into the execution and account event streams. It is safe to call once;
// the underlying client manages listenKey keepalive and reconnect internally.
func (a *Adapter) Start(ctx context.Context) error {
	ws := a.newWsAccountClient(ctx)
	resolve := a.provider.resolveVenueSymbol

	ws.SubscribeOrderUpdate(func(ev *sdkperp.OrderUpdateEvent) {
		for _, e := range execEventsFromOrderUpdate(ev, resolve, a.exec.accountID) {
			a.exec.emit(e)
		}
	})
	ws.SubscribeAccountUpdate(func(ev *sdkperp.AccountUpdateEvent) {
		for _, e := range accountEventsFromUpdate(ev, resolve, a.acct.accountID) {
			a.acct.emit(e)
		}
	})

	if err := ws.Connect(); err != nil {
		return err
	}
	a.wsAccount = ws
	return nil
}

func (a *Adapter) newWsAccountClient(ctx context.Context) *sdkperp.WsAccountClient {
	return sdkperp.NewWsAccountClientWithEndpointProfile(ctx, a.apiKey, a.apiSecret, a.profile)
}

// Close shuts down the adapter's streams.
func (a *Adapter) Close() error {
	if a.wsAccount != nil {
		a.wsAccount.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
