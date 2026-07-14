package exec_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

type execLease struct {
	releases atomic.Int32
}

func (l *execLease) Release() { l.releases.Add(1) }

type contextRisk struct {
	legacyCalls  atomic.Int32
	contextCalls atomic.Int32
	lease        contract.PreTradeLease
	err          error
}

func (r *contextRisk) Check(model.OrderRequest, *model.Instrument) error {
	r.legacyCalls.Add(1)
	return errors.New("legacy Check must not run")
}

func (r *contextRisk) CheckContext(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
	r.contextCalls.Add(1)
	return r.lease, r.err
}

type legacyRisk struct {
	calls atomic.Int32
}

func (r *legacyRisk) Check(model.OrderRequest, *model.Instrument) error {
	r.calls.Add(1)
	return nil
}

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
	e, c, _ := testEngine(fake)
	store := &blockingIntentJournal{
		Store:   journal.NewMemory(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		blocks:  1,
	}
	e.WithJournal(store).WithRisk(risk.New(risk.Limits{MaxPositionQty: decimal.NewFromInt(1)}, c), nil)
	firstDone := make(chan error, 1)
	go func() {
		_, err := e.Submit(context.Background(), testReq("position-first"))
		firstDone <- err
	}()
	<-store.entered

	if _, err := e.Submit(context.Background(), testReq("position-second")); !errors.Is(err, risk.ErrRiskRejected) {
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
	e, c, _ := testEngine(fake)
	memory := journal.NewMemory()
	store := &blockingIntentJournal{
		Store:   memory,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		blocks:  1,
	}
	e.WithJournal(store)
	type submitResult struct {
		order *model.Order
		err   error
	}
	done := make(chan submitResult, 1)
	req := testReq("cache-race-client")
	go func() {
		order, err := e.Submit(context.Background(), req)
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
	if err := c.UpsertOrderChecked(external); err != nil {
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
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d after local denial, want 0", got)
	}
	cached, ok := c.OrderByClientIDForAccount("test", req.ClientID)
	if !ok || cached.VenueOrderID != external.VenueOrderID || cached.Status != enums.StatusNew {
		t.Fatalf("cached order=(%+v,%v), want concurrent external order unchanged", cached, ok)
	}
	if records := memory.Records(); len(records) != 2 || records[0].Type != journal.RecordCommandIntent || records[1].Type != journal.RecordCommandResult {
		t.Fatalf("journal records=%+v, want durable intent plus local-denied result", records)
	}
	assertResultOutcome(t, memory, exec.OutcomeLocalDenied)
}

func TestInFlightJournalKeepsAllSubmissionsStillBeforePendingNew(t *testing.T) {
	fake := &serializedExec{FakeExec: runtimetest.NewFakeExec()}
	e, c, _ := testEngine(fake)
	store := &blockingIntentJournal{
		Store:   journal.NewMemory(),
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
		blocks:  2,
	}
	e.WithJournal(store).WithRisk(risk.New(risk.Limits{}, c).WithClientIDRetentionLimit(1), nil)
	done := make(chan error, 2)
	for _, clientID := range []string{"retained-first", "retained-second"} {
		clientID := clientID
		go func() {
			_, err := e.Submit(context.Background(), testReq(clientID))
			done <- err
		}()
		<-store.entered
	}

	open := e.OpenInFlight()
	openClientIDs := make(map[string]struct{}, len(open))
	for _, entry := range open {
		openClientIDs[entry.Intent.ClientID] = struct{}{}
	}
	for _, clientID := range []string{"retained-first", "retained-second"} {
		if _, ok := openClientIDs[clientID]; !ok {
			t.Fatalf("in-flight client IDs=%v, missing %q before PendingNew", openClientIDs, clientID)
		}
	}
	if _, err := e.Submit(context.Background(), testReq("retained-first")); !errors.Is(err, exec.ErrDuplicateClientID) {
		t.Fatalf("earlier in-flight client ID was not reserved: %v", err)
	}
	close(store.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("blocked submit: %v", err)
		}
	}
}

type cancelingContextRisk struct {
	cancel context.CancelFunc
	lease  contract.PreTradeLease
}

type preparedPathExec struct {
	*runtimetest.FakeExec
	regularCalls  atomic.Int32
	preparedCalls atomic.Int32
	preparedErr   error
}

func (c *preparedPathExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	c.regularCalls.Add(1)
	return c.FakeExec.Submit(ctx, req)
}

func (c *preparedPathExec) SubmitPrepared(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	c.preparedCalls.Add(1)
	if c.preparedErr != nil {
		return nil, c.preparedErr
	}
	return c.FakeExec.Submit(ctx, req)
}

func (r *cancelingContextRisk) Check(model.OrderRequest, *model.Instrument) error {
	return errors.New("legacy Check must not run")
}

func (r *cancelingContextRisk) CheckContext(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
	r.cancel()
	return r.lease, nil
}

func TestSubmitPrefersContextRiskAndOwnsLeaseUntilReturn(t *testing.T) {
	fake := &preparedPathExec{FakeExec: runtimetest.NewFakeExec()}
	e, _, _ := testEngine(fake)
	lease := &execLease{}
	risk := &contextRisk{lease: lease}
	e.WithRisk(risk, nil)
	fake.FakeExec.OnSubmit(func(model.OrderRequest) {
		if got := lease.releases.Load(); got != 0 {
			t.Fatalf("lease released before execution submit: %d", got)
		}
	})

	if _, err := e.Submit(context.Background(), testReq("context-risk")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := risk.contextCalls.Load(); got != 1 {
		t.Fatalf("context risk calls=%d, want 1", got)
	}
	if got := risk.legacyCalls.Load(); got != 0 {
		t.Fatalf("legacy risk calls=%d, want 0", got)
	}
	if got := lease.releases.Load(); got != 1 {
		t.Fatalf("lease releases=%d, want 1 after submit returns", got)
	}
}

func TestSubmitFailsBeforeJournalWhenLeaseClientCannotConsumePrepared(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	lease := &execLease{}
	e.WithRisk(&contextRisk{lease: lease}, nil)
	called := atomic.Bool{}
	fake.OnSubmit(func(model.OrderRequest) { called.Store(true) })
	req := testReq("missing-prepared-client")

	if _, err := e.Submit(context.Background(), req); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("submit err=%v, want ErrNotSupported", err)
	}
	if called.Load() {
		t.Fatal("regular submit called for a pre-trade lease")
	}
	if got := lease.releases.Load(); got != 1 {
		t.Fatalf("lease releases=%d, want 1", got)
	}
	if got := len(j.Records()); got != 0 {
		t.Fatalf("journal records=%d, want 0", got)
	}
	if _, ok := c.Order(req.ClientID); ok {
		t.Fatal("unsupported prepared execution entered cache")
	}
}

func TestSubmitUsesPreparedExecutionPathWhenRiskReturnsLease(t *testing.T) {
	client := &preparedPathExec{FakeExec: runtimetest.NewFakeExec()}
	e, _, _ := testEngine(client)
	e.WithRisk(&contextRisk{lease: &execLease{}}, nil)

	if _, err := e.Submit(context.Background(), testReq("prepared-path")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := client.preparedCalls.Load(); got != 1 {
		t.Fatalf("prepared submit calls=%d, want 1", got)
	}
	if got := client.regularCalls.Load(); got != 0 {
		t.Fatalf("regular submit calls=%d, want 0", got)
	}
}

func TestSubmitUsesRegularExecutionPathWithoutRiskLease(t *testing.T) {
	client := &preparedPathExec{FakeExec: runtimetest.NewFakeExec()}
	e, _, _ := testEngine(client)
	e.WithRisk(&contextRisk{}, nil)

	if _, err := e.Submit(context.Background(), testReq("regular-path")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := client.preparedCalls.Load(); got != 0 {
		t.Fatalf("prepared submit calls=%d, want 0", got)
	}
	if got := client.regularCalls.Load(); got != 1 {
		t.Fatalf("regular submit calls=%d, want 1", got)
	}
}

func TestPreparedStateUnavailableClosesIntentAsLocalDenied(t *testing.T) {
	fail := errors.Join(contract.ErrPreparedStateUnavailable, errors.New("prepared payload expired"))
	client := &preparedPathExec{FakeExec: runtimetest.NewFakeExec(), preparedErr: fail}
	e, c, j := testEngine(client)
	e.WithRisk(&contextRisk{lease: &execLease{}}, nil)
	req := testReq("prepared-expired")

	if _, err := e.Submit(context.Background(), req); !errors.Is(err, contract.ErrPreparedStateUnavailable) {
		t.Fatalf("submit err=%v, want ErrPreparedStateUnavailable", err)
	}
	order, ok := c.Order(req.ClientID)
	if !ok || order.Status != enums.StatusRejected {
		t.Fatalf("cache order=%+v ok=%v, want terminal rejected", order, ok)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight count=%d, want 0", got)
	}
	assertResultOutcome(t, j, exec.OutcomeLocalDenied)
}

func TestSubmitPreservesLegacyRiskChecker(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	risk := &legacyRisk{}
	e.WithRisk(risk, nil)

	if _, err := e.Submit(context.Background(), testReq("legacy-risk")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := risk.calls.Load(); got != 1 {
		t.Fatalf("legacy risk calls=%d, want 1", got)
	}
}

func TestSubmitLocalValidatorRunsBeforeContextRisk(t *testing.T) {
	fail := errors.New("local validation failed")
	client := &validatingExec{FakeExec: runtimetest.NewFakeExec(), err: fail}
	c := cache.New()
	j := journal.NewMemory()
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := exec.New(client, c, clk, "test").WithJournal(j)
	risk := &contextRisk{}
	e.WithRisk(risk, nil)

	if _, err := e.Submit(context.Background(), testReq("validator-before-risk")); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	if got := risk.contextCalls.Load(); got != 0 {
		t.Fatalf("context risk calls=%d, want 0 after local validation rejection", got)
	}
	if got := len(j.Records()); got != 0 {
		t.Fatalf("journal records=%d, want 0", got)
	}
}

func TestSubmitReleasesLeaseWhenIntentAppendFails(t *testing.T) {
	fake := &preparedPathExec{FakeExec: runtimetest.NewFakeExec()}
	e, _, _ := testEngine(fake)
	lease := &execLease{}
	risk := &contextRisk{lease: lease}
	e.WithRisk(risk, nil)
	fail := errors.New("intent append failed")
	e.WithJournal(&failingJournal{Store: journal.NewMemory(), failIntent: fail})
	called := atomic.Bool{}
	fake.FakeExec.OnSubmit(func(model.OrderRequest) { called.Store(true) })

	if _, err := e.Submit(context.Background(), testReq("intent-failure-lease")); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	if called.Load() {
		t.Fatal("venue submit called after intent append failure")
	}
	if got := lease.releases.Load(); got != 1 {
		t.Fatalf("lease releases=%d, want 1", got)
	}
}

func TestSubmitCancellationAfterRiskReleasesLeaseBeforeJournal(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	lease := &execLease{}
	ctx, cancel := context.WithCancel(context.Background())
	e.WithRisk(&cancelingContextRisk{cancel: cancel, lease: lease}, nil)
	called := atomic.Bool{}
	fake.OnSubmit(func(model.OrderRequest) { called.Store(true) })

	if _, err := e.Submit(ctx, testReq("cancel-after-risk")); !errors.Is(err, context.Canceled) {
		t.Fatalf("submit err=%v, want context.Canceled", err)
	}
	if called.Load() {
		t.Fatal("venue submit called after context cancellation")
	}
	if got := len(j.Records()); got != 0 {
		t.Fatalf("journal records=%d, want 0", got)
	}
	if _, ok := c.Order("cancel-after-risk"); ok {
		t.Fatal("canceled order entered cache")
	}
	if got := lease.releases.Load(); got != 1 {
		t.Fatalf("lease releases=%d, want 1", got)
	}
}

type cancelAfterIntentJournal struct {
	journal.Store
	cancel context.CancelFunc
}

func (j *cancelAfterIntentJournal) AppendCommandIntent(ctx context.Context, intent journal.CommandIntent) error {
	if err := j.Store.AppendCommandIntent(ctx, intent); err != nil {
		return err
	}
	j.cancel()
	return nil
}

func TestSubmitCancellationAfterIntentClosesJournalWithoutPendingOrder(t *testing.T) {
	fake := &preparedPathExec{FakeExec: runtimetest.NewFakeExec()}
	e, c, _ := testEngine(fake)
	lease := &execLease{}
	e.WithRisk(&contextRisk{lease: lease}, nil)
	store := journal.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	e.WithJournal(&cancelAfterIntentJournal{Store: store, cancel: cancel})
	called := atomic.Bool{}
	fake.FakeExec.OnSubmit(func(model.OrderRequest) { called.Store(true) })

	if _, err := e.Submit(ctx, testReq("cancel-after-intent")); !errors.Is(err, context.Canceled) {
		t.Fatalf("submit err=%v, want context.Canceled", err)
	}
	if called.Load() {
		t.Fatal("venue submit called after cancellation")
	}
	if _, ok := c.Order("cancel-after-intent"); ok {
		t.Fatal("canceled order entered cache as PendingNew")
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight count=%d, want 0", got)
	}
	if got := lease.releases.Load(); got != 1 {
		t.Fatalf("lease releases=%d, want 1", got)
	}
	records := store.Records()
	if len(records) != 2 {
		t.Fatalf("journal records=%d, want intent + local-denied result: %+v", len(records), records)
	}
	assertResultOutcome(t, store, exec.OutcomeLocalDenied)
}

func TestSubmitReleasesLeaseReturnedWithRiskError(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, j := testEngine(fake)
	lease := &execLease{}
	fail := errors.New("context risk failed")
	e.WithRisk(&contextRisk{lease: lease, err: fail}, nil)

	if _, err := e.Submit(context.Background(), testReq("risk-error-lease")); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	if got := lease.releases.Load(); got != 1 {
		t.Fatalf("lease releases=%d, want 1", got)
	}
	if got := len(j.Records()); got != 0 {
		t.Fatalf("journal records=%d, want 0", got)
	}
}
