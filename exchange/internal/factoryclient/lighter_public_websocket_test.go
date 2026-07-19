package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type fakeLighterPublicWS struct {
	mu sync.Mutex

	connectCalls int
	closeCalls   int

	errorHandler       func(error)
	reconnectStarted   func(error)
	reconnectRecovered func()

	orderBookHandlers map[int]func([]byte)
	tickerHandlers    map[int]func([]byte)
	tradeHandlers     map[int]func([]byte)
	statsHandlers     map[int]func([]byte)
	candleHandlers    map[string]func([]byte)

	unsubscribeOrderBookCalls int
	unsubscribeTickerCalls    int
	unsubscribeTradeCalls     int
	unsubscribeStatsCalls     int
	unsubscribeCandleCalls    int
}

func newFakeLighterPublicWS() *fakeLighterPublicWS {
	return &fakeLighterPublicWS{
		orderBookHandlers: make(map[int]func([]byte)),
		tickerHandlers:    make(map[int]func([]byte)),
		tradeHandlers:     make(map[int]func([]byte)),
		statsHandlers:     make(map[int]func([]byte)),
		candleHandlers:    make(map[string]func([]byte)),
	}
}

func (ws *fakeLighterPublicWS) Connect() error {
	ws.mu.Lock()
	ws.connectCalls++
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) Close() {
	ws.mu.Lock()
	ws.closeCalls++
	ws.mu.Unlock()
}

func (ws *fakeLighterPublicWS) SetErrorHandler(handler func(error)) {
	ws.mu.Lock()
	ws.errorHandler = handler
	ws.mu.Unlock()
}

func (ws *fakeLighterPublicWS) SetReconnectHooks(started func(error), recovered func()) {
	ws.mu.Lock()
	ws.reconnectStarted = started
	ws.reconnectRecovered = recovered
	ws.mu.Unlock()
}

func (ws *fakeLighterPublicWS) SetSubscriptionAuthProvider(func(string) (*string, error)) {}

func (ws *fakeLighterPublicWS) SubscribeOrderBook(marketID int, handler func([]byte)) error {
	ws.mu.Lock()
	ws.orderBookHandlers[marketID] = handler
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) UnsubscribeOrderBook(marketID int) error {
	ws.mu.Lock()
	delete(ws.orderBookHandlers, marketID)
	ws.unsubscribeOrderBookCalls++
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) SubscribeTicker(marketID int, handler func([]byte)) error {
	ws.mu.Lock()
	ws.tickerHandlers[marketID] = handler
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) UnsubscribeTicker(marketID int) error {
	ws.mu.Lock()
	delete(ws.tickerHandlers, marketID)
	ws.unsubscribeTickerCalls++
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) SubscribeTrades(marketID int, handler func([]byte)) error {
	ws.mu.Lock()
	ws.tradeHandlers[marketID] = handler
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) UnsubscribeTrades(marketID int) error {
	ws.mu.Lock()
	delete(ws.tradeHandlers, marketID)
	ws.unsubscribeTradeCalls++
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) SubscribeMarketStats(marketID int, handler func([]byte)) error {
	ws.mu.Lock()
	ws.statsHandlers[marketID] = handler
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) UnsubscribeMarketStats(marketID int) error {
	ws.mu.Lock()
	delete(ws.statsHandlers, marketID)
	ws.unsubscribeStatsCalls++
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) SubscribeCandle(
	marketID int,
	resolution string,
	handler func([]byte),
) error {
	ws.mu.Lock()
	ws.candleHandlers[fmt.Sprintf("%d/%s", marketID, resolution)] = handler
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) UnsubscribeCandle(marketID int, resolution string) error {
	ws.mu.Lock()
	delete(ws.candleHandlers, fmt.Sprintf("%d/%s", marketID, resolution))
	ws.unsubscribeCandleCalls++
	ws.mu.Unlock()
	return nil
}

func (ws *fakeLighterPublicWS) emitOrderBook(marketID int, payload string) {
	ws.mu.Lock()
	handler := ws.orderBookHandlers[marketID]
	ws.mu.Unlock()
	if handler != nil {
		handler([]byte(payload))
	}
}

func (ws *fakeLighterPublicWS) emitTicker(marketID int, payload string) {
	ws.mu.Lock()
	handler := ws.tickerHandlers[marketID]
	ws.mu.Unlock()
	if handler != nil {
		handler([]byte(payload))
	}
}

func (ws *fakeLighterPublicWS) emitTrades(marketID int, payload string) {
	ws.mu.Lock()
	handler := ws.tradeHandlers[marketID]
	ws.mu.Unlock()
	if handler != nil {
		handler([]byte(payload))
	}
}

func (ws *fakeLighterPublicWS) emitStats(marketID int, payload string) {
	ws.mu.Lock()
	handler := ws.statsHandlers[marketID]
	ws.mu.Unlock()
	if handler != nil {
		handler([]byte(payload))
	}
}

func (ws *fakeLighterPublicWS) emitCandle(marketID int, resolution string, payload string) {
	ws.mu.Lock()
	handler := ws.candleHandlers[fmt.Sprintf("%d/%s", marketID, resolution)]
	ws.mu.Unlock()
	if handler != nil {
		handler([]byte(payload))
	}
}

func newTestLighterWSBackend(
	product exchange.Product,
	marketType string,
	ws *fakeLighterPublicWS,
) *lighterWSBackend {
	return newTestLighterWSBackendForMarket(product, marketType, 7, ws)
}

func newTestLighterWSBackendForMarket(
	product exchange.Product,
	marketType string,
	marketID int,
	ws *fakeLighterPublicWS,
) *lighterWSBackend {
	meta := lighterMarketMeta{
		instrument: exchange.Instrument{
			Symbol:  "ETH-USDC",
			Product: product,
		},
		marketID:   marketID,
		marketType: marketType,
	}
	state := newLighterRESTState()
	state.metas = map[string]lighterMarketMeta{meta.instrument.Symbol: meta}
	state.byID = map[int]lighterMarketMeta{meta.marketID: meta}
	return newLighterWSBackend(product, marketType, lighter.NewClient(), state, ws)
}

func TestNewLighterWSBackendsHonorDedicatedWebSocketEndpointWithoutIO(t *testing.T) {
	settings := Settings{
		Environment:       "testnet",
		WebSocketEndpoint: "ws://127.0.0.1:43210/stream",
	}
	for _, backend := range []*lighterWSBackend{
		newLighterSpotWSBackend(lighter.NewClient(), newLighterRESTState(), settings).(*lighterWSBackend),
		newLighterPerpWSBackend(lighter.NewClient(), newLighterRESTState(), settings).(*lighterWSBackend),
	} {
		ws, ok := backend.ws.(*lighter.WebsocketClient)
		if !ok {
			t.Fatalf("native websocket type = %T", backend.ws)
		}
		if ws.URL != settings.WebSocketEndpoint {
			t.Fatalf("websocket URL = %q, want %q", ws.URL, settings.WebSocketEndpoint)
		}
		if ws.Conn != nil {
			t.Fatal("backend construction performed websocket I/O")
		}
	}
}

func TestNewLighterClientsWireLazyWebSocketExchanges(t *testing.T) {
	settings := Settings{
		Environment:       "testnet",
		WebSocketEndpoint: "ws://127.0.0.1:43210/stream",
	}
	spot := NewLighterSpot("", 0, 0, settings).(*lighterSpotClient)
	perp := NewLighterPerp("", 0, 0, settings).(*lighterPerpClient)
	if spot.ws == nil || perp.ws == nil {
		t.Fatal("Lighter constructors did not wire websocket exchanges")
	}
	spotSocket := spot.ws.(*spotWebSocket)
	spotBackend := spotSocket.publicWebSocket.backend.(*lighterWSBackend)
	spotPrivate := spotSocket.private.backend.(*lighterPrivateWSBackend)
	perpSocket := perp.ws.(*perpWebSocket)
	perpBackend := perpSocket.backend.(*lighterWSBackend)
	perpPrivate := perpSocket.privateBackend.(*lighterPrivateWSBackend)
	for _, backend := range []*lighterWSBackend{spotBackend, perpBackend} {
		ws := backend.ws.(*lighter.WebsocketClient)
		if ws.URL != settings.WebSocketEndpoint || ws.Conn != nil {
			t.Fatalf("constructor websocket URL=%q conn=%v", ws.URL, ws.Conn)
		}
	}
	for _, backend := range []*lighterPrivateWSBackend{spotPrivate, perpPrivate} {
		ws := backend.ws.(*lighter.WebsocketClient)
		if ws.URL != settings.WebSocketEndpoint || ws.Conn != nil {
			t.Fatalf("private constructor websocket URL=%q conn=%v", ws.URL, ws.Conn)
		}
	}
}

func TestLighterPerpWebSocketAcceptsMarketIDZero(t *testing.T) {
	ws := newFakeLighterPublicWS()
	backend := newTestLighterWSBackendForMarket(exchange.ProductPerp, lighterPerp, 0, ws)
	resultCh := make(chan error, 1)
	eventCh := make(chan exchange.BBOEvent, 1)

	go func() {
		_, err := backend.StartBBO(context.Background(), "ETH-USDC", streamCallbacks[exchange.BBOEvent]{
			Event: func(event exchange.BBOEvent) { eventCh <- event },
		})
		resultCh <- err
	}()
	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.tickerHandlers[0] != nil
	})
	ws.emitTicker(0, `{"channel":"ticker:0","type":"update/ticker","timestamp":1700000000000,"ticker":{"b":{"price":"2000","size":"1"},"a":{"price":"2001","size":"1"}}}`)
	if got := <-eventCh; got.Instrument != "ETH-USDC" {
		t.Fatalf("market zero BBO = %+v", got)
	}
	if err := <-resultCh; err != nil {
		t.Fatal(err)
	}
}

func TestLighterStartBBOWaitsForFirstValidEventAndMapsTicker(t *testing.T) {
	ws := newFakeLighterPublicWS()
	backend := newTestLighterWSBackend(exchange.ProductSpot, lighterSpot, ws)
	type result struct {
		stop func() error
		err  error
	}
	resultCh := make(chan result, 1)
	eventCh := make(chan exchange.BBOEvent, 1)
	errorCh := make(chan error, 1)

	go func() {
		stop, err := backend.StartBBO(context.Background(), "ETH-USDC", streamCallbacks[exchange.BBOEvent]{
			Event: func(event exchange.BBOEvent) { eventCh <- event },
			Error: func(err error) { errorCh <- err },
		})
		resultCh <- result{stop: stop, err: err}
	}()

	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.tickerHandlers[7] != nil
	})
	select {
	case got := <-resultCh:
		t.Fatalf("StartBBO returned before the first valid event: %+v", got)
	default:
	}

	ws.emitTicker(7, `{"channel":"ticker:7","type":"subscribed/ticker","timestamp":1700000000000,"ticker":{"b":{"price":"2000.5","size":"1.25"},"a":{"price":"2001","size":"0.75"}}}`)
	got := <-eventCh
	if got.Instrument != "ETH-USDC" ||
		!got.Bid.Price.Equal(decimal.RequireFromString("2000.5")) ||
		!got.Bid.Quantity.Equal(decimal.RequireFromString("1.25")) ||
		!got.Ask.Price.Equal(decimal.RequireFromString("2001")) ||
		!got.Ask.Quantity.Equal(decimal.RequireFromString("0.75")) ||
		!got.Time.Equal(time.UnixMilli(1_700_000_000_000).UTC()) {
		t.Fatalf("BBO event = %+v", got)
	}
	started := <-resultCh
	if started.err != nil || started.stop == nil {
		t.Fatalf("StartBBO result = %+v", started)
	}
	if err := started.stop(); err != nil {
		t.Fatal(err)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.connectCalls != 1 || ws.unsubscribeTickerCalls != 1 {
		t.Fatalf("connect=%d unsubscribe=%d", ws.connectCalls, ws.unsubscribeTickerCalls)
	}
	select {
	case err := <-errorCh:
		t.Fatalf("unexpected stream error: %v", err)
	default:
	}
}

func TestLighterStartCandlesValidatesResolutionAndWaitsForFirstValidEvent(t *testing.T) {
	ws := newFakeLighterPublicWS()
	backend := newTestLighterWSBackend(exchange.ProductSpot, lighterSpot, ws)
	if _, err := backend.StartCandles(
		context.Background(),
		"ETH-USDC",
		"2m",
		streamCallbacks[exchange.CandleEvent]{},
	); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("unsupported resolution error = %v", err)
	}
	ws.mu.Lock()
	if ws.connectCalls != 0 {
		t.Fatal("unsupported resolution connected the websocket")
	}
	ws.mu.Unlock()

	type result struct {
		stop func() error
		err  error
	}
	resultCh := make(chan result, 1)
	eventCh := make(chan exchange.CandleEvent, 2)
	errorCh := make(chan error, 1)
	statusCh := make(chan backendStatus, 3)
	go func() {
		stop, err := backend.StartCandles(
			context.Background(),
			"ETH-USDC",
			"1m",
			streamCallbacks[exchange.CandleEvent]{
				Event:  func(event exchange.CandleEvent) { eventCh <- event },
				Status: func(status backendStatus) { statusCh <- status },
				Error:  func(err error) { errorCh <- err },
			},
		)
		resultCh <- result{stop: stop, err: err}
	}()
	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.candleHandlers["7/1m"] != nil
	})

	ws.emitCandle(7, "1m", `{"channel":"candle:7:1m","type":"update/candle","timestamp":1699999981000,"candles":[{"t":1699999980000,"o":100,"h":99,"l":98,"c":99,"v":2,"V":198,"i":10}]}`)
	select {
	case err := <-errorCh:
		if !errors.Is(err, exchange.ErrMalformedResponse) {
			t.Fatalf("malformed candle error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("malformed candle was silently dropped")
	}
	select {
	case got := <-resultCh:
		t.Fatalf("StartCandles returned before a valid event: %+v", got)
	default:
	}

	ws.emitCandle(7, "1m", `{"channel":"candle:7:1m","type":"update/candle","timestamp":1700000041000,"candles":[{"t":1699999980000,"o":100,"h":102,"l":99,"c":101,"v":2,"V":202,"i":10},{"t":1700000040000,"o":101,"h":103,"l":100,"c":102,"v":3,"V":306,"i":11}]}`)
	first := <-eventCh
	second := <-eventCh
	if first.Instrument != "ETH-USDC" || first.Interval != "1m" ||
		!first.Candle.Open.Equal(decimal.NewFromInt(100)) ||
		!first.Candle.Close.Equal(decimal.NewFromInt(101)) ||
		!first.Candle.Volume.Equal(decimal.NewFromInt(2)) ||
		!first.Candle.Complete {
		t.Fatalf("closed candle = %+v", first)
	}
	if !second.Candle.Open.Equal(decimal.NewFromInt(101)) ||
		!second.Candle.Close.Equal(decimal.NewFromInt(102)) ||
		second.Candle.Complete {
		t.Fatalf("live candle = %+v", second)
	}
	started := <-resultCh
	if started.err != nil || started.stop == nil {
		t.Fatalf("StartCandles result = %+v", started)
	}

	ws.mu.Lock()
	reconnectStarted := ws.reconnectStarted
	reconnectRecovered := ws.reconnectRecovered
	ws.mu.Unlock()
	reconnectStarted(errors.New("socket lost"))
	reconnectRecovered()
	gap := <-statusCh
	resyncing := <-statusCh
	select {
	case premature := <-statusCh:
		t.Fatalf("candle recovered before first replacement event: %+v", premature)
	default:
	}
	ws.emitCandle(7, "1m", `{"channel":"candle:7:1m","type":"update/candle","timestamp":1700000042000,"candles":[{"t":1700000040000,"o":101,"h":103,"l":100,"c":102,"v":4,"V":408,"i":12}]}`)
	<-eventCh
	recovered := <-statusCh
	if gap.State != exchange.SubscriptionGap || gap.Phase != exchange.GapStarted ||
		resyncing.State != exchange.SubscriptionResyncing ||
		recovered.State != exchange.SubscriptionActive || recovered.Phase != exchange.GapRecovered {
		t.Fatalf("candle reconnect statuses = %+v %+v %+v", gap, resyncing, recovered)
	}
	if err := started.stop(); err != nil {
		t.Fatal(err)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.unsubscribeCandleCalls != 1 {
		t.Fatalf("candle unsubscribe calls = %d", ws.unsubscribeCandleCalls)
	}
}

func TestLighterCandleResolutionMappingMatchesOfficialWebSocketSet(t *testing.T) {
	want := map[string]time.Duration{
		"1m":  time.Minute,
		"5m":  5 * time.Minute,
		"15m": 15 * time.Minute,
		"30m": 30 * time.Minute,
		"1h":  time.Hour,
		"4h":  4 * time.Hour,
		"12h": 12 * time.Hour,
		"1d":  24 * time.Hour,
	}
	for resolution, duration := range want {
		got, err := lighterWSCandleInterval(resolution)
		if err != nil || got != duration {
			t.Fatalf("resolution %q = %s, %v; want %s", resolution, got, err, duration)
		}
	}
	for _, unsupported := range []string{"", "2m", "60m", "1D"} {
		if _, err := lighterWSCandleInterval(unsupported); err == nil {
			t.Fatalf("unsupported resolution %q was accepted", unsupported)
		}
	}
}

func TestLighterStartBBOReportsMalformedPayloadAndRemainsConnecting(t *testing.T) {
	ws := newFakeLighterPublicWS()
	backend := newTestLighterWSBackend(exchange.ProductSpot, lighterSpot, ws)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan error, 1)
	errorCh := make(chan error, 1)

	go func() {
		_, err := backend.StartBBO(ctx, "ETH-USDC", streamCallbacks[exchange.BBOEvent]{
			Error: func(err error) { errorCh <- err },
		})
		resultCh <- err
	}()
	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.tickerHandlers[7] != nil
	})

	ws.emitTicker(7, `{"channel":"ticker:7","timestamp":1700000000000,"ticker":{"b":{"price":"bad","size":"1"},"a":{"price":"2","size":"1"}}}`)
	select {
	case err := <-errorCh:
		if !errors.Is(err, exchange.ErrMalformedResponse) {
			t.Fatalf("decode error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("malformed payload was silently dropped")
	}
	select {
	case err := <-resultCh:
		t.Fatalf("StartBBO returned after malformed payload: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-resultCh:
		if !errors.Is(err, exchange.ErrCanceled) {
			t.Fatalf("StartBBO cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StartBBO did not observe context cancellation")
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.unsubscribeTickerCalls != 1 {
		t.Fatalf("unsubscribe calls = %d, want 1", ws.unsubscribeTickerCalls)
	}
}

func TestLighterOrderBookDetectsNonceGapAndResynchronizes(t *testing.T) {
	ws := newFakeLighterPublicWS()
	backend := newTestLighterWSBackend(exchange.ProductSpot, lighterSpot, ws)
	eventCh := make(chan exchange.BookEvent, 4)
	statusCh := make(chan backendStatus, 4)
	errorCh := make(chan error, 1)
	resultCh := make(chan error, 1)

	var stop func() error
	go func() {
		var err error
		stop, err = backend.StartOrderBook(context.Background(), "ETH-USDC", streamCallbacks[exchange.BookEvent]{
			Event:  func(event exchange.BookEvent) { eventCh <- event },
			Status: func(status backendStatus) { statusCh <- status },
			Error:  func(err error) { errorCh <- err },
		})
		resultCh <- err
	}()
	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.orderBookHandlers[7] != nil
	})

	ws.emitOrderBook(7, `{"channel":"order_book:7","type":"subscribed/order_book","timestamp":1700000000000,"order_book":{"nonce":10,"begin_nonce":10,"bids":[{"price":"2000","size":"2"}],"asks":[{"price":"2001","size":"3"}]}}`)
	first := <-eventCh
	if first.Kind != exchange.EventSnapshot || first.Sequence != "10" || first.Resync {
		t.Fatalf("initial event = %+v", first)
	}
	if err := <-resultCh; err != nil {
		t.Fatal(err)
	}

	ws.emitOrderBook(7, `{"channel":"order_book:7","type":"update/order_book","timestamp":1700000000100,"order_book":{"nonce":12,"begin_nonce":10,"bids":[{"price":"2000.5","size":"1"}],"asks":[]}}`)
	delta := <-eventCh
	if delta.Kind != exchange.EventDelta || delta.Previous != "10" || delta.Sequence != "12" {
		t.Fatalf("continuous delta = %+v", delta)
	}

	ws.emitOrderBook(7, `{"channel":"order_book:7","type":"update/order_book","timestamp":1700000000200,"order_book":{"nonce":15,"begin_nonce":13,"bids":[],"asks":[]}}`)
	started := <-statusCh
	resyncing := <-statusCh
	if started.State != exchange.SubscriptionGap || started.Phase != exchange.GapStarted || started.Generation != 1 {
		t.Fatalf("gap status = %+v", started)
	}
	if resyncing.State != exchange.SubscriptionResyncing || resyncing.Generation != 1 {
		t.Fatalf("resyncing status = %+v", resyncing)
	}
	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.unsubscribeOrderBookCalls == 1 && ws.orderBookHandlers[7] != nil
	})

	ws.emitOrderBook(7, `{"channel":"order_book:7","type":"subscribed/order_book","timestamp":1700000000300,"order_book":{"nonce":20,"begin_nonce":20,"bids":[{"price":"1999","size":"4"}],"asks":[{"price":"2002","size":"5"}]}}`)
	replacement := <-eventCh
	if replacement.Kind != exchange.EventSnapshot || replacement.Sequence != "20" || !replacement.Resync {
		t.Fatalf("replacement event = %+v", replacement)
	}
	recovered := <-statusCh
	if recovered.State != exchange.SubscriptionActive || recovered.Phase != exchange.GapRecovered || recovered.Generation != 1 {
		t.Fatalf("recovered status = %+v", recovered)
	}
	select {
	case err := <-errorCh:
		t.Fatalf("unexpected resync error: %v", err)
	default:
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestLighterBookEventAcceptsInitialSnapshotBeginNonceZero(t *testing.T) {
	meta := lighterMarketMeta{
		instrument: exchange.Instrument{Symbol: "ETH/USDC", Product: exchange.ProductSpot},
		marketID:   2048,
		marketType: lighterSpot,
	}
	event, err := lighterBookEvent(
		[]byte(`{"channel":"order_book:2048","last_updated_at":1784235030416596,"offset":2,"order_book":{"asks":[{"price":"1500.00","size":"0.2601"}],"begin_nonce":0,"bids":[{"price":"750.01","size":"0.1334"}],"code":0,"last_updated_at":1784235030416596,"nonce":18349502,"offset":2},"timestamp":1784381628219,"type":"subscribed/order_book"}`),
		meta,
	)
	if err != nil {
		t.Fatalf("initial Testnet order-book snapshot: %v", err)
	}
	if event.Sequence != "18349502" || event.Previous != "0" ||
		len(event.Bids) != 1 || len(event.Asks) != 1 {
		t.Fatalf("initial Testnet order-book snapshot = %+v", event)
	}
}

func TestLighterPublicTradesAndPerpReferenceMapping(t *testing.T) {
	ws := newFakeLighterPublicWS()
	backend := newTestLighterWSBackend(exchange.ProductPerp, lighterPerp, ws)
	tradeCh := make(chan exchange.PublicTradeEvent, 2)
	referenceCh := make(chan perpReferenceEvent, 1)
	tradeStart := make(chan error, 1)
	referenceStart := make(chan error, 1)

	go func() {
		_, err := backend.StartPublicTrades(context.Background(), "ETH-USDC", streamCallbacks[exchange.PublicTradeEvent]{
			Event: func(event exchange.PublicTradeEvent) { tradeCh <- event },
		})
		tradeStart <- err
	}()
	go func() {
		_, err := backend.StartReference(context.Background(), "ETH-USDC", streamCallbacks[perpReferenceEvent]{
			Event: func(event perpReferenceEvent) { referenceCh <- event },
		})
		referenceStart <- err
	}()
	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.tradeHandlers[7] != nil && ws.statsHandlers[7] != nil
	})

	ws.emitTrades(7, `{"channel":"trade:7","type":"update/trade","nonce":9,"trades":[{"trade_id":101,"trade_id_str":"101","market_id":7,"price":"2000.25","size":"0.4","is_maker_ask":true,"timestamp":1700000000000}],"liquidation_trades":[{"trade_id":102,"trade_id_str":"102","market_id":7,"price":"1999.5","size":"0.2","is_maker_ask":false,"timestamp":1700000000100}]}`)
	firstTrade := <-tradeCh
	secondTrade := <-tradeCh
	if firstTrade.TradeID != "101" || firstTrade.Side != exchange.SideBuy ||
		secondTrade.TradeID != "102" || secondTrade.Side != exchange.SideSell {
		t.Fatalf("trades = %+v %+v", firstTrade, secondTrade)
	}
	if err := <-tradeStart; err != nil {
		t.Fatal(err)
	}

	ws.emitStats(7, `{"channel":"market_stats:7","type":"subscribed/market_stats","timestamp":1700000000200,"market_stats":{"market_id":7,"mark_price":"2000.75","index_price":"2000.5","current_funding_rate":"-0.00012","funding_rate":"-0.0001","funding_timestamp":1700003600000}}`)
	reference := <-referenceCh
	if reference.MarkPrice.Instrument != "ETH-USDC" ||
		!reference.MarkPrice.Price.Equal(decimal.RequireFromString("2000.75")) ||
		!reference.FundingRate.Rate.Equal(decimal.RequireFromString("-0.00012")) ||
		!reference.FundingRate.EffectiveAt.Equal(time.UnixMilli(1_700_000_000_200).UTC()) ||
		!reference.FundingRate.NextAt.IsZero() {
		t.Fatalf("reference = %+v", reference)
	}
	if err := <-referenceStart; err != nil {
		t.Fatal(err)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.connectCalls != 1 {
		t.Fatalf("lazy connection calls = %d, want 1", ws.connectCalls)
	}
}

func TestLighterPublicTradesSignalsReadyBeforeDeliveringInitialBatch(t *testing.T) {
	ws := newFakeLighterPublicWS()
	backend := newTestLighterWSBackend(exchange.ProductPerp, lighterPerp, ws)
	resultCh := make(chan error, 1)
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	var callbackOnce sync.Once

	go func() {
		_, err := backend.StartPublicTrades(context.Background(), "ETH-USDC", streamCallbacks[exchange.PublicTradeEvent]{
			Event: func(exchange.PublicTradeEvent) {
				callbackOnce.Do(func() { close(callbackEntered) })
				<-releaseCallback
			},
		})
		resultCh <- err
	}()
	waitForLighterSubscription(t, func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		return ws.tradeHandlers[7] != nil
	})
	emitted := make(chan struct{})
	go func() {
		ws.emitTrades(7, `{"channel":"trade:7","type":"subscribed/trade","nonce":9,"trades":[{"trade_id":101,"market_id":7,"price":"2000.25","size":"0.4","timestamp":1700000000000}]}`)
		close(emitted)
	}()
	<-callbackEntered

	var startErr error
	returnedBeforeDelivery := false
	select {
	case startErr = <-resultCh:
		returnedBeforeDelivery = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCallback)
	<-emitted
	if !returnedBeforeDelivery {
		startErr = <-resultCh
	}
	if startErr != nil {
		t.Fatalf("StartPublicTrades: %v", startErr)
	}
	if !returnedBeforeDelivery {
		t.Fatal("StartPublicTrades waited for a blocked initial event callback before signaling ready")
	}
}

func TestLighterPublicTradesRejectMixedMarketZeroForNonzeroInstrument(t *testing.T) {
	meta := lighterMarketMeta{
		instrument: exchange.Instrument{Symbol: "ETH-USDC", Product: exchange.ProductSpot},
		marketID:   7,
		marketType: lighterSpot,
	}
	_, err := lighterPublicTradeEvents(
		[]byte(`{"channel":"trade:7","type":"update/trade","nonce":9,"trades":[{"trade_id":101,"market_id":0,"price":"2000","size":"1","timestamp":1700000000000}]}`),
		meta,
	)
	if err == nil {
		t.Fatal("mixed market_id=0 trade was accepted for market 7")
	}
}

func waitForLighterSubscription(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(fmt.Errorf("lighter websocket subscription was not installed"))
}
