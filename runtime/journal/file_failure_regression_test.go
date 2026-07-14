package journal

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFileJournalPartialWritePoisonsAppenderAndReplayTruncatesTail(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "partial-write.journal")
	j, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	persisted := testIntent("persisted-before-partial-write")
	if err := j.AppendCommandIntent(ctx, persisted); err != nil {
		t.Fatalf("append persisted intent: %v", err)
	}

	writeFailure := errors.New("injected partial write")
	fault := &faultJournalFile{file: j.file}
	fault.write = func(p []byte) (int, error) {
		written, err := fault.file.Write(p[:len(p)/2])
		if err != nil {
			return written, err
		}
		return written, writeFailure
	}
	j.file = fault

	err = j.AppendCommandIntent(ctx, testIntent("partial-write-fails"))
	if !errors.Is(err, writeFailure) {
		t.Fatalf("partial append err=%v, want injected write failure", err)
	}
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial append err=%v, want io.ErrShortWrite", err)
	}
	if fault.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1 after partial write", fault.closeCalls)
	}
	if err := j.AppendCommandIntent(ctx, testIntent("must-not-follow-partial")); err == nil {
		t.Fatal("append after partial write succeeded")
	}
	if fault.writeCalls != 1 {
		t.Fatalf("write calls=%d, want no write after poison", fault.writeCalls)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close poisoned journal: %v", err)
	}

	replayed, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("reopen after partial final frame: %v", err)
	}
	defer replayed.Close()
	assertOnlyIntent(t, replayed, persisted.RecordID)
	if got := len(replayed.Warnings()); got != 1 {
		t.Fatalf("warnings=%d, want one truncated-tail warning", got)
	}
}

func TestFileJournalNilErrorShortWriteReturnsErrShortWriteAndPoisons(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "short-write.journal")
	j, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	persisted := testIntent("persisted-before-short-write")
	if err := j.AppendCommandIntent(ctx, persisted); err != nil {
		t.Fatalf("append persisted intent: %v", err)
	}

	fault := &faultJournalFile{file: j.file}
	fault.write = func(p []byte) (int, error) {
		return fault.file.Write(p[:len(p)/2])
	}
	j.file = fault

	if err := j.AppendCommandIntent(ctx, testIntent("short-write-fails")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short append err=%v, want io.ErrShortWrite", err)
	}
	if fault.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1 after short write", fault.closeCalls)
	}
	if err := j.AppendCommandIntent(ctx, testIntent("must-not-follow-short")); err == nil {
		t.Fatal("append after short write succeeded")
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close poisoned journal: %v", err)
	}

	replayed, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("reopen after short final frame: %v", err)
	}
	defer replayed.Close()
	assertOnlyIntent(t, replayed, persisted.RecordID)
}

func TestFileJournalSyncFailurePoisonsAppenderWithoutCorruptingReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync-failure.journal")
	j, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	persisted := testIntent("persisted-before-sync-failure")
	if err := j.AppendCommandIntent(ctx, persisted); err != nil {
		t.Fatalf("append persisted intent: %v", err)
	}

	syncFailure := errors.New("injected sync failure")
	fault := &faultJournalFile{file: j.file, syncErr: syncFailure}
	j.file = fault
	uncertain := testIntent("fully-written-before-sync-failure")
	if err := j.AppendCommandIntent(ctx, uncertain); !errors.Is(err, syncFailure) {
		t.Fatalf("sync-failed append err=%v, want injected failure", err)
	}
	if fault.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1 after sync failure", fault.closeCalls)
	}
	if err := j.AppendCommandIntent(ctx, testIntent("must-not-follow-sync-failure")); err == nil {
		t.Fatal("append after sync failure succeeded")
	}
	if fault.writeCalls != 1 {
		t.Fatalf("write calls=%d, want no write after poison", fault.writeCalls)
	}
	if got := len(j.Records()); got != 1 {
		t.Fatalf("in-memory records=%d, want only the confirmed record", got)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close poisoned journal: %v", err)
	}

	replayed, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("reopen after sync failure: %v", err)
	}
	defer replayed.Close()
	if got := replayed.Records(); len(got) != 2 || got[0].RecordID != persisted.RecordID || got[1].RecordID != uncertain.RecordID {
		t.Fatalf("replayed records=%+v, want complete frames for persisted and uncertain intents", got)
	}
	if got := len(replayed.Warnings()); got != 0 {
		t.Fatalf("warnings=%d, want no structural corruption", got)
	}
}

func TestFileJournalPostWriteApplyFailurePoisonsAppender(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "apply-failure.journal")
	j, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	persisted := testIntent("persisted-before-apply-failure")
	if err := j.AppendCommandIntent(ctx, persisted); err != nil {
		t.Fatalf("append persisted intent: %v", err)
	}

	fault := &faultJournalFile{file: j.file}
	fault.afterWrite = func() {
		j.st.conflictLimit = len(j.st.recordIdentity)
	}
	j.file = fault
	uncertain := testIntent("fully-written-before-apply-failure")
	if err := j.AppendCommandIntent(ctx, uncertain); !errors.Is(err, ErrConflictIndexCapacity) {
		t.Fatalf("apply-failed append err=%v, want ErrConflictIndexCapacity", err)
	}
	if fault.closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1 after post-write apply failure", fault.closeCalls)
	}
	if err := j.AppendCommandIntent(ctx, testIntent("must-not-follow-apply-failure")); err == nil {
		t.Fatal("append after post-write apply failure succeeded")
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close poisoned journal: %v", err)
	}

	replayed, err := OpenFile(path, FileOptions{})
	if err != nil {
		t.Fatalf("reopen after post-write apply failure: %v", err)
	}
	defer replayed.Close()
	if got := replayed.Records(); len(got) != 2 || got[0].RecordID != persisted.RecordID || got[1].RecordID != uncertain.RecordID {
		t.Fatalf("replayed records=%+v, want both complete frames", got)
	}
}

type faultJournalFile struct {
	file       journalFile
	write      func([]byte) (int, error)
	afterWrite func()
	syncErr    error
	writeCalls int
	closeCalls int
}

func (f *faultJournalFile) Write(p []byte) (int, error) {
	f.writeCalls++
	var n int
	var err error
	if f.write != nil {
		n, err = f.write(p)
	} else {
		n, err = f.file.Write(p)
	}
	if f.afterWrite != nil {
		f.afterWrite()
	}
	return n, err
}

func (f *faultJournalFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.file.Sync()
}

func (f *faultJournalFile) Close() error {
	f.closeCalls++
	return f.file.Close()
}

func assertOnlyIntent(t *testing.T, journal *FileJournal, recordID string) {
	t.Helper()
	records := journal.Records()
	if len(records) != 1 || records[0].RecordID != recordID {
		t.Fatalf("records=%+v, want only intent %q", records, recordID)
	}
}

var _ journalFile = (*os.File)(nil)
var _ journalFile = (*faultJournalFile)(nil)
