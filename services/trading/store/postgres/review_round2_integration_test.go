package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

func TestPostgresLedgerAppendReplaySurvivesDatabaseRestartWithSubMicrosecondTimestamp(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	occurredAt := time.Date(2026, 8, 8, 12, 0, 0, 789, time.FixedZone("test", 8*60*60))
	entry := ledger.Entry{PrincipalID: "principal-a", EntryID: "append-replay", ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: ledger.KindFunding, Amount: "1", Currency: "USDT", OccurredAt: occurredAt}

	if err := NewLedgerStore(database).Append(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = reopen()
	if err := NewLedgerStore(database).Append(ctx, entry); err != nil {
		t.Fatalf("append replay after restart: %v", err)
	}
	entries, err := NewLedgerStore(database).Entries(ctx, ledger.Query{PrincipalID: "principal-a", ProductID: "funding", LineageID: "lineage-a"})
	if err != nil {
		t.Fatal(err)
	}
	wantOccurredAt := occurredAt.UTC().Truncate(time.Microsecond)
	if len(entries) != 1 || !entries[0].OccurredAt.Equal(wantOccurredAt) {
		t.Fatalf("restarted append entries=%+v, want timestamp %s", entries, wantOccurredAt)
	}
}

func TestPostgresFundingSupersessionReplaySurvivesDatabaseRestart(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 8, 12, 0, 0, 789, time.FixedZone("test", 8*60*60))
	original := ledger.Entry{PrincipalID: "principal-a", EntryID: "funding-original", ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: ledger.KindFunding, Amount: "1", Currency: "USDT", OccurredAt: now}
	replacement := original
	replacement.EntryID = "funding-replacement"
	replacement.Amount = "2"
	replacement.OccurredAt = now.Add(time.Minute)

	service := ledger.NewService(NewLedgerStore(database), func() time.Time { return now })
	if err := service.Record(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := service.SupersedeFunding(ctx, original.EntryID, replacement); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = reopen()
	service = ledger.NewService(NewLedgerStore(database), func() time.Time { return now.Add(2 * time.Minute) })
	if err := service.Record(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := service.SupersedeFunding(ctx, original.EntryID, replacement); err != nil {
		t.Fatal(err)
	}
	entries, err := service.List(ctx, ledger.Query{PrincipalID: "principal-a", ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	wantOccurredAt := replacement.OccurredAt.UTC().Truncate(time.Microsecond)
	if len(entries) != 1 || entries[0].EntryID != replacement.EntryID || entries[0].ProductID != replacement.ProductID || entries[0].LineageID != replacement.LineageID || entries[0].AccountID != replacement.AccountID || entries[0].InstrumentID != replacement.InstrumentID || entries[0].Kind != replacement.Kind || entries[0].Amount != replacement.Amount || entries[0].Currency != replacement.Currency || !entries[0].OccurredAt.Equal(wantOccurredAt) {
		t.Fatalf("restarted supersession entries=%+v, want replacement only", entries)
	}
	snapshot, err := service.PnL(ctx, ledger.Query{PrincipalID: "principal-a", ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Funding != "2" {
		t.Fatalf("restarted Funding PnL=%s, want 2", snapshot.Funding)
	}
}

func TestPostgresMarkObservationSurvivesDelayedFillAndDatabaseRestart(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	projection := portfolio.NewPersistentProjection(NewPortfolioStore(database))
	fill := portfolio.Fill{PrincipalID: "principal-a",
		FillID: "fill-initial", CommandID: "command-initial", LineageID: "lineage-a", ProductID: "funding",
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy, Quantity: "1", Price: "100",
		Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: base,
	}
	if _, _, err := projection.ApplyFill(ctx, fill); err != nil {
		t.Fatal(err)
	}
	newestMarkAt := base.Add(4 * time.Minute)
	if _, err := projection.ApplyMark("principal-a", "account-a", "funding", "lineage-a", "BTC-USDT", "120", newestMarkAt); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = reopen()
	projection = portfolio.NewPersistentProjection(NewPortfolioStore(database))
	delayed := fill
	delayed.FillID, delayed.CommandID = "fill-delayed", "command-delayed"
	delayed.Quantity, delayed.Price = "0.5", "110"
	delayed.OccurredAt = base.Add(time.Minute)
	if _, _, err := projection.ApplyFill(ctx, delayed); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = reopen()
	projection = portfolio.NewPersistentProjection(NewPortfolioStore(database))
	position, err := projection.ApplyMark("principal-a", "account-a", "funding", "lineage-a", "BTC-USDT", "90", base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if position.MarkPrice != "120" {
		t.Fatalf("older mark overwrote persisted newest mark after delayed fill: %+v", position)
	}
	var persisted time.Time
	if err := database.QueryRowContext(ctx, `SELECT mark_observed_at FROM positions WHERE mode='test' AND product_id='funding' AND lineage_id='lineage-a' AND account_id='account-a' AND instrument_id='BTC-USDT'`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Equal(newestMarkAt) {
		t.Fatalf("persisted mark observation=%s, want %s", persisted, newestMarkAt)
	}
}
