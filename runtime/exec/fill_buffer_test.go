package exec_test

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/shopspring/decimal"
)

func TestFillBufferCountIncludesVenueOnlyFills(t *testing.T) {
	buf := exec.NewFillBuffer()
	fill := model.Fill{
		InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		VenueOrderID: "venue-only",
		TradeID:      "trade-1",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	buf.Buffer(fill)
	if got := buf.Count(); got != 1 {
		t.Fatalf("count=%d, want 1", got)
	}
}
