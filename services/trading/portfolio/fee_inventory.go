package portfolio

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// QuantityDelta is the signed change in owned inventory/contracts, not the
// gross execution quantity. Instrument facts must come from Gateway.
func QuantityDelta(fill Fill) (decimal.Decimal, error) {
	quantity, qErr := decimal.NewFromString(fill.Quantity)
	fee, feeErr := decimal.NewFromString(fill.Fee)
	if qErr != nil || feeErr != nil || (!fee.IsZero() && fill.FeeCurrency == "") || !quantity.IsPositive() || (fill.Side != SideBuy && fill.Side != SideSell) {
		return decimal.Zero, fmt.Errorf("invalid inventory fill")
	}
	if fill.Side == SideSell {
		quantity = quantity.Neg()
	}
	switch fill.InstrumentProduct {
	case "spot":
		if fill.BaseAsset == "" || fill.BaseAsset == fill.SettlementCurrency {
			return decimal.Zero, fmt.Errorf("spot inventory requires instrument assets")
		}
		if fill.FeeCurrency == fill.BaseAsset {
			quantity = quantity.Sub(fee)
		}
	case "perp": // Collateral fees never change the number of contracts.
	case "":
		// Legacy quote/settlement fees do not affect gross quantity. A different
		// currency could be a spot base fee; never infer that from a symbol.
		if !fee.IsZero() && fill.FeeCurrency != fill.SettlementCurrency {
			return decimal.Zero, fmt.Errorf("inventory fee currency needs instrument facts")
		}
	default:
		return decimal.Zero, fmt.Errorf("unsupported instrument accounting")
	}
	return quantity, nil
}

// ReplayCompatible allows adding formerly absent instrument metadata only when
// it cannot change the historical accounting result. A legacy base-fee fill
// needs an explicit rebuild; reapplying it silently would duplicate the trade.
func ReplayCompatible(recorded string, fill Fill) bool {
	if recorded == FillFingerprint(fill) {
		return true
	}
	legacy := fill
	legacy.InstrumentProduct, legacy.BaseAsset = "", ""
	if recorded != FillFingerprint(legacy) {
		return false
	}
	fee, err := decimal.NewFromString(fill.Fee)
	return err == nil && (fee.IsZero() || fill.InstrumentProduct != "spot" || fill.FeeCurrency != fill.BaseAsset)
}
