package spot

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

const (
	demoRESTBaseURL  = "https://demo-api.binance.com"
	demoWSBaseURL    = "wss://demo-stream.binance.com:9443/ws"
	demoWSAPIBaseURL = "wss://demo-ws-api.binance.com/ws-api/v3"
)

type Config struct {
	APIKey    string
	APISecret string
	Demo      bool

	DemoAPIKey    string
	DemoAPISecret string
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

	wsMarket  *sdkspot.WsMarketClient
	wsAPI     *sdkspot.WsAPIClient
	wsAccount *sdkspot.WsAccountClient
	apiKey    string
	apiSecret string
	demo      bool
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}

	apiKey, apiSecret := cfg.APIKey, cfg.APISecret
	rest := sdkspot.NewClient()
	wsMarket := sdkspot.NewWsMarketClient(ctx)
	wsAPI := sdkspot.NewWsAPIClient(ctx)
	if cfg.Demo {
		apiKey, apiSecret = cfg.DemoAPIKey, cfg.DemoAPISecret
		rest.WithBaseURL(demoRESTBaseURL)
		wsMarket.WsClient.URL = demoWSBaseURL
		wsAPI.WithURL(demoWSAPIBaseURL)
	}
	rest.WithCredentials(apiKey, apiSecret)
	if cfg.HTTPClient != nil {
		rest.HTTPClient = cfg.HTTPClient
	}

	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest); err != nil {
		return nil, err
	}

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
		wsMarket:  wsMarket,
		wsAPI:     wsAPI,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		demo:      cfg.Demo,
	}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	ws := a.newWsAccountClient()
	resolve := a.provider.resolveVenueSymbol

	ws.SubscribeExecutionReport(func(ev *sdkspot.ExecutionReportEvent) {
		for _, e := range execEventsFromExecutionReport(ev, resolve) {
			a.exec.emit(e)
		}
	})
	ws.SubscribeAccountPosition(func(ev *sdkspot.AccountPositionEvent) {
		for _, e := range accountEventsFromAccountPosition(ev) {
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
