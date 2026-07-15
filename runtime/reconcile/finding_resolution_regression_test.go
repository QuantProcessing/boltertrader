package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/shopspring/decimal"
)

func TestJournalStateStoreFindingOperationsAreConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	store := NewJournalStateStore(journal.NewMemory())
	scope := ScopeKey{Venue: "T", AccountID: "concurrent-findings"}
	const iterations = 200

	start := make(chan struct{})
	errCh := make(chan error, iterations*4)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			if _, err := store.LoadOpenFindings(ctx, scope); err != nil {
				errCh <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			id := fmt.Sprintf("concurrent-record-%d", i)
			at := time.Unix(int64(i+1), 0)
			if err := store.RecordFinding(ctx, Finding{
				ID: id, PassID: model.ReconciliationID(id), Scope: scope,
				Severity: FindingBlocking, Blocking: true, CreatedAt: at,
			}); err != nil {
				errCh <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			id := fmt.Sprintf("concurrent-resolve-%d", i)
			at := time.Unix(int64(iterations+i+1), 0)
			if err := store.RecordFinding(ctx, Finding{
				ID: id, PassID: model.ReconciliationID(id), Scope: scope,
				Severity: FindingBlocking, Blocking: true, CreatedAt: at,
			}); err != nil {
				errCh <- err
				continue
			}
			if err := store.ResolveFinding(ctx, FindingResolution{
				FindingID: id, PassID: model.ReconciliationID(id), ResolvedAt: at.Add(time.Nanosecond),
			}); err != nil {
				errCh <- err
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent finding operation: %v", err)
	}
}

func TestJournalStateStoreFindingOperationsPermitReentrantJournalCallbacks(t *testing.T) {
	scope := ScopeKey{Venue: "T", AccountID: "reentrant-findings"}
	finding := Finding{
		ID: "reentrant-finding", PassID: "reentrant-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}

	t.Run("append callback reads findings", func(t *testing.T) {
		journalStore := &reentrantFindingJournal{MemoryJournal: journal.NewMemory()}
		store := NewJournalStateStore(journalStore)
		journalStore.appendHook = func() {
			_, _ = store.LoadOpenFindings(context.Background(), scope)
		}
		assertFindingOperationCompletes(t, func() error {
			return store.RecordFinding(context.Background(), finding)
		})
	})

	t.Run("records callback writes finding", func(t *testing.T) {
		journalStore := &reentrantFindingJournal{MemoryJournal: journal.NewMemory()}
		store := NewJournalStateStore(journalStore)
		journalStore.recordsHook = func() {
			_ = store.RecordFinding(context.Background(), finding)
		}
		assertFindingOperationCompletes(t, func() error {
			_, err := store.LoadOpenFindings(context.Background(), scope)
			return err
		})
	})
}

func TestJournalStateStoreFindingLifecycleSupportsOpaqueJournalStore(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "opaque-findings"}
	store := NewJournalStateStore(&opaqueJournalStore{Store: journal.NewMemory()})
	finding := Finding{
		ID: "opaque-finding", PassID: "opaque-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}

	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("record finding through opaque journal.Store: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load finding through opaque journal.Store: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("open findings=%+v, want recorded opaque finding", open)
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: finding.ID, PassID: "opaque-resolution", ResolvedAt: time.Unix(2, 0),
	}); err != nil {
		t.Fatalf("resolve finding through opaque journal.Store: %v", err)
	}
	open, err = store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("reload finding through opaque journal.Store: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open findings=%+v, resolved opaque finding remained open", open)
	}
}

func TestJournalStateStoreReplayIsNotInvalidatedByUnrelatedFindingUpdates(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "per-finding-replay"}
	journalStore := &destabilizingFindingJournal{MemoryJournal: journal.NewMemory()}
	store := NewJournalStateStore(journalStore)
	noise := Finding{
		ID: "replay-noise", PassID: "noise-0", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, noise); err != nil {
		t.Fatalf("record noise finding: %v", err)
	}

	var noiseSequence atomic.Int64
	var hookMu sync.Mutex
	var hookErr error
	bumpNoise := func() {
		n := noiseSequence.Add(1)
		updated := noise
		updated.PassID = model.ReconciliationID(fmt.Sprintf("noise-%d", n))
		updated.CreatedAt = time.Unix(n+1, 0)
		if err := store.RecordFinding(ctx, updated); err != nil {
			hookMu.Lock()
			hookErr = errors.Join(hookErr, err)
			hookMu.Unlock()
		}
	}
	journalStore.appendHook = bumpNoise
	journalStore.recordsHook = bumpNoise
	target := Finding{
		ID: "must-remain-visible", PassID: "target-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(10, 0),
	}
	if err := store.RecordFinding(ctx, target); err != nil {
		t.Fatalf("record target finding: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load findings while unrelated metadata changes: %v", err)
	}
	hookMu.Lock()
	err = errors.Join(err, hookErr)
	hookMu.Unlock()
	if err != nil {
		t.Fatalf("finding callback: %v", err)
	}
	for _, finding := range open {
		if finding.ID == target.ID {
			return
		}
	}
	t.Fatalf("open findings=%+v, durable target was hidden by unrelated replay changes", open)
}

func TestJournalStateStoreResolvesEveryConcurrentRecordForSameFinding(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "same-finding-race"}
	backing := journal.NewMemoryWithRetention(1)
	journalStore := &twoAppendBarrierJournal{
		MemoryJournal: backing,
		ready:         make(chan struct{}),
		release:       make(chan struct{}),
	}
	store := NewJournalStateStore(journalStore)
	findings := []Finding{
		{ID: "same-logical-finding", PassID: "same-pass-1", Scope: scope, Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0)},
		{ID: "same-logical-finding", PassID: "same-pass-2", Scope: scope, Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(2, 0)},
	}
	errCh := make(chan error, len(findings))
	for _, finding := range findings {
		finding := finding
		go func() { errCh <- store.RecordFinding(ctx, finding) }()
	}
	select {
	case <-journalStore.ready:
		close(journalStore.release)
	case <-time.After(time.Second):
		close(journalStore.release)
		t.Fatal("same-ID records did not reach the concurrent append barrier")
	}
	for range findings {
		if err := <-errCh; err != nil {
			t.Fatalf("record concurrent finding: %v", err)
		}
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: findings[0].ID, PassID: "same-resolution", ResolvedAt: time.Unix(3, 0),
	}); err != nil {
		t.Fatalf("resolve concurrent finding records: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := backing.AppendReport(ctx, journal.ReportRecord{RecordID: fmt.Sprintf("same-finding-noise-%d", i)}); err != nil {
			t.Fatalf("append retention noise %d: %v", i, err)
		}
	}
	rebuilt := NewJournalStateStore(backing)
	open, err := rebuilt.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load rebuilt findings: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("rebuilt findings=%+v, a concurrent same-ID record was not resolved", open)
	}
}

func TestJournalStateStoreReplayPreservesLatestBlockingFindingMetadata(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "latest-finding-metadata"}
	store := NewJournalStateStore(journal.NewMemory())
	first := Finding{
		ID: "stable-logical-finding", PassID: "metadata-pass-1", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	latest := first
	latest.PassID = "metadata-pass-2"
	latest.CreatedAt = time.Unix(2, 0)
	latest.Message = "latest operator context"
	if err := store.RecordFinding(ctx, first); err != nil {
		t.Fatalf("record first finding: %v", err)
	}
	if err := store.RecordFinding(ctx, latest); err != nil {
		t.Fatalf("record latest finding metadata: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load finding metadata: %v", err)
	}
	if len(open) != 1 || open[0].PassID != latest.PassID || !open[0].CreatedAt.Equal(latest.CreatedAt) || open[0].Message != latest.Message {
		t.Fatalf("open findings=%+v, want latest metadata %+v", open, latest)
	}
}

func TestJournalStateStoreReentrantRecordDuringResolutionReopensFinding(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "reentrant-reopen"}
	journalStore := &reentrantFindingJournal{MemoryJournal: journal.NewMemory()}
	store := NewJournalStateStore(journalStore)
	first := Finding{
		ID: "reentrant-reopen-finding", PassID: "reopen-pass-1", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, first); err != nil {
		t.Fatalf("record first finding: %v", err)
	}
	reopened := first
	reopened.PassID = "reopen-pass-2"
	reopened.CreatedAt = time.Unix(2, 0)
	journalStore.appendHook = func() {
		_ = store.RecordFinding(ctx, reopened)
	}
	assertFindingOperationCompletes(t, func() error {
		return store.ResolveFinding(ctx, FindingResolution{
			FindingID: first.ID, PassID: "reopen-resolution", ResolvedAt: time.Unix(3, 0),
		})
	})
	open, err := NewJournalStateStore(journalStore.MemoryJournal).LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load reopened finding: %v", err)
	}
	if len(open) != 1 || open[0].PassID != reopened.PassID {
		t.Fatalf("open findings=%+v, reentrant record during resolution must reopen", open)
	}
}

func TestJournalStateStoreExactReentrantRecordDuringResolutionGetsNewIdentity(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "exact-reentrant-reopen"}
	journalStore := &reentrantFindingJournal{MemoryJournal: journal.NewMemory()}
	store := NewJournalStateStore(journalStore)
	finding := Finding{
		ID: "exact-reentrant-reopen-finding", PassID: "exact-reopen-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("record first finding: %v", err)
	}
	journalStore.appendHook = func() {
		_ = store.RecordFinding(ctx, finding)
	}
	assertFindingOperationCompletes(t, func() error {
		return store.ResolveFinding(ctx, FindingResolution{
			FindingID: finding.ID, PassID: "exact-reopen-resolution", ResolvedAt: time.Unix(2, 0),
		})
	})
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load exact reopened finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("same-instance findings=%+v, exact reentrant record must reopen", open)
	}
	open, err = NewJournalStateStore(journalStore.MemoryJournal).LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load rebuilt exact reopened finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("rebuilt findings=%+v, exact reentrant record must have a new durable identity", open)
	}
}

func TestJournalStateStoreExactFindingCanReopenAfterResolvedHistoryCompacts(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "exact-reopen-after-compaction"}
	backing := journal.NewMemoryWithRetention(1)
	store := NewJournalStateStore(backing)
	finding := Finding{
		ID: "exact-reopen-after-compaction-finding", PassID: "exact-compacted-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("record first finding: %v", err)
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: finding.ID, PassID: "exact-compacted-resolution", ResolvedAt: time.Unix(2, 0),
	}); err != nil {
		t.Fatalf("resolve first finding: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := backing.AppendReport(ctx, journal.ReportRecord{RecordID: fmt.Sprintf("exact-compacted-noise-%d", i)}); err != nil {
			t.Fatalf("append compaction noise %d: %v", i, err)
		}
	}

	restarted := NewJournalStateStore(backing)
	if err := restarted.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("reopen exact finding after compaction: %v", err)
	}
	open, err := restarted.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load exact reopened finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("same-instance findings=%+v, exact finding did not reopen after compaction", open)
	}
	open, err = NewJournalStateStore(backing).LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load rebuilt exact reopened finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("rebuilt findings=%+v, exact reopen was not durable after compaction", open)
	}
}

func TestJournalStateStoreFindingIdentityEntropyFailureHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	backing := journal.NewMemory()
	store := NewJournalStateStore(backing)
	entropyErr := errors.New("finding entropy unavailable")
	store.findingEntropy = failingFindingEntropyReader{err: entropyErr}
	finding := Finding{
		ID: "entropy-finding", PassID: "entropy-pass",
		Scope:    ScopeKey{Venue: "T", AccountID: "finding-entropy"},
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}

	if err := store.RecordFinding(ctx, finding); !errors.Is(err, entropyErr) {
		t.Fatalf("record finding err=%v, want entropy error", err)
	}
	if records := backing.Records(); len(records) != 0 {
		t.Fatalf("journal records=%+v, entropy failure must precede append", records)
	}
	if len(store.findings) != 0 || len(store.findingRecords) != 0 {
		t.Fatalf("local findings=%+v records=%+v, entropy failure mutated state", store.findings, store.findingRecords)
	}
}

func TestJournalStateStorePartialMultiRecordResolutionRemainsFailClosed(t *testing.T) {
	ctx := context.Background()
	scope := ScopeKey{Venue: "T", AccountID: "partial-multi-resolution"}
	backing := journal.NewMemoryWithRetention(1)
	resolutionErr := errors.New("second resolution append failed")
	journalStore := &twoAppendBarrierJournal{
		MemoryJournal:    backing,
		ready:            make(chan struct{}),
		release:          make(chan struct{}),
		failResolutionAt: 2,
		resolutionErr:    resolutionErr,
	}
	store := NewJournalStateStore(journalStore)
	findings := []Finding{
		{ID: "partial-resolution-finding", PassID: "partial-pass-1", Scope: scope, Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0)},
		{ID: "partial-resolution-finding", PassID: "partial-pass-2", Scope: scope, Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(2, 0)},
	}
	errCh := make(chan error, len(findings))
	for _, finding := range findings {
		finding := finding
		go func() { errCh <- store.RecordFinding(ctx, finding) }()
	}
	select {
	case <-journalStore.ready:
		close(journalStore.release)
	case <-time.After(time.Second):
		close(journalStore.release)
		t.Fatal("same-ID records did not reach the concurrent append barrier")
	}
	for range findings {
		if err := <-errCh; err != nil {
			t.Fatalf("record concurrent finding: %v", err)
		}
	}

	err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: findings[0].ID, PassID: "partial-resolution", ResolvedAt: time.Unix(3, 0),
	})
	if !errors.Is(err, resolutionErr) {
		t.Fatalf("partial resolution err=%v, want %v", err, resolutionErr)
	}
	for i := 0; i < 8; i++ {
		if err := backing.AppendReport(ctx, journal.ReportRecord{RecordID: fmt.Sprintf("partial-resolution-noise-%d", i)}); err != nil {
			t.Fatalf("append partial-resolution noise %d: %v", i, err)
		}
	}
	open, err := NewJournalStateStore(backing).LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load partially resolved finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != findings[0].ID {
		t.Fatalf("open findings=%+v, one unconfirmed record must remain blocking", open)
	}

	journalStore.failResolutionAt = 0
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: findings[0].ID, PassID: "partial-resolution-retry", ResolvedAt: time.Unix(4, 0),
	}); err != nil {
		t.Fatalf("retry remaining resolution: %v", err)
	}
	open, err = NewJournalStateStore(backing).LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load fully resolved finding: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open findings=%+v, retry must close the remaining record", open)
	}
}

func TestJournalStateStoreDoesNotRetainNonBlockingOpaqueReports(t *testing.T) {
	ctx := context.Background()
	backing := journal.NewMemory()
	store := NewJournalStateStore(&opaqueJournalStore{Store: backing})
	for i := 0; i < 100; i++ {
		if err := store.RecordFinding(ctx, Finding{
			ID: fmt.Sprintf("opaque-diagnostic-%d", i), PassID: model.ReconciliationID(fmt.Sprintf("opaque-pass-%d", i)),
			Scope: ScopeKey{Venue: "T", AccountID: "opaque-diagnostics"}, Severity: FindingWarning,
			CreatedAt: time.Unix(int64(i+1), 0),
		}); err != nil {
			t.Fatalf("record opaque diagnostic %d: %v", i, err)
		}
	}
	if len(store.findings) != 0 || len(store.findingRecords) != 0 || len(store.findingVersions) != 0 {
		t.Fatalf("nonblocking diagnostics retained local open state: findings=%d records=%d versions=%d", len(store.findings), len(store.findingRecords), len(store.findingVersions))
	}
	if records := backing.Records(); len(records) == 0 {
		t.Fatal("nonblocking diagnostics were not journaled")
	}
}

func TestJournalStateStoreNonBlockingFindingPayloadVersionsDoNotConflict(t *testing.T) {
	ctx := context.Background()
	backing := journal.NewMemory()
	store := NewJournalStateStore(backing)
	first := Finding{
		ID:        "repeated-nonblocking-diagnostic",
		PassID:    "diagnostic-pass",
		Scope:     ScopeKey{Venue: "T", AccountID: "diagnostic-account"},
		Stream:    StreamOrders,
		Severity:  FindingWarning,
		Code:      "PARTIAL_ORDER_REPORT",
		Message:   "partial snapshot cannot prove terminal state",
		CreatedAt: time.Unix(1, 0),
	}
	updated := first
	updated.CreatedAt = time.Unix(2, 0)

	if err := store.RecordFinding(ctx, first); err != nil {
		t.Fatalf("record first diagnostic: %v", err)
	}
	if err := store.RecordFinding(ctx, updated); err != nil {
		t.Fatalf("record updated diagnostic with the same logical ID: %v", err)
	}
	records := backing.Records()
	if len(records) != 2 {
		t.Fatalf("records=%d, want both diagnostic payload versions", len(records))
	}
	if records[0].RecordID == records[1].RecordID {
		t.Fatalf("physical record IDs alias for distinct diagnostic payloads: %q", records[0].RecordID)
	}

	if err := store.RecordFinding(ctx, first); err != nil {
		t.Fatalf("retry first diagnostic: %v", err)
	}
	if got := len(backing.Records()); got != 2 {
		t.Fatalf("records=%d after exact retry, want idempotent physical report", got)
	}
}

func TestJournalStateStoreReplaySkipsNonBlockingReports(t *testing.T) {
	ctx := context.Background()
	backing := journal.NewMemory()
	scope := ScopeKey{Venue: "T", AccountID: "replayable-diagnostics"}
	writer := NewJournalStateStore(backing)
	for i := 0; i < 100; i++ {
		if err := writer.RecordFinding(ctx, Finding{
			ID: fmt.Sprintf("replayable-diagnostic-%d", i), PassID: model.ReconciliationID(fmt.Sprintf("replayable-pass-%d", i)),
			Scope: scope, Severity: FindingWarning, CreatedAt: time.Unix(int64(i+1), 0),
		}); err != nil {
			t.Fatalf("record replayable diagnostic %d: %v", i, err)
		}
	}

	rebuilt := NewJournalStateStore(backing)
	open, err := rebuilt.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load replayed diagnostics: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open findings=%+v, nonblocking reports must not replay as recovery state", open)
	}
	rebuilt.mu.Lock()
	defer rebuilt.mu.Unlock()
	if len(rebuilt.findings) != 0 || len(rebuilt.findingRecords) != 0 || len(rebuilt.findingVersions) != 0 {
		t.Fatalf("replayed diagnostics retained local state: findings=%d records=%d versions=%d", len(rebuilt.findings), len(rebuilt.findingRecords), len(rebuilt.findingVersions))
	}
}

func TestJournalStateStoreReclaimsResolvedFindingVersions(t *testing.T) {
	ctx := context.Background()
	store := NewJournalStateStore(journal.NewMemory())
	scope := ScopeKey{Venue: "T", AccountID: "version-reclamation"}
	for i := 0; i < 100; i++ {
		findingID := fmt.Sprintf("resolved-version-%d", i)
		at := time.Unix(int64(i+1), 0)
		if err := store.RecordFinding(ctx, Finding{
			ID: findingID, PassID: model.ReconciliationID(findingID), Scope: scope,
			Severity: FindingBlocking, Blocking: true, CreatedAt: at,
		}); err != nil {
			t.Fatalf("record finding %d: %v", i, err)
		}
		if err := store.ResolveFinding(ctx, FindingResolution{
			FindingID: findingID, PassID: model.ReconciliationID("resolve-" + findingID), ResolvedAt: at.Add(time.Nanosecond),
		}); err != nil {
			t.Fatalf("resolve finding %d: %v", i, err)
		}
	}
	if len(store.findings) != 0 || len(store.findingRecords) != 0 || len(store.findingVersions) != 0 || len(store.resolvingEpochs) != 0 || len(store.findingOperations) != 0 {
		t.Fatalf("resolved state retained: findings=%d records=%d versions=%d resolving=%d operations=%d", len(store.findings), len(store.findingRecords), len(store.findingVersions), len(store.resolvingEpochs), len(store.findingOperations))
	}
}

func TestJournalStateStoreReclaimsVersionsWhileUnrelatedReplayIsBlocked(t *testing.T) {
	ctx := context.Background()
	journalStore := &blockingFindingReplayJournal{
		MemoryJournal: journal.NewMemory(),
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	store := NewJournalStateStore(journalStore)
	journalStore.blockNext.Store(true)
	replayDone := make(chan error, 1)
	go func() {
		_, err := store.LoadOpenFindings(ctx, ScopeKey{Venue: "T", AccountID: "blocked-replay"})
		replayDone <- err
	}()
	select {
	case <-journalStore.started:
	case <-time.After(time.Second):
		close(journalStore.release)
		t.Fatal("finding replay did not block in Records")
	}
	defer func() {
		close(journalStore.release)
		select {
		case err := <-replayDone:
			if err != nil {
				t.Errorf("blocked replay: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("blocked replay did not finish after release")
		}
	}()

	scope := ScopeKey{Venue: "T", AccountID: "version-reclamation-with-blocked-replay"}
	for i := 0; i < 100; i++ {
		findingID := fmt.Sprintf("blocked-replay-version-%d", i)
		at := time.Unix(int64(i+1), 0)
		if err := store.RecordFinding(ctx, Finding{
			ID: findingID, PassID: model.ReconciliationID(findingID), Scope: scope,
			Severity: FindingBlocking, Blocking: true, CreatedAt: at,
		}); err != nil {
			t.Fatalf("record finding %d: %v", i, err)
		}
		if err := store.ResolveFinding(ctx, FindingResolution{
			FindingID: findingID, PassID: model.ReconciliationID("resolve-" + findingID), ResolvedAt: at.Add(time.Nanosecond),
		}); err != nil {
			t.Fatalf("resolve finding %d: %v", i, err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.findingVersions) != 0 {
		t.Fatalf("inactive finding versions=%d while unrelated replay is blocked, want 0", len(store.findingVersions))
	}
}

func TestJournalStateStoreReplayRecoversFindingPersistedDuringAmbiguousAppend(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("append acknowledgement lost during replay")
	journalStore := &blockingFindingReplayJournal{
		MemoryJournal: journal.NewMemory(),
		started:       make(chan struct{}),
		release:       make(chan struct{}),
		appendErr:     appendErr,
	}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "ambiguous-append-during-replay"}
	journalStore.blockNext.Store(true)
	type loadResult struct {
		findings []Finding
		err      error
	}
	loadDone := make(chan loadResult, 1)
	go func() {
		findings, err := store.LoadOpenFindings(ctx, scope)
		loadDone <- loadResult{findings: findings, err: err}
	}()
	select {
	case <-journalStore.started:
	case <-time.After(time.Second):
		close(journalStore.release)
		t.Fatal("finding replay did not capture its pre-append snapshot")
	}

	finding := Finding{
		ID: "ambiguous-during-replay", PassID: "ambiguous-during-replay-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	journalStore.failNextAppend.Store(true)
	if err := store.RecordFinding(ctx, finding); !errors.Is(err, appendErr) {
		close(journalStore.release)
		t.Fatalf("record finding err=%v, want %v", err, appendErr)
	}
	close(journalStore.release)
	select {
	case result := <-loadDone:
		if result.err != nil {
			t.Fatalf("load findings: %v", result.err)
		}
		if len(result.findings) != 1 || result.findings[0].ID != finding.ID {
			t.Fatalf("open findings=%+v, durable ambiguous append must not be hidden by a stale replay", result.findings)
		}
	case <-time.After(time.Second):
		t.Fatal("finding replay did not finish after release")
	}
}

func TestJournalStateStoreTargetedReplayRetriesAfterAmbiguousAppend(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("targeted append acknowledgement lost")
	journalStore := &coordinatedAmbiguousFindingJournal{
		MemoryJournal:  journal.NewMemory(),
		appendStarted:  make(chan struct{}),
		allowAppend:    make(chan struct{}),
		recordsStarted: make(chan struct{}),
		allowRecords:   make(chan struct{}),
		appendErr:      appendErr,
	}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "targeted-ambiguous-append"}
	finding := Finding{
		ID: "targeted-ambiguous-finding", PassID: "targeted-ambiguous-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	journalStore.blockAppend.Store(true)
	recordDone := make(chan error, 1)
	go func() { recordDone <- store.RecordFinding(ctx, finding) }()
	select {
	case <-journalStore.appendStarted:
	case <-time.After(time.Second):
		close(journalStore.allowAppend)
		t.Fatal("finding append did not reach the coordination barrier")
	}

	journalStore.blockRecords.Store(true)
	replayDone := make(chan error, 1)
	go func() { replayDone <- store.replayFindingUntilStable(ctx, finding.ID) }()
	select {
	case <-journalStore.recordsStarted:
	case <-time.After(time.Second):
		close(journalStore.allowAppend)
		close(journalStore.allowRecords)
		t.Fatal("targeted replay did not capture its pre-append snapshot")
	}
	close(journalStore.allowAppend)
	if err := <-recordDone; !errors.Is(err, appendErr) {
		close(journalStore.allowRecords)
		t.Fatalf("record finding err=%v, want %v", err, appendErr)
	}
	close(journalStore.allowRecords)
	select {
	case err := <-replayDone:
		if err != nil {
			t.Fatalf("targeted replay: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("targeted replay did not finish after release")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.findingRecords[finding.ID]) != 1 || store.findings[finding.ID].ID != finding.ID {
		t.Fatalf("local finding=%+v records=%v, durable ambiguous append was hidden", store.findings[finding.ID], store.findingRecords[finding.ID])
	}
}

func TestJournalStateStoreCanceledRecordFindingHasNoSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backing := journal.NewMemory()
	store := NewJournalStateStore(backing)
	store.pendingFindingLimit = 2
	for i := 0; i < 10; i++ {
		err := store.RecordFinding(ctx, Finding{
			ID: fmt.Sprintf("canceled-finding-%d", i), PassID: model.ReconciliationID(fmt.Sprintf("canceled-pass-%d", i)),
			Scope:    ScopeKey{Venue: "T", AccountID: "canceled-finding"},
			Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(int64(i+1), 0),
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("record canceled finding %d err=%v, want context.Canceled", i, err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.pendingFindingRecords) != 0 || len(store.findingVersions) != 0 || len(store.findingOperations) != 0 || len(store.findings) != 0 || len(store.findingRecords) != 0 {
		t.Fatalf("canceled finding mutated local state: pending=%d versions=%d operations=%d findings=%d records=%d", len(store.pendingFindingRecords), len(store.findingVersions), len(store.findingOperations), len(store.findings), len(store.findingRecords))
	}
	if records := backing.Records(); len(records) != 0 {
		t.Fatalf("canceled finding journal records=%+v, want none", records)
	}
}

func TestJournalStateStorePendingFindingIdentitiesAreBoundedAndRetryStable(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("finding append unavailable")
	journalStore := &failingFindingAppendJournal{MemoryJournal: journal.NewMemory(), err: appendErr}
	store := NewJournalStateStore(journalStore)
	store.pendingFindingLimit = 2
	scope := ScopeKey{Venue: "T", AccountID: "pending-finding-cap"}
	finding := func(id string, at int64) Finding {
		return Finding{
			ID: id, PassID: model.ReconciliationID(id), Scope: scope,
			Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(at, 0),
		}
	}
	first := finding("pending-finding-1", 1)
	if err := store.RecordFinding(ctx, first); !errors.Is(err, appendErr) {
		t.Fatalf("first append err=%v, want %v", err, appendErr)
	}
	if err := store.RecordFinding(ctx, finding("pending-finding-2", 2)); !errors.Is(err, appendErr) {
		t.Fatalf("second append err=%v, want %v", err, appendErr)
	}
	if err := store.RecordFinding(ctx, finding("pending-finding-3", 3)); !errors.Is(err, errPendingFindingCapacity) {
		t.Fatalf("third append err=%v, want pending capacity error", err)
	}
	if err := store.RecordFinding(ctx, first); !errors.Is(err, appendErr) {
		t.Fatalf("retry append err=%v, want original append error", err)
	}

	journalStore.mu.Lock()
	recordIDs := append([]string(nil), journalStore.recordIDs...)
	journalStore.mu.Unlock()
	if len(recordIDs) != 3 || recordIDs[0] != recordIDs[2] {
		t.Fatalf("attempt record IDs=%v, exact failed retry must reuse its identity", recordIDs)
	}
	if len(store.pendingFindingRecords) != 2 || len(store.findingVersions) != 0 || len(store.findings) != 0 || len(store.findingRecords) != 0 {
		t.Fatalf("pending state sizes pending=%d versions=%d findings=%d records=%d", len(store.pendingFindingRecords), len(store.findingVersions), len(store.findings), len(store.findingRecords))
	}
}

func TestJournalStateStoreRecoversFindingPersistedBeforeAppendError(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("append acknowledgement lost")
	journalStore := &appendThenErrorFindingJournal{MemoryJournal: journal.NewMemory(), err: appendErr}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "append-error-recovery"}
	finding := Finding{
		ID: "append-error-finding", PassID: "append-error-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, finding); !errors.Is(err, appendErr) {
		t.Fatalf("record finding err=%v, want %v", err, appendErr)
	}
	if len(store.pendingFindingRecords) != 1 {
		t.Fatalf("pending identities=%d, ambiguous append must retain retry identity", len(store.pendingFindingRecords))
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("replay persisted finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("open findings=%+v, persisted append must remain blocking", open)
	}
	if len(store.pendingFindingRecords) != 0 {
		t.Fatalf("pending identities=%d, replay should retire the persisted attempt", len(store.pendingFindingRecords))
	}
	journalStore.mu.Lock()
	callsBeforeRetry := journalStore.calls
	journalStore.mu.Unlock()
	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("exact retry after replay should coalesce: %v", err)
	}
	journalStore.mu.Lock()
	callsAfterRetry := journalStore.calls
	journalStore.mu.Unlock()
	if callsAfterRetry != callsBeforeRetry {
		t.Fatalf("append calls before=%d after=%d, exact open retry should not append", callsBeforeRetry, callsAfterRetry)
	}
}

func TestJournalStateStoreOpaqueJournalFailsClosedOnAmbiguousFindingAppend(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("opaque append acknowledgement lost")
	journalStore := &appendThenErrorFindingJournal{MemoryJournal: journal.NewMemory(), err: appendErr}
	store := NewJournalStateStore(&opaqueJournalStore{Store: journalStore})
	scope := ScopeKey{Venue: "T", AccountID: "opaque-ambiguous-finding"}
	finding := Finding{
		ID: "opaque-ambiguous-finding", PassID: "opaque-ambiguous-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, finding); !errors.Is(err, appendErr) {
		t.Fatalf("record finding err=%v, want %v", err, appendErr)
	}
	if _, err := store.LoadOpenFindings(ctx, scope); !errors.Is(err, errPendingFindingReplayUnavailable) {
		t.Fatalf("load findings err=%v, want replay-unavailable failure", err)
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: finding.ID, PassID: "opaque-ambiguous-resolution", ResolvedAt: time.Unix(2, 0),
	}); !errors.Is(err, errPendingFindingReplayUnavailable) {
		t.Fatalf("resolve finding err=%v, want replay-unavailable failure", err)
	}

	journalStore.err = nil
	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("retry exact ambiguous finding: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load finding after exact retry: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("open findings=%+v, exact retry should publish the opaque finding", open)
	}
}

func TestJournalStateStoreOpaqueStaleRetryCannotReopenResolvedRecord(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("initial opaque append unavailable")
	journalStore := &staleOpaqueFindingJournal{
		MemoryJournal: journal.NewMemory(),
		appendErr:     appendErr,
		retryReady:    make(chan int32, 2),
		allowFirst:    make(chan struct{}),
		allowSecond:   make(chan struct{}),
	}
	store := NewJournalStateStore(&opaqueJournalStore{Store: journalStore})
	scope := ScopeKey{Venue: "T", AccountID: "opaque-stale-retry"}
	finding := Finding{
		ID: "opaque-stale-retry", PassID: "opaque-stale-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, finding); !errors.Is(err, appendErr) {
		t.Fatalf("initial record err=%v, want %v", err, appendErr)
	}
	retryDone := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { retryDone <- store.RecordFinding(ctx, finding) }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-journalStore.retryReady:
		case <-time.After(time.Second):
			close(journalStore.allowFirst)
			close(journalStore.allowSecond)
			t.Fatal("exact retries did not reach both append barriers")
		}
	}
	close(journalStore.allowFirst)
	if err := <-retryDone; err != nil {
		close(journalStore.allowSecond)
		t.Fatalf("first exact retry: %v", err)
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: finding.ID, PassID: "opaque-stale-resolution", ResolvedAt: time.Unix(2, 0),
	}); err != nil {
		close(journalStore.allowSecond)
		t.Fatalf("resolve finding before stale retry returns: %v", err)
	}
	close(journalStore.allowSecond)
	if err := <-retryDone; err != nil {
		t.Fatalf("stale exact retry: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load same-instance findings: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("same-instance findings=%+v, stale duplicate completion reopened a resolved record", open)
	}
	open, err = NewJournalStateStore(journalStore.MemoryJournal).LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load rebuilt findings: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("rebuilt findings=%+v, durable order should remain resolved", open)
	}
}

func TestJournalStateStoreReplayableJournalFailsClosedWhenPendingRecordIsInvisible(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("durability acknowledgement unavailable")
	journalStore := &toggleFindingAppendJournal{MemoryJournal: journal.NewMemory(), err: appendErr}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "replayable-invisible-pending"}
	finding := Finding{
		ID: "replayable-invisible-pending", PassID: "replayable-invisible-pass", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, finding); !errors.Is(err, appendErr) {
		t.Fatalf("record finding err=%v, want %v", err, appendErr)
	}
	if _, err := store.LoadOpenFindings(ctx, scope); !errors.Is(err, errPendingFindingReplayUnavailable) {
		t.Fatalf("load findings err=%v, want replay-unavailable failure", err)
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID: finding.ID, PassID: "replayable-invisible-resolution", ResolvedAt: time.Unix(2, 0),
	}); !errors.Is(err, errPendingFindingReplayUnavailable) {
		t.Fatalf("resolve finding err=%v, want replay-unavailable failure", err)
	}

	journalStore.err = nil
	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("retry exact pending finding: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load finding after exact retry: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("open findings=%+v, exact retry should publish the finding", open)
	}
}

func TestJournalStateStorePendingRetrySurvivesOpenFindingAndResolutionEpoch(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("reopen append unavailable")
	journalStore := &toggleFindingAppendJournal{MemoryJournal: journal.NewMemory()}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "pending-retry-resolution-epoch"}
	first := Finding{
		ID: "pending-retry-resolution-epoch", PassID: "pending-retry-first", Scope: scope,
		Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, first); err != nil {
		t.Fatalf("record first finding: %v", err)
	}
	reopened := first
	reopened.PassID = "pending-retry-reopen"
	reopened.CreatedAt = time.Unix(2, 0)
	store.mu.Lock()
	store.resolvingEpochs[first.ID] = []string{"transient-resolution-epoch"}
	store.mu.Unlock()
	journalStore.err = appendErr
	if err := store.RecordFinding(ctx, reopened); !errors.Is(err, appendErr) {
		t.Fatalf("record overlapping reopen err=%v, want %v", err, appendErr)
	}
	store.mu.Lock()
	delete(store.resolvingEpochs, first.ID)
	store.mu.Unlock()

	journalStore.err = nil
	if err := store.RecordFinding(ctx, reopened); err != nil {
		t.Fatalf("retry reopen after resolution epoch ended: %v", err)
	}
	store.mu.Lock()
	pending := len(store.pendingFindingRecords)
	records := len(store.findingRecords[first.ID])
	store.mu.Unlock()
	if pending != 0 || records != 2 {
		t.Fatalf("pending=%d records=%d, retry must publish the ambiguous physical record alongside the existing open record", pending, records)
	}
}

func TestJournalStateStoreUpdatedFindingRetriesOlderPendingAttempt(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("first finding append unavailable")
	journalStore := &toggleFindingAppendJournal{MemoryJournal: journal.NewMemory(), err: appendErr}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "updated-pending-retry"}
	first := Finding{
		ID: "updated-pending-retry", PassID: "updated-pending-first", Scope: scope,
		Severity: FindingBlocking, Blocking: true, Message: "first context", CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, first); !errors.Is(err, appendErr) {
		t.Fatalf("record first finding err=%v, want %v", err, appendErr)
	}
	updated := first
	updated.PassID = "updated-pending-second"
	updated.Message = "newer context"
	updated.CreatedAt = time.Unix(2, 0)
	journalStore.err = nil
	if err := store.RecordFinding(ctx, updated); err != nil {
		t.Fatalf("record updated finding retry: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load updated finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != updated.ID || open[0].PassID != updated.PassID || open[0].Message != updated.Message {
		t.Fatalf("open findings=%+v, want recovered physical record with latest in-memory context", open)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.pendingFindingRecords) != 0 || len(store.findingRecords[updated.ID]) != 1 {
		t.Fatalf("pending=%d records=%v, updated retry must retire the older ambiguous attempt", len(store.pendingFindingRecords), store.findingRecords[updated.ID])
	}
}

func TestJournalStateStoreUpdatedPendingRetryPreservesLatestMetadataAfterReentrantReplay(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("first reentrant finding append unavailable")
	journalStore := &reentrantUpdatedFindingJournal{MemoryJournal: journal.NewMemory(), firstErr: appendErr}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "updated-pending-reentrant-replay"}
	first := Finding{
		ID: "updated-pending-reentrant-replay", PassID: "updated-reentrant-first", Scope: scope,
		Severity: FindingBlocking, Blocking: true, Message: "first context", CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, first); !errors.Is(err, appendErr) {
		t.Fatalf("record first finding err=%v, want %v", err, appendErr)
	}
	updated := first
	updated.PassID = "updated-reentrant-second"
	updated.Message = "latest operator context"
	updated.CreatedAt = time.Unix(2, 0)
	journalStore.appendHook = func() {
		_, _ = store.LoadOpenFindings(ctx, scope)
	}
	if err := store.RecordFinding(ctx, updated); err != nil {
		t.Fatalf("record updated finding retry: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load updated finding: %v", err)
	}
	if len(open) != 1 || open[0].PassID != updated.PassID || open[0].CreatedAt != updated.CreatedAt || open[0].Message != updated.Message {
		t.Fatalf("open findings=%+v, want latest metadata %+v", open, updated)
	}
}

func TestJournalStateStoreOlderPendingRetryCannotOverwriteNewerMetadata(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("initial metadata append unavailable")
	journalStore := &staleOpaqueFindingJournal{
		MemoryJournal: journal.NewMemory(),
		appendErr:     appendErr,
		retryReady:    make(chan int32, 2),
		allowFirst:    make(chan struct{}),
		allowSecond:   make(chan struct{}),
	}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "pending-metadata-order"}
	first := Finding{
		ID: "pending-metadata-order", PassID: "metadata-initial", Scope: scope,
		Severity: FindingBlocking, Blocking: true, Message: "initial", CreatedAt: time.Unix(1, 0),
	}
	if err := store.RecordFinding(ctx, first); !errors.Is(err, appendErr) {
		t.Fatalf("initial record err=%v, want %v", err, appendErr)
	}
	newer := first
	newer.PassID = "metadata-newer"
	newer.Message = "newer context"
	newer.CreatedAt = time.Unix(3, 0)
	newerDone := make(chan error, 1)
	go func() { newerDone <- store.RecordFinding(ctx, newer) }()
	select {
	case call := <-journalStore.retryReady:
		if call != 2 {
			t.Fatalf("newer retry append call=%d, want 2", call)
		}
	case <-time.After(time.Second):
		t.Fatal("newer retry did not reach append barrier")
	}

	older := first
	older.PassID = "metadata-older"
	older.Message = "older context"
	older.CreatedAt = time.Unix(2, 0)
	olderDone := make(chan error, 1)
	go func() { olderDone <- store.RecordFinding(ctx, older) }()
	select {
	case call := <-journalStore.retryReady:
		if call != 3 {
			t.Fatalf("older retry append call=%d, want 3", call)
		}
	case <-time.After(time.Second):
		close(journalStore.allowFirst)
		t.Fatal("older retry did not reach append barrier")
	}
	close(journalStore.allowFirst)
	if err := <-newerDone; err != nil {
		close(journalStore.allowSecond)
		t.Fatalf("newer retry: %v", err)
	}
	close(journalStore.allowSecond)
	if err := <-olderDone; err != nil {
		t.Fatalf("older retry: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load finding metadata: %v", err)
	}
	if len(open) != 1 || open[0].PassID != newer.PassID || !open[0].CreatedAt.Equal(newer.CreatedAt) || open[0].Message != newer.Message {
		t.Fatalf("open findings=%+v, older completion overwrote newer metadata %+v", open, newer)
	}
}

func TestReconcilerBatchFindingResolutionUsesConstantJournalScans(t *testing.T) {
	ctx := context.Background()
	journalStore := &countingFindingRecordsJournal{MemoryJournal: journal.NewMemory()}
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "batch-finding-resolution"}
	const findingCount = 32
	resolutions := make([]FindingResolution, 0, findingCount)
	for i := 0; i < findingCount; i++ {
		findingID := fmt.Sprintf("batch-resolution-%d", i)
		at := time.Unix(int64(i+1), 0)
		if err := store.RecordFinding(ctx, Finding{
			ID: findingID, PassID: model.ReconciliationID(findingID), Scope: scope,
			Severity: FindingBlocking, Blocking: true, CreatedAt: at,
		}); err != nil {
			t.Fatalf("record finding %d: %v", i, err)
		}
		resolutions = append(resolutions, FindingResolution{
			FindingID: findingID, PassID: model.ReconciliationID("resolve-" + findingID), ResolvedAt: at.Add(time.Nanosecond),
		})
	}
	journalStore.recordsCalls.Store(0)
	r := &Reconciler{state: store}
	if err := r.applyFindingResolutions(ctx, resolutions); err != nil {
		t.Fatalf("apply batch finding resolutions: %v", err)
	}
	if calls := journalStore.recordsCalls.Load(); calls > 3 {
		t.Fatalf("Records calls=%d for %d resolutions, want a constant number of journal scans", calls, findingCount)
	}
}

type reentrantFindingJournal struct {
	*journal.MemoryJournal
	appendTriggered atomic.Bool
	recordsOnce     sync.Once
	appendHook      func()
	recordsHook     func()
}

func (j *reentrantFindingJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if err := j.MemoryJournal.AppendReport(ctx, report); err != nil {
		return err
	}
	if j.appendHook != nil && j.appendTriggered.CompareAndSwap(false, true) {
		j.appendHook()
	}
	return nil
}

type destabilizingFindingJournal struct {
	*journal.MemoryJournal
	appendTriggered atomic.Bool
	appendHook      func()
	recordsHook     func()
}

func (j *destabilizingFindingJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if err := j.MemoryJournal.AppendReport(ctx, report); err != nil {
		return err
	}
	if j.appendHook != nil && j.appendTriggered.CompareAndSwap(false, true) {
		j.appendHook()
	}
	return nil
}

func (j *destabilizingFindingJournal) Records() []journal.Record {
	records := j.MemoryJournal.Records()
	if j.recordsHook != nil {
		j.recordsHook()
	}
	return records
}

type blockingFindingReplayJournal struct {
	*journal.MemoryJournal
	blockNext      atomic.Bool
	failNextAppend atomic.Bool
	started        chan struct{}
	release        chan struct{}
	appendErr      error
}

func (j *blockingFindingReplayJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if err := j.MemoryJournal.AppendReport(ctx, report); err != nil {
		return err
	}
	if j.failNextAppend.CompareAndSwap(true, false) {
		return j.appendErr
	}
	return nil
}

func (j *blockingFindingReplayJournal) Records() []journal.Record {
	records := j.MemoryJournal.Records()
	if j.blockNext.CompareAndSwap(true, false) {
		close(j.started)
		<-j.release
	}
	return records
}

type coordinatedAmbiguousFindingJournal struct {
	*journal.MemoryJournal
	blockAppend    atomic.Bool
	blockRecords   atomic.Bool
	appendStarted  chan struct{}
	allowAppend    chan struct{}
	recordsStarted chan struct{}
	allowRecords   chan struct{}
	appendErr      error
}

func (j *coordinatedAmbiguousFindingJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if j.blockAppend.CompareAndSwap(true, false) {
		close(j.appendStarted)
		select {
		case <-j.allowAppend:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := j.MemoryJournal.AppendReport(ctx, report); err != nil {
		return err
	}
	return j.appendErr
}

func (j *coordinatedAmbiguousFindingJournal) Records() []journal.Record {
	records := j.MemoryJournal.Records()
	if j.blockRecords.CompareAndSwap(true, false) {
		close(j.recordsStarted)
		<-j.allowRecords
	}
	return records
}

type twoAppendBarrierJournal struct {
	*journal.MemoryJournal
	entered          atomic.Int32
	resolutionCalls  atomic.Int32
	ready            chan struct{}
	release          chan struct{}
	failResolutionAt int32
	resolutionErr    error
}

func (j *twoAppendBarrierJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	entered := j.entered.Add(1)
	if entered <= 2 {
		if entered == 2 {
			close(j.ready)
		}
		select {
		case <-j.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if _, resolution := decodeFindingResolutionRecord(report.Payload); resolution {
		call := j.resolutionCalls.Add(1)
		if j.failResolutionAt > 0 && call == j.failResolutionAt {
			return j.resolutionErr
		}
	}
	return j.MemoryJournal.AppendReport(ctx, report)
}

type failingFindingEntropyReader struct {
	err error
}

func (r failingFindingEntropyReader) Read([]byte) (int, error) {
	return 0, r.err
}

type failingFindingAppendJournal struct {
	*journal.MemoryJournal
	mu        sync.Mutex
	err       error
	recordIDs []string
}

func (j *failingFindingAppendJournal) AppendReport(_ context.Context, report journal.ReportRecord) error {
	j.mu.Lock()
	j.recordIDs = append(j.recordIDs, report.RecordID)
	j.mu.Unlock()
	return j.err
}

type appendThenErrorFindingJournal struct {
	*journal.MemoryJournal
	mu    sync.Mutex
	err   error
	calls int
}

type toggleFindingAppendJournal struct {
	*journal.MemoryJournal
	err error
}

type staleOpaqueFindingJournal struct {
	*journal.MemoryJournal
	appendErr   error
	openCalls   atomic.Int32
	retryReady  chan int32
	allowFirst  chan struct{}
	allowSecond chan struct{}
}

type reentrantUpdatedFindingJournal struct {
	*journal.MemoryJournal
	appendCalls atomic.Int32
	firstErr    error
	appendHook  func()
}

func (j *reentrantUpdatedFindingJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if j.appendCalls.Add(1) == 1 {
		return j.firstErr
	}
	if err := j.MemoryJournal.AppendReport(ctx, report); err != nil {
		return err
	}
	if j.appendHook != nil {
		j.appendHook()
	}
	return nil
}

func (j *staleOpaqueFindingJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if _, resolution := decodeFindingResolutionRecord(report.Payload); resolution {
		return j.MemoryJournal.AppendReport(ctx, report)
	}
	call := j.openCalls.Add(1)
	switch call {
	case 1:
		return j.appendErr
	case 2:
		j.retryReady <- call
		select {
		case <-j.allowFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	case 3:
		j.retryReady <- call
		select {
		case <-j.allowSecond:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return j.MemoryJournal.AppendReport(ctx, report)
}

func (j *toggleFindingAppendJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if j.err != nil {
		return j.err
	}
	return j.MemoryJournal.AppendReport(ctx, report)
}

type countingFindingRecordsJournal struct {
	*journal.MemoryJournal
	recordsCalls atomic.Int64
}

func (j *countingFindingRecordsJournal) Records() []journal.Record {
	j.recordsCalls.Add(1)
	return j.MemoryJournal.Records()
}

func (j *appendThenErrorFindingJournal) AppendReport(ctx context.Context, report journal.ReportRecord) error {
	if err := j.MemoryJournal.AppendReport(ctx, report); err != nil {
		return err
	}
	j.mu.Lock()
	j.calls++
	j.mu.Unlock()
	return j.err
}

func (j *reentrantFindingJournal) Records() []journal.Record {
	records := j.MemoryJournal.Records()
	if j.recordsHook != nil {
		j.recordsOnce.Do(j.recordsHook)
	}
	return records
}

func assertFindingOperationCompletes(t *testing.T, operation func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- operation() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("finding operation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("finding operation deadlocked on a reentrant journal callback")
	}
}

func TestPositionMismatchResolutionPersistsAcrossStateStoreRebuild(t *testing.T) {
	ctx := context.Background()
	j := journal.NewMemoryWithRetention(1)
	store := NewJournalStateStore(j)
	r, exec := positionResolutionScenario(t, store)

	first, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("mismatch pass: %v", err)
	}
	if !hasFindingCode(first.Findings, "POSITION_MISMATCH") || first.ActivationVerdict().Safe {
		t.Fatalf("first report=%+v, want blocking mismatch", first)
	}
	findingID := findingIDByCode(t, first.Findings, "POSITION_MISMATCH")

	exec.mass = positionMass(t, "acct", d("1"), time.Unix(301, 0))
	second, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("clean pass: %v", err)
	}
	if hasFindingCode(second.Findings, "POSITION_MISMATCH") || !second.ActivationVerdict().Safe {
		t.Fatalf("second report=%+v, complete matching position report must resolve old mismatch", second)
	}

	rebuilt := NewJournalStateStore(j)
	open, err := rebuilt.LoadOpenFindings(ctx, ScopeKey{Venue: "T", AccountID: "acct"})
	if err != nil {
		t.Fatalf("load rebuilt findings: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("rebuilt open findings=%+v, resolved mismatch resurrected", open)
	}

	for i := 0; i < 8; i++ {
		if err := j.AppendReport(ctx, journal.ReportRecord{RecordID: fmt.Sprintf("resolution-noise-%d", i)}); err != nil {
			t.Fatalf("append retention noise %d: %v", i, err)
		}
	}
	for _, record := range j.Records() {
		if finding, ok := decodeFindingRecord(record.Payload); ok && finding.ID == findingID {
			t.Fatalf("resolved blocking record %q remained retention-protected: %+v", findingID, j.Records())
		}
	}
}

func TestReconcilerResolutionReportRetainsReentrantFindingReopen(t *testing.T) {
	ctx := context.Background()
	journalStore := &reentrantFindingJournal{MemoryJournal: journal.NewMemory()}
	store := NewJournalStateStore(journalStore)
	r, execClient := positionResolutionScenario(t, store)

	first, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("mismatch pass: %v", err)
	}
	var reopened Finding
	for _, finding := range first.Findings {
		if finding.Code == "POSITION_MISMATCH" {
			reopened = finding
			break
		}
	}
	if reopened.ID == "" {
		t.Fatalf("first findings=%+v, want position mismatch", first.Findings)
	}
	reopened.PassID = "reentrant-reopen-pass"
	reopened.CreatedAt = time.Unix(302, 0)
	reopened.Message = "condition reopened while its prior physical record was resolving"
	var reopenErr error
	journalStore.appendHook = func() {
		reopenErr = store.RecordFinding(ctx, reopened)
	}

	execClient.mass = positionMass(t, "acct", d("1"), time.Unix(301, 0))
	second, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("clean pass with reentrant reopen: %v", err)
	}
	if reopenErr != nil {
		t.Fatalf("reentrant reopen: %v", reopenErr)
	}
	open, err := store.LoadOpenFindings(ctx, reopened.Scope)
	if err != nil {
		t.Fatalf("load reopened finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != reopened.ID {
		t.Fatalf("open findings=%+v, want the reentrant reopen durable", open)
	}
	if !hasFindingCode(second.Findings, "POSITION_MISMATCH") || second.ActivationVerdict().Safe {
		t.Fatalf("second report=%+v, durable reentrant reopen must remain activation-blocking", second)
	}
}

type staticOpenFindingStore struct {
	noopStateStore
	open []Finding
}

func (s *staticOpenFindingStore) LoadOpenFindings(context.Context, ScopeKey) ([]Finding, error) {
	return append([]Finding(nil), s.open...), nil
}

func TestRefreshResolvedFindingsMergesLargeOpenSetWithoutDuplicates(t *testing.T) {
	const findingCount = 4096
	scope := ScopeKey{Venue: "T", AccountID: "bulk-refresh"}
	report := Report{Findings: make([]Finding, 0, findingCount)}
	open := make([]Finding, 0, findingCount)
	for i := 0; i < findingCount; i++ {
		finding := Finding{
			ID: fmt.Sprintf("bulk-finding-%04d", i), PassID: "before-refresh", Scope: scope,
			Severity: FindingBlocking, Blocking: true, CreatedAt: time.Unix(int64(i+1), 0),
		}
		report.Findings = append(report.Findings, finding)
		open = append(open, finding)
	}
	open[0].PassID = "reentrant-reopen"
	open[0].Message = "reopened target"
	open[1].PassID = "concurrent-metadata-update"
	open[1].Message = "newest unrelated metadata"
	store := &staticOpenFindingStore{open: open}
	r := &Reconciler{state: store}

	if err := r.refreshResolvedFindings(context.Background(), &report, scope, []FindingResolution{{FindingID: open[0].ID}}); err != nil {
		t.Fatalf("refresh resolved findings: %v", err)
	}
	if len(report.Findings) != findingCount {
		t.Fatalf("findings=%d, want %d unique open findings", len(report.Findings), findingCount)
	}
	seen := make(map[string]Finding, len(report.Findings))
	for _, finding := range report.Findings {
		if _, duplicate := seen[finding.ID]; duplicate {
			t.Fatalf("duplicate finding ID %q after refresh", finding.ID)
		}
		seen[finding.ID] = finding
	}
	if got := seen[open[0].ID]; got.PassID != open[0].PassID || got.Message != open[0].Message {
		t.Fatalf("reopened target=%+v, want latest %+v", got, open[0])
	}
	if got := seen[open[1].ID]; got.PassID != open[1].PassID || got.Message != open[1].Message {
		t.Fatalf("concurrent metadata=%+v, want latest %+v", got, open[1])
	}
}

func TestPositionMismatchResolutionWaitsForCursorCommit(t *testing.T) {
	ctx := context.Background()
	j := journal.NewMemory()
	commitErr := errors.New("cursor commit unavailable")
	store := &failOnceCursorStore{
		JournalStateStore: NewJournalStateStore(j),
		err:               commitErr,
	}
	r, exec := positionResolutionScenario(t, store)

	first, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("mismatch pass: %v", err)
	}
	if !hasFindingCode(first.Findings, "POSITION_MISMATCH") {
		t.Fatalf("first report=%+v, want blocking mismatch", first)
	}

	exec.mass = positionMass(t, "acct", d("1"), time.Unix(302, 0))
	if _, err := r.Run(ctx); !errors.Is(err, commitErr) {
		t.Fatalf("clean pass error=%v, want cursor commit error %v", err, commitErr)
	}
	open, err := NewJournalStateStore(j).LoadOpenFindings(ctx, ScopeKey{Venue: "T", AccountID: "acct"})
	if err != nil {
		t.Fatalf("load findings after failed commit: %v", err)
	}
	if len(open) != 1 || open[0].Code != "POSITION_MISMATCH" {
		t.Fatalf("open findings after failed commit=%+v, mismatch was resolved before the pass committed", open)
	}

	third, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("retry clean pass: %v", err)
	}
	if hasFindingCode(third.Findings, "POSITION_MISMATCH") || !third.ActivationVerdict().Safe {
		t.Fatalf("retry report=%+v, successful commit must resolve the mismatch", third)
	}
	open, err = NewJournalStateStore(j).LoadOpenFindings(ctx, ScopeKey{Venue: "T", AccountID: "acct"})
	if err != nil {
		t.Fatalf("load findings after retry: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open findings after successful retry=%+v, mismatch was not resolved", open)
	}
}

func TestPositionMismatchResolutionRequiresCompleteCoveredPass(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(*snapshotExec)
	}{
		{
			name: "partial pass",
			mutate: func(exec *snapshotExec) {
				partial := positionMass(t, "acct", d("1"), time.Unix(311, 0))
				exec.mass = nil
				exec.massFn = func(query model.MassStatusQuery) *model.ExecutionMassStatus {
					mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoveragePartial)
					mass.PositionReports = partial.Clone().PositionReports
					return mass
				}
			},
		},
		{
			name: "position stream not covered",
			mutate: func(exec *snapshotExec) {
				exec.positions = false
				exec.mass = model.NewExecutionMassStatus("T", "acct", time.Unix(312, 0))
			},
		},
		{
			name: "fetch error",
			mutate: func(exec *snapshotExec) {
				exec.massErr = errors.New("mass status unavailable")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := journal.NewMemory()
			store := NewJournalStateStore(j)
			r, exec := positionResolutionScenario(t, store)
			if first, err := r.Run(ctx); err != nil || first.ActivationVerdict().Safe {
				t.Fatalf("seed mismatch report=%+v err=%v", first, err)
			}

			tc.mutate(exec)
			report, runErr := r.Run(ctx)
			if tc.name == "fetch error" {
				if runErr == nil {
					t.Fatal("fetch error pass unexpectedly succeeded")
				}
			} else {
				if runErr != nil {
					t.Fatalf("incomplete pass: %v", runErr)
				}
				if !hasFindingCode(report.Findings, "POSITION_MISMATCH") || report.ActivationVerdict().Safe {
					t.Fatalf("report=%+v, incomplete/uncovered pass cleared mismatch", report)
				}
			}

			open, err := NewJournalStateStore(j).LoadOpenFindings(ctx, ScopeKey{Venue: "T", AccountID: "acct"})
			if err != nil {
				t.Fatalf("load findings: %v", err)
			}
			if len(open) != 1 || open[0].Code != "POSITION_MISMATCH" {
				t.Fatalf("open findings=%+v, mismatch must remain unresolved", open)
			}
		})
	}
}

func TestOrderProgressFindingResolvesOnlyAfterCompleteFillEvidence(t *testing.T) {
	ctx := context.Background()
	j := journal.NewMemory()
	store := NewJournalStateStore(j)
	c := cache.New()
	known := order("resolved-progress", btc, "2", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	c.UpsertOrder(known)

	firstAt := time.Unix(320, 0)
	snapshot := known
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = d("1")
	snapshot.UpdatedAt = firstAt
	firstMass := model.NewExecutionMassStatus("T", "acct", firstAt)
	if err := firstMass.AddOrderReport(model.OrderStatusReport{Venue: "T", AccountID: "acct", Order: snapshot, ReportedAt: firstAt}); err != nil {
		t.Fatalf("add first order report: %v", err)
	}
	exec := &snapshotExec{mass: firstMass, fillHistory: true}
	var applied []model.Fill
	r := New(nil, exec, c).
		WithAccountID("acct").
		WithStateStore(store).
		WithFillApplier(func(fill model.Fill, _ contract.EventMeta) FillApplyResult {
			applied = append(applied, fill)
			return FillApplyApplied
		})

	first, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if !hasFindingCode(first.Findings, "ORDER_PROGRESS_WITHOUT_FILL") || first.ActivationVerdict().Safe {
		t.Fatalf("first report=%+v, want blocking missing fill", first)
	}

	secondAt := firstAt.Add(time.Second)
	secondMass := model.NewExecutionMassStatus("T", "acct", secondAt)
	snapshot.UpdatedAt = secondAt
	if err := secondMass.AddOrderReport(model.OrderStatusReport{Venue: "T", AccountID: "acct", Order: snapshot, ReportedAt: secondAt}); err != nil {
		t.Fatalf("add second order report: %v", err)
	}
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: known.Request.ClientID,
		VenueOrderID: known.VenueOrderID, TradeID: "resolved-progress-fill",
		Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: secondAt,
	}
	if err := secondMass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: secondAt}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	exec.mass = secondMass

	second, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(applied) != 1 || !applied[0].Quantity.Equal(d("1")) {
		t.Fatalf("applied fills=%+v, want complete missing-fill evidence", applied)
	}
	if hasFindingCode(second.Findings, "ORDER_PROGRESS_WITHOUT_FILL") || !second.ActivationVerdict().Safe {
		t.Fatalf("second report=%+v, applied complete fill evidence must resolve old progress gap", second)
	}
	open, err := NewJournalStateStore(j).LoadOpenFindings(ctx, ScopeKey{Venue: "T", AccountID: "acct"})
	if err != nil {
		t.Fatalf("load rebuilt findings: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("rebuilt open findings=%+v, resolved order gap resurrected", open)
	}
}

func TestExplicitFindingResolutionSupportsOperatorFindingsAndFileReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "finding-resolution.journal")
	j, err := journal.OpenFileWithRetention(path, journal.FileOptions{}, 1)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	store := NewJournalStateStore(j)
	scope := ScopeKey{Venue: "T", AccountID: "acct"}
	at := time.Unix(330, 0)
	finding := New(nil, nil, cache.New()).finding(
		PassHeader{PassID: PassID(scope, at), Scope: scope},
		StreamFills,
		FindingBlocking,
		"FILL_WITHOUT_ORDER",
		"operator-reviewed unmatched fill",
		true,
	)
	finding.CreatedAt = at
	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("record finding: %v", err)
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID:  finding.ID,
		PassID:     PassID(scope, at.Add(time.Second)),
		ResolvedAt: at.Add(time.Second),
		Reason:     "operator confirmed external fill handling",
	}); err != nil {
		t.Fatalf("resolve finding: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := j.AppendReport(ctx, journal.ReportRecord{RecordID: fmt.Sprintf("file-resolution-noise-%d", i)}); err != nil {
			t.Fatalf("append noise %d: %v", i, err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	reopened, err := journal.OpenFileWithRetention(path, journal.FileOptions{}, 1)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	reopenedStore := NewJournalStateStore(reopened)
	open, err := reopenedStore.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load reopened findings: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("reopened findings=%+v, explicit resolution tombstone was lost", open)
	}
	if err := reopenedStore.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("reopen exact finding after file-journal restart: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened journal: %v", err)
	}

	reopenedAgain, err := journal.OpenFileWithRetention(path, journal.FileOptions{}, 1)
	if err != nil {
		t.Fatalf("reopen journal after exact finding reopen: %v", err)
	}
	defer reopenedAgain.Close()
	open, err = NewJournalStateStore(reopenedAgain).LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load exact reopened finding: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("reopened findings=%+v, exact finding reopen was not durable", open)
	}
}

func TestSeverityOnlyBlockingFindingResolvesWithoutRetentionLeak(t *testing.T) {
	ctx := context.Background()
	j := journal.NewMemoryWithRetention(1)
	store := NewJournalStateStore(j)
	scope := ScopeKey{Venue: "T", AccountID: "acct"}
	at := time.Unix(340, 0)
	finding := Finding{
		ID:        "severity-only-blocking",
		PassID:    PassID(scope, at),
		Scope:     scope,
		Stream:    StreamFills,
		Severity:  FindingBlocking,
		Code:      "OPERATOR_REVIEW",
		Message:   "severity marks this finding blocking",
		CreatedAt: at,
	}
	if err := store.RecordFinding(ctx, finding); err != nil {
		t.Fatalf("record finding: %v", err)
	}
	repeated := finding
	repeated.PassID = PassID(scope, at.Add(time.Second))
	repeated.CreatedAt = at.Add(time.Second)
	if err := store.RecordFinding(ctx, repeated); err != nil {
		t.Fatalf("record repeated finding: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load open findings: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("open findings=%+v, severity-only blocking finding must remain open once", open)
	}
	if err := store.ResolveFinding(ctx, FindingResolution{
		FindingID:  finding.ID,
		PassID:     PassID(scope, at.Add(2*time.Second)),
		ResolvedAt: at.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("resolve finding: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := j.AppendReport(ctx, journal.ReportRecord{RecordID: fmt.Sprintf("severity-resolution-noise-%d", i)}); err != nil {
			t.Fatalf("append noise %d: %v", i, err)
		}
	}
	for _, record := range j.Records() {
		if retained, ok := decodeFindingRecord(record.Payload); ok && retained.ID == finding.ID {
			t.Fatalf("resolved severity-only finding remained protected: %+v", j.Records())
		}
	}
}

func positionResolutionScenario(t *testing.T, store StateStore) (*Reconciler, *snapshotExec) {
	t.Helper()
	c := cache.New()
	c.UpsertPosition(model.Position{AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	exec := &snapshotExec{mass: positionMass(t, "acct", d("2"), time.Unix(300, 0)), positions: true}
	return New(nil, exec, c).WithAccountID("acct").WithStateStore(store), exec
}

func positionMass(t *testing.T, accountID string, quantity decimal.Decimal, at time.Time) *model.ExecutionMassStatus {
	t.Helper()
	mass := model.NewExecutionMassStatus("T", accountID, at)
	if err := mass.AddPositionReport(model.PositionReport{
		Venue: "T", AccountID: accountID,
		Position:   model.Position{AccountID: accountID, InstrumentID: btc, Side: enums.PosNet, Quantity: quantity, UpdatedAt: at},
		ReportedAt: at,
	}); err != nil {
		t.Fatalf("add position report: %v", err)
	}
	return mass
}

func findingIDByCode(t *testing.T, findings []Finding, code string) string {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return finding.ID
		}
	}
	t.Fatalf("finding code %q missing from %+v", code, findings)
	return ""
}
