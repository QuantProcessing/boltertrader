package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

var execInst = model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}

func testReq(clientID string) model.OrderRequest {
	return model.OrderRequest{
		InstrumentID: execInst,
		ClientID:     clientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.NewFromInt(1),
		Price:        decimal.NewFromInt(100),
	}
}

func testEngine(fake contract.ExecutionClient) (*exec.Engine, *cache.Cache, *journal.MemoryJournal) {
	c := cache.New()
	j := journal.NewMemory()
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := exec.New(fake, c, clk, "test").WithJournal(j)
	return e, c, j
}

func TestSubmitRecordsIntentBeforeVenueCall(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, j := testEngine(fake)
	fake.OnSubmit(func(model.OrderRequest) {
		records := j.Records()
		if len(records) != 1 || records[0].Type != journal.RecordCommandIntent {
			t.Fatalf("journal at venue boundary=%+v, want command intent already durable", records)
		}
	})
	if _, err := e.Submit(context.Background(), testReq("intent-before-venue")); err != nil {
		t.Fatalf("submit: %v", err)
	}
}

func TestSubmitAckClearsInFlight(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, j := testEngine(fake)
	order, err := e.Submit(context.Background(), testReq("ack"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != enums.StatusNew {
		t.Fatalf("status=%s, want NEW", order.Status)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
	assertResultOutcome(t, j, exec.OutcomeConfirmedAccepted)
}

func TestSubmitDefinitiveRejectAppliesRejectedOnce(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	fake.SetSubmitResult(nil, exec.DefinitiveReject("bad price"))
	if _, err := e.Submit(context.Background(), testReq("reject")); !errors.Is(err, exec.ErrVenueRejected) {
		t.Fatalf("submit err=%v, want ErrVenueRejected", err)
	}
	order, ok := c.Order("reject")
	if !ok {
		t.Fatal("cache missing rejected order")
	}
	if order.Status != enums.StatusRejected {
		t.Fatalf("status=%s, want REJECTED", order.Status)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
	assertResultOutcome(t, j, exec.OutcomeDefinitiveVenueRejected)
}

func TestSubmitRejectedPayloadReturnsVenueRejectedError(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	req := testReq("payload-reject")
	fake.SetSubmitResult(&model.Order{
		Request:      req,
		VenueOrderID: "venue-payload-reject",
		Status:       enums.StatusRejected,
		RejectReason: "insufficient balance",
	}, nil)
	order, err := e.Submit(context.Background(), req)
	if !errors.Is(err, exec.ErrVenueRejected) {
		t.Fatalf("submit err=%v, want ErrVenueRejected", err)
	}
	if order != nil {
		t.Fatalf("order=%+v, want nil on rejected payload", order)
	}
	cached, ok := c.Order(req.ClientID)
	if !ok || cached.Status != enums.StatusRejected {
		t.Fatalf("cache ok=%v order=%+v, want rejected", ok, cached)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
	assertResultOutcome(t, j, exec.OutcomeDefinitiveVenueRejected)
}

func TestSubmitContractVenueRejectClearsInFlightAndReplay(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	fake.SetSubmitResult(nil, contract.ErrVenueRejected)
	if _, err := e.Submit(context.Background(), testReq("contract-reject")); !errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("submit err=%v, want contract venue rejection", err)
	}
	order, ok := c.Order("contract-reject")
	if !ok {
		t.Fatal("cache missing rejected order")
	}
	if order.Status != enums.StatusRejected {
		t.Fatalf("status=%s, want REJECTED", order.Status)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
	e.WithInFlightJournal(exec.NewInFlightJournal())
	if err := e.ReplayOpenIntents(context.Background()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if open := e.OpenInFlight(); len(open) != 0 {
		t.Fatalf("open=%+v, want no replay after definitive rejection", open)
	}
	assertResultOutcome(t, j, exec.OutcomeDefinitiveVenueRejected)
}

func TestSubmitAmbiguousLeavesInFlight(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	if _, err := e.Submit(context.Background(), testReq("ambiguous")); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ErrAmbiguousResult", err)
	}
	order, ok := c.Order("ambiguous")
	if !ok {
		t.Fatal("cache missing pending order")
	}
	if order.Status != enums.StatusPendingNew {
		t.Fatalf("status=%s, want PENDING_NEW", order.Status)
	}
	if got := e.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d, want 1", got)
	}
	assertResultOutcome(t, j, exec.OutcomeAmbiguous)
}

func TestSubmitLocalValidationFailureDoesNotTouchJournalCacheOrVenue(t *testing.T) {
	fail := errors.New("local validation failed")
	fake := &validatingExec{FakeExec: runtimetest.NewFakeExec(), err: fail}
	e, c, j := testEngine(fake.FakeExec)
	e = exec.New(fake, c, clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), "test").WithJournal(j)
	called := false
	fake.OnSubmit(func(model.OrderRequest) { called = true })
	if _, err := e.Submit(context.Background(), testReq("local-invalid")); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	if called {
		t.Fatal("venue submit was called after local validation failed")
	}
	if got := len(j.Records()); got != 0 {
		t.Fatalf("journal records=%d, want 0", got)
	}
	if _, ok := c.Order("local-invalid"); ok {
		t.Fatal("cache contains order rejected by local validation")
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
}

func TestSubmitIntentCarriesAccountID(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, j := testEngine(fake)
	e.WithAccountID("acct-1")
	if _, err := e.Submit(context.Background(), testReq("account-intent")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	for _, record := range j.Records() {
		if record.Type != journal.RecordCommandIntent {
			continue
		}
		var intent journal.CommandIntent
		if err := json.Unmarshal(record.Payload, &intent); err != nil {
			t.Fatalf("decode intent: %v", err)
		}
		if intent.AccountID != "acct-1" {
			t.Fatalf("account_id=%q, want acct-1", intent.AccountID)
		}
		return
	}
	t.Fatal("no command intent record")
}

func TestSubmitRequestCarriesEngineAccountID(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, _ := testEngine(fake)
	e.WithAccountID("acct-1")
	var submitted model.OrderRequest
	fake.OnSubmit(func(req model.OrderRequest) { submitted = req })

	if _, err := e.Submit(context.Background(), testReq("account-request")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.AccountID != "acct-1" {
		t.Fatalf("submitted account_id=%q, want acct-1", submitted.AccountID)
	}
	cached, ok := c.Order("account-request")
	if !ok || cached.Request.AccountID != "acct-1" {
		t.Fatalf("cached order ok=%v account_id=%q, want acct-1", ok, cached.Request.AccountID)
	}
}

func TestSubmitIntentDefaultsAccountIDFromPrefix(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, j := testEngine(fake)
	if _, err := e.Submit(context.Background(), testReq("default-account-intent")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	for _, record := range j.Records() {
		if record.Type != journal.RecordCommandIntent {
			continue
		}
		var intent journal.CommandIntent
		if err := json.Unmarshal(record.Payload, &intent); err != nil {
			t.Fatalf("decode intent: %v", err)
		}
		if intent.AccountID != "test" {
			t.Fatalf("account_id=%q, want test", intent.AccountID)
		}
		return
	}
	t.Fatal("no command intent record")
}

func TestJournalAppendFailureBeforeVenueCallBlocksCommand(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	fail := errors.New("disk full")
	e.WithJournal(&failingJournal{Store: journal.NewMemory(), failIntent: fail})
	called := false
	fake.OnSubmit(func(model.OrderRequest) { called = true })
	if _, err := e.Submit(context.Background(), testReq("intent-fail")); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	if called {
		t.Fatal("venue was called after intent append failed")
	}
}

func TestJournalAppendFailureAfterVenueCallSignalsRecoverabilityBreach(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	fail := errors.New("result fsync failed")
	e.WithJournal(&failingJournal{Store: journal.NewMemory(), failResult: fail})
	breached := false
	e.WithRecoverabilityHandler(func(err error) {
		if errors.Is(err, fail) {
			breached = true
		}
	})
	if _, err := e.Submit(context.Background(), testReq("result-fail")); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	if !breached {
		t.Fatal("recoverability breach handler was not called")
	}
}

func TestCancelAmbiguousRemainsPendingCancel(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	order, err := e.Submit(context.Background(), testReq("cancel-ambiguous"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fake.SetCancelError(exec.ErrAmbiguousResult)
	if err := e.Cancel(context.Background(), order.Request.ClientID); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("cancel err=%v, want ambiguous", err)
	}
	open := e.OpenInFlight()
	if len(open) != 1 || open[0].State != exec.InFlightPendingCancel {
		t.Fatalf("open in-flight=%+v, want pending cancel", open)
	}
}

func TestCancelConfirmedMarksCachedOrderCanceled(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, _ := testEngine(fake)
	order, err := e.Submit(context.Background(), testReq("cancel-confirmed"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := e.Cancel(context.Background(), order.Request.ClientID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cached, ok := c.Order(order.Request.ClientID)
	if !ok {
		t.Fatal("cache missing canceled order")
	}
	if cached.Status != enums.StatusCanceled {
		t.Fatalf("status=%s, want CANCELED", cached.Status)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
}

func TestModifyAmbiguousRemainsPendingModify(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, _, _ := testEngine(fake)
	order, err := e.Submit(context.Background(), testReq("modify-ambiguous"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fake.SetModifyResult(nil, exec.ErrAmbiguousResult)
	if _, err := e.Modify(context.Background(), order.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2)); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("modify err=%v, want ambiguous", err)
	}
	open := e.OpenInFlight()
	if len(open) != 1 || open[0].State != exec.InFlightPendingModify {
		t.Fatalf("open in-flight=%+v, want pending modify", open)
	}
}

func TestModifyConfirmedSparseVenueOrderPreservesOpenStateAndRequest(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, c, _ := testEngine(fake)
	req := testReq("modify-sparse-ack")
	req.PositionSide = enums.PosLong
	submitted, err := e.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	newPrice := decimal.NewFromInt(101)
	newQty := decimal.NewFromInt(2)
	fake.SetModifyResult(&model.Order{
		Request: model.OrderRequest{
			InstrumentID: req.InstrumentID,
			ClientID:     "venue-generated-client-id",
			Side:         enums.SideBuy,
		},
		VenueOrderID: submitted.VenueOrderID,
		UpdatedAt:    time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
	}, nil)
	got, err := e.Modify(context.Background(), req.ClientID, newPrice, newQty)
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	assertModifiedOrder(t, *got, req, submitted.VenueOrderID, newPrice, newQty)

	cached, ok := c.Order(req.ClientID)
	if !ok {
		t.Fatal("cache missing modified order by client id")
	}
	assertModifiedOrder(t, cached, req, submitted.VenueOrderID, newPrice, newQty)
	open := c.OpenOrders()
	if len(open) != 1 || open[0].Request.ClientID != req.ClientID {
		t.Fatalf("open orders=%+v, want modified order to remain open", open)
	}
}

func TestReplayOpenIntentsSchedulesInFlight(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, j := testEngine(fake)
	intent := journal.CommandIntent{
		RecordID:     journal.NewRecordID("intent", "replay-open"),
		CommandID:    journal.NewRecordID("command", "replay-open"),
		Type:         journal.CommandSubmit,
		ClientID:     "replay-open",
		InstrumentID: execInst,
		Side:         enums.SideBuy,
		OrderType:    enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.NewFromInt(1),
		Price:        decimal.NewFromInt(100),
		SubmittedAt:  time.Now(),
		Attempt:      1,
	}
	if err := j.AppendCommandIntent(context.Background(), intent); err != nil {
		t.Fatalf("append intent: %v", err)
	}
	if err := j.AppendCommandResult(context.Background(), journal.CommandResult{
		RecordID:       journal.NewRecordID("result", intent.RecordID, "ambiguous"),
		IntentRecordID: intent.RecordID,
		CommandID:      intent.CommandID,
		Type:           intent.Type,
		ClientID:       intent.ClientID,
		Outcome:        journal.AmbiguousOutcome,
		ResultAt:       time.Now(),
	}); err != nil {
		t.Fatalf("append result: %v", err)
	}
	e.WithInFlightJournal(exec.NewInFlightJournal())
	if err := e.ReplayOpenIntents(context.Background()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	open := e.OpenInFlight()
	if len(open) != 1 || open[0].Intent.ClientID != "replay-open" {
		t.Fatalf("open=%+v, want replayed in-flight", open)
	}
}

func TestReplayOpenIntentsFromFileJournal(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/exec.journal"
	store, err := journal.OpenFile(path, journal.FileOptions{})
	if err != nil {
		t.Fatalf("open file journal: %v", err)
	}
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	e.WithJournal(store)
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	if _, err := e.Submit(ctx, testReq("durable-open")); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	replayed, err := journal.OpenFile(path, journal.FileOptions{})
	if err != nil {
		t.Fatalf("reopen file journal: %v", err)
	}
	defer replayed.Close()
	c := cache.New()
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC))
	restarted := exec.New(runtimetest.NewFakeExec(), c, clk, "test").WithJournal(replayed)
	if err := restarted.ReplayOpenIntents(ctx); err != nil {
		t.Fatalf("replay open intents: %v", err)
	}
	open := restarted.OpenInFlight()
	if len(open) != 1 || open[0].Intent.ClientID != "durable-open" {
		t.Fatalf("open=%+v, want durable-open in-flight", open)
	}
}

func TestResolvedInFlightDoesNotReplayFromFileJournal(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/resolved.journal"
	store, err := journal.OpenFile(path, journal.FileOptions{})
	if err != nil {
		t.Fatalf("open file journal: %v", err)
	}
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	e.WithJournal(store)
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	if _, err := e.Submit(ctx, testReq("durable-resolved")); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	e.ResolveInFlight("durable-resolved", "venue-resolved", time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0 after resolution", got)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	replayed, err := journal.OpenFile(path, journal.FileOptions{})
	if err != nil {
		t.Fatalf("reopen file journal: %v", err)
	}
	defer replayed.Close()
	c := cache.New()
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC))
	restarted := exec.New(runtimetest.NewFakeExec(), c, clk, "test").WithJournal(replayed)
	if err := restarted.ReplayOpenIntents(ctx); err != nil {
		t.Fatalf("replay open intents: %v", err)
	}
	if open := restarted.OpenInFlight(); len(open) != 0 {
		t.Fatalf("open=%+v, want no replayed intent after durable resolution", open)
	}
}

type failingJournal struct {
	journal.Store
	failIntent error
	failResult error
}

type validatingExec struct {
	*runtimetest.FakeExec
	err error
}

func (v *validatingExec) ValidateSubmit(model.OrderRequest) error { return v.err }

func (j *failingJournal) AppendCommandIntent(ctx context.Context, intent journal.CommandIntent) error {
	if j.failIntent != nil {
		return j.failIntent
	}
	return j.Store.AppendCommandIntent(ctx, intent)
}

func (j *failingJournal) AppendCommandResult(ctx context.Context, result journal.CommandResult) error {
	if j.failResult != nil {
		return j.failResult
	}
	return j.Store.AppendCommandResult(ctx, result)
}

func assertModifiedOrder(t *testing.T, got model.Order, wantReq model.OrderRequest, wantVenueID string, wantPrice, wantQty decimal.Decimal) {
	t.Helper()
	if got.Request.ClientID != wantReq.ClientID {
		t.Fatalf("client id=%q, want %q", got.Request.ClientID, wantReq.ClientID)
	}
	if got.VenueOrderID != wantVenueID {
		t.Fatalf("venue order id=%q, want %q", got.VenueOrderID, wantVenueID)
	}
	if got.Status != enums.StatusNew {
		t.Fatalf("status=%s, want NEW", got.Status)
	}
	if got.Request.InstrumentID != wantReq.InstrumentID {
		t.Fatalf("instrument=%+v, want %+v", got.Request.InstrumentID, wantReq.InstrumentID)
	}
	if got.Request.Side != wantReq.Side {
		t.Fatalf("side=%s, want %s", got.Request.Side, wantReq.Side)
	}
	if got.Request.Type != wantReq.Type {
		t.Fatalf("type=%s, want %s", got.Request.Type, wantReq.Type)
	}
	if got.Request.TIF != wantReq.TIF {
		t.Fatalf("tif=%s, want %s", got.Request.TIF, wantReq.TIF)
	}
	if got.Request.PositionSide != wantReq.PositionSide {
		t.Fatalf("position side=%s, want %s", got.Request.PositionSide, wantReq.PositionSide)
	}
	if !got.Request.Price.Equal(wantPrice) {
		t.Fatalf("price=%s, want %s", got.Request.Price, wantPrice)
	}
	if !got.Request.Quantity.Equal(wantQty) {
		t.Fatalf("quantity=%s, want %s", got.Request.Quantity, wantQty)
	}
}

func assertResultOutcome(t *testing.T, j *journal.MemoryJournal, want exec.OutcomeClass) {
	t.Helper()
	records := j.Records()
	for _, record := range records {
		if record.Type != journal.RecordCommandResult {
			continue
		}
		var result journal.CommandResult
		if err := json.Unmarshal(record.Payload, &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if result.Outcome != string(want) {
			t.Fatalf("outcome=%s, want %s", result.Outcome, want)
		}
		return
	}
	t.Fatalf("no command result record in %+v", records)
}
