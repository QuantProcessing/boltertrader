package spot

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

type Config struct {
	APIKey     string
	APISecret  string
	Passphrase string
	AccountID  string
	TdMode     string // cash (default) or cross for OKX account modes which reject cash

	Environment     okx.Environment
	DemoHostProfile okx.DemoHostProfile
	RESTBaseURL     string
	WSPublicURL     string
	WSPrivateURL    string
	HTTPClient      *http.Client
	Clock           clock.Clock
}

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instrumentProvider
	rest     *okx.Client
	exec     *executionClient
	acct     *accountClient

	apiKey       string
	apiSecret    string
	passphrase   string
	env          okx.Environment
	profile      okx.DemoHostProfile
	wsPrivateURL string

	wsCtx     context.Context
	cancel    context.CancelFunc
	wsPrivate *okx.WSClient
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	tdMode, err := normalizeSpotTdMode(cfg.TdMode)
	if err != nil {
		return nil, err
	}

	rest := okx.NewClient().
		WithCredentials(cfg.APIKey, cfg.APISecret, cfg.Passphrase).
		WithEnvironment(cfg.Environment).
		WithDemoHostProfile(cfg.DemoHostProfile)
	if cfg.RESTBaseURL != "" {
		rest.WithBaseURL(cfg.RESTBaseURL)
	}
	if cfg.HTTPClient != nil {
		rest.WithHTTPClient(cfg.HTTPClient)
	}
	accountID := cfg.AccountID
	if accountID == "" {
		accountID = model.AccountIDOKXDefault
	}

	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest); err != nil {
		return nil, err
	}

	wsCtx, cancel := context.WithCancel(ctx)
	wsPublic := okx.NewWSClient(wsCtx).
		WithEnvironment(cfg.Environment).
		WithDemoHostProfile(cfg.DemoHostProfile)
	if cfg.WSPublicURL != "" {
		wsPublic.WithURL(cfg.WSPublicURL)
	}

	exec := newExecutionClient(rest, provider, clk, tdMode, accountID)
	acct := newAccountClient(rest, provider, clk, accountID)
	market := newMarketDataClient(rest, wsPublic, provider, clk)

	return &Adapter{
		Market:       market,
		Execution:    exec,
		Account:      acct,
		provider:     provider,
		rest:         rest,
		exec:         exec,
		acct:         acct,
		apiKey:       cfg.APIKey,
		apiSecret:    cfg.APISecret,
		passphrase:   cfg.Passphrase,
		env:          cfg.Environment,
		profile:      cfg.DemoHostProfile,
		wsPrivateURL: cfg.WSPrivateURL,
		wsCtx:        wsCtx,
		cancel:       cancel,
	}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	ws := okx.NewWSClient(a.wsCtx).
		WithEnvironment(a.env).
		WithDemoHostProfile(a.profile).
		WithCredentials(a.apiKey, a.apiSecret, a.passphrase)
	if a.wsPrivateURL != "" {
		ws.WithURL(a.wsPrivateURL)
	}
	if err := ws.Connect(); err != nil {
		return err
	}
	if err := ws.SubscribeOrders(instTypeSpot, nil, func(o *okx.Order) {
		for _, e := range execEventsFromOrder(o, a.provider, a.exec.accountID) {
			a.exec.emit(e)
		}
	}); err != nil {
		return err
	}
	a.wsPrivate = ws
	return nil
}

func (a *Adapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
