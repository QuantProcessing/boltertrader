package lighter

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
)

// Config configures a live Lighter adapter. Lighter exposes one unified account
// index across spot and perps, so all adapter clients share the same account id.
type Config struct {
	PrivateKey   string
	AccountID    string
	AccountIndex int64
	APIKeyIndex  uint8

	Environment sdk.Environment
	RESTBaseURL string
	WSURL       string
	HTTPClient  *http.Client
	Clock       clock.Clock
}

// Adapter bundles Lighter market, execution, and account clients over one
// resolved testnet/mainnet registry and one account-owned SDK client.
type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *registry
	rest     *sdk.Client
	ws       *sdk.WebsocketClient

	exec   *executionClient
	acct   *accountClient
	market *marketDataClient

	wsCtx  context.Context
	cancel context.CancelFunc
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	env := cfg.Environment
	if env == "" {
		env = sdk.EnvironmentMainnet
	}

	rest := sdk.NewClient().WithEnvironment(env)
	if cfg.RESTBaseURL != "" {
		rest.WithBaseURL(cfg.RESTBaseURL)
	}
	if cfg.HTTPClient != nil {
		rest.HTTPClient = cfg.HTTPClient
	}
	rest.AccountIndex = cfg.AccountIndex
	rest.KeyIndex = cfg.APIKeyIndex
	if cfg.PrivateKey != "" {
		rest.WithCredentials(cfg.PrivateKey, cfg.AccountIndex, cfg.APIKeyIndex)
	}
	accountID := cfg.AccountID
	if accountID == "" {
		accountID = model.AccountIDLighterDefault
	}

	details, err := rest.GetOrderBookDetails(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	provider, err := newRegistryFromOrderBookDetails(details)
	if err != nil {
		return nil, err
	}

	wsCtx, cancel := context.WithCancel(ctx)
	ws := sdk.NewWebsocketClient(wsCtx).WithEnvironment(env)
	if cfg.WSURL != "" {
		ws.WithURL(cfg.WSURL)
	}

	exec := newExecutionClient(rest, provider, clk, cfg.AccountIndex, accountID)
	acct := newAccountClient(rest, provider, clk, cfg.AccountIndex, accountID)
	market := newMarketDataClient(rest, ws, provider, clk)

	return &Adapter{
		Market:    market,
		Execution: exec,
		Account:   acct,
		provider:  provider,
		rest:      rest,
		ws:        ws,
		exec:      exec,
		acct:      acct,
		market:    market,
		wsCtx:     wsCtx,
		cancel:    cancel,
	}, nil
}

// Start is intentionally a no-op until Lighter private websocket dispatch is
// wired into the contract streams. Runtime reconciliation uses REST reports.
func (a *Adapter) Start(ctx context.Context) error {
	return nil
}

func (a *Adapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.ws != nil {
		a.ws.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
