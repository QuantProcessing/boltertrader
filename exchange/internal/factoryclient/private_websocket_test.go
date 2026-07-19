package factoryclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type fakePrivateWSBackend struct {
	mu sync.Mutex

	startOrdersCalls    map[string]int
	stopOrdersCalls     map[string]int
	orderCallbacks      map[string]streamCallbacks[exchange.OrderEvent]
	startFillsCalls     map[string]int
	stopFillsCalls      map[string]int
	fillCallbacks       map[string]streamCallbacks[exchange.FillEvent]
	startBalancesCalls  int
	stopBalancesCalls   int
	balanceCallbacks    streamCallbacks[exchange.BalanceEvent]
	startPositionsCalls map[string]int
	stopPositionsCalls  map[string]int
	positionCallbacks   map[string]streamCallbacks[exchange.PositionEvent]

	placeCalls  int
	cancelCalls int
	lastPlace   exchange.PlaceOrderRequest
	lastCancel  exchange.CancelOrderRequest
	closeCalls  int
}

func newFakePrivateWSBackend() *fakePrivateWSBackend {
	return &fakePrivateWSBackend{
		startOrdersCalls:    make(map[string]int),
		stopOrdersCalls:     make(map[string]int),
		orderCallbacks:      make(map[string]streamCallbacks[exchange.OrderEvent]),
		startFillsCalls:     make(map[string]int),
		stopFillsCalls:      make(map[string]int),
		fillCallbacks:       make(map[string]streamCallbacks[exchange.FillEvent]),
		startPositionsCalls: make(map[string]int),
		stopPositionsCalls:  make(map[string]int),
		positionCallbacks:   make(map[string]streamCallbacks[exchange.PositionEvent]),
	}
}

func (backend *fakePrivateWSBackend) StartOrders(
	_ context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.OrderEvent],
) (func() error, error) {
	backend.mu.Lock()
	backend.startOrdersCalls[instrument]++
	backend.orderCallbacks[instrument] = callbacks
	backend.mu.Unlock()
	return func() error {
		backend.mu.Lock()
		backend.stopOrdersCalls[instrument]++
		backend.mu.Unlock()
		return nil
	}, nil
}

func (backend *fakePrivateWSBackend) StartFills(
	_ context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.FillEvent],
) (func() error, error) {
	backend.mu.Lock()
	backend.startFillsCalls[instrument]++
	backend.fillCallbacks[instrument] = callbacks
	backend.mu.Unlock()
	return func() error {
		backend.mu.Lock()
		backend.stopFillsCalls[instrument]++
		backend.mu.Unlock()
		return nil
	}, nil
}

func (backend *fakePrivateWSBackend) StartBalances(
	_ context.Context,
	callbacks streamCallbacks[exchange.BalanceEvent],
) (func() error, error) {
	backend.mu.Lock()
	backend.startBalancesCalls++
	backend.balanceCallbacks = callbacks
	backend.mu.Unlock()
	return func() error {
		backend.mu.Lock()
		backend.stopBalancesCalls++
		backend.mu.Unlock()
		return nil
	}, nil
}

func (backend *fakePrivateWSBackend) StartPositions(
	_ context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.PositionEvent],
) (func() error, error) {
	backend.mu.Lock()
	backend.startPositionsCalls[instrument]++
	backend.positionCallbacks[instrument] = callbacks
	backend.mu.Unlock()
	return func() error {
		backend.mu.Lock()
		backend.stopPositionsCalls[instrument]++
		backend.mu.Unlock()
		return nil
	}, nil
}

func (backend *fakePrivateWSBackend) PlaceOrder(
	_ context.Context,
	request exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	backend.mu.Lock()
	backend.placeCalls++
	backend.lastPlace = request
	backend.mu.Unlock()
	return exchange.OrderAcknowledgement{
		Venue:         exchange.VenueLighter,
		Product:       exchange.ProductSpot,
		Operation:     exchange.OrderOperationPlace,
		State:         exchange.AckAcceptedPending,
		Instrument:    request.Instrument,
		ClientOrderID: request.ClientOrderID,
	}, nil
}

func (backend *fakePrivateWSBackend) CancelOrder(
	_ context.Context,
	request exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	backend.mu.Lock()
	backend.cancelCalls++
	backend.lastCancel = request
	backend.mu.Unlock()
	return exchange.OrderAcknowledgement{
		Venue:      exchange.VenueLighter,
		Product:    exchange.ProductSpot,
		Operation:  exchange.OrderOperationCancel,
		State:      exchange.AckCanceled,
		Instrument: request.Instrument,
		OrderID:    request.OrderID,
	}, nil
}

func (backend *fakePrivateWSBackend) Close() error {
	backend.mu.Lock()
	backend.closeCalls++
	backend.mu.Unlock()
	return nil
}

func TestSpotWebSocketPrivateStreamsAreSemanticallyIsolated(t *testing.T) {
	privateBackend := newFakePrivateWSBackend()
	socket := newSpotWebSocket(
		newPublicWebSocket(clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}, &fakePublicWSBackend{}),
		privateBackend,
	)
	orders, err := socket.WatchOrders(context.Background(), exchange.WatchRequest{Instrument: "ETH-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	fills, err := socket.WatchFills(context.Background(), exchange.WatchRequest{Instrument: "ETH-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if privateBackend.startOrdersCalls["ETH-USDC"] != 1 || privateBackend.startFillsCalls["ETH-USDC"] != 1 {
		t.Fatalf("starts orders=%d fills=%d, want one each", privateBackend.startOrdersCalls["ETH-USDC"], privateBackend.startFillsCalls["ETH-USDC"])
	}

	privateBackend.orderCallbacks["ETH-USDC"].Event(exchange.OrderEvent{
		Kind: exchange.EventDelta,
		Order: exchange.Order{
			Instrument: "ETH-USDC",
			OrderID:    "11",
		},
	})
	if got := <-orders.Events(); got.Order.OrderID != "11" {
		t.Fatalf("order event = %+v", got)
	}
	select {
	case got := <-fills.Events():
		t.Fatalf("order event leaked to fills: %+v", got)
	default:
	}

	privateBackend.fillCallbacks["ETH-USDC"].Event(exchange.FillEvent{
		Kind: exchange.EventDelta,
		Fill: exchange.Fill{
			Instrument: "ETH-USDC",
			FillID:     "fill-1",
		},
	})
	if got := <-fills.Events(); got.Fill.FillID != "fill-1" {
		t.Fatalf("fill event = %+v", got)
	}
	select {
	case got := <-orders.Events():
		t.Fatalf("fill event leaked to orders: %+v", got)
	default:
	}
}

func TestSpotWebSocketPrivateStreamsShareExactSemanticTopic(t *testing.T) {
	privateBackend := newFakePrivateWSBackend()
	socket := newSpotWebSocket(
		newPublicWebSocket(clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}, &fakePublicWSBackend{}),
		privateBackend,
	)
	first, err := socket.WatchOrders(context.Background(), exchange.WatchRequest{Instrument: "ETH-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := socket.WatchOrders(context.Background(), exchange.WatchRequest{Instrument: "ETH-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := socket.WatchOrders(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if privateBackend.startOrdersCalls["ETH-USDC"] != 1 || privateBackend.startOrdersCalls["BTC-USDC"] != 1 {
		t.Fatalf("starts = %+v, want one per instrument", privateBackend.startOrdersCalls)
	}

	privateBackend.orderCallbacks["ETH-USDC"].Event(exchange.OrderEvent{Order: exchange.Order{Instrument: "ETH-USDC", OrderID: "eth"}})
	if got := <-first.Events(); got.Order.OrderID != "eth" {
		t.Fatalf("first order = %+v", got)
	}
	if got := <-second.Events(); got.Order.OrderID != "eth" {
		t.Fatalf("second order = %+v", got)
	}
	select {
	case got := <-third.Events():
		t.Fatalf("ETH backend key leaked to BTC subscriber: %+v", got)
	default:
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if privateBackend.stopOrdersCalls["ETH-USDC"] != 0 {
		t.Fatal("closing one shared private subscriber stopped the backend topic")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if privateBackend.stopOrdersCalls["ETH-USDC"] != 1 || privateBackend.stopOrdersCalls["BTC-USDC"] != 1 {
		t.Fatalf("stops = %+v, want one per instrument", privateBackend.stopOrdersCalls)
	}
}

func TestPrivateWebSocketStatusAndBackpressureAreObservable(t *testing.T) {
	privateBackend := newFakePrivateWSBackend()
	socket := newSpotWebSocket(
		newPublicWebSocket(clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}, &fakePublicWSBackend{}),
		privateBackend,
	)
	subscription, err := socket.WatchOrders(context.Background(), exchange.WatchRequest{
		Instrument: "ETH-USDC",
		Options:    exchange.WatchOptions{Buffer: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStatusState(t, subscription.Status(), exchange.SubscriptionConnecting)
	assertStatusState(t, subscription.Status(), exchange.SubscriptionActive)

	privateBackend.orderCallbacks["ETH-USDC"].Event(exchange.OrderEvent{Order: exchange.Order{Instrument: "ETH-USDC", OrderID: "1"}})
	blocked := make(chan struct{})
	go func() {
		privateBackend.orderCallbacks["ETH-USDC"].Event(exchange.OrderEvent{Order: exchange.Order{Instrument: "ETH-USDC", OrderID: "2"}})
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("second private event bypassed bounded backpressure")
	case <-time.After(30 * time.Millisecond):
	}
	if got := <-subscription.Events(); got.Order.OrderID != "1" {
		t.Fatalf("first order = %+v", got)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("draining the private channel did not release the producer")
	}
	if got := <-subscription.Events(); got.Order.OrderID != "2" {
		t.Fatalf("second order = %+v", got)
	}

	privateBackend.orderCallbacks["ETH-USDC"].Status(backendStatus{
		State:      exchange.SubscriptionGap,
		Phase:      exchange.GapStarted,
		Generation: 7,
		Reason:     "private transport disconnected",
	})
	status := <-subscription.Status()
	if status.State != exchange.SubscriptionGap ||
		status.Phase != exchange.GapStarted ||
		status.Generation != 7 ||
		status.StreamID != subscription.ID() {
		t.Fatalf("status = %+v", status)
	}
}

func TestSpotWebSocketBalancesAreAccountWide(t *testing.T) {
	privateBackend := newFakePrivateWSBackend()
	socket := newSpotWebSocket(
		newPublicWebSocket(clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}, &fakePublicWSBackend{}),
		privateBackend,
	)
	first, err := socket.WatchBalances(context.Background(), exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := socket.WatchBalances(context.Background(), exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if privateBackend.startBalancesCalls != 1 {
		t.Fatalf("balance starts = %d, want 1", privateBackend.startBalancesCalls)
	}
	event := exchange.BalanceEvent{
		Kind: exchange.EventSnapshot,
		Balances: []exchange.Balance{{
			Asset:     "USDC",
			Available: decimal.RequireFromString("10"),
			Total:     decimal.RequireFromString("10"),
		}},
	}
	privateBackend.balanceCallbacks.Event(event)
	if got := <-first.Events(); got.Balances[0].Asset != "USDC" {
		t.Fatalf("first balance = %+v", got)
	}
	if got := <-second.Events(); got.Balances[0].Asset != "USDC" {
		t.Fatalf("second balance = %+v", got)
	}
}

func TestPerpWebSocketPositionsArePerpOnlyAndInstrumentKeyed(t *testing.T) {
	privateBackend := newFakePrivateWSBackend()
	socket := newPerpWebSocket(
		clientMeta{venue: exchange.VenueLighter, product: exchange.ProductPerp},
		&fakePerpWSBackend{},
		privateBackend,
	)
	first, err := socket.WatchPositions(context.Background(), exchange.WatchRequest{Instrument: "ETH"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := socket.WatchPositions(context.Background(), exchange.WatchRequest{Instrument: "BTC"})
	if err != nil {
		t.Fatal(err)
	}
	if privateBackend.startPositionsCalls["ETH"] != 1 || privateBackend.startPositionsCalls["BTC"] != 1 {
		t.Fatalf("position starts = %+v, want one per instrument", privateBackend.startPositionsCalls)
	}

	privateBackend.positionCallbacks["ETH"].Event(exchange.PositionEvent{
		Kind: exchange.EventSnapshot,
		Positions: []exchange.Position{{
			Instrument: "ETH",
			Quantity:   decimal.RequireFromString("2"),
		}},
	})
	if got := <-first.Events(); got.Positions[0].Instrument != "ETH" {
		t.Fatalf("ETH position = %+v", got)
	}
	select {
	case got := <-second.Events():
		t.Fatalf("ETH position leaked to BTC subscriber: %+v", got)
	default:
	}

	spot := newSpotWebSocket(
		newPublicWebSocket(clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}, &fakePublicWSBackend{}),
		privateBackend,
	)
	if _, ok := any(spot).(interface {
		WatchPositions(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.PositionEvent], error)
	}); ok {
		t.Fatal("spot websocket exposes perp-only positions")
	}
}

func TestPrivateWebSocketCommandsValidateAndPassThroughAcknowledgements(t *testing.T) {
	privateBackend := newFakePrivateWSBackend()
	socket := newSpotWebSocket(
		newPublicWebSocket(clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}, &fakePublicWSBackend{}),
		privateBackend,
	)
	place := exchange.PlaceOrderRequest{
		Instrument:    "ETH-USDC",
		ClientOrderID: "123",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.RequireFromString("1"),
	}
	ack, err := socket.PlaceOrder(context.Background(), place)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Operation != exchange.OrderOperationPlace || ack.ClientOrderID != "123" {
		t.Fatalf("place ack = %+v", ack)
	}
	if privateBackend.placeCalls != 1 || privateBackend.lastPlace.Instrument != "ETH-USDC" {
		t.Fatalf("place pass-through calls=%d request=%+v", privateBackend.placeCalls, privateBackend.lastPlace)
	}
	if _, err := socket.PlaceOrder(nil, place); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("nil PlaceOrder context error = %v, want invalid request", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := socket.PlaceOrder(canceled, place); !errors.Is(err, exchange.ErrCanceled) {
		t.Fatalf("canceled PlaceOrder context error = %v, want canceled", err)
	}
	if _, err := socket.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{}); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("invalid PlaceOrder request error = %v, want invalid request", err)
	}
	missingClientOrderID := place
	missingClientOrderID.ClientOrderID = ""
	if _, err := socket.PlaceOrder(context.Background(), missingClientOrderID); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("missing client order ID error = %v, want invalid request", err)
	}
	if privateBackend.placeCalls != 1 {
		t.Fatalf("invalid PlaceOrder request reached backend; calls = %d, want 1 valid call", privateBackend.placeCalls)
	}

	cancelAck, err := socket.CancelOrder(context.Background(), exchange.CancelOrderRequest{
		Instrument: "ETH-USDC",
		OrderID:    "99",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelAck.Operation != exchange.OrderOperationCancel || cancelAck.OrderID != "99" {
		t.Fatalf("cancel ack = %+v", cancelAck)
	}
	nadoDigest := "0x1111111111111111111111111111111111111111111111111111111111111111"
	cancelAck, err = socket.CancelOrder(context.Background(), exchange.CancelOrderRequest{
		Instrument: "ETH-USDT0",
		OrderID:    nadoDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelAck.Operation != exchange.OrderOperationCancel || cancelAck.OrderID != nadoDigest {
		t.Fatalf("digest cancel ack = %+v", cancelAck)
	}
	bybitUUID := "cf55eb56-0853-4d3f-945e-17ddd6059a89"
	cancelAck, err = socket.CancelOrder(context.Background(), exchange.CancelOrderRequest{
		Instrument: "BTC-USDT",
		OrderID:    bybitUUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelAck.Operation != exchange.OrderOperationCancel || cancelAck.OrderID != bybitUUID {
		t.Fatalf("UUID cancel ack = %+v", cancelAck)
	}
	if _, err := socket.CancelOrder(nil, exchange.CancelOrderRequest{Instrument: "ETH-USDC", OrderID: "99"}); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("nil CancelOrder context error = %v, want invalid request", err)
	}
	if _, err := socket.CancelOrder(canceled, exchange.CancelOrderRequest{Instrument: "ETH-USDC", OrderID: "99"}); !errors.Is(err, exchange.ErrCanceled) {
		t.Fatalf("canceled CancelOrder context error = %v, want canceled", err)
	}
	if _, err := socket.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: "ETH-USDC"}); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("invalid CancelOrder request error = %v, want invalid request", err)
	}
	for _, orderID := range []string{"0", "01", "not-an-order-id", "9223372036854775808"} {
		if _, err := socket.CancelOrder(context.Background(), exchange.CancelOrderRequest{
			Instrument: "ETH-USDC",
			OrderID:    orderID,
		}); !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Errorf("CancelOrder order id %q error = %v, want invalid request", orderID, err)
		}
	}
	if privateBackend.cancelCalls != 3 {
		t.Fatalf("invalid cancel requests reached backend; calls = %d, want 3 valid calls", privateBackend.cancelCalls)
	}
}

type blockingPrivateWSBackend struct {
	started    chan struct{}
	closeCalls atomic.Int32
}

func newBlockingPrivateWSBackend() *blockingPrivateWSBackend {
	return &blockingPrivateWSBackend{started: make(chan struct{})}
}

func (backend *blockingPrivateWSBackend) StartOrders(
	ctx context.Context,
	_ string,
	_ streamCallbacks[exchange.OrderEvent],
) (func() error, error) {
	close(backend.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (backend *blockingPrivateWSBackend) StartFills(
	context.Context,
	string,
	streamCallbacks[exchange.FillEvent],
) (func() error, error) {
	return func() error { return nil }, nil
}

func (backend *blockingPrivateWSBackend) StartBalances(
	context.Context,
	streamCallbacks[exchange.BalanceEvent],
) (func() error, error) {
	return func() error { return nil }, nil
}

func (backend *blockingPrivateWSBackend) PlaceOrder(
	context.Context,
	exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, nil
}

func (backend *blockingPrivateWSBackend) CancelOrder(
	context.Context,
	exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, nil
}

func (backend *blockingPrivateWSBackend) Close() error {
	backend.closeCalls.Add(1)
	return nil
}

func TestPrivateWebSocketCloseCancelsPendingStartupWithoutDeadlockAndIsIdempotent(t *testing.T) {
	privateBackend := newBlockingPrivateWSBackend()
	socket := newSpotWebSocket(
		newPublicWebSocket(clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}, &fakePublicWSBackend{}),
		privateBackend,
	)
	watchResult := make(chan error, 1)
	go func() {
		_, err := socket.WatchOrders(context.Background(), exchange.WatchRequest{Instrument: "ETH-USDC"})
		watchResult <- err
	}()
	<-privateBackend.started

	closeResult := make(chan error, 1)
	go func() { closeResult <- socket.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked behind pending private startup")
	}
	select {
	case err := <-watchResult:
		if err == nil {
			t.Fatal("pending WatchOrders succeeded after client close")
		}
	case <-time.After(time.Second):
		t.Fatal("pending WatchOrders did not observe client close")
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if privateBackend.closeCalls.Load() != 1 {
		t.Fatalf("private backend Close calls = %d, want 1", privateBackend.closeCalls.Load())
	}
}
