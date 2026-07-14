package gate

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest        *gatesdk.Client
	provider    *instrumentProvider
	clk         clock.Clock
	accountID   string
	scope       []enums.InstrumentKind
	stream      *wsstream.Stream[contract.AccountEnvelope]
	futuresMode *futuresPositionModeState
}

func newAccountClient(rest *gatesdk.Client, provider *instrumentProvider, clk clock.Clock, scope []enums.InstrumentKind, accountIDs ...string) *accountClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	if accountID == "" {
		accountID = AccountIDUnified
	}
	return &accountClient{rest: rest, provider: provider, clk: clk, accountID: accountID, scope: gateKinds(scope), stream: wsstream.New[contract.AccountEnvelope](256), futuresMode: newFuturesPositionModeState()}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0)
	if hasKind(c.scope, enums.KindSpot) {
		accounts, err := c.rest.ListSpotAccounts(ctx, "")
		if err != nil {
			return nil, err
		}
		out = append(out, balancesFromSpotAccounts(accounts, c.accountID, now)...)
	}
	if hasKind(c.scope, enums.KindPerp) {
		account, err := c.rest.GetFuturesAccount(ctx, gatesdk.SettleUSDT)
		if err != nil {
			return nil, err
		}
		if err := c.futuresMode.setAccount(account); err != nil {
			return nil, err
		}
		out = append(out, balanceFromFuturesAccount(account, c.accountID, now))
	}
	return out, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	if !hasKind(c.scope, enums.KindPerp) {
		return []model.Position{}, nil
	}
	positions, err := c.rest.ListPositions(ctx, gatesdk.SettleUSDT, true)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(positions))
	for _, record := range positions {
		pos := positionFromGate(record, c.provider.resolveVenueSymbol, c.accountID, now)
		if pos.InstrumentID.Symbol == "" || pos.Quantity.IsZero() {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	now := c.clk.Now()
	typ := model.AccountCash
	baseCurrency := ""
	balances := make([]model.AccountBalance, 0)
	margins := make([]model.MarginBalance, 0)
	if hasKind(c.scope, enums.KindSpot) {
		accounts, err := c.rest.ListSpotAccounts(ctx, "")
		if err != nil {
			return model.AccountState{}, err
		}
		balances = append(balances, balancesFromSpotAccounts(accounts, c.accountID, now)...)
	}
	if hasKind(c.scope, enums.KindPerp) {
		typ = model.AccountMargin
		baseCurrency = "USDT"
		account, err := c.rest.GetFuturesAccount(ctx, gatesdk.SettleUSDT)
		if err != nil {
			return model.AccountState{}, err
		}
		if err := c.futuresMode.setAccount(account); err != nil {
			return model.AccountState{}, err
		}
		balances = append(balances, balanceFromFuturesAccount(account, c.accountID, now))
		positions, err := c.Positions(ctx)
		if err != nil {
			return model.AccountState{}, err
		}
		margins = marginBalancesFromFuturesAccount(account, positions, now)
	}
	return model.AccountState{
		AccountID:    c.accountID,
		Venue:        VenueName,
		Type:         typ,
		BaseCurrency: baseCurrency,
		Balances:     balances,
		Margins:      margins,
		Reported:     true,
		EventID:      model.AccountStateEventID(VenueName, c.accountID, now),
		TsEvent:      now,
		TsInit:       now,
	}, nil
}

func balancesFromSpotAccounts(accounts []gatesdk.SpotAccount, accountID string, now time.Time) []model.AccountBalance {
	out := make([]model.AccountBalance, 0, len(accounts))
	for _, account := range accounts {
		if account.Currency == "" {
			continue
		}
		free := dec(account.Available)
		locked := dec(account.Locked)
		out = append(out, model.AccountBalance{
			AccountID: accountID,
			Currency:  account.Currency,
			Total:     free.Add(locked),
			Free:      free,
			Available: free,
			Locked:    locked,
			UpdatedAt: now,
		})
	}
	return out
}

func balanceFromSpotBalance(row gatesdk.SpotBalance, accountID string, fallback time.Time) model.AccountBalance {
	ts := firstNonZeroTime(timeFromMillisString(row.TimestampMS), timeFromMillis(row.Timestamp*1000), fallback)
	free := dec(row.Available)
	locked := dec(row.Freeze)
	total := firstNonZero(dec(row.Total), free.Add(locked))
	return model.AccountBalance{
		AccountID: accountID,
		Currency:  row.Currency,
		Total:     total,
		Free:      free,
		Available: free,
		Locked:    locked,
		UpdatedAt: ts,
	}
}

func balanceFromFuturesAccount(account *gatesdk.FuturesAccount, accountID string, now time.Time) model.AccountBalance {
	if account == nil {
		return model.AccountBalance{AccountID: accountID, Currency: "USDT", UpdatedAt: now}
	}
	free := firstNonZero(dec(account.Available), dec(account.CrossAvailable))
	total := firstNonZero(dec(account.Total), dec(account.CrossMarginBalance), free)
	locked := decimal.Zero
	if total.GreaterThan(free) {
		locked = total.Sub(free)
	}
	currency := account.Currency
	if currency == "" {
		currency = "USDT"
	}
	return model.AccountBalance{
		AccountID: accountID,
		Currency:  currency,
		Total:     total,
		Free:      free,
		Available: free,
		Locked:    locked,
		UpdatedAt: now,
	}
}

func marginBalancesFromFuturesAccount(account *gatesdk.FuturesAccount, positions []model.Position, now time.Time) []model.MarginBalance {
	out := make([]model.MarginBalance, 0)
	currency := "USDT"
	if account != nil && account.Currency != "" {
		currency = account.Currency
	}
	initial := decimal.Zero
	maintenance := decimal.Zero
	if account != nil {
		initial = firstNonZero(dec(account.PositionInitialMargin), dec(account.CrossInitialMargin), dec(account.PositionMargin))
		maintenance = firstNonZero(dec(account.MaintenanceMargin), dec(account.CrossMaintenanceMargin))
	}
	out = append(out, model.MarginBalance{Currency: currency, Initial: initial, Maintenance: maintenance, UpdatedAt: now})
	for _, pos := range positions {
		id := pos.InstrumentID
		out = append(out, model.MarginBalance{Currency: currency, InstrumentID: &id, UpdatedAt: now})
	}
	return out
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	_ = ctx
	_ = id
	_ = leverage
	return fmt.Errorf("gate spot: cash accounts do not support leverage: %w", errs.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	_ = ctx
	_ = id
	_ = mode
	return fmt.Errorf("gate spot: cash accounts do not support margin mode: %w", errs.ErrNotSupported)
}

func (c *accountClient) Capabilities() contract.Capabilities {
	products := make([]contract.ProductCapability, 0, len(c.scope))
	for _, kind := range c.scope {
		products = append(products, contract.ProductCapability{Kind: kind, Account: true})
	}
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  products,
		Reports:   contract.ReportCapabilities{PositionReports: hasKind(c.scope, enums.KindPerp), AccountBalanceSnapshots: true, AccountStateSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: true},
	}
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}
func (c *accountClient) Close() error { c.stream.Close(); return nil }
