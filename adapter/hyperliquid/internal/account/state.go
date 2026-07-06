package account

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

type StateInput struct {
	AccountID         string
	AccountMode       sdk.AccountAbstraction
	Perp              *sdkperp.PerpPosition
	Spot              *sdk.SpotClearinghouseState
	ProductScope      []enums.InstrumentKind
	Now               time.Time
	Details           map[string]string
	AccountModeSource string
}

func BuildAccountState(in StateInput) (model.AccountState, error) {
	if strings.TrimSpace(in.AccountID) == "" {
		return model.AccountState{}, fmt.Errorf("hyperliquid account state: account id required")
	}
	if in.Perp == nil {
		return model.AccountState{}, fmt.Errorf("hyperliquid account state: missing clearinghouseState")
	}
	if in.Spot == nil {
		return model.AccountState{}, fmt.Errorf("hyperliquid account state: missing spotClearinghouseState")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	modeInfo, err := modeInfoFromInput(in, now)
	if err != nil {
		return model.AccountState{}, err
	}
	balances, margins, err := balancesAndMargins(in.AccountID, in.Perp, in.Spot, now)
	if err != nil {
		return model.AccountState{}, err
	}
	return model.AccountState{
		AccountID:    in.AccountID,
		Venue:        instruments.VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USDC",
		Balances:     balances,
		Margins:      margins,
		ModeInfo:     modeInfo,
		Reported:     true,
		TsEvent:      now,
		TsInit:       now,
	}, nil
}

func modeInfoFromInput(in StateInput, now time.Time) (model.AccountModeInfo, error) {
	collateralMode := ""
	switch in.AccountMode {
	case sdk.AccountAbstractionDefault:
		collateralMode = "single_usdc"
	case sdk.AccountAbstractionUnifiedAccount:
		collateralMode = "unified"
	case sdk.AccountAbstractionPortfolioMargin:
		collateralMode = "portfolio_margin"
	default:
		return model.AccountModeInfo{}, fmt.Errorf("hyperliquid account state: unsupported account mode %q", in.AccountMode)
	}
	scope := append([]enums.InstrumentKind(nil), in.ProductScope...)
	if len(scope) == 0 {
		scope = []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}
	}
	source := strings.TrimSpace(in.AccountModeSource)
	if source == "" {
		source = "hyperliquid.account_state"
	}
	details := make(map[string]string, len(in.Details)+1)
	for k, v := range in.Details {
		details[k] = v
	}
	details["account_mode"] = string(in.AccountMode)
	return model.AccountModeInfo{
		Venue:          instruments.VenueName,
		AccountID:      in.AccountID,
		AccountMode:    string(in.AccountMode),
		MarginMode:     "cross",
		PositionMode:   "net",
		CollateralMode: collateralMode,
		ProductScope:   scope,
		Verified:       true,
		VerifiedAt:     now,
		Source:         source,
		Details:        details,
	}, nil
}

func balancesAndMargins(accountID string, perp *sdkperp.PerpPosition, spot *sdk.SpotClearinghouseState, now time.Time) ([]model.AccountBalance, []model.MarginBalance, error) {
	total, err := parsePerpDecimal("crossMarginSummary.totalRawUsd", perp.CrossMarginSummary.TotalRawUsd)
	if err != nil {
		return nil, nil, err
	}
	if total.IsZero() && strings.TrimSpace(perp.CrossMarginSummary.TotalRawUsd) == "" {
		total, err = parsePerpDecimal("marginSummary.totalRawUsd", perp.MarginSummary.TotalRawUsd)
		if err != nil {
			return nil, nil, err
		}
	}
	free, err := parsePerpDecimal("withdrawable", perp.Withdrawable)
	if err != nil {
		return nil, nil, err
	}
	if free.IsNegative() {
		free = decimal.Zero
	}
	marginUsed, err := parsePerpDecimal("crossMarginSummary.totalMarginUsed", perp.CrossMarginSummary.TotalMarginUsed)
	if err != nil {
		return nil, nil, err
	}
	if marginUsed.IsZero() && strings.TrimSpace(perp.CrossMarginSummary.TotalMarginUsed) == "" {
		marginUsed, err = parsePerpDecimal("marginSummary.totalMarginUsed", perp.MarginSummary.TotalMarginUsed)
		if err != nil {
			return nil, nil, err
		}
	}
	maintenance, err := parsePerpDecimal("crossMaintenanceMarginUsed", perp.CrossMaintenanceMarginUsed)
	if err != nil {
		return nil, nil, err
	}
	if maintenance.IsZero() && marginUsed.IsPositive() {
		maintenance = marginUsed
	}
	if total.IsNegative() || marginUsed.IsNegative() || maintenance.IsNegative() {
		return nil, nil, fmt.Errorf("hyperliquid account state: negative perp account value or margin")
	}
	if !total.IsNegative() && free.GreaterThan(total) {
		total = free
	}
	perpReflectsUSDC := !total.IsZero() || marginUsed.IsPositive() || free.IsPositive()
	balances := make([]model.AccountBalance, 0, len(spot.Balances)+1)
	if perpReflectsUSDC {
		balances = append(balances, model.AccountBalance{
			AccountID: accountID,
			Currency:  "USDC",
			Total:     total,
			Free:      free,
			Available: free,
			Locked:    marginUsed,
			UpdatedAt: now,
		})
	}
	for _, raw := range spot.Balances {
		balance, err := spotBalance(accountID, raw, now)
		if err != nil {
			return nil, nil, err
		}
		if balance.Total.IsZero() {
			continue
		}
		if perpReflectsUSDC && strings.EqualFold(balance.Currency, "USDC") {
			continue
		}
		balances = append(balances, balance)
	}
	var margins []model.MarginBalance
	if marginUsed.IsPositive() {
		margins = append(margins, model.MarginBalance{
			Currency:    "USDC",
			Initial:     marginUsed,
			Maintenance: maintenance,
			UpdatedAt:   now,
		})
	}
	return balances, margins, nil
}

func spotBalance(accountID string, raw sdk.SpotBalance, now time.Time) (model.AccountBalance, error) {
	total, err := parseRequiredDecimal("spot.total", raw.Total)
	if err != nil {
		return model.AccountBalance{}, err
	}
	locked, err := parseRequiredDecimal("spot.hold", raw.Hold)
	if err != nil {
		return model.AccountBalance{}, err
	}
	if total.IsNegative() || locked.IsNegative() {
		return model.AccountBalance{}, fmt.Errorf("hyperliquid account state: negative spot balance for %s", raw.Coin)
	}
	free := total.Sub(locked)
	if free.IsNegative() {
		return model.AccountBalance{}, fmt.Errorf("hyperliquid account state: spot hold exceeds total for %s", raw.Coin)
	}
	return model.AccountBalance{
		AccountID: accountID,
		Currency:  raw.Coin,
		Total:     total,
		Free:      free,
		Available: free,
		Locked:    locked,
		UpdatedAt: now,
	}, nil
}

func parsePerpDecimal(field, value string) (decimal.Decimal, error) {
	if strings.TrimSpace(value) == "" {
		return decimal.Zero, nil
	}
	return parseRequiredDecimal(field, value)
}

func parseRequiredDecimal(field, value string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return decimal.Zero, fmt.Errorf("hyperliquid account state: parse %s=%q: %w", field, value, err)
	}
	return d, nil
}
