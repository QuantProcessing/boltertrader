package perp

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

// Config configures a live OKX perpetual (SWAP) adapter.
type Config struct {
	APIKey     string
	APISecret  string
	Passphrase string // OKX-specific third credential factor
	AccountID  string
	TdMode     string // cross (default) or isolated for derivative orders

	Environment     okx.Environment
	DemoHostProfile okx.DemoHostProfile
	RESTBaseURL     string
	WSPublicURL     string
	WSPrivateURL    string
	HTTPClient      *http.Client
	Clock           clock.Clock
}

// Adapter bundles the three venue-neutral clients for OKX perps, sharing one
// REST client and a single resolved instrument registry. It owns the WebSocket
// lifecycle: New derives a cancelable context used by both the public and
// private ws clients, and Close cancels it to stop them cleanly.
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

// New constructs a live OKX perp adapter, loading the SWAP instrument registry.
// Credentials are retained for the private stream; call Start to begin it.
func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	tdMode, err := normalizeDerivativeTdMode(cfg.TdMode)
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

	// Adapter-owned context governs all ws clients so Close can stop them.
	wsCtx, cancel := context.WithCancel(ctx)
	wsPublic := okx.NewWSClient(wsCtx).
		WithEnvironment(cfg.Environment).
		WithDemoHostProfile(cfg.DemoHostProfile)
	if cfg.WSPublicURL != "" {
		wsPublic.WithURL(cfg.WSPublicURL)
	}

	exec := newExecutionClient(rest, provider, clk, tdMode, accountID)
	acct := newAccountClient(rest, provider, clk, tdMode, accountID)
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

// Start opens the OKX private WebSocket and routes order and position pushes
// into the execution and account streams. Connect performs the op:"login"
// handshake internally for credentialed clients (no separate Login call). It
// uses the adapter-owned credentials and context.
func (a *Adapter) Start(ctx context.Context) error {
	ws := okx.NewWSClient(a.wsCtx).
		WithEnvironment(a.env).
		WithDemoHostProfile(a.profile).
		WithCredentials(a.apiKey, a.apiSecret, a.passphrase)
	if a.wsPrivateURL != "" {
		ws.WithURL(a.wsPrivateURL)
	}
	if err := ws.Connect(); err != nil { // Connect logs in private clients
		return err
	}

	if err := ws.SubscribeOrders(instTypeSwap, nil, func(o *okx.Order) {
		for _, e := range execEventsFromOrder(o, a.provider, a.exec.accountID) {
			a.exec.emit(e)
		}
	}); err != nil {
		return err
	}
	if err := ws.SubscribePositions(instTypeSwap, func(p *okx.Position) {
		for _, e := range accountEventsFromPosition(p, a.provider, a.acct.accountID) {
			a.acct.emit(e)
		}
	}); err != nil {
		return err
	}

	a.wsPrivate = ws
	return nil
}

// Close cancels the ws context (stopping read loops and callbacks) and then
// closes the event streams. Ordering matters: callbacks stop before the streams
// close, and post-close emits are no-ops, so no send-on-closed can occur.
func (a *Adapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
