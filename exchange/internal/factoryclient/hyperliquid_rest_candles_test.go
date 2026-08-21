package factoryclient

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestHyperliquidCandleWindowDerivesNewestPortableLimit(t *testing.T) {
	before := time.Now().UTC()
	start, end, err := hlCandleWindow(exchange.CandlesRequest{Interval: "1h", Limit: 168})
	if err != nil {
		t.Fatalf("hlCandleWindow: %v", err)
	}
	after := time.Now().UTC()
	startTime := time.UnixMilli(start)
	endTime := time.UnixMilli(end)
	if got := endTime.Sub(startTime); got != 168*time.Hour {
		t.Fatalf("derived window = %s, want 168h", got)
	}
	if endTime.Before(before.Add(-time.Millisecond)) || endTime.After(after.Add(time.Millisecond)) {
		t.Fatalf("derived end = %s, want current time between %s and %s", endTime, before, after)
	}
}

func TestHyperliquidCandlePageKeepsNewestRows(t *testing.T) {
	base := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	candles := []exchange.Candle{
		{OpenTime: base, CloseTime: base.Add(time.Hour)},
		{OpenTime: base.Add(time.Hour), CloseTime: base.Add(2 * time.Hour)},
		{OpenTime: base.Add(2 * time.Hour), CloseTime: base.Add(3 * time.Hour)},
	}

	page := hlCandlePage(candles, exchange.CandlesRequest{Interval: "1h", Limit: 2})
	if len(page.Candles) != 2 {
		t.Fatalf("candles = %d, want 2", len(page.Candles))
	}
	if !page.Candles[0].OpenTime.Equal(base.Add(time.Hour)) ||
		!page.Candles[1].OpenTime.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("kept candle window = %s..%s, want newest two rows", page.Candles[0].OpenTime, page.Candles[1].OpenTime)
	}
}
