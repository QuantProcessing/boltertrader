package bybit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest      *bybitsdk.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *bybitsdk.Client, provider *instrumentProvider, clk clock.Clock, scope []enums.InstrumentKind, accountIDs ...string) *accountClient {
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
	return &accountClient{
		rest:      rest,
		provider:  provider,
		clk:       clk,
		accountID: accountID,
		stream:    wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	wallet, err := c.rest.GetWalletBalance(ctx, "UNIFIED", "")
	if err != nil {
		return nil, err
	}
	return balancesFromWallet(wallet, c.accountID, c.clk.Now()), nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	return c.positions(ctx)
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	info, err := c.rest.GetAccountInfo(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	if !info.AccountMode().IsUnified() {
		return model.AccountState{}, fmt.Errorf("bybit: account mode %s is not a phase-one unified account: %w", info.AccountMode(), errs.ErrNotSupported)
	}
	wallet, err := c.rest.GetWalletBalance(ctx, "UNIFIED", "")
	if err != nil {
		return model.AccountState{}, err
	}
	positions, err := c.positions(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	now := c.clk.Now()
	state := model.AccountState{
		AccountID:    c.accountID,
		Venue:        VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USD",
		Balances:     balancesFromWallet(wallet, c.accountID, now),
		Margins:      c.marginBalancesFromWalletAndPositions(wallet, positions, now),
		Reported:     true,
		EventID:      model.AccountStateEventID(VenueName, c.accountID, now),
		TsEvent:      now,
		TsInit:       now,
	}
	if len(state.Margins) == 0 {
		state.Margins = append(state.Margins, model.MarginBalance{Currency: "USDT", UpdatedAt: now})
	}
	return state, nil
}

func (c *accountClient) positions(ctx context.Context) ([]model.Position, error) {
	now := c.clk.Now()
	out := make([]model.Position, 0)
	for _, settle := range []string{bybitsdk.SettleCoinUSDT, bybitsdk.SettleCoinUSDC} {
		records, err := c.rest.GetPositions(ctx, "linear", "", settle)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			pos := positionFromBybit(record, c.provider.resolveVenueSymbol, c.accountID, now)
			if pos.InstrumentID.Symbol == "" {
				continue
			}
			if pos.Quantity.IsZero() {
				continue
			}
			out = append(out, pos)
		}
	}
	return out, nil
}

func balancesFromWallet(wallet *bybitsdk.WalletBalanceResult, accountID string, now time.Time) []model.AccountBalance {
	if wallet == nil {
		return nil
	}
	out := make([]model.AccountBalance, 0)
	for _, account := range wallet.List {
		if account.TotalEquity != "" || account.TotalAvailableBalance != "" || account.TotalWalletBalance != "" {
			available := dec(account.TotalAvailableBalance)
			out = append(out, model.AccountBalance{
				AccountID: accountID,
				Currency:  "USD",
				Total:     firstNonZero(dec(account.TotalEquity), dec(account.TotalWalletBalance), available),
				Free:      available,
				Available: available,
				UpdatedAt: now,
			})
		}
		for _, coin := range account.Coin {
			if strings.TrimSpace(coin.Coin) == "" {
				continue
			}
			total := firstNonZero(dec(coin.Equity), dec(coin.WalletBalance), dec(coin.UsdValue))
			locked := dec(coin.Locked)
			borrowed := firstNonZero(dec(coin.BorrowAmount), dec(coin.SpotBorrow))
			free := dec(coin.WalletBalance).Sub(locked).Sub(borrowed)
			if free.IsNegative() || free.IsZero() {
				free = total.Sub(locked).Sub(borrowed)
			}
			if free.IsNegative() {
				free = decimal.Zero
			}
			out = append(out, model.AccountBalance{
				AccountID: accountID,
				Currency:  coin.Coin,
				Total:     total,
				Free:      free,
				Available: free,
				Locked:    locked,
				Borrowed:  borrowed,
				UpdatedAt: now,
			})
		}
	}
	return out
}

func (c *accountClient) marginBalancesFromWalletAndPositions(wallet *bybitsdk.WalletBalanceResult, positions []model.Position, now time.Time) []model.MarginBalance {
	out := make([]model.MarginBalance, 0)
	if wallet != nil {
		for _, account := range wallet.List {
			for _, coin := range account.Coin {
				if strings.TrimSpace(coin.Coin) == "" {
					continue
				}
				out = append(out, model.MarginBalance{
					Currency:    coin.Coin,
					Initial:     decimal.Zero,
					Maintenance: decimal.Zero,
					UpdatedAt:   now,
				})
			}
		}
	}
	for _, pos := range positions {
		ccy := ""
		if inst, ok := c.provider.Instrument(pos.InstrumentID); ok {
			ccy = inst.Settle
		}
		if ccy == "" {
			continue
		}
		id := pos.InstrumentID
		out = append(out, model.MarginBalance{
			Currency:     ccy,
			InstrumentID: &id,
			Initial:      decimal.Zero,
			Maintenance:  decimal.Zero,
			UpdatedAt:    now,
		})
	}
	return out
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return fmt.Errorf("bybit: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return err
	}
	if category != "linear" {
		return fmt.Errorf("bybit: spot does not support leverage: %w", errs.ErrNotSupported)
	}
	return c.rest.SetLeverage(ctx, bybitsdk.SetLeverageRequest{
		Category:     category,
		Symbol:       inst.VenueSymbol,
		BuyLeverage:  leverage.String(),
		SellLeverage: leverage.String(),
	})
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("bybit: per-symbol margin mode mutation is not phase-one supported: %w", errs.ErrNotSupported)
}

func (c *accountClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Account: true},
			{Kind: enums.KindPerp, Account: true},
		},
		Reports:   contract.ReportCapabilities{PositionReports: true, AccountBalanceSnapshots: true, AccountStateSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: true, AccountState: true},
	}
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
