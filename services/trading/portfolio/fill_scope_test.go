package portfolio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNativeFillIdentityIsScopedByAccountAndInstrument(t *testing.T) {
	projection := NewProjection()
	ctx := context.Background()
	base := Fill{PrincipalID: "p", FillID: "123", CommandID: "c", LineageID: "l", ProductID: "manual", AccountID: "a", InstrumentID: "binance:live:spot:BTC-USDT", Side: SideBuy, Quantity: "1", Price: "100", Fee: "0.1", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: time.Now()}
	fills := []Fill{base, base, base}
	fills[1].InstrumentID = "binance:live:spot:ETH-USDT"
	fills[2].AccountID = "b"
	for _, fill := range fills {
		for range 2 {
			position, _, err := projection.ApplyFill(ctx, fill)
			if err != nil || position.Quantity != "1" {
				t.Fatalf("scoped replay=%+v err=%v", position, err)
			}
		}
	}
	for _, mutate := range []func(*Fill){func(f *Fill) { f.Quantity = "2" }, func(f *Fill) { f.ProductID = "funding" }, func(f *Fill) { f.LineageID = "other" }} {
		changed := base
		mutate(&changed)
		if _, _, err := projection.ApplyFill(ctx, changed); !errors.Is(err, ErrFillConflict) {
			t.Fatalf("same fact booked twice err=%v", err)
		}
	}
}
