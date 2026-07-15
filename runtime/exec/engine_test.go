package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestSubmitCrossedVenueAliasFailsClosedBeforeResolvingInFlight(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	collision := model.Order{
		Request: model.OrderRequest{
			AccountID: "test", InstrumentID: execInst, ClientID: "other-submit",
			Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100),
		},
		VenueOrderID: "shared-submit-venue",
		Status:       enums.StatusNew,
	}
	c.UpsertOrder(collision)
	req := testReq("crossed-submit")
	fake.SetSubmitResult(&model.Order{Request: req, VenueOrderID: collision.VenueOrderID, Status: enums.StatusNew}, nil)

	order, err := e.Submit(context.Background(), req)
	if !errors.Is(err, cache.ErrOrderIdentityConflict) || order != nil {
		t.Fatalf("submit order=%+v err=%v, want fail-closed order identity conflict", order, err)
	}
	if got := e.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d, crossed submit response consumed pending intent", got)
	}
	if records := j.Records(); len(records) != 1 || records[0].Type != journal.RecordCommandIntent {
		t.Fatalf("journal records=%+v, crossed response must not record confirmed acceptance", records)
	}
	pending, ok := c.OrderByClientIDForAccount("test", req.ClientID)
	if !ok || pending.Status != enums.StatusPendingNew || pending.VenueOrderID != "" {
		t.Fatalf("pending order=(%+v,%v), want unresolved PENDING_NEW", pending, ok)
	}
	if got, ok := c.OrderByClientIDForAccount("test", collision.Request.ClientID); !ok || got.VenueOrderID != collision.VenueOrderID {
		t.Fatalf("collision order=(%+v,%v), want unchanged", got, ok)
	}
}

func TestSubmitResponseClientIDMismatchFailsBeforeResultJournal(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	var breach error
	e.WithRecoverabilityHandler(func(err error) { breach = err })
	req := testReq("submit-response-client")
	fake.SetSubmitResult(&model.Order{
		Request:      model.OrderRequest{ClientID: "different-response-client"},
		VenueOrderID: "response-client-venue",
		Status:       enums.StatusNew,
	}, nil)

	order, err := e.Submit(context.Background(), req)
	if order != nil || !errors.Is(err, cache.ErrOrderIdentityConflict) {
		t.Fatalf("submit order=%+v err=%v, want response identity conflict", order, err)
	}
	if breach == nil || !errors.Is(breach, cache.ErrOrderIdentityConflict) {
		t.Fatalf("recoverability breach=%v, want response identity conflict", breach)
	}
	if records := j.Records(); len(records) != 1 || records[0].Type != journal.RecordCommandIntent {
		t.Fatalf("journal records=%+v, mismatched response must not persist a result", records)
	}
	if got := e.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d, mismatched response consumed recovery state", got)
	}
	pending, ok := c.OrderByClientIDForAccount("test", req.ClientID)
	if !ok || pending.Status != enums.StatusPendingNew || pending.VenueOrderID != "" {
		t.Fatalf("pending order=(%+v,%v), want original unresolved PendingNew", pending, ok)
	}
	if _, exists := c.OrderByClientIDForAccount("test", "different-response-client"); exists {
		t.Fatal("mismatched response client was inserted into cache")
	}
}

func TestSubmitCachedVenueAliasCollisionFailsBeforeEveryResultOutcome(t *testing.T) {
	tests := []struct {
		name   string
		status enums.OrderStatus
		err    error
	}{
		{name: "ambiguous", status: enums.StatusUnknown, err: exec.ErrAmbiguousResult},
		{name: "rejected", status: enums.StatusRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			e, c, j := testEngine(fake)
			const sharedVenueOrderID = "resolved-cache-venue"
			existing := model.Order{
				Request: model.OrderRequest{
					AccountID: "test", InstrumentID: execInst, ClientID: "resolved-cache-client",
					Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100),
				},
				VenueOrderID: sharedVenueOrderID,
				Status:       enums.StatusNew,
			}
			c.UpsertOrder(existing)
			var breach error
			e.WithRecoverabilityHandler(func(err error) { breach = err })
			req := testReq("cached-alias-" + tt.name)
			fake.SetSubmitResult(&model.Order{VenueOrderID: sharedVenueOrderID, Status: tt.status}, tt.err)

			order, err := e.Submit(context.Background(), req)
			if order != nil || !errors.Is(err, cache.ErrOrderIdentityConflict) {
				t.Fatalf("submit order=%+v err=%v, want cached venue identity conflict", order, err)
			}
			if breach == nil || !errors.Is(breach, cache.ErrOrderIdentityConflict) {
				t.Fatalf("recoverability breach=%v, want cached venue identity conflict", breach)
			}
			if records := j.Records(); len(records) != 1 || records[0].Type != journal.RecordCommandIntent {
				t.Fatalf("journal records=%+v, conflicting %s result must not persist", records, tt.name)
			}
			if got := e.InFlightCount(); got != 1 {
				t.Fatalf("in-flight=%d, conflicting %s result consumed recovery state", got, tt.name)
			}
			pending, ok := c.OrderByClientIDForAccount("test", req.ClientID)
			if !ok || pending.Status != enums.StatusPendingNew || pending.VenueOrderID != "" {
				t.Fatalf("pending order=(%+v,%v), want unresolved PendingNew", pending, ok)
			}
			gotExisting, ok := c.OrderByClientIDForAccount("test", existing.Request.ClientID)
			if !ok || gotExisting.VenueOrderID != sharedVenueOrderID || gotExisting.Status != enums.StatusNew {
				t.Fatalf("existing order=(%+v,%v), want unchanged", gotExisting, ok)
			}
		})
	}
}

func TestSubmitSparseAcknowledgementUsesOriginalRequestIdentity(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, _ := testEngine(fake)
	req := testReq("sparse-submit-ack")
	fake.SetSubmitResult(&model.Order{
		Request:      model.OrderRequest{ClientID: req.ClientID},
		VenueOrderID: "sparse-submit-venue",
		Status:       enums.StatusNew,
	}, nil)

	order, err := e.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order == nil || order.Request.AccountID != "test" || order.Request.InstrumentID != req.InstrumentID ||
		order.Request.Side != req.Side || order.Request.Type != req.Type || !order.Request.Quantity.Equal(req.Quantity) ||
		!order.Request.Price.Equal(req.Price) {
		t.Fatalf("acknowledged order=%+v, want original request identity and terms", order)
	}
	cached, ok := c.OrderByClientIDForAccount("test", req.ClientID)
	if !ok || cached.VenueOrderID != order.VenueOrderID || cached.Request.InstrumentID != req.InstrumentID {
		t.Fatalf("cached order=(%+v,%v), want normalized acknowledgement", cached, ok)
	}
}

func TestSubmitResultAndCacheAliasClaimAreAtomic(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, _ := testEngine(fake)
	j := newBlockingResultJournal()
	e.WithJournal(j)
	req := testReq("atomic-submit-result")
	const venueOrderID = "atomic-submit-venue"
	fake.SetSubmitResult(&model.Order{VenueOrderID: venueOrderID, Status: enums.StatusNew}, nil)

	type submitResult struct {
		order *model.Order
		err   error
	}
	submitDone := make(chan submitResult, 1)
	go func() {
		order, err := e.Submit(context.Background(), req)
		submitDone <- submitResult{order: order, err: err}
	}()

	select {
	case <-j.entered:
	case result := <-submitDone:
		t.Fatalf("submit returned before result journal blocked: order=%+v err=%v", result.order, result.err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result journal")
	}

	collision := model.Order{
		Request: model.OrderRequest{
			AccountID: "test", InstrumentID: execInst, ClientID: "atomic-submit-collision",
			Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100),
		},
		VenueOrderID: venueOrderID,
		Status:       enums.StatusNew,
	}
	collisionDone := make(chan error, 1)
	go func() { collisionDone <- c.UpsertOrderChecked(collision) }()

	var collisionErr error
	collisionReturnedBeforeCommit := false
	select {
	case collisionErr = <-collisionDone:
		collisionReturnedBeforeCommit = true
	case <-time.After(50 * time.Millisecond):
	}
	close(j.release)

	result := <-submitDone
	if !collisionReturnedBeforeCommit {
		collisionErr = <-collisionDone
	}
	if collisionReturnedBeforeCommit {
		t.Fatal("conflicting cache write completed while the accepted result was durably committing")
	}
	if result.err != nil || result.order == nil || result.order.VenueOrderID != venueOrderID {
		t.Fatalf("submit result=(%+v,%v), want accepted order", result.order, result.err)
	}
	if !errors.Is(collisionErr, cache.ErrOrderIdentityConflict) {
		t.Fatalf("conflicting cache write err=%v, want identity conflict", collisionErr)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want accepted intent resolved", got)
	}
	assertResultOutcome(t, j.MemoryJournal, exec.OutcomeConfirmedAccepted)
}

func TestSubmitResultCannotClaimAnotherInFlightVenueOrderID(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, j := testEngine(fake)
	var breaches []error
	e.WithRecoverabilityHandler(func(err error) { breaches = append(breaches, err) })
	const sharedVenueOrderID = "shared-inflight-venue"

	firstReq := testReq("first-inflight-client")
	firstVenueReq := firstReq
	firstVenueReq.AccountID = "test"
	fake.SetSubmitResult(&model.Order{
		Request: firstVenueReq, VenueOrderID: sharedVenueOrderID, Status: enums.StatusNew,
	}, exec.ErrAmbiguousResult)
	if _, err := e.Submit(context.Background(), firstReq); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("first submit err=%v, want ambiguous", err)
	}

	secondReq := testReq("second-inflight-client")
	secondVenueReq := secondReq
	secondVenueReq.AccountID = "test"
	fake.SetSubmitResult(&model.Order{
		Request: secondVenueReq, VenueOrderID: sharedVenueOrderID, Status: enums.StatusNew,
	}, nil)
	order, err := e.Submit(context.Background(), secondReq)
	if order != nil || !errors.Is(err, cache.ErrOrderIdentityConflict) {
		t.Fatalf("second submit order=%+v err=%v, want fail-closed cached venue identity conflict", order, err)
	}
	if len(breaches) != 1 || !errors.Is(breaches[0], cache.ErrOrderIdentityConflict) {
		t.Fatalf("recoverability breaches=%v, want one venue identity conflict", breaches)
	}

	open := e.OpenInFlight()
	if len(open) != 2 {
		t.Fatalf("open=%+v, want both intents retained", open)
	}
	byClient := make(map[string]exec.InFlightEntry, len(open))
	for _, entry := range open {
		byClient[entry.Intent.ClientID] = entry
	}
	if got := byClient[firstReq.ClientID].Intent.VenueOrderID; got != sharedVenueOrderID {
		t.Fatalf("first venue order id=%q, want %q", got, sharedVenueOrderID)
	}
	if got := byClient[secondReq.ClientID].Intent.VenueOrderID; got != "" {
		t.Fatalf("second venue order id=%q, conflicting alias must not be claimed", got)
	}
	firstCached, ok := c.OrderByClientIDForAccount("test", firstReq.ClientID)
	if !ok || firstCached.Status != enums.StatusPendingNew || firstCached.VenueOrderID != sharedVenueOrderID {
		t.Fatalf("first cached order=(%+v,%v), want PendingNew with its unambiguous returned venue alias", firstCached, ok)
	}
	secondCached, ok := c.OrderByClientIDForAccount("test", secondReq.ClientID)
	if !ok || secondCached.Status != enums.StatusPendingNew || secondCached.VenueOrderID != "" {
		t.Fatalf("second cached order=(%+v,%v), want unresolved PendingNew without crossed venue alias", secondCached, ok)
	}
	if records := j.Records(); len(records) != 3 || records[2].Type != journal.RecordCommandIntent {
		t.Fatalf("journal records=%+v, conflicting confirmed result must not be persisted", records)
	}
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

func TestSubmitDuplicateClientIDFailsBeforeSecondVenueCall(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	e, _, _ := testEngine(fake)
	calls := 0
	fake.OnSubmit(func(model.OrderRequest) { calls++ })
	req := testReq("duplicate-client-id")

	if _, err := e.Submit(context.Background(), req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("first submit err=%v, want ambiguous", err)
	}
	open := e.OpenInFlight()
	if len(open) != 1 {
		t.Fatalf("open=%+v, want original in-flight intent", open)
	}
	originalRecordID := open[0].Intent.RecordID

	if _, err := e.Submit(context.Background(), req); err == nil || !strings.Contains(err.Error(), "duplicate client id") {
		t.Fatalf("second submit err=%v, want duplicate client id rejection", err)
	}
	if calls != 1 {
		t.Fatalf("venue submit calls=%d, duplicate ClientID crossed venue boundary", calls)
	}
	open = e.OpenInFlight()
	if len(open) != 1 || open[0].Intent.RecordID != originalRecordID {
		t.Fatalf("open=%+v, duplicate submit overwrote original in-flight intent %q", open, originalRecordID)
	}
}

func TestSubmitConfirmedClientIDCannotBeReused(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, _ := testEngine(fake)
	calls := 0
	fake.OnSubmit(func(model.OrderRequest) { calls++ })
	req := testReq("confirmed-duplicate-client-id")

	if _, err := e.Submit(context.Background(), req); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d after confirmed submit, want 0", got)
	}
	if _, err := e.Submit(context.Background(), req); !errors.Is(err, exec.ErrDuplicateClientID) {
		t.Fatalf("second submit err=%v, want ErrDuplicateClientID", err)
	}
	if calls != 1 {
		t.Fatalf("venue submit calls=%d, confirmed ClientID reuse crossed venue boundary", calls)
	}
	order, ok := c.OrderByClientIDForAccount("test", req.ClientID)
	if !ok || order.Status != enums.StatusNew {
		t.Fatalf("cached order=(%+v,%v), want original confirmed order", order, ok)
	}
}

func TestConcurrentSubmitDuplicateClientIDIsReservedBeforeJournalAndVenue(t *testing.T) {
	fake := newStagedDuplicateExec()
	e, _, _ := testEngine(fake)
	req := testReq("concurrent-duplicate-client-id")
	type result struct {
		order *model.Order
		err   error
	}
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)

	go func() {
		order, err := e.Submit(context.Background(), req)
		firstResult <- result{order: order, err: err}
	}()
	select {
	case <-fake.firstAtValidation:
	case <-time.After(2 * time.Second):
		t.Fatal("first submit did not reach validation")
	}
	go func() {
		order, err := e.Submit(context.Background(), req)
		secondResult <- result{order: order, err: err}
	}()
	select {
	case <-fake.secondAtValidation:
	case <-time.After(2 * time.Second):
		t.Fatal("second submit did not pass the initial duplicate check")
	}
	close(fake.releaseFirst)

	first := <-firstResult
	if first.err != nil || first.order == nil {
		t.Fatalf("first submit order=%+v err=%v, want confirmed", first.order, first.err)
	}
	second := <-secondResult
	if second.order != nil || !errors.Is(second.err, exec.ErrDuplicateClientID) {
		t.Fatalf("second submit order=%+v err=%v, want duplicate rejection", second.order, second.err)
	}
	if calls := fake.submitCalls.Load(); calls != 1 {
		t.Fatalf("venue submit calls=%d, want 1", calls)
	}
}

func TestInFlightLateResultDoesNotResolveReplacementWithSameClientID(t *testing.T) {
	inflight := exec.NewInFlightJournal()
	first := journal.CommandIntent{
		RecordID: "intent-first", CommandID: "command-first", Type: journal.CommandSubmit,
		ClientID: "reused-client", InstrumentID: execInst, Quantity: decimal.NewFromInt(1),
	}
	second := first
	second.RecordID = "intent-second"
	second.CommandID = "command-second"
	inflight.TrackIntent(first, exec.InFlightSubmitted)
	inflight.TrackIntent(second, exec.InFlightPendingModify)

	inflight.ApplyResult(journal.CommandResult{
		IntentRecordID: first.RecordID,
		CommandID:      first.CommandID,
		Type:           first.Type,
		ClientID:       first.ClientID,
		Outcome:        string(exec.OutcomeConfirmedAccepted),
		ResultAt:       time.Unix(101, 0),
	})

	entry, ok := inflight.ByClientID(second.ClientID)
	if !ok || entry.Intent.RecordID != second.RecordID || entry.State != exec.InFlightPendingModify {
		t.Fatalf("replacement entry=(%+v,%v), late result consumed the wrong intent", entry, ok)
	}
}

func TestReplayOpenIntentsRejectsConflictingClientIDs(t *testing.T) {
	ctx := context.Background()
	store := journal.NewMemory()
	for i, recordID := range []string{"replay-first", "replay-second"} {
		intent := journal.CommandIntent{
			RecordID: recordID, CommandID: "command-" + recordID, Type: journal.CommandSubmit,
			ClientID: "replay-duplicate", InstrumentID: execInst, Quantity: decimal.NewFromInt(int64(i + 1)),
			SubmittedAt: time.Unix(int64(100+i), 0),
		}
		if err := store.AppendCommandIntent(ctx, intent); err != nil {
			t.Fatalf("append intent: %v", err)
		}
	}
	e := exec.New(runtimetest.NewFakeExec(), cache.New(), clock.NewSimulatedClock(time.Unix(200, 0)), "replay").
		WithJournal(store).
		WithInFlightJournal(exec.NewInFlightJournal())

	err := e.ReplayOpenIntents(ctx)
	if err == nil || !strings.Contains(err.Error(), "duplicate client id") {
		t.Fatalf("ReplayOpenIntents err=%v, want duplicate client id conflict", err)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d after failed transactional replay, want 0", got)
	}
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

func TestSubmitRejectsMismatchedAccountBeforeAnySideEffect(t *testing.T) {
	fake := &countingExec{FakeExec: runtimetest.NewFakeExec()}
	e, c, j := testEngine(fake)
	gate := &countingCommandGate{}
	risk := &countingRisk{}
	e.WithAccountID("acct-1").WithCommandGate(gate).WithRisk(risk, nil)
	req := testReq("foreign-account")
	req.AccountID = "acct-2"

	if _, err := e.Submit(context.Background(), req); err == nil {
		t.Fatal("submit succeeded with an account id owned by another engine")
	}
	if fake.validateCalls != 0 || fake.submitCalls != 0 {
		t.Fatalf("client calls validate=%d submit=%d, want 0", fake.validateCalls, fake.submitCalls)
	}
	if gate.submitCalls != 0 {
		t.Fatalf("gate submit calls=%d, want 0", gate.submitCalls)
	}
	if risk.calls != 0 {
		t.Fatalf("risk calls=%d, want 0", risk.calls)
	}
	if got := len(j.Records()); got != 0 {
		t.Fatalf("journal records=%d, want 0", got)
	}
	if _, ok := c.Order(req.ClientID); ok {
		t.Fatal("cache contains order rejected for account mismatch")
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
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
	e, c, _ := testEngine(fake)
	fail := errors.New("result fsync failed")
	store := journal.NewMemory()
	e.WithJournal(&failingJournal{Store: store, failResult: fail})
	breached := false
	e.WithRecoverabilityHandler(func(err error) {
		if errors.Is(err, fail) {
			// Recovery handlers are allowed to inspect cache state. Result/cache
			// atomicity must not invoke this callback while holding Cache.mu.
			_, _ = c.OrderByClientIDForAccount("test", "result-fail")
			breached = true
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := e.Submit(context.Background(), testReq("result-fail"))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, fail) {
			t.Fatalf("submit err=%v, want %v", err, fail)
		}
	case <-time.After(time.Second):
		t.Fatal("submit deadlocked while recoverability handler inspected cache")
	}
	if !breached {
		t.Fatal("recoverability breach handler was not called")
	}
	pending, ok := c.OrderByClientIDForAccount("test", "result-fail")
	if !ok || pending.Status != enums.StatusPendingNew || pending.VenueOrderID != "" {
		t.Fatalf("pending order=(%+v,%v), failed result journal must leave pre-ack cache state", pending, ok)
	}
	if got := e.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d, failed result journal must retain recovery intent", got)
	}
	if records := store.Records(); len(records) != 1 || records[0].Type != journal.RecordCommandIntent {
		t.Fatalf("journal records=%+v, want only durable intent after result failure", records)
	}
}

func TestSubmitPersistsConfirmedResultAfterCallerContextCanceledByAdapter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &cancelBeforeReturnExec{FakeExec: runtimetest.NewFakeExec(), cancel: cancel, onSubmit: true}
	e, _, _ := testEngine(fake)
	resultJournal := newDetachedResultJournal()
	e.WithJournal(resultJournal)

	order, err := e.Submit(ctx, testReq("submit-canceled-on-return"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order == nil || order.Status != enums.StatusNew {
		t.Fatalf("order=%+v, want confirmed new order", order)
	}
	resultJournal.assertResultContext(t)
	assertResultOutcome(t, resultJournal.MemoryJournal, exec.OutcomeConfirmedAccepted)
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

func TestConfirmedCancelNotifiesTerminalOrderHandler(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	var terminal []model.Order
	e.WithTerminalOrderHandler(func(order model.Order) { terminal = append(terminal, order) })
	order, err := e.Submit(context.Background(), testReq("cancel-terminal-hook"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := e.Cancel(context.Background(), order.Request.ClientID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(terminal) != 1 || terminal[0].Status != enums.StatusCanceled || terminal[0].Request.ClientID != order.Request.ClientID {
		t.Fatalf("terminal notifications=%+v, want confirmed canceled order", terminal)
	}
}

func TestConfirmedCancelUsesLatestCanonicalOrderState(t *testing.T) {
	tests := []struct {
		name      string
		updatedAt func(time.Time) time.Time
	}{
		{name: "newer venue event", updatedAt: func(at time.Time) time.Time { return at.Add(time.Minute) }},
		{name: "sparse venue timestamp", updatedAt: func(time.Time) time.Time { return time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			e, c, _ := testEngine(fake)
			target, err := e.Submit(context.Background(), testReq("cancel-latest-"+strings.ReplaceAll(tt.name, " ", "-")))
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			newPrice := decimal.NewFromInt(101)
			newQty := decimal.NewFromInt(2)
			fake.OnCancel(func(_ model.InstrumentID, _ string) {
				concurrent, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
				if !ok {
					t.Fatal("cache missing order during venue cancel")
				}
				concurrent.Request.Price = newPrice
				concurrent.Request.Quantity = newQty
				concurrent.Status = enums.StatusPartiallyFilled
				concurrent.FilledQty = decimal.RequireFromString("0.5")
				concurrent.AvgFillPrice = decimal.NewFromInt(100)
				concurrent.UpdatedAt = tt.updatedAt(concurrent.UpdatedAt)
				if err := c.UpsertOrderChecked(concurrent); err != nil {
					t.Fatalf("concurrent order event: %v", err)
				}
			})

			if err := e.Cancel(context.Background(), target.Request.ClientID); err != nil {
				t.Fatalf("cancel: %v", err)
			}
			cached, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
			if !ok || cached.Status != enums.StatusCanceled || !cached.Request.Price.Equal(newPrice) ||
				!cached.Request.Quantity.Equal(newQty) || !cached.FilledQty.Equal(decimal.RequireFromString("0.5")) {
				t.Fatalf("cached order=(%+v,%v), want latest terms/fill with confirmed CANCELED", cached, ok)
			}
		})
	}
}

func TestConfirmedSameAliasModifyUsesLatestCanonicalOrderState(t *testing.T) {
	tests := []struct {
		name      string
		updatedAt func(time.Time) time.Time
	}{
		{name: "newer venue event", updatedAt: func(at time.Time) time.Time { return at.Add(time.Minute) }},
		{name: "sparse venue timestamp", updatedAt: func(time.Time) time.Time { return time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			fake.SetModifySupported(true)
			e, c, _ := testEngine(fake)
			target, err := e.Submit(context.Background(), testReq("modify-latest-"+strings.ReplaceAll(tt.name, " ", "-")))
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			newPrice := decimal.NewFromInt(101)
			newQty := decimal.NewFromInt(2)
			filledQty := decimal.RequireFromString("0.5")
			fake.OnModify(func(_ model.InstrumentID, _ string, _, _ decimal.Decimal) {
				concurrent, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
				if !ok {
					t.Fatal("cache missing order during venue modify")
				}
				concurrent.Status = enums.StatusPartiallyFilled
				concurrent.FilledQty = filledQty
				concurrent.AvgFillPrice = decimal.NewFromInt(100)
				concurrent.UpdatedAt = tt.updatedAt(concurrent.UpdatedAt)
				if err := c.UpsertOrderChecked(concurrent); err != nil {
					t.Fatalf("concurrent order event: %v", err)
				}
			})

			modified, err := e.Modify(context.Background(), target.Request.ClientID, newPrice, newQty)
			if err != nil || modified == nil {
				t.Fatalf("modify order=%+v err=%v", modified, err)
			}
			cached, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
			if !ok {
				t.Fatal("cache missing modified order")
			}
			for label, got := range map[string]model.Order{"return": *modified, "cache": cached} {
				if got.Status != enums.StatusPartiallyFilled || !got.Request.Price.Equal(newPrice) ||
					!got.Request.Quantity.Equal(newQty) || !got.FilledQty.Equal(filledQty) {
					t.Fatalf("%s order=%+v, want latest fill evidence plus confirmed amended terms", label, got)
				}
			}
		})
	}
}

func TestCancelAndModifyUseClientIDNamespaceWhenTextCollides(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, c, _ := testEngine(fake)
	target, err := e.Submit(context.Background(), testReq("shared"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	collision := model.Order{
		Request: model.OrderRequest{
			AccountID: "test", InstrumentID: execInst, ClientID: "other",
			Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100),
		},
		VenueOrderID: "shared",
		Status:       enums.StatusNew,
	}
	c.UpsertOrder(collision)

	var modifiedVenueID, canceledVenueID string
	fake.OnModify(func(_ model.InstrumentID, venueOrderID string, _, _ decimal.Decimal) {
		modifiedVenueID = venueOrderID
	})
	fake.OnCancel(func(_ model.InstrumentID, venueOrderID string) {
		canceledVenueID = venueOrderID
	})
	if _, err := e.Modify(context.Background(), "shared", decimal.NewFromInt(101), decimal.NewFromInt(2)); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if err := e.Cancel(context.Background(), "shared"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if modifiedVenueID != target.VenueOrderID || canceledVenueID != target.VenueOrderID {
		t.Fatalf("modify venue=%q cancel venue=%q, want client order venue %q", modifiedVenueID, canceledVenueID, target.VenueOrderID)
	}
	if got, ok := c.OrderByClientIDForAccount("test", "shared"); !ok || got.Status != enums.StatusCanceled {
		t.Fatalf("target order=(%+v,%v), want canceled", got, ok)
	}
	if got, ok := c.OrderByVenueOrderIDForAccount("test", "shared"); !ok || got.Request.ClientID != "other" || got.Status != enums.StatusNew {
		t.Fatalf("venue-collision order=(%+v,%v), want untouched", got, ok)
	}
}

func TestModifyCrossedVenueAliasFailsClosedBeforeResolvingInFlight(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, c, j := testEngine(fake)
	target, err := e.Submit(context.Background(), testReq("crossed-modify"))
	if err != nil {
		t.Fatalf("submit target: %v", err)
	}
	collision := model.Order{
		Request: model.OrderRequest{
			AccountID: "test", InstrumentID: execInst, ClientID: "other-modify",
			Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100),
		},
		VenueOrderID: "shared-modify-venue",
		Status:       enums.StatusNew,
	}
	c.UpsertOrder(collision)
	fake.SetModifyResult(&model.Order{VenueOrderID: collision.VenueOrderID, Status: enums.StatusNew}, nil)

	modified, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
	if !errors.Is(err, cache.ErrOrderIdentityConflict) || modified != nil {
		t.Fatalf("modify order=%+v err=%v, want fail-closed order identity conflict", modified, err)
	}
	if got := e.InFlightCount(); got != 1 {
		t.Fatalf("in-flight=%d, crossed modify response consumed pending intent", got)
	}
	if records := j.Records(); len(records) != 3 || records[2].Type != journal.RecordCommandIntent {
		t.Fatalf("journal records=%+v, crossed modify must add only its intent", records)
	}
	gotTarget, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
	if !ok || gotTarget.VenueOrderID != target.VenueOrderID || gotTarget.Request.Price.Equal(decimal.NewFromInt(101)) {
		t.Fatalf("target order=(%+v,%v), want pre-modify state", gotTarget, ok)
	}
	if got, ok := c.OrderByClientIDForAccount("test", collision.Request.ClientID); !ok || got.VenueOrderID != collision.VenueOrderID {
		t.Fatalf("collision order=(%+v,%v), want unchanged", got, ok)
	}
}

func TestModifyCachedVenueAliasCollisionFailsBeforeEveryResultOutcome(t *testing.T) {
	tests := []struct {
		name   string
		status enums.OrderStatus
		err    error
	}{
		{name: "ambiguous", status: enums.StatusUnknown, err: exec.ErrAmbiguousResult},
		{name: "rejected", status: enums.StatusRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			fake.SetModifySupported(true)
			e, c, j := testEngine(fake)
			target, err := e.Submit(context.Background(), testReq("modify-alias-"+tt.name))
			if err != nil {
				t.Fatalf("submit target: %v", err)
			}
			const sharedVenueOrderID = "modify-result-collision"
			collision := model.Order{
				Request: model.OrderRequest{
					AccountID: "test", InstrumentID: execInst, ClientID: "modify-collision-" + tt.name,
					Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100),
				},
				VenueOrderID: sharedVenueOrderID,
				Status:       enums.StatusNew,
			}
			c.UpsertOrder(collision)
			var breach error
			e.WithRecoverabilityHandler(func(err error) { breach = err })
			fake.SetModifyResult(&model.Order{VenueOrderID: sharedVenueOrderID, Status: tt.status}, tt.err)

			modified, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
			if modified != nil || !errors.Is(err, cache.ErrOrderIdentityConflict) {
				t.Fatalf("modify order=%+v err=%v, want cached venue identity conflict", modified, err)
			}
			if breach == nil || !errors.Is(breach, cache.ErrOrderIdentityConflict) {
				t.Fatalf("recoverability breach=%v, want cached venue identity conflict", breach)
			}
			if records := j.Records(); len(records) != 3 || records[2].Type != journal.RecordCommandIntent {
				t.Fatalf("journal records=%+v, conflicting %s result must not persist", records, tt.name)
			}
			if got := e.InFlightCount(); got != 1 {
				t.Fatalf("in-flight=%d, conflicting %s result consumed recovery state", got, tt.name)
			}
			gotTarget, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
			if !ok || gotTarget.VenueOrderID != target.VenueOrderID || !gotTarget.Request.Price.Equal(target.Request.Price) {
				t.Fatalf("target order=(%+v,%v), want pre-modify state", gotTarget, ok)
			}
		})
	}
}

func TestModifyConfirmedVenueAliasRotationCommitsAtomically(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, c, _ := testEngine(fake)
	target, err := e.Submit(context.Background(), testReq("modify-rotation"))
	if err != nil {
		t.Fatalf("submit target: %v", err)
	}
	j := newBlockingResultJournal()
	e.WithJournal(j)
	const replacementVenueOrderID = "modify-replacement-venue"
	fake.SetModifyResult(&model.Order{
		Request:      model.OrderRequest{ClientID: "venue-generated-replacement-client"},
		VenueOrderID: replacementVenueOrderID,
		Status:       enums.StatusNew,
	}, nil)

	type modifyResult struct {
		order *model.Order
		err   error
	}
	modifyDone := make(chan modifyResult, 1)
	go func() {
		order, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
		modifyDone <- modifyResult{order: order, err: err}
	}()

	select {
	case <-j.entered:
	case result := <-modifyDone:
		t.Fatalf("modify returned before result journal blocked: order=%+v err=%v", result.order, result.err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for modify result journal")
	}

	collision := model.Order{
		Request: model.OrderRequest{
			AccountID: "test", InstrumentID: execInst, ClientID: "modify-rotation-collision",
			Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100),
		},
		VenueOrderID: replacementVenueOrderID,
		Status:       enums.StatusNew,
	}
	collisionDone := make(chan error, 1)
	go func() { collisionDone <- c.UpsertOrderChecked(collision) }()

	var collisionErr error
	collisionReturnedBeforeCommit := false
	select {
	case collisionErr = <-collisionDone:
		collisionReturnedBeforeCommit = true
	case <-time.After(50 * time.Millisecond):
	}
	close(j.release)

	result := <-modifyDone
	if !collisionReturnedBeforeCommit {
		collisionErr = <-collisionDone
	}
	if collisionReturnedBeforeCommit {
		t.Fatal("conflicting replacement alias write completed while modify result was durably committing")
	}
	if result.err != nil || result.order == nil {
		t.Fatalf("modify result=(%+v,%v), want confirmed replacement", result.order, result.err)
	}
	if result.order.Request.ClientID != target.Request.ClientID || result.order.VenueOrderID != replacementVenueOrderID {
		t.Fatalf("modified order=%+v, want logical client %q and replacement venue id %q", result.order, target.Request.ClientID, replacementVenueOrderID)
	}
	if !errors.Is(collisionErr, cache.ErrOrderIdentityConflict) {
		t.Fatalf("conflicting replacement write err=%v, want identity conflict", collisionErr)
	}
	if _, ok := c.OrderByVenueOrderIDForAccount("test", target.VenueOrderID); ok {
		t.Fatalf("old venue order id %q still resolves after confirmed replacement", target.VenueOrderID)
	}
	cached, ok := c.OrderByVenueOrderIDForAccount("test", replacementVenueOrderID)
	if !ok || cached.Request.ClientID != target.Request.ClientID {
		t.Fatalf("replacement cache order=(%+v,%v), want original logical client", cached, ok)
	}
	if got := e.InFlightCount(); got != 0 {
		t.Fatalf("in-flight=%d, want confirmed modify resolved", got)
	}
	assertResultOutcome(t, j.MemoryJournal, exec.OutcomeConfirmedAccepted)
}

func TestConcurrentCancelUsesCommittedReplacementAlias(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, _, _ := testEngine(fake)
	target, err := e.Submit(context.Background(), testReq("modify-then-cancel"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	const replacementVenueOrderID = "modify-then-cancel-replacement"
	fake.SetModifyResult(&model.Order{VenueOrderID: replacementVenueOrderID, Status: enums.StatusNew}, nil)
	resultJournal := newBlockingResultJournal()
	e.WithJournal(resultJournal)

	modifyDone := make(chan error, 1)
	go func() {
		_, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
		modifyDone <- err
	}()
	select {
	case <-resultJournal.entered:
	case <-time.After(time.Second):
		t.Fatal("modify did not reach durable result commit")
	}

	cancelVenue := make(chan string, 1)
	fake.OnCancel(func(_ model.InstrumentID, venueOrderID string) { cancelVenue <- venueOrderID })
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- e.Cancel(context.Background(), target.Request.ClientID) }()
	select {
	case venueOrderID := <-cancelVenue:
		t.Fatalf("cancel crossed venue boundary early with alias %q", venueOrderID)
	case <-time.After(50 * time.Millisecond):
	}

	close(resultJournal.release)
	if err := <-modifyDone; err != nil {
		t.Fatalf("modify: %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case venueOrderID := <-cancelVenue:
		if venueOrderID != replacementVenueOrderID {
			t.Fatalf("cancel venue order id=%q, want committed replacement %q", venueOrderID, replacementVenueOrderID)
		}
	default:
		t.Fatal("cancel did not cross venue boundary")
	}
}

func TestModifyVenueAliasRotationDoesNotCarryOldAliasTerminalState(t *testing.T) {
	tests := []struct {
		name      string
		status    enums.OrderStatus
		filledQty decimal.Decimal
	}{
		{name: "canceled", status: enums.StatusCanceled},
		{name: "filled", status: enums.StatusFilled, filledQty: decimal.NewFromInt(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			fake.SetModifySupported(true)
			e, c, _ := testEngine(fake)
			target, err := e.Submit(context.Background(), testReq("modify-old-terminal-"+tt.name))
			if err != nil {
				t.Fatalf("submit target: %v", err)
			}
			oldTerminal := *target
			oldTerminal.Status = tt.status
			oldTerminal.FilledQty = tt.filledQty
			if tt.filledQty.IsPositive() {
				oldTerminal.AvgFillPrice = decimal.NewFromInt(100)
			}
			oldTerminal.UpdatedAt = target.UpdatedAt.Add(time.Second)
			if err := c.UpsertOrderChecked(oldTerminal); err != nil {
				t.Fatalf("cache old terminal event: %v", err)
			}

			replacementVenueOrderID := "replacement-after-old-" + tt.name
			fake.SetModifyResult(&model.Order{
				VenueOrderID: replacementVenueOrderID,
				Status:       enums.StatusNew,
				UpdatedAt:    oldTerminal.UpdatedAt.Add(time.Second),
			}, nil)
			modified, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
			if err != nil {
				t.Fatalf("modify: %v", err)
			}
			if modified == nil || modified.Status != enums.StatusNew || !modified.FilledQty.IsZero() || !modified.AvgFillPrice.IsZero() {
				t.Fatalf("modified replacement=%+v, want independent NEW venue lifecycle", modified)
			}
			cached, ok := c.OrderByVenueOrderIDForAccount("test", replacementVenueOrderID)
			if !ok || cached.Status != enums.StatusNew || !cached.FilledQty.IsZero() || !cached.AvgFillPrice.IsZero() {
				t.Fatalf("cached replacement=(%+v,%v), old alias terminal state leaked", cached, ok)
			}
			if _, ok := c.OrderByVenueOrderIDForAccount("test", target.VenueOrderID); ok {
				t.Fatalf("old venue alias %q still resolves after replacement", target.VenueOrderID)
			}
			open := c.OpenOrders()
			if len(open) != 1 || open[0].VenueOrderID != replacementVenueOrderID {
				t.Fatalf("open orders=%+v, replacement was hidden by old terminal state", open)
			}
		})
	}
}

func TestModifyVenueAliasRotationReturnsCanonicalReplacementEvidence(t *testing.T) {
	tests := []struct {
		name          string
		venueEvidence model.Order
		wantStatus    enums.OrderStatus
		wantFilled    decimal.Decimal
	}{
		{
			name: "confirmed terms override sparse placeholder",
			venueEvidence: model.Order{
				Status: enums.StatusUnknown,
				Request: model.OrderRequest{
					Price:    decimal.NewFromInt(1),
					Quantity: decimal.NewFromInt(99),
				},
			},
			wantStatus: enums.StatusNew,
			wantFilled: decimal.Zero,
		},
		{
			name: "cumulative fill evidence survives acknowledgement",
			venueEvidence: model.Order{
				Status:       enums.StatusPartiallyFilled,
				FilledQty:    decimal.RequireFromString("0.5"),
				AvgFillPrice: decimal.NewFromInt(101),
				UpdatedAt:    time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC),
			},
			wantStatus: enums.StatusPartiallyFilled,
			wantFilled: decimal.RequireFromString("0.5"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			fake.SetModifySupported(true)
			e, c, _ := testEngine(fake)
			target, err := e.Submit(context.Background(), testReq("modify-canonical-"+strings.ReplaceAll(tt.name, " ", "-")))
			if err != nil {
				t.Fatalf("submit: %v", err)
			}

			const replacementVenueOrderID = "modify-canonical-replacement"
			venueEvidence := tt.venueEvidence
			venueEvidence.Request.AccountID = "test"
			venueEvidence.Request.InstrumentID = execInst
			venueEvidence.Request.Side = enums.SideBuy
			venueEvidence.VenueOrderID = replacementVenueOrderID
			if err := c.UpsertOrderChecked(venueEvidence); err != nil {
				t.Fatalf("seed replacement venue evidence: %v", err)
			}

			newPrice := decimal.NewFromInt(101)
			newQty := decimal.NewFromInt(2)
			fake.SetModifyResult(&model.Order{
				VenueOrderID: replacementVenueOrderID,
				Status:       enums.StatusNew,
				UpdatedAt:    time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC),
			}, nil)
			modified, err := e.Modify(context.Background(), target.Request.ClientID, newPrice, newQty)
			if err != nil || modified == nil {
				t.Fatalf("modify order=%+v err=%v", modified, err)
			}
			cached, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
			if !ok {
				t.Fatal("cache missing replacement order")
			}
			for label, got := range map[string]model.Order{"return": *modified, "cache": cached} {
				if got.VenueOrderID != replacementVenueOrderID || got.Status != tt.wantStatus ||
					!got.FilledQty.Equal(tt.wantFilled) || !got.Request.Price.Equal(newPrice) || !got.Request.Quantity.Equal(newQty) {
					t.Fatalf("%s order=%+v, want canonical alias/status/fill/amended terms", label, got)
				}
			}
		})
	}
}

func TestModifyStructuredVenueRejectionReturnsErrorAndPreservesOrder(t *testing.T) {
	tests := []struct {
		name        string
		status      enums.OrderStatus
		replacement bool
	}{
		{name: "rejected without replacement alias", status: enums.StatusRejected},
		{name: "expired with replacement alias", status: enums.StatusExpired, replacement: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			fake.SetModifySupported(true)
			e, c, j := testEngine(fake)
			target, err := e.Submit(context.Background(), testReq("modify-structured-reject-"+strings.ReplaceAll(tt.name, " ", "-")))
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			response := &model.Order{Status: tt.status, RejectReason: "venue rejected replacement"}
			if tt.replacement {
				response.VenueOrderID = "rejected-replacement-alias"
			}
			fake.SetModifyResult(response, nil)

			modified, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
			if modified != nil || !errors.Is(err, exec.ErrVenueRejected) {
				t.Fatalf("modify order=%+v err=%v, want definitive venue rejection", modified, err)
			}
			cached, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
			if !ok || cached.VenueOrderID != target.VenueOrderID || cached.Status != target.Status ||
				!cached.Request.Price.Equal(target.Request.Price) || !cached.Request.Quantity.Equal(target.Request.Quantity) {
				t.Fatalf("cached order=(%+v,%v), rejected modify must preserve original order", cached, ok)
			}
			if tt.replacement {
				if _, ok := c.OrderByVenueOrderIDForAccount("test", response.VenueOrderID); ok {
					t.Fatalf("rejected replacement alias %q became canonical", response.VenueOrderID)
				}
			}
			if got := e.InFlightCount(); got != 0 {
				t.Fatalf("in-flight=%d, definitive rejection must resolve intent", got)
			}
			assertLastResultOutcome(t, j, exec.OutcomeDefinitiveVenueRejected)
		})
	}
}

func TestModifyFirstDefinitiveResultWinsOverLateAdapterResult(t *testing.T) {
	tests := []struct {
		name              string
		eventAccepted     bool
		lateResponse      *model.Order
		wantErr           bool
		wantPrice         decimal.Decimal
		wantQty           decimal.Decimal
		wantStatus        enums.OrderStatus
		wantJournalResult exec.OutcomeClass
	}{
		{
			name:              "accepted event beats late structured rejection",
			eventAccepted:     true,
			lateResponse:      &model.Order{Status: enums.StatusRejected, RejectReason: "late reject"},
			wantPrice:         decimal.NewFromInt(101),
			wantQty:           decimal.NewFromInt(2),
			wantStatus:        enums.StatusNew,
			wantJournalResult: exec.OutcomeConfirmedAccepted,
		},
		{
			name:              "rejected event beats late acceptance",
			lateResponse:      &model.Order{Status: enums.StatusNew},
			wantErr:           true,
			wantPrice:         decimal.NewFromInt(100),
			wantQty:           decimal.NewFromInt(1),
			wantStatus:        enums.StatusRejected,
			wantJournalResult: exec.OutcomeDefinitiveVenueRejected,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runtimetest.NewFakeExec()
			fake.SetModifySupported(true)
			e, c, j := testEngine(fake)
			target, err := e.Submit(context.Background(), testReq("first-result-"+strings.ReplaceAll(tt.name, " ", "-")))
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			newPrice := decimal.NewFromInt(101)
			newQty := decimal.NewFromInt(2)
			late := *tt.lateResponse
			late.VenueOrderID = target.VenueOrderID
			fake.SetModifyResult(&late, nil)
			fake.OnModify(func(_ model.InstrumentID, _ string, _, _ decimal.Decimal) {
				canonical, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
				if !ok {
					t.Fatal("cache missing order during modify")
				}
				if tt.eventAccepted {
					canonical.Request.Price = newPrice
					canonical.Request.Quantity = newQty
					canonical.Status = enums.StatusNew
				} else {
					canonical.Status = enums.StatusRejected
					canonical.RejectReason = "authoritative reject"
				}
				canonical.UpdatedAt = canonical.UpdatedAt.Add(time.Second)
				if err := c.UpsertOrderChecked(canonical); err != nil {
					t.Fatalf("apply authoritative event: %v", err)
				}
				if resolved := e.ResolveOrderInFlight(canonical, canonical.UpdatedAt); !resolved {
					t.Fatal("authoritative event did not resolve pending modify")
				}
			})

			modified, err := e.Modify(context.Background(), target.Request.ClientID, newPrice, newQty)
			if tt.wantErr {
				if modified != nil || !errors.Is(err, exec.ErrVenueRejected) {
					t.Fatalf("modify order=%+v err=%v, want first authoritative rejection", modified, err)
				}
			} else if err != nil || modified == nil {
				t.Fatalf("modify order=%+v err=%v, want first authoritative acceptance", modified, err)
			}
			cached, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
			if !ok || cached.Status != tt.wantStatus || !cached.Request.Price.Equal(tt.wantPrice) || !cached.Request.Quantity.Equal(tt.wantQty) {
				t.Fatalf("cached order=(%+v,%v), late adapter result overwrote first definitive result", cached, ok)
			}
			results := commandResultsForType(t, j.Records(), journal.CommandModify)
			if len(results) != 1 || results[0].Outcome != string(tt.wantJournalResult) {
				t.Fatalf("modify results=%+v, want exactly one %s result", results, tt.wantJournalResult)
			}
		})
	}
}

func TestFixedClockRepeatedModifyCommandsHaveDistinctIntentIDs(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, _, j := testEngine(fake)
	target, err := e.Submit(context.Background(), testReq("fixed-clock-repeat-modify"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var calls atomic.Int32
	fake.OnModify(func(model.InstrumentID, string, decimal.Decimal, decimal.Decimal) { calls.Add(1) })
	for i, price := range []int64{101, 102} {
		if _, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(price), decimal.NewFromInt(2)); err != nil {
			t.Fatalf("modify %d: %v", i+1, err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("venue modify calls=%d, want 2", got)
	}
	intents := commandIntentsForType(t, j.Records(), journal.CommandModify)
	results := commandResultsForType(t, j.Records(), journal.CommandModify)
	if len(intents) != 2 || len(results) != 2 {
		t.Fatalf("modify intents=%+v results=%+v, want two durable command pairs", intents, results)
	}
	if intents[0].RecordID == intents[1].RecordID || intents[0].CommandID == intents[1].CommandID {
		t.Fatalf("repeated modify IDs collided: first=%+v second=%+v", intents[0], intents[1])
	}
}

func TestFixedClockCommandsRemainDistinctAcrossEngineRecreation(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	c := cache.New()
	j := journal.NewMemory()
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	request := testReq("fixed-clock-engine-recreation")
	initial := model.Order{
		Request:      request,
		VenueOrderID: "fixed-clock-engine-recreation-venue",
		Status:       enums.StatusNew,
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	}
	if err := c.UpsertOrderChecked(initial); err != nil {
		t.Fatal(err)
	}

	first := exec.New(fake, c, clk, "test").WithJournal(j)
	second := exec.New(fake, c, clk, "test").WithJournal(j)
	if _, err := first.Modify(context.Background(), request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2)); err != nil {
		t.Fatalf("first engine modify: %v", err)
	}
	if _, err := second.Modify(context.Background(), request.ClientID, decimal.NewFromInt(102), decimal.NewFromInt(3)); err != nil {
		t.Fatalf("recreated engine modify: %v", err)
	}

	intents := commandIntentsForType(t, j.Records(), journal.CommandModify)
	if len(intents) != 2 {
		t.Fatalf("modify intents=%+v, want two durable commands", intents)
	}
	if intents[0].CommandID == intents[1].CommandID || intents[0].RecordID == intents[1].RecordID {
		t.Fatalf("engine recreation reused command identity: first=%+v second=%+v", intents[0], intents[1])
	}
}

func TestResultJournalMayReadCacheWithoutDeadlock(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, c, _ := testEngine(fake)
	target, err := e.Submit(context.Background(), testReq("journal-cache-read"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	store := &cacheReadingResultJournal{
		MemoryJournal: journal.NewMemory(),
		cache:         c,
		accountID:     "test",
		clientID:      target.Request.ClientID,
		observed:      make(chan model.Order, 1),
	}
	e.WithJournal(store)

	done := make(chan error, 1)
	go func() {
		_, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("modify: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("journal cache read deadlocked behind the cache transaction")
	}
	select {
	case observed := <-store.observed:
		if observed.Request.ClientID != target.Request.ClientID {
			t.Fatalf("journal observed order=%+v, want target order", observed)
		}
	default:
		t.Fatal("journal did not observe cache state")
	}
}

func TestModifyVenueAliasRotationJournalFailurePreservesOldCacheAndInFlight(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, c, _ := testEngine(fake)
	target, err := e.Submit(context.Background(), testReq("modify-rotation-journal-fail"))
	if err != nil {
		t.Fatalf("submit target: %v", err)
	}
	const replacementVenueOrderID = "modify-replacement-journal-fail"
	fake.SetModifyResult(&model.Order{VenueOrderID: replacementVenueOrderID, Status: enums.StatusNew}, nil)
	fail := errors.New("modify result fsync failed")
	store := journal.NewMemory()
	e.WithJournal(&failingJournal{Store: store, failResult: fail})
	breaches := 0
	e.WithRecoverabilityHandler(func(err error) {
		if errors.Is(err, fail) {
			_, _ = c.OrderByClientIDForAccount("test", target.Request.ClientID)
			breaches++
		}
	})

	modified, err := e.Modify(context.Background(), target.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2))
	if modified != nil || !errors.Is(err, fail) {
		t.Fatalf("modify order=%+v err=%v, want durable result failure", modified, err)
	}
	if breaches != 1 {
		t.Fatalf("recoverability breaches=%d, want exactly one", breaches)
	}
	cached, ok := c.OrderByClientIDForAccount("test", target.Request.ClientID)
	if !ok || cached.VenueOrderID != target.VenueOrderID || !cached.Request.Price.Equal(target.Request.Price) {
		t.Fatalf("cached order=(%+v,%v), want original pre-modify state", cached, ok)
	}
	if _, ok := c.OrderByVenueOrderIDForAccount("test", replacementVenueOrderID); ok {
		t.Fatalf("replacement venue order id %q became visible despite result journal failure", replacementVenueOrderID)
	}
	open := e.OpenInFlight()
	if len(open) != 1 || open[0].State != exec.InFlightPendingModify || open[0].Intent.VenueOrderID != target.VenueOrderID {
		t.Fatalf("open in-flight=%+v, want original pending modify retained", open)
	}
	if records := store.Records(); len(records) != 1 || records[0].Type != journal.RecordCommandIntent {
		t.Fatalf("journal records=%+v, want only modify intent", records)
	}
}

func TestMatchFillInFlightIsReadOnlyAndValidatesDirectAliasIdentity(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, _, _ := testEngine(fake)
	inflight := exec.NewInFlightJournal()
	inflight.TrackIntent(journal.CommandIntent{
		RecordID:     "intent-read-only-match",
		CommandID:    "command-read-only-match",
		Type:         journal.CommandCancel,
		ClientID:     "client-read-only-match",
		VenueOrderID: "venue-read-only-match",
		AccountID:    "test",
		InstrumentID: execInst,
		Side:         enums.SideBuy,
		Quantity:     decimal.NewFromInt(1),
	}, exec.InFlightPendingCancel)
	e.WithInFlightJournal(inflight)

	wrongInstrument := model.InstrumentID{Venue: "FAKE", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	if _, ok := e.MatchFillInFlight(model.Fill{
		AccountID: "test", InstrumentID: wrongInstrument, VenueOrderID: "venue-read-only-match",
		Side: enums.SideBuy, Quantity: decimal.NewFromInt(1),
	}); ok {
		t.Fatal("direct venue alias matched a conflicting instrument")
	}
	matched, ok := e.MatchFillInFlight(model.Fill{
		AccountID: "test", InstrumentID: execInst, VenueOrderID: "venue-read-only-match",
		Side: enums.SideBuy, Quantity: decimal.NewFromInt(1),
	})
	if !ok || matched.ClientID != "client-read-only-match" {
		t.Fatalf("matched=(%+v,%v), want identity-enriched fill", matched, ok)
	}
	if got := e.InFlightCount(); got != 1 {
		t.Fatalf("read-only match consumed in-flight intent: %d", got)
	}
}

func TestCancelConfirmedOverridesFutureDatedCachedStatus(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	e, c, _ := testEngine(fake)
	order, err := e.Submit(context.Background(), testReq("cancel-future-cache"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	future, ok := c.Order(order.Request.ClientID)
	if !ok {
		t.Fatal("cache missing submitted order")
	}
	future.Status = enums.StatusUnknown
	future.UpdatedAt = future.UpdatedAt.Add(time.Minute)
	c.UpsertOrder(future)

	if err := e.Cancel(context.Background(), order.Request.ClientID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cached, ok := c.Order(order.Request.ClientID)
	if !ok {
		t.Fatal("cache missing canceled order")
	}
	if cached.Status != enums.StatusCanceled {
		t.Fatalf("status=%s, want CANCELED after confirmed venue cancellation", cached.Status)
	}
}

func TestCancelPersistsConfirmedResultAfterCallerContextCanceledByAdapter(t *testing.T) {
	fake := &cancelBeforeReturnExec{FakeExec: runtimetest.NewFakeExec()}
	e, c, _ := testEngine(fake)
	order, err := e.Submit(context.Background(), testReq("cancel-canceled-on-return"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	resultJournal := newDetachedResultJournal()
	e.WithJournal(resultJournal)
	ctx, cancel := context.WithCancel(context.Background())
	fake.cancel = cancel
	fake.onCancel = true

	if err := e.Cancel(ctx, order.Request.ClientID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	resultJournal.assertResultContext(t)
	assertResultOutcome(t, resultJournal.MemoryJournal, exec.OutcomeConfirmedAccepted)
	cached, ok := c.Order(order.Request.ClientID)
	if !ok || cached.Status != enums.StatusCanceled {
		t.Fatalf("cached ok=%v status=%s, want CANCELED", ok, cached.Status)
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

func TestConfirmedTerminalModifyNotifiesTerminalOrderHandler(t *testing.T) {
	fake := runtimetest.NewFakeExec()
	fake.SetModifySupported(true)
	e, _, _ := testEngine(fake)
	var terminal []model.Order
	e.WithTerminalOrderHandler(func(order model.Order) { terminal = append(terminal, order) })
	order, err := e.Submit(context.Background(), testReq("modify-terminal-hook"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fake.SetModifyResult(&model.Order{
		VenueOrderID: order.VenueOrderID,
		Status:       enums.StatusCanceled,
		UpdatedAt:    time.Unix(101, 0),
	}, nil)
	if _, err := e.Modify(context.Background(), order.Request.ClientID, decimal.NewFromInt(101), decimal.NewFromInt(2)); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if len(terminal) != 1 || terminal[0].Status != enums.StatusCanceled || terminal[0].Request.ClientID != order.Request.ClientID {
		t.Fatalf("terminal notifications=%+v, want terminal modify order", terminal)
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

func TestModifyPersistsConfirmedResultAfterCallerContextCanceledByAdapter(t *testing.T) {
	fake := &cancelBeforeReturnExec{FakeExec: runtimetest.NewFakeExec()}
	fake.SetModifySupported(true)
	e, c, _ := testEngine(fake)
	order, err := e.Submit(context.Background(), testReq("modify-canceled-on-return"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	resultJournal := newDetachedResultJournal()
	e.WithJournal(resultJournal)
	ctx, cancel := context.WithCancel(context.Background())
	fake.cancel = cancel
	fake.onModify = true

	newPrice := decimal.NewFromInt(101)
	newQty := decimal.NewFromInt(2)
	modified, err := e.Modify(ctx, order.Request.ClientID, newPrice, newQty)
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	if modified == nil {
		t.Fatal("modify returned nil order")
	}
	resultJournal.assertResultContext(t)
	assertResultOutcome(t, resultJournal.MemoryJournal, exec.OutcomeConfirmedAccepted)
	cached, ok := c.Order(order.Request.ClientID)
	if !ok || !cached.Request.Price.Equal(newPrice) || !cached.Request.Quantity.Equal(newQty) {
		t.Fatalf("cached ok=%v order=%+v, want amended price=%s quantity=%s", ok, cached, newPrice, newQty)
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

type countingExec struct {
	*runtimetest.FakeExec
	validateCalls int
	submitCalls   int
}

type stagedDuplicateExec struct {
	*runtimetest.FakeExec
	validateCalls      atomic.Int32
	submitCalls        atomic.Int32
	firstAtValidation  chan struct{}
	secondAtValidation chan struct{}
	releaseFirst       chan struct{}
	firstSubmitDone    chan struct{}
}

func newStagedDuplicateExec() *stagedDuplicateExec {
	return &stagedDuplicateExec{
		FakeExec:           runtimetest.NewFakeExec(),
		firstAtValidation:  make(chan struct{}),
		secondAtValidation: make(chan struct{}),
		releaseFirst:       make(chan struct{}),
		firstSubmitDone:    make(chan struct{}),
	}
}

func (f *stagedDuplicateExec) ValidateSubmit(model.OrderRequest) error {
	switch f.validateCalls.Add(1) {
	case 1:
		close(f.firstAtValidation)
		<-f.releaseFirst
	case 2:
		close(f.secondAtValidation)
		<-f.firstSubmitDone
	}
	return nil
}

func (f *stagedDuplicateExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	call := f.submitCalls.Add(1)
	order, err := f.FakeExec.Submit(ctx, req)
	if call == 1 {
		close(f.firstSubmitDone)
	}
	return order, err
}

func (f *countingExec) ValidateSubmit(model.OrderRequest) error {
	f.validateCalls++
	return nil
}

func (f *countingExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	f.submitCalls++
	return f.FakeExec.Submit(ctx, req)
}

type countingCommandGate struct {
	submitCalls int
}

func (g *countingCommandGate) CanSubmit(model.OrderRequest) error {
	g.submitCalls++
	return nil
}

func (*countingCommandGate) CanCancel() error { return nil }
func (*countingCommandGate) CanModify() error { return nil }

type countingRisk struct {
	calls int
}

func (r *countingRisk) CheckSubmission(context.Context, model.OrderRequest, *model.Instrument) (func(), error) {
	r.calls++
	return nil, nil
}

type cancelBeforeReturnExec struct {
	*runtimetest.FakeExec
	cancel   context.CancelFunc
	onSubmit bool
	onCancel bool
	onModify bool
}

func (f *cancelBeforeReturnExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := f.FakeExec.Submit(ctx, req)
	if f.onSubmit {
		f.cancel()
	}
	return order, err
}

func (f *cancelBeforeReturnExec) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	err := f.FakeExec.Cancel(ctx, id, venueOrderID)
	if f.onCancel {
		f.cancel()
	}
	return err
}

func (f *cancelBeforeReturnExec) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	order, err := f.FakeExec.Modify(ctx, id, venueOrderID, newPrice, newQty)
	if f.onModify {
		f.cancel()
	}
	return order, err
}

type detachedResultJournal struct {
	*journal.MemoryJournal
	resultContexts int
	remaining      time.Duration
}

type blockingResultJournal struct {
	*journal.MemoryJournal
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type cacheReadingResultJournal struct {
	*journal.MemoryJournal
	cache     *cache.Cache
	accountID string
	clientID  string
	observed  chan model.Order
}

func (j *cacheReadingResultJournal) AppendCommandResult(ctx context.Context, result journal.CommandResult) error {
	order, ok := j.cache.OrderByClientIDForAccount(j.accountID, j.clientID)
	if !ok {
		return errors.New("result journal could not read target order from cache")
	}
	j.observed <- order
	return j.MemoryJournal.AppendCommandResult(ctx, result)
}

func newBlockingResultJournal() *blockingResultJournal {
	return &blockingResultJournal{
		MemoryJournal: journal.NewMemory(),
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
}

func (j *blockingResultJournal) AppendCommandResult(ctx context.Context, result journal.CommandResult) error {
	j.once.Do(func() { close(j.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-j.release:
	}
	return j.MemoryJournal.AppendCommandResult(ctx, result)
}

func newDetachedResultJournal() *detachedResultJournal {
	return &detachedResultJournal{MemoryJournal: journal.NewMemory()}
}

func (j *detachedResultJournal) AppendCommandResult(ctx context.Context, result journal.CommandResult) error {
	j.resultContexts++
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("command result context has no deadline")
	}
	j.remaining = time.Until(deadline)
	if j.remaining <= 0 || j.remaining > 5*time.Second {
		return errors.New("command result context deadline is outside the 5s bound")
	}
	return j.MemoryJournal.AppendCommandResult(ctx, result)
}

func (j *detachedResultJournal) assertResultContext(t *testing.T) {
	t.Helper()
	if j.resultContexts != 1 {
		t.Fatalf("result contexts=%d, want 1", j.resultContexts)
	}
	if j.remaining < 4*time.Second || j.remaining > 5*time.Second {
		t.Fatalf("result context remaining=%s, want approximately 5s", j.remaining)
	}
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

func commandIntentsForType(t *testing.T, records []journal.Record, commandType journal.CommandType) []journal.CommandIntent {
	t.Helper()
	var intents []journal.CommandIntent
	for _, record := range records {
		if record.Type != journal.RecordCommandIntent {
			continue
		}
		var intent journal.CommandIntent
		if err := json.Unmarshal(record.Payload, &intent); err != nil {
			t.Fatalf("decode command intent: %v", err)
		}
		if intent.Type == commandType {
			intents = append(intents, intent)
		}
	}
	return intents
}

func commandResultsForType(t *testing.T, records []journal.Record, commandType journal.CommandType) []journal.CommandResult {
	t.Helper()
	var results []journal.CommandResult
	for _, record := range records {
		if record.Type != journal.RecordCommandResult {
			continue
		}
		var result journal.CommandResult
		if err := json.Unmarshal(record.Payload, &result); err != nil {
			t.Fatalf("decode command result: %v", err)
		}
		if result.Type == commandType {
			results = append(results, result)
		}
	}
	return results
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

func assertLastResultOutcome(t *testing.T, j *journal.MemoryJournal, want exec.OutcomeClass) {
	t.Helper()
	records := j.Records()
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Type != journal.RecordCommandResult {
			continue
		}
		var result journal.CommandResult
		if err := json.Unmarshal(records[i].Payload, &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if result.Outcome != string(want) {
			t.Fatalf("last outcome=%s, want %s", result.Outcome, want)
		}
		return
	}
	t.Fatalf("no command result record in %+v", records)
}
