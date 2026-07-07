package lighter

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest         *sdk.Client
	provider     *registry
	clk          clock.Clock
	accountIndex int64
	accountID    string
	stream       *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *sdk.Client, provider *registry, clk clock.Clock, accountIndex int64, accountIDs ...string) *accountClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := model.AccountIDLighterDefault
	if len(accountIDs) > 0 && accountIDs[0] != "" {
		accountID = accountIDs[0]
	}
	return &accountClient{
		rest:         rest,
		provider:     provider,
		clk:          clk,
		accountIndex: accountIndex,
		accountID:    accountID,
		stream:       wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	acct, err := c.fetchAccount(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	return accountStateFromLighterAccount(acct, c.accountID, c.clk.Now()), nil
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	state, err := c.AccountState(ctx)
	if err != nil {
		return nil, err
	}
	return state.Balances, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	acct, err := c.fetchAccount(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(acct.Positions))
	for _, pos := range acct.Positions {
		p, ok := positionFromLighter(pos, c.provider, c.accountID, now)
		if !ok || p.Quantity.IsZero() {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (c *accountClient) fetchAccount(ctx context.Context) (*sdk.Account, error) {
	if c.rest == nil {
		return nil, fmt.Errorf("lighter: rest client required")
	}
	res, err := c.rest.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	for _, acct := range res.Accounts {
		if acct == nil {
			continue
		}
		if acct.AccountIndex == c.accountIndex || acct.Index == c.accountIndex {
			return acct, nil
		}
	}
	return nil, fmt.Errorf("lighter: account index %d not found", c.accountIndex)
}

func accountStateFromLighterAccount(acct *sdk.Account, accountID string, now time.Time) model.AccountState {
	if accountID == "" {
		accountID = model.AccountIDLighterDefault
	}
	accountIndex := acct.AccountIndex
	if accountIndex == 0 {
		accountIndex = acct.Index
	}
	balances := make([]model.AccountBalance, 0, len(acct.Assets))
	for _, asset := range acct.Assets {
		if asset == nil {
			continue
		}
		total := dec(asset.Balance).Add(dec(asset.MarginBalance))
		locked := dec(asset.LockedBalance)
		free := total.Sub(locked)
		if free.IsNegative() {
			free = decimal.Zero
		}
		balances = append(balances, model.AccountBalance{
			AccountID: accountID,
			Currency:  asset.Symbol,
			Total:     total,
			Free:      free,
			Available: free,
			Locked:    locked,
			UpdatedAt: now,
		})
	}
	initial := dec(acct.CrossInitialMarginReq)
	if initial.IsZero() {
		collateral := dec(acct.Collateral)
		available := dec(acct.AvailableBalance)
		if collateral.GreaterThan(available) {
			initial = collateral.Sub(available)
		}
	}
	margins := []model.MarginBalance{{
		Currency:    "USDC",
		Initial:     initial,
		Maintenance: dec(acct.CrossMaintenanceMarginReq),
		UpdatedAt:   now,
	}}
	return model.AccountState{
		AccountID:    accountID,
		Venue:        venueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USDC",
		Balances:     balances,
		Margins:      margins,
		ModeInfo: model.AccountModeInfo{
			Venue:          venueName,
			AccountID:      accountID,
			AccountMode:    "UNIFIED",
			MarginMode:     "cross",
			PositionMode:   "net",
			CollateralMode: "unified",
			ProductScope:   []enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
			Verified:       true,
			VerifiedAt:     now,
			Source:         "GET /api/v1/account?by=index&value=<account_index>",
			Details: map[string]string{
				"account_index":        strconv.FormatInt(accountIndex, 10),
				"account_type":         strconv.Itoa(int(acct.AccountType)),
				"account_status":       strconv.Itoa(int(acct.Status)),
				"account_trading_mode": strconv.Itoa(acct.AccountTradingMode),
				"l1_address":           acct.L1Address,
			},
		},
		Reported: true,
		TsEvent:  lighterAccountTime(acct, now),
		TsInit:   now,
	}
}

func positionFromLighter(pos *sdk.Position, provider *registry, accountID string, now time.Time) (model.Position, bool) {
	if pos == nil {
		return model.Position{}, false
	}
	inst, ok := provider.byMarket(pos.MarketId)
	if !ok {
		return model.Position{}, false
	}
	qty := dec(pos.Position)
	if pos.Sign < 0 && qty.IsPositive() {
		qty = qty.Neg()
	}
	return model.Position{
		AccountID:     accountID,
		InstrumentID:  inst.ID,
		Side:          enums.PosNet,
		Quantity:      qty,
		EntryPrice:    dec(pos.AvgEntryPrice),
		UnrealizedPnL: dec(pos.UnrealizedPnl),
		UpdatedAt:     now,
	}, true
}

func lighterAccountTime(acct *sdk.Account, fallback time.Time) time.Time {
	if acct != nil && acct.TransactionTime > 0 {
		return time.UnixMicro(acct.TransactionTime)
	}
	return fallback
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	if !leverage.IsPositive() {
		return fmt.Errorf("lighter: leverage must be positive: %w", errs.ErrNotSupported)
	}
	fraction := decimal.NewFromInt(10_000).Div(leverage)
	_, err := c.rest.UpdateLeverage(ctx, *inst.AssetIndex, uint16(fraction.IntPart()), sdk.CrossMarginMode)
	return err
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	marginMode, err := lighterMarginMode(mode)
	if err != nil {
		return err
	}
	_, err = c.rest.UpdateLeverage(ctx, *inst.AssetIndex, 500, marginMode)
	return err
}

func lighterMarginMode(mode string) (uint8, error) {
	switch mode {
	case "", "cross":
		return sdk.CrossMarginMode, nil
	case "isolated":
		return sdk.IsolatedMarginMode, nil
	default:
		return 0, fmt.Errorf("lighter: unsupported margin mode %q: %w", mode, errs.ErrNotSupported)
	}
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: venueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Account: true},
			{Kind: enums.KindPerp, Account: true},
		},
		Reports: contract.ReportCapabilities{
			AccountBalanceSnapshots: true,
			PositionReports:         true,
			AccountStateSnapshots:   true,
		},
		Streaming: contract.StreamCapabilities{Account: false},
	}
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
