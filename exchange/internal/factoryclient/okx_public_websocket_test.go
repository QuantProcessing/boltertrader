package factoryclient

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func TestOKXPublicWSBackendIsLazyConnectsOnceAndUsesBooks5(t *testing.T) {
	ws := newFakeOKXPublicWSClient()
	backend := newOKXSpotWSBackendWithClient(ws)
	if ws.connectCalls != 0 {
		t.Fatal("constructor connected websocket")
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
	if len(ws.bookDepths) != 1 || ws.bookDepths[0] != 5 {
		t.Fatalf("book depths = %v, want native books5 depth", ws.bookDepths)
	}
	ws.emitBook(&okx.OrderBook{
		Bids: [][]string{{"100.1", "2", "0", "1"}},
		Asks: [][]string{{"101.2", "3", "0", "1"}},
		Ts:   "1700000000000",
	}, "snapshot")
	if got.Kind != exchange.EventSnapshot ||
		got.Instrument != "ETH-USDT" ||
		!got.Bids[0].Quantity.Equal(decimal.NewFromInt(2)) ||
		!got.Asks[0].Price.Equal(decimal.RequireFromString("101.2")) {
		t.Fatalf("book event = %+v", got)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(ws.unsubscribed) != 1 || ws.unsubscribed[0].Channel != "books5" {
		t.Fatalf("unsubscribed = %+v, want books5", ws.unsubscribed)
	}
}

func TestOKXPublicWSBackendNormalizesBBOAndTrades(t *testing.T) {
	ws := newFakeOKXPublicWSClient()
	backend := newOKXSpotWSBackendWithClient(ws)
	var bbo exchange.BBOEvent
	if _, err := backend.StartBBO(context.Background(), "ETH-USDT", streamCallbacks[exchange.BBOEvent]{
		Event: func(event exchange.BBOEvent) { bbo = event },
	}); err != nil {
		t.Fatalf("StartBBO: %v", err)
	}
	ws.emitTicker(&okx.Ticker{
		InstId: "ETH-USDT",
		BidPx:  "100.1",
		BidSz:  "2.3",
		AskPx:  "100.2",
		AskSz:  "4.5",
		Ts:     "1700000000001",
	})
	if bbo.Instrument != "ETH-USDT" ||
		!bbo.Bid.Quantity.Equal(decimal.RequireFromString("2.3")) ||
		!bbo.Ask.Price.Equal(decimal.RequireFromString("100.2")) ||
		bbo.Time.UnixMilli() != 1700000000001 {
		t.Fatalf("bbo event = %+v", bbo)
	}

	var trade exchange.PublicTradeEvent
	if _, err := backend.StartPublicTrades(context.Background(), "ETH-USDT", streamCallbacks[exchange.PublicTradeEvent]{
		Event: func(event exchange.PublicTradeEvent) { trade = event },
	}); err != nil {
		t.Fatalf("StartPublicTrades: %v", err)
	}
	ws.emitTrade(&okx.PublicTrade{
		InstId:  "ETH-USDT",
		TradeId: "trade-1",
		Px:      "100.3",
		Sz:      "0.25",
		Side:    "buy",
		Ts:      "1700000000002",
	})
	if trade.TradeID != "trade-1" ||
		trade.Side != exchange.SideBuy ||
		!trade.Quantity.Equal(decimal.RequireFromString("0.25")) ||
		trade.Time.UnixMilli() != 1700000000002 {
		t.Fatalf("trade event = %+v", trade)
	}
}

func TestOKXPublicWSBackendSurfacesRawDecodeErrorsForEveryPublicStream(t *testing.T) {
	ws := newFakeOKXPublicWSClient()
	backend := newOKXPerpWSBackendWithClient(ws, func(context.Context, string) (okxContractMeta, error) {
		return okxContractMeta{contractValue: decimal.NewFromInt(1)}, nil
	})
	var got []error
	callback := func(err error) { got = append(got, err) }

	if _, err := backend.StartOrderBook(context.Background(), "BTC-USDT-SWAP", streamCallbacks[exchange.BookEvent]{Error: callback}); err != nil {
		t.Fatalf("StartOrderBook: %v", err)
	}
	if _, err := backend.StartBBO(context.Background(), "BTC-USDT-SWAP", streamCallbacks[exchange.BBOEvent]{Error: callback}); err != nil {
		t.Fatalf("StartBBO: %v", err)
	}
	if _, err := backend.StartPublicTrades(context.Background(), "BTC-USDT-SWAP", streamCallbacks[exchange.PublicTradeEvent]{Error: callback}); err != nil {
		t.Fatalf("StartPublicTrades: %v", err)
	}
	if _, err := backend.StartReference(context.Background(), "BTC-USDT-SWAP", streamCallbacks[perpReferenceEvent]{Error: callback}); err != nil {
		t.Fatalf("StartReference: %v", err)
	}

	ws.emitBookDecodeError(errors.New("bad book json"))
	ws.emitTickerDecodeError(errors.New("bad ticker json"))
	ws.emitTradeDecodeError(errors.New("bad trade json"))
	ws.emitMarkDecodeError(errors.New("bad mark json"))
	ws.emitFundingDecodeError(errors.New("bad funding json"))

	if len(got) != 5 {
		t.Fatalf("decode errors = %v, want one from each public stream", got)
	}
	for _, err := range got {
		var exchangeErr *exchange.Error
		if !errors.As(err, &exchangeErr) || exchangeErr.Kind() != exchange.KindMalformedResponse {
			t.Fatalf("decode error = %v, want malformed exchange error", err)
		}
	}
}

func TestOKXPerpWSBackendAppliesContractMultiplierAndReferenceStream(t *testing.T) {
	ws := newFakeOKXPublicWSClient()
	backend := newOKXPerpWSBackendWithClient(ws, func(context.Context, string) (okxContractMeta, error) {
		return okxContractMeta{contractValue: decimal.RequireFromString("0.01")}, nil
	})
	var trade exchange.PublicTradeEvent
	if _, err := backend.StartPublicTrades(context.Background(), "BTC-USDT-SWAP", streamCallbacks[exchange.PublicTradeEvent]{
		Event: func(event exchange.PublicTradeEvent) { trade = event },
	}); err != nil {
		t.Fatalf("StartPublicTrades: %v", err)
	}
	ws.emitTrade(&okx.PublicTrade{
		InstId:  "BTC-USDT-SWAP",
		TradeId: "swap-trade",
		Px:      "50000",
		Sz:      "3",
		Side:    "sell",
		Ts:      "1700000000003",
	})
	if trade.Side != exchange.SideSell || !trade.Quantity.Equal(decimal.RequireFromString("0.03")) {
		t.Fatalf("perp trade = %+v, want contract multiplier applied", trade)
	}

	var references []perpReferenceEvent
	if _, err := backend.StartReference(context.Background(), "BTC-USDT-SWAP", streamCallbacks[perpReferenceEvent]{
		Event: func(event perpReferenceEvent) { references = append(references, event) },
	}); err != nil {
		t.Fatalf("StartReference: %v", err)
	}
	ws.emitMark(&okx.MarkPrice{InstId: "BTC-USDT-SWAP", MarkPx: "50001.5", Ts: "1700000000004"})
	if len(references) != 1 ||
		!references[0].MarkValid ||
		references[0].FundingValid ||
		!references[0].MarkPrice.Price.Equal(decimal.RequireFromString("50001.5")) {
		t.Fatalf("mark reference = %+v, want independent mark event", references)
	}
	ws.emitFunding(&okx.FundingRate{
		InstrumentID: "BTC-USDT-SWAP",
		FundingRate:  "0",
		FundingTime:  "1700003600000",
		Ts:           "1700000000005",
	})
	if len(references) != 2 ||
		references[1].MarkValid ||
		!references[1].FundingValid ||
		!references[1].FundingRate.Rate.IsZero() ||
		references[1].FundingRate.EffectiveAt.UnixMilli() != 1700003600000 {
		t.Fatalf("funding reference = %+v, want independent funding event", references)
	}
}

func TestOKXPublicWSBackendStartCandlesMapsStrictIntervalsAndNormalizes(t *testing.T) {
	cases := map[string]string{
		"1m":  "candle1m",
		"5m":  "candle5m",
		"15m": "candle15m",
		"30m": "candle30m",
		"1h":  "candle1H",
		"4h":  "candle4H",
		"12h": "candle12H",
		"1d":  "candle1D",
	}
	for interval, channel := range cases {
		t.Run(interval, func(t *testing.T) {
			ws := newFakeOKXPublicWSClient()
			backend := newOKXSpotWSBackendWithClient(ws)
			var got exchange.CandleEvent
			stop, err := backend.StartCandles(context.Background(), "ETH-USDT", interval, streamCallbacks[exchange.CandleEvent]{
				Event: func(event exchange.CandleEvent) { got = event },
			})
			if err != nil {
				t.Fatalf("StartCandles: %v", err)
			}
			if len(ws.candleChannels) != 1 || ws.candleChannels[0] != channel {
				t.Fatalf("candle channels = %v, want %s", ws.candleChannels, channel)
			}
			ws.emitCandle(okx.Candle{"1700000000000", "100", "102", "99", "101", "2.5", "250", "252.5", "1"})
			if got.Instrument != "ETH-USDT" ||
				got.Interval != interval ||
				got.Candle.OpenTime.UnixMilli() != 1700000000000 ||
				got.Candle.CloseTime.Sub(got.Candle.OpenTime) <= 0 ||
				!got.Candle.Open.Equal(decimal.NewFromInt(100)) ||
				!got.Candle.Volume.Equal(decimal.RequireFromString("2.5")) ||
				!got.Candle.Complete {
				t.Fatalf("candle event = %+v", got)
			}
			if err := stop(); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if len(ws.unsubscribed) != 1 || ws.unsubscribed[0] != (okx.WsSubscribeArgs{Channel: channel, InstId: "ETH-USDT"}) {
				t.Fatalf("unsubscribed = %+v, want exact candle args", ws.unsubscribed)
			}
		})
	}
}

func TestOKXPublicWSBackendStartsCandlesOnBusinessSocket(t *testing.T) {
	publicWS := newFakeOKXPublicWSClient()
	businessWS := newFakeOKXPublicWSClient()
	backend := newOKXSpotWSBackendWithClients(publicWS, businessWS)
	stop, err := backend.StartCandles(context.Background(), "ETH-USDT", "1m", streamCallbacks[exchange.CandleEvent]{})
	if err != nil {
		t.Fatalf("StartCandles: %v", err)
	}
	if publicWS.connectCalls != 0 || len(publicWS.candleChannels) != 0 {
		t.Fatalf(
			"public socket calls = connect:%d candles:%v, want unused for candles",
			publicWS.connectCalls,
			publicWS.candleChannels,
		)
	}
	if businessWS.connectCalls != 1 ||
		len(businessWS.candleChannels) != 1 ||
		businessWS.candleChannels[0] != "candle1m" {
		t.Fatalf(
			"business socket calls = connect:%d candles:%v, want candle1m",
			businessWS.connectCalls,
			businessWS.candleChannels,
		)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(publicWS.unsubscribed) != 0 {
		t.Fatalf("public socket unsubscribed = %+v, want none", publicWS.unsubscribed)
	}
	if len(businessWS.unsubscribed) != 1 ||
		businessWS.unsubscribed[0] != (okx.WsSubscribeArgs{Channel: "candle1m", InstId: "ETH-USDT"}) {
		t.Fatalf("business socket unsubscribed = %+v", businessWS.unsubscribed)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if publicWS.closeCalls != 1 || businessWS.closeCalls != 1 {
		t.Fatalf("close calls = public:%d business:%d, want one each", publicWS.closeCalls, businessWS.closeCalls)
	}
}

func TestNewOKXDemoClientsWireBusinessSocketForCandles(t *testing.T) {
	spotClient := NewOKXSpot("", "", "", Settings{Environment: "demo"}).(*okxSpotClient)
	spotSocket := spotClient.ws.(*spotWebSocket)
	assertOKXDemoBusinessSocket(t, "spot", spotSocket.publicWebSocket.backend.(*okxPublicWSBackend))

	perpClient := NewOKXUSDTPerp("", "", "", Settings{Environment: "demo"}).(*okxPerpClient)
	perpSocket := perpClient.ws.(*perpWebSocket)
	assertOKXDemoBusinessSocket(t, "perp", perpSocket.backend.(*okxPublicWSBackend))
}

func assertOKXDemoBusinessSocket(t *testing.T, product string, backend *okxPublicWSBackend) {
	t.Helper()
	if backend.candleWS == nil {
		t.Fatalf("%s candle websocket is nil", product)
	}
	client, ok := backend.candleWS.(*okx.WSClient)
	if !ok {
		t.Fatalf("%s candle websocket type = %T", product, backend.candleWS)
	}
	if client.URL != okx.WSDemoBusinessBaseURL {
		t.Fatalf("%s candle websocket URL = %q, want %q", product, client.URL, okx.WSDemoBusinessBaseURL)
	}
}

func TestOKXPublicWSBackendStartCandlesRejectsUnsupportedAndMalformed(t *testing.T) {
	ws := newFakeOKXPublicWSClient()
	backend := newOKXSpotWSBackendWithClient(ws)
	if _, err := backend.StartCandles(context.Background(), "ETH-USDT", "3m", streamCallbacks[exchange.CandleEvent]{}); err == nil {
		t.Fatalf("StartCandles unsupported interval succeeded")
	}
	if len(ws.candleChannels) != 0 {
		t.Fatalf("candle channels = %v, want no subscription", ws.candleChannels)
	}

	var gotErr error
	if _, err := backend.StartCandles(context.Background(), "ETH-USDT", "1m", streamCallbacks[exchange.CandleEvent]{
		Error: func(err error) { gotErr = err },
	}); err != nil {
		t.Fatalf("StartCandles: %v", err)
	}
	ws.emitCandle(okx.Candle{"bad-ts", "100", "102", "99", "101", "2.5", "250", "252.5", "0"})
	if gotErr == nil {
		t.Fatalf("expected malformed candle error")
	}
	var exchangeErr *exchange.Error
	if !errors.As(gotErr, &exchangeErr) || exchangeErr.Kind() != exchange.KindMalformedResponse {
		t.Fatalf("error = %v, want malformed exchange error", gotErr)
	}
}

func TestOKXPerpWSBackendStartCandlesUsesBaseVolumeAndReconnectLifecycle(t *testing.T) {
	ws := newFakeOKXPublicWSClient()
	backend := newOKXPerpWSBackendWithClient(ws, func(context.Context, string) (okxContractMeta, error) {
		return okxContractMeta{contractValue: decimal.RequireFromString("0.01")}, nil
	})
	var statuses []backendStatus
	var got exchange.CandleEvent
	if _, err := backend.StartCandles(context.Background(), "BTC-USDT-SWAP", "1h", streamCallbacks[exchange.CandleEvent]{
		Event:  func(event exchange.CandleEvent) { got = event },
		Status: func(status backendStatus) { statuses = append(statuses, status) },
	}); err != nil {
		t.Fatalf("StartCandles: %v", err)
	}
	if len(ws.candleChannels) != 1 || ws.candleChannels[0] != "candle1H" {
		t.Fatalf("candle channels = %v, want candle1H", ws.candleChannels)
	}
	ws.reconnectStarted(errors.New("rotate"))
	ws.reconnectRecovered()
	if len(statuses) != 3 ||
		statuses[0].State != exchange.SubscriptionGap ||
		statuses[1].State != exchange.SubscriptionResyncing ||
		statuses[2].State != exchange.SubscriptionActive ||
		statuses[2].Phase != exchange.GapRecovered {
		t.Fatalf("statuses = %+v", statuses)
	}
	ws.emitCandle(okx.Candle{"1700000000000", "50000", "50100", "49900", "50050", "3", "0.03", "1501.5", "0"})
	if got.Instrument != "BTC-USDT-SWAP" ||
		got.Interval != "1h" ||
		!got.Candle.Volume.Equal(decimal.RequireFromString("0.03")) ||
		got.Candle.Complete {
		t.Fatalf("perp candle event = %+v", got)
	}
}

func TestOKXPublicWSBackendReconnectStatusMarksNextBookResync(t *testing.T) {
	ws := newFakeOKXPublicWSClient()
	backend := newOKXSpotWSBackendWithClient(ws)
	var statuses []backendStatus
	var book exchange.BookEvent
	if _, err := backend.StartOrderBook(context.Background(), "ETH-USDT", streamCallbacks[exchange.BookEvent]{
		Event:  func(event exchange.BookEvent) { book = event },
		Status: func(status backendStatus) { statuses = append(statuses, status) },
	}); err != nil {
		t.Fatalf("StartOrderBook: %v", err)
	}
	ws.reconnectStarted(errors.New("rotate"))
	ws.reconnectRecovered()
	if len(statuses) != 3 ||
		statuses[0].State != exchange.SubscriptionGap ||
		statuses[0].Phase != exchange.GapStarted ||
		statuses[1].State != exchange.SubscriptionResyncing ||
		statuses[2].State != exchange.SubscriptionActive ||
		statuses[2].Phase != exchange.GapRecovered ||
		statuses[0].Generation != 1 ||
		statuses[1].Generation != 1 ||
		statuses[2].Generation != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
	ws.emitBook(&okx.OrderBook{
		Bids: [][]string{{"100", "1", "0", "1"}},
		Asks: [][]string{{"101", "1", "0", "1"}},
		Ts:   "1700000000006",
	}, "snapshot")
	if !book.Resync {
		t.Fatalf("book after reconnect = %+v, want Resync", book)
	}
}

type blockingOKXConnectClient struct {
	*fakeOKXPublicWSClient
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (client *blockingOKXConnectClient) Connect() error {
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

func TestOKXPublicWSBackendConcurrentTopicsSharePendingConnect(t *testing.T) {
	ws := &blockingOKXConnectClient{
		fakeOKXPublicWSClient: newFakeOKXPublicWSClient(),
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
	backend := newOKXSpotWSBackendWithClient(ws)
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

type fakeOKXPublicWSClient struct {
	connectCalls int
	closeCalls   int

	bookDepths     []int
	candleChannels []string
	unsubscribed   []okx.WsSubscribeArgs

	bookHandler    func(*okx.OrderBook, string)
	bookError      func(error)
	tickerHandler  func(*okx.Ticker)
	tickerError    func(error)
	tradeHandler   func(*okx.PublicTrade)
	tradeError     func(error)
	candleHandler  func(okx.Candle)
	candleError    func(error)
	markHandler    func(*okx.MarkPrice)
	markError      func(error)
	fundingHandler func(*okx.FundingRate)
	fundingError   func(error)

	reconnectStarted   func(error)
	reconnectRecovered func()
}

func newFakeOKXPublicWSClient() *fakeOKXPublicWSClient {
	return &fakeOKXPublicWSClient{}
}

func (client *fakeOKXPublicWSClient) Connect() error {
	client.connectCalls++
	return nil
}

func (client *fakeOKXPublicWSClient) Close() {
	client.closeCalls++
}

func (client *fakeOKXPublicWSClient) SetReconnectHooks(started func(error), recovered func()) {
	client.reconnectStarted = started
	client.reconnectRecovered = recovered
}

func (client *fakeOKXPublicWSClient) SubscribeOrderBookDepth(_ string, depth int, handler func(*okx.OrderBook, string)) error {
	return client.SubscribeOrderBookDepthWithError("", depth, handler, nil)
}

func (client *fakeOKXPublicWSClient) SubscribeOrderBookDepthWithError(
	_ string,
	depth int,
	handler func(*okx.OrderBook, string),
	errorHandler func(error),
) error {
	client.bookDepths = append(client.bookDepths, depth)
	client.bookHandler = handler
	client.bookError = errorHandler
	return nil
}

func (client *fakeOKXPublicWSClient) SubscribeTicker(_ string, handler func(*okx.Ticker)) error {
	return client.SubscribeTickerWithError("", handler, nil)
}

func (client *fakeOKXPublicWSClient) SubscribeTickerWithError(_ string, handler func(*okx.Ticker), errorHandler func(error)) error {
	client.tickerHandler = handler
	client.tickerError = errorHandler
	return nil
}

func (client *fakeOKXPublicWSClient) SubscribeTrades(_ string, handler func(*okx.PublicTrade)) error {
	return client.SubscribeTradesWithError("", handler, nil)
}

func (client *fakeOKXPublicWSClient) SubscribeTradesWithError(_ string, handler func(*okx.PublicTrade), errorHandler func(error)) error {
	client.tradeHandler = handler
	client.tradeError = errorHandler
	return nil
}

func (client *fakeOKXPublicWSClient) SubscribeCandlesWithError(_ string, channel string, handler func(okx.Candle), errorHandler func(error)) error {
	client.candleChannels = append(client.candleChannels, channel)
	client.candleHandler = handler
	client.candleError = errorHandler
	return nil
}

func (client *fakeOKXPublicWSClient) SubscribeMarkPrice(_ string, handler func(*okx.MarkPrice)) error {
	return client.SubscribeMarkPriceWithError("", handler, nil)
}

func (client *fakeOKXPublicWSClient) SubscribeMarkPriceWithError(_ string, handler func(*okx.MarkPrice), errorHandler func(error)) error {
	client.markHandler = handler
	client.markError = errorHandler
	return nil
}

func (client *fakeOKXPublicWSClient) SubscribeFundingRate(_ string, handler func(*okx.FundingRate)) error {
	return client.SubscribeFundingRateWithError("", handler, nil)
}

func (client *fakeOKXPublicWSClient) SubscribeFundingRateWithError(_ string, handler func(*okx.FundingRate), errorHandler func(error)) error {
	client.fundingHandler = handler
	client.fundingError = errorHandler
	return nil
}

func (client *fakeOKXPublicWSClient) Unsubscribe(args okx.WsSubscribeArgs) error {
	client.unsubscribed = append(client.unsubscribed, args)
	return nil
}

func (client *fakeOKXPublicWSClient) emitBook(book *okx.OrderBook, action string) {
	client.bookHandler(book, action)
}

func (client *fakeOKXPublicWSClient) emitTicker(ticker *okx.Ticker) {
	client.tickerHandler(ticker)
}

func (client *fakeOKXPublicWSClient) emitTrade(trade *okx.PublicTrade) {
	client.tradeHandler(trade)
}

func (client *fakeOKXPublicWSClient) emitCandle(candle okx.Candle) {
	client.candleHandler(candle)
}

func (client *fakeOKXPublicWSClient) emitMark(mark *okx.MarkPrice) {
	client.markHandler(mark)
}

func (client *fakeOKXPublicWSClient) emitFunding(funding *okx.FundingRate) {
	client.fundingHandler(funding)
}

func (client *fakeOKXPublicWSClient) emitBookDecodeError(err error) {
	if client.bookError != nil {
		client.bookError(err)
	}
}

func (client *fakeOKXPublicWSClient) emitTickerDecodeError(err error) {
	if client.tickerError != nil {
		client.tickerError(err)
	}
}

func (client *fakeOKXPublicWSClient) emitTradeDecodeError(err error) {
	if client.tradeError != nil {
		client.tradeError(err)
	}
}

func (client *fakeOKXPublicWSClient) emitMarkDecodeError(err error) {
	if client.markError != nil {
		client.markError(err)
	}
}

func (client *fakeOKXPublicWSClient) emitFundingDecodeError(err error) {
	if client.fundingError != nil {
		client.fundingError(err)
	}
}
