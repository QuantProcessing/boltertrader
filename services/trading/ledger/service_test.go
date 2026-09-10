package ledger

import (
	"context"
	"testing"
	"time"
)

func TestServiceAggregatesAttributedPnLWithoutCrossProductLeakage(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	entries := []Entry{
		{PrincipalID: "principal-a", EntryID: "entry-1", LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindRealizedPnL, Amount: "10", Currency: "USDT", OccurredAt: time.Now()},
		{PrincipalID: "principal-a", EntryID: "entry-2", LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindFee, Amount: "1.5", Currency: "USDT", OccurredAt: time.Now()},
		{PrincipalID: "principal-a", EntryID: "entry-3", LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindFunding, Amount: "2", Currency: "USDT", OccurredAt: time.Now()},
		{PrincipalID: "principal-a", EntryID: "entry-4", LineageID: "lineage-b", ProductID: "funding", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindRealizedPnL, Amount: "999", Currency: "USDT", OccurredAt: time.Now()},
	}
	for _, entry := range entries {
		if err := service.Record(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.SetUnrealized(context.Background(), Unrealized{PrincipalID: "principal-a",
		LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Amount: "5", Currency: "USDT", AsOf: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.PnL(context.Background(), Query{PrincipalID: "principal-a", ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ByCurrency[0].RealizedPnL != "10" || snapshot.ByCurrency[0].UnrealizedPnL != "5" || snapshot.ByCurrency[0].Fees != "1.5" || snapshot.ByCurrency[0].Funding != "2" || snapshot.ByCurrency[0].NetPnL != "15.5" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestServiceReportsMixedCurrenciesWithoutScalar(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	entries := []Entry{
		{PrincipalID: "principal-a", EntryID: "realized", LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindRealizedPnL, Amount: "10", Currency: "USDT", OccurredAt: time.Now()},
		{PrincipalID: "principal-a", EntryID: "fee", LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindFee, Amount: "1", Currency: "BNB", OccurredAt: time.Now()},
	}
	for _, entry := range entries {
		if err := service.Record(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := service.PnL(context.Background(), Query{PrincipalID: "principal-a", ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil || len(snapshot.ByCurrency) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.ByCurrency[0].Currency != "BNB" || snapshot.ByCurrency[0].BookedNetPnL != "-1" || snapshot.ByCurrency[1].Currency != "USDT" || snapshot.ByCurrency[1].BookedNetPnL != "10" || snapshot.ByCurrency[0].NetPnL != "" {
		t.Fatalf("mixed currency totals=%+v", snapshot)
	}
}

func TestServiceRejectsUnattributedAndDuplicateFacts(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	entry := Entry{PrincipalID: "principal-a", EntryID: "entry-a", ProductID: "manual", Kind: KindFee, Amount: "1", Currency: "USDT", OccurredAt: time.Now()}
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

func TestServiceRejectsUnrealizedPnLWithoutCurrency(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	err := service.SetUnrealized(context.Background(), Unrealized{PrincipalID: "principal-a",
		LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Amount: "5", AsOf: time.Now(),
	})
	if err == nil {
		t.Fatal("currency-free unrealized PnL accepted")
	}
}

func TestServicePreservesUnrealizedCurrency(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	entry := Entry{PrincipalID: "principal-a", EntryID: "realized", LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: KindRealizedPnL, Amount: "10", Currency: "USDT", OccurredAt: time.Now()}
	if err := service.Record(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if err := service.SetUnrealized(context.Background(), Unrealized{PrincipalID: "principal-a",
		LineageID: "lineage-a", ProductID: "manual", AccountID: "account-a", InstrumentID: "BTC-USDT", Amount: "5", Currency: "USDC", AsOf: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.PnL(context.Background(), Query{PrincipalID: "principal-a", ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil || len(snapshot.ByCurrency) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.ByCurrency[0].Currency != "USDC" || snapshot.ByCurrency[0].NetPnL != "5" || snapshot.ByCurrency[1].Currency != "USDT" || snapshot.ByCurrency[1].NetPnL != "10" {
		t.Fatalf("mixed valuation=%+v", snapshot)
	}
}
