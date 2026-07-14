package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordIDIsIdempotentOnlyForIdenticalTypeAndPayload(t *testing.T) {
	for _, factory := range journalStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.open(t)
			defer store.Close()

			ctx := context.Background()
			original := ReportRecord{
				RecordID:   "shared-record-id",
				ReportedAt: time.Unix(100, 0),
				Payload:    json.RawMessage(`{"value":"original"}`),
			}
			if err := store.AppendReport(ctx, original); err != nil {
				t.Fatalf("append original report: %v", err)
			}
			if err := store.AppendReport(ctx, original); err != nil {
				t.Fatalf("append identical report retry: %v", err)
			}

			changedPayload := original
			changedPayload.Payload = json.RawMessage(`{"value":"changed"}`)
			if err := store.AppendReport(ctx, changedPayload); !errors.Is(err, ErrRecordIDConflict) {
				t.Fatalf("changed payload err=%v, want ErrRecordIDConflict", err)
			}
			if err := store.AppendAppliedEvent(ctx, AppliedEventRecord{
				RecordID:  original.RecordID,
				AppliedAt: original.ReportedAt,
			}); !errors.Is(err, ErrRecordIDConflict) {
				t.Fatalf("changed record type err=%v, want ErrRecordIDConflict", err)
			}
			if got := len(store.Records()); got != 1 {
				t.Fatalf("records=%d, want only the original record", got)
			}
		})
	}
}

func TestCommandResultTransitionsAreMonotonic(t *testing.T) {
	for _, factory := range journalStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.open(t)
			defer store.Close()

			ctx := context.Background()
			intent := fixedIntent("monotonic-result")
			if err := store.AppendCommandIntent(ctx, intent); err != nil {
				t.Fatalf("append intent: %v", err)
			}
			ambiguous := fixedResult(intent, "result-ambiguous", AmbiguousOutcome, time.Unix(101, 0))
			if err := store.AppendCommandResult(ctx, ambiguous); err != nil {
				t.Fatalf("append ambiguous result: %v", err)
			}
			ambiguousUpdate := ambiguous
			ambiguousUpdate.RecordID = "result-ambiguous-update"
			ambiguousUpdate.Error = "second recovery probe remained ambiguous"
			ambiguousUpdate.ResultAt = time.Unix(102, 0)
			if err := store.AppendCommandResult(ctx, ambiguousUpdate); err != nil {
				t.Fatalf("append changed ambiguous result: %v", err)
			}
			accepted := fixedResult(intent, "result-accepted", "confirmed_accepted", time.Unix(102, 0))
			accepted.VenueOrderID = "venue-accepted"
			if err := store.AppendCommandResult(ctx, accepted); err != nil {
				t.Fatalf("append definitive result: %v", err)
			}

			retry := accepted
			retry.RecordID = "result-accepted-retry"
			retry.ResultAt = time.Unix(103, 0)
			if err := store.AppendCommandResult(ctx, retry); err != nil {
				t.Fatalf("append same definitive semantic retry: %v", err)
			}
			if got := len(store.Records()); got != 4 {
				t.Fatalf("records=%d, want intent + two distinct ambiguous results + one definitive result", got)
			}

			lateAmbiguous := ambiguous
			lateAmbiguous.RecordID = "result-late-ambiguous"
			lateAmbiguous.ResultAt = time.Unix(104, 0)
			if err := store.AppendCommandResult(ctx, lateAmbiguous); !errors.Is(err, ErrCommandResultConflict) {
				t.Fatalf("definitive to ambiguous err=%v, want ErrCommandResultConflict", err)
			}
			rejected := accepted
			rejected.RecordID = "result-rejected"
			rejected.Outcome = "definitive_venue_rejected"
			rejected.ResultAt = time.Unix(105, 0)
			if err := store.AppendCommandResult(ctx, rejected); !errors.Is(err, ErrCommandResultConflict) {
				t.Fatalf("different definitive err=%v, want ErrCommandResultConflict", err)
			}
			for _, test := range []struct {
				name   string
				mutate func(*CommandResult)
			}{
				{name: "command id", mutate: func(result *CommandResult) { result.CommandID = "different-command" }},
				{name: "command type", mutate: func(result *CommandResult) { result.Type = CommandCancel }},
				{name: "client id", mutate: func(result *CommandResult) { result.ClientID = "different-client" }},
				{name: "venue order id", mutate: func(result *CommandResult) { result.VenueOrderID = "different-venue-order" }},
				{name: "error", mutate: func(result *CommandResult) { result.Error = "different-result-error" }},
			} {
				t.Run(test.name, func(t *testing.T) {
					conflict := accepted
					conflict.RecordID = "result-conflict-" + test.name
					conflict.ResultAt = time.Unix(106, 0)
					test.mutate(&conflict)
					if err := store.AppendCommandResult(ctx, conflict); !errors.Is(err, ErrCommandResultConflict) {
						t.Fatalf("same outcome with changed semantics err=%v, want ErrCommandResultConflict", err)
					}
				})
			}
			if got := len(store.Records()); got != 4 {
				t.Fatalf("records=%d after conflicts, want 4", got)
			}
			open, err := store.OpenIntents(ctx)
			if err != nil {
				t.Fatalf("open intents: %v", err)
			}
			if len(open) != 0 {
				t.Fatalf("open intents=%+v, definitive result must remain final", open)
			}
		})
	}
}

func TestCommandResultMustMatchItsIntentIdentity(t *testing.T) {
	for _, factory := range journalStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.open(t)
			defer store.Close()

			ctx := context.Background()
			intent := fixedIntent("result-intent-identity")
			if err := store.AppendCommandIntent(ctx, intent); err != nil {
				t.Fatalf("append intent: %v", err)
			}
			base := fixedResult(intent, "result-intent-identity-first", "confirmed_accepted", time.Unix(110, 0))
			for _, test := range []struct {
				name   string
				mutate func(*CommandResult)
			}{
				{name: "command id", mutate: func(result *CommandResult) { result.CommandID = "wrong-command" }},
				{name: "command type", mutate: func(result *CommandResult) { result.Type = CommandCancel }},
				{name: "client id", mutate: func(result *CommandResult) { result.ClientID = "wrong-client" }},
			} {
				t.Run(test.name, func(t *testing.T) {
					result := base
					result.RecordID += "-" + test.name
					test.mutate(&result)
					if err := store.AppendCommandResult(ctx, result); !errors.Is(err, ErrCommandResultConflict) {
						t.Fatalf("mismatched first result err=%v, want ErrCommandResultConflict", err)
					}
				})
			}

			unknown := base
			unknown.RecordID = "result-unknown-intent"
			unknown.IntentRecordID = "missing-intent"
			if err := store.AppendCommandResult(ctx, unknown); !errors.Is(err, ErrCommandResultConflict) {
				t.Fatalf("unknown-intent result err=%v, want ErrCommandResultConflict", err)
			}
			open, err := store.OpenIntents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(open) != 1 || open[0].RecordID != intent.RecordID {
				t.Fatalf("open intents=%+v, mismatched results must not resolve the intent", open)
			}
		})
	}
}

func TestAmbiguousResultTransitionMustPreserveCommandIdentity(t *testing.T) {
	for _, factory := range journalStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.open(t)
			defer store.Close()

			ctx := context.Background()
			intent := fixedIntent("ambiguous-result-identity")
			if err := store.AppendCommandIntent(ctx, intent); err != nil {
				t.Fatal(err)
			}
			ambiguous := fixedResult(intent, "ambiguous-result-identity-first", AmbiguousOutcome, time.Unix(120, 0))
			if err := store.AppendCommandResult(ctx, ambiguous); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name   string
				mutate func(*CommandResult)
			}{
				{name: "command id", mutate: func(result *CommandResult) { result.CommandID = "wrong-command" }},
				{name: "command type", mutate: func(result *CommandResult) { result.Type = CommandCancel }},
				{name: "client id", mutate: func(result *CommandResult) { result.ClientID = "wrong-client" }},
			} {
				t.Run(test.name, func(t *testing.T) {
					definitive := fixedResult(intent, "ambiguous-transition-"+test.name, "confirmed_accepted", time.Unix(121, 0))
					test.mutate(&definitive)
					if err := store.AppendCommandResult(ctx, definitive); !errors.Is(err, ErrCommandResultConflict) {
						t.Fatalf("identity-changing transition err=%v, want ErrCommandResultConflict", err)
					}
				})
			}
		})
	}
}

func TestFileReplayEnforcesRecordIDContentIdentity(t *testing.T) {
	report := ReportRecord{
		RecordID:   "replay-shared-record-id",
		ReportedAt: time.Unix(200, 0),
		Payload:    json.RawMessage(`{"value":"original"}`),
	}
	original := rawJournalRecord(t, 1, RecordReport, report.RecordID, report)

	t.Run("identical retry", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "identical.journal")
		duplicate := original
		duplicate.Sequence = 2
		duplicate.Timestamp = time.Unix(201, 0)
		writeRawJournal(t, path, original, duplicate)

		store, err := OpenFile(path, FileOptions{})
		if err != nil {
			t.Fatalf("replay identical records: %v", err)
		}
		defer store.Close()
		if got := len(store.Records()); got != 1 {
			t.Fatalf("records=%d, want one idempotent record", got)
		}
	})

	t.Run("changed payload", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "payload-conflict.journal")
		changed := report
		changed.Payload = json.RawMessage(`{"value":"changed"}`)
		writeRawJournal(t, path, original, rawJournalRecord(t, 2, RecordReport, report.RecordID, changed))

		if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrRecordIDConflict) {
			t.Fatalf("replay err=%v, want ErrRecordIDConflict", err)
		}
	})

	t.Run("changed type", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "type-conflict.journal")
		applied := AppliedEventRecord{RecordID: report.RecordID, AppliedAt: time.Unix(201, 0)}
		writeRawJournal(t, path, original, rawJournalRecord(t, 2, RecordAppliedEvent, report.RecordID, applied))

		if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrRecordIDConflict) {
			t.Fatalf("replay err=%v, want ErrRecordIDConflict", err)
		}
	})
}

func TestFileReplayEnforcesCommandResultMonotonicity(t *testing.T) {
	intent := fixedIntent("replay-monotonic-result")
	intentRecord := rawJournalRecord(t, 1, RecordCommandIntent, intent.RecordID, intent)
	accepted := fixedResult(intent, "replay-result-accepted", "confirmed_accepted", time.Unix(301, 0))
	acceptedRecord := rawJournalRecord(t, 2, RecordCommandResult, accepted.RecordID, accepted)

	t.Run("same definitive retry is collapsed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "same-definitive.journal")
		retry := accepted
		retry.RecordID = "replay-result-accepted-retry"
		retry.ResultAt = time.Unix(302, 0)
		writeRawJournal(t, path, intentRecord, acceptedRecord, rawJournalRecord(t, 3, RecordCommandResult, retry.RecordID, retry))

		store, err := OpenFile(path, FileOptions{})
		if err != nil {
			t.Fatalf("replay same definitive retry: %v", err)
		}
		defer store.Close()
		if got := len(store.Records()); got != 2 {
			t.Fatalf("records=%d, want intent + one definitive result", got)
		}
	})

	t.Run("definitive cannot become ambiguous", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "late-ambiguous.journal")
		late := fixedResult(intent, "replay-result-late-ambiguous", AmbiguousOutcome, time.Unix(302, 0))
		writeRawJournal(t, path, intentRecord, acceptedRecord, rawJournalRecord(t, 3, RecordCommandResult, late.RecordID, late))

		if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrCommandResultConflict) {
			t.Fatalf("replay err=%v, want ErrCommandResultConflict", err)
		}
	})

	t.Run("definitive outcome cannot change", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "different-definitive.journal")
		rejected := fixedResult(intent, "replay-result-rejected", "definitive_venue_rejected", time.Unix(302, 0))
		writeRawJournal(t, path, intentRecord, acceptedRecord, rawJournalRecord(t, 3, RecordCommandResult, rejected.RecordID, rejected))

		if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrCommandResultConflict) {
			t.Fatalf("replay err=%v, want ErrCommandResultConflict", err)
		}
	})

	t.Run("definitive semantics cannot change", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "different-definitive-semantics.journal")
		changed := accepted
		changed.RecordID = "replay-result-different-venue"
		changed.VenueOrderID = "different-venue-order"
		changed.ResultAt = time.Unix(302, 0)
		writeRawJournal(t, path, intentRecord, acceptedRecord, rawJournalRecord(t, 3, RecordCommandResult, changed.RecordID, changed))

		if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrCommandResultConflict) {
			t.Fatalf("replay err=%v, want ErrCommandResultConflict", err)
		}
	})
}

func TestFileReplayRejectsResultWithoutMatchingIntent(t *testing.T) {
	intent := fixedIntent("replay-result-intent-identity")
	intentRecord := rawJournalRecord(t, 1, RecordCommandIntent, intent.RecordID, intent)

	t.Run("wrong first result identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wrong-result-identity.journal")
		result := fixedResult(intent, "wrong-result-identity", "confirmed_accepted", time.Unix(310, 0))
		result.CommandID = "wrong-command"
		writeRawJournal(t, path, intentRecord, rawJournalRecord(t, 2, RecordCommandResult, result.RecordID, result))
		if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrCommandResultConflict) {
			t.Fatalf("replay err=%v, want ErrCommandResultConflict", err)
		}
	})

	t.Run("unknown intent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "unknown-result-intent.journal")
		result := fixedResult(intent, "unknown-result-intent", "confirmed_accepted", time.Unix(311, 0))
		result.IntentRecordID = "missing-intent"
		writeRawJournal(t, path, rawJournalRecord(t, 1, RecordCommandResult, result.RecordID, result))
		if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrCommandResultConflict) {
			t.Fatalf("replay err=%v, want ErrCommandResultConflict", err)
		}
	})
}

func TestCompactionPreservesConflictGuards(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		store := NewMemoryWithRetention(1)
		primeCompactedConflictState(t, store)
		assertCompactedConflictGuards(t, store)
	})

	t.Run("file replay", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "compacted-conflicts.journal")
		store, err := OpenFileWithRetention(path, FileOptions{}, 1)
		if err != nil {
			t.Fatalf("open file journal: %v", err)
		}
		primeCompactedConflictState(t, store)
		if err := store.Close(); err != nil {
			t.Fatalf("close file journal: %v", err)
		}

		store, err = OpenFileWithRetention(path, FileOptions{}, 1)
		if err != nil {
			t.Fatalf("reopen file journal: %v", err)
		}
		defer store.Close()
		assertCompactedConflictGuards(t, store)
	})
}

func primeCompactedConflictState(t *testing.T, store DurableStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.AppendReport(ctx, ReportRecord{
		RecordID:   "compacted-record-id",
		ReportedAt: time.Unix(400, 0),
		Payload:    json.RawMessage(`{"value":"original"}`),
	}); err != nil {
		t.Fatalf("append original report: %v", err)
	}
	intent := fixedIntent("compacted-result")
	if err := store.AppendCommandIntent(ctx, intent); err != nil {
		t.Fatalf("append intent: %v", err)
	}
	if err := store.AppendCommandResult(ctx, fixedResult(
		intent,
		"compacted-definitive-result",
		"confirmed_accepted",
		time.Unix(401, 0),
	)); err != nil {
		t.Fatalf("append definitive result: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := store.AppendReport(ctx, ReportRecord{
			RecordID:   NewRecordID("compaction-noise", time.Unix(int64(i), 0).String()),
			ReportedAt: time.Unix(int64(410+i), 0),
		}); err != nil {
			t.Fatalf("append compaction noise %d: %v", i, err)
		}
	}
}

func assertCompactedConflictGuards(t *testing.T, store DurableStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.AppendReport(ctx, ReportRecord{
		RecordID:   "compacted-record-id",
		ReportedAt: time.Unix(400, 0),
		Payload:    json.RawMessage(`{"value":"changed"}`),
	}); !errors.Is(err, ErrRecordIDConflict) {
		t.Fatalf("compacted record ID reuse err=%v, want ErrRecordIDConflict", err)
	}
	intent := fixedIntent("compacted-result")
	late := fixedResult(intent, "compacted-late-ambiguous", AmbiguousOutcome, time.Unix(420, 0))
	if err := store.AppendCommandResult(ctx, late); !errors.Is(err, ErrCommandResultConflict) {
		t.Fatalf("compacted definitive to ambiguous err=%v, want ErrCommandResultConflict", err)
	}
}

func TestConflictIndexCapacityFailsClosedWithoutForgettingKnownIdentity(t *testing.T) {
	ctx := context.Background()
	store := &MemoryJournal{st: newStateWithConflictLimit(1, 3)}
	reports := []ReportRecord{
		{RecordID: "capacity-one", ReportedAt: time.Unix(501, 0)},
		{RecordID: "capacity-two", ReportedAt: time.Unix(502, 0)},
		{RecordID: "capacity-three", ReportedAt: time.Unix(503, 0)},
	}
	for _, report := range reports {
		if err := store.AppendReport(ctx, report); err != nil {
			t.Fatalf("append %s: %v", report.RecordID, err)
		}
	}
	if err := store.AppendReport(ctx, reports[0]); err != nil {
		t.Fatalf("exact retry at capacity: %v", err)
	}
	changed := reports[0]
	changed.ReportedAt = time.Unix(599, 0)
	if err := store.AppendReport(ctx, changed); !errors.Is(err, ErrRecordIDConflict) {
		t.Fatalf("known conflict at capacity err=%v, want ErrRecordIDConflict", err)
	}
	if err := store.AppendReport(ctx, ReportRecord{RecordID: "capacity-four"}); !errors.Is(err, ErrConflictIndexCapacity) {
		t.Fatalf("new identity at capacity err=%v, want ErrConflictIndexCapacity", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := len(store.st.recordIdentity); got != 3 {
		t.Fatalf("record identity index=%d, want hard cap 3", got)
	}
}

func TestConflictIndexCapacityPreservesResultConflictPrecedence(t *testing.T) {
	ctx := context.Background()
	store := &MemoryJournal{st: newStateWithConflictLimit(1, 3)}
	intent := fixedIntent("capacity-result")
	accepted := fixedResult(intent, "capacity-result-accepted", "confirmed_accepted", time.Unix(601, 0))
	if err := store.AppendCommandIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCommandResult(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendReport(ctx, ReportRecord{RecordID: "capacity-result-noise"}); err != nil {
		t.Fatal(err)
	}

	retry := accepted
	retry.RecordID = "capacity-result-semantic-retry"
	retry.ResultAt = time.Unix(602, 0)
	if err := store.AppendCommandResult(ctx, retry); err != nil {
		t.Fatalf("same semantic retry at capacity: %v", err)
	}
	late := accepted
	late.RecordID = "capacity-result-late-ambiguous"
	late.Outcome = AmbiguousOutcome
	late.ResultAt = time.Unix(603, 0)
	if err := store.AppendCommandResult(ctx, late); !errors.Is(err, ErrCommandResultConflict) {
		t.Fatalf("known result conflict at capacity err=%v, want ErrCommandResultConflict", err)
	}
}

func TestReplayConflictIndexCapacityFailsClosedWithoutRewritingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.journal")
	records := make([]Record, 0, 4)
	for i := 0; i < 4; i++ {
		report := ReportRecord{RecordID: NewRecordID("replay-capacity", time.Unix(int64(i), 0).String())}
		records = append(records, rawJournalRecord(t, uint64(i+1), RecordReport, report.RecordID, report))
	}
	writeRawJournal(t, path, records...)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayFile(path, newStateWithConflictLimit(defaultMemoryRetentionLimit, 3)); !errors.Is(err, ErrConflictIndexCapacity) {
		t.Fatalf("replay err=%v, want ErrConflictIndexCapacity", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("capacity failure rewrote journal: before=%d after=%d", before.Size(), after.Size())
	}
}

func TestReplaySemanticRetriesRespectConflictIndexCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "semantic-retry-capacity.journal")
	intent := fixedIntent("semantic-retry-capacity")
	accepted := fixedResult(intent, "semantic-retry-accepted", "confirmed_accepted", time.Unix(701, 0))
	retryOne := accepted
	retryOne.RecordID = "semantic-retry-one"
	retryOne.ResultAt = time.Unix(702, 0)
	retryTwo := accepted
	retryTwo.RecordID = "semantic-retry-two"
	retryTwo.ResultAt = time.Unix(703, 0)
	writeRawJournal(t, path,
		rawJournalRecord(t, 1, RecordCommandIntent, intent.RecordID, intent),
		rawJournalRecord(t, 2, RecordCommandResult, accepted.RecordID, accepted),
		rawJournalRecord(t, 3, RecordCommandResult, retryOne.RecordID, retryOne),
		rawJournalRecord(t, 4, RecordCommandResult, retryTwo.RecordID, retryTwo),
	)
	if err := replayFile(path, newStateWithConflictLimit(defaultMemoryRetentionLimit, 3)); !errors.Is(err, ErrConflictIndexCapacity) {
		t.Fatalf("semantic retry replay err=%v, want ErrConflictIndexCapacity", err)
	}
}

func TestReplayWarningsAreBoundedAndDoNotRewriteJournal(t *testing.T) {
	const warningLimit = 1_024
	path := filepath.Join(t.TempDir(), "bounded-warnings.journal")
	records := make([]Record, 0, warningLimit+8)
	for i := 0; i < warningLimit+8; i++ {
		cursor := ReconciliationCursor{
			RecordID:              NewRecordID("missing-dependency-cursor", time.Unix(int64(i), 0).String()),
			Scope:                 "FAKE|acct|",
			Stream:                "fills",
			Cursor:                time.Unix(int64(i), 0).String(),
			AppliedEventRecordIDs: []string{NewRecordID("missing-applied-event", time.Unix(int64(i), 0).String())},
		}
		records = append(records, rawJournalRecord(t, uint64(i+1), RecordReconciliationCursor, cursor.RecordID, cursor))
	}
	writeRawJournal(t, path, records...)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileWithRetention(path, FileOptions{}, 1)
	if err != nil {
		t.Fatalf("replay missing-dependency cursors: %v", err)
	}
	defer store.Close()
	warnings := store.Warnings()
	if len(warnings) > warningLimit {
		t.Fatalf("replay warnings=%d, want bounded at %d", len(warnings), warningLimit)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[len(warnings)-1].Reason, "omitted") {
		t.Fatalf("last replay warning=%+v, want omitted-warning summary", warnings)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("bounded warning replay rewrote the journal")
	}
}

type journalStoreFactory struct {
	name string
	open func(*testing.T) DurableStore
}

func journalStoreFactories() []journalStoreFactory {
	return []journalStoreFactory{
		{name: "memory", open: func(*testing.T) DurableStore { return NewMemory() }},
		{name: "file", open: func(t *testing.T) DurableStore {
			t.Helper()
			store, err := OpenFile(filepath.Join(t.TempDir(), "store.journal"), FileOptions{})
			if err != nil {
				t.Fatalf("open file journal: %v", err)
			}
			return store
		}},
	}
}

func fixedIntent(clientID string) CommandIntent {
	intent := testIntent(clientID)
	intent.RecordID = "intent-" + clientID
	intent.CommandID = "command-" + clientID
	intent.CorrelationID = "correlation-" + clientID
	intent.SubmittedAt = time.Unix(100, 0)
	return intent
}

func fixedResult(intent CommandIntent, recordID, outcome string, at time.Time) CommandResult {
	return CommandResult{
		RecordID:       recordID,
		IntentRecordID: intent.RecordID,
		CommandID:      intent.CommandID,
		Type:           intent.Type,
		ClientID:       intent.ClientID,
		Outcome:        outcome,
		ResultAt:       at,
	}
}

func rawJournalRecord(t *testing.T, sequence uint64, recordType RecordType, recordID string, payload any) Record {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return Record{
		Sequence:  sequence,
		Type:      recordType,
		RecordID:  recordID,
		Timestamp: time.Unix(int64(sequence), 0),
		Payload:   body,
	}
}

func writeRawJournal(t *testing.T, path string, records ...Record) {
	t.Helper()
	var data []byte
	for _, record := range records {
		frame, err := marshalFrame(record)
		if err != nil {
			t.Fatalf("marshal frame: %v", err)
		}
		data = append(data, frame...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write raw journal: %v", err)
	}
}
