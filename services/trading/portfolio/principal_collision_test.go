package portfolio

import (
	"context"
	"testing"
	"time"
)

func TestProjectionSeparatesIdenticalEconomicIdentityAcrossPrincipals(t *testing.T) {
	projection := NewProjection()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	fill := Fill{
		PrincipalID: "principal-a", FillID: "shared-fill", CommandID: "command-a", LineageID: "shared-lineage", ProductID: "funding",
		AccountID: "shared-account", InstrumentID: "BTC-USDT", Side: SideBuy,
		Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now,
	}
	if _, _, err := projection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatal(err)
	}
	fill.PrincipalID, fill.CommandID, fill.Quantity, fill.Price = "principal-b", "command-b", "2", "200"
	if _, _, err := projection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatalf("same external fill ID for another principal: %v", err)
	}
	if _, err := projection.ApplyMark("principal-a", "shared-account", "funding", "shared-lineage", "BTC-USDT", "120", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	a := projection.List("principal-a", "shared-account", "funding")
	b := projection.List("principal-b", "shared-account", "funding")
	if len(a) != 1 || a[0].PrincipalID != "principal-a" || a[0].Quantity != "1" || a[0].MarkPrice != "120" {
		t.Fatalf("principal-a positions=%+v", a)
	}
	if len(b) != 1 || b[0].PrincipalID != "principal-b" || b[0].Quantity != "2" || b[0].MarkPrice != "0" {
		t.Fatalf("principal-b positions=%+v", b)
	}
}
