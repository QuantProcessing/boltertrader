package journal

import (
	"context"
	"testing"
	"time"
)

func TestCursorRejectsMissingAppliedEventDependency(t *testing.T) {
	j := NewMemory()
	cursor := ReconciliationCursor{
		RecordID:              "cursor-missing-dependency",
		PassID:                "pass-missing-dependency",
		Scope:                 "T|acct|",
		Stream:                "orders",
		Cursor:                `{"last_venue_time":"2026-07-13T10:00:00Z"}`,
		UpdatedAt:             time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC),
		AppliedEventRecordIDs: []string{"missing-applied-event"},
	}

	if err := j.CommitReconciliationCursor(context.Background(), cursor); err == nil {
		t.Fatal("cursor commit with a missing applied-event dependency succeeded")
	}
	cursors, err := j.LoadReconciliationCursors(context.Background())
	if err != nil {
		t.Fatalf("load cursors: %v", err)
	}
	if len(cursors) != 0 {
		t.Fatalf("loaded invalid cursors=%+v, want none", cursors)
	}
}
