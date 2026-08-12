package ledger

import (
	"context"
	"testing"
	"time"
)

func TestServiceSeparatesIdenticalEconomicIdentityAcrossPrincipals(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	entry := Entry{
		PrincipalID: "principal-a", EntryID: "shared-entry", Mode: ModeTest, ProductID: "funding", LineageID: "shared-lineage",
		AccountID: "shared-account", InstrumentID: "BTC-USDT", Kind: KindFunding, Amount: "1", Currency: "USDT", OccurredAt: now,
	}
	if err := service.Record(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	entry.PrincipalID, entry.Amount = "principal-b", "2"
	if err := service.Record(context.Background(), entry); err != nil {
		t.Fatalf("same external entry ID for another principal: %v", err)
	}
	for principal, amount := range map[string]string{"principal-a": "3", "principal-b": "4"} {
		if err := service.SetUnrealized(context.Background(), Unrealized{
			PrincipalID: principal, Mode: ModeTest, ProductID: "funding", LineageID: "shared-lineage", AccountID: "shared-account",
			InstrumentID: "BTC-USDT", Amount: amount, Currency: "USDT", AsOf: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	a, err := service.PnL(context.Background(), Query{PrincipalID: "principal-a", Mode: ModeTest, ProductID: "funding", LineageID: "shared-lineage", AccountID: "shared-account", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.PnL(context.Background(), Query{PrincipalID: "principal-b", Mode: ModeTest, ProductID: "funding", LineageID: "shared-lineage", AccountID: "shared-account", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Funding != "1" || a.UnrealizedPnL != "3" || a.NetPnL != "4" {
		t.Fatalf("principal-a pnl=%+v", a)
	}
	if b.Funding != "2" || b.UnrealizedPnL != "4" || b.NetPnL != "6" {
		t.Fatalf("principal-b pnl=%+v", b)
	}
}

func TestFundingSupersessionCannotRemoveAnotherPrincipalsIdenticalEntryID(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	for _, principal := range []string{"principal-a", "principal-b"} {
		if err := service.Record(context.Background(), Entry{
			PrincipalID: principal, EntryID: "shared-original", Mode: ModeTest, ProductID: "funding", LineageID: "shared-lineage",
			AccountID: "shared-account", InstrumentID: "BTC-USDT", Kind: KindFunding, Amount: "1", Currency: "USDT", OccurredAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	replacement := Entry{
		PrincipalID: "principal-a", EntryID: "shared-replacement", Mode: ModeTest, ProductID: "funding", LineageID: "shared-lineage",
		AccountID: "shared-account", InstrumentID: "BTC-USDT", Kind: KindFunding, Amount: "2", Currency: "USDT", OccurredAt: now.Add(time.Minute),
	}
	if err := service.SupersedeFunding(context.Background(), "shared-original", replacement); err != nil {
		t.Fatal(err)
	}
	a, _ := service.List(context.Background(), Query{PrincipalID: "principal-a", Mode: ModeTest})
	b, _ := service.List(context.Background(), Query{PrincipalID: "principal-b", Mode: ModeTest})
	if len(a) != 1 || a[0].EntryID != "shared-replacement" {
		t.Fatalf("principal-a entries=%+v", a)
	}
	if len(b) != 1 || b[0].EntryID != "shared-original" {
		t.Fatalf("principal-b entries=%+v", b)
	}
}

func TestServiceRejectsPrincipalFreeReadAndUnrealizedScopes(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	if _, err := service.List(context.Background(), Query{Mode: ModeTest}); err == nil {
		t.Fatal("principal-free ledger list accepted")
	}
	if err := service.SetUnrealized(context.Background(), Unrealized{
		Mode: ModeTest, ProductID: "funding", LineageID: "lineage", AccountID: "account", InstrumentID: "BTC-USDT",
		Amount: "1", Currency: "USDT", AsOf: time.Now(),
	}); err == nil {
		t.Fatal("principal-free unrealized scope accepted")
	}
}
