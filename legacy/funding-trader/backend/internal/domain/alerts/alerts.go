package alerts

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Watch    Severity = "watch"
)

type RuleType string

const (
	Above RuleType = "above"
	Below RuleType = "below"
)

var (
	criticalThreshold = decimal.RequireFromString("0.00040")
	warningThreshold  = decimal.RequireFromString("0.00025")
	watchThreshold    = decimal.RequireFromString("0.00015")
	percentMultiplier = decimal.NewFromInt(100)
)

type Input struct {
	Token                string
	ExchangeID           string
	Contract             string
	CurrentRate          decimal.Decimal
	History              []decimal.Decimal
	NextFundingAt        time.Time
	ObservedAt           time.Time
	RelatedOpportunityID string
}

type Rule struct {
	Type         RuleType
	ThresholdPct decimal.Decimal
	Label        string
}

type Alert struct {
	Token                string
	ExchangeID           string
	Contract             string
	CurrentRatePct       decimal.Decimal
	Rule                 Rule
	ChangePct            decimal.Decimal
	Severity             Severity
	NextFundingAt        time.Time
	RelatedOpportunityID string
	ObservedAt           time.Time
}

func Evaluate(input Input) (Alert, bool) {
	absolute := input.CurrentRate.Abs()
	var severity Severity
	var threshold decimal.Decimal
	switch {
	case absolute.GreaterThanOrEqual(criticalThreshold):
		severity, threshold = Critical, criticalThreshold
	case absolute.GreaterThanOrEqual(warningThreshold):
		severity, threshold = Warning, warningThreshold
	case absolute.GreaterThanOrEqual(watchThreshold):
		severity, threshold = Watch, watchThreshold
	default:
		return Alert{}, false
	}
	ruleType := Above
	operator := ">"
	if input.CurrentRate.IsNegative() {
		ruleType = Below
		operator = "<"
		threshold = threshold.Neg()
	}
	change := decimal.Zero
	if len(input.History) >= 3 {
		change = input.CurrentRate.Sub(input.History[len(input.History)-3])
	}
	thresholdPct := threshold.Mul(percentMultiplier)
	return Alert{
		Token: input.Token, ExchangeID: input.ExchangeID, Contract: input.Contract,
		CurrentRatePct: input.CurrentRate.Mul(percentMultiplier),
		Rule: Rule{
			Type: ruleType, ThresholdPct: thresholdPct,
			Label: fmt.Sprintf("%s %s%%", operator, thresholdPct.StringFixed(4)),
		},
		ChangePct: change.Mul(percentMultiplier), Severity: severity,
		NextFundingAt:        input.NextFundingAt.UTC(),
		RelatedOpportunityID: input.RelatedOpportunityID,
		ObservedAt:           input.ObservedAt.UTC(),
	}, true
}
