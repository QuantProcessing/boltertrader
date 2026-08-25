package markethub

import (
	"context"
	"encoding/json"
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
	snapshotSequence string
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
	return NativeStream{
		Snapshot: Message{Kind: MessageSnapshot, ObservedAt: time.Now().UTC(), SourceSequence: owner.snapshotSequence, Data: snapshot},
		Events:   owner.events, Errors: make(chan error), Close: func() error {
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

func TestHubGivesLateDepthConsumerMaterializedSnapshot(t *testing.T) {
	owner := &fakeOwner{snapshot: Encode(map[string]any{
		"bids": []exchange.BookLevel{{Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1)}},
		"asks": []exchange.BookLevel{},
	}), events: make(chan Message, 2), closed: make(chan struct{})}
	hub, err := New(Options{Owner: owner, IdleGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	key := SubscriptionKey{Owner: OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, Dataset: exchange.DatasetDepth, InstrumentID: "btc", Depth: 10}
	owner.events <- Message{Kind: MessageDelta, ObservedAt: time.Now().UTC(), SourceSequence: "2", Data: Encode(map[string]any{
		"kind": "delta",
		"bids": []exchange.BookLevel{{Price: decimal.NewFromInt(2), Quantity: decimal.NewFromInt(3)}},
		"asks": []exchange.BookLevel{},
	})}
	first, err := hub.Subscribe(t.Context(), key, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	<-first.Events()
	deadline := time.Now().Add(time.Second)
	for {
		if latest, ok := hub.Latest(key); ok && latest.SourceSequence == "2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("depth delta was not materialized")
		}
		time.Sleep(time.Millisecond)
	}
	late, err := hub.Subscribe(t.Context(), key, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	message := <-late.Events()
	if message.Kind != MessageSnapshot {
		t.Fatalf("late first kind=%s", message.Kind)
	}
	var book wireBook
	if err := json.Unmarshal(message.Data, &book); err != nil || len(book.Bids) != 2 || !book.Bids[0].Price.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("late book=%+v err=%v", book, err)
	}
}

type fakeBBOBootstrap struct {
	mu    sync.Mutex
	calls int
	quote BBOQuote
}

func (source *fakeBBOBootstrap) SnapshotBBO(context.Context, string) (BBOQuote, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return source.quote, nil
}

func TestFreshBBOUsesCacheAndSingleflightFallback(t *testing.T) {
	now := time.Date(2026, 8, 25, 2, 0, 0, 0, time.UTC)
	source := &fakeBBOBootstrap{quote: BBOQuote{InstrumentID: "btc", Bid: exchange.BookLevel{Price: decimal.NewFromInt(99), Quantity: decimal.NewFromInt(1)}, Ask: exchange.BookLevel{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)}, ObservedAt: now}}
	cache, err := NewBBOCache(source, func() time.Time { return now })
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
	source.mu.Lock()
	calls := source.calls
	source.mu.Unlock()
	if calls != 1 {
		t.Fatalf("REST calls = %d, want 1", calls)
	}
	if _, err := cache.GetFreshBBO(context.Background(), "btc", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	calls = source.calls
	source.mu.Unlock()
	if calls != 1 {
		t.Fatalf("cache miss caused REST calls = %d", calls)
	}
}
