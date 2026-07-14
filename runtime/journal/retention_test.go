package journal

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestMemoryRetentionBoundsHistoryAndPreservesRecoveryRecords(t *testing.T) {
	ctx := context.Background()
	j := NewMemoryWithRetention(2)

	open := testIntent("open-retained")
	if err := j.AppendCommandIntent(ctx, open); err != nil {
		t.Fatal(err)
	}
	ambiguous := testIntent("ambiguous-retained")
	if err := j.AppendCommandIntent(ctx, ambiguous); err != nil {
		t.Fatal(err)
	}
	ambiguousResult := testResult(ambiguous, AmbiguousOutcome)
	if err := j.AppendCommandResult(ctx, ambiguousResult); err != nil {
		t.Fatal(err)
	}

	appliedID := "applied-retained"
	if err := j.AppendAppliedEvent(ctx, AppliedEventRecord{
		RecordID:  appliedID,
		AppliedAt: time.Unix(2, 0),
		Payload:   json.RawMessage(`{"pass_id":"pass-retained"}`),
	}); err != nil {
		t.Fatal(err)
	}
	cursor := ReconciliationCursor{
		RecordID:              "cursor-retained",
		PassID:                "pass-retained",
		Scope:                 "FAKE|acct|",
		Stream:                "orders",
		Cursor:                "latest",
		UpdatedAt:             time.Unix(3, 0),
		AppliedEventRecordIDs: []string{appliedID},
	}
	if err := j.CommitReconciliationCursor(ctx, cursor); err != nil {
		t.Fatal(err)
	}

	blockingID := "blocking-retained"
	if err := j.AppendReport(ctx, ReportRecord{
		RecordID:   blockingID,
		ReportedAt: time.Unix(4, 0),
		Payload:    json.RawMessage(`{"Blocking":true,"Severity":"blocking","Code":"UNRESOLVED"}`),
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		id := NewRecordID("noise", time.Unix(int64(i), 0).String())
		if err := j.AppendReport(ctx, ReportRecord{RecordID: id, ReportedAt: time.Unix(int64(i), 0)}); err != nil {
			t.Fatal(err)
		}
	}

	// Six protected records plus the two-record diagnostic history window.
	if got := len(j.Records()); got > 8 {
		t.Fatalf("retained records=%d, want at most 8", got)
	}
	assertRecordIDs(t, j.Records(), open.RecordID, ambiguous.RecordID, ambiguousResult.RecordID, appliedID, cursor.RecordID, blockingID)
	intents, err := j.OpenIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 2 {
		t.Fatalf("open/ambiguous intents=%d, want 2", len(intents))
	}
	cursors, err := j.LoadReconciliationCursors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 1 || cursors[0].Cursor != "latest" {
		t.Fatalf("cursors=%+v, want latest", cursors)
	}
}

func TestFileReplayUsesBoundedMemoryWithoutDiscardingDiskRecoveryState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retained.journal")
	opts := FileOptions{}
	j, err := OpenFileWithRetention(path, opts, 2)
	if err != nil {
		t.Fatal(err)
	}
	open := testIntent("file-open-retained")
	if err := j.AppendCommandIntent(ctx, open); err != nil {
		t.Fatal(err)
	}
	appliedID := "file-applied-retained"
	if err := j.AppendAppliedEvent(ctx, AppliedEventRecord{
		RecordID: appliedID,
		Payload:  json.RawMessage(`{"pass_id":"file-pass"}`),
	}); err != nil {
		t.Fatal(err)
	}
	cursor := ReconciliationCursor{
		RecordID:              "file-cursor-retained",
		PassID:                "file-pass",
		Scope:                 "FAKE|acct|",
		Stream:                "orders",
		Cursor:                "file-latest",
		AppliedEventRecordIDs: []string{appliedID},
	}
	if err := j.CommitReconciliationCursor(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		id := NewRecordID("file-noise", time.Unix(int64(i), 0).String())
		if err := j.AppendReport(ctx, ReportRecord{RecordID: id, ReportedAt: time.Unix(int64(i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	replayed, err := OpenFileWithRetention(path, opts, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	if got := len(replayed.Records()); got > 5 {
		t.Fatalf("replayed in-memory records=%d, want 3 recovery records + 2 history", got)
	}
	assertRecordIDs(t, replayed.Records(), open.RecordID, appliedID, cursor.RecordID)
	intents, err := replayed.OpenIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].RecordID != open.RecordID {
		t.Fatalf("open intents=%+v, want retained file intent", intents)
	}
	cursors, err := replayed.LoadReconciliationCursors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 1 || cursors[0].Cursor != "file-latest" {
		t.Fatalf("replayed cursors=%+v, want latest cursor and dependency", cursors)
	}
}

func TestMemoryRetentionReplacesCursorAndItsAppliedEventDependency(t *testing.T) {
	ctx := context.Background()
	j := NewMemoryWithRetention(1)
	appendPass := func(passID, appliedID, cursorID, value string) {
		t.Helper()
		if err := j.AppendAppliedEvent(ctx, AppliedEventRecord{
			RecordID: appliedID,
			Payload:  json.RawMessage(`{"pass_id":"` + passID + `"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if err := j.CommitReconciliationCursor(ctx, ReconciliationCursor{
			RecordID:              cursorID,
			PassID:                model.ReconciliationID(passID),
			Scope:                 "FAKE|acct|",
			Stream:                "orders",
			Cursor:                value,
			AppliedEventRecordIDs: []string{appliedID},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendPass("old-pass", "old-applied", "old-cursor", "old")
	appendPass("new-pass", "new-applied", "new-cursor", "new")
	for i := 0; i < 4; i++ {
		if err := j.AppendReport(ctx, ReportRecord{RecordID: NewRecordID("cursor-noise", string(rune(i)))}); err != nil {
			t.Fatal(err)
		}
	}
	records := j.Records()
	assertRecordIDs(t, records, "new-applied", "new-cursor")
	for _, record := range records {
		if record.RecordID == "old-applied" || record.RecordID == "old-cursor" {
			t.Fatalf("superseded cursor dependency should become evictable: %+v", records)
		}
	}
}

func TestMemoryRetentionKeepsAppliedEventUntilItsCursorCommits(t *testing.T) {
	ctx := context.Background()
	j := NewMemoryWithRetention(1)
	if err := j.AppendAppliedEvent(ctx, AppliedEventRecord{
		RecordID: "uncommitted-applied",
		Payload:  json.RawMessage(`{"pass_id":"uncommitted-pass"}`),
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := j.AppendReport(ctx, ReportRecord{RecordID: NewRecordID("uncommitted-noise", string(rune(i)))}); err != nil {
			t.Fatal(err)
		}
	}
	assertRecordIDs(t, j.Records(), "uncommitted-applied")
}

func TestMemoryRetentionKeepsOpaqueAppliedEventUntilItsCursorCommits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload json.RawMessage
	}{
		{name: "empty payload"},
		{name: "opaque payload", payload: json.RawMessage(`{"kind":"position-update"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			j := NewMemoryWithRetention(1)
			appliedID := NewRecordID("opaque-applied", tc.name)
			if err := j.AppendAppliedEvent(ctx, AppliedEventRecord{
				RecordID: appliedID,
				Payload:  tc.payload,
			}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				if err := j.AppendReport(ctx, ReportRecord{
					RecordID: NewRecordID("opaque-applied-noise", tc.name, string(rune(i))),
				}); err != nil {
					t.Fatal(err)
				}
			}

			cursor := ReconciliationCursor{
				RecordID:              NewRecordID("opaque-applied-cursor", tc.name),
				Scope:                 "FAKE|acct|",
				Stream:                "orders",
				Cursor:                "latest",
				AppliedEventRecordIDs: []string{appliedID},
			}
			if err := j.CommitReconciliationCursor(ctx, cursor); err != nil {
				t.Fatalf("commit cursor after compaction: %v", err)
			}
			cursors, err := j.LoadReconciliationCursors(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(cursors) != 1 || cursors[0].RecordID != cursor.RecordID {
				t.Fatalf("cursors=%+v, want committed cursor %q", cursors, cursor.RecordID)
			}
			assertRecordIDs(t, j.Records(), appliedID, cursor.RecordID)
		})
	}
}

func assertRecordIDs(t *testing.T, records []Record, want ...string) {
	t.Helper()
	got := make(map[string]struct{}, len(records))
	for _, record := range records {
		got[record.RecordID] = struct{}{}
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("record %q was not retained", id)
		}
	}
}
