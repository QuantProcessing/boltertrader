package backtest

import (
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// one is the multiplicative identity, reused to avoid reallocating it.
var one = decimal.NewFromInt(1)

// SlippageModel adjusts the execution price of a marketable (taker) order away
// from a reference price, always in the adverse direction for the order's side:
// buys fill higher, sells fill lower. Passive (resting limit) fills are never
// slipped — they execute at their limit price by construction.
type SlippageModel interface {
	// Apply returns the adjusted fill price for a taker order of the given side
	// at the reference price.
	Apply(side enums.OrderSide, refPrice decimal.Decimal) decimal.Decimal
}

// BpsSlippage returns a SlippageModel that moves the fill price by a fixed
// number of basis points against the taker (1 bp = 0.01%). bps of 5 means a buy
// fills 0.05% above and a sell 0.05% below the reference price.
func BpsSlippage(bps decimal.Decimal) SlippageModel { return bpsSlippage{bps: bps} }

type bpsSlippage struct{ bps decimal.Decimal }

func (s bpsSlippage) Apply(side enums.OrderSide, ref decimal.Decimal) decimal.Decimal {
	if s.bps.IsZero() || ref.IsZero() {
		return ref
	}
	frac := s.bps.Div(decimal.NewFromInt(10000))
	if side == enums.SideSell {
		return ref.Mul(one.Sub(frac))
	}
	return ref.Mul(one.Add(frac))
}

// applySlippage returns the taker fill price after the configured slippage
// model, or the reference price unchanged when no model is set.
func (v *Venue) applySlippage(side enums.OrderSide, ref decimal.Decimal) decimal.Decimal {
	if v.cfg.Slippage == nil {
		return ref
	}
	return v.cfg.Slippage.Apply(side, ref)
}

// feeRate resolves the fee rate for a fill by its liquidity side. Maker/Taker
// rates take precedence; the legacy single FeeRate is the fallback when the
// specific rate is unset (zero), preserving older configs that set only FeeRate.
func (v *Venue) feeRate(liq enums.LiquiditySide) decimal.Decimal {
	if liq == enums.LiqMaker {
		if !v.cfg.MakerFeeRate.IsZero() {
			return v.cfg.MakerFeeRate
		}
		return v.cfg.FeeRate
	}
	if !v.cfg.TakerFeeRate.IsZero() {
		return v.cfg.TakerFeeRate
	}
	return v.cfg.FeeRate
}

// multiplier returns the contract multiplier for an instrument, defaulting to 1
// for unregistered instruments or a non-positive configured value (linear perp).
func (v *Venue) multiplier(id model.InstrumentID) decimal.Decimal {
	if inst, ok := v.instruments[id.String()]; ok && inst.ContractMultiplier.IsPositive() {
		return inst.ContractMultiplier
	}
	return one
}

// notional returns the value of qty contracts at px: price * qty * multiplier.
func (v *Venue) notional(id model.InstrumentID, px, qty decimal.Decimal) decimal.Decimal {
	return px.Mul(qty).Mul(v.multiplier(id))
}
