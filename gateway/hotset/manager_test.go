package hotset

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

type recordingActivator struct {
	mu     sync.Mutex
	active map[Key]bool
}

func (target *recordingActivator) SetActive(key Key, active bool) {
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.active == nil {
		target.active = make(map[Key]bool)
	}
	target.active[key] = active
}

func (target *recordingActivator) isActive(key Key) bool {
	target.mu.Lock()
	defer target.mu.Unlock()
	return target.active[key]
}

func testKey(instrument string) Key {
	return Key{Owner: markethub.OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, InstrumentID: instrument}
}

func TestManagerReferenceCountsAndGrace(t *testing.T) {
	activation := &recordingActivator{}
	manager, err := New(Options{Activator: activation, Capacity: func(markethub.OwnerKey) int { return 2 }, Grace: 15 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	first, _ := manager.Acquire(t.Context(), testKey("btc"), ReasonBrowser)
	second, _ := manager.Acquire(t.Context(), testKey("btc"), ReasonBrowser)
	if !activation.isActive(testKey("btc")) {
		t.Fatal("first lease did not activate instrument")
	}
	first.Release()
	if !activation.isActive(testKey("btc")) {
		t.Fatal("one release deactivated a referenced instrument")
	}
	second.Release()
	if !activation.isActive(testKey("btc")) {
		t.Fatal("last release skipped idle grace")
	}
	time.Sleep(30 * time.Millisecond)
	if activation.isActive(testKey("btc")) {
		t.Fatal("instrument remained active after idle grace")
	}
}

func TestManagerPriorityAndProtectedOverflow(t *testing.T) {
	activation := &recordingActivator{}
	manager, err := New(Options{Activator: activation, Capacity: func(markethub.OwnerKey) int { return 1 }, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	browser, _ := manager.Acquire(context.Background(), testKey("browser"), ReasonBrowser)
	defer browser.Release()
	arbitrage, _ := manager.Acquire(context.Background(), testKey("arbitrage"), ReasonArbitrageTop)
	defer arbitrage.Release()
	if !activation.isActive(testKey("browser")) || activation.isActive(testKey("arbitrage")) {
		t.Fatal("capacity did not retain the higher-priority browser lease")
	}
	position, _ := manager.Acquire(context.Background(), testKey("position"), ReasonPosition)
	defer position.Release()
	order, _ := manager.Acquire(context.Background(), testKey("order"), ReasonOpenOrder)
	defer order.Release()
	if !activation.isActive(testKey("position")) || !activation.isActive(testKey("order")) {
		t.Fatal("protected leases were evicted at capacity")
	}
	if activation.isActive(testKey("browser")) {
		t.Fatal("unprotected lease remained selected ahead of protected demand")
	}
}

func TestManagerReasonsHaveLockedPriorityOrder(t *testing.T) {
	want := []Reason{ReasonPosition, ReasonOpenOrder, ReasonExecution, ReasonBrowser, ReasonArbitrageTop, ReasonFundingHistory}
	for index := 1; index < len(want); index++ {
		if priorities[want[index-1]] <= priorities[want[index]] {
			t.Fatalf("priority order changed at %s/%s", want[index-1], want[index])
		}
	}
}

func TestManagerObserverSeesStableReasonTransitions(t *testing.T) {
	activation := &recordingActivator{}
	observer := &recordingObserver{}
	manager, err := New(Options{Activator: activation, Capacity: func(markethub.OwnerKey) int { return 1 }, Grace: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.SetObserver(observer)
	key := testKey("BTC-USDT")
	first, err := manager.Acquire(context.Background(), key, ReasonPosition)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire(context.Background(), key, ReasonPosition)
	if err != nil {
		t.Fatal(err)
	}
	if got := observer.Count(key, ReasonPosition, true); got != 1 {
		t.Fatalf("activation transitions=%d, want 1", got)
	}
	first.Release()
	if got := observer.Count(key, ReasonPosition, false); got != 0 {
		t.Fatalf("early deactivation transitions=%d, want 0", got)
	}
	second.Release()
	if got := observer.Count(key, ReasonPosition, false); got != 1 {
		t.Fatalf("deactivation transitions=%d, want 1", got)
	}
}

type recordingObserver struct {
	mu          sync.Mutex
	transitions []observedTransition
}

type observedTransition struct {
	key    Key
	reason Reason
	active bool
}

func (observer *recordingObserver) ReasonChanged(key Key, reason Reason, active bool) {
	observer.mu.Lock()
	observer.transitions = append(observer.transitions, observedTransition{key: key, reason: reason, active: active})
	observer.mu.Unlock()
}

func (observer *recordingObserver) Count(key Key, reason Reason, active bool) int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	count := 0
	for _, transition := range observer.transitions {
		if transition.key == key && transition.reason == reason && transition.active == active {
			count++
		}
	}
	return count
}
