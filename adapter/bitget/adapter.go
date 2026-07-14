package bitget

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/QuantProcessing/boltertrader/adapter/internal/streamgap"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

const privateStreamID = "bitget:uta:private"

type reconnectHookSetter interface {
	SetReconnectHooks(func(error), func())
}

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider         *instrumentProvider
	rest             *bitgetsdk.Client
	private          *bitgetsdk.PrivateWSClient
	exec             *executionClient
	acct             *accountClient
	market           *marketDataClient
	clk              clock.Clock
	privateGapMu     sync.Mutex
	privateGap       *streamgap.Reporter
	privateGapActive bool
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
	categories, err := normalizeBitgetCategories(cfg.Categories)
	if err != nil {
		return nil, err
	}
	cfg.Categories = categories
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
	a.bindPrivateGapHooks(a.private)
	resolve := a.resolvePrivateInstrument
	subs := []struct {
		arg     bitgetsdk.WSArg
		handler func(json.RawMessage)
	}{
		{bitgetsdk.WSArg{InstType: "UTA", Topic: "order"}, func(payload json.RawMessage) {
			msg, err := bitgetsdk.DecodeOrderMessage(payload)
			if err == nil {
				events, conversionErr := execEventsFromOrderMessage(msg, resolve, a.exec.accountID)
				if conversionErr != nil {
					a.emitPrivateGapPair(conversionErr.Error())
					return
				}
				for _, event := range events {
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
				events, conversionErr := accountEventsFromPositionMessage(msg, resolve, a.acct.accountID, a.clk.Now())
				if conversionErr != nil {
					a.emitPrivateGapPair(conversionErr.Error())
					return
				}
				for _, event := range events {
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

func (a *Adapter) resolvePrivateInstrument(category, symbol string) (model.InstrumentID, bool) {
	if a == nil || a.provider == nil {
		return model.InstrumentID{}, false
	}
	if id, ok := a.provider.ResolveVenueCategorySymbol(category, symbol); ok {
		return id, true
	}
	if _, supported := normalizeBitgetCategory(category); !supported {
		switch normalizeVenueSymbol(category) {
		case "MARGIN", "COIN-FUTURES":
			return model.InstrumentID{}, false
		default:
			normalizedCategory := normalizeVenueSymbol(category)
			if normalizedCategory == "" {
				normalizedCategory = "<EMPTY>"
			}
			a.emitPrivateGapPair(fmt.Sprintf("unresolved private category=%s symbol=%s", normalizedCategory, normalizeVenueSymbol(symbol)))
			return model.InstrumentID{}, false
		}
	}
	if !a.provider.CategoryInScope(category) {
		return model.InstrumentID{}, false
	}
	normalizedCategory, _ := normalizeBitgetCategory(category)
	reason := fmt.Sprintf("unresolved private instrument category=%s symbol=%s", normalizedCategory, normalizeVenueSymbol(symbol))
	a.emitPrivateGapPair(reason)
	return model.InstrumentID{}, false
}

func (a *Adapter) bindPrivateGapHooks(hooks reconnectHookSetter) {
	if hooks == nil || a.exec == nil {
		return
	}
	a.privateGapMu.Lock()
	if a.privateGap == nil {
		a.privateGap = streamgap.New(VenueName, a.exec.accountID, privateStreamID, a.exec.stream.Emit)
	}
	a.privateGapMu.Unlock()
	hooks.SetReconnectHooks(func(err error) {
		reason := "private stream disconnected"
		if err != nil {
			reason = err.Error()
		}
		a.startPrivateGap(reason)
	}, func() {
		a.recoverPrivateGap("private stream subscriptions restored")
	})
}

func (a *Adapter) startPrivateGap(reason string) {
	a.privateGapMu.Lock()
	defer a.privateGapMu.Unlock()
	if a.privateGap != nil && a.privateGap.Started(reason) {
		a.privateGapActive = true
	}
}

func (a *Adapter) recoverPrivateGap(reason string) {
	a.privateGapMu.Lock()
	defer a.privateGapMu.Unlock()
	if a.privateGap != nil && a.privateGap.Recovered(reason) {
		a.privateGapActive = false
	}
}

func (a *Adapter) emitPrivateGapPair(reason string) {
	a.privateGapMu.Lock()
	defer a.privateGapMu.Unlock()
	if a.privateGap == nil || a.privateGapActive {
		return
	}
	if !a.privateGap.Started(reason) {
		return
	}
	a.privateGapActive = true
	if a.privateGap.Recovered(reason) {
		a.privateGapActive = false
	}
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
