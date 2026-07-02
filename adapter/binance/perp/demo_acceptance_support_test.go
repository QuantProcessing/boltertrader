package perp

import (
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/enums"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

type demoAcceptanceSymbolSpec struct {
	VenueSymbol string
	PriceTick   decimal.Decimal
	SizeStep    decimal.Decimal
	MinQty      decimal.Decimal
	MinNotional decimal.Decimal
}

func normalizeDemoAcceptanceSymbol(symbol string) string {
	replacer := strings.NewReplacer("-", "", "_", "", "/", "", " ", "")
	return strings.ToUpper(replacer.Replace(symbol))
}

func demoAcceptanceSymbolSpecFromExchangeInfo(info *sdkperp.ExchangeInfoResponse, symbol string) (demoAcceptanceSymbolSpec, error) {
	if info == nil {
		return demoAcceptanceSymbolSpec{}, fmt.Errorf("missing exchange info")
	}
	want := normalizeDemoAcceptanceSymbol(symbol)
	for _, candidate := range info.Symbols {
		if normalizeDemoAcceptanceSymbol(candidate.Symbol) != want {
			continue
		}
		tick, step, minQty, minNotional := extractFilters(candidate.Filters)
		spec := demoAcceptanceSymbolSpec{
			VenueSymbol: candidate.Symbol,
			PriceTick:   tick,
			SizeStep:    step,
			MinQty:      minQty,
			MinNotional: minNotional,
		}
		if spec.PriceTick.IsZero() || spec.SizeStep.IsZero() || spec.MinQty.IsZero() || spec.MinNotional.IsZero() {
			return demoAcceptanceSymbolSpec{}, fmt.Errorf("symbol %s has incomplete exchange filters: %+v", candidate.Symbol, spec)
		}
		return spec, nil
	}
	return demoAcceptanceSymbolSpec{}, fmt.Errorf("symbol %s not found in exchange info", want)
}

func selectDemoAcceptanceOrderQuantity(spec demoAcceptanceSymbolSpec, configuredQty, maxNotional, refPrice decimal.Decimal) (decimal.Decimal, error) {
	return selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, refPrice, refPrice)
}

func selectDemoAcceptanceOrderQuantityForPriceBand(spec demoAcceptanceSymbolSpec, configuredQty, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) (decimal.Decimal, error) {
	if spec.SizeStep.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("symbol %s has invalid size step %s", spec.VenueSymbol, spec.SizeStep)
	}
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	if minNotionalPrice.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("min-notional reference price must be positive")
	}
	if maxNotionalPrice.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max-notional reference price must be positive")
	}

	qty := configuredQty
	if qty.IsZero() {
		minByNotional := spec.MinNotional.Div(minNotionalPrice)
		qty = ceilDecimalToStep(maxDecimal(spec.MinQty, minByNotional), spec.SizeStep)
	} else {
		if qty.LessThan(spec.MinQty) {
			return decimal.Zero, fmt.Errorf("configured quantity %s below min quantity %s for %s", qty, spec.MinQty, spec.VenueSymbol)
		}
		if !qty.Equal(ceilDecimalToStep(qty, spec.SizeStep)) {
			return decimal.Zero, fmt.Errorf("configured quantity %s is not aligned to step %s for %s", qty, spec.SizeStep, spec.VenueSymbol)
		}
	}

	minCheckedNotional := qty.Mul(minNotionalPrice)
	if minCheckedNotional.LessThan(spec.MinNotional) {
		return decimal.Zero, fmt.Errorf("quantity %s notional %s below min notional %s for %s", qty, minCheckedNotional, spec.MinNotional, spec.VenueSymbol)
	}
	maxCheckedNotional := qty.Mul(maxNotionalPrice)
	if maxCheckedNotional.GreaterThan(maxNotional) {
		return decimal.Zero, fmt.Errorf("quantity %s notional %s exceeds max notional %s for %s", qty, maxCheckedNotional, maxNotional, spec.VenueSymbol)
	}
	return qty, nil
}

func ceilDecimalToStep(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorDecimalToStep(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func maxDecimal(a, b decimal.Decimal) decimal.Decimal {
	if a.GreaterThan(b) {
		return a
	}
	return b
}

type demoAcceptanceCleanupMetadata struct {
	Symbol         string
	Side           string
	Quantity       decimal.Decimal
	VenueOrderIDs  []string
	ClientOrderIDs []string
	Exposure       decimal.Decimal
}

func (m demoAcceptanceCleanupMetadata) Remediation() string {
	return fmt.Sprintf(
		"Binance Demo cleanup failed: symbol=%s side=%s quantity=%s exposure=%s venueOrderIDs=%s clientOrderIDs=%s. Manually cancel open orders and flatten remaining exposure in Binance Futures Demo Trading.",
		m.Symbol,
		m.Side,
		m.Quantity,
		m.Exposure,
		strings.Join(m.VenueOrderIDs, ","),
		strings.Join(m.ClientOrderIDs, ","),
	)
}

type demoAcceptanceCleanupState struct {
	needed bool
	meta   demoAcceptanceCleanupMetadata
}

func newDemoAcceptanceCleanupState(symbol string, qty decimal.Decimal) demoAcceptanceCleanupState {
	return demoAcceptanceCleanupState{
		meta: demoAcceptanceCleanupMetadata{
			Symbol:   symbol,
			Quantity: qty,
		},
	}
}

func (s *demoAcceptanceCleanupState) Arm(side enums.OrderSide, clientID string) {
	s.needed = true
	s.meta.Side = side.String()
	if clientID != "" {
		s.meta.ClientOrderIDs = append(s.meta.ClientOrderIDs, clientID)
	}
}

func (s *demoAcceptanceCleanupState) RecordVenueOrderID(venueOrderID string) {
	if venueOrderID != "" {
		s.meta.VenueOrderIDs = append(s.meta.VenueOrderIDs, venueOrderID)
	}
}

func (s *demoAcceptanceCleanupState) SetExposure(exposure decimal.Decimal) {
	s.meta.Exposure = exposure
}

func (s *demoAcceptanceCleanupState) MarkClean() {
	s.needed = false
	s.meta.Exposure = decimal.Zero
}

func (s demoAcceptanceCleanupState) Needed() bool {
	return s.needed
}

func (s demoAcceptanceCleanupState) Metadata() demoAcceptanceCleanupMetadata {
	return s.meta
}
