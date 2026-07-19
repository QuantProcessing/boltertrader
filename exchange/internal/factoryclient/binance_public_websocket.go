package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

type binanceSpotPublicWSClient interface {
	Connect() error
	Close()
	SetPostReconnect(func())
	SubscribeLimitOrderBook(string, int, string, func(*binancespot.DepthEvent) error) error
	SubscribeBookTicker(string, func(*binancespot.BookTickerEvent) error) error
	SubscribeAggTrade(string, func(*binancespot.AggTradeEvent) error) error
	SubscribeKline(string, string, func(*binancespot.KlineEvent) error) error
	UnsubscribeLimitOrderBook(string, int, string) error
	UnsubscribeBookTicker(string) error
	UnsubscribeAggTrade(string) error
	UnsubscribeKline(string, string) error
}

type binancePerpPublicWSClient interface {
	Connect() error
	Close()
	SetPostReconnect(func())
	SubscribeIncrementOrderBook(string, string, func(*binanceperp.WsDepthEvent) error) error
	SubscribeLimitOrderBook(string, int, string, func(*binanceperp.WsDepthEvent) error) error
	SubscribeBookTicker(string, func(*binanceperp.WsBookTickerEvent) error) error
	SubscribeAggTrade(string, func(*binanceperp.WsAggTradeEvent) error) error
	SubscribeKline(string, string, func(*binanceperp.WsKlineEvent) error) error
	SubscribeMarkPrice(string, string, func(*binanceperp.WsMarkPriceEvent) error) error
	UnsubscribeIncrementOrderBook(string, string) error
	UnsubscribeLimitOrderBook(string, int, string) error
	UnsubscribeBookTicker(string) error
	UnsubscribeAggTrade(string) error
	UnsubscribeKline(string, string) error
	UnsubscribeMarkPrice(string, string) error
}

type binancePublicWSBackend struct {
	meta      clientMeta
	ws        binanceSpotPublicWSClient
	lifecycle *backendLifecycle

	closeOnce sync.Once
	mu        sync.Mutex

	connected   bool
	connecting  bool
	connectDone chan struct{}
	connectErr  error
	closed      bool

	resyncNextBook bool
}

func newBinanceSpotWSBackend(ws binanceSpotPublicWSClient) publicWSBackend {
	return newBinanceSpotWSBackendWithClient(ws)
}

func newBinanceSpotWSBackendWithClient(ws binanceSpotPublicWSClient) publicWSBackend {
	backend := &binancePublicWSBackend{
		meta:      clientMeta{venue: exchange.VenueBinance, product: exchange.ProductSpot},
		ws:        ws,
		lifecycle: newBackendLifecycle(),
	}
	backend.ws.SetPostReconnect(func() {
		backend.lifecycle.SynthesizedRecovery("Binance websocket subscriptions restored")
	})
	return backend
}

type binancePerpPublicWSBackend struct {
	meta      clientMeta
	ws        binancePerpPublicWSClient
	lifecycle *backendLifecycle
	diffDepth bool

	closeOnce sync.Once
	mu        sync.Mutex

	connected   bool
	connecting  bool
	connectDone chan struct{}
	connectErr  error
	closed      bool

	resyncNextBook bool
}

func newBinancePerpWSBackend(ws binancePerpPublicWSClient) perpWSBackend {
	return newBinancePerpWSBackendWithClient(ws)
}

func newBinancePerpWSBackendWithClient(ws binancePerpPublicWSClient) perpWSBackend {
	backend := &binancePerpPublicWSBackend{
		meta:      clientMeta{venue: exchange.VenueBinance, product: exchange.ProductPerp},
		ws:        ws,
		lifecycle: newBackendLifecycle(),
	}
	if backend.ws != nil {
		backend.ws.SetPostReconnect(func() {
			backend.lifecycle.SynthesizedRecovery("Binance websocket subscriptions restored")
		})
	}
	return backend
}

func newBinancePerpDemoWSBackendWithClient(ws binancePerpPublicWSClient) perpWSBackend {
	backend := newBinancePerpWSBackendWithClient(ws).(*binancePerpPublicWSBackend)
	backend.diffDepth = true
	return backend
}

func (backend *binancePublicWSBackend) ensureConnected(ctx context.Context, operation string) error {
	if backend == nil || backend.ws == nil {
		return websocketError(socketMeta(nil), operation, exchange.KindInvalidConfig, "Binance websocket client is not configured")
	}
	if ctx == nil {
		return websocketError(backend.meta, operation, exchange.KindInvalidRequest, "context is required")
	}
	if err := ctx.Err(); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return websocketError(backend.meta, operation, exchange.KindCanceled, "request context ended")
		case errors.Is(err, context.DeadlineExceeded):
			return websocketError(backend.meta, operation, exchange.KindDeadlineExceeded, "request context ended")
		default:
			return websocketError(backend.meta, operation, exchange.KindInvalidRequest, "request context ended")
		}
	}

	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.connected {
		backend.mu.Unlock()
		return nil
	}
	if backend.connecting {
		done := backend.connectDone
		backend.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return websocketContextError(backend.meta, operation, ctx.Err())
		}
		backend.mu.Lock()
		closed := backend.closed
		connected := backend.connected
		connectErr := backend.connectErr
		backend.mu.Unlock()
		if closed {
			return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
		}
		if connected {
			return nil
		}
		if connectErr != nil {
			return websocketError(backend.meta, operation, exchange.KindTransport, connectErr.Error())
		}
		return websocketError(backend.meta, operation, exchange.KindTransport, "websocket connection failed")
	}
	backend.connecting = true
	backend.connectDone = make(chan struct{})
	connectDone := backend.connectDone
	backend.mu.Unlock()

	err := backend.ws.Connect()

	backend.mu.Lock()
	backend.connecting = false
	backend.connectErr = err
	if err == nil && !backend.closed {
		backend.connected = true
	}
	closed := backend.closed
	close(connectDone)
	backend.mu.Unlock()
	if closed {
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if err != nil {
		return websocketError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *binancePublicWSBackend) StartOrderBook(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BookEvent],
) (func() error, error) {
	venueSymbol, canonical, err := binanceSpotSymbols(instrument, "WatchOrderBook")
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(ctx, "WatchOrderBook"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("book:"+venueSymbol, callbacks.Status, func() {
		backend.markResyncNextBook()
	})

	first := true
	err = backend.ws.SubscribeLimitOrderBook(venueSymbol, 5, "100ms", func(row *binancespot.DepthEvent) error {
		event, err := binanceSpotWSBookEvent(canonical, row, &first, backend.popResyncNextBook)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchOrderBook", exchange.KindTransport, err.Error())
	}

	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeLimitOrderBook(venueSymbol, 5, "100ms"); err != nil {
			return websocketError(backend.meta, "WatchOrderBook", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePublicWSBackend) StartBBO(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BBOEvent],
) (func() error, error) {
	venueSymbol, canonical, err := binanceSpotSymbols(instrument, "WatchBBO")
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(ctx, "WatchBBO"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("bbo:"+venueSymbol, callbacks.Status, nil)
	err = backend.ws.SubscribeBookTicker(venueSymbol, func(ticker *binancespot.BookTickerEvent) error {
		event, err := binanceSpotWSBBOEvent(canonical, ticker)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchBBO", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeBookTicker(venueSymbol); err != nil {
			return websocketError(backend.meta, "WatchBBO", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePublicWSBackend) StartPublicTrades(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.PublicTradeEvent],
) (func() error, error) {
	venueSymbol, canonical, err := binanceSpotSymbols(instrument, "WatchPublicTrades")
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(ctx, "WatchPublicTrades"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("trade:"+venueSymbol, callbacks.Status, nil)
	err = backend.ws.SubscribeAggTrade(venueSymbol, func(row *binancespot.AggTradeEvent) error {
		event, err := binanceSpotWSPublicTrade(canonical, row)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchPublicTrades", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeAggTrade(venueSymbol); err != nil {
			return websocketError(backend.meta, "WatchPublicTrades", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePublicWSBackend) StartCandles(
	ctx context.Context,
	instrument string,
	interval string,
	callbacks streamCallbacks[exchange.CandleEvent],
) (func() error, error) {
	venueSymbol, canonical, err := binanceSpotSymbols(instrument, "WatchCandles")
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(ctx, "WatchCandles"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("candles:"+venueSymbol+":"+interval, callbacks.Status, nil)
	err = backend.ws.SubscribeKline(venueSymbol, interval, func(row *binancespot.KlineEvent) error {
		event, err := binanceSpotWSCandleEvent(canonical, interval, row)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchCandles", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeKline(venueSymbol, interval); err != nil {
			return websocketError(backend.meta, "WatchCandles", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePerpPublicWSBackend) ensureConnected(ctx context.Context, operation string) error {
	if backend == nil || backend.ws == nil {
		return websocketError(socketMeta(nil), operation, exchange.KindInvalidConfig, "Binance websocket client is not configured")
	}
	if ctx == nil {
		return websocketError(backend.meta, operation, exchange.KindInvalidRequest, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(backend.meta, operation, err)
	}

	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.connected {
		backend.mu.Unlock()
		return nil
	}
	if backend.connecting {
		done := backend.connectDone
		backend.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return websocketContextError(backend.meta, operation, ctx.Err())
		}
		backend.mu.Lock()
		closed := backend.closed
		connected := backend.connected
		connectErr := backend.connectErr
		backend.mu.Unlock()
		if closed {
			return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
		}
		if connected {
			return nil
		}
		if connectErr != nil {
			return websocketError(backend.meta, operation, exchange.KindTransport, connectErr.Error())
		}
		return websocketError(backend.meta, operation, exchange.KindTransport, "websocket connection failed")
	}
	backend.connecting = true
	backend.connectDone = make(chan struct{})
	connectDone := backend.connectDone
	backend.mu.Unlock()

	err := backend.ws.Connect()

	backend.mu.Lock()
	backend.connecting = false
	backend.connectErr = err
	if err == nil && !backend.closed {
		backend.connected = true
	}
	closed := backend.closed
	close(connectDone)
	backend.mu.Unlock()
	if closed {
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if err != nil {
		return websocketError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *binancePerpPublicWSBackend) StartOrderBook(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BookEvent],
) (func() error, error) {
	canonical, venueSymbol, err := binancePerpNativeSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchOrderBook", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureConnected(ctx, "WatchOrderBook"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("book:"+venueSymbol, callbacks.Status, func() {
		backend.markResyncNextBook()
	})
	if backend.diffDepth {
		err = backend.ws.SubscribeIncrementOrderBook(venueSymbol, "100ms", func(row *binanceperp.WsDepthEvent) error {
			event, err := binancePerpWSBookDeltaEvent(canonical, row, backend.popResyncNextBook)
			if err != nil {
				emitBinanceWSError(callbacks, err)
				return nil
			}
			callbacks.Event(event)
			return nil
		})
		if err != nil {
			removeLifecycle()
			return nil, websocketError(backend.meta, "WatchOrderBook", exchange.KindTransport, err.Error())
		}
		return func() error {
			removeLifecycle()
			if err := backend.ws.UnsubscribeIncrementOrderBook(venueSymbol, "100ms"); err != nil {
				return websocketError(backend.meta, "WatchOrderBook", exchange.KindTransport, err.Error())
			}
			return nil
		}, nil
	}
	first := true
	err = backend.ws.SubscribeLimitOrderBook(venueSymbol, 5, "100ms", func(row *binanceperp.WsDepthEvent) error {
		event, err := binancePerpWSBookEvent(canonical, row, &first, backend.popResyncNextBook)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchOrderBook", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeLimitOrderBook(venueSymbol, 5, "100ms"); err != nil {
			return websocketError(backend.meta, "WatchOrderBook", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePerpPublicWSBackend) StartBBO(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BBOEvent],
) (func() error, error) {
	canonical, venueSymbol, err := binancePerpNativeSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchBBO", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureConnected(ctx, "WatchBBO"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("bbo:"+venueSymbol, callbacks.Status, nil)
	err = backend.ws.SubscribeBookTicker(venueSymbol, func(row *binanceperp.WsBookTickerEvent) error {
		event, err := binancePerpWSBBOEvent(canonical, row)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchBBO", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeBookTicker(venueSymbol); err != nil {
			return websocketError(backend.meta, "WatchBBO", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePerpPublicWSBackend) StartPublicTrades(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.PublicTradeEvent],
) (func() error, error) {
	canonical, venueSymbol, err := binancePerpNativeSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchPublicTrades", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureConnected(ctx, "WatchPublicTrades"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("trade:"+venueSymbol, callbacks.Status, nil)
	err = backend.ws.SubscribeAggTrade(venueSymbol, func(row *binanceperp.WsAggTradeEvent) error {
		event, err := binancePerpWSPublicTrade(canonical, row)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchPublicTrades", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeAggTrade(venueSymbol); err != nil {
			return websocketError(backend.meta, "WatchPublicTrades", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePerpPublicWSBackend) StartCandles(
	ctx context.Context,
	instrument string,
	interval string,
	callbacks streamCallbacks[exchange.CandleEvent],
) (func() error, error) {
	canonical, venueSymbol, err := binancePerpNativeSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchCandles", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureConnected(ctx, "WatchCandles"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("candles:"+venueSymbol+":"+interval, callbacks.Status, nil)
	err = backend.ws.SubscribeKline(venueSymbol, interval, func(row *binanceperp.WsKlineEvent) error {
		event, err := binancePerpWSCandleEvent(canonical, interval, row)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchCandles", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeKline(venueSymbol, interval); err != nil {
			return websocketError(backend.meta, "WatchCandles", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePerpPublicWSBackend) StartReference(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[perpReferenceEvent],
) (func() error, error) {
	canonical, venueSymbol, err := binancePerpNativeSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchPerpReference", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureConnected(ctx, "WatchPerpReference"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("reference:"+venueSymbol, callbacks.Status, nil)
	err = backend.ws.SubscribeMarkPrice(venueSymbol, "1s", func(row *binanceperp.WsMarkPriceEvent) error {
		event, err := binancePerpWSReferenceEvent(canonical, row)
		if err != nil {
			emitBinanceWSError(callbacks, err)
			return nil
		}
		callbacks.Event(event)
		return nil
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, "WatchPerpReference", exchange.KindTransport, err.Error())
	}
	return func() error {
		removeLifecycle()
		if err := backend.ws.UnsubscribeMarkPrice(venueSymbol, "1s"); err != nil {
			return websocketError(backend.meta, "WatchPerpReference", exchange.KindTransport, err.Error())
		}
		return nil
	}, nil
}

func (backend *binancePublicWSBackend) markResyncNextBook() {
	backend.mu.Lock()
	backend.resyncNextBook = true
	backend.mu.Unlock()
}

func (backend *binancePublicWSBackend) popResyncNextBook() bool {
	backend.mu.Lock()
	resync := backend.resyncNextBook
	backend.resyncNextBook = false
	backend.mu.Unlock()
	return resync
}

func (backend *binancePublicWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		backend.mu.Lock()
		backend.closed = true
		backend.mu.Unlock()
		if backend.ws != nil {
			backend.ws.Close()
		}
	})
	return nil
}

func (backend *binancePerpPublicWSBackend) markResyncNextBook() {
	backend.mu.Lock()
	backend.resyncNextBook = true
	backend.mu.Unlock()
}

func (backend *binancePerpPublicWSBackend) popResyncNextBook() bool {
	backend.mu.Lock()
	resync := backend.resyncNextBook
	backend.resyncNextBook = false
	backend.mu.Unlock()
	return resync
}

func (backend *binancePerpPublicWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		backend.mu.Lock()
		backend.closed = true
		backend.mu.Unlock()
		if backend.ws != nil {
			backend.ws.Close()
		}
	})
	return nil
}

func binanceSpotWSBookEvent(
	instrument string,
	row *binancespot.DepthEvent,
	first *bool,
	popResync func() bool,
) (exchange.BookEvent, error) {
	if row == nil {
		return exchange.BookEvent{}, binanceSpotMalformed("WatchOrderBook", "order book push is nil")
	}
	if row.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.BookEvent{}, binanceSpotMalformed("WatchOrderBook", "order book instrument mismatch")
	}
	if row.FinalUpdateID <= 0 {
		return exchange.BookEvent{}, binanceSpotMalformed("WatchOrderBook", "invalid order book sequence")
	}
	bids, err := binanceSpotWSBookLevels(row.Bids)
	if err != nil {
		return exchange.BookEvent{}, binanceSpotMalformed("WatchOrderBook", err.Error())
	}
	asks, err := binanceSpotWSBookLevels(row.Asks)
	if err != nil {
		return exchange.BookEvent{}, binanceSpotMalformed("WatchOrderBook", err.Error())
	}

	kind := exchange.EventSnapshot
	previous := ""
	if *first {
		*first = false
	}
	resync := false
	if popResync != nil {
		resync = popResync()
	}
	observedAt := time.Now().UTC()
	if row.EventTime > 0 {
		observedAt = time.UnixMilli(row.EventTime).UTC()
	}
	return exchange.BookEvent{
		Kind:       kind,
		Instrument: instrument,
		Sequence:   strconv.FormatInt(row.FinalUpdateID, 10),
		Previous:   previous,
		Resync:     resync,
		Bids:       bids,
		Asks:       asks,
		Time:       observedAt,
	}, nil
}

func binanceSpotWSBBOEvent(instrument string, ticker *binancespot.BookTickerEvent) (exchange.BBOEvent, error) {
	if ticker == nil {
		return exchange.BBOEvent{}, binanceSpotMalformed("WatchBBO", "ticker is nil")
	}
	if ticker.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.BBOEvent{}, binanceSpotMalformed("WatchBBO", "ticker instrument mismatch")
	}
	bidPrice, err := positiveDecimal(ticker.BestBidPrice)
	if err != nil {
		return exchange.BBOEvent{}, binanceSpotMalformed("WatchBBO", "invalid best bid price")
	}
	bidQty, err := positiveDecimal(ticker.BestBidQty)
	if err != nil {
		return exchange.BBOEvent{}, binanceSpotMalformed("WatchBBO", "invalid best bid size")
	}
	askPrice, err := positiveDecimal(ticker.BestAskPrice)
	if err != nil {
		return exchange.BBOEvent{}, binanceSpotMalformed("WatchBBO", "invalid best ask price")
	}
	askQty, err := positiveDecimal(ticker.BestAskQty)
	if err != nil {
		return exchange.BBOEvent{}, binanceSpotMalformed("WatchBBO", "invalid best ask size")
	}
	return exchange.BBOEvent{
		Instrument: instrument,
		Bid:        exchange.BookLevel{Price: bidPrice, Quantity: bidQty},
		Ask:        exchange.BookLevel{Price: askPrice, Quantity: askQty},
		Time:       time.Now().UTC(),
	}, nil
}

func binanceSpotWSCandleEvent(instrument string, interval string, row *binancespot.KlineEvent) (exchange.CandleEvent, error) {
	if row == nil {
		return exchange.CandleEvent{}, binanceSpotMalformed("WatchCandles", "candle push is nil")
	}
	expected := strings.ReplaceAll(instrument, "-", "")
	if row.Symbol != expected || row.Kline.Symbol != expected || row.Kline.Interval != interval {
		return exchange.CandleEvent{}, binanceSpotMalformed("WatchCandles", "candle instrument mismatch")
	}
	candle, err := binanceWSCandle(row.Kline.StartTime, row.Kline.CloseTime, row.Kline.OpenPrice, row.Kline.HighPrice, row.Kline.LowPrice, row.Kline.ClosePrice, row.Kline.Volume, row.Kline.IsClosed)
	if err != nil {
		return exchange.CandleEvent{}, binanceSpotMalformed("WatchCandles", err.Error())
	}
	return exchange.CandleEvent{Instrument: instrument, Interval: interval, Candle: candle}, nil
}

func binanceSpotWSPublicTrade(instrument string, row *binancespot.AggTradeEvent) (exchange.PublicTradeEvent, error) {
	if row == nil {
		return exchange.PublicTradeEvent{}, binanceSpotMalformed("WatchPublicTrades", "trade push is nil")
	}
	if row.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.PublicTradeEvent{}, binanceSpotMalformed("WatchPublicTrades", "trade instrument mismatch")
	}
	if row.AggTradeID <= 0 || row.TradeTime <= 0 {
		return exchange.PublicTradeEvent{}, binanceSpotMalformed("WatchPublicTrades", "invalid trade identity")
	}
	price, err := positiveDecimal(row.Price)
	if err != nil {
		return exchange.PublicTradeEvent{}, binanceSpotMalformed("WatchPublicTrades", "invalid trade price")
	}
	quantity, err := positiveDecimal(row.Quantity)
	if err != nil {
		return exchange.PublicTradeEvent{}, binanceSpotMalformed("WatchPublicTrades", "invalid trade quantity")
	}
	side := exchange.SideBuy
	if row.IsBuyerMaker {
		side = exchange.SideSell
	}
	return exchange.PublicTradeEvent{
		Instrument: instrument,
		TradeID:    strconv.FormatInt(row.AggTradeID, 10),
		Side:       side,
		Price:      price,
		Quantity:   quantity,
		Time:       time.UnixMilli(row.TradeTime).UTC(),
	}, nil
}

func binanceSpotWSBookLevels(rows [][]string) ([]exchange.BookLevel, error) {
	levels := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("order book level shape is invalid")
		}
		price, err := positiveDecimal(row[0])
		if err != nil {
			return nil, fmt.Errorf("order book level price is invalid")
		}
		qty, err := positiveDecimal(row[1])
		if err != nil {
			return nil, fmt.Errorf("order book level quantity is invalid")
		}
		levels = append(levels, exchange.BookLevel{Price: price, Quantity: qty})
	}
	if len(levels) == 0 {
		return nil, fmt.Errorf("order book has no levels")
	}
	return levels, nil
}

func binancePerpWSBookEvent(
	instrument string,
	row *binanceperp.WsDepthEvent,
	first *bool,
	popResync func() bool,
) (exchange.BookEvent, error) {
	if row == nil {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", "order book push is nil")
	}
	if row.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", "order book instrument mismatch")
	}
	if row.FinalUpdateID <= 0 {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", "invalid order book sequence")
	}
	bids, err := binancePerpWSBookLevels(row.Bids)
	if err != nil {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", err.Error())
	}
	asks, err := binancePerpWSBookLevels(row.Asks)
	if err != nil {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", err.Error())
	}
	if *first {
		*first = false
	}
	resync := false
	if popResync != nil {
		resync = popResync()
	}
	ts := firstPositive(row.TransactionTime, row.EventTime)
	observedAt := time.Now().UTC()
	if ts > 0 {
		observedAt = time.UnixMilli(ts).UTC()
	}
	return exchange.BookEvent{
		Kind:       exchange.EventSnapshot,
		Instrument: instrument,
		Sequence:   strconv.FormatInt(row.FinalUpdateID, 10),
		Resync:     resync,
		Bids:       bids,
		Asks:       asks,
		Time:       observedAt,
	}, nil
}

func binancePerpWSBookDeltaEvent(
	instrument string,
	row *binanceperp.WsDepthEvent,
	popResync func() bool,
) (exchange.BookEvent, error) {
	if row == nil {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", "order book push is nil")
	}
	if row.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", "order book instrument mismatch")
	}
	if row.FirstUpdateID <= 0 || row.FinalUpdateID < row.FirstUpdateID || row.FinalUpdateIDLast < 0 {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", "invalid order book sequence")
	}
	bids, err := binancePerpWSBookDeltaLevels(row.Bids)
	if err != nil {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", err.Error())
	}
	asks, err := binancePerpWSBookDeltaLevels(row.Asks)
	if err != nil {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", err.Error())
	}
	if len(bids) == 0 && len(asks) == 0 {
		return exchange.BookEvent{}, binancePerpMalformed("WatchOrderBook", "order book update has no levels")
	}
	resync := false
	if popResync != nil {
		resync = popResync()
	}
	ts := firstPositive(row.TransactionTime, row.EventTime)
	observedAt := time.Now().UTC()
	if ts > 0 {
		observedAt = time.UnixMilli(ts).UTC()
	}
	previous := ""
	if row.FinalUpdateIDLast > 0 {
		previous = strconv.FormatInt(row.FinalUpdateIDLast, 10)
	}
	return exchange.BookEvent{
		Kind:       exchange.EventDelta,
		Instrument: instrument,
		Sequence:   strconv.FormatInt(row.FinalUpdateID, 10),
		Previous:   previous,
		Resync:     resync,
		Bids:       bids,
		Asks:       asks,
		Time:       observedAt,
	}, nil
}

func binancePerpWSBBOEvent(instrument string, ticker *binanceperp.WsBookTickerEvent) (exchange.BBOEvent, error) {
	if ticker == nil {
		return exchange.BBOEvent{}, binancePerpMalformed("WatchBBO", "ticker is nil")
	}
	if ticker.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.BBOEvent{}, binancePerpMalformed("WatchBBO", "ticker instrument mismatch")
	}
	bidPrice, err := positiveDecimal(ticker.BestBidPrice)
	if err != nil {
		return exchange.BBOEvent{}, binancePerpMalformed("WatchBBO", "invalid best bid price")
	}
	bidQty, err := positiveDecimal(ticker.BestBidQty)
	if err != nil {
		return exchange.BBOEvent{}, binancePerpMalformed("WatchBBO", "invalid best bid size")
	}
	askPrice, err := positiveDecimal(ticker.BestAskPrice)
	if err != nil {
		return exchange.BBOEvent{}, binancePerpMalformed("WatchBBO", "invalid best ask price")
	}
	askQty, err := positiveDecimal(ticker.BestAskQty)
	if err != nil {
		return exchange.BBOEvent{}, binancePerpMalformed("WatchBBO", "invalid best ask size")
	}
	ts := time.Now().UTC()
	if ticker.EventTime > 0 {
		ts = time.UnixMilli(ticker.EventTime).UTC()
	}
	return exchange.BBOEvent{
		Instrument: instrument,
		Bid:        exchange.BookLevel{Price: bidPrice, Quantity: bidQty},
		Ask:        exchange.BookLevel{Price: askPrice, Quantity: askQty},
		Time:       ts,
	}, nil
}

func binancePerpWSPublicTrade(instrument string, row *binanceperp.WsAggTradeEvent) (exchange.PublicTradeEvent, error) {
	if row == nil {
		return exchange.PublicTradeEvent{}, binancePerpMalformed("WatchPublicTrades", "trade push is nil")
	}
	if row.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.PublicTradeEvent{}, binancePerpMalformed("WatchPublicTrades", "trade instrument mismatch")
	}
	if row.AggTradeID <= 0 || row.TradeTime <= 0 {
		return exchange.PublicTradeEvent{}, binancePerpMalformed("WatchPublicTrades", "invalid trade identity")
	}
	price, err := positiveDecimal(row.Price)
	if err != nil {
		return exchange.PublicTradeEvent{}, binancePerpMalformed("WatchPublicTrades", "invalid trade price")
	}
	quantity, err := positiveDecimal(row.Quantity)
	if err != nil {
		return exchange.PublicTradeEvent{}, binancePerpMalformed("WatchPublicTrades", "invalid trade quantity")
	}
	side := exchange.SideBuy
	if row.IsBuyerMaker {
		side = exchange.SideSell
	}
	return exchange.PublicTradeEvent{
		Instrument: instrument,
		TradeID:    strconv.FormatInt(row.AggTradeID, 10),
		Side:       side,
		Price:      price,
		Quantity:   quantity,
		Time:       time.UnixMilli(row.TradeTime).UTC(),
	}, nil
}

func binancePerpWSCandleEvent(instrument string, interval string, row *binanceperp.WsKlineEvent) (exchange.CandleEvent, error) {
	if row == nil {
		return exchange.CandleEvent{}, binancePerpMalformed("WatchCandles", "candle push is nil")
	}
	expected := strings.ReplaceAll(instrument, "-", "")
	if row.Symbol != expected || row.Kline.Symbol != expected || row.Kline.Interval != interval {
		return exchange.CandleEvent{}, binancePerpMalformed("WatchCandles", "candle instrument mismatch")
	}
	candle, err := binanceWSCandle(row.Kline.StartTime, row.Kline.EndTime, row.Kline.OpenPrice, row.Kline.HighPrice, row.Kline.LowPrice, row.Kline.ClosePrice, row.Kline.Volume, row.Kline.IsClosed)
	if err != nil {
		return exchange.CandleEvent{}, binancePerpMalformed("WatchCandles", err.Error())
	}
	return exchange.CandleEvent{Instrument: instrument, Interval: interval, Candle: candle}, nil
}

func binancePerpWSReferenceEvent(instrument string, row *binanceperp.WsMarkPriceEvent) (perpReferenceEvent, error) {
	if row == nil {
		return perpReferenceEvent{}, binancePerpMalformed("WatchPerpReference", "reference push is nil")
	}
	if row.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return perpReferenceEvent{}, binancePerpMalformed("WatchPerpReference", "reference instrument mismatch")
	}
	mark, err := positiveDecimal(row.MarkPrice)
	if err != nil {
		return perpReferenceEvent{}, binancePerpMalformed("WatchPerpReference", "invalid mark price")
	}
	funding, err := positiveOrNegativeDecimal(row.FundingRate)
	if err != nil {
		return perpReferenceEvent{}, binancePerpMalformed("WatchPerpReference", "invalid funding rate")
	}
	if row.EventTime <= 0 || row.NextFundingTime <= 0 {
		return perpReferenceEvent{}, binancePerpMalformed("WatchPerpReference", "invalid reference timestamp")
	}
	observedAt := time.UnixMilli(row.EventTime).UTC()
	return perpReferenceEvent{
		MarkPrice: exchange.MarkPriceEvent{
			Instrument: instrument,
			Price:      mark,
			Time:       observedAt,
		},
		MarkValid: true,
		FundingRate: exchange.FundingRateEvent{
			Instrument:  instrument,
			Rate:        funding,
			EffectiveAt: observedAt,
			NextAt:      time.UnixMilli(row.NextFundingTime).UTC(),
		},
		FundingValid: true,
	}, nil
}

func binancePerpWSBookLevels(rows [][]interface{}) ([]exchange.BookLevel, error) {
	levels := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("order book level shape is invalid")
		}
		price, err := positiveDecimal(fmt.Sprint(row[0]))
		if err != nil {
			return nil, fmt.Errorf("order book level price is invalid")
		}
		qty, err := positiveDecimal(fmt.Sprint(row[1]))
		if err != nil {
			return nil, fmt.Errorf("order book level quantity is invalid")
		}
		levels = append(levels, exchange.BookLevel{Price: price, Quantity: qty})
	}
	if len(levels) == 0 {
		return nil, fmt.Errorf("order book has no levels")
	}
	return levels, nil
}

func binancePerpWSBookDeltaLevels(rows [][]interface{}) ([]exchange.BookLevel, error) {
	levels := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("order book level shape is invalid")
		}
		price, err := positiveDecimal(fmt.Sprint(row[0]))
		if err != nil {
			return nil, fmt.Errorf("order book level price is invalid")
		}
		qty, err := decimal.NewFromString(fmt.Sprint(row[1]))
		if err != nil || qty.IsNegative() {
			return nil, fmt.Errorf("order book level quantity is invalid")
		}
		levels = append(levels, exchange.BookLevel{Price: price, Quantity: qty})
	}
	return levels, nil
}

func binanceWSCandle(openMillis int64, closeMillisInclusive int64, openRaw, highRaw, lowRaw, closeRaw, volumeRaw string, complete bool) (exchange.Candle, error) {
	if openMillis <= 0 || closeMillisInclusive < openMillis {
		return exchange.Candle{}, fmt.Errorf("invalid candle time range")
	}
	open, err := positiveDecimal(openRaw)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle open")
	}
	high, err := positiveDecimal(highRaw)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle high")
	}
	low, err := positiveDecimal(lowRaw)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle low")
	}
	closeValue, err := positiveDecimal(closeRaw)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle close")
	}
	volume, err := binanceSpotDecimalValue(volumeRaw)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle volume")
	}
	if high.LessThan(low) ||
		open.GreaterThan(high) || open.LessThan(low) ||
		closeValue.GreaterThan(high) || closeValue.LessThan(low) {
		return exchange.Candle{}, fmt.Errorf("invalid candle OHLC")
	}
	return exchange.Candle{
		OpenTime:  time.UnixMilli(openMillis).UTC(),
		CloseTime: time.UnixMilli(closeMillisInclusive + 1).UTC(),
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closeValue,
		Volume:    volume,
		Complete:  complete,
	}, nil
}

func binanceSpotDecimalValue(raw string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return decimal.Zero, fmt.Errorf("decimal is empty")
	}
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, err
	}
	if value.IsNegative() {
		return decimal.Zero, fmt.Errorf("decimal is negative")
	}
	return value, nil
}

func emitBinanceWSError[T any](callbacks streamCallbacks[T], err error) {
	if callbacks.Error != nil {
		callbacks.Error(err)
	}
}

var _ publicWSBackend = (*binancePublicWSBackend)(nil)
var _ perpWSBackend = (*binancePerpPublicWSBackend)(nil)
