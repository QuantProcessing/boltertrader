package factoryclient

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type okxPublicWebSocketClient interface {
	Connect() error
	Close()
	SetReconnectHooks(func(error), func())
	SubscribeOrderBookDepthWithError(string, int, func(*okx.OrderBook, string), func(error)) error
	SubscribeTickerWithError(string, func(*okx.Ticker), func(error)) error
	SubscribeTradesWithError(string, func(*okx.PublicTrade), func(error)) error
	SubscribeCandlesWithError(string, string, func(okx.Candle), func(error)) error
	SubscribeMarkPriceWithError(string, func(*okx.MarkPrice), func(error)) error
	SubscribeFundingRateWithError(string, func(*okx.FundingRate), func(error)) error
	Unsubscribe(okx.WsSubscribeArgs) error
}

type okxPerpMetaLoader func(context.Context, string) (okxContractMeta, error)

type okxPublicWSBackend struct {
	meta      clientMeta
	ws        okxPublicWebSocketClient
	candleWS  okxPublicWebSocketClient
	perpMeta  okxPerpMetaLoader
	closeOnce sync.Once

	mu             sync.Mutex
	connected      bool
	connecting     bool
	connectDone    chan struct{}
	connectErr     error
	closed         bool
	generation     uint64
	statusHandlers map[*okxWSStatusRegistration]struct{}
	resyncNextBook bool

	candleMu          sync.Mutex
	candleConnected   bool
	candleConnecting  bool
	candleConnectDone chan struct{}
	candleConnectErr  error
}

type okxWSStatusRegistration struct {
	status func(backendStatus)
}

func newOKXSpotWSBackend(ws *okx.WSClient) publicWSBackend {
	return newOKXSpotWSBackendWithClient(ws)
}

func newOKXPerpWSBackend(ws *okx.WSClient, meta okxPerpMetaLoader) perpWSBackend {
	return newOKXPerpWSBackendWithClient(ws, meta)
}

func newOKXSpotWSBackendWithClient(ws okxPublicWebSocketClient) publicWSBackend {
	return &okxPublicWSBackend{
		meta:           clientMeta{venue: exchange.VenueOKX, product: exchange.ProductSpot},
		ws:             ws,
		statusHandlers: make(map[*okxWSStatusRegistration]struct{}),
	}
}

func newOKXSpotWSBackendWithClients(ws, candleWS okxPublicWebSocketClient) publicWSBackend {
	backend := newOKXSpotWSBackendWithClient(ws).(*okxPublicWSBackend)
	backend.candleWS = candleWS
	return backend
}

func newOKXPerpWSBackendWithClient(ws okxPublicWebSocketClient, meta okxPerpMetaLoader) perpWSBackend {
	return &okxPublicWSBackend{
		meta:           clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp},
		ws:             ws,
		perpMeta:       meta,
		statusHandlers: make(map[*okxWSStatusRegistration]struct{}),
	}
}

func newOKXPerpWSBackendWithClients(ws, candleWS okxPublicWebSocketClient, meta okxPerpMetaLoader) perpWSBackend {
	backend := newOKXPerpWSBackendWithClient(ws, meta).(*okxPublicWSBackend)
	backend.candleWS = candleWS
	return backend
}

func (backend *okxPublicWSBackend) StartOrderBook(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BookEvent],
) (func() error, error) {
	if err := backend.ensureConnected(ctx, "WatchOrderBook"); err != nil {
		return nil, err
	}
	registration := backend.registerStatus(callbacks.Status)
	multiplier, err := backend.quantityMultiplier(ctx, "WatchOrderBook", instrument)
	if err != nil {
		backend.unregisterStatus(registration)
		return nil, err
	}
	if err := backend.ws.SubscribeOrderBookDepthWithError(
		instrument,
		5,
		func(book *okx.OrderBook, action string) {
			event, err := backend.bookEvent("WatchOrderBook", instrument, book, action, multiplier)
			if err != nil {
				emitOKXWSError(callbacks, err)
				return
			}
			callbacks.Event(event)
		},
		func(err error) {
			emitOKXWSError(callbacks, okxWSError(backend.meta, "WatchOrderBook", exchange.KindMalformedResponse, err.Error()))
		},
	); err != nil {
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, "WatchOrderBook", exchange.KindTransport, err.Error())
	}
	return func() error {
		backend.unregisterStatus(registration)
		return backend.unsubscribe("WatchOrderBook", okx.WsSubscribeArgs{Channel: "books5", InstId: instrument})
	}, nil
}

func (backend *okxPublicWSBackend) StartBBO(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BBOEvent],
) (func() error, error) {
	if err := backend.ensureConnected(ctx, "WatchBBO"); err != nil {
		return nil, err
	}
	registration := backend.registerStatus(callbacks.Status)
	if err := backend.ws.SubscribeTickerWithError(
		instrument,
		func(ticker *okx.Ticker) {
			event, err := okxBBOEvent(backend.meta.product, "WatchBBO", instrument, ticker)
			if err != nil {
				emitOKXWSError(callbacks, err)
				return
			}
			callbacks.Event(event)
		},
		func(err error) {
			emitOKXWSError(callbacks, okxWSError(backend.meta, "WatchBBO", exchange.KindMalformedResponse, err.Error()))
		},
	); err != nil {
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, "WatchBBO", exchange.KindTransport, err.Error())
	}
	return func() error {
		backend.unregisterStatus(registration)
		return backend.unsubscribe("WatchBBO", okx.WsSubscribeArgs{Channel: "tickers", InstId: instrument})
	}, nil
}

func (backend *okxPublicWSBackend) StartPublicTrades(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.PublicTradeEvent],
) (func() error, error) {
	if err := backend.ensureConnected(ctx, "WatchPublicTrades"); err != nil {
		return nil, err
	}
	registration := backend.registerStatus(callbacks.Status)
	multiplier, err := backend.quantityMultiplier(ctx, "WatchPublicTrades", instrument)
	if err != nil {
		backend.unregisterStatus(registration)
		return nil, err
	}
	if err := backend.ws.SubscribeTradesWithError(
		instrument,
		func(trade *okx.PublicTrade) {
			event, err := okxPublicTradeEvent(backend.meta.product, "WatchPublicTrades", instrument, trade, multiplier)
			if err != nil {
				emitOKXWSError(callbacks, err)
				return
			}
			callbacks.Event(event)
		},
		func(err error) {
			emitOKXWSError(callbacks, okxWSError(backend.meta, "WatchPublicTrades", exchange.KindMalformedResponse, err.Error()))
		},
	); err != nil {
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, "WatchPublicTrades", exchange.KindTransport, err.Error())
	}
	return func() error {
		backend.unregisterStatus(registration)
		return backend.unsubscribe("WatchPublicTrades", okx.WsSubscribeArgs{Channel: "trades", InstId: instrument})
	}, nil
}

func (backend *okxPublicWSBackend) StartCandles(
	ctx context.Context,
	instrument string,
	interval string,
	callbacks streamCallbacks[exchange.CandleEvent],
) (func() error, error) {
	const operation = "WatchCandles"
	if err := backend.ensureCandleConnected(ctx, operation); err != nil {
		return nil, err
	}
	registration := backend.registerStatus(callbacks.Status)
	channel, duration, err := okxWSCandleChannel(interval)
	if err != nil {
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
	}
	if backend.meta.product == exchange.ProductSpot {
		if err := okxValidateSpotInstrument(instrument); err != nil {
			backend.unregisterStatus(registration)
			return nil, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
		}
	} else if err := okxValidateSwapInstrument(instrument); err != nil {
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.candleClient().SubscribeCandlesWithError(
		instrument,
		channel,
		func(candle okx.Candle) {
			event, err := okxWSCandleEvent(backend.meta.product, operation, instrument, interval, duration, candle)
			if err != nil {
				emitOKXWSError(callbacks, err)
				return
			}
			callbacks.Event(event)
		},
		func(err error) {
			emitOKXWSError(callbacks, okxWSError(backend.meta, operation, exchange.KindMalformedResponse, err.Error()))
		},
	); err != nil {
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return func() error {
		backend.unregisterStatus(registration)
		return backend.unsubscribeCandle(operation, okx.WsSubscribeArgs{Channel: channel, InstId: instrument})
	}, nil
}

func (backend *okxPublicWSBackend) StartReference(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[perpReferenceEvent],
) (func() error, error) {
	if backend.meta.product != exchange.ProductPerp {
		return nil, okxWSError(backend.meta, "WatchReference", exchange.KindInvalidRequest, "reference streams require perp product")
	}
	if err := backend.ensureConnected(ctx, "WatchReference"); err != nil {
		return nil, err
	}
	registration := backend.registerStatus(callbacks.Status)
	if err := backend.ws.SubscribeMarkPriceWithError(
		instrument,
		func(mark *okx.MarkPrice) {
			event, err := okxMarkPriceEvent("WatchMarkPrice", instrument, mark)
			if err != nil {
				emitOKXWSError(callbacks, err)
				return
			}
			callbacks.Event(perpReferenceEvent{
				MarkPrice: event,
				MarkValid: true,
			})
		},
		func(err error) {
			emitOKXWSError(callbacks, okxWSError(backend.meta, "WatchMarkPrice", exchange.KindMalformedResponse, err.Error()))
		},
	); err != nil {
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, "WatchMarkPrice", exchange.KindTransport, err.Error())
	}
	fundingSubscribed := false
	if err := backend.ws.SubscribeFundingRateWithError(
		instrument,
		func(funding *okx.FundingRate) {
			event, err := okxFundingRateEvent("WatchFundingRate", instrument, funding)
			if err != nil {
				emitOKXWSError(callbacks, err)
				return
			}
			callbacks.Event(perpReferenceEvent{
				FundingRate:  event,
				FundingValid: true,
			})
		},
		func(err error) {
			emitOKXWSError(callbacks, okxWSError(backend.meta, "WatchFundingRate", exchange.KindMalformedResponse, err.Error()))
		},
	); err != nil {
		_ = backend.unsubscribe("WatchMarkPrice", okx.WsSubscribeArgs{Channel: "mark-price", InstId: instrument})
		backend.unregisterStatus(registration)
		return nil, okxWSError(backend.meta, "WatchFundingRate", exchange.KindTransport, err.Error())
	}
	fundingSubscribed = true
	return func() error {
		backend.unregisterStatus(registration)
		var errs []error
		if err := backend.unsubscribe("WatchMarkPrice", okx.WsSubscribeArgs{Channel: "mark-price", InstId: instrument}); err != nil {
			errs = append(errs, err)
		}
		if fundingSubscribed {
			if err := backend.unsubscribe("WatchFundingRate", okx.WsSubscribeArgs{Channel: "funding-rate", InstId: instrument}); err != nil {
				errs = append(errs, err)
			}
		}
		return errorsJoin(errs)
	}, nil
}

func okxWSCandleChannel(interval string) (string, time.Duration, error) {
	switch interval {
	case "1m":
		return "candle1m", time.Minute, nil
	case "5m":
		return "candle5m", 5 * time.Minute, nil
	case "15m":
		return "candle15m", 15 * time.Minute, nil
	case "30m":
		return "candle30m", 30 * time.Minute, nil
	case "1h":
		return "candle1H", time.Hour, nil
	case "4h":
		return "candle4H", 4 * time.Hour, nil
	case "12h":
		return "candle12H", 12 * time.Hour, nil
	case "1d":
		return "candle1D", 24 * time.Hour, nil
	default:
		return "", 0, fmt.Errorf("interval must be one of 1m, 5m, 15m, 30m, 1h, 4h, 12h, or 1d")
	}
}

func (backend *okxPublicWSBackend) ensureConnected(ctx context.Context, operation string) error {
	if backend == nil || backend.ws == nil {
		return okxWSError(socketMeta(nil), operation, exchange.KindInvalidConfig, "OKX websocket client is not configured")
	}
	if ctx == nil {
		return okxWSError(backend.meta, operation, exchange.KindInvalidRequest, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(backend.meta, operation, err)
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
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
			return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
		}
		if connected {
			return nil
		}
		if connectErr != nil {
			return okxWSError(backend.meta, operation, exchange.KindTransport, connectErr.Error())
		}
		return okxWSError(backend.meta, operation, exchange.KindTransport, "websocket connection failed")
	}
	backend.connecting = true
	backend.connectDone = make(chan struct{})
	connectDone := backend.connectDone
	backend.mu.Unlock()

	backend.ws.SetReconnectHooks(backend.reconnectStarted, backend.reconnectRecovered)
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
		return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if err != nil {
		return okxWSError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *okxPublicWSBackend) candleClient() okxPublicWebSocketClient {
	if backend.candleWS != nil {
		return backend.candleWS
	}
	return backend.ws
}

func (backend *okxPublicWSBackend) ensureCandleConnected(ctx context.Context, operation string) error {
	if backend == nil || backend.candleClient() == nil {
		return okxWSError(socketMeta(nil), operation, exchange.KindInvalidConfig, "OKX candle websocket client is not configured")
	}
	if backend.candleWS == nil {
		return backend.ensureConnected(ctx, operation)
	}
	if ctx == nil {
		return okxWSError(backend.meta, operation, exchange.KindInvalidRequest, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(backend.meta, operation, err)
	}
	backend.mu.Lock()
	closed := backend.closed
	backend.mu.Unlock()
	if closed {
		return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}

	backend.candleMu.Lock()
	if backend.candleConnected {
		backend.candleMu.Unlock()
		return nil
	}
	if backend.candleConnecting {
		done := backend.candleConnectDone
		backend.candleMu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return websocketContextError(backend.meta, operation, ctx.Err())
		}
		backend.candleMu.Lock()
		connected := backend.candleConnected
		connectErr := backend.candleConnectErr
		backend.candleMu.Unlock()
		backend.mu.Lock()
		closed = backend.closed
		backend.mu.Unlock()
		if closed {
			return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
		}
		if connected {
			return nil
		}
		if connectErr != nil {
			return okxWSError(backend.meta, operation, exchange.KindTransport, connectErr.Error())
		}
		return okxWSError(backend.meta, operation, exchange.KindTransport, "websocket connection failed")
	}
	backend.candleConnecting = true
	backend.candleConnectDone = make(chan struct{})
	connectDone := backend.candleConnectDone
	backend.candleMu.Unlock()

	backend.candleWS.SetReconnectHooks(backend.reconnectStarted, backend.reconnectRecovered)
	err := backend.candleWS.Connect()

	backend.mu.Lock()
	closed = backend.closed
	backend.mu.Unlock()
	backend.candleMu.Lock()
	backend.candleConnecting = false
	backend.candleConnectErr = err
	if err == nil && !closed {
		backend.candleConnected = true
	}
	close(connectDone)
	backend.candleMu.Unlock()
	if closed {
		return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if err != nil {
		return okxWSError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *okxPublicWSBackend) reconnectStarted(err error) {
	reason := "OKX websocket disconnected"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		reason = err.Error()
	}
	backend.mu.Lock()
	backend.generation++
	generation := backend.generation
	backend.resyncNextBook = true
	backend.mu.Unlock()
	backend.emitStatus(backendStatus{
		State:      exchange.SubscriptionGap,
		Phase:      exchange.GapStarted,
		Generation: generation,
		Reason:     reason,
	})
}

func (backend *okxPublicWSBackend) reconnectRecovered() {
	backend.mu.Lock()
	generation := backend.generation
	backend.mu.Unlock()
	backend.emitStatus(backendStatus{
		State:      exchange.SubscriptionResyncing,
		Generation: generation,
		Reason:     "OKX websocket subscriptions restored; order book snapshot pending",
	})
	backend.emitStatus(backendStatus{
		State:      exchange.SubscriptionActive,
		Phase:      exchange.GapRecovered,
		Generation: generation,
		Reason:     "OKX websocket subscriptions restored",
	})
}

func (backend *okxPublicWSBackend) registerStatus(status func(backendStatus)) *okxWSStatusRegistration {
	registration := &okxWSStatusRegistration{status: status}
	backend.mu.Lock()
	backend.statusHandlers[registration] = struct{}{}
	backend.mu.Unlock()
	return registration
}

func (backend *okxPublicWSBackend) unregisterStatus(registration *okxWSStatusRegistration) {
	backend.mu.Lock()
	delete(backend.statusHandlers, registration)
	backend.mu.Unlock()
}

func (backend *okxPublicWSBackend) emitStatus(status backendStatus) {
	backend.mu.Lock()
	handlers := make([]func(backendStatus), 0, len(backend.statusHandlers))
	for registration := range backend.statusHandlers {
		if registration.status != nil {
			handlers = append(handlers, registration.status)
		}
	}
	backend.mu.Unlock()
	for _, handler := range handlers {
		handler(status)
	}
}

func (backend *okxPublicWSBackend) bookEvent(
	operation string,
	instrument string,
	book *okx.OrderBook,
	action string,
	multiplier decimal.Decimal,
) (exchange.BookEvent, error) {
	if book == nil {
		return exchange.BookEvent{}, okxWSError(backend.meta, operation, exchange.KindMalformedResponse, "book push is nil")
	}
	restBook, err := okxOrderBook(backend.meta.product, operation, instrument, 5, []okx.OrderBook{*book}, multiplier)
	if err != nil {
		return exchange.BookEvent{}, err
	}
	kind := exchange.EventDelta
	if action == "snapshot" || action == "" {
		kind = exchange.EventSnapshot
	}
	backend.mu.Lock()
	resync := backend.resyncNextBook
	backend.resyncNextBook = false
	backend.mu.Unlock()
	return exchange.BookEvent{
		Kind:       kind,
		Instrument: restBook.Instrument,
		Resync:     resync,
		Bids:       restBook.Bids,
		Asks:       restBook.Asks,
		Time:       restBook.Time,
	}, nil
}

func (backend *okxPublicWSBackend) quantityMultiplier(ctx context.Context, operation, instrument string) (decimal.Decimal, error) {
	if backend.meta.product == exchange.ProductSpot {
		if err := okxValidateSpotInstrument(instrument); err != nil {
			return decimal.Zero, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
		}
		return decimal.NewFromInt(1), nil
	}
	if err := okxValidateSwapInstrument(instrument); err != nil {
		return decimal.Zero, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
	}
	if backend.perpMeta == nil {
		return decimal.Zero, okxWSError(backend.meta, operation, exchange.KindInvalidConfig, "OKX perp metadata loader is not configured")
	}
	meta, err := backend.perpMeta(ctx, instrument)
	if err != nil {
		return decimal.Zero, err
	}
	if !meta.contractValue.IsPositive() {
		return decimal.Zero, okxWSError(backend.meta, operation, exchange.KindMalformedResponse, "OKX contract value is invalid")
	}
	return meta.contractValue, nil
}

func (backend *okxPublicWSBackend) unsubscribe(operation string, args okx.WsSubscribeArgs) error {
	if err := backend.ws.Unsubscribe(args); err != nil {
		return okxWSError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *okxPublicWSBackend) unsubscribeCandle(operation string, args okx.WsSubscribeArgs) error {
	if err := backend.candleClient().Unsubscribe(args); err != nil {
		return okxWSError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *okxPublicWSBackend) Close() error {
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
		if backend.candleWS != nil {
			backend.candleWS.Close()
		}
	})
	return nil
}

func okxBBOEvent(product exchange.Product, operation, instrument string, ticker *okx.Ticker) (exchange.BBOEvent, error) {
	if ticker == nil || ticker.InstId != instrument {
		return exchange.BBOEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "ticker instrument mismatch")
	}
	bidPrice, err := okxPositiveDecimal(ticker.BidPx)
	if err != nil {
		return exchange.BBOEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid bid price")
	}
	bidSize, err := okxNonNegativeDecimal(ticker.BidSz)
	if err != nil {
		return exchange.BBOEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid bid size")
	}
	askPrice, err := okxPositiveDecimal(ticker.AskPx)
	if err != nil {
		return exchange.BBOEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid ask price")
	}
	askSize, err := okxNonNegativeDecimal(ticker.AskSz)
	if err != nil {
		return exchange.BBOEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid ask size")
	}
	ts, err := okxMillis(ticker.Ts)
	if err != nil {
		return exchange.BBOEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid ticker timestamp")
	}
	return exchange.BBOEvent{
		Instrument: instrument,
		Bid:        exchange.BookLevel{Price: bidPrice, Quantity: bidSize},
		Ask:        exchange.BookLevel{Price: askPrice, Quantity: askSize},
		Time:       ts,
	}, nil
}

func okxPublicTradeEvent(product exchange.Product, operation, instrument string, trade *okx.PublicTrade, multiplier decimal.Decimal) (exchange.PublicTradeEvent, error) {
	if trade == nil || trade.InstId != instrument {
		return exchange.PublicTradeEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "trade instrument mismatch")
	}
	price, err := okxPositiveDecimal(trade.Px)
	if err != nil {
		return exchange.PublicTradeEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid trade price")
	}
	quantity, err := okxPositiveDecimal(trade.Sz)
	if err != nil {
		return exchange.PublicTradeEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid trade quantity")
	}
	side, err := okxExchangeSide(trade.Side)
	if err != nil {
		return exchange.PublicTradeEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid trade side")
	}
	ts, err := okxMillis(trade.Ts)
	if err != nil {
		return exchange.PublicTradeEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, "invalid trade timestamp")
	}
	return exchange.PublicTradeEvent{
		Instrument: instrument,
		TradeID:    trade.TradeId,
		Side:       side,
		Price:      price,
		Quantity:   quantity.Mul(multiplier),
		Time:       ts,
	}, nil
}

func okxWSCandleEvent(
	product exchange.Product,
	operation string,
	instrument string,
	interval string,
	duration time.Duration,
	row okx.Candle,
) (exchange.CandleEvent, error) {
	candle, err := okxCandle(row, product, duration)
	if err != nil {
		return exchange.CandleEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: product}, operation, exchange.KindMalformedResponse, err.Error())
	}
	return exchange.CandleEvent{
		Instrument: instrument,
		Interval:   interval,
		Candle:     candle,
	}, nil
}

func okxMarkPriceEvent(operation, instrument string, mark *okx.MarkPrice) (exchange.MarkPriceEvent, error) {
	if mark == nil || mark.InstId != instrument {
		return exchange.MarkPriceEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}, operation, exchange.KindMalformedResponse, "mark-price instrument mismatch")
	}
	price, err := okxPositiveDecimal(mark.MarkPx)
	if err != nil {
		return exchange.MarkPriceEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}, operation, exchange.KindMalformedResponse, "invalid mark price")
	}
	ts, err := okxMillis(mark.Ts)
	if err != nil {
		return exchange.MarkPriceEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}, operation, exchange.KindMalformedResponse, "invalid mark timestamp")
	}
	return exchange.MarkPriceEvent{Instrument: instrument, Price: price, Time: ts}, nil
}

func okxFundingRateEvent(operation, instrument string, funding *okx.FundingRate) (exchange.FundingRateEvent, error) {
	if funding == nil || funding.InstrumentID != instrument {
		return exchange.FundingRateEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}, operation, exchange.KindMalformedResponse, "funding-rate instrument mismatch")
	}
	rate, err := okxDecimal(funding.FundingRate)
	if err != nil {
		return exchange.FundingRateEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}, operation, exchange.KindMalformedResponse, "invalid funding rate")
	}
	effectiveAt, err := okxMillis(funding.FundingTime)
	if err != nil {
		return exchange.FundingRateEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}, operation, exchange.KindMalformedResponse, "invalid funding timestamp")
	}
	nextAt, err := okxOptionalMillis(funding.NextFundingTime)
	if err != nil {
		return exchange.FundingRateEvent{}, okxWSError(clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}, operation, exchange.KindMalformedResponse, "invalid next funding timestamp")
	}
	return exchange.FundingRateEvent{Instrument: instrument, Rate: rate, EffectiveAt: effectiveAt, NextAt: nextAt}, nil
}

func emitOKXWSError[T any](callbacks streamCallbacks[T], err error) {
	if callbacks.Error != nil {
		callbacks.Error(err)
	}
}

func okxWSError(meta clientMeta, operation string, kind exchange.ErrorKind, message string) error {
	if strings.TrimSpace(message) == "" {
		message = string(kind)
	}
	return websocketError(meta, operation, kind, message)
}

func errorsJoin(errs []error) error {
	var joined error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if joined == nil {
			joined = err
			continue
		}
		joined = fmt.Errorf("%v; %w", joined, err)
	}
	return joined
}

var _ publicWSBackend = (*okxPublicWSBackend)(nil)
var _ perpWSBackend = (*okxPublicWSBackend)(nil)
