package uistream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

func TestFirstConnectionEmitsAuthoritativeSnapshotWithOpaqueCursor(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour})
	service.source.(*testSnapshotSource).snapshots["portfolio"] = json.RawMessage(`{"positions":[{"symbol":"BTC"}]}`)

	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runServe(t, service, ctx, sink, Request{Scope: "portfolio"})

	first := sink.waitFor(t, "snapshot")
	if first.Scope != "portfolio" || string(first.Data) != `{"positions":[{"symbol":"BTC"}]}` {
		t.Fatalf("snapshot=%+v", first)
	}
	if first.ContractVersion != readmodel.ContractVersion {
		t.Fatalf("contractVersion=%q", first.ContractVersion)
	}
	if first.PrincipalID != "principal-default" || first.Mode != "test" {
		t.Fatalf("stream scope principal=%q mode=%q", first.PrincipalID, first.Mode)
	}
	if first.Cursor == "" || containsAny(first.Cursor, "portfolio", "BTC") {
		t.Fatalf("cursor is not opaque: %q", first.Cursor)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(encoded), `"contractVersion":"`+readmodel.ContractVersion+`"`) || contains(string(encoded), `"environment"`) {
		t.Fatalf("stream event contract fields are wrong: %s", encoded)
	}

	cancel()
	waitDone(t, done)
	if service.SubscriberCount() != 0 {
		t.Fatalf("subscriber leak: %d", service.SubscriberCount())
	}
}

func TestDefaultReplayLimitFitsPersistentStoreContract(t *testing.T) {
	service := newTestService(t, ServiceConfig{})
	if service.replayLimit != defaultReplayLimit {
		t.Fatalf("default replay limit=%d, want %d", service.replayLimit, defaultReplayLimit)
	}
}

func TestReconnectWithSameBootCursorReplaysAfterCursorThenSubscribesLive(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour})
	ctx := context.Background()
	first, _ := publishTestEvent(t, service, ctx, "orders", "order.created", `{"id":"1"}`)
	cursor := first.Cursor
	second, _ := publishTestEvent(t, service, ctx, "orders", "order.filled", `{"id":"2"}`)

	sink := newRecordingSink()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := runServe(t, service, serveCtx, sink, Request{Scope: "orders", Cursor: cursor})

	replayed := sink.waitFor(t, "event")
	if replayed.Cursor != second.Cursor || replayed.Kind != "order.filled" || string(replayed.Data) != `{"id":"2"}` {
		t.Fatalf("replayed=%+v want cursor %q", replayed, second.Cursor)
	}
	live, _ := publishTestEvent(t, service, ctx, "orders", "order.cancelled", `{"id":"3"}`)
	gotLive := sink.waitFor(t, "event")
	if gotLive.Cursor != live.Cursor || gotLive.Kind != "order.cancelled" {
		t.Fatalf("live=%+v want cursor %q", gotLive, live.Cursor)
	}

	cancel()
	waitDone(t, done)
}

func TestInvalidStaleOtherBootAndRetentionGapCursorsRequireBrowserResnapshot(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour})
	service.source.(*testSnapshotSource).snapshots["orders"] = json.RawMessage(`{"orders":[]}`)
	ctx := context.Background()
	event, _ := publishTestEvent(t, service, ctx, "orders", "order.created", `{"id":"1"}`)

	otherBoot := newTestService(t, ServiceConfig{BootID: "boot-b", HeartbeatInterval: time.Hour, SigningKey: testSigningKey()})
	otherEvent, _ := publishTestEvent(t, otherBoot, ctx, "orders", "order.created", `{"id":"1"}`)
	otherMode := newTestService(t, ServiceConfig{Mode: "live", HeartbeatInterval: time.Hour, SigningKey: testSigningKey()})
	otherModeEvent, _ := publishTestEvent(t, otherMode, ctx, "orders", "order.created", `{"id":"1"}`)
	otherPrincipal := newTestService(t, ServiceConfig{PrincipalID: "principal-other", HeartbeatInterval: time.Hour, SigningKey: testSigningKey()})
	otherPrincipalEvent, _ := publishTestEvent(t, otherPrincipal, ctx, "orders", "order.created", `{"id":"1"}`)

	gapCursor := service.mustEncodeCursor(t, "orders", 1)
	legacyCursor := service.mustEncodeLegacyV1Cursor(t, "orders", 1)
	service.store.(*MemoryEventStore).dropBefore("orders", 3)
	_, _ = publishTestEvent(t, service, ctx, "orders", "order.filled", `{"id":"2"}`)
	_, _ = publishTestEvent(t, service, ctx, "orders", "order.cancelled", `{"id":"3"}`)

	for name, cursor := range map[string]string{
		"tampered":        tamper(event.Cursor),
		"other boot":      otherEvent.Cursor,
		"other mode":      otherModeEvent.Cursor,
		"other principal": otherPrincipalEvent.Cursor,
		"legacy v1":       legacyCursor,
		"retention gap":   gapCursor,
	} {
		t.Run(name, func(t *testing.T) {
			sink := newRecordingSink()
			serveCtx, cancel := context.WithCancel(ctx)
			done := runServe(t, service, serveCtx, sink, Request{Scope: "orders", Cursor: cursor})
			got := sink.waitFor(t, "resnapshot.required")
			if got.Scope != "orders" || got.ContractVersion != readmodel.ContractVersion || got.Cursor == "" || got.Cursor == cursor || string(got.Data) != `{"scope":["orders"]}` {
				t.Fatalf("resnapshot event=%+v cursor=%q", got, cursor)
			}
			if got.Name == "resync_required" {
				t.Fatal("browser event leaked raw upstream resync_required name")
			}
			cancel()
			waitDone(t, done)
		})
	}
}

func TestDuplicateCursorReplayIsSafeAndKindFilteringIsRepeatable(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour})
	ctx := context.Background()
	first, _ := publishTestEvent(t, service, ctx, "orders", "order.created", `{"id":"1"}`)
	_, _ = publishTestEvent(t, service, ctx, "orders", "fill.created", `{"id":"f1"}`)
	secondOrder, _ := publishTestEvent(t, service, ctx, "orders", "order.filled", `{"id":"2"}`)

	for i := 0; i < 2; i++ {
		sink := newRecordingSink()
		serveCtx, cancel := context.WithCancel(ctx)
		done := runServe(t, service, serveCtx, sink, Request{
			Scope: "orders", Cursor: first.Cursor, Kinds: []string{"order.filled", "order.created", "order.filled"},
		})
		got := sink.waitFor(t, "event")
		if got.Cursor != secondOrder.Cursor || got.Kind != "order.filled" {
			t.Fatalf("attempt %d got %+v", i, got)
		}
		cancel()
		waitDone(t, done)
	}
}

func TestBoundedSubscriberBufferGapForcesResnapshot(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour, SubscriberBuffer: 1})
	ctx := context.Background()
	sink := newBlockingSink()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := runServe(t, service, serveCtx, sink, Request{Scope: "orders"})
	sink.waitFor(t, "snapshot")
	sink.block()

	_, _ = publishTestEvent(t, service, ctx, "orders", "order.created", `{"id":"1"}`)
	gap, _ := publishTestEvent(t, service, ctx, "orders", "order.filled", `{"id":"2"}`)

	sink.unblock()
	got := sink.waitFor(t, "resnapshot.required")
	if got.Scope != "orders" || got.Cursor != gap.Cursor || string(got.Data) != `{"scope":["orders"]}` {
		t.Fatalf("gap event=%+v want cursor %q", got, gap.Cursor)
	}
	cancel()
	waitDone(t, done)
}

func TestHeartbeatAndCancellationCloseWithoutLeaks(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: 5 * time.Millisecond})
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := runServe(t, service, ctx, sink, Request{Scope: "orders"})

	_ = sink.waitFor(t, "snapshot")
	heartbeat := sink.waitFor(t, "heartbeat")
	if heartbeat.Scope != "orders" || heartbeat.Cursor == "" {
		t.Fatalf("heartbeat=%+v", heartbeat)
	}
	if heartbeat.ContractVersion != readmodel.ContractVersion {
		t.Fatalf("heartbeat contractVersion=%q", heartbeat.ContractVersion)
	}
	cancel()
	waitDone(t, done)
	if service.SubscriberCount() != 0 {
		t.Fatalf("subscriber leak: %d", service.SubscriberCount())
	}
}

func TestUpstreamResyncRequiredMapsToBrowserResnapshotRequired(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour})
	ctx := context.Background()
	first, _ := publishTestEvent(t, service, ctx, "orders", "order.created", `{"id":"1"}`)
	_, _ = publishTestEvent(t, service, ctx, "orders", "order.filled", `{"id":"2"}`)
	service.store.(*MemoryEventStore).replayErr = ErrResyncRequired

	sink := newRecordingSink()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := runServe(t, service, serveCtx, sink, Request{Scope: "orders", Cursor: first.Cursor})
	got := sink.waitFor(t, "resnapshot.required")
	if got.Name != "resnapshot.required" || got.Scope != "orders" || got.Cursor == "" || got.Cursor == first.Cursor {
		t.Fatalf("mapped event=%+v", got)
	}
	cancel()
	waitDone(t, done)
}

func TestBroadcastStoredPublishesCommittedEventWithoutAppendingDuplicate(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour})
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runServe(t, service, ctx, sink, Request{Scope: "orders"})
	_ = sink.waitFor(t, "snapshot")

	err := service.BroadcastStored(StoredEvent{
		Sequence: 7, Scope: "orders", Kind: "order.filled",
		Data: json.RawMessage(`{"id":"committed"}`), OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	event := sink.waitFor(t, "event")
	if event.Kind != "order.filled" || string(event.Data) != `{"id":"committed"}` {
		t.Fatalf("event=%+v", event)
	}
	if event.ContractVersion != readmodel.ContractVersion {
		t.Fatalf("event contractVersion=%q", event.ContractVersion)
	}
	latest, err := service.store.Latest(ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if latest != 0 {
		t.Fatalf("BroadcastStored appended a duplicate event: latest=%d", latest)
	}
	cancel()
	waitDone(t, done)
}

func TestResnapshotRequiredBypassesKindFiltersForReplayAndLiveDelivery(t *testing.T) {
	service := newTestService(t, ServiceConfig{HeartbeatInterval: time.Hour})
	ctx := context.Background()
	first, _ := publishTestEvent(t, service, ctx, "orders", "order.created", `{"id":"1"}`)
	_, _ = publishTestEvent(t, service, ctx, "orders", EventResnapshotRequired, `{"scope":["orders"]}`)

	replaySink := newRecordingSink()
	replayCtx, cancelReplay := context.WithCancel(ctx)
	replayDone := runServe(t, service, replayCtx, replaySink, Request{
		Scope: "orders", Cursor: first.Cursor, Kinds: []string{"order.filled"},
	})
	replayed := replaySink.waitFor(t, "event")
	if replayed.Kind != EventResnapshotRequired {
		t.Fatalf("replayed=%+v", replayed)
	}
	cancelReplay()
	waitDone(t, replayDone)

	liveSink := newRecordingSink()
	liveCtx, cancelLive := context.WithCancel(ctx)
	liveDone := runServe(t, service, liveCtx, liveSink, Request{
		Scope: "orders", Kinds: []string{"order.filled"},
	})
	_ = liveSink.waitFor(t, "snapshot")
	if err := service.BroadcastStored(StoredEvent{
		Sequence: 99, Scope: "orders", Kind: EventResnapshotRequired,
		Data: json.RawMessage(`{"scope":["orders"]}`), OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	live := liveSink.waitFor(t, "event")
	if live.Kind != EventResnapshotRequired {
		t.Fatalf("live=%+v", live)
	}
	cancelLive()
	waitDone(t, liveDone)
}

func newTestService(t *testing.T, override ServiceConfig) *Service {
	t.Helper()
	store := newMemoryEventStore()
	source := &testSnapshotSource{snapshots: map[string]json.RawMessage{
		"orders": json.RawMessage(`{"orders":[]}`),
	}}
	cfg := ServiceConfig{
		BootID:            "boot-a",
		PrincipalID:       "principal-default",
		Mode:              "test",
		SigningKey:        testSigningKey(),
		Store:             store,
		SnapshotSource:    source,
		HeartbeatInterval: time.Hour,
		SubscriberBuffer:  8,
	}
	if override.BootID != "" {
		cfg.BootID = override.BootID
	}
	if override.PrincipalID != "" {
		cfg.PrincipalID = override.PrincipalID
	}
	if override.Mode != "" {
		cfg.Mode = override.Mode
	}
	if override.SigningKey != nil {
		cfg.SigningKey = override.SigningKey
	}
	if override.HeartbeatInterval != 0 {
		cfg.HeartbeatInterval = override.HeartbeatInterval
	}
	if override.SubscriberBuffer != 0 {
		cfg.SubscriberBuffer = override.SubscriberBuffer
	}
	service, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testSigningKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func tamper(cursor string) string {
	replacement := byte('A')
	if cursor[len(cursor)-1] == replacement {
		replacement = 'B'
	}
	return cursor[:len(cursor)-1] + string(replacement)
}

func publishTestEvent(t *testing.T, service *Service, ctx context.Context, scope, kind, data string) (StreamEvent, StoredEvent) {
	t.Helper()
	stored, err := service.Publish(ctx, EventInput{Scope: scope, Kind: kind, Data: json.RawMessage(data)})
	if err != nil {
		t.Fatal(err)
	}
	return StreamEvent{
		Name: "event", Scope: scope,
		PrincipalID: service.principalID, Mode: service.mode,
		ContractVersion: readmodel.ContractVersion,
		Kind:            kind, Cursor: stored.Cursor, Data: stored.Data,
	}, stored
}

func (service *Service) mustEncodeLegacyV1Cursor(t *testing.T, scope string, sequence uint64) string {
	t.Helper()
	payload, err := json.Marshal(struct {
		Version  int `json:"version"`
		Position struct {
			BootID   string `json:"bootId"`
			Scope    string `json:"scope"`
			Sequence uint64 `json:"sequence"`
		} `json:"position"`
	}{
		Version: 1,
		Position: struct {
			BootID   string `json:"bootId"`
			Scope    string `json:"scope"`
			Sequence uint64 `json:"sequence"`
		}{BootID: service.bootID, Scope: scope, Sequence: sequence},
	})
	if err != nil {
		t.Fatal(err)
	}
	nonceSeed := service.codec.sign(payload)
	nonce := nonceSeed[:service.codec.aead.NonceSize()]
	ciphertext := service.codec.aead.Seal(nil, nonce, payload, nil)
	body := append(nonce, ciphertext...)
	signature := service.codec.sign(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(signature)
}

func runServe(t *testing.T, service *Service, ctx context.Context, sink Sink, request Request) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- service.Serve(ctx, sink, request)
	}()
	return done
}

func waitDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
}

type testSnapshotSource struct {
	snapshots map[string]json.RawMessage
}

func (source *testSnapshotSource) Snapshot(_ context.Context, request Request) (Snapshot, error) {
	return Snapshot{Scope: request.Scope, Data: source.snapshots[request.Scope]}, nil
}

type recordingSink struct {
	mu     sync.Mutex
	events []StreamEvent
	wake   chan struct{}
}

func newRecordingSink() *recordingSink {
	return &recordingSink{wake: make(chan struct{})}
}

func (sink *recordingSink) Send(_ context.Context, event StreamEvent) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events = append(sink.events, event)
	close(sink.wake)
	sink.wake = make(chan struct{})
	return nil
}

func (sink *recordingSink) waitFor(t *testing.T, name string) StreamEvent {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		sink.mu.Lock()
		for i, event := range sink.events {
			if event.Name == name {
				sink.events = append(sink.events[:i], sink.events[i+1:]...)
				sink.mu.Unlock()
				return event
			}
		}
		wake := sink.wake
		sink.mu.Unlock()
		select {
		case <-wake:
		case <-deadline:
			t.Fatalf("timed out waiting for %q", name)
		}
	}
}

type blockingSink struct {
	*recordingSink
	mu      sync.Mutex
	blocked chan struct{}
}

func newBlockingSink() *blockingSink {
	return &blockingSink{recordingSink: newRecordingSink()}
}

func (sink *blockingSink) Send(ctx context.Context, event StreamEvent) error {
	sink.mu.Lock()
	blocked := sink.blocked
	sink.mu.Unlock()
	if blocked != nil {
		select {
		case <-blocked:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return sink.recordingSink.Send(ctx, event)
}

func (sink *blockingSink) block() {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.blocked = make(chan struct{})
}

func (sink *blockingSink) unblock() {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.blocked != nil {
		close(sink.blocked)
		sink.blocked = nil
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if len(needle) != 0 && contains(s, needle) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
