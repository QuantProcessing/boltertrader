package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recordingSink struct {
	mu       sync.Mutex
	messages []Message
}

func (sink *recordingSink) Send(payload []byte) bool {
	var message Message
	_ = json.Unmarshal(payload, &message)
	sink.mu.Lock()
	sink.messages = append(sink.messages, message)
	sink.mu.Unlock()
	return true
}

func (sink *recordingSink) count(channel string) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	total := 0
	for _, message := range sink.messages {
		if message.Channel == channel {
			total++
		}
	}
	return total
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestTopicSharesOneUpstreamAndReplaysRetained(t *testing.T) {
	var opens atomic.Int32
	emitted := make(chan struct{})
	source := func(ctx context.Context, key Key, emit Emit) error {
		opens.Add(1)
		emit(Message{Channel: "book", Instrument: key.Instrument, Data: 1}, true)
		emit(Message{Channel: "trades", Instrument: key.Instrument, Data: 2}, false)
		close(emitted)
		<-ctx.Done()
		return nil
	}
	hub := New(source, 20*time.Millisecond)
	defer hub.Close()
	key := Key{Channel: "book", Instrument: "binance:perp:BTC-USDT"}
	first, second := &recordingSink{}, &recordingSink{}
	hub.Subscribe(key, first)
	<-emitted
	hub.Subscribe(key, second)

	if opens.Load() != 1 {
		t.Fatalf("upstream opened %d times", opens.Load())
	}
	if first.count("book") != 1 || first.count("trades") != 1 {
		t.Fatalf("first sink got %+v", first.messages)
	}
	if second.count("book") != 1 || second.count("trades") != 0 {
		t.Fatalf("late sink must replay retained only, got %+v", second.messages)
	}
}

func TestTopicClosesAfterLingerAndReopens(t *testing.T) {
	var opens atomic.Int32
	source := func(ctx context.Context, key Key, emit Emit) error {
		opens.Add(1)
		<-ctx.Done()
		return nil
	}
	hub := New(source, 20*time.Millisecond)
	defer hub.Close()
	key := Key{Channel: "book", Instrument: "x"}
	sink := &recordingSink{}

	hub.Subscribe(key, sink)
	hub.Unsubscribe(key, sink)
	hub.Subscribe(key, sink) // within linger: reuse
	eventually(t, func() bool { return opens.Load() == 1 })
	hub.Unsubscribe(key, sink)
	eventually(t, func() bool { return hub.Topics() == 0 })
	hub.Subscribe(key, sink)
	eventually(t, func() bool { return opens.Load() == 2 })
}

func TestFailedUpstreamIsRetried(t *testing.T) {
	var opens atomic.Int32
	source := func(ctx context.Context, key Key, emit Emit) error {
		if opens.Add(1) == 1 {
			return errors.New("boom")
		}
		emit(Message{Channel: "book", Data: "ok"}, true)
		<-ctx.Done()
		return nil
	}
	hub := New(source, time.Second)
	defer hub.Close()
	sink := &recordingSink{}
	hub.Subscribe(Key{Channel: "book"}, sink)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && sink.count("book") == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if sink.count("book.status") != 1 || sink.count("book") != 1 {
		t.Fatalf("messages: %+v", sink.messages)
	}
}
