package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestWorkCompletionFinalizesAuthorityBeforeMarkingWorkCompleted(t *testing.T) {
	source, err := os.ReadFile("work.go")
	if err != nil {
		t.Fatal(err)
	}
	implementation := string(source)
	start := strings.Index(implementation, "func (store *WorkStore) terminalLease")
	end := strings.Index(implementation, "func (store *WorkStore) updateLease")
	if start < 0 || end <= start {
		t.Fatal("work terminal transaction boundary is missing")
	}
	terminal := implementation[start:end]
	lock := strings.Index(terminal, "FOR UPDATE")
	finalize := strings.Index(terminal, "finalizeWorkAuthority")
	markCompleted := strings.Index(terminal, "UPDATE platform_work_items SET")
	commit := strings.Index(terminal, "tx.Commit()")
	if lock < 0 || finalize < lock || markCompleted < finalize || commit < markCompleted {
		t.Fatalf("completion order lock=%d finalize=%d update=%d commit=%d", lock, finalize, markCompleted, commit)
	}
	for _, required := range []string{
		"platform_work_intents",
		"state NOT IN ('filled','canceled')",
		"platform_reconciliation_tasks",
		"platform_funding_executions",
		"state NOT IN ('hedged','closed')",
		"UPDATE platform_reservations",
		"r.status IN ('held','consumed')",
	} {
		if !strings.Contains(terminal, required) {
			t.Errorf("completion authority transaction missing %q", required)
		}
	}
}
