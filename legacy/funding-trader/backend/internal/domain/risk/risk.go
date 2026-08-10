package risk

import (
	"time"

	"github.com/shopspring/decimal"
)

type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityWarning Severity = "warning"
)

type Code string

const (
	CodeStaleData         Code = "stale_data"
	CodeCapacityUsage     Code = "capacity_usage"
	CodeLowStability      Code = "low_stability"
	CodeRecentReversal    Code = "recent_reversal"
	CodeLiquidationBuffer Code = "liquidation_buffer"
)

const MaxDataAge = 3 * time.Second

var (
	CapacityUsageBlockerPercent = decimal.RequireFromString("80")
	StabilityWarningScore       = decimal.RequireFromString("80")
	LiquidationBlockerPercent   = decimal.RequireFromString("30")
	LiquidationWarningPercent   = decimal.RequireFromString("50")
)

type Finding struct {
	Code     Code
	Severity Severity
	Message  string
}

type Assessment struct {
	Findings []Finding
}

func (assessment Assessment) Blocked() bool {
	for _, finding := range assessment.Findings {
		if finding.Severity == SeverityBlocker {
			return true
		}
	}
	return false
}

type Snapshot struct {
	DataAge              time.Duration
	CapacityUsagePercent decimal.Decimal
	StabilityScore       decimal.Decimal
	RecentReversal       bool
	LiquidationBufferPct decimal.Decimal
}

func EvaluateSnapshot(snapshot Snapshot) Assessment {
	var findings []Finding
	if snapshot.DataAge > MaxDataAge {
		findings = append(findings, Finding{
			Code:     CodeStaleData,
			Severity: SeverityBlocker,
			Message:  "market data is older than the maximum allowed age",
		})
	}
	if snapshot.CapacityUsagePercent.GreaterThan(CapacityUsageBlockerPercent) {
		findings = append(findings, Finding{
			Code:     CodeCapacityUsage,
			Severity: SeverityBlocker,
			Message:  "requested notional uses more than the allowed share of executable capacity",
		})
	}
	if snapshot.StabilityScore.LessThan(StabilityWarningScore) {
		findings = append(findings, Finding{
			Code:     CodeLowStability,
			Severity: SeverityWarning,
			Message:  "funding spread stability is below the warning threshold",
		})
	}
	if snapshot.RecentReversal {
		findings = append(findings, Finding{
			Code:     CodeRecentReversal,
			Severity: SeverityWarning,
			Message:  "recent funding history reversed against the current route",
		})
	}
	if snapshot.LiquidationBufferPct.LessThan(LiquidationBlockerPercent) {
		findings = append(findings, Finding{
			Code:     CodeLiquidationBuffer,
			Severity: SeverityBlocker,
			Message:  "liquidation buffer is below the blocker threshold",
		})
	} else if snapshot.LiquidationBufferPct.LessThan(LiquidationWarningPercent) {
		findings = append(findings, Finding{
			Code:     CodeLiquidationBuffer,
			Severity: SeverityWarning,
			Message:  "liquidation buffer is below the warning threshold",
		})
	}
	return Assessment{Findings: findings}
}
