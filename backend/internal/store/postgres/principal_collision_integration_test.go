package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

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
