package exec_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

type blockingIntentJournal struct {
	journal.Store
	entered chan struct{}
	release chan struct{}
	blocks  int32
	calls   atomic.Int32
}

type serializedExec struct {
	*runtimetest.FakeExec
	mu sync.Mutex
}

func (e *serializedExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.FakeExec.Submit(ctx, req)
}

func (j *blockingIntentJournal) AppendCommandIntent(ctx context.Context, intent journal.CommandIntent) error {
	call := j.calls.Add(1)
	if call <= j.blocks {
		j.entered <- struct{}{}
		select {
		case <-j.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return j.Store.AppendCommandIntent(ctx, intent)
}

func TestConcurrentSubmitsReservePositionBeforePendingNewCacheInsert(t *testing.T) {
	fake := &serializedExec{FakeExec: runtimetest.NewFakeExec()}
	engine, cached, _ := testEngine(fake)
	store := &blockingIntentJournal{
		Store:   journal.NewMemory(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		blocks:  1,
	}
	engine.WithJournal(store).WithRisk(risk.New(risk.Limits{MaxPositionQty: decimal.NewFromInt(1)}, cached), nil)
	firstDone := make(chan error, 1)
	go func() {
		_, err := engine.Submit(context.Background(), testReq("position-first"))
		firstDone <- err
	}()
	<-store.entered

	if _, err := engine.Submit(context.Background(), testReq("position-second")); !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("second order crossed the position limit before first reached PendingNew: %v", err)
	}
	close(store.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first submit: %v", err)
	}
}

func TestSubmitCacheReservationRejectsClientIDInsertedWhileJournalBlocks(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	var venueCalls atomic.Int32
	fake.OnSubmit(func(model.OrderRequest) { venueCalls.Add(1) })
	engine, cached, _ := testEngine(fake)
	memory := journal.NewMemory()
	store := &blockingIntentJournal{
		Store:   memory,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		blocks:  1,
	}
	engine.WithJournal(store)
	type submitResult struct {
		order *model.Order
		err   error
	}
	done := make(chan submitResult, 1)
	req := testReq("cache-race-client")
	go func() {
		order, err := engine.Submit(context.Background(), req)
		done <- submitResult{order: order, err: err}
	}()
	<-store.entered

	external := model.Order{
		Request: model.OrderRequest{
			AccountID: "test", InstrumentID: req.InstrumentID, ClientID: req.ClientID,
			Side: req.Side, Type: req.Type, TIF: req.TIF, Quantity: req.Quantity, Price: req.Price,
		},
		VenueOrderID: "external-before-handoff",
		Status:       enums.StatusNew,
	}
	if err := cached.UpsertOrderChecked(external); err != nil {
		t.Fatalf("insert concurrent external order: %v", err)
	}
	close(store.release)
	result := <-done

	if result.order != nil || !errors.Is(result.err, exec.ErrDuplicateClientID) || !errors.Is(result.err, cache.ErrOrderClientIDExists) {
		t.Fatalf("submit order=%+v err=%v, want atomic cache reservation rejection", result.order, result.err)
	}
	if got := venueCalls.Load(); got != 0 {
		t.Fatalf("venue submit calls=%d, want 0", got)
	}
	if got := engine.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d after local denial, want 0", got)
	}
	stored, ok := cached.OrderByClientIDForAccount("test", req.ClientID)
	if !ok || stored.VenueOrderID != external.VenueOrderID || stored.Status != enums.StatusNew {
		t.Fatalf("cached order=(%+v,%v), want concurrent external order unchanged", stored, ok)
	}
	if records := memory.Records(); len(records) != 2 || records[0].Type != journal.RecordCommandIntent || records[1].Type != journal.RecordCommandResult {
		t.Fatalf("journal records=%+v, want durable intent plus local-denied result", records)
	}
	assertResultOutcome(t, memory, exec.OutcomeLocalDenied)
}

func TestInFlightJournalKeepsAllSubmissionsStillBeforePendingNew(t *testing.T) {
	fake := &serializedExec{FakeExec: runtimetest.NewFakeExec()}
	engine, cached, _ := testEngine(fake)
	store := &blockingIntentJournal{
		Store:   journal.NewMemory(),
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
		blocks:  2,
	}
	engine.WithJournal(store).WithRisk(risk.New(risk.Limits{}, cached).WithClientIDRetentionLimit(1), nil)
	done := make(chan error, 2)
	for _, clientID := range []string{"retained-first", "retained-second"} {
		clientID := clientID
		go func() {
			_, err := engine.Submit(context.Background(), testReq(clientID))
			done <- err
		}()
		<-store.entered
	}

	open := engine.OpenInFlight()
	openClientIDs := make(map[string]struct{}, len(open))
	for _, entry := range open {
		openClientIDs[entry.Intent.ClientID] = struct{}{}
	}
	for _, clientID := range []string{"retained-first", "retained-second"} {
		if _, ok := openClientIDs[clientID]; !ok {
			t.Fatalf("in-flight client IDs=%v, missing %q before PendingNew", openClientIDs, clientID)
		}
	}
	if _, err := engine.Submit(context.Background(), testReq("retained-first")); !errors.Is(err, exec.ErrDuplicateClientID) {
		t.Fatalf("earlier in-flight client ID was not reserved: %v", err)
	}
	close(store.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("blocked submit: %v", err)
		}
	}
}
