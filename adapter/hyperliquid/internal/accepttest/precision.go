package accepttest

import (
	"math"

	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

var restingBuyDiscount = decimal.RequireFromString("0.50")

// RestingBuyPrice derives a conservative buy price that stays away from the
// touch while satisfying Hyperliquid's order price precision envelope.
func RestingBuyPrice(inst *model.Instrument, bestBid decimal.Decimal, spot bool) decimal.Decimal {
	return RoundDownOrderPrice(bestBid.Mul(restingBuyDiscount), spot, sizeDecimals(inst))
}

func RoundDownOrderPrice(price decimal.Decimal, spot bool, szDecimals int) decimal.Decimal {
	if !price.IsPositive() {
		return decimal.Zero
	}
	maxPriceDecimals := maxOrderPriceDecimals(spot, szDecimals)
	if price.GreaterThanOrEqual(decimal.NewFromInt(100000)) {
		return price.Truncate(0)
	}
	limited := truncateSignificantFigures(price, 5)
	limited = limited.Truncate(int32(maxPriceDecimals))
	if !limited.IsPositive() && maxPriceDecimals > 0 {
		return decimal.New(1, -int32(maxPriceDecimals))
	}
	return limited
}

func maxOrderPriceDecimals(spot bool, szDecimals int) int {
	total := 6
	if spot {
		total = 8
	}
	maxPriceDecimals := total - szDecimals
	if maxPriceDecimals < 0 {
		return 0
	}
	return maxPriceDecimals
}

func truncateSignificantFigures(value decimal.Decimal, sig int) decimal.Decimal {
	if sig <= 0 || !value.IsPositive() {
		return decimal.Zero
	}
	f, _ := value.Float64()
	if f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return decimal.Zero
	}
	magnitude := int(math.Floor(math.Log10(f)))
	scale := math.Pow10(sig - 1 - magnitude)
	if scale == 0 || math.IsInf(scale, 0) || math.IsNaN(scale) {
		return value.Truncate(0)
	}
	return decimal.NewFromFloat(math.Floor(f*scale) / scale)
}

func sizeDecimals(inst *model.Instrument) int {
	if inst == nil || !inst.SizeStep.IsPositive() {
		return 0
	}
	if exp := inst.SizeStep.Exponent(); exp < 0 {
		return int(-exp)
	}
	return 0
}
