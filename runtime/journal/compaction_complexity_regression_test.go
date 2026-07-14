package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestFileJournalProtectedGrowthDoesNotTriggerFullCompactionScans(t *testing.T) {
	const total = 256

	journal, err := OpenFileWithRetention(
		filepath.Join(t.TempDir(), "protected-growth.journal"),
		FileOptions{UnsafeNoSync: true},
		8,
	)
	if err != nil {
		t.Fatalf("open file journal: %v", err)
	}
	defer journal.Close()

	ctx := context.Background()
	for i := 0; i < total; i++ {
		intent := testIntent(fmt.Sprintf("protected-%03d", i))
		if err := journal.AppendCommandIntent(ctx, intent); err != nil {
			t.Fatalf("append protected intent %d: %v", i, err)
		}
	}

	journal.mu.Lock()
	scans := journal.st.compactionScans
	journal.mu.Unlock()
	if scans != 0 {
		t.Fatalf("full compaction scans=%d, want 0 while all %d records are recovery-protected", scans, total)
	}
}

func TestJournalOrdinaryHistoryUsesRetentionSlackBetweenFullScans(t *testing.T) {
	const (
		limit = 128
		total = 900
	)

	journal := NewMemoryWithRetention(limit)
	ctx := context.Background()
	for i := 0; i < total; i++ {
		if err := journal.AppendReport(ctx, ReportRecord{
			RecordID:   fmt.Sprintf("ordinary-%03d", i),
			ReportedAt: time.Unix(int64(i), 0),
		}); err != nil {
			t.Fatalf("append ordinary report %d: %v", i, err)
		}
	}

	journal.mu.Lock()
	scans := journal.st.compactionScans
	journal.mu.Unlock()
	threshold := limit + retentionSlack(limit)
	wantScans := 1 + (total-(threshold+1))/(retentionSlack(limit)+1)
	if scans != uint64(wantScans) {
		t.Fatalf("full compaction scans=%d, want %d for %d ordinary records", scans, wantScans, total)
	}
}

func TestJournalIncrementalUnprotectedCountTracksProtectionTransitions(t *testing.T) {
	journal := &MemoryJournal{st: newStateWithConflictLimit(1_000, 5_000)}
	ctx := context.Background()
	assertCount := func(step string) {
		t.Helper()
		journal.mu.Lock()
		defer journal.mu.Unlock()
		protected := journal.st.protectedRecordIDs()
		want := 0
		for _, record := range journal.st.records {
			if _, ok := protected[record.RecordID]; !ok {
				want++
			}
		}
		if got := journal.st.unprotected; got != want {
			t.Fatalf("%s: incremental unprotected=%d, classified=%d", step, got, want)
		}
	}

	intent := testIntent("protection-transitions")
	if err := journal.AppendCommandIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	assertCount("open intent")
	ambiguous := testResult(intent, AmbiguousOutcome)
	if err := journal.AppendCommandResult(ctx, ambiguous); err != nil {
		t.Fatal(err)
	}
	assertCount("first ambiguous result")
	secondAmbiguous := testResult(intent, AmbiguousOutcome)
	secondAmbiguous.Error = "different ambiguous observation"
	if err := journal.AppendCommandResult(ctx, secondAmbiguous); err != nil {
		t.Fatal(err)
	}
	assertCount("replacement ambiguous result")
	if err := journal.AppendCommandResult(ctx, testResult(intent, "confirmed_accepted")); err != nil {
		t.Fatal(err)
	}
	assertCount("definitive result")

	const blockingID = "blocking-transition"
	if err := journal.AppendReport(ctx, ReportRecord{
		RecordID: blockingID,
		Payload:  json.RawMessage(`{"blocking":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("blocking report")
	resolution, err := json.Marshal(findingResolutionMarkerPayload{
		Marker:          findingResolutionMarker,
		FindingRecordID: blockingID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendReport(ctx, ReportRecord{
		RecordID: "blocking-transition-resolution",
		Payload:  resolution,
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("blocking report resolution")

	for _, recordID := range []string{"event-a", "event-b", "event-c"} {
		if err := journal.AppendAppliedEvent(ctx, AppliedEventRecord{RecordID: recordID}); err != nil {
			t.Fatal(err)
		}
	}
	assertCount("uncommitted events")
	if err := journal.CommitReconciliationCursor(ctx, ReconciliationCursor{
		RecordID:              "cursor-one",
		Scope:                 "scope",
		Stream:                "orders",
		AppliedEventRecordIDs: []string{"event-a", "event-b", "event-b"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("first cursor")
	if err := journal.CommitReconciliationCursor(ctx, ReconciliationCursor{
		RecordID:              "cursor-two",
		Scope:                 "scope",
		Stream:                "orders",
		AppliedEventRecordIDs: []string{"event-b", "event-c"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("replacement cursor")
	if err := journal.CommitReconciliationCursor(ctx, ReconciliationCursor{
		RecordID:              "cursor-shared",
		Scope:                 "shared-scope",
		Stream:                "orders",
		AppliedEventRecordIDs: []string{"event-c"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("event shared by two cursors")
	if err := journal.CommitReconciliationCursor(ctx, ReconciliationCursor{
		RecordID:              "cursor-three",
		Scope:                 "scope",
		Stream:                "orders",
		AppliedEventRecordIDs: []string{"event-b"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("one shared cursor reference removed")
	if err := journal.CommitReconciliationCursor(ctx, ReconciliationCursor{
		RecordID: "cursor-shared-empty",
		Scope:    "shared-scope",
		Stream:   "orders",
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("last shared cursor reference removed")
	if err := journal.CommitReconciliationCursor(ctx, ReconciliationCursor{
		RecordID:              "cursor-other",
		Scope:                 "other-scope",
		Stream:                "orders",
		AppliedEventRecordIDs: []string{"event-a"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("committed event protected again")
	if err := journal.CommitReconciliationCursor(ctx, ReconciliationCursor{
		RecordID: "cursor-other-empty",
		Scope:    "other-scope",
		Stream:   "orders",
	}); err != nil {
		t.Fatal(err)
	}
	assertCount("reprotected event released")
}
