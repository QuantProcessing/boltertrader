package perp

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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest      *sdkperp.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]
	streaming bool
}

func newAccountClient(rest *sdkperp.Client, provider *instrumentProvider, clk clock.Clock, accountID string) *accountClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	if accountID == "" {
		accountID = AccountIDDefault
	}
	return &accountClient{rest: rest, provider: provider, clk: clk, accountID: accountID, stream: wsstream.New[contract.AccountEnvelope](256)}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
		Reports:   contract.ReportCapabilities{PositionReports: true, AccountBalanceSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: c.streaming, AccountState: false},
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	if c.rest == nil {
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	account, err := c.rest.GetAccount(ctx)
	if err != nil {
		return nil, mapAsterError(err)
	}
	if err := validateAccountResponseDecimals(account); err != nil {
		return nil, err
	}
	return balancesFromResponse(account, c.accountID, c.clk.Now()), nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	if c.rest == nil {
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	positions, err := c.rest.GetPositionRisk(ctx, "")
	if err != nil {
		return nil, mapAsterError(err)
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(positions))
	for _, row := range positions {
		if err := validatePositionRiskDecimals(row); err != nil {
			return nil, err
		}
		if side := strings.ToUpper(strings.TrimSpace(row.PositionSide)); side != "" && side != "BOTH" {
			return nil, fmt.Errorf("aster perp: position side %q requires unsupported hedge mode: %w", row.PositionSide, errs.ErrNotSupported)
		}
		if dec(row.PositionAmt).IsZero() {
			continue
		}
		id, ok := c.provider.resolveKnownVenueSymbol(row.Symbol)
		if !ok {
			return nil, fmt.Errorf("aster perp: unresolved position risk symbol %q", row.Symbol)
		}
		pos := positionFromRisk(row, id, c.accountID, now)
		if pos.InstrumentID.Symbol == "" {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	if c.rest == nil {
		return model.AccountState{}, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	account, err := c.rest.GetAccount(ctx)
	if err != nil {
		return model.AccountState{}, mapAsterError(err)
	}
	if err := validateAccountResponseDecimals(account); err != nil {
		return model.AccountState{}, err
	}
	state := accountStateFromResponse(account, c.accountID, c.clk.Now())
	if err := state.Validate(); err != nil {
		return model.AccountState{}, err
	}
	return state, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	return fmt.Errorf("aster perp: leverage mutation is not implemented in Story 5: %w", errs.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("aster perp: margin mode mutation is not implemented in Story 5: %w", errs.ErrNotSupported)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}
func (c *accountClient) Close() error { c.stream.Close(); return nil }

func accountStateFromResponse(account *sdkperp.AccountResponse, accountID string, fallback time.Time) model.AccountState {
	ts := fallback
	if account != nil && account.UpdateTime > 0 {
		ts = timeFromMillis(account.UpdateTime)
	}
	return model.AccountState{
		AccountID:    accountID,
		Venue:        VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances:     balancesFromResponse(account, accountID, fallback),
		Margins:      marginsFromResponse(account, fallback),
		Summary:      summaryFromResponse(account, fallback),
		Reported:     true,
		EventID:      model.AccountStateEventID(VenueName, accountID, ts),
		TsEvent:      ts,
		TsInit:       fallback,
	}
}

func balancesFromResponse(account *sdkperp.AccountResponse, accountID string, fallback time.Time) []model.AccountBalance {
	if account == nil {
		return nil
	}
	if len(account.Assets) == 0 {
		free := dec(account.AvailableBalance)
		total := firstNonZero(dec(account.TotalWalletBalance), dec(account.TotalMarginBalance), free)
		return []model.AccountBalance{{AccountID: accountID, Currency: "USDT", Total: total, Free: free, Locked: positiveSub(total, free), UpdatedAt: fallback}}
	}
	out := make([]model.AccountBalance, 0, len(account.Assets))
	for _, asset := range account.Assets {
		if asset.Asset == "" {
			continue
		}
		ts := fallback
		if asset.UpdateTime > 0 {
			ts = timeFromMillis(asset.UpdateTime)
		}
		free := dec(asset.AvailableBalance)
		total := firstNonZero(dec(asset.WalletBalance), dec(asset.MarginBalance), free)
		out = append(out, model.AccountBalance{AccountID: accountID, Currency: asset.Asset, Total: total, Free: free, Locked: positiveSub(total, free), UpdatedAt: ts})
	}
	return out
}

func marginsFromResponse(account *sdkperp.AccountResponse, fallback time.Time) []model.MarginBalance {
	if account == nil {
		return nil
	}
	out := make([]model.MarginBalance, 0, len(account.Assets)+len(account.Positions))
	for _, asset := range account.Assets {
		currency := asset.Asset
		if currency == "" {
			currency = "USDT"
		}
		ts := fallback
		if asset.UpdateTime > 0 {
			ts = timeFromMillis(asset.UpdateTime)
		}
		out = append(out, model.MarginBalance{Currency: currency, Initial: dec(asset.InitialMargin), Maintenance: dec(asset.MaintMargin), UpdatedAt: ts})
	}
	return out
}

func summaryFromResponse(account *sdkperp.AccountResponse, fallback time.Time) *model.AccountSummary {
	if account == nil {
		return nil
	}
	ts := fallback
	if account.UpdateTime > 0 {
		ts = timeFromMillis(account.UpdateTime)
	}
	return &model.AccountSummary{
		SettlementCurrency:  "USDT",
		Equity:              firstNonZero(dec(account.TotalMarginBalance), dec(account.TotalWalletBalance)),
		AvailableCollateral: dec(account.AvailableBalance),
		UpdatedAt:           ts,
	}
}

func validateAccountResponseDecimals(account *sdkperp.AccountResponse) error {
	if account == nil {
		return fmt.Errorf("aster perp: account response is required")
	}
	for field, raw := range map[string]string{
		"availableBalance":      account.AvailableBalance,
		"totalWalletBalance":    account.TotalWalletBalance,
		"totalMarginBalance":    account.TotalMarginBalance,
		"totalInitialMargin":    account.TotalInitialMargin,
		"totalMaintMargin":      account.TotalMaintMargin,
		"totalUnrealizedProfit": account.TotalUnrealizedProfit,
	} {
		value, err := parseRequiredSDKDecimal(field, raw)
		if err != nil {
			return fmt.Errorf("aster perp: account response: %w", err)
		}
		if field != "totalUnrealizedProfit" && value.IsNegative() {
			return fmt.Errorf("aster perp: account response %s is negative", field)
		}
	}
	for _, asset := range account.Assets {
		if asset.Asset == "" {
			return fmt.Errorf("aster perp: account asset currency is required")
		}
		for field, raw := range map[string]string{
			"walletBalance":    asset.WalletBalance,
			"availableBalance": asset.AvailableBalance,
			"initialMargin":    asset.InitialMargin,
			"maintMargin":      asset.MaintMargin,
			"marginBalance":    asset.MarginBalance,
		} {
			value, err := parseRequiredSDKDecimal(field, raw)
			if err != nil {
				return fmt.Errorf("aster perp: account asset %s: %w", asset.Asset, err)
			}
			if value.IsNegative() {
				return fmt.Errorf("aster perp: account asset %s has negative %s", asset.Asset, field)
			}
		}
	}
	return nil
}

func validatePositionRiskDecimals(row sdkperp.PositionRiskResponse) error {
	if strings.TrimSpace(row.Symbol) == "" {
		return fmt.Errorf("aster perp: position risk symbol is required")
	}
	for field, raw := range map[string]string{
		"positionAmt":      row.PositionAmt,
		"entryPrice":       row.EntryPrice,
		"markPrice":        row.MarkPrice,
		"unRealizedProfit": row.UnRealizedProfit,
		"leverage":         row.Leverage,
	} {
		value, err := parseRequiredSDKDecimal(field, raw)
		if err != nil {
			return fmt.Errorf("aster perp: position risk %s: %w", row.Symbol, err)
		}
		if field != "positionAmt" && field != "unRealizedProfit" && value.IsNegative() {
			return fmt.Errorf("aster perp: position risk %s has negative %s", row.Symbol, field)
		}
	}
	return nil
}

func positionFromRisk(row sdkperp.PositionRiskResponse, id model.InstrumentID, accountID string, fallback time.Time) model.Position {
	return model.Position{AccountID: accountID, InstrumentID: id, Side: positionSideFromAster(row.PositionSide, dec(row.PositionAmt)), Quantity: dec(row.PositionAmt), EntryPrice: dec(row.EntryPrice), MarkPrice: dec(row.MarkPrice), UnrealizedPnL: dec(row.UnRealizedProfit), Leverage: dec(row.Leverage), UpdatedAt: firstNonZeroTime(timeFromMillis(row.UpdateTime), fallback)}
}

func positionSideFromAster(value string, qty decimal.Decimal) enums.PositionSide {
	switch strings.ToUpper(value) {
	case "LONG":
		return enums.PosLong
	case "SHORT":
		return enums.PosShort
	default:
		return enums.PosNet
	}
}

func positiveSub(total, free decimal.Decimal) decimal.Decimal {
	if total.GreaterThan(free) {
		return total.Sub(free)
	}
	return decimal.Zero
}
