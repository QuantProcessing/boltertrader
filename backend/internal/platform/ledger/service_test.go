package ledger

import (
	"context"
	"testing"
	"time"
)

func TestServiceAggregatesAttributedPnLWithoutCrossProductLeakage(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	entries := []Entry{
		{EntryID: "entry-1", Mode: ModeTest, LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindRealizedPnL, Amount: "10", Currency: "USDT", OccurredAt: time.Now()},
		{EntryID: "entry-2", Mode: ModeTest, LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindFee, Amount: "1.5", Currency: "USDT", OccurredAt: time.Now()},
		{EntryID: "entry-3", Mode: ModeTest, LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindFunding, Amount: "2", Currency: "USDT", OccurredAt: time.Now()},
		{EntryID: "entry-4", Mode: ModeTest, LineageID: "lineage-b", ProductID: "funding", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindRealizedPnL, Amount: "999", Currency: "USDT", OccurredAt: time.Now()},
		{EntryID: "entry-5", Mode: ModeLive, LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindRealizedPnL, Amount: "777", Currency: "USDT", OccurredAt: time.Now()},
	}
	for _, entry := range entries {
		if err := service.Record(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.SetUnrealized(context.Background(), Unrealized{
		Mode: ModeTest, LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Amount: "5", AsOf: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.PnL(context.Background(), Query{Mode: ModeTest, ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RealizedPnL != "10" || snapshot.UnrealizedPnL != "5" || snapshot.Fees != "1.5" || snapshot.Funding != "2" || snapshot.NetPnL != "15.5" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestServiceRejectsUnattributedAndDuplicateFacts(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	entry := Entry{EntryID: "entry-a", Mode: ModeTest, ProductID: "manual", Kind: KindFee, Amount: "1", Currency: "USDT", OccurredAt: time.Now()}
	if err := service.Record(context.Background(), entry); err == nil {
		t.Fatal("unattributed entry accepted")
	}
	entry.LineageID, entry.AccountID, entry.InstrumentID = "lineage-a", "account-a", "BTC-USDT"
	if err := service.Record(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if err := service.Record(context.Background(), entry); err != nil {
		t.Fatalf("identical replay failed: %v", err)
	}
	entry.Amount = "2"
	if err := service.Record(context.Background(), entry); err == nil {
		t.Fatal("conflicting immutable entry accepted")
	}
}
