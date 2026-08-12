package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

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

func TestPrincipalScopedAccountingMigrationBackfillsUniqueDurableCommandOwner(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	applyIntegrationMigrations(t, database, 1, 12)
	recordAppliedIntegrationMigrations(t, database, 1, 12)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	insertLegacyAccountingCommand(t, database, "principal-a", "command-a", now)
	statements := []string{
		`INSERT INTO platform_positions(mode,product_id,lineage_id,account_id,instrument_id,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,updated_at,settlement_currency,mark_observed_at) VALUES ('test','funding','shared-lineage','shared-account','BTC-USDT',1,100,110,0,10,$1,'USDT',$1)`,
		`INSERT INTO platform_portfolio_fills(fill_id,fill_hash,command_id,mode,product_id,lineage_id,account_id,instrument_id,side,quantity,price,fee,fee_currency,realized_pnl,occurred_at,settlement_currency) VALUES ('fill-a','hash-a','command-a','test','funding','shared-lineage','shared-account','BTC-USDT','buy',1,100,0,'USDT',0,$1,'USDT')`,
		`INSERT INTO platform_ledger_entries(entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ('entry-a','test','funding','shared-lineage','shared-account','BTC-USDT','funding',1,'USDT',$1)`,
		`INSERT INTO platform_unrealized_pnl(mode,product_id,lineage_id,account_id,instrument_id,amount,as_of,currency) VALUES ('test','funding','shared-lineage','shared-account','BTC-USDT',10,$1,'USDT')`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"platform_positions", "platform_portfolio_fills", "platform_ledger_entries", "platform_unrealized_pnl"} {
		var principal string
		if err := database.QueryRow(`SELECT principal_id FROM ` + table).Scan(&principal); err != nil || principal != "principal-a" {
			t.Fatalf("table=%s principal=%q err=%v", table, principal, err)
		}
	}
}

func TestPrincipalScopedAccountingMigrationPreservesExactDurableCommandOwner(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	applyIntegrationMigrations(t, database, 1, 12)
	recordAppliedIntegrationMigrations(t, database, 1, 12)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	const principal = " principal-a "
	insertLegacyAccountingCommand(t, database, principal, "command-a", now)
	statements := []string{
		`INSERT INTO platform_positions(mode,product_id,lineage_id,account_id,instrument_id,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,updated_at,settlement_currency,mark_observed_at) VALUES ('test','funding','shared-lineage','shared-account','BTC-USDT',1,100,110,0,10,$1,'USDT',$1)`,
		`INSERT INTO platform_portfolio_fills(fill_id,fill_hash,command_id,mode,product_id,lineage_id,account_id,instrument_id,side,quantity,price,fee,fee_currency,realized_pnl,occurred_at,settlement_currency) VALUES ('fill-a','hash-a','command-a','test','funding','shared-lineage','shared-account','BTC-USDT','buy',1,100,0,'USDT',0,$1,'USDT')`,
		`INSERT INTO platform_ledger_entries(entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ('entry-a','test','funding','shared-lineage','shared-account','BTC-USDT','funding',1,'USDT',$1)`,
		`INSERT INTO platform_unrealized_pnl(mode,product_id,lineage_id,account_id,instrument_id,amount,as_of,currency) VALUES ('test','funding','shared-lineage','shared-account','BTC-USDT',10,$1,'USDT')`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"platform_positions", "platform_portfolio_fills", "platform_ledger_entries", "platform_unrealized_pnl"} {
		var got string
		if err := database.QueryRow(`SELECT principal_id FROM ` + table).Scan(&got); err != nil || got != principal {
			t.Fatalf("table=%s principal=%q want=%q err=%v", table, got, principal, err)
		}
	}
}

func TestPrincipalScopedAccountingMigrationRejectsAmbiguousDurableCommandOwner(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	applyIntegrationMigrations(t, database, 1, 12)
	recordAppliedIntegrationMigrations(t, database, 1, 12)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	insertLegacyAccountingCommand(t, database, "principal-a", "command-a", now)
	insertLegacyAccountingCommand(t, database, "principal-b", "command-b", now)
	if _, err := database.Exec(`INSERT INTO platform_positions(mode,product_id,lineage_id,account_id,instrument_id,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,updated_at,settlement_currency,mark_observed_at) VALUES ('test','funding','shared-lineage','shared-account','BTC-USDT',1,100,110,0,10,$1,'USDT',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err == nil || !strings.Contains(err.Error(), "principal accounting ownership is missing or ambiguous") {
		t.Fatalf("ambiguous ownership migration error=%v", err)
	}
	assertPrincipalMigrationRolledBack(t, database)
}

func TestPrincipalScopedAccountingMigrationRejectsMissingDurableCommandOwner(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	applyIntegrationMigrations(t, database, 1, 12)
	recordAppliedIntegrationMigrations(t, database, 1, 12)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`INSERT INTO platform_positions(mode,product_id,lineage_id,account_id,instrument_id,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,updated_at,settlement_currency,mark_observed_at) VALUES ('test','funding','shared-lineage','shared-account','BTC-USDT',1,100,110,0,10,$1,'USDT',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err == nil || !strings.Contains(err.Error(), "principal accounting ownership is missing or ambiguous") {
		t.Fatalf("missing ownership migration error=%v", err)
	}
	assertPrincipalMigrationRolledBack(t, database)
}

func TestPrincipalScopedAccountingMigrationRejectsBlankDurableCommandOwner(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	applyIntegrationMigrations(t, database, 1, 12)
	recordAppliedIntegrationMigrations(t, database, 1, 12)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	insertLegacyAccountingCommand(t, database, "   ", "command-blank", now)
	if _, err := database.Exec(`INSERT INTO platform_positions(mode,product_id,lineage_id,account_id,instrument_id,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,updated_at,settlement_currency,mark_observed_at) VALUES ('test','funding','shared-lineage','shared-account','BTC-USDT',1,100,110,0,10,$1,'USDT',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err == nil || !strings.Contains(err.Error(), "principal accounting ownership is missing or ambiguous") {
		t.Fatalf("blank ownership migration error=%v", err)
	}
	assertPrincipalMigrationRolledBack(t, database)
}

func insertLegacyAccountingCommand(t *testing.T, database *sql.DB, principal, commandID string, now time.Time) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO platform_commands(command_id,principal_id,account_id,mode,product_id,lineage_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,created_at,updated_at) VALUES ($1,$2,'shared-account','test','funding','shared-lineage',$1,'POST','/internal/funding','funding.open','filled','{}','hash',$3,$3)`, commandID, principal, now)
	if err != nil {
		t.Fatal(err)
	}
}

func recordAppliedIntegrationMigrations(t *testing.T, database *sql.DB, first, last int) {
	t.Helper()
	migrations, err := embeddedPlatformMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version < first || migration.version > last {
			continue
		}
		if _, err := database.Exec(`INSERT INTO platform_schema_migrations(version,checksum) VALUES ($1,$2)`, migration.version, migration.checksum); err != nil {
			t.Fatal(err)
		}
	}
}

func assertPrincipalMigrationRolledBack(t *testing.T, database *sql.DB) {
	t.Helper()
	var principalColumns int
	if err := database.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('platform_positions','platform_portfolio_fills','platform_ledger_entries','platform_unrealized_pnl') AND column_name='principal_id'`).Scan(&principalColumns); err != nil {
		t.Fatal(err)
	}
	if principalColumns != 0 {
		t.Fatalf("failed migration persisted %d principal columns", principalColumns)
	}
	var version13, positions int
	if err := database.QueryRow(`SELECT count(*) FROM platform_schema_migrations WHERE version=13`).Scan(&version13); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM platform_positions`).Scan(&positions); err != nil {
		t.Fatal(err)
	}
	if version13 != 0 || positions != 1 {
		t.Fatalf("failed migration version13=%d positions=%d, want 0/1", version13, positions)
	}
}
