package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

// nonThreadSafeObserver intentionally has no mutex around active or seen. The
// runtime's Observer contract is what makes those fields safe to mutate.
type nonThreadSafeObserver struct {
	active string
	seen   []string

	blockKind string
	entered   chan struct{}
	release   chan struct{}
	overlap   chan string
}

func newNonThreadSafeObserver() *nonThreadSafeObserver {
	return &nonThreadSafeObserver{overlap: make(chan string, 8)}
}

func (o *nonThreadSafeObserver) block(kind string) (<-chan struct{}, chan<- struct{}) {
	o.blockKind = kind
	o.entered = make(chan struct{})
	o.release = make(chan struct{})
	return o.entered, o.release
}

func (o *nonThreadSafeObserver) observe(kind string) {
	if o.active != "" {
		select {
		case o.overlap <- o.active + "/" + kind:
		default:
		}
	}
	o.active = kind
	if kind == o.blockKind {
		close(o.entered)
		<-o.release
	}
	o.seen = append(o.seen, kind)
	o.active = ""
}

func (o *nonThreadSafeObserver) OnNodeStart()                   { o.observe("start") }
func (o *nonThreadSafeObserver) OnNodeStop()                    { o.observe("stop") }
func (o *nonThreadSafeObserver) OnOrder(model.Order)            { o.observe("order") }
func (o *nonThreadSafeObserver) OnFill(model.Fill)              { o.observe("fill") }
func (o *nonThreadSafeObserver) OnReject(string, string)        { o.observe("reject") }
func (o *nonThreadSafeObserver) OnLatency(latency.EventLatency) { o.observe("latency") }
func (o *nonThreadSafeObserver) OnHealth(observ.Health)         { o.observe("health") }
func (o *nonThreadSafeObserver) OnReconciliation(observ.Reconciliation) {
	o.observe("reconciliation")
}

func TestObserverCallbacksAreSynchronousAndSerialized(t *testing.T) {
	at := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    "observer-serialization",
			InstrumentID: id,
			ClientID:     "observer-order",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: "observer-venue-order",
		Status:       enums.StatusNew,
		CreatedAt:    at,
		UpdatedAt:    at,
	}

	execClient := runtimetest.NewFakeExec()
	execClient.SetOrderStatusReports(order)
	observer := newNonThreadSafeObserver()
	node := NewNode(
		Clients{Execution: execClient},
		clock.NewSimulatedClock(at),
		"observer-serialization",
		WithObserver(observer),
	)
	node.Start(context.Background())

	orderEntered, releaseOrder := observer.block("order")
	orderDone := make(chan struct{})
	go func() {
		node.onExec(contract.NewExecEnvelope(contract.OrderEvent{Order: order}))
		close(orderDone)
	}()
	waitObserverCallback(t, orderEntered, "OnOrder")

	haltStarted := make(chan struct{})
	haltDone := make(chan struct{})
	go func() {
		close(haltStarted)
		node.Halt("observer serialization test")
		close(haltDone)
	}()
	<-haltStarted
	healthReturnedEarly := returnsBeforeRelease(haltDone)
	close(releaseOrder)
	waitObserverCallback(t, orderDone, "OnOrder completion")
	waitObserverCallback(t, haltDone, "Halt completion")
	if healthReturnedEarly {
		t.Error("Halt returned while OnOrder was blocked; OnHealth overlapped OnOrder")
	}

	reconciliationEntered, releaseReconciliation := observer.block("reconciliation")
	resyncDone := make(chan error, 1)
	go func() {
		_, err := node.Resync(context.Background())
		resyncDone <- err
	}()
	waitObserverCallback(t, reconciliationEntered, "OnReconciliation")

	fill := model.Fill{
		AccountID:    order.Request.AccountID,
		InstrumentID: id,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		TradeID:      "observer-fill",
		Side:         enums.SideBuy,
		Price:        order.Request.Price,
		Quantity:     order.Request.Quantity,
		Timestamp:    at.Add(time.Second),
	}
	fillDone := make(chan struct{})
	go func() {
		node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: fill}))
		close(fillDone)
	}()
	fillReturnedEarly := returnsBeforeRelease(fillDone)
	close(releaseReconciliation)
	waitObserverCallback(t, fillDone, "OnFill completion")
	select {
	case err := <-resyncDone:
		if err != nil {
			t.Fatalf("Resync: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Resync completion")
	}
	if fillReturnedEarly {
		t.Error("execution callback returned while OnReconciliation was blocked; OnFill overlapped OnReconciliation")
	}

	observer.blockKind = ""
	node.Stop()
	select {
	case overlap := <-observer.overlap:
		t.Fatalf("observer callbacks overlapped: %s", overlap)
	default:
	}
	for _, want := range []string{"order", "fill", "health", "reconciliation"} {
		if !observerSaw(observer.seen, want) {
			t.Errorf("observer did not receive %s callback; saw %s", want, strings.Join(observer.seen, ", "))
		}
	}
}

func returnsBeforeRelease(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func waitObserverCallback(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func observerSaw(seen []string, want string) bool {
	for _, got := range seen {
		if got == want {
			return true
		}
	}
	return false
}
