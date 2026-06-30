package perp

import (
	"context"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

// Config configures a live Binance USD-M perpetual adapter.
type Config struct {
	APIKey    string
	APISecret string
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

	rest := sdkperp.NewClient().WithCredentials(cfg.APIKey, cfg.APISecret)

	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest); err != nil {
		return nil, err
	}

	wsMarket := sdkperp.NewWsMarketClient(ctx)

	exec := newExecutionClient(rest, provider, clk)
	acct := newAccountClient(rest, provider, clk)
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
		apiKey:    cfg.APIKey,
		apiSecret: cfg.APISecret,
	}, nil
}

// Start opens the Binance user-data WebSocket and routes its order and account
// pushes into the execution and account event streams. It is safe to call once;
// the underlying client manages listenKey keepalive and reconnect internally.
func (a *Adapter) Start(ctx context.Context) error {
	ws := sdkperp.NewWsAccountClient(ctx, a.apiKey, a.apiSecret)
	resolve := a.provider.resolveVenueSymbol

	ws.SubscribeOrderUpdate(func(ev *sdkperp.OrderUpdateEvent) {
		for _, e := range execEventsFromOrderUpdate(ev, resolve) {
			a.exec.emit(e)
		}
	})
	ws.SubscribeAccountUpdate(func(ev *sdkperp.AccountUpdateEvent) {
		for _, e := range accountEventsFromUpdate(ev, resolve) {
			a.acct.emit(e)
		}
	})

	if err := ws.Connect(); err != nil {
		return err
	}
	a.wsAccount = ws
	return nil
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
