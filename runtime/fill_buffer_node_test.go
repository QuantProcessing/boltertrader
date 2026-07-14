package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
)

func TestFillBeforeOrderBuffersAndDrains(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	filled := make(chan model.Fill, 8)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"fill-buffer",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	fill := model.Fill{
		InstrumentID: inst,
		ClientID:     "buffered",
		VenueOrderID: "venue-buffered",
		TradeID:      "trade-buffered",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	}
	fexec.EmitFill(fill)
	select {
	case got := <-filled:
		t.Fatalf("fill applied before order: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if got := node.Metrics().PendingFills; got != 1 {
		t.Fatalf("pending fills=%d, want 1", got)
	}

	fexec.EmitOrder(model.Order{
		Request: model.OrderRequest{
			InstrumentID: inst,
			ClientID:     "buffered",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("1"),
			Price:        d("100"),
		},
		VenueOrderID: "venue-buffered",
		Status:       enums.StatusNew,
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	})
	waitFill(t, filled)
	if got := node.Metrics().PendingFills; got != 0 {
		t.Fatalf("pending fills=%d, want 0", got)
	}
}

func TestBufferedFillPreservesOriginalEnvelopeMeta(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	strat := &fillMetaStrategy{meta: make(chan contract.EventMeta, 1)}
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"fill-buffer-meta",
		runtime.WithStrategy(strat),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	fill := model.Fill{
		InstrumentID: inst,
		ClientID:     "buffered-meta",
		VenueOrderID: "venue-buffered-meta",
		TradeID:      "trade-buffered-meta",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	}
	wantMeta := contract.NewExecEnvelopeWithMeta(
		contract.FillEvent{Fill: fill},
		contract.EventMeta{Source: contract.SourceTest, Flags: contract.EventFlagSynthetic},
	).Meta()
	fexec.EmitFill(fill)
	select {
	case meta := <-strat.meta:
		t.Fatalf("fill applied before order with meta %+v", meta)
	case <-time.After(50 * time.Millisecond):
	}

	fexec.EmitOrder(model.Order{
		Request: model.OrderRequest{
			InstrumentID: inst,
			ClientID:     "buffered-meta",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("1"),
			Price:        d("100"),
		},
		VenueOrderID: "venue-buffered-meta",
		Status:       enums.StatusNew,
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	})
	got := waitFillMeta(t, strat.meta)
	if got.EventID != wantMeta.EventID || got.TradeID != wantMeta.TradeID || got.Source != wantMeta.Source {
		t.Fatalf("fill meta=%+v, want original fill meta %+v", got, wantMeta)
	}
}

func TestBufferedFillWithoutTradeIDDedupesClientAndVenueIndexes(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	filled := make(chan model.Fill, 8)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"fill-buffer-no-trade-id",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	fill := model.Fill{
		InstrumentID: inst,
		ClientID:     "buffered-no-trade-id",
		VenueOrderID: "venue-buffered-no-trade-id",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	}
	fexec.EmitFill(fill)
	select {
	case got := <-filled:
		t.Fatalf("fill applied before order: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	fexec.EmitOrder(model.Order{
		Request: model.OrderRequest{
			InstrumentID: inst,
			ClientID:     "buffered-no-trade-id",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("1"),
			Price:        d("100"),
		},
		VenueOrderID: "venue-buffered-no-trade-id",
		Status:       enums.StatusNew,
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	})
	waitFill(t, filled)
	select {
	case got := <-filled:
		t.Fatalf("duplicate buffered fill applied: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if got := node.Metrics().FillsSeen; got != 1 {
		t.Fatalf("fills seen=%d, want 1", got)
	}
}

func TestFillBeforeOrderCanMaterializeExternalOrder(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	spotInst := inst
	spotInst.Kind = enums.KindSpot
	fexec := runtimetest.NewFakeExec()
	filled := make(chan model.Fill, 8)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"external-fill",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	fexec.EmitFill(model.Fill{
		InstrumentID: spotInst,
		VenueOrderID: "external-venue",
		TradeID:      "external-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	})
	waitFill(t, filled)
	order, ok := node.Cache.Order("external-external-fill-external-venue-external-trade")
	if !ok {
		t.Fatal("materialized external order missing")
	}
	if order.Request.AccountID != "external-fill" || order.Status != enums.StatusFilled || !order.FilledQty.Equal(d("1")) {
		t.Fatalf("external order=%+v, want filled qty 1", order)
	}

	fexec.EmitOrder(model.Order{
		Request: model.OrderRequest{
			InstrumentID: spotInst,
			Side:         enums.SideBuy,
			Type:         enums.TypeMarket,
			Quantity:     d("1"),
			Price:        d("100"),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: "external-venue",
		Status:       enums.StatusNew,
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	})
	waitUntil(t, func() bool { return node.Metrics().OrdersSeen == 1 }, "stale order event should be processed")
	order, ok = node.Cache.Order("external-external-fill-external-venue-external-trade")
	if !ok || order.Request.AccountID != "external-fill" || order.Status != enums.StatusFilled || !order.FilledQty.Equal(d("1")) {
		t.Fatalf("external order after stale order ok=%v order=%+v, want still filled", ok, order)
	}
}

type fillMetaStrategy struct {
	strategy.Base
	meta chan contract.EventMeta
}

func (s *fillMetaStrategy) OnFill(c *strategy.Context, f model.Fill) {
	s.meta <- c.CurrentEventMeta()
}

func waitFillMeta(t *testing.T, ch <-chan contract.EventMeta) contract.EventMeta {
	t.Helper()
	select {
	case meta := <-ch:
		return meta
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fill meta")
		return contract.EventMeta{}
	}
}

func TestFillEventUpdatesCachedOrderState(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	filled := make(chan model.Fill, 8)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"fill-cache",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("100"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		TradeID:      "fill-cache-1",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	})
	waitFill(t, filled)
	got, _ := node.Cache.Order(order.Request.ClientID)
	if got.Status != enums.StatusPartiallyFilled || !got.FilledQty.Equal(d("1")) || !got.AvgFillPrice.Equal(d("100")) {
		t.Fatalf("after first fill order=%+v, want partial qty 1 avg 100", got)
	}

	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		TradeID:      "fill-cache-2",
		Side:         enums.SideBuy,
		Price:        d("200"),
		Quantity:     d("1"),
		Timestamp:    clk.Now().Add(time.Second),
	})
	waitFill(t, filled)
	got, _ = node.Cache.Order(order.Request.ClientID)
	if got.Status != enums.StatusFilled || !got.FilledQty.Equal(d("2")) || !got.AvgFillPrice.Equal(d("150")) {
		t.Fatalf("after second fill order=%+v, want filled qty 2 avg 150", got)
	}
}

func TestLateCompleteFillOverridesTerminalNonFilledOrderState(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	filled := make(chan model.Fill, 8)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"late-fill",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
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
	fexec.EmitOrder(model.Order{
		Request:      order.Request,
		VenueOrderID: order.VenueOrderID,
		Status:       enums.StatusCanceled,
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	})
	waitUntil(t, func() bool {
		got, ok := node.Cache.Order(order.Request.ClientID)
		return ok && got.Status == enums.StatusCanceled
	}, "canceled order event should be applied")

	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		TradeID:      "late-fill-complete",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now().Add(time.Second),
	})
	waitFill(t, filled)
	got, _ := node.Cache.Order(order.Request.ClientID)
	if got.Status != enums.StatusFilled || !got.FilledQty.Equal(d("1")) {
		t.Fatalf("order=%+v, want late complete fill to mark FILLED", got)
	}
}

func TestDuplicateTradeIDIgnored(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	filled := make(chan model.Fill, 8)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"duplicate-fill",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
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
	fill := model.Fill{
		InstrumentID: inst,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		TradeID:      "duplicate-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	}
	fexec.EmitFill(fill)
	waitFill(t, filled)
	fexec.EmitFill(fill)
	select {
	case got := <-filled:
		t.Fatalf("duplicate fill applied: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAmbiguousSubmitResolvedByLaterOrderEvent(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	fexec.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, clk, "ambiguous-stream")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	req := model.OrderRequest{
		InstrumentID: inst,
		ClientID:     "ambiguous-stream",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := node.Exec.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	if got := node.Metrics().InFlight; got != 1 {
		t.Fatalf("in-flight=%d, want 1", got)
	}
	fexec.EmitOrder(model.Order{
		Request:      req,
		VenueOrderID: "venue-late",
		Status:       enums.StatusNew,
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	})
	waitUntil(t, func() bool { return node.Metrics().InFlight == 0 }, "ambiguous intent should resolve from stream order")
	if o, ok := node.Cache.Order(req.ClientID); !ok || o.Status != enums.StatusNew {
		t.Fatalf("cached order ok=%v order=%+v, want stream NEW", ok, o)
	}
}

func TestAmbiguousSubmitResolvedByRejectedOrderEvent(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := journal.NewMemory()
	fexec := runtimetest.NewFakeExec()
	fexec.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"rejected-order-stream",
		runtime.WithJournal(store),
	)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go node.Run(runCtx)
	waitNodeRunning(t, node)

	req := model.OrderRequest{
		InstrumentID: inst,
		ClientID:     "rejected-order-stream",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := node.Exec.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	fexec.EmitOrder(model.Order{
		Request:      req,
		VenueOrderID: "venue-rejected-stream",
		Status:       enums.StatusRejected,
		RejectReason: "post-only would take",
		CreatedAt:    clk.Now(),
		UpdatedAt:    clk.Now(),
	})
	waitUntil(t, func() bool { return node.Metrics().InFlight == 0 }, "rejected order event should resolve in-flight")
	got, ok := node.Cache.Order(req.ClientID)
	if !ok || got.Status != enums.StatusRejected {
		t.Fatalf("cache order ok=%v order=%+v, want REJECTED", ok, got)
	}
	records := store.Records()
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Type != journal.RecordCommandResult {
			continue
		}
		var result journal.CommandResult
		if err := json.Unmarshal(records[i].Payload, &result); err != nil {
			t.Fatalf("decode command result: %v", err)
		}
		if result.ClientID == req.ClientID {
			if result.Outcome != string(exec.OutcomeDefinitiveVenueRejected) {
				t.Fatalf("latest outcome=%s, want definitive rejection", result.Outcome)
			}
			return
		}
	}
	t.Fatal("missing command result for rejected stream order")
}

func TestReplayedAmbiguousSubmitResolvedByRejectWithoutCache(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := journal.NewMemory()
	firstFake := runtimetest.NewFakeExec()
	firstFake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	first := exec.New(firstFake, cache.New(), clk, "reject-replay").WithJournal(store)
	req := model.OrderRequest{
		InstrumentID: inst,
		ClientID:     "reject-replay",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := first.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}

	replayFake := runtimetest.NewFakeExec()
	node := runtime.NewNode(
		runtime.Clients{Execution: replayFake},
		clk,
		"reject-replay",
		runtime.WithJournal(store),
	)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go node.Run(runCtx)
	waitNodeRunning(t, node)
	if got := node.Metrics().InFlight; got != 1 {
		t.Fatalf("in-flight=%d, want replayed ambiguous intent", got)
	}

	replayFake.EmitReject(req.ClientID, "definitive reject")
	waitUntil(t, func() bool { return node.Metrics().InFlight == 0 }, "reject event should resolve replayed in-flight intent without cache hit")
	open, err := store.OpenIntents(ctx)
	if err != nil {
		t.Fatalf("open intents: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open intents=%+v, want durable reject resolution", open)
	}
}

func TestAmbiguousSubmitResolvedByLaterFillEvent(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	fexec.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	filled := make(chan model.Fill, 1)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"ambiguous-fill-stream",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	req := model.OrderRequest{
		InstrumentID: inst,
		ClientID:     "ambiguous-fill-stream",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := node.Exec.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	if got := node.Metrics().InFlight; got != 1 {
		t.Fatalf("in-flight=%d, want 1", got)
	}
	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		ClientID:     req.ClientID,
		VenueOrderID: "venue-fill-stream",
		TradeID:      "stream-fill-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	})
	waitFill(t, filled)
	waitUntil(t, func() bool { return node.Metrics().InFlight == 0 }, "ambiguous intent should resolve from stream fill")
	if o, ok := node.Cache.Order(req.ClientID); !ok || o.Status != enums.StatusFilled || o.VenueOrderID != "venue-fill-stream" {
		t.Fatalf("cached order ok=%v order=%+v, want stream fill to materialize FILLED order", ok, o)
	}
}

func TestAmbiguousSubmitResolvedByLaterVenueOnlyFillEvent(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	fexec.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	filled := make(chan model.Fill, 1)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec},
		clk,
		"ambiguous-venue-only-stream",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	req := model.OrderRequest{
		InstrumentID: inst,
		ClientID:     "ambiguous-venue-only-stream",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := node.Exec.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	if got := node.Metrics().InFlight; got != 1 {
		t.Fatalf("in-flight=%d, want 1", got)
	}
	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		VenueOrderID: "venue-only-stream",
		TradeID:      "venue-only-stream-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    clk.Now(),
	})
	waitFill(t, filled)
	waitUntil(t, func() bool { return node.Metrics().InFlight == 0 }, "ambiguous intent should resolve from venue-only stream fill")
	if o, ok := node.Cache.Order(req.ClientID); !ok || o.Status != enums.StatusFilled || o.VenueOrderID != "venue-only-stream" {
		t.Fatalf("cached order ok=%v order=%+v, want original client order FILLED with venue id", ok, o)
	}
	if _, ok := node.Cache.Order("external-venue-only-stream-venue-only-stream-trade"); ok {
		t.Fatal("venue-only stream fill was incorrectly materialized as external order")
	}
}
