package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestFundingWorkMigrationSupportsReplayRetryAndMonotonicLeases(t *testing.T) {
	source, err := os.ReadFile("0005_funding_work_store.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(source)
	for _, required := range []string{
		"retry_wait", "manual_intervention", "attempt integer", "next_attempt_at timestamptz",
		"lease_generation integer", "platform_work_operations", "request_hash text", "response jsonb",
		"registered_at timestamptz", "heartbeat_expires_at timestamptz",
		"command_id text NOT NULL REFERENCES platform_commands(command_id)",
		"CREATE TABLE platform_funding_command_plans", "PRIMARY KEY (principal_id, mode, plan_key)",
		"CREATE TABLE platform_funding_executions", "CREATE TABLE platform_funding_execution_transitions", "UNIQUE (execution_id, version)",
		"PRIMARY KEY (operation_kind, idempotency_key)",
		"UNIQUE (worker_id, mode)", "worker_mode text NOT NULL", "FOREIGN KEY (worker_id, worker_mode)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
}

func TestWorkFactCursorBatchMigrationRemovesSingleFactConstraint(t *testing.T) {
	source, err := os.ReadFile("0009_funding_work_integrity.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(source)
	for _, required := range []string{"DROP CONSTRAINT platform_work_facts_cursor_key", "platform_work_facts_cursor_batch", "cursor, fact_id"} {
		if !strings.Contains(sql, required) {
			t.Errorf("cursor batch migration missing %q", required)
		}
	}
}
