package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

func TestScopedIdentityMigrationPreservesBookedFillsAndLedger(t *testing.T) {
	database, reopen := openIntegrationDatabaseAtVersion(t, 2)
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	fill := portfolio.Fill{PrincipalID: "p", FillID: "123", CommandID: "c", LineageID: "l", ProductID: "manual", AccountID: "a", InstrumentID: "binance:live:spot:BTC-USDT", Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: "0.1", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now}
	// These rows are what v2 already booked. Use its SQL shape so the upgrade
	// test does not depend on the new store being compatible with the old key.
	_, err := database.ExecContext(ctx, `INSERT INTO portfolio_fills(principal_id,fill_id,fill_hash,command_id,product_id,lineage_id,account_id,instrument_id,side,quantity,price,fee,fee_currency,settlement_currency,realized_pnl,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,0,$15)`, fill.PrincipalID, fill.FillID, portfolio.FillFingerprint(fill), fill.CommandID, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID, fill.Side, fill.Quantity, fill.Price, fill.Fee, fill.FeeCurrency, fill.SettlementCurrency, fill.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertPreFeePosition(ctx, database, portfolio.Position{PrincipalID: "p", ProductID: "manual", LineageID: "l", AccountID: "a", InstrumentID: fill.InstrumentID, SettlementCurrency: "USDT", Quantity: "1", AverageEntryPrice: "100", MarkPrice: "0", RealizedPnL: "0", UnrealizedPnL: "0", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `INSERT INTO ledger_entries(principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ('p','123:fee','manual','l','a',$1,'fee','0.1','USDT',$2)`, fill.InstrumentID, now)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, database); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	projection := portfolio.NewPersistentProjection(NewPortfolioStore(database))
	book := ledger.NewService(NewLedgerStore(database), func() time.Time { return now })
	fills := []portfolio.Fill{fill, fill, fill}
	fills[1].InstrumentID = "binance:live:spot:ETH-USDT"
	fills[2].AccountID = "b"
	for _, item := range fills {
		for range 2 {
			position, result, err := projection.ApplyFill(ctx, item)
			if err != nil || position.Quantity != "1" {
				t.Fatalf("fill scope=%+v position=%+v err=%v", item, position, err)
			}
			if err := book.Record(ctx, ledger.Entry{PrincipalID: item.PrincipalID, EntryID: item.FillID + ":fee", ProductID: item.ProductID, LineageID: item.LineageID, AccountID: item.AccountID, InstrumentID: item.InstrumentID, Kind: ledger.KindFee, Amount: result.Fee, Currency: item.FeeCurrency, OccurredAt: item.OccurredAt}); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := book.List(ctx, ledger.Query{PrincipalID: "p"})
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	changed := fill
	changed.ProductID = "funding"
	if _, _, err := projection.ApplyFill(ctx, changed); !errors.Is(err, portfolio.ErrFillConflict) {
		t.Fatalf("reattribution err=%v", err)
	}
	changed.Quantity = "2"
	changed.ProductID = fill.ProductID
	if _, _, err := projection.ApplyFill(ctx, changed); !errors.Is(err, portfolio.ErrFillConflict) {
		t.Fatalf("quantity conflict err=%v", err)
	}
	var count int
	var hash string
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM portfolio_fills`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("fill count=%d err=%v", count, err)
	}
	if err := database.QueryRowContext(ctx, `SELECT fill_hash FROM portfolio_fills WHERE principal_id='p' AND account_id='a' AND instrument_id=$1 AND fill_id='123'`, fill.InstrumentID).Scan(&hash); err != nil || hash != portfolio.FillFingerprint(fill) {
		t.Fatalf("historical fingerprint changed err=%v", err)
	}
}

func TestFundingSupersessionDoesNotDeleteAnotherFillScope(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	ctx := context.Background()
	book := ledger.NewService(NewLedgerStore(database), time.Now)
	base := ledger.Entry{PrincipalID: "p", EntryID: "shared", ProductID: "funding", LineageID: "l", AccountID: "a", InstrumentID: "BTC-USDT", Kind: ledger.KindFunding, Amount: "1", Currency: "USDT", OccurredAt: time.Now()}
	entries := []ledger.Entry{base, base, base}
	entries[1].AccountID = "b"
	entries[2].InstrumentID = "ETH-USDT"
	for _, entry := range entries {
		if err := book.Record(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	replacement := base
	replacement.EntryID, replacement.Amount = "replacement", "2"
	for range 2 {
		if err := book.SupersedeFunding(ctx, base.EntryID, replacement); err != nil {
			t.Fatal(err)
		}
	}
	for i, entry := range entries {
		got, err := book.List(ctx, ledger.Query{PrincipalID: "p", AccountID: entry.AccountID, InstrumentID: entry.InstrumentID})
		want := "1"
		if i == 0 {
			want = "2"
		}
		if err != nil || len(got) != 1 || got[0].Amount != want {
			t.Fatalf("scope=%+v entries=%+v err=%v", entry, got, err)
		}
	}
}
