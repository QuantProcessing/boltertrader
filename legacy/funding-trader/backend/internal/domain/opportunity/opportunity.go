package opportunity

import (
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/risk"
	"github.com/shopspring/decimal"
)

const FormulaVersion = "funding-arb-v1"

var (
	capacitySafetyFactor = decimal.RequireFromString("0.70")
	maxSlippageFraction  = decimal.RequireFromString("0.002")
	roundTripMultiplier  = decimal.NewFromInt(2)
	holdingDays          = decimal.NewFromInt(7)
)

type Code string

const (
	CodeIncompatibleLegs    Code = "incompatible_legs"
	CodeMissingLegData      Code = "missing_leg_data"
	CodeStaleData           Code = "stale_data"
	CodeFutureData          Code = "future_data"
	CodeInvalidMarket       Code = "invalid_market"
	CodeNonPositiveCapacity Code = "nonpositive_capacity"
	CodeScheduleMismatch    Code = "schedule_mismatch"
	CodeUnverifiableInputs  Code = "unverifiable_inputs"
	CodeLowStability        Code = "low_stability"
	CodeRecentReversal      Code = "recent_reversal"
	CodeLiquidationBuffer   Code = "liquidation_buffer"
	CodeCapacityUsage       Code = "capacity_usage"
)

type Finding struct {
	Code    Code
	Message string
}

type FundingPoint struct {
	Rate        decimal.Decimal
	ObservedAt  time.Time
	FundingTime time.Time
	NextFunding time.Time
}

type Quote struct {
	BidPrice decimal.Decimal
	BidSize  decimal.Decimal
	AskPrice decimal.Decimal
	AskSize  decimal.Decimal
}

type Level struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

type Depth struct {
	Bids []Level
	Asks []Level
}

type Leg struct {
	AccountID        string
	InstrumentID     string
	Venue            string
	VenueEnvironment string
	Symbol           string
	Current          FundingPoint
	History          []FundingPoint
	MarketObservedAt time.Time
	MarkPrice        decimal.Decimal
	BBO              Quote
	Depth            Depth
}

type Fees struct {
	LongTakerRate  decimal.Decimal
	ShortTakerRate decimal.Decimal
}

type Input struct {
	ObservedAt            time.Time
	Notional              decimal.Decimal
	Long                  Leg
	Short                 Leg
	Fees                  Fees
	LiquidationBufferPct  decimal.Decimal
	RequireHistoryPairing bool
}

type CalculationInputs struct {
	LongRate             decimal.Decimal
	ShortRate            decimal.Decimal
	SpreadPerPeriod      decimal.Decimal
	PeriodsPerYear       decimal.Decimal
	RequestedNotional    decimal.Decimal
	CapacityUsagePercent decimal.Decimal
	LongTakerFeeRate     decimal.Decimal
	ShortTakerFeeRate    decimal.Decimal
	LiquidationBufferPct decimal.Decimal
	LongHistoryRates     []decimal.Decimal
	ShortHistoryRates    []decimal.Decimal
}

type Snapshot struct {
	RouteID          string
	Long             Leg
	Short            Leg
	GrossAPR         decimal.Decimal
	NetAPR           decimal.Decimal
	Stability        decimal.Decimal
	CapacityNotional decimal.Decimal
	RoundTripFees    decimal.Decimal
	Slippage         decimal.Decimal
	Basis            decimal.Decimal
	DataAge          time.Duration
	NextFunding      time.Time
	FormulaVersion   string
	Blockers         []Finding
	Warnings         []Finding
	ObservedAt       time.Time
	Calculation      CalculationInputs
}

func (snapshot Snapshot) Blocked() bool {
	return len(snapshot.Blockers) > 0
}

func Evaluate(input Input) Snapshot {
	snapshot := Snapshot{
		RouteID:        routeID(input.Long, input.Short),
		Long:           input.Long,
		Short:          input.Short,
		ObservedAt:     input.ObservedAt,
		NextFunding:    input.Long.Current.NextFunding,
		FormulaVersion: FormulaVersion,
		Stability:      decimal.NewFromInt(100),
		Basis:          basisRatio(input.Long.MarkPrice, input.Short.MarkPrice),
		RoundTripFees: input.Notional.
			Mul(input.Fees.LongTakerRate.Add(input.Fees.ShortTakerRate)).
			Mul(roundTripMultiplier),
		CapacityNotional: minDecimal(
			capacityFromLevels(input.Long.Depth.Asks, input.Long.MarkPrice, true),
			capacityFromLevels(input.Short.Depth.Bids, input.Short.MarkPrice, false),
		).Mul(capacitySafetyFactor),
	}
	snapshot.DataAge = maxDuration(
		maxDuration(
			age(input.ObservedAt, input.Long.Current.ObservedAt),
			age(input.ObservedAt, input.Short.Current.ObservedAt),
		),
		maxDuration(
			age(input.ObservedAt, input.Long.MarketObservedAt),
			age(input.ObservedAt, input.Short.MarketObservedAt),
		),
	)
	snapshot.Slippage = slippageToBuy(input.Notional, input.Long.MarkPrice, input.Long.BBO.AskPrice).
		Add(slippageToSell(input.Notional, input.Short.MarkPrice, input.Short.BBO.BidPrice)).
		Mul(roundTripMultiplier)

	spread := input.Short.Current.Rate.Sub(input.Long.Current.Rate)
	periods := decimal.Zero
	if interval, ok := verifiedFundingInterval(input); ok {
		periods = periodsPerYear(interval)
	}
	snapshot.GrossAPR = spread.Mul(periods)
	if input.Notional.IsPositive() {
		annualizedCosts := snapshot.RoundTripFees.Add(snapshot.Slippage).
			Div(input.Notional).
			Mul(decimal.NewFromInt(365)).
			Div(holdingDays)
		snapshot.NetAPR = snapshot.GrossAPR.Sub(annualizedCosts)
	}
	snapshot.Calculation = CalculationInputs{
		LongRate:             input.Long.Current.Rate,
		ShortRate:            input.Short.Current.Rate,
		SpreadPerPeriod:      spread,
		PeriodsPerYear:       periods,
		RequestedNotional:    input.Notional,
		LongTakerFeeRate:     input.Fees.LongTakerRate,
		ShortTakerFeeRate:    input.Fees.ShortTakerRate,
		LiquidationBufferPct: input.LiquidationBufferPct,
		LongHistoryRates:     historyRates(input.Long.History),
		ShortHistoryRates:    historyRates(input.Short.History),
	}
	if snapshot.CapacityNotional.IsPositive() && input.Notional.IsPositive() {
		snapshot.Calculation.CapacityUsagePercent = input.Notional.Div(snapshot.CapacityNotional).Mul(decimal.NewFromInt(100))
	}
	snapshot.Stability = stabilityScore(spread, input.Long.History, input.Short.History)

	snapshot.addValidationBlockers(input)
	snapshot.addRiskFindings(input)
	return snapshot
}

func (snapshot *Snapshot) addValidationBlockers(input Input) {
	if !hasLegIdentity(input.Long) || !hasLegIdentity(input.Short) ||
		input.Long.MarketObservedAt.IsZero() || input.Short.MarketObservedAt.IsZero() ||
		!input.Notional.IsPositive() {
		snapshot.block(CodeMissingLegData, "both legs and a positive notional are required")
	}
	if input.Long.Venue == input.Short.Venue || input.Long.AccountID == "" || input.Short.AccountID == "" {
		snapshot.block(CodeIncompatibleLegs, "legs must be explicit account-scoped instruments on distinct venues")
	}
	if isFuture(input.ObservedAt, input.Long.Current.ObservedAt) ||
		isFuture(input.ObservedAt, input.Short.Current.ObservedAt) ||
		isFuture(input.ObservedAt, input.Long.MarketObservedAt) ||
		isFuture(input.ObservedAt, input.Short.MarketObservedAt) {
		snapshot.block(CodeFutureData, "funding and market observations cannot be in the future")
	}
	if snapshot.DataAge > risk.MaxDataAge {
		snapshot.block(CodeStaleData, "funding or market data is stale")
	}
	if !validQuote(input.Long.BBO) || !validQuote(input.Short.BBO) {
		snapshot.block(CodeInvalidMarket, "BBO must be positive and non-crossed")
	}
	if !snapshot.CapacityNotional.IsPositive() {
		snapshot.block(CodeNonPositiveCapacity, "executable book capacity must be positive")
	}
	if input.Long.Current.NextFunding.IsZero() || input.Short.Current.NextFunding.IsZero() ||
		!input.Long.Current.NextFunding.Equal(input.Short.Current.NextFunding) {
		snapshot.block(CodeScheduleMismatch, "leg funding schedules do not match")
	}
	if _, ok := verifiedFundingInterval(input); !ok {
		snapshot.block(CodeUnverifiableInputs, "funding interval cannot be verified")
	}
	if input.RequireHistoryPairing &&
		(len(input.Long.History) == 0 || len(input.Long.History) != len(input.Short.History)) {
		snapshot.block(CodeUnverifiableInputs, "funding history pairs cannot be verified")
	}
}

func (snapshot *Snapshot) addRiskFindings(input Input) {
	assessment := risk.EvaluateSnapshot(risk.Snapshot{
		DataAge:              snapshot.DataAge,
		CapacityUsagePercent: snapshot.Calculation.CapacityUsagePercent,
		StabilityScore:       snapshot.Stability,
		RecentReversal:       recentReversal(input.Long.History, input.Short.History),
		LiquidationBufferPct: input.LiquidationBufferPct,
	})
	for _, finding := range assessment.Findings {
		code := Code(finding.Code)
		if finding.Severity == risk.SeverityBlocker {
			snapshot.block(code, finding.Message)
		} else {
			snapshot.warn(code, finding.Message)
		}
	}
}

func (snapshot *Snapshot) block(code Code, message string) {
	snapshot.Blockers = append(snapshot.Blockers, Finding{Code: code, Message: message})
}

func (snapshot *Snapshot) warn(code Code, message string) {
	snapshot.Warnings = append(snapshot.Warnings, Finding{Code: code, Message: message})
}

func routeID(long Leg, short Leg) string {
	return strings.Join([]string{
		long.AccountID + ":" + long.Venue + ":" + long.InstrumentID + ":" + long.VenueEnvironment + "|long",
		short.AccountID + ":" + short.Venue + ":" + short.InstrumentID + ":" + short.VenueEnvironment + "|short",
	}, "->")
}

func hasLegIdentity(leg Leg) bool {
	return leg.AccountID != "" &&
		leg.InstrumentID != "" &&
		leg.Venue != "" &&
		leg.VenueEnvironment != ""
}

func validQuote(quote Quote) bool {
	return quote.BidPrice.IsPositive() && quote.AskPrice.IsPositive() &&
		quote.BidSize.IsPositive() && quote.AskSize.IsPositive() &&
		quote.BidPrice.LessThanOrEqual(quote.AskPrice)
}

func isFuture(observedAt time.Time, value time.Time) bool {
	return !value.IsZero() && value.After(observedAt)
}

func age(observedAt time.Time, value time.Time) time.Duration {
	if observedAt.IsZero() || value.IsZero() || value.After(observedAt) {
		return 0
	}
	return observedAt.Sub(value)
}

func maxDuration(first time.Duration, second time.Duration) time.Duration {
	if first > second {
		return first
	}
	return second
}

func verifiedFundingInterval(input Input) (time.Duration, bool) {
	if input.Long.Current.NextFunding.IsZero() ||
		input.Short.Current.NextFunding.IsZero() ||
		!input.Long.Current.NextFunding.Equal(input.Short.Current.NextFunding) {
		return 0, false
	}
	longInterval, longOK := legFundingInterval(input.Long)
	shortInterval, shortOK := legFundingInterval(input.Short)
	if !longOK || !shortOK || longInterval <= 0 || longInterval != shortInterval {
		return 0, false
	}
	return longInterval, true
}

func legFundingInterval(leg Leg) (time.Duration, bool) {
	next := leg.Current.NextFunding
	if next.IsZero() {
		return 0, false
	}
	current := leg.Current.FundingTime
	if !current.IsZero() {
		if !next.After(current) {
			return 0, false
		}
		return next.Sub(current).Round(time.Second), true
	}
	latest, previous, ok := latestTwoHistoricalFundingTimes(leg.History, next)
	if !ok {
		return 0, false
	}
	nextInterval := next.Sub(latest).Round(time.Second)
	historyInterval := latest.Sub(previous).Round(time.Second)
	if nextInterval <= 0 || nextInterval != historyInterval {
		return 0, false
	}
	return nextInterval, true
}

func latestTwoHistoricalFundingTimes(history []FundingPoint, next time.Time) (time.Time, time.Time, bool) {
	var latest, previous time.Time
	for _, point := range history {
		fundingTime := point.FundingTime.UTC()
		if fundingTime.IsZero() || !fundingTime.Before(next) {
			continue
		}
		if latest.IsZero() || fundingTime.After(latest) {
			previous = latest
			latest = fundingTime
			continue
		}
		if previous.IsZero() || fundingTime.After(previous) {
			previous = fundingTime
		}
	}
	return latest, previous, !latest.IsZero() && !previous.IsZero()
}

func periodsPerYear(interval time.Duration) decimal.Decimal {
	if interval <= 0 {
		return decimal.Zero
	}
	seconds := decimal.NewFromInt(int64(interval.Seconds()))
	return decimal.NewFromInt(365 * 24 * 60 * 60).Div(seconds)
}

func capacityFromLevels(levels []Level, mark decimal.Decimal, buy bool) decimal.Decimal {
	if !mark.IsPositive() {
		return decimal.Zero
	}
	upper := mark.Mul(decimal.NewFromInt(1).Add(maxSlippageFraction))
	lower := mark.Mul(decimal.NewFromInt(1).Sub(maxSlippageFraction))
	capacity := decimal.Zero
	for _, level := range levels {
		if !level.Price.IsPositive() || !level.Quantity.IsPositive() {
			continue
		}
		if buy && level.Price.GreaterThan(upper) {
			continue
		}
		if !buy && level.Price.LessThan(lower) {
			continue
		}
		capacity = capacity.Add(level.Price.Mul(level.Quantity))
	}
	return capacity
}

func basisRatio(longMark, shortMark decimal.Decimal) decimal.Decimal {
	if !longMark.IsPositive() || !shortMark.IsPositive() {
		return decimal.Zero
	}
	midpoint := longMark.Add(shortMark).Div(decimal.NewFromInt(2))
	if !midpoint.IsPositive() {
		return decimal.Zero
	}
	return shortMark.Sub(longMark).Div(midpoint)
}

func slippageToBuy(notional decimal.Decimal, mark decimal.Decimal, ask decimal.Decimal) decimal.Decimal {
	if !notional.IsPositive() || !mark.IsPositive() || !ask.IsPositive() || ask.LessThanOrEqual(mark) {
		return decimal.Zero
	}
	return notional.Mul(ask.Sub(mark)).Div(mark)
}

func slippageToSell(notional decimal.Decimal, mark decimal.Decimal, bid decimal.Decimal) decimal.Decimal {
	if !notional.IsPositive() || !mark.IsPositive() || !bid.IsPositive() || bid.GreaterThanOrEqual(mark) {
		return decimal.Zero
	}
	return notional.Mul(mark.Sub(bid)).Div(mark)
}

func minDecimal(first decimal.Decimal, second decimal.Decimal) decimal.Decimal {
	if first.IsZero() || first.LessThan(second) {
		return first
	}
	return second
}

func historyRates(history []FundingPoint) []decimal.Decimal {
	rates := make([]decimal.Decimal, 0, len(history))
	for _, point := range history {
		rates = append(rates, point.Rate)
	}
	return rates
}

func stabilityScore(currentSpread decimal.Decimal, longHistory []FundingPoint, shortHistory []FundingPoint) decimal.Decimal {
	pairs := minInt(len(longHistory), len(shortHistory))
	if pairs == 0 || currentSpread.IsZero() {
		return decimal.NewFromInt(100)
	}
	stable := 0
	for i := 0; i < pairs; i++ {
		if sameSign(shortHistory[i].Rate.Sub(longHistory[i].Rate), currentSpread) {
			stable++
		}
	}
	return decimal.NewFromInt(int64(stable)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(pairs)))
}

func recentReversal(longHistory []FundingPoint, shortHistory []FundingPoint) bool {
	pairs := minInt(len(longHistory), len(shortHistory))
	if pairs == 0 {
		return false
	}
	return shortHistory[pairs-1].Rate.Sub(longHistory[pairs-1].Rate).IsNegative()
}

func sameSign(first decimal.Decimal, second decimal.Decimal) bool {
	return first.IsZero() || second.IsZero() || (first.IsPositive() && second.IsPositive()) || (first.IsNegative() && second.IsNegative())
}

func minInt(first int, second int) int {
	if first < second {
		return first
	}
	return second
}
