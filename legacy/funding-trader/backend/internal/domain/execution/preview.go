package execution

import (
	"errors"
	"math/big"
	"time"

	"github.com/shopspring/decimal"
)

const previewTTL = 30 * time.Second

var (
	minCapitalUSDT        = decimal.NewFromInt(100)
	oneHundred            = decimal.NewFromInt(100)
	seven                 = decimal.NewFromInt(7)
	threeHundredSixtyFive = decimal.NewFromInt(365)
	fixedMaxSlippagePct   = decimal.RequireFromString("0.2")
	capacityBlockerPct    = decimal.NewFromInt(80)
	capacityWarningPct    = decimal.NewFromInt(50)
	bufferBlockerPct      = decimal.NewFromInt(30)
	bufferWarningPct      = decimal.NewFromInt(50)
	stabilityWarningPct   = decimal.NewFromInt(80)
)

func Preview(input PreviewInput) (PreviewResult, error) {
	if input.PreviewedAt.IsZero() {
		return PreviewResult{}, errors.New("previewed at is required")
	}
	result := PreviewResult{
		PreviewID:         input.PreviewID,
		SnapshotID:        input.SnapshotID,
		PrincipalID:       input.PrincipalID,
		Mode:              input.Mode,
		Operation:         input.Operation,
		OpportunityID:     input.OpportunityID,
		PositionID:        input.PositionID,
		ParentExecutionID: input.ParentExecutionID,
		LineageID:         input.LineageID,
		State:             StatePreviewed,
		PreviewedAt:       input.PreviewedAt.UTC(),
		ExpiresAt:         input.PreviewedAt.UTC().Add(previewTTL),
		CapitalUSDT:       input.CapitalUSDT,
		Leverage:          input.Leverage,
		MaxSlippagePct:    fixedMaxSlippagePct,
		FeesUSDT:          input.FeesUSDT,
		SlippageUSDT:      input.SlippageUSDT,
		SingleLegNotional: input.CapitalUSDT.Mul(decimal.NewFromInt(int64(input.Leverage))),
	}
	result.ExpectedNetAprPct = input.GrossAprPct
	result.GrossProfit7d = result.SingleLegNotional.
		Mul(input.GrossAprPct).
		Div(oneHundred).
		Mul(seven).
		Div(threeHundredSixtyFive)
	result.NetProfit7d = result.GrossProfit7d.Sub(input.FeesUSDT).Sub(input.SlippageUSDT)
	if result.SingleLegNotional.IsPositive() {
		result.ExpectedNetAprPct = result.NetProfit7d.
			Div(result.SingleLegNotional).
			Mul(threeHundredSixtyFive).
			Div(seven).
			Mul(oneHundred)
	}
	longSide, shortSide := SideBuy, SideSell
	if input.Operation == OperationClose {
		longSide, shortSide = SideSell, SideBuy
	}
	if input.Operation == OperationClose {
		result.PlannedLegs = []PlannedLeg{
			planLegQuantity(input.Long, LegRoleLong, longSide, input.CurrentLongQuantity, true),
			planLegQuantity(input.Short, LegRoleShort, shortSide, input.CurrentShortQuantity, true),
		}
	} else {
		quantity := commonOpenQuantity(input, result.SingleLegNotional)
		result.PlannedLegs = []PlannedLeg{
			planLegQuantity(input.Long, LegRoleLong, longSide, quantity, false),
			planLegQuantity(input.Short, LegRoleShort, shortSide, quantity, false),
		}
	}
	if len(result.PlannedLegs) == 2 {
		result.InitialLongQuantity = result.PlannedLegs[0].Quantity
		result.InitialShortQuantity = result.PlannedLegs[1].Quantity
		result.GrossExposure = result.PlannedLegs[0].Notional.Add(result.PlannedLegs[1].Notional)
	}

	for _, blocker := range input.OriginalBlockers {
		if blocker.Code == "" {
			blocker.Code = ReasonOriginalBlocker
		}
		result.block(blocker.Code, blocker.Message)
	}
	result.addValidation(input)
	result.addRiskFindings(input)
	result.CanSubmit = !result.Blocked()
	return result, nil
}

func (result *PreviewResult) addValidation(input PreviewInput) {
	if input.PreviewID == "" {
		result.block(ReasonInvalidPreviewID, "preview id is required")
	}
	if input.SnapshotID == "" {
		result.block(ReasonInvalidSnapshotID, "snapshot id is required")
	}
	if input.Operation != OperationOpen && input.Operation != OperationClose {
		result.block(ReasonInvalidOperation, "operation must be open or close")
	}
	if input.Operation == OperationOpen {
		if input.OpportunityID == "" {
			result.block(ReasonInvalidOpportunityID, "open requires opportunity id")
		}
		if input.PositionID != "" {
			result.block(ReasonInvalidPositionID, "open must not carry position id")
		}
	}
	if input.Operation == OperationClose && input.PositionID == "" {
		result.block(ReasonInvalidPositionID, "close requires position id")
	}
	if input.Operation == OperationOpen && (input.CapitalUSDT.LessThan(minCapitalUSDT) || input.CapitalUSDT.GreaterThan(input.AvailableCapitalUSDT)) {
		result.block(ReasonInvalidCapital, "capital must be at least 100 USDT and not exceed available capital")
	}
	if input.Leverage != 1 && input.Leverage != 2 {
		result.block(ReasonInvalidLeverage, "leverage must be 1 or 2")
	}
	if input.PreviewedAt.Sub(input.DataObservedAt) > 3*time.Second || input.DataObservedAt.After(input.PreviewedAt) {
		result.block(ReasonStaleData, "market data age must be no more than 3 seconds")
	}
	if !input.RiskVerifiable {
		result.warn(ReasonRiskUnverifiable, "Gateway risk limits are unavailable; capital and leverage remain user controlled")
	}
	if !input.AccountVerifiable {
		result.block(ReasonAccountUnverified, "account state is not verifiable")
	}
	if !input.Long.LegIdentity.Valid() || !input.Short.LegIdentity.Valid() {
		result.block(ReasonAccountUnverified, "leg route identity is incomplete")
	}
	if !input.RulesVerifiable {
		result.block(ReasonRulesUnverified, "instrument rules are not verifiable")
	}
	if input.Operation == OperationOpen && !result.NetProfit7d.IsPositive() {
		result.block(ReasonNonPositiveProfit, "net 7 day profit must be positive")
	}
	for _, leg := range result.PlannedLegs {
		legInput := inputForRole(input, leg.Role)
		if !leg.Quantity.IsPositive() ||
			!quantityConformsToIncrement(leg.Quantity, legInput.QuantityIncrement) ||
			leg.Quantity.LessThan(legInput.MinQuantity) ||
			leg.Quantity.Mul(leg.MarkPrice).LessThan(legInput.MinNotional) {
			result.block(ReasonMinTradeRules, "rounded quantity cannot satisfy min quantity or min notional")
			break
		}
	}
	if input.Operation == OperationClose {
		if !input.CurrentPositionVerifiable {
			result.block(ReasonPositionUnverifiable, "current position exposure must be verifiable for close")
		} else {
			for _, leg := range result.PlannedLegs {
				current := input.CurrentLongQuantity
				if leg.Role == LegRoleShort {
					current = input.CurrentShortQuantity
				}
				if leg.Quantity.GreaterThan(current) {
					result.block(ReasonCloseExceedsExposure, "close quantity cannot exceed current exposure")
					break
				}
			}
		}
	}
}

func (result *PreviewResult) addRiskFindings(input PreviewInput) {
	if input.CapacityNotional.IsPositive() && result.SingleLegNotional.IsPositive() {
		usage := result.SingleLegNotional.Div(input.CapacityNotional).Mul(oneHundred)
		if usage.GreaterThan(capacityBlockerPct) {
			result.block(ReasonCapacityUsage, "capacity utilization is above 80%")
		}
		if usage.GreaterThan(capacityWarningPct) {
			result.warn(ReasonCapacityUsage, "capacity utilization is above 50%")
		}
	} else {
		result.block(ReasonCapacityUsage, "capacity cannot be verified")
	}
	if input.Operation == OperationOpen && input.LiquidationBufferPct.LessThan(bufferBlockerPct) {
		result.block(ReasonLiquidationBuffer, "liquidation buffer is below 30%")
	} else if input.Operation == OperationOpen && input.LiquidationBufferPct.LessThan(bufferWarningPct) {
		result.warn(ReasonLiquidationBuffer, "liquidation buffer is below 50%")
	}
	if input.StabilityPct.LessThan(stabilityWarningPct) {
		result.warn(ReasonLowStability, "route stability is below 80%")
	}
	if input.RecentReversal {
		result.warn(ReasonRecentReversal, "recent funding history reversed against the route")
	}
}

func planLeg(input PreviewLegInput, role LegRole, side Side, notional decimal.Decimal, reduceOnly bool) PlannedLeg {
	quantity := decimal.Zero
	if input.MarkPrice.IsPositive() && input.QuantityIncrement.IsPositive() {
		quantity = roundDown(notional.Div(input.MarkPrice), input.QuantityIncrement)
	}
	return PlannedLeg{
		LegIdentity:         input.LegIdentity,
		Role:                role,
		Side:                side,
		Symbol:              input.Symbol,
		GatewayInstrumentID: input.GatewayInstrumentID,
		MarkPrice:           input.MarkPrice,
		Notional:            notional,
		Quantity:            quantity,
		ReduceOnly:          reduceOnly,
	}
}

func planLegQuantity(input PreviewLegInput, role LegRole, side Side, quantity decimal.Decimal, reduceOnly bool) PlannedLeg {
	return PlannedLeg{
		LegIdentity:         input.LegIdentity,
		Role:                role,
		Side:                side,
		Symbol:              input.Symbol,
		GatewayInstrumentID: input.GatewayInstrumentID,
		MarkPrice:           input.MarkPrice,
		Notional:            quantity.Mul(input.MarkPrice),
		Quantity:            quantity,
		ReduceOnly:          reduceOnly,
	}
}

func commonOpenQuantity(input PreviewInput, singleLegBudget decimal.Decimal) decimal.Decimal {
	if !input.Long.MarkPrice.IsPositive() || !input.Short.MarkPrice.IsPositive() {
		return decimal.Zero
	}
	increment := commonIncrement(input.Long.QuantityIncrement, input.Short.QuantityIncrement)
	if !increment.IsPositive() {
		return decimal.Zero
	}
	longBudgetQuantity := singleLegBudget.Div(input.Long.MarkPrice)
	shortBudgetQuantity := singleLegBudget.Div(input.Short.MarkPrice)
	maxQuantity := decimal.Min(longBudgetQuantity, shortBudgetQuantity)
	return roundDown(maxQuantity, increment)
}

func commonIncrement(left, right decimal.Decimal) decimal.Decimal {
	if !left.IsPositive() || !right.IsPositive() {
		return decimal.Zero
	}
	scale := -left.Exponent()
	if -right.Exponent() > scale {
		scale = -right.Exponent()
	}
	leftInt := scaledInt(left, scale)
	rightInt := scaledInt(right, scale)
	if leftInt.Sign() <= 0 || rightInt.Sign() <= 0 {
		return decimal.Zero
	}
	gcd := new(big.Int).GCD(nil, nil, leftInt, rightInt)
	lcm := new(big.Int).Div(new(big.Int).Mul(leftInt, rightInt), gcd)
	return decimal.NewFromBigInt(lcm, -scale)
}

func scaledInt(value decimal.Decimal, scale int32) *big.Int {
	coefficient := new(big.Int).Set(value.Coefficient())
	multiplier := scale + value.Exponent()
	if multiplier > 0 {
		coefficient.Mul(coefficient, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(multiplier)), nil))
	}
	return coefficient.Abs(coefficient)
}

func inputForRole(input PreviewInput, role LegRole) PreviewLegInput {
	if role == LegRoleShort {
		return input.Short
	}
	return input.Long
}

func roundDown(value, increment decimal.Decimal) decimal.Decimal {
	if !value.IsPositive() || !increment.IsPositive() {
		return decimal.Zero
	}
	return value.Div(increment).Floor().Mul(increment)
}

func quantityConformsToIncrement(quantity, increment decimal.Decimal) bool {
	if !quantity.IsPositive() || !increment.IsPositive() {
		return false
	}
	return quantity.Equal(roundDown(quantity, increment))
}

func (result *PreviewResult) block(code ReasonCode, message string) {
	result.Blockers = append(result.Blockers, Finding{Code: code, Message: message})
}

func (result *PreviewResult) warn(code ReasonCode, message string) {
	result.Warnings = append(result.Warnings, Finding{Code: code, Message: message})
}
