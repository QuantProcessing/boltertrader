package reconcile

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestReportActivationVerdict(t *testing.T) {
	tests := []struct {
		name string
		rep  Report
		safe bool
	}{
		{name: "complete", rep: Report{}, safe: true},
		{
			name: "open-orders-only partial remains safe",
			rep:  Report{Partial: true, Warnings: []model.ReportWarning{{Code: "OPEN_ORDERS_ONLY"}}},
			safe: true,
		},
		{name: "fill partial", rep: Report{FillsPartial: true}},
		{
			name: "fill limit warning",
			rep:  Report{Warnings: []model.ReportWarning{{Code: "FILL_REPORTS_LIMIT_REACHED"}}},
		},
		{
			name: "open orders unavailable",
			rep:  Report{Warnings: []model.ReportWarning{{Code: "OPEN_ORDERS_UNAVAILABLE"}}},
		},
		{
			name: "blocking finding",
			rep:  Report{Findings: []Finding{{Severity: FindingBlocking, Blocking: true}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict := tt.rep.ActivationVerdict()
			if verdict.Safe != tt.safe {
				t.Fatalf("verdict=%+v, want safe=%v", verdict, tt.safe)
			}
			if !tt.safe && verdict.Reason == "" {
				t.Fatal("unsafe verdict missing reason")
			}
		})
	}
}
