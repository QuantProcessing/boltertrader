package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type lighterPublicWSClient interface {
	Connect() error
	Close()
	SetErrorHandler(func(error))
	SetReconnectHooks(func(error), func())
	SetSubscriptionAuthProvider(func(string) (*string, error))
	SubscribeOrderBook(int, func([]byte)) error
	UnsubscribeOrderBook(int) error
	SubscribeTicker(int, func([]byte)) error
	UnsubscribeTicker(int) error
	SubscribeTrades(int, func([]byte)) error
	UnsubscribeTrades(int) error
	SubscribeMarketStats(int, func([]byte)) error
	UnsubscribeMarketStats(int) error
	SubscribeCandle(int, string, func([]byte)) error
	UnsubscribeCandle(int, string) error
}

type lighterWSLifecycle struct {
	started   func(error)
	recovered func()
	report    func(error)
}

type lighterWSBackend struct {
	product    exchange.Product
	marketType string
	rest       *lighter.Client
	state      *lighterRESTState
	ws         lighterPublicWSClient

	connectMu sync.Mutex
	connected bool
	closed    bool

	lifecycleMu sync.Mutex
	lifecycles  map[string]lighterWSLifecycle

	closeOnce sync.Once
}

func newLighterSpotWSBackend(
	rest *lighter.Client,
	state *lighterRESTState,
	settings Settings,
) publicWSBackend {
	ws := lighter.NewWebsocketClientWithConfig(context.Background(), lighter.WSConfig{
		ReadOnly: true,
	})
	if settings.Environment == "testnet" {
		ws.WithEnvironment(lighter.EnvironmentTestnet)
	} else {
		ws.WithEnvironment(lighter.EnvironmentMainnet)
	}
	if settings.WebSocketEndpoint != "" {
		ws.WithURL(settings.WebSocketEndpoint)
	}
	return newLighterWSBackend(exchange.ProductSpot, lighterSpot, rest, state, ws)
}

func newLighterPerpWSBackend(
	rest *lighter.Client,
	state *lighterRESTState,
	settings Settings,
) perpWSBackend {
	ws := lighter.NewWebsocketClientWithConfig(context.Background(), lighter.WSConfig{
		ReadOnly: true,
	})
	if settings.Environment == "testnet" {
		ws.WithEnvironment(lighter.EnvironmentTestnet)
	} else {
		ws.WithEnvironment(lighter.EnvironmentMainnet)
	}
	if settings.WebSocketEndpoint != "" {
		ws.WithURL(settings.WebSocketEndpoint)
	}
	return newLighterWSBackend(exchange.ProductPerp, lighterPerp, rest, state, ws)
}

func newLighterWSBackend(
	product exchange.Product,
	marketType string,
	rest *lighter.Client,
	state *lighterRESTState,
	ws lighterPublicWSClient,
) *lighterWSBackend {
	backend := &lighterWSBackend{
		product:    product,
		marketType: marketType,
		rest:       rest,
		state:      state,
		ws:         ws,
		lifecycles: make(map[string]lighterWSLifecycle),
	}
	if ws != nil {
		ws.SetErrorHandler(backend.reportTransportError)
		ws.SetReconnectHooks(backend.reconnectStarted, backend.reconnectRecovered)
	}
	return backend
}

func (backend *lighterWSBackend) StartOrderBook(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BookEvent],
) (func() error, error) {
	const operation = "WatchOrderBook"
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}

	var (
		stateMu     sync.Mutex
		initialized bool
		resyncing   bool
		lastNonce   int64
		generation  uint64
		stopped     atomic.Bool
		ready       = make(chan struct{})
		readyOnce   sync.Once
		handler     func([]byte)
	)

	beginGap := func(reason string) bool {
		stateMu.Lock()
		if resyncing || stopped.Load() {
			stateMu.Unlock()
			return false
		}
		resyncing = true
		generation++
		currentGeneration := generation
		stateMu.Unlock()
		emitLighterStatus(callbacks.Status, backendStatus{
			State:      exchange.SubscriptionGap,
			Phase:      exchange.GapStarted,
			Generation: currentGeneration,
			Reason:     reason,
		})
		emitLighterStatus(callbacks.Status, backendStatus{
			State:      exchange.SubscriptionResyncing,
			Generation: currentGeneration,
			Reason:     reason,
		})
		return true
	}

	handler = func(payload []byte) {
		event, err := lighterBookEvent(payload, meta)
		if err != nil {
			emitLighterError(callbacks.Error, backend.malformed(operation))
			return
		}

		stateMu.Lock()
		if stopped.Load() {
			stateMu.Unlock()
			return
		}
		if !initialized || resyncing {
			wasResync := resyncing
			currentGeneration := generation
			event.Kind = exchange.EventSnapshot
			event.Resync = wasResync
			initialized = true
			resyncing = false
			lastNonce = eventNonce(event)
			stateMu.Unlock()
			emitLighterEvent(callbacks.Event, event)
			readyOnce.Do(func() { close(ready) })
			if wasResync {
				emitLighterStatus(callbacks.Status, backendStatus{
					State:      exchange.SubscriptionActive,
					Phase:      exchange.GapRecovered,
					Generation: currentGeneration,
				})
			}
			return
		}

		nonce := eventNonce(event)
		beginNonce := eventBeginNonce(event)
		if nonce <= lastNonce {
			stateMu.Unlock()
			return
		}
		if beginNonce != lastNonce {
			stateMu.Unlock()
			if !beginGap("Lighter order-book nonce gap") {
				return
			}
			go func() {
				if err := backend.ws.UnsubscribeOrderBook(meta.marketID); err != nil {
					emitLighterError(callbacks.Error, backend.transport(operation))
					return
				}
				if stopped.Load() {
					return
				}
				if err := backend.ws.SubscribeOrderBook(meta.marketID, handler); err != nil {
					emitLighterError(callbacks.Error, backend.transport(operation))
				}
			}()
			return
		}
		event.Kind = exchange.EventDelta
		event.Previous = strconv.FormatInt(lastNonce, 10)
		lastNonce = nonce
		stateMu.Unlock()
		emitLighterEvent(callbacks.Event, event)
	}

	key := lighterWSKey("order-book", meta.marketID)
	backend.addLifecycle(key, lighterWSLifecycle{
		started: func(cause error) {
			beginGap(lighterReconnectReason(cause))
		},
		report: callbacks.Error,
	})
	if err := backend.ws.SubscribeOrderBook(meta.marketID, handler); err != nil {
		backend.removeLifecycle(key)
		_ = backend.ws.UnsubscribeOrderBook(meta.marketID)
		return nil, backend.transport(operation)
	}
	stop := lighterStop(func() error {
		stopped.Store(true)
		backend.removeLifecycle(key)
		if err := backend.ws.UnsubscribeOrderBook(meta.marketID); err != nil {
			return backend.transport(operation)
		}
		return nil
	})
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func (backend *lighterWSBackend) StartBBO(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BBOEvent],
) (func() error, error) {
	const operation = "WatchBBO"
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	var stopped atomic.Bool
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	handler := func(payload []byte) {
		event, err := lighterBBOEvent(payload, meta)
		if err != nil {
			emitLighterError(callbacks.Error, backend.malformed(operation))
			return
		}
		if stopped.Load() {
			return
		}
		emitLighterEvent(callbacks.Event, event)
		recoveredOnValid()
		readyOnce.Do(func() { close(ready) })
	}
	key := lighterWSKey("bbo", meta.marketID)
	backend.addLifecycle(key, lifecycle)
	if err := backend.ws.SubscribeTicker(meta.marketID, handler); err != nil {
		backend.removeLifecycle(key)
		_ = backend.ws.UnsubscribeTicker(meta.marketID)
		return nil, backend.transport(operation)
	}
	stop := lighterStop(func() error {
		stopped.Store(true)
		backend.removeLifecycle(key)
		if err := backend.ws.UnsubscribeTicker(meta.marketID); err != nil {
			return backend.transport(operation)
		}
		return nil
	})
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func (backend *lighterWSBackend) StartPublicTrades(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.PublicTradeEvent],
) (func() error, error) {
	const operation = "WatchPublicTrades"
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	var stopped atomic.Bool
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	handler := func(payload []byte) {
		events, err := lighterPublicTradeEvents(payload, meta)
		if err != nil {
			emitLighterError(callbacks.Error, backend.malformed(operation))
			return
		}
		if stopped.Load() || len(events) == 0 {
			return
		}
		recoveredOnValid()
		readyOnce.Do(func() { close(ready) })
		for _, event := range events {
			emitLighterEvent(callbacks.Event, event)
		}
	}
	key := lighterWSKey("trades", meta.marketID)
	backend.addLifecycle(key, lifecycle)
	if err := backend.ws.SubscribeTrades(meta.marketID, handler); err != nil {
		backend.removeLifecycle(key)
		_ = backend.ws.UnsubscribeTrades(meta.marketID)
		return nil, backend.transport(operation)
	}
	stop := lighterStop(func() error {
		stopped.Store(true)
		backend.removeLifecycle(key)
		if err := backend.ws.UnsubscribeTrades(meta.marketID); err != nil {
			return backend.transport(operation)
		}
		return nil
	})
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func (backend *lighterWSBackend) StartCandles(
	ctx context.Context,
	instrument string,
	interval string,
	callbacks streamCallbacks[exchange.CandleEvent],
) (func() error, error) {
	const operation = "WatchCandles"
	duration, err := lighterWSCandleInterval(interval)
	if err != nil {
		return nil, websocketError(
			clientMeta{venue: exchange.VenueLighter, product: backend.product},
			operation,
			exchange.KindInvalidRequest,
			"unsupported Lighter candle interval",
		)
	}
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	var stopped atomic.Bool
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	handler := func(payload []byte) {
		events, err := lighterCandleEvents(payload, meta, interval, duration)
		if err != nil {
			emitLighterError(callbacks.Error, backend.malformed(operation))
			return
		}
		if stopped.Load() {
			return
		}
		for _, event := range events {
			emitLighterEvent(callbacks.Event, event)
		}
		recoveredOnValid()
		readyOnce.Do(func() { close(ready) })
	}
	key := lighterWSKey("candle/"+interval, meta.marketID)
	backend.addLifecycle(key, lifecycle)
	if err := backend.ws.SubscribeCandle(meta.marketID, interval, handler); err != nil {
		backend.removeLifecycle(key)
		_ = backend.ws.UnsubscribeCandle(meta.marketID, interval)
		return nil, backend.transport(operation)
	}
	stop := lighterStop(func() error {
		stopped.Store(true)
		backend.removeLifecycle(key)
		if err := backend.ws.UnsubscribeCandle(meta.marketID, interval); err != nil {
			return backend.transport(operation)
		}
		return nil
	})
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func (backend *lighterWSBackend) StartReference(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[perpReferenceEvent],
) (func() error, error) {
	const operation = "WatchPerpReference"
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	var stopped atomic.Bool
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	handler := func(payload []byte) {
		event, err := lighterReferenceEvent(payload, meta)
		if err != nil {
			emitLighterError(callbacks.Error, backend.malformed(operation))
			return
		}
		if stopped.Load() {
			return
		}
		emitLighterEvent(callbacks.Event, event)
		recoveredOnValid()
		readyOnce.Do(func() { close(ready) })
	}
	key := lighterWSKey("reference", meta.marketID)
	backend.addLifecycle(key, lifecycle)
	if err := backend.ws.SubscribeMarketStats(meta.marketID, handler); err != nil {
		backend.removeLifecycle(key)
		_ = backend.ws.UnsubscribeMarketStats(meta.marketID)
		return nil, backend.transport(operation)
	}
	stop := lighterStop(func() error {
		stopped.Store(true)
		backend.removeLifecycle(key)
		if err := backend.ws.UnsubscribeMarketStats(meta.marketID); err != nil {
			return backend.transport(operation)
		}
		return nil
	})
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func (backend *lighterWSBackend) resolveMeta(
	ctx context.Context,
	operation string,
	instrument string,
) (lighterMarketMeta, error) {
	return lighterMeta(ctx, backend.rest, backend.state, operation, backend.product, backend.marketType, instrument)
}

func (backend *lighterWSBackend) ensureConnected(operation string) error {
	backend.connectMu.Lock()
	defer backend.connectMu.Unlock()
	if backend.closed {
		return websocketError(
			clientMeta{venue: exchange.VenueLighter, product: backend.product},
			operation,
			exchange.KindSubscriptionClosed,
			"websocket backend is closed",
		)
	}
	if backend.connected {
		return nil
	}
	if backend.ws == nil {
		return websocketError(
			clientMeta{venue: exchange.VenueLighter, product: backend.product},
			operation,
			exchange.KindInvalidConfig,
			"Lighter websocket client is not configured",
		)
	}
	if err := backend.ws.Connect(); err != nil {
		return backend.transport(operation)
	}
	backend.connected = true
	return nil
}

func (backend *lighterWSBackend) waitFirst(
	ctx context.Context,
	operation string,
	ready <-chan struct{},
	stop func() error,
) error {
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		_ = stop()
		return websocketContextError(
			clientMeta{venue: exchange.VenueLighter, product: backend.product},
			operation,
			ctx.Err(),
		)
	}
}

func (backend *lighterWSBackend) addLifecycle(key string, lifecycle lighterWSLifecycle) {
	backend.lifecycleMu.Lock()
	backend.lifecycles[key] = lifecycle
	backend.lifecycleMu.Unlock()
}

func (backend *lighterWSBackend) removeLifecycle(key string) {
	backend.lifecycleMu.Lock()
	delete(backend.lifecycles, key)
	backend.lifecycleMu.Unlock()
}

func (backend *lighterWSBackend) lifecycleSnapshot() []lighterWSLifecycle {
	backend.lifecycleMu.Lock()
	defer backend.lifecycleMu.Unlock()
	out := make([]lighterWSLifecycle, 0, len(backend.lifecycles))
	for _, lifecycle := range backend.lifecycles {
		out = append(out, lifecycle)
	}
	return out
}

func (backend *lighterWSBackend) reportTransportError(_ error) {
	err := backend.transport("WebSocket")
	for _, lifecycle := range backend.lifecycleSnapshot() {
		emitLighterError(lifecycle.report, err)
	}
}

func (backend *lighterWSBackend) reconnectStarted(cause error) {
	for _, lifecycle := range backend.lifecycleSnapshot() {
		if lifecycle.started != nil {
			lifecycle.started(cause)
		}
	}
}

func (backend *lighterWSBackend) reconnectRecovered() {
	for _, lifecycle := range backend.lifecycleSnapshot() {
		if lifecycle.recovered != nil {
			lifecycle.recovered()
		}
	}
}

func (backend *lighterWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		if backend.ws != nil {
			// Close cancels the SDK dial/read context before taking its own
			// connection lock, so an in-flight Connect is interrupted promptly.
			backend.ws.Close()
		}
		backend.connectMu.Lock()
		backend.closed = true
		backend.connected = false
		backend.connectMu.Unlock()
	})
	return nil
}

func (backend *lighterWSBackend) transport(operation string) error {
	return websocketError(
		clientMeta{venue: exchange.VenueLighter, product: backend.product},
		operation,
		exchange.KindTransport,
		"Lighter websocket transport error",
	)
}

func (backend *lighterWSBackend) malformed(operation string) error {
	return websocketError(
		clientMeta{venue: exchange.VenueLighter, product: backend.product},
		operation,
		exchange.KindMalformedResponse,
		"Lighter websocket payload is malformed",
	)
}

func lighterBookEvent(payload []byte, meta lighterMarketMeta) (exchange.BookEvent, error) {
	var row lighter.WsOrderBookEvent
	if err := json.Unmarshal(payload, &row); err != nil {
		return exchange.BookEvent{}, err
	}
	if !lighterWSChannel(row.Channel, "order_book", meta.marketID) ||
		row.OrderBook.Nonce <= 0 ||
		row.OrderBook.BeginNonce < 0 ||
		row.OrderBook.BeginNonce > row.OrderBook.Nonce {
		return exchange.BookEvent{}, errors.New("invalid order-book identity")
	}
	bids, err := lighterWSBookLevels(row.OrderBook.Bids)
	if err != nil {
		return exchange.BookEvent{}, err
	}
	asks, err := lighterWSBookLevels(row.OrderBook.Asks)
	if err != nil {
		return exchange.BookEvent{}, err
	}
	timestamp := firstPositive(row.OrderBook.LastUpdatedAt, row.LastUpdatedAt, row.Timestamp)
	if timestamp <= 0 {
		return exchange.BookEvent{}, errors.New("invalid order-book timestamp")
	}
	return exchange.BookEvent{
		Instrument: meta.instrument.Symbol,
		Sequence:   strconv.FormatInt(row.OrderBook.Nonce, 10),
		Previous:   strconv.FormatInt(row.OrderBook.BeginNonce, 10),
		Bids:       bids,
		Asks:       asks,
		Time:       lighterFlexibleUnix(timestamp),
	}, nil
}

func lighterBBOEvent(payload []byte, meta lighterMarketMeta) (exchange.BBOEvent, error) {
	var row lighter.WsTickerEvent
	if err := json.Unmarshal(payload, &row); err != nil {
		return exchange.BBOEvent{}, err
	}
	if !lighterWSChannel(row.Channel, "ticker", meta.marketID) {
		return exchange.BBOEvent{}, errors.New("invalid ticker identity")
	}
	bid, err := lighterWSBookLevel(row.Ticker.B, false)
	if err != nil {
		return exchange.BBOEvent{}, err
	}
	ask, err := lighterWSBookLevel(row.Ticker.A, false)
	if err != nil {
		return exchange.BBOEvent{}, err
	}
	timestamp := firstPositive(row.LastUpdatedAt, row.Timestamp)
	if timestamp <= 0 {
		return exchange.BBOEvent{}, errors.New("invalid ticker timestamp")
	}
	return exchange.BBOEvent{
		Instrument: meta.instrument.Symbol,
		Bid:        bid,
		Ask:        ask,
		Time:       lighterFlexibleUnix(timestamp),
	}, nil
}

func lighterPublicTradeEvents(payload []byte, meta lighterMarketMeta) ([]exchange.PublicTradeEvent, error) {
	var row lighter.WsTradeEvent
	if err := json.Unmarshal(payload, &row); err != nil {
		return nil, err
	}
	if !lighterWSChannel(row.Channel, "trade", meta.marketID) || row.Nonce <= 0 {
		return nil, errors.New("invalid trade stream identity")
	}
	trades := make([]lighter.Trade, 0, len(row.Trades)+len(row.LiquidationTrades))
	trades = append(trades, row.Trades...)
	trades = append(trades, row.LiquidationTrades...)
	events := make([]exchange.PublicTradeEvent, 0, len(trades))
	for _, trade := range trades {
		if trade.MarketId != meta.marketID {
			return nil, errors.New("mixed trade market")
		}
		tradeID := strings.TrimSpace(trade.TradeIdStr)
		if tradeID == "" && trade.TradeId > 0 {
			tradeID = strconv.FormatInt(trade.TradeId, 10)
		}
		if tradeID == "" {
			return nil, errors.New("invalid trade id")
		}
		price, err := lighterPositiveDecimal(trade.Price)
		if err != nil {
			return nil, err
		}
		quantity, err := lighterPositiveDecimal(trade.Size)
		if err != nil {
			return nil, err
		}
		timestamp := firstPositive(trade.Timestamp, trade.TransactionTime)
		if timestamp <= 0 {
			return nil, errors.New("invalid trade timestamp")
		}
		side := exchange.SideSell
		if trade.IsMakerAsk {
			side = exchange.SideBuy
		}
		events = append(events, exchange.PublicTradeEvent{
			Instrument: meta.instrument.Symbol,
			TradeID:    tradeID,
			Side:       side,
			Price:      price,
			Quantity:   quantity,
			Time:       lighterFlexibleUnix(timestamp),
		})
	}
	return events, nil
}

func lighterReferenceEvent(payload []byte, meta lighterMarketMeta) (perpReferenceEvent, error) {
	var row lighter.WsMarketStatsEvent
	if err := json.Unmarshal(payload, &row); err != nil {
		return perpReferenceEvent{}, err
	}
	if !lighterWSChannel(row.Channel, "market_stats", meta.marketID) ||
		row.MarketStats.MarketId != meta.marketID ||
		row.Timestamp <= 0 {
		return perpReferenceEvent{}, errors.New("invalid market stats identity")
	}
	mark, err := lighterPositiveDecimal(row.MarketStats.MarkPrice)
	if err != nil {
		return perpReferenceEvent{}, err
	}
	fundingRaw := strings.TrimSpace(row.MarketStats.CurrentFundingRate)
	if fundingRaw == "" {
		fundingRaw = strings.TrimSpace(row.MarketStats.FundingRate)
	}
	if fundingRaw == "" {
		return perpReferenceEvent{}, errors.New("missing funding rate")
	}
	funding, err := decimal.NewFromString(fundingRaw)
	if err != nil {
		return perpReferenceEvent{}, err
	}
	observedAt := lighterFlexibleUnix(row.Timestamp)
	return perpReferenceEvent{
		MarkPrice: exchange.MarkPriceEvent{
			Instrument: meta.instrument.Symbol,
			Price:      mark,
			Time:       observedAt,
		},
		MarkValid: true,
		FundingRate: exchange.FundingRateEvent{
			Instrument:  meta.instrument.Symbol,
			Rate:        funding,
			EffectiveAt: observedAt,
		},
		FundingValid: true,
	}, nil
}

func lighterCandleEvents(
	payload []byte,
	meta lighterMarketMeta,
	interval string,
	duration time.Duration,
) ([]exchange.CandleEvent, error) {
	var row lighter.WsCandleEvent
	if err := json.Unmarshal(payload, &row); err != nil {
		return nil, err
	}
	if !lighterWSCandleChannel(row.Channel, meta.marketID, interval) ||
		row.Timestamp <= 0 ||
		len(row.Candles) == 0 ||
		len(row.Candles) > 2 {
		return nil, errors.New("invalid candle stream identity")
	}
	observedAt := time.UnixMilli(row.Timestamp).UTC()
	intervalMillis := duration.Milliseconds()
	events := make([]exchange.CandleEvent, 0, len(row.Candles))
	var previousOpen int64
	for _, candle := range row.Candles {
		if candle.OpenTime <= 0 ||
			candle.OpenTime%intervalMillis != 0 ||
			(previousOpen != 0 && candle.OpenTime != previousOpen+intervalMillis) ||
			candle.LastTradeID <= 0 {
			return nil, errors.New("invalid candle sequence")
		}
		open, err := lighterWSCandleDecimal(candle.Open, true)
		if err != nil {
			return nil, err
		}
		high, err := lighterWSCandleDecimal(candle.High, true)
		if err != nil {
			return nil, err
		}
		low, err := lighterWSCandleDecimal(candle.Low, true)
		if err != nil {
			return nil, err
		}
		closeValue, err := lighterWSCandleDecimal(candle.Close, true)
		if err != nil {
			return nil, err
		}
		volume, err := lighterWSCandleDecimal(candle.Volume, false)
		if err != nil {
			return nil, err
		}
		if low.GreaterThan(open) ||
			low.GreaterThan(closeValue) ||
			high.LessThan(open) ||
			high.LessThan(closeValue) ||
			low.GreaterThan(high) {
			return nil, errors.New("invalid candle OHLC")
		}
		openTime := time.UnixMilli(candle.OpenTime).UTC()
		closeTime := openTime.Add(duration)
		events = append(events, exchange.CandleEvent{
			Instrument: meta.instrument.Symbol,
			Interval:   interval,
			Candle: exchange.Candle{
				OpenTime:  openTime,
				CloseTime: closeTime,
				Open:      open,
				High:      high,
				Low:       low,
				Close:     closeValue,
				Volume:    volume,
				Complete:  !closeTime.After(observedAt),
			},
		})
		previousOpen = candle.OpenTime
	}
	return events, nil
}

func lighterWSCandleDecimal(raw json.Number, positive bool) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(raw.String())
	if err != nil || value.IsNegative() || (positive && !value.IsPositive()) {
		return decimal.Decimal{}, errors.New("invalid candle decimal")
	}
	return value, nil
}

func lighterWSCandleInterval(interval string) (time.Duration, error) {
	switch interval {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "30m":
		return 30 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "12h":
		return 12 * time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	default:
		return 0, errors.New("unsupported candle interval")
	}
}

func lighterWSBookLevels(rows []lighter.OrderBookLevel) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		level, err := lighterWSBookLevel(row, true)
		if err != nil {
			return nil, err
		}
		out = append(out, level)
	}
	return out, nil
}

func lighterWSBookLevel(row lighter.OrderBookLevel, allowZeroQuantity bool) (exchange.BookLevel, error) {
	price, err := lighterPositiveDecimal(row.Price)
	if err != nil {
		return exchange.BookLevel{}, err
	}
	quantity, err := lighterNonNegativeDecimal(row.Size)
	if err != nil || (!allowZeroQuantity && !quantity.IsPositive()) {
		return exchange.BookLevel{}, errors.New("invalid book quantity")
	}
	return exchange.BookLevel{Price: price, Quantity: quantity}, nil
}

func lighterWSChannel(channel, kind string, marketID int) bool {
	return strings.ReplaceAll(channel, ":", "/") == fmt.Sprintf("%s/%d", kind, marketID)
}

func lighterWSCandleChannel(channel string, marketID int, interval string) bool {
	return strings.ReplaceAll(channel, ":", "/") == fmt.Sprintf("candle/%d/%s", marketID, interval)
}

func lighterWSKey(kind string, marketID int) string {
	return fmt.Sprintf("%s/%d", kind, marketID)
}

func lighterReconnectReason(err error) string {
	if err == nil {
		return "Lighter websocket reconnecting"
	}
	return "Lighter websocket disconnected"
}

func lighterSimpleLifecycle[T any](callbacks streamCallbacks[T]) (lighterWSLifecycle, func()) {
	var mu sync.Mutex
	var generation uint64
	var reconnecting bool
	lifecycle := lighterWSLifecycle{
		started: func(cause error) {
			mu.Lock()
			if reconnecting {
				mu.Unlock()
				return
			}
			reconnecting = true
			generation++
			currentGeneration := generation
			mu.Unlock()
			reason := lighterReconnectReason(cause)
			emitLighterStatus(callbacks.Status, backendStatus{
				State:      exchange.SubscriptionGap,
				Phase:      exchange.GapStarted,
				Generation: currentGeneration,
				Reason:     reason,
			})
			emitLighterStatus(callbacks.Status, backendStatus{
				State:      exchange.SubscriptionResyncing,
				Generation: currentGeneration,
				Reason:     reason,
			})
		},
		report: callbacks.Error,
	}
	recoveredOnValid := func() {
		mu.Lock()
		if !reconnecting {
			mu.Unlock()
			return
		}
		reconnecting = false
		currentGeneration := generation
		mu.Unlock()
		emitLighterStatus(callbacks.Status, backendStatus{
			State:      exchange.SubscriptionActive,
			Phase:      exchange.GapRecovered,
			Generation: currentGeneration,
		})
	}
	return lifecycle, recoveredOnValid
}

func lighterStop(stop func() error) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() {
			err = stop()
		})
		return err
	}
}

func emitLighterEvent[T any](emit func(T), event T) {
	if emit != nil {
		emit(event)
	}
}

func emitLighterStatus(emit func(backendStatus), status backendStatus) {
	if emit != nil {
		emit(status)
	}
}

func emitLighterError(emit func(error), err error) {
	if emit != nil && err != nil {
		emit(err)
	}
}

func eventNonce(event exchange.BookEvent) int64 {
	nonce, _ := strconv.ParseInt(event.Sequence, 10, 64)
	return nonce
}

func eventBeginNonce(event exchange.BookEvent) int64 {
	nonce, _ := strconv.ParseInt(event.Previous, 10, 64)
	return nonce
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

var (
	_ publicWSBackend = (*lighterWSBackend)(nil)
	_ perpWSBackend   = (*lighterWSBackend)(nil)
)
