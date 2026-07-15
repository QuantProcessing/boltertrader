package account

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

type StateInput struct {
	AccountID   string
	AccountMode sdk.AccountAbstraction
	Perp        *sdkperp.PerpPosition
	PerpDexes   []PerpDexState
	Spot        *sdk.SpotClearinghouseState
	Now         time.Time
}

// PerpDexState is one configured HIP-3 clearinghouse snapshot and the
// settlement currency resolved from that DEX's instrument metadata.
type PerpDexState struct {
	Dex        string
	Collateral string
	State      *sdkperp.PerpPosition
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
	if err := validateAccountMode(in.AccountMode); err != nil {
		return model.AccountState{}, err
	}
	balances, margins, err := balancesAndMargins(in.AccountID, in.AccountMode, in.Perp, in.PerpDexes, in.Spot, now)
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
		Reported:     true,
		EventID:      model.AccountStateEventID(instruments.VenueName, in.AccountID, now),
		TsEvent:      now,
		TsInit:       now,
	}, nil
}

func validateAccountMode(mode sdk.AccountAbstraction) error {
	switch mode {
	case sdk.AccountAbstractionDefault:
	case sdk.AccountAbstractionUnifiedAccount:
	case sdk.AccountAbstractionPortfolioMargin:
	default:
		return fmt.Errorf("hyperliquid account state: unsupported account mode %q", mode)
	}
	return nil
}

type perpTotals struct {
	total       decimal.Decimal
	free        decimal.Decimal
	marginUsed  decimal.Decimal
	maintenance decimal.Decimal
	reflects    bool
}

func balancesAndMargins(accountID string, accountMode sdk.AccountAbstraction, perp *sdkperp.PerpPosition, perpDexes []PerpDexState, spot *sdk.SpotClearinghouseState, now time.Time) ([]model.AccountBalance, []model.MarginBalance, error) {
	perpByCurrency := make(map[string]perpTotals, len(perpDexes)+1)
	perpPoolsByCurrency := make(map[string]int, len(perpDexes)+1)
	perpOrder := make([]string, 0, len(perpDexes)+1)
	addPerp := func(label, currency string, state *sdkperp.PerpPosition) error {
		currency = canonicalCurrency(currency)
		if currency == "" {
			return fmt.Errorf("hyperliquid account state: %s collateral required", label)
		}
		if state == nil {
			return fmt.Errorf("hyperliquid account state: missing %s clearinghouseState", label)
		}
		totals, err := totalsFromPerp(state)
		if err != nil {
			return fmt.Errorf("hyperliquid account state: %s: %w", label, err)
		}
		if !totals.reflects {
			return nil
		}
		perpPoolsByCurrency[currency]++
		current, ok := perpByCurrency[currency]
		if !ok {
			perpOrder = append(perpOrder, currency)
		}
		current.total = current.total.Add(totals.total)
		current.free = current.free.Add(totals.free)
		current.marginUsed = current.marginUsed.Add(totals.marginUsed)
		current.maintenance = current.maintenance.Add(totals.maintenance)
		current.reflects = true
		perpByCurrency[currency] = current
		return nil
	}
	if err := addPerp("default perp", "USDC", perp); err != nil {
		return nil, nil, err
	}
	for _, dex := range perpDexes {
		label := "HIP-3 DEX " + strings.TrimSpace(dex.Dex)
		if strings.TrimSpace(dex.Dex) == "" {
			label = "HIP-3 DEX"
		}
		if err := addPerp(label, dex.Collateral, dex.State); err != nil {
			return nil, nil, err
		}
	}
	if accountMode == sdk.AccountAbstractionDefault {
		for _, currency := range perpOrder {
			if pools := perpPoolsByCurrency[currency]; pools > 1 {
				return nil, nil, fmt.Errorf("hyperliquid account state: cannot represent %d independent %s perp margin pools in one currency balance", pools, currency)
			}
		}
	}

	balances := make([]model.AccountBalance, 0, len(perpOrder)+len(spot.Balances))
	balanceIndex := make(map[string]int, len(perpOrder)+len(spot.Balances))
	usePerpFunding := accountMode == sdk.AccountAbstractionDefault
	if usePerpFunding {
		for _, currency := range perpOrder {
			totals := perpByCurrency[currency]
			balanceIndex[currency] = len(balances)
			balances = append(balances, model.AccountBalance{
				AccountID: accountID,
				Currency:  currency,
				Total:     totals.total,
				Free:      totals.free,
				Locked:    totals.marginUsed,
				UpdatedAt: now,
			})
		}
	}
	spotBalances, err := SpotBalances(accountID, *spot, now)
	if err != nil {
		return nil, nil, err
	}
	for _, balance := range spotBalances {
		if balance.Total.IsZero() {
			continue
		}
		currency := canonicalCurrency(balance.Currency)
		if _, ok := perpByCurrency[currency]; usePerpFunding && ok {
			continue
		}
		balance.Currency = currency
		if idx, ok := balanceIndex[currency]; ok {
			balances[idx].Total = balances[idx].Total.Add(balance.Total)
			balances[idx].Free = balances[idx].Free.Add(balance.Free)
			balances[idx].Locked = balances[idx].Locked.Add(balance.Locked)
			continue
		}
		balanceIndex[currency] = len(balances)
		balances = append(balances, balance)
	}

	margins := make([]model.MarginBalance, 0, len(perpOrder))
	for _, currency := range perpOrder {
		totals := perpByCurrency[currency]
		if !totals.marginUsed.IsPositive() {
			continue
		}
		margins = append(margins, model.MarginBalance{
			Currency:    currency,
			Initial:     totals.marginUsed,
			Maintenance: totals.maintenance,
			UpdatedAt:   now,
		})
	}
	return balances, margins, nil
}

func totalsFromPerp(perp *sdkperp.PerpPosition) (perpTotals, error) {
	total, err := parsePerpDecimal("crossMarginSummary.totalRawUsd", perp.CrossMarginSummary.TotalRawUsd)
	if err != nil {
		return perpTotals{}, err
	}
	if total.IsZero() && strings.TrimSpace(perp.CrossMarginSummary.TotalRawUsd) == "" {
		total, err = parsePerpDecimal("marginSummary.totalRawUsd", perp.MarginSummary.TotalRawUsd)
		if err != nil {
			return perpTotals{}, err
		}
	}
	free, err := parsePerpDecimal("withdrawable", perp.Withdrawable)
	if err != nil {
		return perpTotals{}, err
	}
	if free.IsNegative() {
		free = decimal.Zero
	}
	marginUsed, err := parsePerpDecimal("crossMarginSummary.totalMarginUsed", perp.CrossMarginSummary.TotalMarginUsed)
	if err != nil {
		return perpTotals{}, err
	}
	if marginUsed.IsZero() && strings.TrimSpace(perp.CrossMarginSummary.TotalMarginUsed) == "" {
		marginUsed, err = parsePerpDecimal("marginSummary.totalMarginUsed", perp.MarginSummary.TotalMarginUsed)
		if err != nil {
			return perpTotals{}, err
		}
	}
	maintenance, err := parsePerpDecimal("crossMaintenanceMarginUsed", perp.CrossMaintenanceMarginUsed)
	if err != nil {
		return perpTotals{}, err
	}
	if maintenance.IsZero() && marginUsed.IsPositive() {
		maintenance = marginUsed
	}
	if total.IsNegative() || marginUsed.IsNegative() || maintenance.IsNegative() {
		return perpTotals{}, fmt.Errorf("negative perp account value or margin")
	}
	if !total.IsNegative() && free.GreaterThan(total) {
		total = free
	}
	return perpTotals{
		total:       total,
		free:        free,
		marginUsed:  marginUsed,
		maintenance: maintenance,
		reflects:    !total.IsZero() || marginUsed.IsPositive() || free.IsPositive(),
	}, nil
}

func canonicalCurrency(currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency))
}

// SpotBalances converts one authoritative spotClearinghouseState snapshot into
// strict core balances. Callers that consume replacement snapshots must still
// emit zeroes for currencies omitted from a later snapshot.
func SpotBalances(accountID string, state sdk.SpotClearinghouseState, now time.Time) ([]model.AccountBalance, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("hyperliquid account state: spot snapshot timestamp required")
	}
	seen := make(map[string]struct{}, len(state.Balances))
	out := make([]model.AccountBalance, 0, len(state.Balances))
	for _, raw := range state.Balances {
		currency := canonicalCurrency(raw.Coin)
		if currency == "" {
			return nil, fmt.Errorf("hyperliquid account state: spot balance currency required")
		}
		if _, ok := seen[currency]; ok {
			return nil, fmt.Errorf("hyperliquid account state: duplicate spot balance for %s", currency)
		}
		seen[currency] = struct{}{}
		balance, err := spotBalance(accountID, raw, now)
		if err != nil {
			return nil, err
		}
		balance.Currency = currency
		out = append(out, balance)
	}
	return out, nil
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
