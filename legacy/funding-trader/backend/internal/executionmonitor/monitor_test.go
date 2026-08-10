package executionmonitor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

func TestMonitorTriggersSingleFlightRecoverForPrivateOrderFillPositionAndHealthEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway := &fakeGateway{streams: []*fakeStream{{
		events: []boltertrader.PrivateEvent{
			privateEvent("acct-1", 1, "order"),
			privateEvent("acct-1", 2, "fill"),
			privateEvent("acct-1", 3, "position"),
			privateEvent("acct-1", 4, "account_health"),
			privateEvent("acct-1", 5, "balance"),
		},
		endErr: context.Canceled,
	}}}
	reconciler := &blockingReconciler{entered: make(chan struct{}), release: make(chan struct{})}
	monitor := New(Options{
		Gateway: gateway, Reconciler: reconciler, Accounts: []string{"acct-1"},
		RecoverMinInterval: time.Hour, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- monitor.Run(ctx) }()
	<-reconciler.entered
	if got := reconciler.calls(); got != 1 {
		t.Fatalf("recover calls while first is running = %d, want single-flight 1", got)
	}
	close(reconciler.release)
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if len(gateway.requests) != 1 || gateway.requests[0].AccountID != "acct-1" {
		t.Fatalf("subscribe requests = %#v", gateway.requests)
	}
}

func TestMonitorResnapshotRecoversThenPublishesResnapshotRequiredWithScopeAndCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}
	resyncErr := &boltertrader.ResnapshotRequiredError{Reason: "sequence_gap raw-token"}
	gateway := &fakeGateway{streams: []*fakeStream{{endErr: resyncErr}, {endErr: context.Canceled}}}
	reconciler := &recordingReconciler{}
	publisher := &recordingResnapshotPublisher{}
	monitor := New(Options{
		Gateway: gateway, Reconciler: reconciler, ResnapshotPublisher: publisher,
		Accounts:       []string{"acct-1"},
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- monitor.Run(ctx) }()
	waitUntil(t, func() bool { return publisher.calls() == 1 })
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if reconciler.calls() == 0 {
		t.Fatal("resnapshot did not recover before publishing")
	}
	if got := publisher.payloads[0]; !sameStrings(got.Scope, []string{"executions", "positions", "activities"}) ||
		got.Reason == "" || strings.Contains(got.Reason, "raw-token") || strings.Contains(got.Reason, scope.PrincipalID) || strings.Contains(got.Reason, scope.Mode) {
		t.Fatalf("unsafe or incomplete resnapshot payload: %#v", got)
	}
	if len(gateway.requests) < 2 {
		t.Fatalf("monitor did not reconnect after resnapshot: %#v", gateway.requests)
	}
	if gateway.requests[1].Cursor != "" {
		t.Fatalf("resnapshot reconnect must clear account cursor, got requests %#v", gateway.requests)
	}
}

func TestMonitorReconnectsOnStreamEndOrErrorWithoutLeakingRawErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway := &fakeGateway{streams: []*fakeStream{{endErr: errors.New("transport secret=sk-raw")}, {events: []boltertrader.PrivateEvent{privateEvent("acct-1", 1, "order")}, endErr: context.Canceled}}}
	reconciler := &recordingReconciler{}
	monitor := New(Options{
		Gateway: gateway, Reconciler: reconciler, Accounts: []string{"acct-1"},
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- monitor.Run(ctx) }()
	waitUntil(t, func() bool { return reconciler.calls() == 1 })
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if len(gateway.requests) < 2 {
		t.Fatalf("monitor did not reconnect after stream error: %#v", gateway.requests)
	}
	if monitor.LastSafeError() != "gateway-private-stream" {
		t.Fatalf("safe error = %q", monitor.LastSafeError())
	}
}

func TestMonitorTracksPerAccountCursorsAndClearsOnlyResnapshotAccount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway := &fakeGateway{byAccount: map[string][]*fakeStream{
		"acct-1": {
			{events: []boltertrader.PrivateEvent{privateEvent("acct-1", 7, "order")}, endErr: errors.New("stream end")},
			{endErr: context.Canceled},
		},
		"acct-2": {
			{events: []boltertrader.PrivateEvent{privateEvent("acct-2", 5, "order")}, endErr: &boltertrader.ResnapshotRequiredError{Reason: "sequence_gap"}},
			{endErr: context.Canceled},
		},
	}}
	monitor := New(Options{
		Gateway: gateway, Reconciler: &recordingReconciler{}, ResnapshotPublisher: &recordingResnapshotPublisher{},
		Accounts:       []string{"acct-1", "acct-2"},
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- monitor.Run(ctx) }()
	waitUntil(t, func() bool { return gateway.requestCount("acct-1") >= 2 && gateway.requestCount("acct-2") >= 2 })
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	acct1 := gateway.requestsFor("acct-1")
	acct2 := gateway.requestsFor("acct-2")
	if acct1[1].Cursor != "018f0000-0000-7000-8000-000000000001:7" {
		t.Fatalf("acct-1 reconnect cursor = %q; requests=%#v", acct1[1].Cursor, acct1)
	}
	if acct2[1].Cursor != "" {
		t.Fatalf("acct-2 resnapshot reconnect cursor = %q; requests=%#v", acct2[1].Cursor, acct2)
	}
}

func TestMonitorDoesNotPublishResnapshotWhenConcurrentRecoveryFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recoverErr := errors.New("recover failed")
	gateway := &fakeGateway{streams: []*fakeStream{{endErr: &boltertrader.ResnapshotRequiredError{Reason: "sequence_gap"}}, {endErr: context.Canceled}}}
	reconciler := &recordingReconciler{err: recoverErr}
	publisher := &recordingResnapshotPublisher{}
	monitor := New(Options{
		Gateway: gateway, Reconciler: reconciler, ResnapshotPublisher: publisher,
		Accounts:       []string{"acct-1"},
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- monitor.Run(ctx) }()
	waitUntil(t, func() bool { return reconciler.calls() >= 1 && gateway.totalRequests() >= 2 })
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if publisher.calls() != 0 {
		t.Fatalf("published resnapshot after failed recovery: %#v", publisher.payloads)
	}
	if monitor.LastSafeError() != "gateway-private-resnapshot" {
		t.Fatalf("safe error = %q", monitor.LastSafeError())
	}
}

func TestMonitorAsyncRecoveryFailureKeepsExecutionContinuityUnready(t *testing.T) {
	reconciler := &recordingReconciler{err: errors.New("raw recovery failure")}
	monitor := New(Options{
		Gateway: &fakeGateway{}, Reconciler: reconciler, Accounts: []string{"acct-1"},
	})
	monitor.markSubscribed("acct-1")
	if !monitor.Readiness().Ready {
		t.Fatalf("subscription should initially be ready: %#v", monitor.Readiness())
	}

	monitor.triggerRecover(context.Background())
	waitUntil(t, func() bool {
		return reconciler.calls() == 1 && !monitor.Readiness().Ready
	})
	monitor.recoveryWG.Wait()

	if readiness := monitor.Readiness(); readiness.Ready || readiness.Reason != "gateway-private-recovery" {
		t.Fatalf("failed recovery readiness=%#v", readiness)
	}
	if monitor.LastSafeError() != "gateway-private-recovery" {
		t.Fatalf("unsafe recovery error state=%q", monitor.LastSafeError())
	}
}

func TestMonitorReadinessTracksSubscriptionsFailuresAndRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failNow := make(chan struct{})
	gateway := &fakeGateway{byAccount: map[string][]*fakeStream{
		"acct-1": {
			{events: []boltertrader.PrivateEvent{privateEvent("acct-1", 1, "order")}, beforeEnd: failNow, endErr: errors.New("stream failed")},
			{endErr: context.Canceled},
		},
		"acct-2": {
			{endErr: context.Canceled},
		},
	}}
	reconciler := &recordingReconciler{}
	monitor := New(Options{
		Gateway: gateway, Reconciler: reconciler, Accounts: []string{"acct-1", "acct-2"},
		InitialBackoff: 100 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
	})
	if readiness := monitor.Readiness(); readiness.Ready || readiness.Reason != "not-subscribed" {
		t.Fatalf("initial readiness = %#v", readiness)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- monitor.Run(ctx) }()
	waitUntil(t, func() bool { return monitor.Readiness().Ready })
	close(failNow)
	waitUntil(t, func() bool { return monitor.Readiness().Reason == "gateway-private-stream" })
	if readiness := monitor.Readiness(); readiness.Ready {
		t.Fatalf("stream failure readiness = %#v", readiness)
	}
	waitUntil(t, func() bool { return monitor.Readiness().Ready && gateway.requestCount("acct-1") >= 2 })
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
}

func TestMonitorReadinessExposesFreshAuthoritativeResnapshotDuringReconnect(t *testing.T) {
	monitor := New(Options{Gateway: &fakeGateway{}, Reconciler: &recordingReconciler{}, Accounts: []string{"acct-1"}})
	monitor.markSubscribed("acct-1")
	monitor.markUnready("acct-1", "gateway-private-resnapshot")
	monitor.markRecoverySucceeded()
	got := monitor.Readiness()
	if got.Ready || !got.AuthoritativeResnapshotFresh || got.Reason != "gateway-private-resnapshot" {
		t.Fatalf("readiness=%+v", got)
	}
	monitor.markUnready("acct-1", "gateway-private-stream")
	if monitor.Readiness().AuthoritativeResnapshotFresh {
		t.Fatalf("ordinary stream failure retained stale authoritative resnapshot: %+v", monitor.Readiness())
	}
}

func TestMonitorMergesEventsDuringRecoveryIntoOneFollowUpRecoveryAndWaitsOnExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &fakeStream{
		events: []boltertrader.PrivateEvent{
			privateEvent("acct-1", 1, "order"),
			privateEvent("acct-1", 2, "fill"),
			privateEvent("acct-1", 3, "position"),
		},
		endErr: context.Canceled,
	}
	gateway := &fakeGateway{streams: []*fakeStream{stream}}
	reconciler := &stepReconciler{
		entered: make(chan int, 2),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	monitor := New(Options{
		Gateway: gateway, Reconciler: reconciler, Accounts: []string{"acct-1"},
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- monitor.Run(ctx) }()
	if got := <-reconciler.entered; got != 1 {
		t.Fatalf("first recovery index = %d", got)
	}
	waitUntil(t, func() bool { return reconciler.calls() == 1 })
	close(reconciler.release)
	if got := <-reconciler.entered; got != 2 {
		t.Fatalf("follow-up recovery index = %d", got)
	}
	cancel()
	select {
	case <-errCh:
		t.Fatal("Run returned before recovery goroutine finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(reconciler.done)
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if got := reconciler.calls(); got != 2 {
		t.Fatalf("recoveries = %d, want exactly merged pair", got)
	}
}

func privateEvent(account string, sequence uint64, kind string) boltertrader.PrivateEvent {
	return boltertrader.PrivateEvent{
		BootID: "018f0000-0000-7000-8000-000000000001", Sequence: sequence, EventID: "event-" + kind,
		AccountID: account, ResourceVersion: int64(sequence), Kind: kind, OccurredAt: time.Unix(int64(sequence), 0).UTC(),
		Payload: []byte(`{"ok":true}`),
	}
}

type fakeGateway struct {
	mu        sync.Mutex
	streams   []*fakeStream
	byAccount map[string][]*fakeStream
	requests  []boltertrader.PrivateEventSubscribeRequest
}

func (gateway *fakeGateway) SubscribePrivateEvents(_ context.Context, request boltertrader.PrivateEventSubscribeRequest) (PrivateStream, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.requests = append(gateway.requests, request)
	if len(gateway.byAccount) != 0 {
		streams := gateway.byAccount[request.AccountID]
		if len(streams) == 0 {
			return &fakeStream{endErr: context.Canceled}, nil
		}
		stream := streams[0]
		gateway.byAccount[request.AccountID] = streams[1:]
		return stream, nil
	}
	if len(gateway.streams) == 0 {
		return &fakeStream{endErr: context.Canceled}, nil
	}
	stream := gateway.streams[0]
	gateway.streams = gateway.streams[1:]
	return stream, nil
}

func (gateway *fakeGateway) requestCount(accountID string) int {
	return len(gateway.requestsFor(accountID))
}

func (gateway *fakeGateway) totalRequests() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return len(gateway.requests)
}

func (gateway *fakeGateway) requestsFor(accountID string) []boltertrader.PrivateEventSubscribeRequest {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	var result []boltertrader.PrivateEventSubscribeRequest
	for _, request := range gateway.requests {
		if request.AccountID == accountID {
			result = append(result, request)
		}
	}
	return result
}

type fakeStream struct {
	events    []boltertrader.PrivateEvent
	beforeEnd chan struct{}
	endErr    error
	closed    bool
}

func (stream *fakeStream) Next(context.Context) (boltertrader.PrivateEvent, error) {
	if len(stream.events) == 0 {
		if stream.beforeEnd != nil {
			<-stream.beforeEnd
			stream.beforeEnd = nil
		}
		return boltertrader.PrivateEvent{}, stream.endErr
	}
	next := stream.events[0]
	stream.events = stream.events[1:]
	return next, nil
}

func (stream *fakeStream) Close() error {
	stream.closed = true
	return nil
}

type blockingReconciler struct {
	mu      sync.Mutex
	n       int
	entered chan struct{}
	release chan struct{}
}

func (reconciler *blockingReconciler) Recover(context.Context) error {
	reconciler.mu.Lock()
	reconciler.n++
	if reconciler.n == 1 {
		close(reconciler.entered)
	}
	reconciler.mu.Unlock()
	<-reconciler.release
	return nil
}

func (reconciler *blockingReconciler) calls() int {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.n
}

type recordingReconciler struct {
	mu  sync.Mutex
	n   int
	err error
}

func (reconciler *recordingReconciler) Recover(context.Context) error {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.n++
	return reconciler.err
}

func (reconciler *recordingReconciler) calls() int {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.n
}

type recordingResnapshotPublisher struct {
	mu       sync.Mutex
	payloads []Resnapshot
}

func (publisher *recordingResnapshotPublisher) PublishResnapshotRequired(_ context.Context, payload Resnapshot) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.payloads = append(publisher.payloads, payload)
	return nil
}

func (publisher *recordingResnapshotPublisher) calls() int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return len(publisher.payloads)
}

type stepReconciler struct {
	mu      sync.Mutex
	n       int
	entered chan int
	release chan struct{}
	done    chan struct{}
}

func (reconciler *stepReconciler) Recover(context.Context) error {
	reconciler.mu.Lock()
	reconciler.n++
	n := reconciler.n
	reconciler.mu.Unlock()
	reconciler.entered <- n
	if n == 1 {
		<-reconciler.release
		return nil
	}
	<-reconciler.done
	return nil
}

func (reconciler *stepReconciler) calls() int {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.n
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		case <-ticker.C:
			if ok() {
				return
			}
		}
	}
}
