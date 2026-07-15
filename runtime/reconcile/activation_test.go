package reconcile

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestReportActivationVerdict(t *testing.T) {
	complete := Report{
		OpenOrdersCoverage: model.ReportCoverage{State: model.CoverageComplete},
		FillsCoverage:      model.ReportCoverage{State: model.CoverageNotRequested},
		PositionsCoverage:  model.ReportCoverage{State: model.CoverageNotRequested},
	}
	tests := []struct {
		name string
		rep  Report
		safe bool
	}{
		{name: "complete", rep: complete, safe: true},
		{
			name: "diagnostic warning remains safe",
			rep: func() Report {
				rep := complete
				rep.Partial = true
				rep.Warnings = []model.ReportWarning{{Code: "SNAPSHOT_PAGE_NOTE"}}
				return rep
			}(),
			safe: true,
		},
		{name: "fill partial", rep: Report{
			OpenOrdersCoverage: model.ReportCoverage{State: model.CoverageComplete},
			FillsCoverage:      model.ReportCoverage{State: model.CoveragePartial},
			PositionsCoverage:  model.ReportCoverage{State: model.CoverageNotRequested},
			FillsPartial:       true,
		}},
		{
			name: "fill limit warning is diagnostic",
			rep: func() Report {
				rep := complete
				rep.Warnings = []model.ReportWarning{{Code: "HISTORY_PAGE_NOTE"}}
				return rep
			}(),
			safe: true,
		},
		{
			name: "open orders unavailable typed",
			rep: Report{
				OpenOrdersCoverage: model.ReportCoverage{State: model.CoverageUnavailable},
				FillsCoverage:      model.ReportCoverage{State: model.CoverageNotRequested},
				PositionsCoverage:  model.ReportCoverage{State: model.CoverageNotRequested},
			},
		},
		{
			name: "blocking finding",
			rep: func() Report {
				rep := complete
				rep.Findings = []Finding{{Severity: FindingBlocking, Blocking: true}}
				return rep
			}(),
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
