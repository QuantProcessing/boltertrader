// Package observ provides lightweight, dependency-free observability hooks for
// the runtime: an Observer interface the TradingNode calls on lifecycle and
// trading events, and a Metrics snapshot aggregated from runtime state. Concrete
// sinks (zap, prometheus, etc.) implement Observer without the runtime taking a
// dependency on any of them.
package observ

import (
	"sync"
	"sync/atomic"

	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/shopspring/decimal"
)

// Observer receives runtime events. Methods run synchronously and never overlap,
// including lifecycle callbacks initiated outside the event goroutine.
// Implementations must not block or re-enter TradingNode methods that emit
// Observer callbacks. Read-only node methods such as State, Health, and Metrics
// are safe to call. Any method may be a no-op; embed Base to implement only what
// you need.
type Observer interface {
	// OnNodeStart is called once when the node starts.
	OnNodeStart()
	// OnNodeStop is called once when the node stops.
	OnNodeStop()
	// OnOrder is called for every order lifecycle update applied to the cache.
	OnOrder(o model.Order)
	// OnFill is called for every fill applied to cache and portfolio.
	OnFill(f model.Fill)
	// OnReject is called when an order is rejected (by the venue or risk gate).
	OnReject(clientID, reason string)
	// OnLatency is called after the runtime records an event or command latency.
	OnLatency(latency.EventLatency)
	// OnHealth is reserved for lifecycle/stream health observations.
	OnHealth(Health)
	// OnReconciliation is called after reconciliation passes.
	OnReconciliation(Reconciliation)
}

// Base is a no-op Observer for embedding.
type Base struct{}

func (Base) OnNodeStart()                    {}
func (Base) OnNodeStop()                     {}
func (Base) OnOrder(model.Order)             {}
func (Base) OnFill(model.Fill)               {}
func (Base) OnReject(string, string)         {}
func (Base) OnLatency(latency.EventLatency)  {}
func (Base) OnHealth(Health)                 {}
func (Base) OnReconciliation(Reconciliation) {}

type Health struct {
	Component               string
	Status                  string
	Detail                  string
	Lifecycle               lifecycle.Snapshot
	Clients                 []string
	Streams                 []string
	LastReconciliationError string
	LatencyDrops            uint64
	ObserverDrops           uint64
	EventQueueDepth         int
	InFlight                int
	PendingFills            int
	Accounts                int
	AccountStateAgeNs       int64
}

type Reconciliation struct {
	Reason     string
	DurationNs int64
	Error      string
}

// Metrics is a point-in-time snapshot of runtime trading state.
type Metrics struct {
	OpenOrders        int
	Positions         int
	RealizedPnL       decimal.Decimal
	RealizedPnLNet    decimal.Decimal
	Fees              decimal.Decimal
	FeesByCurrency    map[string]decimal.Decimal
	OrdersSeen        int64
	FillsSeen         int64
	RejectsSeen       int64
	Latency           latency.Snapshot
	ObserverDrops     uint64
	EventQueueDepth   int
	Lifecycle         lifecycle.Snapshot
	InFlight          int
	PendingFills      int
	Accounts          int
	AccountStateAgeNs int64
}

// Counters accumulates event counts. Fields are accessed via sync/atomic by the
// runtime (the serialized event path adds; Metrics readers load), so reads from
// other goroutines are race-free and eventually consistent.
type Counters struct {
	Orders  int64
	Fills   int64
	Rejects int64
}

type DroppingObserver interface {
	Observer
	Drops() uint64
	QueueDepth() int
}

type AsyncObserver struct {
	inner Observer
	queue chan func(Observer)
	done  chan struct{}
	once  sync.Once
	drops atomic.Uint64
}

func NewAsyncObserver(inner Observer, capacity int) *AsyncObserver {
	if capacity <= 0 {
		capacity = 1024
	}
	a := &AsyncObserver{
		inner: inner,
		queue: make(chan func(Observer), capacity),
		done:  make(chan struct{}),
	}
	go a.run()
	return a
}

func (a *AsyncObserver) run() {
	for {
		select {
		case <-a.done:
			return
		case fn := <-a.queue:
			if fn != nil {
				fn(a.inner)
			}
		}
	}
}

func (a *AsyncObserver) Close()          { a.once.Do(func() { close(a.done) }) }
func (a *AsyncObserver) Drops() uint64   { return a.drops.Load() }
func (a *AsyncObserver) QueueDepth() int { return len(a.queue) }

func (a *AsyncObserver) enqueue(fn func(Observer)) {
	select {
	case a.queue <- fn:
	default:
		a.drops.Add(1)
	}
}

func (a *AsyncObserver) OnNodeStart() { a.enqueue(func(o Observer) { o.OnNodeStart() }) }
func (a *AsyncObserver) OnNodeStop()  { a.enqueue(func(o Observer) { o.OnNodeStop() }) }
func (a *AsyncObserver) OnOrder(order model.Order) {
	a.enqueue(func(o Observer) { o.OnOrder(order) })
}
func (a *AsyncObserver) OnFill(fill model.Fill) {
	a.enqueue(func(o Observer) { o.OnFill(fill) })
}
func (a *AsyncObserver) OnReject(clientID, reason string) {
	a.enqueue(func(o Observer) { o.OnReject(clientID, reason) })
}
func (a *AsyncObserver) OnLatency(lat latency.EventLatency) {
	a.enqueue(func(o Observer) { o.OnLatency(lat) })
}
func (a *AsyncObserver) OnHealth(h Health) {
	a.enqueue(func(o Observer) { o.OnHealth(h) })
}
func (a *AsyncObserver) OnReconciliation(r Reconciliation) {
	a.enqueue(func(o Observer) { o.OnReconciliation(r) })
}
