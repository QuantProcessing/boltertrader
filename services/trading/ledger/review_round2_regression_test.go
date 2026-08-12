package ledger

import (
	"context"
	"testing"
	"time"
)

func TestFundingSupersessionReplayDoesNotRestoreOriginalEntry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	original := Entry{PrincipalID: "principal-a", EntryID: "funding-original", Mode: ModeTest, ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindFunding, Amount: "1", Currency: "USDT", OccurredAt: now}
	replacement := original
	replacement.EntryID = "funding-replacement"
	replacement.Amount = "2"
	replacement.OccurredAt = now.Add(time.Minute)

	service := NewService(store, func() time.Time { return now })
	if err := service.Record(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := service.SupersedeFunding(ctx, original.EntryID, replacement); err != nil {
		t.Fatal(err)
	}

	// Simulate a restart replaying the complete authoritative fact stream.
	restarted := NewService(store, func() time.Time { return now.Add(2 * time.Minute) })
	if err := restarted.Record(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := restarted.SupersedeFunding(ctx, original.EntryID, replacement); err != nil {
		t.Fatal(err)
	}

	entries, err := restarted.List(ctx, Query{PrincipalID: "principal-a", Mode: ModeTest, ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != replacement {
		t.Fatalf("replayed supersession entries=%+v, want replacement only", entries)
	}
	snapshot, err := restarted.PnL(ctx, Query{PrincipalID: "principal-a", Mode: ModeTest, ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Funding != "2" {
		t.Fatalf("replayed Funding PnL=%s, want 2", snapshot.Funding)
	}
}
