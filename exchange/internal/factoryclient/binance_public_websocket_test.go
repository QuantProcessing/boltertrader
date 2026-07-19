package factoryclient

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

func TestBinancePublicWSBackendIsLazyConnectAndUsesSpotDepth5(t *testing.T) {
	ws := newFakeBinancePublicWSClient()
	backend := newBinanceSpotWSBackendWithClient(ws)
	if ws.connectCalls != 0 {
		t.Fatalf("constructor connected websocket")
	}
	var got exchange.BookEvent
	stop, err := backend.StartOrderBook(context.Background(), "ETH-USDT", streamCallbacks[exchange.BookEvent]{
		Event: func(event exchange.BookEvent) { got = event },
	})
	if err != nil {
		t.Fatalf("StartOrderBook: %v", err)
	}
	if ws.connectCalls != 1 {
		t.Fatalf("Connect calls = %d, want 1", ws.connectCalls)
	}
	if ws.bookSymbol != "ETHUSDT" || ws.bookDepth != 5 || ws.bookInterval != "100ms" {
		t.Fatalf("subscribe book args = (%s, %d, %s)", ws.bookSymbol, ws.bookDepth, ws.bookInterval)
	}
	ws.emitDepth(&binancespot.DepthEvent{
		Symbol:        "ETHUSDT",
		EventType:     "depthUpdate",
		FirstUpdateID: 3,
		FinalUpdateID: 7,
		Bids:          [][]string{{"100.50", "2.5"}},
		Asks:          [][]string{{"100.80", "1.2"}},
	})
	if got.Kind != exchange.EventSnapshot || got.Instrument != "ETH-USDT" || got.Resync ||
		!got.Bids[0].Price.Equal(decimal.RequireFromString("100.50")) ||
		!got.Asks[0].Quantity.Equal(decimal.RequireFromString("1.2")) {
		t.Fatalf("book event = %+v", got)
	}
	if got.Time.IsZero() {
		t.Fatalf("book time = %v", got.Time)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if ws.unsubBookCalls != 1 {
		t.Fatalf("unsubscribe calls = %d, want 1", ws.unsubBookCalls)
	}
}

func TestBinancePublicWSBackendNormalizesBBOAndTrades(t *testing.T) {
	ws := newFakeBinancePublicWSClient()
	backend := newBinanceSpotWSBackendWithClient(ws)
	var bbo exchange.BBOEvent
	if _, err := backend.StartBBO(context.Background(), "ETH-USDT", streamCallbacks[exchange.BBOEvent]{
		Event: func(event exchange.BBOEvent) { bbo = event },
	}); err != nil {
		t.Fatalf("StartBBO: %v", err)
	}
	if ws.bboSymbol != "ETHUSDT" {
		t.Fatalf("book ticker symbol = %q, want ETHUSDT", ws.bboSymbol)
	}
	ws.emitBBO(&binancespot.BookTickerEvent{
		Symbol:       "ETHUSDT",
		BestBidPrice: "101.1",
		BestBidQty:   "5",
		BestAskPrice: "101.2",
		BestAskQty:   "4",
	})
	if bbo.Instrument != "ETH-USDT" || !bbo.Bid.Price.Equal(decimal.RequireFromString("101.1")) ||
		!bbo.Ask.Quantity.Equal(decimal.RequireFromString("4")) || bbo.Time.IsZero() {
		t.Fatalf("bbo event = %+v", bbo)
	}

	var trade exchange.PublicTradeEvent
	if _, err := backend.StartPublicTrades(context.Background(), "ETH-USDT", streamCallbacks[exchange.PublicTradeEvent]{
		Event: func(event exchange.PublicTradeEvent) { trade = event },
	}); err != nil {
		t.Fatalf("StartPublicTrades: %v", err)
	}
	if ws.tradeSymbol != "ETHUSDT" {
		t.Fatalf("agg trade symbol = %q, want ETHUSDT", ws.tradeSymbol)
	}
	ws.emitTrade(&binancespot.AggTradeEvent{
		Symbol:       "ETHUSDT",
		AggTradeID:   777,
		Price:        "102.2",
		Quantity:     "0.75",
		TradeTime:    1700000000002,
		IsBuyerMaker: true,
	})
	if trade.Instrument != "ETH-USDT" || trade.TradeID != "777" || trade.Side != exchange.SideSell ||
		!trade.Price.Equal(decimal.RequireFromString("102.2")) || trade.Time.UnixMilli() != 1700000000002 {
		t.Fatalf("trade event = %+v", trade)
	}
}

func TestBinancePublicWSBackendReconnectStatusMarksNextBookResync(t *testing.T) {
	ws := newFakeBinancePublicWSClient()
	backend := newBinanceSpotWSBackendWithClient(ws)
	var statuses []backendStatus
	var book exchange.BookEvent
	if _, err := backend.StartOrderBook(context.Background(), "ETH-USDT", streamCallbacks[exchange.BookEvent]{
		Event:  func(event exchange.BookEvent) { book = event },
		Status: func(status backendStatus) { statuses = append(statuses, status) },
	}); err != nil {
		t.Fatalf("StartOrderBook: %v", err)
	}
	ws.emitDepth(&binancespot.DepthEvent{
		Symbol:        "ETHUSDT",
		FirstUpdateID: 10,
		FinalUpdateID: 11,
		Bids:          [][]string{{"100", "1"}},
		Asks:          [][]string{{"101", "1"}},
		EventTime:     1700000000010,
	})
	ws.emitPostReconnect()
	if len(statuses) != 3 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].State != exchange.SubscriptionGap || statuses[0].Phase != exchange.GapStarted ||
		statuses[1].State != exchange.SubscriptionResyncing || statuses[2].State != exchange.SubscriptionActive {
		t.Fatalf("unexpected statuses = %+v", statuses)
	}

	next := time.After(time.Second)
	select {
	case <-next:
		// fallthrough
	default:
	}

	ws.emitDepth(&binancespot.DepthEvent{
		Symbol:        "ETHUSDT",
		FirstUpdateID: 12,
		FinalUpdateID: 13,
		Bids:          [][]string{{"99", "1"}},
		Asks:          [][]string{{"100", "1"}},
		EventTime:     1700000000011,
	})
	if !book.Resync {
		t.Fatalf("book after reconnect = %+v, want Resync", book)
	}
}

func TestBinancePublicWSBackendCandlesAndMalformedErrors(t *testing.T) {
	ws := newFakeBinancePublicWSClient()
	backend := newBinanceSpotWSBackendWithClient(ws)
	var got exchange.CandleEvent
	var gotErr error
	stop, err := backend.StartCandles(context.Background(), "ETH-USDT", "5m", streamCallbacks[exchange.CandleEvent]{
		Event: func(event exchange.CandleEvent) { got = event },
		Error: func(err error) { gotErr = err },
	})
	if err != nil {
		t.Fatalf("StartCandles: %v", err)
	}
	if ws.candleSymbol != "ETHUSDT" || ws.candleInterval != "5m" {
		t.Fatalf("kline args = (%s, %s), want (ETHUSDT, 5m)", ws.candleSymbol, ws.candleInterval)
	}
	row := &binancespot.KlineEvent{
		EventType: "kline",
		EventTime: 1700000300000,
		Symbol:    "ETHUSDT",
	}
	row.Kline.StartTime = 1700000000000
	row.Kline.CloseTime = 1700000299999
	row.Kline.Symbol = "ETHUSDT"
	row.Kline.Interval = "5m"
	row.Kline.OpenPrice = "100"
	row.Kline.HighPrice = "105"
	row.Kline.LowPrice = "99"
	row.Kline.ClosePrice = "102"
	row.Kline.Volume = "12.5"
	row.Kline.IsClosed = true
	ws.emitCandle(row)
	if got.Instrument != "ETH-USDT" || got.Interval != "5m" ||
		got.Candle.OpenTime.UnixMilli() != 1700000000000 ||
		got.Candle.CloseTime.UnixMilli() != 1700000300000 ||
		!got.Candle.High.Equal(decimal.RequireFromString("105")) ||
		!got.Candle.Volume.Equal(decimal.RequireFromString("12.5")) ||
		!got.Candle.Complete {
		t.Fatalf("candle event = %+v", got)
	}

	row.Symbol = "BTCUSDT"
	ws.emitCandle(row)
	if gotErr == nil || !errors.Is(gotErr, exchange.ErrMalformedResponse) {
		t.Fatalf("malformed candle error = %v, want ErrMalformedResponse", gotErr)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if ws.unsubCandleSymbol != "ETHUSDT" || ws.unsubCandleInterval != "5m" {
		t.Fatalf("unsubscribe kline args = (%s, %s)", ws.unsubCandleSymbol, ws.unsubCandleInterval)
	}
}

func TestBinancePublicWSConvertersRejectZeroIdentityAndEmptyTopSize(t *testing.T) {
	first := true
	if _, err := binanceSpotWSBookEvent("ETH-USDT", &binancespot.DepthEvent{
		Symbol: "ETHUSDT",
		Bids:   [][]string{{"100", "1"}},
		Asks:   [][]string{{"101", "1"}},
	}, &first, nil); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot zero-sequence book error = %v", err)
	}
	if _, err := binanceSpotWSPublicTrade("ETH-USDT", &binancespot.AggTradeEvent{
		Symbol:   "ETHUSDT",
		Price:    "100",
		Quantity: "1",
	}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot zero-identity trade error = %v", err)
	}
	if _, err := binancePerpWSPublicTrade("ETH-USDT", &binanceperp.WsAggTradeEvent{
		Symbol:   "ETHUSDT",
		Price:    "100",
		Quantity: "1",
	}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("perp zero-identity trade error = %v", err)
	}
	if _, err := binanceSpotWSBBOEvent("ETH-USDT", &binancespot.BookTickerEvent{
		Symbol:       "ETHUSDT",
		BestBidPrice: "100",
		BestBidQty:   "0",
		BestAskPrice: "101",
		BestAskQty:   "1",
	}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot zero-size BBO error = %v", err)
	}
}

func TestBinancePerpPublicWSBackendNormalizesStreamsAndReference(t *testing.T) {
	ws := newFakeBinancePerpPublicWSClient()
	backend := newBinancePerpWSBackendWithClient(ws)
	var book exchange.BookEvent
	if _, err := backend.StartOrderBook(context.Background(), "ETH-USDT", streamCallbacks[exchange.BookEvent]{
		Event: func(event exchange.BookEvent) { book = event },
	}); err != nil {
		t.Fatalf("StartOrderBook: %v", err)
	}
	if ws.bookSymbol != "ETHUSDT" || ws.bookDepth != 5 || ws.bookInterval != "100ms" {
		t.Fatalf("perp book args = (%s, %d, %s)", ws.bookSymbol, ws.bookDepth, ws.bookInterval)
	}
	ws.emitDepth(&binanceperp.WsDepthEvent{
		Symbol:        "ETHUSDT",
		FinalUpdateID: 9,
		Bids:          [][]interface{}{{"100.1", "2"}},
		Asks:          [][]interface{}{{"100.2", "3"}},
	})
	if book.Kind != exchange.EventSnapshot || book.Sequence != "9" || book.Time.IsZero() ||
		!book.Bids[0].Quantity.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("perp book event = %+v", book)
	}

	var bbo exchange.BBOEvent
	if _, err := backend.StartBBO(context.Background(), "ETH-USDT", streamCallbacks[exchange.BBOEvent]{
		Event: func(event exchange.BBOEvent) { bbo = event },
	}); err != nil {
		t.Fatalf("StartBBO: %v", err)
	}
	ws.emitBBO(&binanceperp.WsBookTickerEvent{
		Symbol:       "ETHUSDT",
		BestBidPrice: "101",
		BestBidQty:   "4",
		BestAskPrice: "102",
		BestAskQty:   "5",
	})
	if bbo.Instrument != "ETH-USDT" || bbo.Time.IsZero() || !bbo.Ask.Price.Equal(decimal.RequireFromString("102")) {
		t.Fatalf("perp bbo event = %+v", bbo)
	}

	var trade exchange.PublicTradeEvent
	if _, err := backend.StartPublicTrades(context.Background(), "ETH-USDT", streamCallbacks[exchange.PublicTradeEvent]{
		Event: func(event exchange.PublicTradeEvent) { trade = event },
	}); err != nil {
		t.Fatalf("StartPublicTrades: %v", err)
	}
	ws.emitTrade(&binanceperp.WsAggTradeEvent{
		Symbol:       "ETHUSDT",
		AggTradeID:   88,
		Price:        "103",
		Quantity:     "0.5",
		TradeTime:    1700000000120,
		IsBuyerMaker: false,
	})
	if trade.Side != exchange.SideBuy || trade.TradeID != "88" || !trade.Quantity.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("perp trade event = %+v", trade)
	}

	var candle exchange.CandleEvent
	if _, err := backend.StartCandles(context.Background(), "ETH-USDT", "1h", streamCallbacks[exchange.CandleEvent]{
		Event: func(event exchange.CandleEvent) { candle = event },
	}); err != nil {
		t.Fatalf("StartCandles: %v", err)
	}
	row := &binanceperp.WsKlineEvent{EventType: "kline", EventTime: 1700003600000, Symbol: "ETHUSDT"}
	row.Kline.StartTime = 1700000000000
	row.Kline.EndTime = 1700003599999
	row.Kline.Symbol = "ETHUSDT"
	row.Kline.Interval = "1h"
	row.Kline.OpenPrice = "100"
	row.Kline.HighPrice = "110"
	row.Kline.LowPrice = "90"
	row.Kline.ClosePrice = "105"
	row.Kline.Volume = "1000"
	row.Kline.IsClosed = true
	ws.emitCandle(row)
	if candle.Interval != "1h" || candle.Candle.CloseTime.UnixMilli() != 1700003600000 || !candle.Candle.Complete {
		t.Fatalf("perp candle event = %+v", candle)
	}

	var reference perpReferenceEvent
	stopReference, err := backend.StartReference(context.Background(), "ETH-USDT", streamCallbacks[perpReferenceEvent]{
		Event: func(event perpReferenceEvent) { reference = event },
	})
	if err != nil {
		t.Fatalf("StartReference: %v", err)
	}
	if ws.markSymbol != "ETHUSDT" || ws.markInterval != "1s" {
		t.Fatalf("mark args = (%s, %s), want (ETHUSDT, 1s)", ws.markSymbol, ws.markInterval)
	}
	ws.emitMark(&binanceperp.WsMarkPriceEvent{
		Symbol:          "ETHUSDT",
		EventTime:       1700000000200,
		MarkPrice:       "104.25",
		FundingRate:     "-0.0001",
		NextFundingTime: 1700028800000,
	})
	if reference.MarkPrice.Instrument != "ETH-USDT" ||
		!reference.MarkPrice.Price.Equal(decimal.RequireFromString("104.25")) ||
		!reference.FundingRate.Rate.Equal(decimal.RequireFromString("-0.0001")) ||
		reference.FundingRate.EffectiveAt.UnixMilli() != 1700000000200 ||
		reference.FundingRate.NextAt.UnixMilli() != 1700028800000 {
		t.Fatalf("reference event = %+v", reference)
	}
	if err := stopReference(); err != nil {
		t.Fatalf("stop reference: %v", err)
	}
	if ws.unsubMarkSymbol != "ETHUSDT" || ws.unsubMarkInterval != "1s" {
		t.Fatalf("unsubscribe mark args = (%s, %s)", ws.unsubMarkSymbol, ws.unsubMarkInterval)
	}
}

func TestBinancePerpDemoOrderBookUsesDiffDepthPayload(t *testing.T) {
	ws := newFakeBinancePerpPublicWSClient()
	backend := newBinancePerpDemoWSBackendWithClient(ws)
	var got exchange.BookEvent
	stop, err := backend.StartOrderBook(context.Background(), "ETH-USDT", streamCallbacks[exchange.BookEvent]{
		Event: func(event exchange.BookEvent) { got = event },
	})
	if err != nil {
		t.Fatalf("StartOrderBook: %v", err)
	}
	if ws.incrementBookSymbol != "ETHUSDT" || ws.incrementBookInterval != "100ms" {
		t.Fatalf(
			"diff depth args = (%s, %s), want (ETHUSDT, 100ms)",
			ws.incrementBookSymbol,
			ws.incrementBookInterval,
		)
	}
	if ws.bookSymbol != "" {
		t.Fatalf("partial depth subscription = %q, want none", ws.bookSymbol)
	}
	ws.emitIncrementDepth(&binanceperp.WsDepthEvent{
		EventType:         "depthUpdate",
		EventTime:         1700000000000,
		TransactionTime:   1700000000001,
		Symbol:            "ETHUSDT",
		FirstUpdateID:     10,
		FinalUpdateID:     12,
		FinalUpdateIDLast: 9,
		Bids:              [][]interface{}{{"100.1", "0"}},
	})
	if got.Kind != exchange.EventDelta ||
		got.Instrument != "ETH-USDT" ||
		got.Sequence != "12" ||
		got.Previous != "9" ||
		len(got.Bids) != 1 ||
		!got.Bids[0].Quantity.IsZero() ||
		len(got.Asks) != 0 {
		t.Fatalf("diff depth event = %+v", got)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if ws.unsubIncrementBookSymbol != "ETHUSDT" || ws.unsubIncrementBookInterval != "100ms" {
		t.Fatalf(
			"diff depth unsubscribe args = (%s, %s)",
			ws.unsubIncrementBookSymbol,
			ws.unsubIncrementBookInterval,
		)
	}
}

func TestNewBinanceClientsWirePublicWebSockets(t *testing.T) {
	spot := NewBinanceSpot("", "", Settings{WebSocketEndpoint: "ws://127.0.0.1:1/ws"})
	if spot.WebSocket() == nil {
		t.Fatalf("spot websocket is nil")
	}
	perp := NewBinanceUSDPerp("", "", Settings{WebSocketEndpoint: "ws://127.0.0.1:1/ws"})
	if perp.WebSocket() == nil {
		t.Fatalf("perp websocket is nil")
	}
}

func TestNewBinanceUSDPerpDemoSelectsDiffDepthBackend(t *testing.T) {
	client := NewBinanceUSDPerp("", "", Settings{
		Environment:       "demo",
		WebSocketEndpoint: "ws://127.0.0.1:1/ws",
	}).(*binancePerpClient)
	socket := client.ws.(*perpWebSocket)
	backend := socket.backend.(*binancePerpPublicWSBackend)
	if !backend.diffDepth {
		t.Fatal("Binance Demo perp backend must use diff-depth payloads")
	}
}

type blockingBinanceConnectClient struct {
	*fakeBinancePublicWSClient
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (client *blockingBinanceConnectClient) Connect() error {
	client.mu.Lock()
	client.connectCalls++
	first := client.connectCalls == 1
	client.mu.Unlock()
	if first {
		close(client.entered)
	}
	<-client.release
	return nil
}

func TestBinancePublicWSBackendConcurrentTopicsSharePendingConnect(t *testing.T) {
	ws := &blockingBinanceConnectClient{
		fakeBinancePublicWSClient: newFakeBinancePublicWSClient(),
		entered:                   make(chan struct{}),
		release:                   make(chan struct{}),
	}
	backend := newBinanceSpotWSBackendWithClient(ws)
	results := make(chan error, 2)
	go func() {
		_, err := backend.StartBBO(context.Background(), "ETH-USDT", streamCallbacks[exchange.BBOEvent]{})
		results <- err
	}()
	<-ws.entered
	go func() {
		_, err := backend.StartPublicTrades(context.Background(), "ETH-USDT", streamCallbacks[exchange.PublicTradeEvent]{})
		results <- err
	}()
	select {
	case err := <-results:
		t.Fatalf("concurrent topic returned before shared connect completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(ws.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent topic start: %v", err)
		}
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.connectCalls != 1 {
		t.Fatalf("Connect calls = %d, want 1", ws.connectCalls)
	}
}

type fakeBinancePublicWSClient struct {
	connectCalls int
	closeCalls   int

	postReconnect func()

	bookSymbol, bboSymbol, tradeSymbol string
	bookDepth                          int
	bookInterval                       string
	bookHandler                        func(*binancespot.DepthEvent) error
	bboHandler                         func(*binancespot.BookTickerEvent) error
	tradeHandler                       func(*binancespot.AggTradeEvent) error
	candleSymbol, candleInterval       string
	candleHandler                      func(*binancespot.KlineEvent) error
	unsubBookCalls                     int
	unsubBBOCalls                      int
	unsubTradeCalls                    int
	unsubCandleSymbol                  string
	unsubCandleInterval                string
}

func newFakeBinancePublicWSClient() *fakeBinancePublicWSClient {
	return &fakeBinancePublicWSClient{}
}

func (ws *fakeBinancePublicWSClient) Connect() error {
	ws.connectCalls++
	return nil
}

func (ws *fakeBinancePublicWSClient) Close() {
	ws.closeCalls++
}

func (ws *fakeBinancePublicWSClient) SetPostReconnect(handler func()) {
	ws.postReconnect = handler
}

func (ws *fakeBinancePublicWSClient) SubscribeLimitOrderBook(symbol string, depth int, interval string, handler func(*binancespot.DepthEvent) error) error {
	ws.bookSymbol, ws.bookDepth, ws.bookInterval = symbol, depth, interval
	ws.bookHandler = handler
	return nil
}

func (ws *fakeBinancePublicWSClient) SubscribeBookTicker(symbol string, handler func(*binancespot.BookTickerEvent) error) error {
	ws.bboSymbol = symbol
	ws.bboHandler = handler
	return nil
}

func (ws *fakeBinancePublicWSClient) SubscribeAggTrade(symbol string, handler func(*binancespot.AggTradeEvent) error) error {
	ws.tradeSymbol = symbol
	ws.tradeHandler = handler
	return nil
}

func (ws *fakeBinancePublicWSClient) SubscribeKline(symbol string, interval string, handler func(*binancespot.KlineEvent) error) error {
	ws.candleSymbol, ws.candleInterval = symbol, interval
	ws.candleHandler = handler
	return nil
}

func (ws *fakeBinancePublicWSClient) UnsubscribeLimitOrderBook(_ string, _ int, _ string) error {
	ws.unsubBookCalls++
	return nil
}

func (ws *fakeBinancePublicWSClient) UnsubscribeBookTicker(_ string) error {
	ws.unsubBBOCalls++
	return nil
}

func (ws *fakeBinancePublicWSClient) UnsubscribeAggTrade(_ string) error {
	ws.unsubTradeCalls++
	return nil
}

func (ws *fakeBinancePublicWSClient) UnsubscribeKline(symbol string, interval string) error {
	ws.unsubCandleSymbol, ws.unsubCandleInterval = symbol, interval
	return nil
}

func (ws *fakeBinancePublicWSClient) emitPostReconnect() {
	if ws.postReconnect != nil {
		ws.postReconnect()
	}
}

func (ws *fakeBinancePublicWSClient) emitDepth(row *binancespot.DepthEvent) {
	if ws.bookHandler != nil {
		_ = ws.bookHandler(row)
	}
}

func (ws *fakeBinancePublicWSClient) emitBBO(ticker *binancespot.BookTickerEvent) {
	if ws.bboHandler != nil {
		_ = ws.bboHandler(ticker)
	}
}

func (ws *fakeBinancePublicWSClient) emitTrade(trade *binancespot.AggTradeEvent) {
	if ws.tradeHandler != nil {
		_ = ws.tradeHandler(trade)
	}
}

func (ws *fakeBinancePublicWSClient) emitCandle(candle *binancespot.KlineEvent) {
	if ws.candleHandler != nil {
		_ = ws.candleHandler(candle)
	}
}

type fakeBinancePerpPublicWSClient struct {
	connectCalls int
	closeCalls   int

	postReconnect func()

	bookSymbol, bboSymbol, tradeSymbol string
	bookDepth                          int
	bookInterval                       string
	bookHandler                        func(*binanceperp.WsDepthEvent) error
	incrementBookSymbol                string
	incrementBookInterval              string
	incrementBookHandler               func(*binanceperp.WsDepthEvent) error
	unsubIncrementBookSymbol           string
	unsubIncrementBookInterval         string
	bboHandler                         func(*binanceperp.WsBookTickerEvent) error
	tradeHandler                       func(*binanceperp.WsAggTradeEvent) error
	candleSymbol, candleInterval       string
	candleHandler                      func(*binanceperp.WsKlineEvent) error
	markSymbol, markInterval           string
	markHandler                        func(*binanceperp.WsMarkPriceEvent) error
	unsubMarkSymbol                    string
	unsubMarkInterval                  string
}

func newFakeBinancePerpPublicWSClient() *fakeBinancePerpPublicWSClient {
	return &fakeBinancePerpPublicWSClient{}
}

func (ws *fakeBinancePerpPublicWSClient) Connect() error {
	ws.connectCalls++
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) Close() {
	ws.closeCalls++
}

func (ws *fakeBinancePerpPublicWSClient) SetPostReconnect(handler func()) {
	ws.postReconnect = handler
}

func (ws *fakeBinancePerpPublicWSClient) SubscribeLimitOrderBook(symbol string, depth int, interval string, handler func(*binanceperp.WsDepthEvent) error) error {
	ws.bookSymbol, ws.bookDepth, ws.bookInterval = symbol, depth, interval
	ws.bookHandler = handler
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) SubscribeIncrementOrderBook(symbol string, interval string, handler func(*binanceperp.WsDepthEvent) error) error {
	ws.incrementBookSymbol, ws.incrementBookInterval = symbol, interval
	ws.incrementBookHandler = handler
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) SubscribeBookTicker(symbol string, handler func(*binanceperp.WsBookTickerEvent) error) error {
	ws.bboSymbol = symbol
	ws.bboHandler = handler
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) SubscribeAggTrade(symbol string, handler func(*binanceperp.WsAggTradeEvent) error) error {
	ws.tradeSymbol = symbol
	ws.tradeHandler = handler
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) SubscribeKline(symbol string, interval string, handler func(*binanceperp.WsKlineEvent) error) error {
	ws.candleSymbol, ws.candleInterval = symbol, interval
	ws.candleHandler = handler
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) SubscribeMarkPrice(symbol string, interval string, handler func(*binanceperp.WsMarkPriceEvent) error) error {
	ws.markSymbol, ws.markInterval = symbol, interval
	ws.markHandler = handler
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) UnsubscribeLimitOrderBook(string, int, string) error {
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) UnsubscribeIncrementOrderBook(symbol string, interval string) error {
	ws.unsubIncrementBookSymbol, ws.unsubIncrementBookInterval = symbol, interval
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) UnsubscribeBookTicker(string) error {
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) UnsubscribeAggTrade(string) error {
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) UnsubscribeKline(string, string) error {
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) UnsubscribeMarkPrice(symbol string, interval string) error {
	ws.unsubMarkSymbol, ws.unsubMarkInterval = symbol, interval
	return nil
}

func (ws *fakeBinancePerpPublicWSClient) emitDepth(row *binanceperp.WsDepthEvent) {
	if ws.bookHandler != nil {
		_ = ws.bookHandler(row)
	}
}

func (ws *fakeBinancePerpPublicWSClient) emitIncrementDepth(row *binanceperp.WsDepthEvent) {
	if ws.incrementBookHandler != nil {
		_ = ws.incrementBookHandler(row)
	}
}

func (ws *fakeBinancePerpPublicWSClient) emitBBO(row *binanceperp.WsBookTickerEvent) {
	if ws.bboHandler != nil {
		_ = ws.bboHandler(row)
	}
}

func (ws *fakeBinancePerpPublicWSClient) emitTrade(row *binanceperp.WsAggTradeEvent) {
	if ws.tradeHandler != nil {
		_ = ws.tradeHandler(row)
	}
}

func (ws *fakeBinancePerpPublicWSClient) emitCandle(row *binanceperp.WsKlineEvent) {
	if ws.candleHandler != nil {
		_ = ws.candleHandler(row)
	}
}

func (ws *fakeBinancePerpPublicWSClient) emitMark(row *binanceperp.WsMarkPriceEvent) {
	if ws.markHandler != nil {
		_ = ws.markHandler(row)
	}
}
