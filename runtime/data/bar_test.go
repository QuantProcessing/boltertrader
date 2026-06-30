package data

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var inst = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}

func trade(price, qty string, ts time.Time) model.TradeTick {
	return model.TradeTick{InstrumentID: inst, Price: d(price), Quantity: d(qty), Timestamp: ts}
}

// TestBarAggregation builds 1-minute bars from trades and checks OHLCV and the
// bucket-crossing emit.
func TestBarAggregation(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	agg := NewBarAggregator(inst, time.Minute, "1m")

	// Three trades in minute 0.
	if _, ok := agg.OnTrade(trade("100", "1", base.Add(1*time.Second))); ok {
		t.Fatal("first trade should not complete a bar")
	}
	if _, ok := agg.OnTrade(trade("105", "2", base.Add(10*time.Second))); ok {
		t.Fatal("same-bucket trade should not complete a bar")
	}
	if _, ok := agg.OnTrade(trade("95", "1", base.Add(30*time.Second))); ok {
		t.Fatal("same-bucket trade should not complete a bar")
	}

	// A trade in minute 1 closes minute 0's bar.
	bar, ok := agg.OnTrade(trade("110", "1", base.Add(70*time.Second)))
	if !ok {
		t.Fatal("crossing into a new bucket should complete the previous bar")
	}
	if !bar.Open.Equal(d("100")) || !bar.High.Equal(d("105")) || !bar.Low.Equal(d("95")) || !bar.Close.Equal(d("95")) {
		t.Errorf("OHLC=%s/%s/%s/%s, want 100/105/95/95", bar.Open, bar.High, bar.Low, bar.Close)
	}
	if !bar.Volume.Equal(d("4")) {
		t.Errorf("volume=%s, want 4", bar.Volume)
	}
	if !bar.OpenTime.Equal(base) {
		t.Errorf("openTime=%v, want %v", bar.OpenTime, base)
	}

	// Flush returns the in-progress minute-1 bar.
	last, ok := agg.Flush()
	if !ok || !last.Open.Equal(d("110")) {
		t.Errorf("flush bar open=%s ok=%v, want 110/true", last.Open, ok)
	}
}
