package nado

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/adapter/internal/streamgap"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
)

const privateStreamID = "nado:account:private"

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instrumentProvider
	rest     *sdk.Client
	exec     *executionClient
	acct     *accountClient
	market   *marketDataClient
	clk      clock.Clock

	privateGap *streamgap.Reporter
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	if cfg.AccountID == "" {
		cfg.AccountID = AccountIDUnified
	}
	if cfg.ProductKind == enums.KindUnknown {
		cfg.ProductKind = enums.KindPerp
	}
	if _, err := productKinds(cfg.ProductKind); err != nil {
		return nil, err
	}

	profile, err := profileFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	rest := cfg.Client
	if rest == nil {
		rest, err = sdk.NewClient(profile)
		if err != nil {
			return nil, err
		}
	} else if rest.Profile().Environment() != profile.Environment() || rest.Profile().ChainID() != profile.ChainID() {
		return nil, fmt.Errorf("nado: injected client profile does not match adapter profile")
	}
	if cfg.HTTPClient != nil {
		rest.WithHTTPClient(cfg.HTTPClient)
	}
	if cfg.PrivateKey != "" {
		if _, err := rest.WithCredentials(cfg.PrivateKey, cfg.Subaccount); err != nil {
			return nil, err
		}
	}

	status, err := rest.GetStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("nado: status discovery: %w", err)
	}
	if status != sdk.SequencerStatusActive {
		return nil, fmt.Errorf("%w: %q", sdk.ErrNadoSequencerInactive, status)
	}
	products, err := rest.GetAllProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("nado: product discovery: %w", err)
	}
	symbols, err := rest.QuerySymbols(ctx, sdk.SymbolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("nado: symbol discovery: %w", err)
	}
	assets, err := rest.GetAssets(ctx)
	if err != nil {
		return nil, fmt.Errorf("nado: asset discovery: %w", err)
	}
	// Account state is unified across Spot and Perp. ProductKind scopes only the
	// market/execution surface, never the authoritative account registry.
	provider, err := newInstrumentProviderFromDiscovery(*products, *symbols, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	if err != nil {
		return nil, err
	}
	if err := provider.ApplyAssetDiscovery(assets); err != nil {
		return nil, err
	}

	market := newMarketDataClient(rest, provider, clk, cfg.ProductKind)
	exec := newExecutionClient(rest, provider, clk, cfg.ProductKind, cfg.AccountID)
	acct := newAccountClient(rest, provider, clk, cfg.ProductKind, cfg.AccountID)
	if rest != nil {
		marketStream, err := sdk.NewWsMarketClient(context.Background(), rest.Profile())
		if err != nil {
			return nil, fmt.Errorf("nado: market stream client: %w", err)
		}
		market.streamBackend = marketStream
		market.snapshotBackend = rest
	}
	if rest.Signer != nil {
		api, err := sdk.NewWsApiClient(context.Background(), rest)
		if err != nil {
			return nil, fmt.Errorf("nado: ws api client: %w", err)
		}
		exec.submitter = nadoSDKSubmissionBackend{api: api}
		accountStream, err := sdk.NewWsAccountClient(context.Background(), rest)
		if err != nil {
			return nil, fmt.Errorf("nado: account stream client: %w", err)
		}
		exec.accountStream = accountStream
		acct.streamBackend = accountStream
	}
	return &Adapter{
		Market:    market,
		Execution: exec,
		Account:   acct,
		provider:  provider,
		rest:      rest,
		exec:      exec,
		acct:      acct,
		market:    market,
		clk:       clk,
	}, nil
}

func profileFromConfig(cfg Config) (sdk.Profile, error) {
	if cfg.Profile != nil {
		if cfg.Environment != "" && cfg.Environment != cfg.Profile.Environment() {
			return sdk.Profile{}, fmt.Errorf("nado: environment %q conflicts with profile %q", cfg.Environment, cfg.Profile.Environment())
		}
		return *cfg.Profile, nil
	}
	env := cfg.Environment
	if env == "" {
		env = sdk.EnvironmentMainnet
	}
	return sdk.NewProfile(env)
}

func (a *Adapter) Start(ctx context.Context) error {
	if a.exec.accountStream != nil && a.privateGap == nil {
		a.privateGap = streamgap.New(VenueName, a.exec.accountID, privateStreamID, a.exec.stream.Emit)
		if hooks, ok := a.exec.accountStream.(interface {
			SetReconnectHooks(func(error), func())
		}); ok {
			hooks.SetReconnectHooks(func(err error) {
				reason := "private account stream disconnected"
				if err != nil {
					reason = err.Error()
				}
				a.privateGap.Started(reason)
			}, func() {
				a.privateGap.Recovered("private account stream authentication and subscriptions restored")
			})
		}
	}
	if err := a.market.Start(ctx); err != nil {
		return err
	}
	if starter, ok := a.exec.submitter.(interface{ Connect() error }); ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := starter.Connect(); err != nil {
			return err
		}
	}
	if err := a.exec.Start(ctx); err != nil {
		return err
	}
	if err := a.acct.Start(ctx); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) Close() error {
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
