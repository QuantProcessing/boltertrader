package spot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest      *sdkspot.Client
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]

	spotMu         sync.Mutex
	spotCurrencies map[string]struct{}
}

func newAccountClient(rest *sdkspot.Client, clk clock.Clock, accountID ...string) *accountClient {
	return &accountClient{
		rest:           rest,
		clk:            clk,
		accountID:      firstAccountID(accountID),
		stream:         wsstream.New[contract.AccountEnvelope](256),
		spotCurrencies: make(map[string]struct{}),
	}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	bal, err := c.rest.GetBalance()
	if err != nil {
		return nil, err
	}
	c.rememberSpotCurrencies(bal.Balances)
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0, len(bal.Balances))
	for _, b := range bal.Balances {
		total := dec(b.Total)
		locked := dec(b.Hold)
		available := total.Sub(locked)
		out = append(out, model.AccountBalance{
			AccountID: c.accountID,
			Currency:  b.Coin,
			Total:     total,
			Free:      available,
			Locked:    locked,
			UpdatedAt: now,
		})
	}
	return out, nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	mode, err := c.rest.GetUserAbstraction(ctx, c.rest.AccountAddr)
	if err != nil {
		return model.AccountState{}, err
	}
	perpState, err := sdkperp.NewClient(c.rest.Client).GetBalance(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	spotState, err := c.rest.GetSpotClearinghouseState(ctx, c.rest.AccountAddr)
	if err != nil {
		return model.AccountState{}, err
	}
	state, err := hlaccount.BuildAccountState(hlaccount.StateInput{
		AccountID:   c.accountID,
		AccountMode: mode,
		Perp:        perpState,
		Spot:        spotState,
		Now:         c.clk.Now(),
	})
	if err != nil {
		return model.AccountState{}, err
	}
	c.rememberSpotCurrencies(spotState.Balances)
	return state, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	return []model.Position{}, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	return fmt.Errorf("hyperliquid spot: cash accounts do not support leverage: %w", errs.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("hyperliquid spot: cash accounts do not support margin mode: %w", errs.ErrNotSupported)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) rememberSpotCurrencies(balances []sdk.SpotBalance) {
	current := make(map[string]struct{}, len(balances))
	for _, raw := range balances {
		if currency := strings.ToUpper(strings.TrimSpace(raw.Coin)); currency != "" {
			current[currency] = struct{}{}
		}
	}
	c.spotMu.Lock()
	if c.spotCurrencies == nil {
		c.spotCurrencies = make(map[string]struct{}, len(current))
	}
	for currency := range current {
		c.spotCurrencies[currency] = struct{}{}
	}
	c.spotMu.Unlock()
}

func (c *accountClient) eventsFromSpotState(state sdk.SpotClearinghouseState, now time.Time) ([]contract.AccountEvent, error) {
	events, err := accountEventsFromSpotState(state, now, c.accountID)
	if err != nil {
		return nil, err
	}
	current := make(map[string]struct{}, len(events))
	for _, event := range events {
		balance := event.(contract.BalanceEvent).Balance
		current[balance.Currency] = struct{}{}
	}
	c.spotMu.Lock()
	for currency := range c.spotCurrencies {
		if _, ok := current[currency]; ok {
			continue
		}
		events = append(events, contract.BalanceEvent{Balance: model.AccountBalance{
			AccountID: c.accountID,
			Currency:  currency,
			UpdatedAt: now,
		}})
	}
	c.spotCurrencies = current
	c.spotMu.Unlock()
	return events, nil
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}

func firstAccountID(ids []string) string {
	if len(ids) == 0 || ids[0] == "" {
		return AccountIDDefault
	}
	return ids[0]
}
