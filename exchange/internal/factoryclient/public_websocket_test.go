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

type fakePublicWSBackend struct {
	mu sync.Mutex

	startBBOCalls int
	stopBBOCalls  int
	closeCalls    int
	bboCallbacks  streamCallbacks[exchange.BBOEvent]

	startCandleCalls int
	stopCandleCalls  int
	candleIntervals  []string
}

func (backend *fakePublicWSBackend) StartOrderBook(
	context.Context,
	string,
	streamCallbacks[exchange.BookEvent],
) (func() error, error) {
	return func() error { return nil }, nil
}

func (backend *fakePublicWSBackend) StartBBO(
	_ context.Context,
	_ string,
	callbacks streamCallbacks[exchange.BBOEvent],
) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.startBBOCalls++
	backend.bboCallbacks = callbacks
	return func() error {
		backend.mu.Lock()
		backend.stopBBOCalls++
		backend.mu.Unlock()
		return nil
	}, nil
}

func (backend *fakePublicWSBackend) StartPublicTrades(
	context.Context,
	string,
	streamCallbacks[exchange.PublicTradeEvent],
) (func() error, error) {
	return func() error { return nil }, nil
}

func (backend *fakePublicWSBackend) StartCandles(
	_ context.Context,
	_ string,
	interval string,
	_ streamCallbacks[exchange.CandleEvent],
) (func() error, error) {
	backend.mu.Lock()
	backend.startCandleCalls++
	backend.candleIntervals = append(backend.candleIntervals, interval)
	backend.mu.Unlock()
	return func() error {
		backend.mu.Lock()
		backend.stopCandleCalls++
		backend.mu.Unlock()
		return nil
	}, nil
}

func (backend *fakePublicWSBackend) Close() error {
	backend.mu.Lock()
	backend.closeCalls++
	backend.mu.Unlock()
	return nil
}

func (backend *fakePublicWSBackend) emitBBO(event exchange.BBOEvent) {
	backend.mu.Lock()
	emit := backend.bboCallbacks.Event
	backend.mu.Unlock()
	emit(event)
}

func (backend *fakePublicWSBackend) statusBBO(status backendStatus) {
	backend.mu.Lock()
	emit := backend.bboCallbacks.Status
	backend.mu.Unlock()
	emit(status)
}

func TestPublicWebSocketIsLazyAndSharesOneNativeTopic(t *testing.T) {
	backend := &fakePublicWSBackend{}
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueBinance, product: exchange.ProductSpot},
		backend,
	)
	if backend.startBBOCalls != 0 {
		t.Fatal("construction performed websocket I/O")
	}

	first, err := socket.WatchBBO(context.Background(), exchange.WatchRequest{
		Instrument: "BTC-USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := socket.WatchBBO(context.Background(), exchange.WatchRequest{
		Instrument: "BTC-USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == "" || second.ID() == "" || first.ID() == second.ID() {
		t.Fatalf("subscription IDs are not stable and unique: %q %q", first.ID(), second.ID())
	}
	if backend.startBBOCalls != 1 {
		t.Fatalf("native topic starts = %d, want 1", backend.startBBOCalls)
	}

	event := exchange.BBOEvent{
		Instrument: "BTC-USDT",
		Bid: exchange.BookLevel{
			Price:    decimal.RequireFromString("100"),
			Quantity: decimal.RequireFromString("2"),
		},
		Ask: exchange.BookLevel{
			Price:    decimal.RequireFromString("101"),
			Quantity: decimal.RequireFromString("3"),
		},
	}
	go backend.emitBBO(event)
	assertBBOEvent(t, first.Events(), event)
	assertBBOEvent(t, second.Events(), event)

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.stopBBOCalls != 0 {
		t.Fatal("closing one local subscriber stopped the shared native topic")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.stopBBOCalls != 1 {
		t.Fatalf("native topic stops = %d, want 1", backend.stopBBOCalls)
	}
}

func TestPublicWebSocketStatusAndBackpressureAreObservable(t *testing.T) {
	backend := &fakePublicWSBackend{}
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueOKX, product: exchange.ProductSpot},
		backend,
	)
	subscription, err := socket.WatchBBO(context.Background(), exchange.WatchRequest{
		Instrument: "BTC-USDT",
		Options:    exchange.WatchOptions{Buffer: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStatusState(t, subscription.Status(), exchange.SubscriptionConnecting)
	assertStatusState(t, subscription.Status(), exchange.SubscriptionActive)

	first := exchange.BBOEvent{Instrument: "BTC-USDT"}
	second := exchange.BBOEvent{Instrument: "BTC-USDT"}
	backend.emitBBO(first)
	blocked := make(chan struct{})
	go func() {
		backend.emitBBO(second)
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("second event bypassed bounded backpressure")
	case <-time.After(30 * time.Millisecond):
	}
	assertBBOEvent(t, subscription.Events(), first)
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("draining the channel did not release the producer")
	}
	assertBBOEvent(t, subscription.Events(), second)

	backend.statusBBO(backendStatus{
		State:      exchange.SubscriptionGap,
		Phase:      exchange.GapStarted,
		Generation: 3,
		Reason:     "transport disconnected",
	})
	status := <-subscription.Status()
	if status.State != exchange.SubscriptionGap ||
		status.Phase != exchange.GapStarted ||
		status.Generation != 3 ||
		status.Venue != exchange.VenueOKX ||
		status.Product != exchange.ProductSpot ||
		status.StreamID != subscription.ID() {
		t.Fatalf("status = %+v", status)
	}
}

func TestPublicWebSocketContextAndClientCloseAreIdempotent(t *testing.T) {
	backend := &fakePublicWSBackend{}
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductSpot},
		backend,
	)
	ctx, cancel := context.WithCancel(context.Background())
	subscription, err := socket.WatchBBO(ctx, exchange.WatchRequest{Instrument: "BTC-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, open := <-subscription.Events():
		if open {
			t.Fatal("events channel remained open after context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close subscription")
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("backend Close calls = %d, want 1", backend.closeCalls)
	}
}

func TestPublicWebSocketRejectsInvalidWatchRequestsBeforeBackend(t *testing.T) {
	backend := &fakePublicWSBackend{}
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot},
		backend,
	)
	for _, request := range []exchange.WatchRequest{
		{},
		{Instrument: " BTC-USDC"},
		{Instrument: "BTC-USDC", Options: exchange.WatchOptions{Buffer: -1}},
		{Instrument: "BTC-USDC", Options: exchange.WatchOptions{Buffer: maxWebSocketBuffer + 1}},
	} {
		_, err := socket.WatchBBO(context.Background(), request)
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("WatchBBO(%+v) error = %v, want invalid request", request, err)
		}
	}
	if backend.startBBOCalls != 0 {
		t.Fatal("invalid requests reached backend")
	}
}

func TestPublicWebSocketCandlesShareOnlyExactInstrumentIntervalTopic(t *testing.T) {
	backend := &fakePublicWSBackend{}
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueLighter, product: exchange.ProductPerp},
		backend,
	)
	first, err := socket.WatchCandles(context.Background(), exchange.WatchCandlesRequest{
		Instrument: "ETH",
		Interval:   "1m",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := socket.WatchCandles(context.Background(), exchange.WatchCandlesRequest{
		Instrument: "ETH",
		Interval:   "1m",
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := socket.WatchCandles(context.Background(), exchange.WatchCandlesRequest{
		Instrument: "ETH",
		Interval:   "5m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend.startCandleCalls != 2 {
		t.Fatalf("native candle topics = %d, want 2", backend.startCandleCalls)
	}
	if got := backend.candleIntervals; len(got) != 2 || got[0] != "1m" || got[1] != "5m" {
		t.Fatalf("native candle intervals = %v", got)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.stopCandleCalls != 0 {
		t.Fatal("closing one shared candle subscriber stopped its native topic")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.stopCandleCalls != 2 {
		t.Fatalf("native candle stops = %d, want 2", backend.stopCandleCalls)
	}
}

func TestPublicWebSocketCandlesRejectIntervalsOutsideStrictIntersection(t *testing.T) {
	backend := &fakePublicWSBackend{}
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot},
		backend,
	)
	for _, interval := range []string{"", "3m", "1M", " 1m"} {
		_, err := socket.WatchCandles(context.Background(), exchange.WatchCandlesRequest{
			Instrument: "ETH-USDC",
			Interval:   interval,
		})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("WatchCandles(%q) error = %v, want invalid request", interval, err)
		}
	}
	if backend.startCandleCalls != 0 {
		t.Fatal("invalid candle intervals reached backend")
	}
}

func TestPublicWebSocketConcurrentCloseDoesNotRaceEmit(t *testing.T) {
	backend := &fakePublicWSBackend{}
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueBinance, product: exchange.ProductSpot},
		backend,
	)
	subscription, err := socket.WatchBBO(context.Background(), exchange.WatchRequest{
		Instrument: "BTC-USDT",
		Options:    exchange.WatchOptions{Buffer: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var completed atomic.Int32
	go func() {
		for range 100 {
			backend.emitBBO(exchange.BBOEvent{Instrument: "BTC-USDT"})
		}
		completed.Add(1)
	}()
	go func() {
		_ = subscription.Close()
		completed.Add(1)
	}()
	deadline := time.After(time.Second)
	for completed.Load() != 2 {
		select {
		case <-deadline:
			t.Fatal("concurrent emit/close deadlocked")
		case <-subscription.Events():
		}
	}
}

type blockingStartupWSBackend struct {
	bboStarted       chan struct{}
	referenceStarted chan struct{}
	closeCalls       atomic.Int32
}

func newBlockingStartupWSBackend() *blockingStartupWSBackend {
	return &blockingStartupWSBackend{
		bboStarted:       make(chan struct{}),
		referenceStarted: make(chan struct{}),
	}
}

func (backend *blockingStartupWSBackend) StartOrderBook(
	context.Context,
	string,
	streamCallbacks[exchange.BookEvent],
) (func() error, error) {
	return func() error { return nil }, nil
}

func (backend *blockingStartupWSBackend) StartBBO(
	ctx context.Context,
	_ string,
	_ streamCallbacks[exchange.BBOEvent],
) (func() error, error) {
	close(backend.bboStarted)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (backend *blockingStartupWSBackend) StartPublicTrades(
	context.Context,
	string,
	streamCallbacks[exchange.PublicTradeEvent],
) (func() error, error) {
	return func() error { return nil }, nil
}

func (backend *blockingStartupWSBackend) StartCandles(
	context.Context,
	string,
	string,
	streamCallbacks[exchange.CandleEvent],
) (func() error, error) {
	return func() error { return nil }, nil
}

func (backend *blockingStartupWSBackend) StartReference(
	ctx context.Context,
	_ string,
	_ streamCallbacks[perpReferenceEvent],
) (func() error, error) {
	close(backend.referenceStarted)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (backend *blockingStartupWSBackend) Close() error {
	backend.closeCalls.Add(1)
	return nil
}

func TestPublicWebSocketCloseCancelsPendingStartupWithoutDeadlock(t *testing.T) {
	backend := newBlockingStartupWSBackend()
	socket := newPublicWebSocket(
		clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot},
		backend,
	)
	watchResult := make(chan error, 1)
	go func() {
		_, err := socket.WatchBBO(context.Background(), exchange.WatchRequest{
			Instrument: "ETH-USDC",
		})
		watchResult <- err
	}()
	<-backend.bboStarted

	closeResult := make(chan error, 1)
	go func() { closeResult <- socket.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked behind pending websocket startup")
	}
	select {
	case err := <-watchResult:
		if err == nil {
			t.Fatal("pending WatchBBO succeeded after client close")
		}
	case <-time.After(time.Second):
		t.Fatal("pending WatchBBO did not observe client close")
	}
	if backend.closeCalls.Load() != 1 {
		t.Fatalf("backend Close calls = %d, want 1", backend.closeCalls.Load())
	}
}

func assertBBOEvent(t *testing.T, events <-chan exchange.BBOEvent, want exchange.BBOEvent) {
	t.Helper()
	select {
	case got := <-events:
		if got.Instrument != want.Instrument ||
			!got.Bid.Price.Equal(want.Bid.Price) ||
			!got.Ask.Price.Equal(want.Ask.Price) {
			t.Fatalf("event = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for BBO event")
	}
}

func assertStatusState(t *testing.T, statuses <-chan exchange.StreamStatusEvent, want exchange.SubscriptionState) {
	t.Helper()
	select {
	case got := <-statuses:
		if got.State != want {
			t.Fatalf("status state = %q, want %q", got.State, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s status", want)
	}
}
