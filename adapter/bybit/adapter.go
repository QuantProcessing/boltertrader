package bybit

import (
	"context"
	"encoding/json"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instrumentProvider
	rest     *bybitsdk.Client
	private  *bybitsdk.PrivateWSClient
	exec     *executionClient
	acct     *accountClient
	market   *marketDataClient
	clk      clock.Clock
	cfg      Config
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	profile := cfg.Environment
	if profile.RESTBaseURL == "" {
		profile = bybitsdk.MainnetEnvironmentProfile()
	}
	if cfg.AccountID == "" {
		cfg.AccountID = AccountIDUnified
	}
	if len(cfg.Categories) == 0 {
		cfg.Categories = []string{"spot", "linear"}
	}

	rest := bybitsdk.NewClient().WithEnvironmentProfile(profile).WithCredentials(cfg.APIKey, cfg.APISecret)
	if cfg.HTTPClient != nil {
		rest.WithHTTPClient(cfg.HTTPClient)
	}
	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest, cfg.Categories...); err != nil {
		return nil, err
	}

	wsByCategory := make(map[string]*bybitsdk.PublicWSClient)
	for _, category := range cfg.Categories {
		wsByCategory[category] = bybitsdk.NewPublicWSClientWithProfile(profile, category)
	}
	market := newMarketDataClient(rest, wsByCategory, provider, clk)
	exec := newExecutionClient(rest, provider, clk, cfg.AccountID)
	acct := newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}, cfg.AccountID)

	return &Adapter{
		Market:    market,
		Execution: exec,
		Account:   acct,
		provider:  provider,
		rest:      rest,
		private:   bybitsdk.NewPrivateWSClientWithProfile(profile).WithCredentials(cfg.APIKey, cfg.APISecret),
		exec:      exec,
		acct:      acct,
		market:    market,
		clk:       clk,
		cfg:       cfg,
	}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	if a.private == nil {
		return nil
	}
	resolve := a.provider.resolveVenueSymbol
	if err := a.private.Subscribe(ctx, "order", func(payload json.RawMessage) {
		msg, err := bybitsdk.DecodeOrderMessage(payload)
		if err != nil {
			return
		}
		for _, event := range execEventsFromOrderMessage(msg, resolve, a.exec.accountID) {
			a.exec.emit(event)
		}
	}); err != nil {
		return err
	}
	if err := a.private.Subscribe(ctx, "execution", func(payload json.RawMessage) {
		msg, err := bybitsdk.DecodeExecutionMessage(payload)
		if err != nil {
			return
		}
		for _, event := range execEventsFromExecutionMessage(msg, resolve, a.exec.accountID) {
			a.exec.emit(event)
		}
	}); err != nil {
		return err
	}
	if err := a.private.Subscribe(ctx, "position", func(payload json.RawMessage) {
		msg, err := bybitsdk.DecodePositionMessage(payload)
		if err != nil {
			return
		}
		for _, event := range accountEventsFromPositionMessage(msg, resolve, a.acct.accountID, a.clk.Now()) {
			a.acct.emit(event)
		}
	}); err != nil {
		return err
	}
	if err := a.private.Subscribe(ctx, "wallet", func(payload json.RawMessage) {
		msg, err := bybitsdk.DecodeWalletMessage(payload)
		if err != nil {
			return
		}
		for _, event := range accountEventsFromWalletMessage(msg, a.acct.accountID, a.clk.Now()) {
			a.acct.emit(event)
		}
	}); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) Close() error {
	if a.private != nil {
		_ = a.private.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
