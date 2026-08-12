package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

// Two principals are allowed to use the same fill, entry and position
// identifiers — they are each other's namespace, not each other's collision.
// This proves the stores keep them apart all the way through a restart, when
// every in-memory projection is gone and only the schema's keys remain.
func TestPostgresPortfolioAndLedgerSeparatePrincipalCollisionsAcrossRestart(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	projection := portfolio.NewPersistentProjection(NewPortfolioStore(database))
	ledgerService := ledger.NewService(NewLedgerStore(database), func() time.Time { return now })
	fill := portfolio.Fill{
		PrincipalID: "principal-a", FillID: "shared-fill", CommandID: "command-a", LineageID: "shared-lineage", ProductID: "funding",
		Mode: portfolio.ModeTest, AccountID: "shared-account", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy,
		Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now,
	}
	for _, principal := range []string{"principal-a", "principal-b"} {
		fill.PrincipalID, fill.CommandID = principal, "command-"+principal
		fill.Price = map[string]string{"principal-a": "100", "principal-b": "200"}[principal]
		if _, _, err := projection.ApplyFill(ctx, fill); err != nil {
			t.Fatal(err)
		}
		if err := ledgerService.Record(ctx, ledger.Entry{PrincipalID: principal, EntryID: "shared-entry", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "shared-lineage", AccountID: "shared-account", InstrumentID: "BTC-USDT", Kind: ledger.KindFunding, Amount: map[string]string{"principal-a": "1", "principal-b": "2"}[principal], Currency: "USDT", OccurredAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := ledgerService.SetUnrealized(ctx, ledger.Unrealized{PrincipalID: principal, Mode: ledger.ModeTest, ProductID: "funding", LineageID: "shared-lineage", AccountID: "shared-account", InstrumentID: "BTC-USDT", Amount: map[string]string{"principal-a": "3", "principal-b": "4"}[principal], Currency: "USDT", AsOf: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := projection.ApplyMark("principal-a", portfolio.ModeTest, "shared-account", "funding", "shared-lineage", "BTC-USDT", "120", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	projection = portfolio.NewPersistentProjection(NewPortfolioStore(database))
	ledgerService = ledger.NewService(NewLedgerStore(database), func() time.Time { return now })
	for _, principal := range []string{"principal-a", "principal-b"} {
		positions, err := projection.ListContext(ctx, principal, portfolio.ModeTest, "shared-account", "funding")
		if err != nil || len(positions) != 1 || positions[0].PrincipalID != principal {
			t.Fatalf("principal=%s positions=%+v err=%v", principal, positions, err)
		}
		wantMark := map[string]string{"principal-a": "120", "principal-b": "0"}[principal]
		if positions[0].MarkPrice != wantMark {
			t.Fatalf("principal=%s mark=%s want=%s", principal, positions[0].MarkPrice, wantMark)
		}
		entries, err := ledgerService.List(ctx, ledger.Query{PrincipalID: principal, Mode: ledger.ModeTest, ProductID: "funding", LineageID: "shared-lineage"})
		if err != nil || len(entries) != 1 || entries[0].PrincipalID != principal {
			t.Fatalf("principal=%s entries=%+v err=%v", principal, entries, err)
		}
		snapshot, err := ledgerService.PnL(ctx, ledger.Query{PrincipalID: principal, Mode: ledger.ModeTest, ProductID: "funding", LineageID: "shared-lineage", AccountID: "shared-account", InstrumentID: "BTC-USDT"})
		wantNet := map[string]string{"principal-a": "4", "principal-b": "6"}[principal]
		if err != nil || snapshot.NetPnL != wantNet {
			t.Fatalf("principal=%s pnl=%+v err=%v", principal, snapshot, err)
		}
	}
}
