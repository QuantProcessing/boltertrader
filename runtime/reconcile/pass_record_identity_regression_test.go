package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/runtime/journal"
)

func TestBeginPassUsesDistinctRecordIDWhenAttemptPayloadChanges(t *testing.T) {
	ctx := context.Background()
	store := journal.NewMemory()
	state := NewJournalStateStore(store)
	scope := ScopeKey{Venue: "TEST", AccountID: "account"}
	first := PassHeader{
		PassID:        PassID(scope, time.Unix(100, 0)),
		Scope:         scope,
		StartedAt:     time.Unix(101, 0),
		StableEventAt: time.Unix(100, 0),
	}
	second := first
	second.StartedAt = first.StartedAt.Add(time.Second)

	if err := state.BeginPass(ctx, first); err != nil {
		t.Fatalf("first BeginPass: %v", err)
	}
	if err := state.BeginPass(ctx, second); err != nil {
		t.Fatalf("second BeginPass: %v", err)
	}

	var passRecords []journal.Record
	for _, record := range store.Records() {
		if record.Type == journal.RecordReconciliationPass {
			passRecords = append(passRecords, record)
		}
	}
	if len(passRecords) != 2 {
		t.Fatalf("pass records=%+v, want two distinct attempts", passRecords)
	}
	if passRecords[0].RecordID == passRecords[1].RecordID {
		t.Fatalf("pass attempts reused record id %q despite different payloads", passRecords[0].RecordID)
	}
}

func TestCommitCursorUsesDistinctRecordIDWhenPayloadChangesWithinPass(t *testing.T) {
	ctx := context.Background()
	store := journal.NewMemory()
	state := NewJournalStateStore(store)
	scope := ScopeKey{Venue: "TEST", AccountID: "account"}
	first := Cursor{
		Scope:              scope,
		Stream:             StreamFills,
		LastSuccessfulPass: PassID(scope, time.Unix(100, 0)),
		LastVenueTime:      time.Unix(100, 0),
		LastLocalApplyTime: time.Unix(101, 0),
	}
	second := first
	second.AppliedEventRecordIDs = []string{"applied-fill"}

	if err := store.AppendAppliedEvent(ctx, journal.AppliedEventRecord{
		RecordID:  "applied-fill",
		AppliedAt: time.Unix(101, 0),
	}); err != nil {
		t.Fatalf("append cursor dependency: %v", err)
	}
	if err := state.CommitCursor(ctx, first); err != nil {
		t.Fatalf("first CommitCursor: %v", err)
	}
	if err := state.CommitCursor(ctx, second); err != nil {
		t.Fatalf("second CommitCursor: %v", err)
	}

	var cursorRecords []journal.Record
	for _, record := range store.Records() {
		if record.Type == journal.RecordReconciliationCursor {
			cursorRecords = append(cursorRecords, record)
		}
	}
	if len(cursorRecords) != 2 {
		t.Fatalf("cursor records=%+v, want two distinct payload versions", cursorRecords)
	}
	if cursorRecords[0].RecordID == cursorRecords[1].RecordID {
		t.Fatalf("cursor updates reused record id %q despite different payloads", cursorRecords[0].RecordID)
	}
}
