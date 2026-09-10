package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEntriesAndFundingSupersessionUseAccountInstrumentScope(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), time.Now)
	base := Entry{PrincipalID: "p", EntryID: "123", LineageID: "l", ProductID: "manual", AccountID: "a", InstrumentID: "BTC-USDT", Kind: KindFunding, Amount: "1", Currency: "USDT", OccurredAt: time.Now()}
	entries := []Entry{base, base, base}
	entries[1].InstrumentID = "ETH-USDT"
	entries[2].AccountID = "b"
	for _, entry := range entries {
		for range 2 {
			if err := service.Record(ctx, entry); err != nil {
				t.Fatal(err)
			}
		}
	}
	changed := base
	changed.LineageID = "other"
	if err := service.Record(ctx, changed); !errors.Is(err, ErrEntryConflict) {
		t.Fatalf("reattribution err=%v", err)
	}
	replacement := base
	replacement.EntryID, replacement.Amount = "replacement", "2"
	for range 2 {
		if err := service.SupersedeFunding(ctx, base.EntryID, replacement); err != nil {
			t.Fatal(err)
		}
	}
	for i, entry := range entries {
		got, err := service.List(ctx, Query{PrincipalID: "p", AccountID: entry.AccountID, InstrumentID: entry.InstrumentID})
		want := "1"
		if i == 0 {
			want = "2"
		}
		if err != nil || len(got) != 1 || got[0].Amount != want {
			t.Fatalf("scope=%+v entries=%+v err=%v", entry, got, err)
		}
	}
}
