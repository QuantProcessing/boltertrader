package alerts

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestEvaluateUsesSymmetricFundingThresholdsAndThreeSettlementChange(t *testing.T) {
	at := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		rate     string
		severity Severity
		rule     RuleType
	}{
		{name: "positive critical", rate: "0.00041", severity: Critical, rule: Above},
		{name: "negative warning", rate: "-0.00030", severity: Warning, rule: Below},
		{name: "positive watch", rate: "0.00015", severity: Watch, rule: Above},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			alert, ok := Evaluate(Input{
				Token: "BTC", ExchangeID: "binance", Contract: "BTC-USDT-PERP",
				CurrentRate: decimal.RequireFromString(test.rate),
				History: []decimal.Decimal{
					decimal.RequireFromString("0.00010"),
					decimal.RequireFromString("0.00011"),
					decimal.RequireFromString("0.00012"),
					decimal.RequireFromString("0.00013"),
				},
				NextFundingAt: at.Add(8 * time.Hour), ObservedAt: at,
			})
			if !ok {
				t.Fatal("expected alert")
			}
			if alert.Severity != test.severity || alert.Rule.Type != test.rule {
				t.Fatalf("alert=%+v", alert)
			}
			expectedChange := decimal.RequireFromString(test.rate).
				Sub(decimal.RequireFromString("0.00011")).
				Mul(decimal.NewFromInt(100))
			if !alert.ChangePct.Equal(expectedChange) {
				t.Fatalf("change=%s want=%s", alert.ChangePct, expectedChange)
			}
		})
	}
}

func TestEvaluateSuppressesRatesBelowWatchThreshold(t *testing.T) {
	if _, ok := Evaluate(Input{
		Token: "ETH", ExchangeID: "bybit", Contract: "ETH-USDT-PERP",
		CurrentRate: decimal.RequireFromString("0.000149"),
		ObservedAt:  time.Now().UTC(),
	}); ok {
		t.Fatal("unexpected alert below watch threshold")
	}
}
