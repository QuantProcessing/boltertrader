package calculation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

const (
	FormulaVersion = "funding-arb-v1"
	ModeTest       = "test"
	ModeLive       = "live"
	previewTTL     = 30 * time.Second
	maxDataAge     = 3 * time.Second
)

var (
	capacitySafetyFactor = decimal.RequireFromString("0.70")
	maxSlippageFraction  = decimal.RequireFromString("0.002")
	roundTripMultiplier  = decimal.NewFromInt(2)
	holdingDays          = decimal.NewFromInt(7)
	capacityBlockerPct   = decimal.NewFromInt(80)
	stabilityWarningPct  = decimal.NewFromInt(80)
)

type Code string

const (
	CodeAccountFence      Code = "account_fence"
	CodeCapacityUsage     Code = "capacity_usage"
	CodeIncompatibleLegs  Code = "incompatible_legs"
	CodeInvalidMarket     Code = "invalid_market"
	CodeLiveDisabled      Code = "live_disabled"
	CodeLowStability      Code = "low_stability"
	CodeMinTradeRules     Code = "min_trade_rules"
	CodeMissingFacts      Code = "missing_facts"
	CodeNonPositiveProfit Code = "non_positive_net_profit"
	CodeRecentReversal    Code = "recent_reversal"
	CodeScheduleMismatch  Code = "schedule_mismatch"
	CodeStaleData         Code = "stale_data"
	CodeTestNotionalLimit Code = "test_notional_limit"
	CodeUnverifiable      Code = "unverifiable_inputs"
	CodeRiskAuthority     Code = "risk_authority_unavailable"
)

type Finding struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// AccountFence is copied from the claimed Platform fact scope. A calculation
// with an incomplete fence fails closed before producing a submittable plan.
type AccountFence struct {
	AccountID         string `json:"accountId"`
	RuntimeGeneration int    `json:"runtimeGeneration"`
}

type FundingPoint struct {
	Rate        decimal.Decimal `json:"rate"`
	ObservedAt  time.Time       `json:"observedAt"`
	FundingTime time.Time       `json:"fundingTime"`
	NextFunding time.Time       `json:"nextFunding"`
}

type Quote struct {
	BidPrice decimal.Decimal `json:"bidPrice"`
	BidSize  decimal.Decimal `json:"bidSize"`
	AskPrice decimal.Decimal `json:"askPrice"`
	AskSize  decimal.Decimal `json:"askSize"`
}

type Level struct {
	Price    decimal.Decimal `json:"price"`
	Quantity decimal.Decimal `json:"quantity"`
}

type Depth struct {
	Bids []Level `json:"bids"`
	Asks []Level `json:"asks"`
}

// LegFact contains only normalized Platform facts. It deliberately has no
// client, credential, transport, or persistence dependency.
type LegFact struct {
	AccountFence      AccountFence    `json:"accountFence"`
	Venue             string          `json:"venue"`
	VenueEnvironment  string          `json:"venueEnvironment"`
	MarketProduct     string          `json:"marketProduct,omitempty"`
	InstrumentID      string          `json:"instrumentId"`
	Symbol            string          `json:"symbol"`
	Current           FundingPoint    `json:"current"`
	History           []FundingPoint  `json:"history"`
	MarketObservedAt  time.Time       `json:"marketObservedAt"`
	MarkPrice         decimal.Decimal `json:"markPrice"`
	BBO               Quote           `json:"bbo"`
	Depth             Depth           `json:"depth"`
	QuantityIncrement decimal.Decimal `json:"quantityIncrement"`
	MinQuantity       decimal.Decimal `json:"minQuantity"`
	MinNotional       decimal.Decimal `json:"minNotional"`
	TakerFeeRate      decimal.Decimal `json:"takerFeeRate"`
}

type Input struct {
	Mode                    string          `json:"mode"`
	ProductID               string          `json:"productId"`
	OpportunityID           string          `json:"opportunityId"`
	AuthorityIdentity       string          `json:"authorityIdentity,omitempty"`
	ObservedAt              time.Time       `json:"observedAt"`
	RequestedNotionalPerLeg decimal.Decimal `json:"requestedNotionalPerLeg"`
	Long                    LegFact         `json:"long"`
	Short                   LegFact         `json:"short"`
}

type PlannedLeg struct {
	AccountID         string          `json:"accountId"`
	RuntimeGeneration int             `json:"runtimeGeneration"`
	Venue             string          `json:"venue"`
	VenueEnvironment  string          `json:"venueEnvironment"`
	InstrumentID      string          `json:"instrumentId"`
	Symbol            string          `json:"symbol"`
	Role              string          `json:"role"`
	Side              string          `json:"side"`
	Quantity          decimal.Decimal `json:"quantity"`
	Notional          decimal.Decimal `json:"notional"`
	ReduceOnly        bool            `json:"reduceOnly"`
}

// Plan is the stable product DTO consumed by later Platform execution work.
// Decimal values are exact and NormalizedHash covers the source facts and plan.
type Plan struct {
	PreviewID        string          `json:"previewId"`
	OpportunityID    string          `json:"opportunityId"`
	ProductID        string          `json:"productId"`
	Mode             string          `json:"mode"`
	RouteID          string          `json:"routeId"`
	FormulaVersion   string          `json:"formulaVersion"`
	ObservedAt       time.Time       `json:"observedAt"`
	ExpiresAt        time.Time       `json:"expiresAt"`
	GrossAPR         decimal.Decimal `json:"grossApr"`
	NetAPR           decimal.Decimal `json:"netApr"`
	RoundTripFees    decimal.Decimal `json:"roundTripFees"`
	Slippage         decimal.Decimal `json:"slippage"`
	CapacityNotional decimal.Decimal `json:"capacityNotional"`
	Stability        decimal.Decimal `json:"stability"`
	NextFunding      time.Time       `json:"nextFunding"`
	Legs             []PlannedLeg    `json:"legs"`
	Blockers         []Finding       `json:"blockers"`
	Warnings         []Finding       `json:"warnings"`
	CanSubmit        bool            `json:"canSubmit"`
	NormalizedHash   string          `json:"normalizedHash"`
}

func Compute(input Input) (Plan, error) {
	return ComputeWithAdmission(input, fundingpolicy.DefaultAdmission())
}

func ComputeWithAdmission(input Input, admission fundingpolicy.Admission) (Plan, error) {
	if input.ObservedAt.IsZero() {
		return Plan{}, fmt.Errorf("observed at is required")
	}
	plan := Plan{
		OpportunityID: input.OpportunityID, ProductID: input.ProductID, Mode: input.Mode,
		RouteID: routeID(input.Long, input.Short), FormulaVersion: FormulaVersion,
		ObservedAt: input.ObservedAt.UTC(), ExpiresAt: input.ObservedAt.UTC().Add(previewTTL),
		NextFunding: input.Long.Current.NextFunding.UTC(), Stability: stabilityScore(input),
		Blockers: []Finding{}, Warnings: []Finding{},
	}

	interval, intervalOK := verifiedFundingInterval(input.Long, input.Short)
	if intervalOK {
		spread := input.Short.Current.Rate.Sub(input.Long.Current.Rate)
		plan.GrossAPR = spread.Mul(periodsPerYear(interval))
	}
	plan.RoundTripFees = input.RequestedNotionalPerLeg.
		Mul(input.Long.TakerFeeRate.Add(input.Short.TakerFeeRate)).
		Mul(roundTripMultiplier)
	plan.Slippage = slippageToBuy(input.RequestedNotionalPerLeg, input.Long.MarkPrice, input.Long.BBO.AskPrice).
		Add(slippageToSell(input.RequestedNotionalPerLeg, input.Short.MarkPrice, input.Short.BBO.BidPrice)).
		Mul(roundTripMultiplier)
	plan.CapacityNotional = decimal.Min(
		capacityFromLevels(input.Long.Depth.Asks, input.Long.MarkPrice, true),
		capacityFromLevels(input.Short.Depth.Bids, input.Short.MarkPrice, false),
	).Mul(capacitySafetyFactor)
	if input.RequestedNotionalPerLeg.IsPositive() {
		annualizedCosts := plan.RoundTripFees.Add(plan.Slippage).
			Div(input.RequestedNotionalPerLeg).
			Mul(decimal.NewFromInt(365)).
			Div(holdingDays)
		plan.NetAPR = plan.GrossAPR.Sub(annualizedCosts)
	}
	quantity := commonOpenQuantity(input)
	plan.Legs = []PlannedLeg{
		plannedLeg(input.Long, "long", "buy", quantity),
		plannedLeg(input.Short, "short", "sell", quantity),
	}
	validate(&plan, input, intervalOK, admission)
	plan.CanSubmit = len(plan.Blockers) == 0

	hashBody, err := json.Marshal(struct {
		Input Input `json:"input"`
		Plan  Plan  `json:"plan"`
	}{Input: input, Plan: plan})
	if err != nil {
		return Plan{}, fmt.Errorf("normalize plan: %w", err)
	}
	sum := sha256.Sum256(hashBody)
	plan.NormalizedHash = hex.EncodeToString(sum[:])
	plan.PreviewID = "funding_preview_" + plan.NormalizedHash[:16]
	return plan, nil
}

func validate(plan *Plan, input Input, intervalOK bool, admission fundingpolicy.Admission) {
	if !admission.Allows(input.Mode, input.RequestedNotionalPerLeg) {
		if input.Mode == ModeLive && !admission.LiveEnabled() {
			plan.block(CodeLiveDisabled, "Funding Live mode is disabled by operator policy")
		} else {
			plan.block(CodeTestNotionalLimit, "Funding notional exceeds the configured bound for this mode")
		}
	}
	if !validFence(input.Long.AccountFence) || !validFence(input.Short.AccountFence) {
		plan.block(CodeAccountFence, "both legs require claimed account runtime fences")
	}
	if !validIdentity(input.Long) || !validIdentity(input.Short) {
		plan.block(CodeMissingFacts, "both account-scoped leg identities are required")
	}
	if input.Long.AccountFence.AccountID == input.Short.AccountFence.AccountID || input.Long.Venue == input.Short.Venue {
		plan.block(CodeIncompatibleLegs, "legs must use distinct accounts and venues")
	}
	if stale(input.ObservedAt, input.Long.Current.ObservedAt) || stale(input.ObservedAt, input.Short.Current.ObservedAt) ||
		stale(input.ObservedAt, input.Long.MarketObservedAt) || stale(input.ObservedAt, input.Short.MarketObservedAt) {
		plan.block(CodeStaleData, "funding and market facts must be no more than 3 seconds old")
	}
	if !validQuote(input.Long.BBO) || !validQuote(input.Short.BBO) {
		plan.block(CodeInvalidMarket, "BBO must be positive and non-crossed")
	}
	if !intervalOK {
		plan.block(CodeScheduleMismatch, "leg funding schedules and verified intervals must match")
	}
	if !plan.CapacityNotional.IsPositive() {
		plan.block(CodeUnverifiable, "executable capacity is required")
	} else if input.RequestedNotionalPerLeg.Div(plan.CapacityNotional).Mul(decimal.NewFromInt(100)).GreaterThan(capacityBlockerPct) {
		plan.block(CodeCapacityUsage, "requested notional uses more than 80% of executable capacity")
	}
	if admission.RiskCheckEnabled() {
		plan.block(CodeRiskAuthority, "Funding risk check is enabled but Gateway V1 provides no standardized liquidation authority")
	}
	if !plan.NetAPR.IsPositive() {
		plan.block(CodeNonPositiveProfit, "net annualized return must be positive")
	}
	for index, leg := range plan.Legs {
		fact := input.Long
		if index == 1 {
			fact = input.Short
		}
		if !leg.Quantity.IsPositive() || !leg.Quantity.Equal(roundDown(leg.Quantity, fact.QuantityIncrement)) ||
			leg.Quantity.LessThan(fact.MinQuantity) || leg.Notional.LessThan(fact.MinNotional) {
			plan.block(CodeMinTradeRules, "common rounded quantity cannot satisfy instrument rules")
			break
		}
	}
	if plan.Stability.LessThan(stabilityWarningPct) {
		plan.warn(CodeLowStability, "funding spread stability is below 80%")
	}
	if recentReversal(input) {
		plan.warn(CodeRecentReversal, "recent funding history reversed against the current route")
	}
}

func validFence(fence AccountFence) bool {
	return strings.TrimSpace(fence.AccountID) != "" && fence.RuntimeGeneration > 0
}

func validIdentity(leg LegFact) bool {
	return validFence(leg.AccountFence) && leg.Venue != "" && leg.VenueEnvironment != "" && leg.InstrumentID != ""
}

func stale(now, observed time.Time) bool {
	return observed.IsZero() || observed.After(now) || now.Sub(observed) > maxDataAge
}

func validQuote(quote Quote) bool {
	return quote.BidPrice.IsPositive() && quote.BidSize.IsPositive() && quote.AskPrice.IsPositive() &&
		quote.AskSize.IsPositive() && quote.BidPrice.LessThanOrEqual(quote.AskPrice)
}

func verifiedFundingInterval(long, short LegFact) (time.Duration, bool) {
	if long.Current.NextFunding.IsZero() || !long.Current.NextFunding.Equal(short.Current.NextFunding) {
		return 0, false
	}
	longInterval, longOK := legFundingInterval(long)
	shortInterval, shortOK := legFundingInterval(short)
	return longInterval, longOK && shortOK && longInterval > 0 && longInterval == shortInterval
}

func legFundingInterval(leg LegFact) (time.Duration, bool) {
	if !leg.Current.FundingTime.IsZero() && leg.Current.NextFunding.After(leg.Current.FundingTime) {
		return leg.Current.NextFunding.Sub(leg.Current.FundingTime).Round(time.Second), true
	}
	var latest, previous time.Time
	for _, point := range leg.History {
		at := point.FundingTime.UTC()
		if at.IsZero() || !at.Before(leg.Current.NextFunding) {
			continue
		}
		if latest.IsZero() || at.After(latest) {
			previous, latest = latest, at
		} else if previous.IsZero() || at.After(previous) {
			previous = at
		}
	}
	if latest.IsZero() || previous.IsZero() {
		return 0, false
	}
	nextInterval := leg.Current.NextFunding.Sub(latest).Round(time.Second)
	return nextInterval, nextInterval > 0 && nextInterval == latest.Sub(previous).Round(time.Second)
}

func periodsPerYear(interval time.Duration) decimal.Decimal {
	return decimal.NewFromInt(365 * 24 * 60 * 60).Div(decimal.NewFromInt(int64(interval.Seconds())))
}

func capacityFromLevels(levels []Level, mark decimal.Decimal, buy bool) decimal.Decimal {
	if !mark.IsPositive() {
		return decimal.Zero
	}
	upper := mark.Mul(decimal.NewFromInt(1).Add(maxSlippageFraction))
	lower := mark.Mul(decimal.NewFromInt(1).Sub(maxSlippageFraction))
	capacity := decimal.Zero
	for _, level := range levels {
		if !level.Price.IsPositive() || !level.Quantity.IsPositive() || (buy && level.Price.GreaterThan(upper)) || (!buy && level.Price.LessThan(lower)) {
			continue
		}
		capacity = capacity.Add(level.Price.Mul(level.Quantity))
	}
	return capacity
}

func slippageToBuy(notional, mark, ask decimal.Decimal) decimal.Decimal {
	if !notional.IsPositive() || !mark.IsPositive() || !ask.GreaterThan(mark) {
		return decimal.Zero
	}
	return notional.Mul(ask.Sub(mark)).Div(mark)
}

func slippageToSell(notional, mark, bid decimal.Decimal) decimal.Decimal {
	if !notional.IsPositive() || !mark.IsPositive() || !bid.IsPositive() || !bid.LessThan(mark) {
		return decimal.Zero
	}
	return notional.Mul(mark.Sub(bid)).Div(mark)
}

func commonOpenQuantity(input Input) decimal.Decimal {
	if !input.Long.MarkPrice.IsPositive() || !input.Short.MarkPrice.IsPositive() {
		return decimal.Zero
	}
	increment := commonIncrement(input.Long.QuantityIncrement, input.Short.QuantityIncrement)
	if !increment.IsPositive() {
		return decimal.Zero
	}
	maximum := decimal.Min(input.RequestedNotionalPerLeg.Div(input.Long.MarkPrice), input.RequestedNotionalPerLeg.Div(input.Short.MarkPrice))
	return roundDown(maximum, increment)
}

func commonIncrement(left, right decimal.Decimal) decimal.Decimal {
	if !left.IsPositive() || !right.IsPositive() {
		return decimal.Zero
	}
	scale := -left.Exponent()
	if -right.Exponent() > scale {
		scale = -right.Exponent()
	}
	leftInt, rightInt := scaledInt(left, scale), scaledInt(right, scale)
	gcd := new(big.Int).GCD(nil, nil, leftInt, rightInt)
	lcm := new(big.Int).Div(new(big.Int).Mul(leftInt, rightInt), gcd)
	return decimal.NewFromBigInt(lcm, -scale)
}

func scaledInt(value decimal.Decimal, scale int32) *big.Int {
	coefficient := new(big.Int).Set(value.Coefficient())
	if multiplier := scale + value.Exponent(); multiplier > 0 {
		coefficient.Mul(coefficient, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(multiplier)), nil))
	}
	return coefficient.Abs(coefficient)
}

func roundDown(value, increment decimal.Decimal) decimal.Decimal {
	if !value.IsPositive() || !increment.IsPositive() {
		return decimal.Zero
	}
	return value.Div(increment).Floor().Mul(increment)
}

func plannedLeg(fact LegFact, role, side string, quantity decimal.Decimal) PlannedLeg {
	return PlannedLeg{
		AccountID: fact.AccountFence.AccountID, RuntimeGeneration: fact.AccountFence.RuntimeGeneration,
		Venue: fact.Venue, VenueEnvironment: fact.VenueEnvironment, InstrumentID: fact.InstrumentID, Symbol: fact.Symbol,
		Role: role, Side: side, Quantity: quantity, Notional: quantity.Mul(fact.MarkPrice), ReduceOnly: false,
	}
}

func stabilityScore(input Input) decimal.Decimal {
	pairs := len(input.Long.History)
	if len(input.Short.History) < pairs {
		pairs = len(input.Short.History)
	}
	spread := input.Short.Current.Rate.Sub(input.Long.Current.Rate)
	if pairs == 0 || spread.IsZero() {
		return decimal.NewFromInt(100)
	}
	stable := 0
	for index := 0; index < pairs; index++ {
		value := input.Short.History[index].Rate.Sub(input.Long.History[index].Rate)
		if value.IsZero() || (value.IsPositive() && spread.IsPositive()) || (value.IsNegative() && spread.IsNegative()) {
			stable++
		}
	}
	return decimal.NewFromInt(int64(stable)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(pairs)))
}

func recentReversal(input Input) bool {
	pairs := len(input.Long.History)
	if len(input.Short.History) < pairs {
		pairs = len(input.Short.History)
	}
	return pairs > 0 && input.Short.History[pairs-1].Rate.Sub(input.Long.History[pairs-1].Rate).IsNegative()
}

func routeID(long, short LegFact) string {
	return long.AccountFence.AccountID + ":" + long.Venue + ":" + long.InstrumentID + ":" + long.VenueEnvironment + "|long->" +
		short.AccountFence.AccountID + ":" + short.Venue + ":" + short.InstrumentID + ":" + short.VenueEnvironment + "|short"
}

func (plan *Plan) block(code Code, message string) {
	plan.Blockers = append(plan.Blockers, Finding{Code: code, Message: message})
}

func (plan *Plan) warn(code Code, message string) {
	plan.Warnings = append(plan.Warnings, Finding{Code: code, Message: message})
}
