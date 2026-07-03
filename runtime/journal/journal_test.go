package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestMemoryJournalOpenIntents(t *testing.T) {
	ctx := context.Background()
	j := NewMemory()
	intent := testIntent("open")
	if err := j.AppendCommandIntent(ctx, intent); err != nil {
		t.Fatalf("append intent: %v", err)
	}
	open, err := j.OpenIntents(ctx)
	if err != nil {
		t.Fatalf("open intents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open=%d, want 1", len(open))
	}
	if err := j.AppendCommandResult(ctx, testResult(intent, AmbiguousOutcome)); err != nil {
		t.Fatalf("append ambiguous: %v", err)
	}
	open, err = j.OpenIntents(ctx)
	if err != nil {
		t.Fatalf("open intents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("ambiguous open=%d, want 1", len(open))
	}
	if err := j.AppendCommandResult(ctx, testResult(intent, "confirmed_accepted")); err != nil {
		t.Fatalf("append confirmed: %v", err)
	}
	open, err = j.OpenIntents(ctx)
	if err != nil {
		t.Fatalf("open intents: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("resolved open=%d, want 0", len(open))
	}
}

func TestFileJournalReplayOpenIntentsAndCursors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "exec.journal")
	j, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("open file journal: %v", err)
	}
	intent := testIntent("replay")
	if err := j.AppendCommandIntent(ctx, intent); err != nil {
		t.Fatalf("append intent: %v", err)
	}
	if err := j.AppendCommandResult(ctx, testResult(intent, AmbiguousOutcome)); err != nil {
		t.Fatalf("append result: %v", err)
	}
	cursor := ReconciliationCursor{
		RecordID:  NewRecordID("cursor", "orders"),
		Scope:     "FAKE",
		Stream:    "orders",
		Cursor:    "42",
		UpdatedAt: time.Now(),
	}
	if err := j.CommitReconciliationCursor(ctx, cursor); err != nil {
		t.Fatalf("commit cursor: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	replayed, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	defer replayed.Close()
	open, err := replayed.OpenIntents(ctx)
	if err != nil {
		t.Fatalf("open intents: %v", err)
	}
	if len(open) != 1 || open[0].ClientID != "replay" {
		t.Fatalf("open=%+v, want replay intent", open)
	}
	cursors, err := replayed.LoadReconciliationCursors(ctx)
	if err != nil {
		t.Fatalf("load cursors: %v", err)
	}
	if len(cursors) != 1 || cursors[0].Cursor != "42" {
		t.Fatalf("cursors=%+v, want cursor 42", cursors)
	}
	if replayed.UnsafeNoSync() {
		t.Fatal("file journal default should fsync")
	}
}

func TestFileJournalTruncatesPartialFinalRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "partial.journal")
	j, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := j.AppendCommandIntent(ctx, testIntent("partial")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := appendBytes(path, []byte{1, 2, 3}); err != nil {
		t.Fatalf("append partial: %v", err)
	}
	replayed, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	defer replayed.Close()
	if len(replayed.Warnings()) != 1 {
		t.Fatalf("warnings=%+v, want one truncation warning", replayed.Warnings())
	}
	open, err := replayed.OpenIntents(ctx)
	if err != nil {
		t.Fatalf("open intents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open=%d, want 1", len(open))
	}
}

func TestFileJournalRejectsNonFinalCorruption(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "corrupt.journal")
	j, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := j.AppendCommandIntent(ctx, testIntent("corrupt")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if _, err := OpenFile(path, FileOptions{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("replay err=%v, want ErrCorrupt", err)
	}
}

func TestJournalIgnoresDuplicateRecordIDs(t *testing.T) {
	ctx := context.Background()
	j := NewMemory()
	intent := testIntent("dup")
	if err := j.AppendCommandIntent(ctx, intent); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := j.AppendCommandIntent(ctx, intent); err != nil {
		t.Fatalf("append duplicate: %v", err)
	}
	if got := len(j.Records()); got != 1 {
		t.Fatalf("records=%d, want 1", got)
	}
}

func BenchmarkMemoryJournalAppendCommandIntent(b *testing.B) {
	ctx := context.Background()
	j := NewMemory()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		intent := testIntent(NewRecordID("bench", time.Now().String(), string(rune(i))))
		if err := j.AppendCommandIntent(ctx, intent); err != nil {
			b.Fatal(err)
		}
	}
}

func testIntent(clientID string) CommandIntent {
	return CommandIntent{
		RecordID:      NewRecordID("intent", clientID),
		CommandID:     NewRecordID("command", clientID),
		Type:          CommandSubmit,
		ClientID:      clientID,
		InstrumentID:  model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:          enums.SideBuy,
		OrderType:     enums.TypeLimit,
		TIF:           enums.TifGTC,
		Quantity:      decimal.NewFromInt(1),
		Price:         decimal.NewFromInt(100),
		SubmittedAt:   time.Now(),
		CorrelationID: NewRecordID("corr", clientID),
		Attempt:       1,
	}
}

func testResult(intent CommandIntent, outcome string) CommandResult {
	return CommandResult{
		RecordID:       NewRecordID("result", intent.RecordID, outcome, time.Now().String()),
		IntentRecordID: intent.RecordID,
		CommandID:      intent.CommandID,
		Type:           intent.Type,
		ClientID:       intent.ClientID,
		Outcome:        outcome,
		ResultAt:       time.Now(),
	}
}

func appendBytes(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
