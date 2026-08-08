package portfolio

import (
	"context"
	"testing"
	"time"
)

func TestDelayedFillCannotRegressMarkObservationOrdering(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	projection := NewProjection()
	fill := Fill{PrincipalID: "principal-a",
		FillID: "fill-initial", CommandID: "command-initial", LineageID: "lineage-a", ProductID: "funding", Mode: ModeTest,
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: SideBuy, Quantity: "1", Price: "100",
		Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: base,
	}
	if _, _, err := projection.ApplyFill(ctx, fill); err != nil {
		t.Fatal(err)
	}
	newestMarkAt := base.Add(4 * time.Minute)
	if _, err := projection.ApplyMark("principal-a", ModeTest, "account-a", "funding", "lineage-a", "BTC-USDT", "120", newestMarkAt); err != nil {
		t.Fatal(err)
	}

	delayed := fill
	delayed.FillID, delayed.CommandID = "fill-delayed", "command-delayed"
	delayed.Quantity, delayed.Price = "0.5", "110"
	delayed.OccurredAt = base.Add(time.Minute)
	if _, _, err := projection.ApplyFill(ctx, delayed); err != nil {
		t.Fatal(err)
	}
	position, err := projection.ApplyMark("principal-a", ModeTest, "account-a", "funding", "lineage-a", "BTC-USDT", "90", base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if position.MarkPrice != "120" {
		t.Fatalf("older mark overwrote newest mark after delayed fill: %+v", position)
	}
}
