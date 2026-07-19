package factoryclient

import (
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type backendLifecycleSink struct {
	status     func(backendStatus)
	markResync func()
}

type backendLifecycle struct {
	mu         sync.Mutex
	generation uint64
	gap        bool
	sinks      map[string]backendLifecycleSink
}

func newBackendLifecycle() *backendLifecycle {
	return &backendLifecycle{sinks: make(map[string]backendLifecycleSink)}
}

func (lifecycle *backendLifecycle) Register(
	key string,
	status func(backendStatus),
	markResync func(),
) func() {
	if lifecycle == nil {
		return func() {}
	}
	lifecycle.mu.Lock()
	lifecycle.sinks[key] = backendLifecycleSink{
		status:     status,
		markResync: markResync,
	}
	lifecycle.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			lifecycle.mu.Lock()
			delete(lifecycle.sinks, key)
			lifecycle.mu.Unlock()
		})
	}
}

func (lifecycle *backendLifecycle) Started(_ error) {
	if lifecycle == nil {
		return
	}
	lifecycle.mu.Lock()
	if lifecycle.gap {
		lifecycle.mu.Unlock()
		return
	}
	lifecycle.generation++
	lifecycle.gap = true
	generation := lifecycle.generation
	sinks := lifecycle.snapshotLocked()
	lifecycle.mu.Unlock()
	emitBackendStatus(sinks, backendStatus{
		State:      exchange.SubscriptionGap,
		Phase:      exchange.GapStarted,
		Generation: generation,
		Reason:     "websocket transport disconnected",
		Time:       time.Now().UTC(),
	})
}

func (lifecycle *backendLifecycle) Recovered(reason string) {
	if lifecycle == nil {
		return
	}
	lifecycle.mu.Lock()
	if !lifecycle.gap {
		lifecycle.mu.Unlock()
		return
	}
	generation := lifecycle.generation
	sinks := lifecycle.snapshotLocked()
	lifecycle.gap = false
	lifecycle.mu.Unlock()

	emitBackendStatus(sinks, backendStatus{
		State:      exchange.SubscriptionResyncing,
		Generation: generation,
		Reason:     reason,
		Time:       time.Now().UTC(),
	})
	for _, sink := range sinks {
		if sink.markResync != nil {
			sink.markResync()
		}
	}
	emitBackendStatus(sinks, backendStatus{
		State:      exchange.SubscriptionActive,
		Phase:      exchange.GapRecovered,
		Generation: generation,
		Reason:     reason,
		Time:       time.Now().UTC(),
	})
}

func (lifecycle *backendLifecycle) SynthesizedRecovery(reason string) {
	if lifecycle == nil {
		return
	}
	lifecycle.Started(nil)
	lifecycle.Recovered(reason)
}

func (lifecycle *backendLifecycle) snapshotLocked() []backendLifecycleSink {
	sinks := make([]backendLifecycleSink, 0, len(lifecycle.sinks))
	for _, sink := range lifecycle.sinks {
		sinks = append(sinks, sink)
	}
	return sinks
}

func emitBackendStatus(sinks []backendLifecycleSink, status backendStatus) {
	for _, sink := range sinks {
		if sink.status != nil {
			sink.status(status)
		}
	}
}
