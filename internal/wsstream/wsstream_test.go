package wsstream

import (
	"sync"
	"testing"
	"time"
)

func TestCloseUnblocksEmitterAndClosesEvents(t *testing.T) {
	stream := New[int](1)
	if !stream.Emit(1) {
		t.Fatal("initial emit failed")
	}

	result := make(chan bool, 1)
	go func() { result <- stream.Emit(2) }()
	select {
	case <-result:
		t.Fatal("second emit did not block under backpressure")
	case <-time.After(20 * time.Millisecond):
	}

	stream.Close()
	select {
	case delivered := <-result:
		if delivered {
			t.Fatal("blocked emit reported delivery after close")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked emit did not unblock on close")
	}

	if got, ok := <-stream.C(); !ok || got != 1 {
		t.Fatalf("buffered event=(%d,%v), want (1,true)", got, ok)
	}
	if _, ok := <-stream.C(); ok {
		t.Fatal("events channel remains open after Close")
	}
}

func TestConcurrentCloseIsIdempotentAndPostCloseEmitFails(t *testing.T) {
	stream := New[int](8)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream.Close()
		}()
	}
	wg.Wait()

	for i := 0; i < 10_000; i++ {
		if stream.Emit(i) {
			t.Fatalf("post-close emit %d reported delivery", i)
		}
	}
	if _, ok := <-stream.C(); ok {
		t.Fatal("events channel remains open after concurrent Close")
	}
}
