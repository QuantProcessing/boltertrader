package spot

import (
	"context"
	"net/http"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/startgate"
	"github.com/QuantProcessing/boltertrader/adapter/internal/streamgap"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

const (
	privateStreamID      = "hyperliquid:spot:private"
	accountStateStreamID = "hyperliquid:spot:private:account-state"
)

type Config struct {
	PrivateKey     string
	AccountID      string
	AccountAddress string
	VaultAddress   string
	Environment    sdk.Environment
	RESTBaseURL    string
	WSURL          string
	HTTPClient     *http.Client
	Clock          clock.Clock
}

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider *instruments.Registry
	rest     *sdkspot.Client
	ws       *sdkspot.WebsocketClient
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
	rest := sdkspot.NewClient(base)
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

	meta, err := rest.GetSpotMeta(ctx)
	if err != nil {
		return nil, err
	}
	insts, err := instruments.BuildSpotInstruments(meta)
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
	ws := sdkspot.NewWebsocketClient(wsBase)

	exec := newExecutionClient(rest, provider, clk, accountID)
	acct := newAccountClient(rest, clk, accountID)
	market := newMarketDataClient(rest, provider, clk)

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
	orderSubscribed := false
	fillsSubscribed := false
	spotStateSubscribed := false
	rollback := func() {
		gate.Abort()
		// Tear down the failed startup socket first. Unsubscribe then removes
		// only local desired state, preventing a successful early subscription
		// from being replayed into the next explicit Start attempt.
		a.ws.Disconnect()
		if spotStateSubscribed {
			_ = a.ws.UnsubscribeSpotState(account, false)
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
	if err := a.ws.SubscribeSpotStateConfirmedWithErrors(account, false, func(state sdk.SpotClearinghouseState) {
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
