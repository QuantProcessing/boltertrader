package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/accounting"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

// Frozen pre-v6 SQL: upgrade fixtures must not depend on today's store shape.
func insertPreFeePosition(ctx context.Context, db *sql.DB, p portfolio.Position) error {
	_, err := db.ExecContext(ctx, `INSERT INTO positions(principal_id,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,mark_observed_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, p.PrincipalID, p.ProductID, p.LineageID, p.AccountID, p.InstrumentID, p.SettlementCurrency, p.Quantity, p.AverageEntryPrice, p.MarkPrice, p.RealizedPnL, p.UnrealizedPnL, p.MarkObservedAt, p.UpdatedAt)
	return err
}

func TestPostgresFeeInventoryReplayCompletesLedgerAfterRestart(t *testing.T) {
	db, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	command := orders.Command{PrincipalID: "p", CommandID: "c", AccountID: "a", ProductID: "manual", LineageID: "l"}
	fill := accounting.FillFact{FillID: "f", CommandID: "c", AccountID: "a", InstrumentID: "binance:live:spot:BTC-USDT", InstrumentProduct: "spot", BaseAsset: "BTC", Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: "0.001", FeeCurrency: "BTC", SettlementCurrency: "USDT", OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}
	if _, err := db.Exec(`ALTER TABLE ledger_entries ADD CONSTRAINT interrupt_fee CHECK (kind <> 'fee')`); err != nil {
		t.Fatal(err)
	}
	record := func() error {
		return accounting.RecordFills(ctx, portfolio.NewPersistentProjection(NewPortfolioStore(db)), ledger.NewService(NewLedgerStore(db), time.Now), command, []accounting.FillFact{fill})
	}
	if err := record(); err == nil {
		t.Fatal("ledger failure not injected")
	}
	positions, err := NewPortfolioStore(db).List(ctx, "p", "a", "manual")
	if err != nil || len(positions) != 1 || positions[0].Quantity != "0.999" {
		t.Fatalf("position commit=%+v err=%v", positions, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = reopen()
	if _, err := db.Exec(`ALTER TABLE ledger_entries DROP CONSTRAINT interrupt_fee`); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := record(); err != nil {
			t.Fatal(err)
		}
	}
	positions, err = NewPortfolioStore(db).List(ctx, "p", "a", "manual")
	if err != nil || positions[0].Quantity != "0.999" || positions[0].AccountingStatus != "ready" {
		t.Fatalf("replay=%+v err=%v", positions, err)
	}
	snapshot, err := ledger.NewService(NewLedgerStore(db), time.Now).PnL(ctx, ledger.Query{PrincipalID: "p", ProductID: "manual", LineageID: "l", AccountID: "a", InstrumentID: fill.InstrumentID})
	if err != nil || len(snapshot.ByCurrency) != 1 || snapshot.ByCurrency[0].BookedNetPnL != "-0.001" {
		t.Fatalf("pnl=%+v err=%v", snapshot, err)
	}
	var product, base string
	var count int
	if err := db.QueryRow(`SELECT instrument_product,base_asset FROM portfolio_fills`).Scan(&product, &base); err != nil || product != "spot" || base != "BTC" {
		t.Fatalf("fill metadata=%s/%s err=%v", product, base, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM ledger_entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ledger count=%d err=%v", count, err)
	}
}

func TestFeeContextMigrationPreservesMoneyAndBlocksUnverifiedInventory(t *testing.T) {
	db, reopen := openIntegrationDatabaseAtVersion(t, 5)
	ctx := context.Background()
	when := time.Now().UTC().Truncate(time.Microsecond)
	for _, test := range []struct{ lineage, instrument, currency, fee, status string }{
		{"quote", "binance:live:spot:BTC-USDT", "USDT", "0.1", "ready"},
		{"base", "binance:live:spot:BTC-USDT", "BTC", "0.001", "legacy_fee_review"},
		{"third", "binance:live:spot:BTC-USDT", "BNB", "0.01", "legacy_fee_review"},
		{"okx", "okx:live:spot:BTC-USDT", "USDT", "-0.1", "legacy_fee_review"},
	} {
		fill := portfolio.Fill{PrincipalID: "p", FillID: test.lineage, CommandID: "c", ProductID: "manual", LineageID: test.lineage, AccountID: "a", InstrumentID: test.instrument, Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: test.fee, FeeCurrency: test.currency, SettlementCurrency: "USDT", OccurredAt: when}
		_, err := db.ExecContext(ctx, `INSERT INTO portfolio_fills(principal_id,fill_id,fill_hash,command_id,product_id,lineage_id,account_id,instrument_id,side,quantity,price,fee,fee_currency,settlement_currency,realized_pnl,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,0,$15)`, fill.PrincipalID, fill.FillID, portfolio.FillFingerprint(fill), fill.CommandID, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID, fill.Side, fill.Quantity, fill.Price, fill.Fee, fill.FeeCurrency, fill.SettlementCurrency, fill.OccurredAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := insertPreFeePosition(ctx, db, portfolio.Position{PrincipalID: "p", ProductID: "manual", LineageID: test.lineage, AccountID: "a", InstrumentID: test.instrument, SettlementCurrency: "USDT", Quantity: "1", AverageEntryPrice: "100", MarkPrice: "0", RealizedPnL: "0", UnrealizedPnL: "0", UpdatedAt: when}); err != nil {
			t.Fatal(err)
		}
		if err := NewLedgerStore(db).Append(ctx, ledger.Entry{PrincipalID: "p", EntryID: fill.FillID + ":fee", ProductID: "manual", LineageID: test.lineage, AccountID: "a", InstrumentID: test.instrument, Kind: ledger.KindFee, Amount: test.fee, Currency: test.currency, OccurredAt: when}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = reopen()
	positions, err := NewPortfolioStore(db).List(ctx, "p", "a", "manual")
	if err != nil || len(positions) != 4 {
		t.Fatalf("positions=%+v err=%v", positions, err)
	}
	for _, position := range positions {
		ready := position.LineageID == "quote"
		if position.Quantity != "1" || (position.AccountingStatus == "ready") != ready {
			t.Fatalf("migration rewrote/accepted historical inventory: %+v", position)
		}
		var hash, amount, currency string
		if err := db.QueryRow(`SELECT fill_hash,fee::text,fee_currency FROM portfolio_fills WHERE fill_id=$1`, position.LineageID).Scan(&hash, &amount, &currency); err != nil {
			t.Fatal(err)
		}
		fill := portfolio.Fill{PrincipalID: "p", FillID: position.LineageID, CommandID: "c", ProductID: "manual", LineageID: position.LineageID, AccountID: "a", InstrumentID: position.InstrumentID, Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: amount, FeeCurrency: currency, SettlementCurrency: "USDT", OccurredAt: when}
		if hash != portfolio.FillFingerprint(fill) {
			t.Fatal("historical hash changed")
		}
		var ledgerAmount string
		if err := db.QueryRow(`SELECT amount::text FROM ledger_entries WHERE lineage_id=$1`, position.LineageID).Scan(&ledgerAmount); err != nil || amount != ledgerAmount {
			t.Fatalf("ledger changed: %s/%s %v", amount, ledgerAmount, err)
		}
		fill.InstrumentProduct, fill.BaseAsset = "spot", "BTC"
		if ready {
			if p, _, err := portfolio.NewPersistentProjection(NewPortfolioStore(db)).ApplyFill(ctx, fill); err != nil || p.Quantity != "1" {
				t.Fatalf("safe enrichment replay=%+v %v", p, err)
			}
		}
		fill.FillID += "-next"
		_, _, err := portfolio.NewPersistentProjection(NewPortfolioStore(db)).ApplyFill(ctx, fill)
		if ready && err != nil || !ready && !errors.Is(err, portfolio.ErrAccountingReview) {
			t.Fatalf("lineage=%s new fill err=%v", position.LineageID, err)
		}
	}
}
