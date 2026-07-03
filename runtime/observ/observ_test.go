package observ

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/runtime/latency"
)

type blockingObserver struct{ Base }

func (blockingObserver) OnLatency(latency.EventLatency) { time.Sleep(50 * time.Millisecond) }

func TestAsyncObserverDropsInsteadOfBlocking(t *testing.T) {
	obs := NewAsyncObserver(blockingObserver{}, 1)
	defer obs.Close()
	obs.OnLatency(latency.EventLatency{Chain: latency.ChainMarket})
	for i := 0; i < 100; i++ {
		obs.OnLatency(latency.EventLatency{Chain: latency.ChainExecution})
	}
	if obs.Drops() == 0 {
		t.Fatal("expected async observer to drop when bounded queue is full")
	}
}

func TestLatencyObserverNoopSafe(t *testing.T) {
	var base Base
	base.OnLatency(latency.EventLatency{Chain: latency.ChainMarket})
	base.OnHealth(Health{Component: "runtime", Status: "ok"})
	base.OnReconciliation(Reconciliation{Reason: "test"})
}
