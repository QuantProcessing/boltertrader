package bitget

import (
	"context"
	"encoding/json"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instrumentProvider
	rest     *bitgetsdk.Client
	private  *bitgetsdk.PrivateWSClient
	exec     *executionClient
	acct     *accountClient
	market   *marketDataClient
	clk      clock.Clock
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	profile := cfg.Environment
	if profile.RESTBaseURL == "" {
		profile = bitgetsdk.MainnetEnvironmentProfile()
	}
	if cfg.AccountID == "" {
		cfg.AccountID = AccountIDUnified
	}
	if len(cfg.Categories) == 0 {
		cfg.Categories = []string{"SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures}
	}
	rest := bitgetsdk.NewClient().WithEnvironmentProfile(profile).WithCredentials(cfg.APIKey, cfg.APISecret, cfg.Passphrase)
	if cfg.HTTPClient != nil {
		rest.WithHTTPClient(cfg.HTTPClient)
	}
	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest, cfg.Categories...); err != nil {
		return nil, err
	}
	market := newMarketDataClient(rest, bitgetsdk.NewPublicWSClientWithProfile(profile), provider, clk)
	exec := newExecutionClient(rest, provider, clk, cfg.AccountID)
	acct := newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}, cfg.AccountID)
	private := bitgetsdk.NewPrivateWSClientWithProfile(profile).WithCredentials(cfg.APIKey, cfg.APISecret, cfg.Passphrase)
	return &Adapter{Market: market, Execution: exec, Account: acct, provider: provider, rest: rest, private: private, exec: exec, acct: acct, market: market, clk: clk}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	if a.private == nil {
		return nil
	}
	resolve := a.provider.resolveVenueSymbol
	subs := []struct {
		arg     bitgetsdk.WSArg
		handler func(json.RawMessage)
	}{
		{bitgetsdk.WSArg{InstType: "UTA", Topic: "order"}, func(payload json.RawMessage) {
			msg, err := bitgetsdk.DecodeOrderMessage(payload)
			if err == nil {
				for _, event := range execEventsFromOrderMessage(msg, resolve, a.exec.accountID) {
					a.exec.emit(event)
				}
			}
		}},
		{bitgetsdk.WSArg{InstType: "UTA", Topic: "fill"}, func(payload json.RawMessage) {
			msg, err := bitgetsdk.DecodeFillMessage(payload)
			if err == nil {
				for _, event := range execEventsFromFillMessage(msg, resolve, a.exec.accountID) {
					a.exec.emit(event)
				}
			}
		}},
		{bitgetsdk.WSArg{InstType: "UTA", Topic: "position"}, func(payload json.RawMessage) {
			msg, err := bitgetsdk.DecodePositionMessage(payload)
			if err == nil {
				for _, event := range accountEventsFromPositionMessage(msg, resolve, a.acct.accountID, a.clk.Now()) {
					a.acct.emit(event)
				}
			}
		}},
		{bitgetsdk.WSArg{InstType: "UTA", Topic: "account"}, func(payload json.RawMessage) {
			msg, err := bitgetsdk.DecodeAccountMessage(payload)
			if err == nil {
				for _, event := range accountEventsFromAccountMessage(msg, a.acct.accountID, a.clk.Now()) {
					a.acct.emit(event)
				}
			}
		}},
	}
	for _, sub := range subs {
		if err := a.private.Subscribe(ctx, sub.arg, sub.handler); err != nil {
			return err
		}
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
