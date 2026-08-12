package postgres

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
)

func TestLifecycleAuditStoreRecordsAccountTransitionsAlongsideOrderAudits(t *testing.T) {
	pool := newTestPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	store := NewAccountLifecycleAuditStore(pool)

	occurred := time.Unix(1710000000, 0).UTC()
	record := lifecycle.AuditRecord{
		PrincipalID: "principal-a",
		Action:      "account.create",
		ResourceID:  "acct_0123456789ab",
		Outcome:     "accepted",
		OccurredAt:  occurred,
		RequestID:   "request-1",
	}
	if err := store.Append(ctx, record); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var (
		principal, action, resource, outcome, resourceType, requestID string
		at                                                            time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT principal_id, action, resource_id, outcome,
		       metadata ->> 'resource_type', metadata ->> 'request_id', occurred_at
		FROM audit_records
	`).Scan(&principal, &action, &resource, &outcome, &resourceType, &requestID, &at); err != nil {
		t.Fatalf("read audit record: %v", err)
	}
	if principal != record.PrincipalID || action != record.Action || resource != record.ResourceID || outcome != record.Outcome {
		t.Fatalf("audit row = %s/%s/%s/%s", principal, action, resource, outcome)
	}
	// The order path shares this table, so an account row has to say which kind
	// of resource it names rather than relying on the action prefix.
	if resourceType != "account" || requestID != record.RequestID {
		t.Fatalf("audit metadata = %s/%s", resourceType, requestID)
	}
	if !at.Equal(occurred) {
		t.Fatalf("occurred_at = %s, want %s", at, occurred)
	}

	// Every append is a distinct event: the trail must not collapse a replayed
	// transition onto the row it already wrote.
	if err := store.Append(ctx, record); err != nil {
		t.Fatalf("Append replay: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records`).Scan(&rows); err != nil {
		t.Fatalf("count audit records: %v", err)
	}
	if rows != 2 {
		t.Fatalf("audit rows = %d, want 2", rows)
	}
}

func TestLifecycleAuditStoreRejectsUnreadableAndSensitiveRecords(t *testing.T) {
	pool := newTestPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	store := NewAccountLifecycleAuditStore(pool)

	valid := lifecycle.AuditRecord{
		PrincipalID: "principal-a", Action: "account.rotate", ResourceID: "acct_0123456789ab",
		Outcome: "accepted", OccurredAt: time.Unix(1710000000, 0).UTC(), RequestID: "request-1",
	}
	tests := map[string]func(lifecycle.AuditRecord) lifecycle.AuditRecord{
		"missing principal": func(record lifecycle.AuditRecord) lifecycle.AuditRecord {
			record.PrincipalID = ""
			return record
		},
		"untrimmed action": func(record lifecycle.AuditRecord) lifecycle.AuditRecord {
			record.Action = " account.rotate"
			return record
		},
		"missing timestamp": func(record lifecycle.AuditRecord) lifecycle.AuditRecord {
			record.OccurredAt = time.Time{}
			return record
		},
		"credential material": func(record lifecycle.AuditRecord) lifecycle.AuditRecord {
			record.ResourceID = "secretRef:abc"
			return record
		},
		"authorization header": func(record lifecycle.AuditRecord) lifecycle.AuditRecord {
			record.RequestID = "Authorization=Bearer abc"
			return record
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := store.Append(ctx, mutate(valid)); err == nil {
				t.Fatal("Append accepted an invalid audit record")
			}
		})
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records`).Scan(&rows); err != nil {
		t.Fatalf("count audit records: %v", err)
	}
	if rows != 0 {
		t.Fatalf("audit rows = %d, want 0", rows)
	}
}
