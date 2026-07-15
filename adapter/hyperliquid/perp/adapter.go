package perp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/startgate"
	"github.com/QuantProcessing/boltertrader/adapter/internal/streamgap"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

const (
	AccountIDDefault     = hlaccount.DefaultAccountID
	privateStreamID      = "hyperliquid:perp:private"
	accountStateStreamID = "hyperliquid:perp:private:account-state"
)

type Config struct {
	PrivateKey         string
	AccountID          string
	AccountAddress     string
	VaultAddress       string
	Environment        sdk.Environment
	RESTBaseURL        string
	WSURL              string
	HTTPClient         *http.Client
	Clock              clock.Clock
	IncludeHIP3        bool
	HIP3Dexes          []string
	MarginMode         string
	MarginModeLeverage decimal.Decimal
}

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instruments.Registry
	rest     *sdkperp.Client
	ws       *sdkperp.WebsocketClient
	exec     *executionClient
	acct     *accountClient
	clk      clock.Clock

	privateGap      *streamgap.Reporter
	accountStateGap *streamgap.Reporter
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	cfg.HIP3Dexes = normalizeHIP3DexNames(cfg.HIP3Dexes)

	base := sdk.NewClient().WithEnvironment(cfg.Environment)
	if cfg.PrivateKey != "" || cfg.VaultAddress != "" {
		vault := cfg.VaultAddress
		base.WithCredentials(cfg.PrivateKey, &vault)
	}
	if cfg.RESTBaseURL != "" {
		base.BaseURL = cfg.RESTBaseURL
	}
	if cfg.HTTPClient != nil {
		base.Http = cfg.HTTPClient
	}
	apiAccountAddress, err := hlaccount.ResolveAPIAccountAddress(ctx, base, cfg.AccountAddress)
	if err != nil {
		return nil, err
	}

	rest := sdkperp.NewClient(base)
	identity, err := hlaccount.ResolveIdentity(hlaccount.Source{
		ExplicitAccountID: cfg.AccountID,
		AccountAddress:    apiAccountAddress,
		VaultAddress:      cfg.VaultAddress,
		SignerAddress:     base.AccountAddr,
	})
	if err != nil {
		return nil, err
	}
	accountID := identity.AccountID
	insts, err := buildRegistryInstruments(ctx, rest, sdkspot.NewClient(base), cfg)
	if err != nil {
		return nil, err
	}
	provider := instruments.NewRegistry(insts...)

	wsBase := sdk.NewWebsocketClient(ctx).WithEnvironment(cfg.Environment)
	if cfg.WSURL != "" {
		wsBase.WithURL(cfg.WSURL)
	}
	if cfg.PrivateKey != "" || cfg.VaultAddress != "" {
		vault := cfg.VaultAddress
		wsBase.WithCredentials(cfg.PrivateKey, &vault)
	}
	if apiAccountAddress != "" {
		wsBase.AccountAddr = apiAccountAddress
	}
	ws := sdkperp.NewWebsocketClient(wsBase)

	accountMode, err := resolveAccountMode(ctx, rest)
	if err != nil {
		return nil, err
	}
	exec := newExecutionClient(rest, provider, clk, accountID)
	accountDexes := cfg.HIP3Dexes
	if cfg.IncludeHIP3 && len(accountDexes) == 0 {
		accountDexes = hip3DexesFromRegistry(provider)
	}
	acct := newAccountClient(rest, provider, clk, cfg.MarginMode, cfg.MarginModeLeverage, accountMode, accountID).withHIP3Dexes(accountDexes)
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
		clk:       clk,
	}, nil
}

func hip3DexesFromRegistry(provider *instruments.Registry) []string {
	if provider == nil {
		return nil
	}
	var dexs []string
	for _, inst := range provider.All() {
		dex, _, ok := strings.Cut(inst.VenueSymbol, ":")
		if ok {
			dexs = append(dexs, dex)
		}
	}
	return normalizeHIP3DexNames(dexs)
}

func resolveAccountMode(ctx context.Context, rest *sdkperp.Client) (sdk.AccountAbstraction, error) {
	if rest == nil || rest.AccountAddr == "" {
		return sdk.AccountAbstractionUnknown, nil
	}
	mode, err := rest.GetUserAbstraction(ctx, rest.AccountAddr)
	if err != nil {
		return sdk.AccountAbstractionUnknown, fmt.Errorf("hyperliquid perp: resolve account abstraction for %s: %w", rest.AccountAddr, err)
	}
	if err := validateResolvedAccountMode(mode); err != nil {
		return sdk.AccountAbstractionUnknown, err
	}
	return mode, nil
}

func validateResolvedAccountMode(mode sdk.AccountAbstraction) error {
	switch mode {
	case sdk.AccountAbstractionDefault, sdk.AccountAbstractionUnifiedAccount, sdk.AccountAbstractionPortfolioMargin:
		return nil
	default:
		return fmt.Errorf("hyperliquid perp: unsupported account abstraction %q", mode)
	}
}

func buildRegistryInstruments(ctx context.Context, rest *sdkperp.Client, spotRest *sdkspot.Client, cfg Config) ([]*model.Instrument, error) {
	if cfg.IncludeHIP3 || len(cfg.HIP3Dexes) > 0 {
		insts, ok, err := buildRegistryInstrumentsFromAllPerpMetas(ctx, rest, spotRest, cfg)
		if err == nil && ok {
			return insts, nil
		}
	}

	meta, err := rest.GetPrepMeta(ctx)
	if err != nil {
		return nil, err
	}
	insts, err := instruments.BuildStandardPerpInstruments(meta)
	if err != nil {
		return nil, err
	}
	if !cfg.IncludeHIP3 && len(cfg.HIP3Dexes) == 0 {
		return insts, nil
	}

	spotMeta, err := spotRest.GetSpotMeta(ctx)
	if err != nil {
		return nil, err
	}
	dexs, err := rest.GetPerpDexs(ctx)
	if err != nil {
		return nil, err
	}
	selected := selectedHIP3Dexes(dexs, cfg.HIP3Dexes)
	for _, dex := range selected {
		meta, err := rest.GetPrepMetaForDex(ctx, dex.Name)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid perp: load HIP-3 meta for dex %s: %w", dex.Name, err)
		}
		hip3, err := instruments.BuildHIP3PerpInstruments(dex, meta, spotMeta)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid perp: build HIP-3 instruments for dex %s: %w", dex.Name, err)
		}
		insts = append(insts, hip3...)
	}
	return insts, nil
}

func buildRegistryInstrumentsFromAllPerpMetas(ctx context.Context, rest *sdkperp.Client, spotRest *sdkspot.Client, cfg Config) ([]*model.Instrument, bool, error) {
	metas, err := rest.GetAllPerpMetas(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(metas) == 0 {
		return nil, false, fmt.Errorf("hyperliquid perp: allPerpMetas returned no metas")
	}

	insts, err := instruments.BuildStandardPerpInstruments(&metas[0])
	if err != nil {
		return nil, false, err
	}

	spotMeta, err := spotRest.GetSpotMeta(ctx)
	if err != nil {
		return nil, false, err
	}
	dexs, _ := rest.GetPerpDexs(ctx)
	want := wantedHIP3Dexes(cfg.HIP3Dexes)
	for idx := 1; idx < len(metas); idx++ {
		dex, ok := resolveHIP3DexForMeta(idx, &metas[idx], dexs)
		if !ok {
			continue
		}
		if len(want) > 0 {
			if _, ok := want[dex.Name]; !ok {
				continue
			}
		}
		hip3, err := instruments.BuildHIP3PerpInstruments(dex, &metas[idx], spotMeta)
		if err != nil {
			return nil, false, fmt.Errorf("hyperliquid perp: build HIP-3 instruments for dex %s: %w", dex.Name, err)
		}
		insts = append(insts, hip3...)
	}
	return insts, true, nil
}

func resolveHIP3DexForMeta(index int, meta *sdkperp.PrepMeta, dexs []sdkperp.PerpDex) (sdkperp.PerpDex, bool) {
	for _, dex := range dexs {
		if dex.Index == index && dex.Name != "" {
			return dex, true
		}
	}
	if meta != nil {
		for _, asset := range meta.Universe {
			dexName, _, ok := strings.Cut(asset.Name, ":")
			if ok && dexName != "" {
				return sdkperp.PerpDex{Index: index, Name: dexName}, true
			}
		}
	}
	return sdkperp.PerpDex{}, false
}

func wantedHIP3Dexes(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(names))
	for _, name := range names {
		want[name] = struct{}{}
	}
	return want
}

func selectedHIP3Dexes(dexs []sdkperp.PerpDex, names []string) []sdkperp.PerpDex {
	if len(names) == 0 {
		return dexs
	}
	want := make(map[string]struct{}, len(names))
	for _, name := range names {
		want[name] = struct{}{}
	}
	out := make([]sdkperp.PerpDex, 0, len(names))
	for _, dex := range dexs {
		if _, ok := want[dex.Name]; ok {
			out = append(out, dex)
		}
	}
	return out
}

func spotStateSubscriptionMode(accountMode sdk.AccountAbstraction) (subscribe, ignorePortfolioMargin bool) {
	if !accountMode.UsesSpotClearinghouseState() {
		return false, false
	}
	// Testnet currently acknowledges the documented isPortfolioMargin request
	// field as ignorePortfolioMargin. Keeping it false preserves Portfolio
	// Margin-derived availability for both Unified and Portfolio accounts.
	return true, false
}

func (a *Adapter) Start(ctx context.Context) error {
	if a.ws == nil || a.ws.WebsocketClient == nil || a.ws.AccountAddr == "" {
		return nil
	}
	if a.privateGap == nil {
		a.privateGap = streamgap.New(venueName, a.exec.accountID, privateStreamID, a.exec.stream.Emit)
	}
	if a.accountStateGap == nil {
		a.accountStateGap = streamgap.New(venueName, a.exec.accountID, accountStateStreamID, a.exec.stream.Emit)
	}
	var gate startgate.Gate
	a.ws.SetReconnectHooks(func(err error) {
		reason := "private stream disconnected"
		if err != nil {
			reason = err.Error()
		}
		gate.Handle(func() { a.privateGap.Started(reason) })
	}, func() {
		gate.Handle(func() { a.privateGap.Recovered("private stream subscriptions restored") })
	})
	if err := a.ws.Connect(); err != nil {
		return err
	}
	account := a.ws.AccountAddr
	subscribeSpotState, ignorePortfolioMargin := spotStateSubscriptionMode(a.acct.accountMode)
	spotWS := sdkspot.NewWebsocketClient(a.ws.WebsocketClient)
	orderSubscribed := false
	fillsSubscribed := false
	accountStateSubscribedDexes := make([]string, 0, 1+len(a.acct.hip3Dexes))
	spotStateSubscribed := false
	rollback := func() {
		gate.Abort()
		// Tear down the failed startup socket first. Unsubscribe then removes
		// only local desired state, preventing a successful early subscription
		// from being replayed into the next explicit Start attempt.
		a.ws.Disconnect()
		if spotStateSubscribed {
			_ = spotWS.UnsubscribeSpotState(account, ignorePortfolioMargin)
		}
		for index := len(accountStateSubscribedDexes) - 1; index >= 0; index-- {
			_ = a.ws.UnsubscribeClearinghouseState(account, accountStateSubscribedDexes[index])
		}
		if fillsSubscribed {
			_ = a.ws.UnsubscribeUserFills(account)
		}
		if orderSubscribed {
			_ = a.ws.UnsubscribeOrderUpdates(account)
		}
	}
	if err := a.ws.SubscribeOrderUpdatesConfirmed(account, func(updates []sdk.WsOrderUpdate) {
		gate.Handle(func() {
			for _, update := range updates {
				for _, ev := range execEventsFromOrderUpdate(update, a.provider, a.exec.accountID) {
					a.exec.emit(ev)
				}
			}
		})
	}); err != nil {
		rollback()
		return err
	}
	orderSubscribed = true
	if err := a.ws.SubscribeUserFillsConfirmed(account, func(fills sdk.WsUserFills) {
		gate.Handle(func() { a.emitUserFills(fills) })
	}); err != nil {
		rollback()
		return err
	}
	fillsSubscribed = true
	accountStateDexes := make([]string, 0, 1+len(a.acct.hip3Dexes))
	accountStateDexes = append(accountStateDexes, "")
	accountStateDexes = append(accountStateDexes, a.acct.hip3Dexes...)
	for _, configuredDex := range accountStateDexes {
		dex := configuredDex
		if err := a.ws.SubscribeClearinghouseStateConfirmed(account, dex, func(pos sdkperp.PerpPosition) {
			gate.Handle(func() {
				events, err := a.acct.eventsFromClearinghouseState(&pos, dex)
				if err != nil {
					a.accountStateGap.Started("invalid clearinghouseState for dex " + dex + ": " + err.Error())
					return
				}
				a.accountStateGap.Recovered("valid clearinghouseState resumed for dex " + dex)
				for _, event := range events {
					a.acct.emit(event)
				}
			})
		}); err != nil {
			rollback()
			return err
		}
		accountStateSubscribedDexes = append(accountStateSubscribedDexes, dex)
	}
	if subscribeSpotState {
		if err := spotWS.SubscribeSpotStateConfirmedWithErrors(account, ignorePortfolioMargin, func(state sdk.SpotClearinghouseState) {
			gate.Handle(func() {
				events, err := a.acct.eventsFromSpotState(state, a.clk.Now())
				if err != nil {
					a.accountStateGap.Started("invalid spotState: " + err.Error())
					return
				}
				a.accountStateGap.Recovered("valid spotState resumed")
				for _, event := range events {
					a.acct.emit(event)
				}
			})
		}, func(err error) {
			gate.Handle(func() { a.accountStateGap.Started("invalid spotState payload: " + err.Error()) })
		}); err != nil {
			rollback()
			return err
		}
		spotStateSubscribed = true
	}
	gate.Commit()
	return nil
}

func (a *Adapter) emitUserFills(fills sdk.WsUserFills) {
	flags := contract.EventFlags(0)
	if fills.IsSnapshot {
		flags |= contract.EventFlagFromSnapshot
	}
	for _, ev := range execEventsFromUserFills(fills, a.provider, a.exec.accountID) {
		a.exec.emitWithFlags(ev, flags)
	}
}

func (a *Adapter) Close() error {
	if a.ws != nil {
		a.ws.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}
