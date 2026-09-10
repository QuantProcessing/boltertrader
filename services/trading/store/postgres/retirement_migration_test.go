package postgres

import (
	"context"
	"testing"
	"time"
)

func TestRetirementMigrationPreservesFinancialHistoryAndManualRecovery(t *testing.T) {
	db, _ := openIntegrationDatabaseAtVersion(t, 6)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Microsecond)
	for _, row := range []struct {
		id, product string
		status      string
	}{
		{"unused", "arbitrage", "held"},
		{"linked", "arbitrage", "held"},
		{"consumed", "arbitrage", "consumed"},
		{"manual", "manual", "held"},
	} {
		_, err := db.Exec(`INSERT INTO reservations (reservation_id,principal_id,product_id,reservation_kind,netting_scope,scope_key,status,idempotency_key,payload_hash,created_at,updated_at)
 VALUES ($1,'p',$2,'open','{}',$1,$3,$1,'hash',$4,$4)`, row.id, row.product, row.status, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, product := range []string{"manual", "arbitrage"} {
		command := recoveryCommand(now)
		command.CommandID = product
		command.ProductID = product
		command.IdempotencyKey = product
		if product == "arbitrage" {
			id := "linked"
			command.ReservationID = &id
		}
		if _, _, err := NewCommandStore(db).Create(ctx, command); err != nil {
			t.Fatal(err)
		}
	}
	// Retired application state is disposable; shared venue evidence and money are not.
	_, err := db.Exec(`
 INSERT INTO arbitrage_executions (execution_id,principal_id,lineage_id,idempotency_key,action,request_hash,intent_hash,legs,state,created_at,expires_at,updated_at)
 VALUES ('e','p','l','k','open','hash','hash','[]','executing',now(),now(),now());
 INSERT INTO positions (principal_id,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,mark_observed_at,updated_at)
 VALUES ('p','arbitrage','l','a','BTC-USDT','USDT',1,100,100,0,0,now(),now());
 INSERT INTO portfolio_fills (principal_id,fill_id,fill_hash,command_id,product_id,lineage_id,account_id,instrument_id,side,quantity,price,fee,fee_currency,settlement_currency,realized_pnl,occurred_at)
 VALUES ('p','f','hash','arbitrage','arbitrage','l','a','BTC-USDT','buy',1,100,0.1,'USDT','USDT',0,now());
 INSERT INTO ledger_entries (principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at)
 VALUES ('p','e','arbitrage','l','a','BTC-USDT','fee',0.1,'USDT',now());
 INSERT INTO unrealized_pnl (principal_id,product_id,lineage_id,account_id,instrument_id,amount,currency,as_of)
 VALUES ('p','arbitrage','l','a','BTC-USDT',0,'USDT',now());
 `)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(table string) string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + table + ` t`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := map[string]string{}
	for _, table := range []string{"commands", "positions", "portfolio_fills", "ledger_entries", "unrealized_pnl"} {
		before[table] = snapshot(table)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	for table, want := range before {
		if got := snapshot(table); got != want {
			t.Fatalf("%s history changed: %s -> %s", table, want, got)
		}
	}
	var retiredTableExists bool
	if err := db.QueryRow(`SELECT to_regclass('arbitrage_executions') IS NOT NULL`).Scan(&retiredTableExists); err != nil || retiredTableExists {
		t.Fatalf("retired table exists=%v err=%v", retiredTableExists, err)
	}
	var taskCount int
	if err := db.QueryRow(`SELECT count(*) FROM reconciliation_tasks`).Scan(&taskCount); err != nil || taskCount != 1 {
		t.Fatalf("tasks=%d err=%v", taskCount, err)
	}
	var owner string
	if err := db.QueryRow(`SELECT command_id FROM reconciliation_tasks`).Scan(&owner); err != nil || owner != "manual" {
		t.Fatalf("remaining task=%s err=%v", owner, err)
	}
	for id, want := range map[string]string{"unused": "released", "linked": "held", "consumed": "consumed", "manual": "held"} {
		var status string
		err := db.QueryRow(`SELECT status FROM reservations WHERE reservation_id=$1`, id).Scan(&status)
		if err != nil || status != want {
			t.Fatalf("reservation %s=%s want=%s err=%v", id, status, want, err)
		}
	}
}
