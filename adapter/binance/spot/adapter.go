package spot

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/adapter/internal/streamgap"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

const (
	AccountIDDefault = "BINANCE-001"
	demoRESTBaseURL  = sdkspot.DemoBaseURL
	demoWSBaseURL    = sdkspot.DemoWSBaseURL
	demoWSAPIBaseURL = sdkspot.DemoWSAPIBaseURL
	privateStreamID  = "binance:spot:private"
)

type spotAccountWebsocket interface {
	SubscribeExecutionReport(func(*sdkspot.ExecutionReportEvent))
	SubscribeAccountPosition(func(*sdkspot.AccountPositionEvent))
	Connect() error
	Close()
}

type spotAdapterTestHooks struct {
	accountWSFactory func() spotAccountWebsocket
}

type Config struct {
	APIKey    string
	APISecret string
	// Environment selects Binance production or Demo endpoints. Zero value is
	// LIVE; Demo is retained below as a compatibility shortcut.
	Environment sdkspot.Environment
	Demo        bool

	DemoAPIKey    string
	DemoAPISecret string
	AccountID     string
	HTTPClient    *http.Client
	Clock         clock.Clock
}

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instrumentProvider
	rest     *sdkspot.Client
	exec     *executionClient
	acct     *accountClient
	clk      clock.Clock

	wsMarket   *sdkspot.WsMarketClient
	wsAPI      *sdkspot.WsAPIClient
	wsAccount  spotAccountWebsocket
	testHooks  *spotAdapterTestHooks
	privateGap *streamgap.Reporter
	apiKey     string
	apiSecret  string
	demo       bool
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}

	env := cfg.Environment
	if env == "" && cfg.Demo {
		env = sdkspot.EnvironmentDemo
	}
	profile, err := sdkspot.EndpointProfileForEnvironment(env)
	if err != nil {
		return nil, err
	}
	apiKey, apiSecret := cfg.APIKey, cfg.APISecret
	rest := sdkspot.NewClient()
	wsMarket := sdkspot.NewWsMarketClient(ctx)
	wsAPI := sdkspot.NewWsAPIClient(ctx)
	if sdkspot.DefaultEnvironment(env) == sdkspot.EnvironmentDemo {
		apiKey, apiSecret = cfg.DemoAPIKey, cfg.DemoAPISecret
	}
	rest.WithBaseURL(profile.RESTBaseURL)
	wsMarket.WsClient.URL = profile.WSBaseURL
	wsAPI.WithURL(profile.WSAPIBaseURL)
	rest.WithCredentials(apiKey, apiSecret)
	if cfg.HTTPClient != nil {
		rest.HTTPClient = cfg.HTTPClient
	}
	accountID := cfg.AccountID
	if accountID == "" {
		accountID = AccountIDDefault
	}

	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest); err != nil {
		return nil, err
	}

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
		wsMarket:  wsMarket,
		wsAPI:     wsAPI,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		demo:      sdkspot.DefaultEnvironment(env) == sdkspot.EnvironmentDemo,
	}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	var ws spotAccountWebsocket = a.newWsAccountClient()
	if a.testHooks != nil && a.testHooks.accountWSFactory != nil {
		ws = a.testHooks.accountWSFactory()
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

	ws.SubscribeExecutionReport(func(ev *sdkspot.ExecutionReportEvent) {
		for _, e := range execEventsFromExecutionReport(ev, resolve, a.exec.accountID) {
			a.exec.emit(e)
		}
	})
	ws.SubscribeAccountPosition(func(ev *sdkspot.AccountPositionEvent) {
		for _, e := range accountEventsFromAccountPosition(ev, a.acct.accountID) {
			a.acct.emit(e)
		}
	})

	if err := ws.Connect(); err != nil {
		return err
	}
	a.wsAccount = ws
	return nil
}

func (a *Adapter) newWsAccountClient() *sdkspot.WsAccountClient {
	return sdkspot.NewWsAccountClient(a.wsAPI, a.apiKey, a.apiSecret)
}

func (a *Adapter) Close() error {
	if a.wsAccount != nil {
		a.wsAccount.Close()
	}
	if a.wsAPI != nil {
		a.wsAPI.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
