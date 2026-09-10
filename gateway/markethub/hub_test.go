package markethub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type fakeOwner struct {
	mu               sync.Mutex
	opens            int
	snapshot         json.RawMessage
	snapshotAt       time.Time
	snapshotSequence string
	replay           json.RawMessage
	replaySequence   string
	events           chan Message
	closed           chan struct{}
}

func (owner *fakeOwner) Open(context.Context, SubscriptionKey) (NativeStream, error) {
	owner.mu.Lock()
	owner.opens++
	owner.mu.Unlock()
	snapshot := owner.snapshot
	if len(snapshot) == 0 {
		snapshot = json.RawMessage(`{"price":"1"}`)
	}
	snapshotAt := owner.snapshotAt
	if snapshotAt.IsZero() {
		snapshotAt = time.Now().UTC()
	}
	return NativeStream{
		Snapshot: Message{Kind: MessageSnapshot, ObservedAt: snapshotAt, SourceSequence: owner.snapshotSequence, Data: snapshot},
		Events:   owner.events, Errors: make(chan error), Replay: func() (Message, bool) {
			owner.mu.Lock()
			defer owner.mu.Unlock()
			if len(owner.replay) == 0 {
				return Message{}, false
			}
			return Message{Kind: MessageSnapshot, ObservedAt: time.Now().UTC(), SourceSequence: owner.replaySequence, Data: append(json.RawMessage(nil), owner.replay...)}, true
		}, Close: func() error {
			select {
			case <-owner.closed:
			default:
				close(owner.closed)
			}
			return nil
		},
	}, nil
}

func TestHubDetectsDepthPreviousSequenceGap(t *testing.T) {
	owner := &fakeOwner{
		snapshot:         Encode(map[string]any{"bids": []exchange.BookLevel{}, "asks": []exchange.BookLevel{}}),
		snapshotSequence: "10", events: make(chan Message, 2), closed: make(chan struct{}),
	}
	hub, err := New(Options{Owner: owner, IdleGrace: time.Second, ReconnectBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, Dataset: exchange.DatasetDepth, InstrumentID: "btc", Depth: 10}
	subscription, err := hub.Subscribe(t.Context(), key, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	first := <-subscription.Events()
	owner.events <- Message{ObservedAt: time.Now().UTC(), SourceSequence: "12", Data: Encode(map[string]any{
		"kind": "delta", "previous": "9", "bids": []exchange.BookLevel{}, "asks": []exchange.BookLevel{},
	})}
	second := <-subscription.Events()
	third := <-subscription.Events()
	if first.Kind != MessageSnapshot || first.Generation != 1 || second.Kind != MessageResyncRequired || second.Generation != 2 ||
		third.Kind != MessageSnapshot || third.Generation != 2 {
		t.Fatalf("unexpected gap lifecycle: first=%+v second=%+v third=%+v", first, second, third)
	}
}

func TestHubBoundsReplaceableBBOFanoutWithoutLaggingSubscriber(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	updates := make(chan Message, 1)
	hub := &Hub{now: func() time.Time { return now }}
	state := &feedState{
		key: SubscriptionKey{
			Owner:   OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"},
			Dataset: exchange.DatasetBBO, InstrumentID: "btc",
		},
		generation: 1,
		subs:       map[uint64]chan Message{1: updates},
	}
	hub.publishLocked(state, Message{ObservedAt: now, Data: Encode(map[string]any{"bid": "1"})}, MessageSnapshot)
	for index := 0; index < 500; index++ {
		hub.publishLocked(state, Message{ObservedAt: now.Add(time.Millisecond), Data: Encode(map[string]any{"bid": "2"})}, MessageDelta)
	}
	if len(state.subs) != 1 || len(updates) != 1 {
		t.Fatalf("BBO burst removed subscriber or expanded queue: subscribers=%d queued=%d", len(state.subs), len(updates))
	}
	if message := <-updates; message.Kind != MessageSnapshot {
		t.Fatalf("first BBO message=%s want snapshot", message.Kind)
	}
	now = now.Add(bboFanoutMinInterval)
	hub.publishLocked(state, Message{ObservedAt: now, Data: Encode(map[string]any{"bid": "3"})}, MessageDelta)
	if message := <-updates; message.Kind != MessageDelta || message.Sequence != 502 {
		t.Fatalf("bounded BBO message=%+v", message)
	}
}

func TestHubSharesOneNativeStreamAndHonorsIdleGrace(t *testing.T) {
	owner := &fakeOwner{events: make(chan Message, 2), closed: make(chan struct{})}
	hub, err := New(Options{Owner: owner, IdleGrace: 20 * time.Millisecond, ReconnectBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, Dataset: exchange.DatasetMark}
	first, err := hub.Subscribe(t.Context(), key, 4)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.Subscribe(t.Context(), key, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-first.Events(); got.Kind != MessageSnapshot {
		t.Fatalf("first message = %#v", got)
	}
	if got := <-second.Events(); got.Kind != MessageSnapshot {
		t.Fatalf("second message = %#v", got)
	}
	owner.mu.Lock()
	opens := owner.opens
	owner.mu.Unlock()
	if opens != 1 {
		t.Fatalf("native opens = %d, want 1", opens)
	}
	first.Close()
	second.Close()
	select {
	case <-owner.closed:
		t.Fatal("native stream closed before idle grace")
	case <-time.After(5 * time.Millisecond):
	}
	select {
	case <-owner.closed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("native stream remained open after idle grace")
	}
}

func TestHubReplaysExchangeSnapshotWithoutMaterializingDepth(t *testing.T) {
	owner := &fakeOwner{snapshot: Encode(map[string]any{
		"bids": []exchange.BookLevel{{Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1)}},
		"asks": []exchange.BookLevel{},
	}), snapshotSequence: "1", events: make(chan Message, 4), closed: make(chan struct{})}
	hub, err := New(Options{Owner: owner, IdleGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, Dataset: exchange.DatasetDepth, InstrumentID: "btc", Depth: 10}
	first, err := hub.Subscribe(t.Context(), key, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if message := <-first.Events(); message.Kind != MessageSnapshot {
		t.Fatalf("first event = %+v", message)
	}
	owner.events <- Message{Kind: MessageDelta, ObservedAt: time.Now().UTC(), SourceSequence: "2", Data: Encode(map[string]any{
		"kind": "delta", "previous": "1",
		"bids": []exchange.BookLevel{{Price: decimal.NewFromInt(2), Quantity: decimal.NewFromInt(3)}},
		"asks": []exchange.BookLevel{},
	})}
	if message := <-first.Events(); message.Kind != MessageDelta || message.SourceSequence != "2" {
		t.Fatalf("first delta = %+v", message)
	}
	if _, ok := hub.Latest(key); ok {
		t.Fatal("Gateway retained a materialized depth book")
	}
	owner.mu.Lock()
	owner.replay = Encode(map[string]any{
		"bids": []exchange.BookLevel{
			{Price: decimal.NewFromInt(2), Quantity: decimal.NewFromInt(3)},
			{Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1)},
		},
		"asks": []exchange.BookLevel{},
	})
	owner.replaySequence = "2"
	owner.mu.Unlock()
	late, err := hub.Subscribe(t.Context(), key, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	if message := <-late.Events(); message.Kind != MessageSnapshot || message.SourceSequence != "2" {
		t.Fatalf("late WS replay = %+v", message)
	}
	owner.events <- Message{Kind: MessageDelta, ObservedAt: time.Now().UTC(), SourceSequence: "3", Data: Encode(map[string]any{
		"kind": "delta", "previous": "2", "bids": []exchange.BookLevel{}, "asks": []exchange.BookLevel{},
	})}
	if message := <-late.Events(); message.Kind != MessageDelta || message.SourceSequence != "3" {
		t.Fatalf("late live event = %+v", message)
	}
}

func TestHubPreservesNativeDepthSnapshotSemantics(t *testing.T) {
	owner := &fakeOwner{
		snapshot:         Encode(map[string]any{"bids": []exchange.BookLevel{}, "asks": []exchange.BookLevel{}}),
		snapshotSequence: "1", events: make(chan Message, 1), closed: make(chan struct{}),
	}
	hub, err := New(Options{Owner: owner, IdleGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "spot"}, Dataset: exchange.DatasetDepth, InstrumentID: "btc", Depth: 5}
	subscription, err := hub.Subscribe(t.Context(), key, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	<-subscription.Events()
	owner.events <- Message{Kind: MessageSnapshot, ObservedAt: time.Now().UTC(), SourceSequence: "2", Data: Encode(map[string]any{
		"kind": "snapshot", "bids": []exchange.BookLevel{}, "asks": []exchange.BookLevel{},
	})}
	if message := <-subscription.Events(); message.Kind != MessageSnapshot || message.SourceSequence != "2" {
		t.Fatalf("native snapshot was downgraded: %+v", message)
	}
}

type retryingOwner struct {
	mu     sync.Mutex
	opens  int
	events chan Message
}

func (owner *retryingOwner) Open(context.Context, SubscriptionKey) (NativeStream, error) {
	owner.mu.Lock()
	owner.opens++
	opens := owner.opens
	owner.mu.Unlock()
	if opens < 3 {
		return NativeStream{}, errors.New("temporary open failure")
	}
	return NativeStream{
		Snapshot: Message{Kind: MessageSnapshot, ObservedAt: time.Now().UTC(), Data: Encode(map[string]any{"price": "1"})},
		Events:   owner.events, Errors: make(chan error), Close: func() error { return nil },
	}, nil
}

func TestHubKeepsLogicalSubscriptionAcrossOpenFailures(t *testing.T) {
	owner := &retryingOwner{events: make(chan Message)}
	hub, err := New(Options{
		Owner: owner, IdleGrace: time.Second,
		ReconnectMinimum: time.Millisecond, ReconnectMaximum: 2 * time.Millisecond,
		ReconnectJitter: func(delay time.Duration) time.Duration { return delay },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, Dataset: exchange.DatasetMark}
	subscription, err := hub.Subscribe(t.Context(), key, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	deadline := time.After(time.Second)
	for {
		select {
		case message := <-subscription.Events():
			if message.Kind == MessageSnapshot {
				owner.mu.Lock()
				opens := owner.opens
				owner.mu.Unlock()
				if opens != 3 {
					t.Fatalf("native opens = %d, want 3", opens)
				}
				return
			}
		case <-deadline:
			t.Fatal("logical subscription did not recover")
		}
	}
}

type fakeBBOKeyResolver struct{ key SubscriptionKey }

func (resolver fakeBBOKeyResolver) SubscriptionKey(context.Context, string, exchange.MarketDataset, string, int) (SubscriptionKey, error) {
	return resolver.key, nil
}

func TestFreshBBOJoinsSharedHubWebSocket(t *testing.T) {
	now := time.Now().UTC()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, Dataset: exchange.DatasetBBO, InstrumentID: "btc"}
	owner := &fakeOwner{
		snapshot: Encode(map[string]any{
			"bid": exchange.BookLevel{Price: decimal.NewFromInt(99), Quantity: decimal.NewFromInt(1)},
			"ask": exchange.BookLevel{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)},
		}),
		snapshotAt: now, snapshotSequence: "7", events: make(chan Message, 2), closed: make(chan struct{}),
	}
	hub, err := New(Options{Owner: owner, IdleGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	cache, err := NewBBOCache(hub, fakeBBOKeyResolver{key: key}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := cache.GetFreshBBO(context.Background(), "btc", 2*time.Second); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	owner.mu.Lock()
	opens := owner.opens
	owner.mu.Unlock()
	if opens != 1 {
		t.Fatalf("native BBO opens = %d, want 1", opens)
	}
	quote, err := cache.GetFreshBBO(context.Background(), "btc", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if quote.SourceSequence != "7" || !quote.Bid.Price.Equal(decimal.NewFromInt(99)) || !quote.Ask.Price.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("quote = %+v", quote)
	}
	owner.mu.Lock()
	opens = owner.opens
	owner.mu.Unlock()
	if opens != 1 {
		t.Fatalf("Hub latest read opened another BBO stream: %d", opens)
	}
}

func TestFreshBBOWaitsPastStaleSnapshotForLiveUpdate(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, Dataset: exchange.DatasetBBO, InstrumentID: "btc"}
	owner := &fakeOwner{
		snapshot: Encode(map[string]any{
			"bid": exchange.BookLevel{Price: decimal.NewFromInt(98), Quantity: decimal.NewFromInt(1)},
			"ask": exchange.BookLevel{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(1)},
		}),
		snapshotAt: now.Add(-time.Minute), events: make(chan Message, 2), closed: make(chan struct{}),
	}
	hub, err := New(Options{Owner: owner, IdleGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	cache, err := NewBBOCache(hub, fakeBBOKeyResolver{key: key}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan BBOQuote, 1)
	failures := make(chan error, 1)
	go func() {
		quote, readErr := cache.GetFreshBBO(t.Context(), "btc", 2*time.Second)
		if readErr != nil {
			failures <- readErr
			return
		}
		result <- quote
	}()

	deadline := time.After(time.Second)
	for {
		owner.mu.Lock()
		opens := owner.opens
		owner.mu.Unlock()
		if opens == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("BBO stream did not open")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	owner.events <- Message{Kind: MessageDelta, ObservedAt: now, SourceSequence: "8", Data: Encode(map[string]any{
		"bid": exchange.BookLevel{Price: decimal.NewFromInt(99), Quantity: decimal.NewFromInt(2)},
		"ask": exchange.BookLevel{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(3)},
	})}
	select {
	case err := <-failures:
		t.Fatal(err)
	case quote := <-result:
		if quote.SourceSequence != "8" || !quote.Bid.Price.Equal(decimal.NewFromInt(99)) {
			t.Fatalf("quote = %+v", quote)
		}
	case <-time.After(time.Second):
		t.Fatal("FreshBBO did not wait for a live update")
	}
}

type isolatedOwners struct{ healthy *fakeOwner }

func (o isolatedOwners) Open(ctx context.Context, key SubscriptionKey) (NativeStream, error) {
	if key.Owner.Venue == exchange.VenueOKX {
		return NativeStream{}, errors.New("venue unavailable")
	}
	return o.healthy.Open(ctx, key)
}

func TestOneVenueFailureAndSlowReaderDoNotStopAnotherVenue(t *testing.T) {
	healthy := &fakeOwner{events: make(chan Message, 4), closed: make(chan struct{})}
	hub, err := New(Options{Owner: isolatedOwners{healthy}, ReconnectMinimum: time.Millisecond, ReconnectMaximum: 5 * time.Millisecond, IdleGrace: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueOKX, Environment: exchange.EnvironmentLive, NativeFamily: "swap"}, Dataset: exchange.DatasetMark}
	failed, err := hub.Subscribe(t.Context(), key, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer failed.Close()
	// Never drain the failed venue's reconnect notifications.
	key.Owner.Venue, key.Owner.NativeFamily = exchange.VenueBinance, "perpetual"
	good, err := hub.Subscribe(t.Context(), key, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	select {
	case <-good.Events():
	case <-time.After(time.Second):
		t.Fatal("healthy snapshot blocked by other venue")
	}
	healthy.events <- Message{ObservedAt: time.Now().UTC(), Data: Encode(map[string]any{"price": "2"})}
	select {
	case event := <-good.Events():
		if event.Kind != MessageDelta {
			t.Fatalf("healthy event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy updates blocked by other venue")
	}
	good.Close()
	select {
	case <-healthy.closed:
	case <-time.After(time.Second):
		t.Fatal("healthy upstream leaked after unsubscribe")
	}
}
