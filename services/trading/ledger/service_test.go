package ledger

import (
	"context"
	"errors"
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
	if snapshot.RealizedPnL != "10" || snapshot.UnrealizedPnL != "5" || snapshot.Fees != "1.5" || snapshot.Funding != "2" || snapshot.NetPnL != "15.5" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestServiceRejectsMixedCurrencyPnLScalar(t *testing.T) {
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
	_, err := service.PnL(context.Background(), Query{PrincipalID: "principal-a", ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("PnL error=%v, want ErrCurrencyMismatch", err)
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

func TestServiceRejectsUnrealizedCurrencyMismatch(t *testing.T) {
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
	_, err := service.PnL(context.Background(), Query{PrincipalID: "principal-a", ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("PnL error=%v, want ErrCurrencyMismatch", err)
	}
}
