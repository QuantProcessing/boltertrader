package ledger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRebatesRemainNativeAndUnknownValuationIsNotZero(t *testing.T) {
	ctx := context.Background()
	when := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	service := NewService(NewMemoryStore(), func() time.Time { return when.Add(time.Hour) })
	query := Query{PrincipalID: "p", ProductID: "manual", LineageID: "l", AccountID: "a", InstrumentID: "BTC-USDT"}
	if err := service.Record(ctx, Entry{PrincipalID: "p", ProductID: "manual", LineageID: "l", AccountID: "a", InstrumentID: "BTC-USDT", EntryID: "f:fee", Kind: KindFee, Amount: "-0.00000001", Currency: "BTC", OccurredAt: when}); err != nil {
		t.Fatal(err)
	}
	before, err := service.PnL(ctx, query)
	if err != nil || len(before.ByCurrency) != 1 || before.ByCurrency[0].BookedNetPnL != "0.00000001" || !before.AsOf.Equal(when) {
		t.Fatalf("pnl=%+v err=%v", before, err)
	}
	raw, err := json.Marshal(before)
	if err != nil || strings.Contains(string(raw), `"netPnl"`) || strings.Contains(string(raw), `"unrealizedPnl"`) || strings.Contains(string(raw), `"valuationAsOf"`) {
		t.Fatalf("unknown valuation became zero: %s %v", raw, err)
	}
	if err := service.SetUnrealized(ctx, Unrealized{PrincipalID: "p", ProductID: "manual", LineageID: "l", AccountID: "a", InstrumentID: "BTC-USDT", Amount: "0", Currency: "USDT", AsOf: when.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	valued, err := service.PnL(ctx, query)
	if err != nil || len(valued.ByCurrency) != 2 || valued.ByCurrency[0].NetPnL != "0.00000001" || valued.ByCurrency[1].NetPnL != "0" || valued.ValuationAsOf == nil {
		t.Fatalf("observed zero valuation=%+v err=%v", valued, err)
	}
}
