package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
)

type latencyTradeStrategy struct {
	strategy.Base
	seen chan struct{}
}

func (s latencyTradeStrategy) OnTrade(c *strategy.Context, t model.TradeTick) {
	if c.CurrentEventMeta().EventID != "" {
		select {
		case s.seen <- struct{}{}:
		default:
		}
	}
}

func TestMarketLatencyChainFakeVenue(t *testing.T) {
	fmarket := runtimetest.NewFakeMarket()
	strat := latencyTradeStrategy{seen: make(chan struct{}, 1)}
	node := runtime.NewNode(runtime.Clients{Market: fmarket}, nil, "lat", runtime.WithStrategy(strat))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: time.Now()})
	select {
	case <-strat.seen:
	case <-time.After(time.Second):
		t.Fatal("strategy did not observe event metadata")
	}
	waitUntil(t, func() bool {
		return node.Metrics().Latency.EventsTotal > 0
	}, "timed out waiting for market latency sample")
}

func TestCommandLatencyChainFakeSubmit(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "lat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)
	order, err := node.Exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	m := node.Metrics()
	if m.Latency.CommandsTotal == 0 || len(m.Latency.RecentCommands) == 0 {
		t.Fatalf("missing command latency metrics: %+v", m.Latency)
	}
	found := false
	for _, cmd := range m.Latency.RecentCommands {
		if cmd.Command == "submit" && cmd.ClientID == order.Request.ClientID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing submit command for %q: %+v", order.Request.ClientID, m.Latency.RecentCommands)
	}
}

func TestReconciliationLatencyChain(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	fexec.SetOrderStatusReports(model.Order{
		Request:      model.OrderRequest{ClientID: "ext-1", InstrumentID: inst, Quantity: d("1")},
		VenueOrderID: "v-ext-1",
		Status:       enums.StatusNew,
	})
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "lat")
	if _, err := node.Resync(context.Background()); err != nil {
		t.Fatalf("resync: %v", err)
	}
	m := node.Metrics()
	if m.Latency.ReconciliationsTotal == 0 {
		t.Fatalf("missing reconciliation latency metrics: %+v", m.Latency)
	}
}

type slowLatencyObserver struct{ observ.Base }

func (slowLatencyObserver) OnLatency(latency.EventLatency) { time.Sleep(50 * time.Millisecond) }

func TestMetricsIncludeLatencyAndDropCounters(t *testing.T) {
	fmarket := runtimetest.NewFakeMarket()
	async := observ.NewAsyncObserver(slowLatencyObserver{}, 1)
	defer async.Close()
	node := runtime.NewNode(runtime.Clients{Market: fmarket}, nil, "lat", runtime.WithObserver(async))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	for i := 0; i < 100; i++ {
		fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond)})
	}
	waitUntil(t, func() bool {
		m := node.Metrics()
		return m.Latency.EventsTotal > 0 && m.ObserverDrops > 0
	}, "timed out waiting for latency and observer drop metrics")
}
