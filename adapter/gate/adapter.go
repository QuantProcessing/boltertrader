package gate

import (
	"context"
	"encoding/json"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
)

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider       *instrumentProvider
	rest           *gatesdk.Client
	privateSpot    *gatesdk.WSClient
	privateFutures *gatesdk.WSClient
	exec           *executionClient
	acct           *accountClient
	market         *marketDataClient
	clk            clock.Clock
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	profile := cfg.Environment
	if profile.RESTBaseURL == "" {
		profile = gatesdk.MainnetEnvironmentProfile()
	}
	if cfg.AccountID == "" {
		cfg.AccountID = AccountIDUnified
	}
	if len(cfg.Products) == 0 {
		cfg.Products = []string{gatesdk.ProductSpot, gatesdk.ProductFuturesUSDT}
	}

	rest := gatesdk.NewClient().WithEnvironmentProfile(profile).WithCredentials(cfg.APIKey, cfg.APISecret)
	if cfg.HTTPClient != nil {
		rest.WithHTTPClient(cfg.HTTPClient)
	}
	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest, cfg.Products...); err != nil {
		return nil, err
	}
	publicWS, err := gatesdk.NewWSClientWithProfile(profile, gatesdk.ProductSpot)
	if err != nil {
		return nil, err
	}
	futuresWS, err := gatesdk.NewWSClientWithProfile(profile, gatesdk.ProductFuturesUSDT)
	if err != nil {
		return nil, err
	}
	privateSpot, err := gatesdk.NewWSClientWithProfile(profile, gatesdk.ProductSpot)
	if err != nil {
		return nil, err
	}
	privateSpot.WithCredentials(cfg.APIKey, cfg.APISecret)
	privateFutures, err := gatesdk.NewWSClientWithProfile(profile, gatesdk.ProductFuturesUSDT)
	if err != nil {
		return nil, err
	}
	privateFutures.WithCredentials(cfg.APIKey, cfg.APISecret)

	kinds := kindsForProducts(cfg.Products)
	market := newMarketDataClient(rest, publicWS, futuresWS, provider, clk).withScope(kinds)
	exec := newExecutionClient(rest, provider, clk, cfg.AccountID).withScope(kinds)
	acct := newAccountClient(rest, provider, clk, kinds, cfg.AccountID)
	return &Adapter{Market: market, Execution: exec, Account: acct, provider: provider, rest: rest, privateSpot: privateSpot, privateFutures: privateFutures, exec: exec, acct: acct, market: market, clk: clk}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	if err := a.startSpotStreams(ctx); err != nil {
		return err
	}
	return a.startFuturesStreams(ctx)
}

func (a *Adapter) startSpotStreams(ctx context.Context) error {
	if a.privateSpot == nil {
		return nil
	}
	symbols := a.spotVenueSymbols()
	if len(symbols) == 0 {
		return nil
	}
	resolve := a.provider.resolveVenueSymbol
	for _, symbol := range symbols {
		if err := a.privateSpot.Subscribe(ctx, gatesdk.ChannelSpotOrder, []string{symbol}, func(payload json.RawMessage) {
			msg, err := gatesdk.DecodeSpotOrderMessage(payload)
			if err == nil {
				for _, event := range execEventsFromSpotOrderMessage(msg, resolve, a.exec.accountID) {
					a.exec.emit(event)
				}
			}
		}); err != nil {
			return err
		}
		if err := a.privateSpot.Subscribe(ctx, gatesdk.ChannelSpotUserTrade, []string{symbol}, func(payload json.RawMessage) {
			msg, err := gatesdk.DecodeSpotUserTradeMessage(payload)
			if err == nil {
				for _, event := range execEventsFromSpotUserTradeMessage(msg, resolve, a.exec.accountID) {
					a.exec.emit(event)
				}
			}
		}); err != nil {
			return err
		}
	}
	return a.privateSpot.Subscribe(ctx, gatesdk.ChannelSpotBalance, nil, func(payload json.RawMessage) {
		msg, err := gatesdk.DecodeSpotBalanceMessage(payload)
		if err == nil {
			for _, event := range accountEventsFromSpotBalanceMessage(msg, a.acct.accountID, a.clk.Now()) {
				a.acct.emit(event)
			}
		}
	})
}

func (a *Adapter) startFuturesStreams(ctx context.Context) error {
	if a.privateFutures == nil {
		return nil
	}
	symbols := a.futuresVenueSymbols()
	if len(symbols) == 0 {
		return nil
	}
	resolve := a.provider.resolveVenueSymbol
	for _, symbol := range symbols {
		if err := a.privateFutures.Subscribe(ctx, gatesdk.ChannelFuturesOrder, []string{symbol}, func(payload json.RawMessage) {
			msg, err := gatesdk.DecodeFuturesOrderMessage(payload)
			if err == nil {
				for _, event := range execEventsFromFuturesOrderMessage(msg, resolve, a.exec.accountID) {
					a.exec.emit(event)
				}
			}
		}); err != nil {
			return err
		}
		if err := a.privateFutures.Subscribe(ctx, gatesdk.ChannelFuturesUserTrade, []string{symbol}, func(payload json.RawMessage) {
			msg, err := gatesdk.DecodeFuturesUserTradeMessage(payload)
			if err == nil {
				for _, event := range execEventsFromFuturesUserTradeMessage(msg, resolve, a.exec.accountID) {
					a.exec.emit(event)
				}
			}
		}); err != nil {
			return err
		}
		if err := a.privateFutures.Subscribe(ctx, gatesdk.ChannelFuturesPosition, []string{symbol}, func(payload json.RawMessage) {
			msg, err := gatesdk.DecodeFuturesPositionMessage(payload)
			if err == nil {
				for _, event := range accountEventsFromFuturesPositionMessage(msg, resolve, a.acct.accountID, a.clk.Now()) {
					a.acct.emit(event)
				}
			}
		}); err != nil {
			return err
		}
	}
	return a.privateFutures.Subscribe(ctx, gatesdk.ChannelFuturesBalance, nil, func(payload json.RawMessage) {
		msg, err := gatesdk.DecodeFuturesBalanceMessage(payload)
		if err == nil {
			for _, event := range accountEventsFromFuturesBalanceMessage(msg, a.acct.accountID, a.clk.Now()) {
				a.acct.emit(event)
			}
		}
	})
}

func (a *Adapter) Close() error {
	if a.privateSpot != nil {
		_ = a.privateSpot.Close()
	}
	if a.privateFutures != nil {
		_ = a.privateFutures.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}

func (a *Adapter) futuresVenueSymbols() []string {
	out := make([]string, 0)
	for _, inst := range a.provider.All() {
		if inst != nil && inst.ID.Kind == enums.KindPerp && inst.Settle == "USDT" && inst.VenueSymbol != "" {
			out = append(out, inst.VenueSymbol)
		}
	}
	return out
}

func (a *Adapter) spotVenueSymbols() []string {
	out := make([]string, 0)
	for _, inst := range a.provider.All() {
		if inst != nil && inst.ID.Kind == enums.KindSpot && inst.VenueSymbol != "" {
			out = append(out, inst.VenueSymbol)
		}
	}
	return out
}
