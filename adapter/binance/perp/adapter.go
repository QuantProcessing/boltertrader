package perp

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/adapter/internal/streamgap"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

const AccountIDDefault = "BINANCE-001"

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

const privateStreamID = "binance:perp:private"

type perpAccountWebsocket interface {
	SubscribeOrderUpdate(func(*sdkperp.OrderUpdateEvent))
	SubscribeAccountUpdate(func(*sdkperp.AccountUpdateEvent))
	Connect() error
	Close()
}

type perpAdapterTestHooks struct {
	accountWSFactory func(context.Context) perpAccountWebsocket
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

	wsAccount  perpAccountWebsocket
	testHooks  *perpAdapterTestHooks
	privateGap *streamgap.Reporter
	apiKey     string
	apiSecret  string
	profile    sdkperp.EndpointProfile
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
		accountID = AccountIDDefault
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
	var ws perpAccountWebsocket = a.newWsAccountClient(ctx)
	if a.testHooks != nil && a.testHooks.accountWSFactory != nil {
		ws = a.testHooks.accountWSFactory(ctx)
	}
	resolve := a.provider.resolveVenueSymbol
	if a.privateGap == nil {
		a.privateGap = streamgap.New(venueName, a.exec.accountID, privateStreamID, a.exec.stream.Emit)
	}
	if hooks, ok := ws.(interface {
		SetReconnectHooks(func(error), func())
	}); ok {
		hooks.SetReconnectHooks(func(err error) {
			reason := "private stream disconnected"
			if err != nil {
				reason = err.Error()
			}
			a.privateGap.Started(reason)
		}, func() {
			a.privateGap.Recovered("private stream subscription restored")
		})
	}

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
