package accepttest

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestRoundDownOrderPriceFollowsHyperliquidPrecisionEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		price      string
		spot       bool
		szDecimals int
		want       string
	}{
		{
			name:       "spot keeps five significant figures",
			price:      "0.0713905",
			spot:       true,
			szDecimals: 0,
			want:       "0.07139",
		},
		{
			name:       "perp limits decimals by szDecimals",
			price:      "2587.35",
			spot:       false,
			szDecimals: 4,
			want:       "2587.3",
		},
		{
			name:       "perp decimal cap can be stricter than significant figures",
			price:      "0.012345",
			spot:       false,
			szDecimals: 1,
			want:       "0.01234",
		},
		{
			name:       "large integer price remains valid",
			price:      "123456.78",
			spot:       false,
			szDecimals: 5,
			want:       "123456",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RoundDownOrderPrice(decimal.RequireFromString(tt.price), tt.spot, tt.szDecimals)
			if !got.Equal(decimal.RequireFromString(tt.want)) {
				t.Fatalf("RoundDownOrderPrice=%s, want %s", got, tt.want)
			}
		})
	}
}

func TestRestingBuyPriceUsesInstrumentSizeStep(t *testing.T) {
	inst := &model.Instrument{SizeStep: decimal.RequireFromString("0.0001")}
	got := RestingBuyPrice(inst, decimal.RequireFromString("5174.70"), false)
	if !got.Equal(decimal.RequireFromString("2587.3")) {
		t.Fatalf("RestingBuyPrice=%s, want 2587.3", got)
	}
}
