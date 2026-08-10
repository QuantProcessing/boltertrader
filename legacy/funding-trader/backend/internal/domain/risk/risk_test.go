package risk

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestEvaluateSnapshotAppliesStableThresholds(t *testing.T) {
	assessment := EvaluateSnapshot(Snapshot{
		DataAge:              4 * time.Second,
		CapacityUsagePercent: decimal.RequireFromString("80.01"),
		StabilityScore:       decimal.RequireFromString("79.99"),
		RecentReversal:       true,
		LiquidationBufferPct: decimal.RequireFromString("29.99"),
	})

	assertHasFinding(t, assessment, SeverityBlocker, CodeStaleData)
	assertHasFinding(t, assessment, SeverityBlocker, CodeCapacityUsage)
	assertHasFinding(t, assessment, SeverityWarning, CodeLowStability)
	assertHasFinding(t, assessment, SeverityWarning, CodeRecentReversal)
	assertHasFinding(t, assessment, SeverityBlocker, CodeLiquidationBuffer)
	if !assessment.Blocked() {
		t.Fatal("assessment should be blocked")
	}
}

func TestEvaluateSnapshotWarnsBeforeLiquidationBufferBlocker(t *testing.T) {
	assessment := EvaluateSnapshot(Snapshot{
		DataAge:              3 * time.Second,
		CapacityUsagePercent: decimal.RequireFromString("80"),
		StabilityScore:       decimal.RequireFromString("80"),
		LiquidationBufferPct: decimal.RequireFromString("49.99"),
	})

	assertNoFinding(t, assessment, CodeStaleData)
	assertNoFinding(t, assessment, CodeCapacityUsage)
	assertNoFinding(t, assessment, CodeLowStability)
	assertHasFinding(t, assessment, SeverityWarning, CodeLiquidationBuffer)
	if assessment.Blocked() {
		t.Fatal("assessment should not be blocked")
	}
}

func assertHasFinding(t *testing.T, assessment Assessment, severity Severity, code Code) {
	t.Helper()
	for _, finding := range assessment.Findings {
		if finding.Code == code && finding.Severity == severity {
			return
		}
	}
	t.Fatalf("missing %s finding %s in %#v", severity, code, assessment.Findings)
}

func assertNoFinding(t *testing.T, assessment Assessment, code Code) {
	t.Helper()
	for _, finding := range assessment.Findings {
		if finding.Code == code {
			t.Fatalf("unexpected finding %s in %#v", code, assessment.Findings)
		}
	}
}
