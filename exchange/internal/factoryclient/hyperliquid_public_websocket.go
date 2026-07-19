package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

type hyperliquidPublicWSBackend struct {
	meta           clientMeta
	base           *hyperliquid.WebsocketClient
	resolve        func(context.Context, string, string) (hyperliquidMarketMeta, error)
	candleSnapshot func(context.Context, string, string) (exchange.CandleEvent, bool, error)
	lifecycle      *backendLifecycle
	cancel         context.CancelFunc

	connectMu sync.Mutex
	connected bool
	closed    bool
	closeOnce sync.Once
}

type hyperliquidSpotWSBackend struct {
	*hyperliquidPublicWSBackend
}

type hyperliquidPerpWSBackend struct {
	*hyperliquidPublicWSBackend
}

func newHyperliquidSpotWSBackend(
	rest *hyperliquidSpotClient,
	privateKey string,
	settings Settings,
) *hyperliquidSpotWSBackend {
	ctx, cancel := context.WithCancel(context.Background())
	base := hyperliquid.NewWebsocketClient(ctx).WithCredentials(privateKey, nil)
	if settings.Environment == "testnet" {
		base.WithEnvironment(hyperliquid.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquid.EnvironmentMainnet)
	}
	if settings.WebSocketEndpoint != "" {
		base.WithURL(settings.WebSocketEndpoint)
	}
	return newHyperliquidSpotWSBackendWithClient(
		rest,
		hyperliquidspot.NewWebsocketClient(base),
		base,
		cancel,
	)
}

func newHyperliquidSpotWSBackendWithClient(
	rest *hyperliquidSpotClient,
	_ *hyperliquidspot.WebsocketClient,
	base *hyperliquid.WebsocketClient,
	cancel context.CancelFunc,
) *hyperliquidSpotWSBackend {
	backend := &hyperliquidPublicWSBackend{
		meta:      rest.meta,
		base:      base,
		resolve:   rest.spotMeta,
		lifecycle: newBackendLifecycle(),
		cancel:    cancel,
	}
	if rest.sdk != nil {
		backend.candleSnapshot = func(ctx context.Context, instrument, interval string) (exchange.CandleEvent, bool, error) {
			return hyperliquidLatestCandleSnapshot(ctx, instrument, interval, rest.Candles)
		}
	}
	backend.installReconnectHooks()
	return &hyperliquidSpotWSBackend{hyperliquidPublicWSBackend: backend}
}

func newHyperliquidPerpWSBackend(
	rest *hyperliquidPerpClient,
	privateKey string,
	settings Settings,
) *hyperliquidPerpWSBackend {
	ctx, cancel := context.WithCancel(context.Background())
	base := hyperliquid.NewWebsocketClient(ctx).WithCredentials(privateKey, nil)
	if settings.Environment == "testnet" {
		base.WithEnvironment(hyperliquid.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquid.EnvironmentMainnet)
	}
	if settings.WebSocketEndpoint != "" {
		base.WithURL(settings.WebSocketEndpoint)
	}
	return newHyperliquidPerpWSBackendWithClient(
		rest,
		hyperliquidperp.NewWebsocketClient(base),
		base,
		cancel,
	)
}

func newHyperliquidPerpWSBackendWithClient(
	rest *hyperliquidPerpClient,
	_ *hyperliquidperp.WebsocketClient,
	base *hyperliquid.WebsocketClient,
	cancel context.CancelFunc,
) *hyperliquidPerpWSBackend {
	backend := &hyperliquidPublicWSBackend{
		meta:      rest.meta,
		base:      base,
		resolve:   rest.perpMeta,
		lifecycle: newBackendLifecycle(),
		cancel:    cancel,
	}
	if rest.sdk != nil {
		backend.candleSnapshot = func(ctx context.Context, instrument, interval string) (exchange.CandleEvent, bool, error) {
			return hyperliquidLatestCandleSnapshot(ctx, instrument, interval, rest.Candles)
		}
	}
	backend.installReconnectHooks()
	return &hyperliquidPerpWSBackend{hyperliquidPublicWSBackend: backend}
}

func (backend *hyperliquidPublicWSBackend) installReconnectHooks() {
	backend.base.SetReconnectHooks(
		backend.lifecycle.Started,
		func() {
			backend.lifecycle.Recovered("subscriptions confirmed and order book resynchronization scheduled")
		},
	)
}

func (backend *hyperliquidPublicWSBackend) ensureConnected(operation string) error {
	backend.connectMu.Lock()
	defer backend.connectMu.Unlock()
	if backend.closed {
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket backend is closed")
	}
	if backend.connected {
		return nil
	}
	if err := backend.base.Connect(); err != nil {
		return websocketError(backend.meta, operation, exchange.KindTransport, fmt.Sprintf("websocket connect failed: %v", err))
	}
	backend.connected = true
	return nil
}

func (backend *hyperliquidPublicWSBackend) StartOrderBook(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BookEvent],
) (func() error, error) {
	const operation = "WatchOrderBook"
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	subscription := map[string]string{"type": "l2Book", "coin": meta.nativeCoin}
	var resyncNext atomic.Bool
	removeLifecycle := backend.lifecycle.Register(
		"book:"+instrument,
		callbacks.Status,
		func() { resyncNext.Store(true) },
	)
	err = backend.base.SubscribeConfirmed("l2Book", subscription, func(message hyperliquid.WsMessage) {
		var raw hyperliquid.WsL2Book
		if err := json.Unmarshal(message.Data, &raw); err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, "invalid order book payload"))
			return
		}
		event, err := hyperliquidBookEvent(meta, raw)
		if err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
			return
		}
		event.Resync = resyncNext.Swap(false)
		callbacks.Event(event)
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
	}
	return backend.stop("l2Book", subscription, operation, removeLifecycle), nil
}

func (backend *hyperliquidPublicWSBackend) StartBBO(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.BBOEvent],
) (func() error, error) {
	const operation = "WatchBBO"
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	subscription := map[string]string{"type": "l2Book", "coin": meta.nativeCoin}
	removeLifecycle := backend.lifecycle.Register("bbo:"+instrument, callbacks.Status, nil)
	err = backend.base.SubscribeConfirmed("l2Book", subscription, func(message hyperliquid.WsMessage) {
		var raw hyperliquid.WsL2Book
		if err := json.Unmarshal(message.Data, &raw); err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, "invalid BBO payload"))
			return
		}
		event, err := hyperliquidBBOFromBookEvent(meta, raw)
		if err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
	}
	return backend.stop("l2Book", subscription, operation, removeLifecycle), nil
}

func (backend *hyperliquidPublicWSBackend) StartPublicTrades(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.PublicTradeEvent],
) (func() error, error) {
	const operation = "WatchPublicTrades"
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	subscription := map[string]string{"type": "trades", "coin": meta.nativeCoin}
	removeLifecycle := backend.lifecycle.Register("trades:"+instrument, callbacks.Status, nil)
	err = backend.base.SubscribeConfirmed("trades", subscription, func(message hyperliquid.WsMessage) {
		var rows []hyperliquid.WsTrade
		if err := json.Unmarshal(message.Data, &rows); err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, "invalid public trades payload"))
			return
		}
		for _, row := range rows {
			event, err := hyperliquidTradeEvent(meta, row)
			if err != nil {
				callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
				return
			}
			callbacks.Event(event)
		}
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
	}
	return backend.stop("trades", subscription, operation, removeLifecycle), nil
}

func (backend *hyperliquidPublicWSBackend) StartCandles(
	ctx context.Context,
	instrument string,
	interval string,
	callbacks streamCallbacks[exchange.CandleEvent],
) (func() error, error) {
	const operation = "WatchCandles"
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	var snapshot exchange.CandleEvent
	var hasSnapshot bool
	if backend.candleSnapshot != nil {
		snapshot, hasSnapshot, _ = backend.candleSnapshot(ctx, instrument, interval)
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	subscription := map[string]string{
		"type":     "candle",
		"coin":     meta.nativeCoin,
		"interval": interval,
	}
	removeLifecycle := backend.lifecycle.Register(
		"candles:"+instrument+":"+interval,
		callbacks.Status,
		nil,
	)
	var eventMu sync.Mutex
	snapshotPending := hasSnapshot
	pendingEvents := make([]exchange.CandleEvent, 0, 1)
	err = backend.base.SubscribeConfirmed("candle", subscription, func(message hyperliquid.WsMessage) {
		var raw hyperliquid.WsCandle
		if err := json.Unmarshal(message.Data, &raw); err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, "invalid candle payload"))
			return
		}
		event, err := hyperliquidCandleEvent(meta, interval, raw, time.Now().UTC())
		if err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
			return
		}
		eventMu.Lock()
		if snapshotPending {
			pendingEvents = append(pendingEvents, event)
			eventMu.Unlock()
			return
		}
		eventMu.Unlock()
		callbacks.Event(event)
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
	}
	if hasSnapshot {
		eventMu.Lock()
		callbacks.Event(snapshot)
		for _, event := range pendingEvents {
			callbacks.Event(event)
		}
		pendingEvents = nil
		snapshotPending = false
		eventMu.Unlock()
	}
	return backend.stop("candle", subscription, operation, removeLifecycle), nil
}

func hyperliquidLatestCandleSnapshot(
	ctx context.Context,
	instrument string,
	interval string,
	load func(context.Context, exchange.CandlesRequest) (exchange.CandlePage, error),
) (exchange.CandleEvent, bool, error) {
	duration, err := hyperliquidCandleDuration(interval)
	if err != nil {
		return exchange.CandleEvent{}, false, err
	}
	end := time.Now().UTC()
	window := 6 * time.Hour
	if intervalWindow := 3 * duration; intervalWindow > window {
		window = intervalWindow
	}
	request := exchange.CandlesRequest{
		Instrument: instrument,
		Interval:   interval,
		Start:      end.Add(-window),
		End:        end,
	}
	page, err := load(ctx, request)
	if err != nil {
		return exchange.CandleEvent{}, false, err
	}
	if len(page.Candles) == 0 {
		request.Start = time.Time{}
		page, err = load(ctx, request)
		if err != nil {
			return exchange.CandleEvent{}, false, err
		}
	}
	if len(page.Candles) == 0 {
		return exchange.CandleEvent{}, false, nil
	}
	return exchange.CandleEvent{
		Instrument: instrument,
		Interval:   interval,
		Candle:     page.Candles[len(page.Candles)-1],
	}, true, nil
}

func hyperliquidCandleDuration(interval string) (time.Duration, error) {
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

func (backend *hyperliquidPerpWSBackend) StartReference(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[perpReferenceEvent],
) (func() error, error) {
	const operation = "WatchPerpReference"
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	subscription := map[string]string{"type": "activeAssetCtx", "coin": meta.nativeCoin}
	removeLifecycle := backend.lifecycle.Register("reference:"+instrument, callbacks.Status, nil)
	err = backend.base.SubscribeConfirmed("activeAssetCtx", subscription, func(message hyperliquid.WsMessage) {
		var raw hyperliquid.WsActiveAssetCtx
		if err := json.Unmarshal(message.Data, &raw); err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, "invalid reference payload"))
			return
		}
		event, err := hyperliquidReferenceEvent(meta, raw, time.Now().UTC())
		if err != nil {
			callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		removeLifecycle()
		return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
	}
	return backend.stop("activeAssetCtx", subscription, operation, removeLifecycle), nil
}

func (backend *hyperliquidPublicWSBackend) stop(
	channel string,
	subscription any,
	operation string,
	removeLifecycle func(),
) func() error {
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			removeLifecycle()
			if err := backend.base.Unsubscribe(channel, subscription); err != nil {
				stopErr = websocketError(backend.meta, operation, exchange.KindTransport, "websocket unsubscribe outcome is unknown")
			}
		})
		return stopErr
	}
}

func (backend *hyperliquidPublicWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		if backend.cancel != nil {
			backend.cancel()
		}
		backend.connectMu.Lock()
		backend.closed = true
		backend.connectMu.Unlock()
		backend.base.Close()
	})
	return nil
}

func hyperliquidBookEvent(
	meta hyperliquidMarketMeta,
	raw hyperliquid.WsL2Book,
) (exchange.BookEvent, error) {
	if raw.Coin != meta.nativeCoin {
		return exchange.BookEvent{}, errors.New("order book product identity mismatch")
	}
	if raw.Time <= 0 || len(raw.Levels) != 2 {
		return exchange.BookEvent{}, errors.New("invalid order book shape")
	}
	bids, err := hyperliquidWSLevels(raw.Levels[0])
	if err != nil {
		return exchange.BookEvent{}, fmt.Errorf("invalid bid levels: %w", err)
	}
	asks, err := hyperliquidWSLevels(raw.Levels[1])
	if err != nil {
		return exchange.BookEvent{}, fmt.Errorf("invalid ask levels: %w", err)
	}
	return exchange.BookEvent{
		Kind:       exchange.EventSnapshot,
		Instrument: meta.instrument.Symbol,
		Bids:       bids,
		Asks:       asks,
		Time:       time.UnixMilli(raw.Time).UTC(),
	}, nil
}

func hyperliquidWSLevels(rows []hyperliquid.WsLevel) ([]exchange.BookLevel, error) {
	levels := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		price, err := positiveWebSocketDecimal(row.Px)
		if err != nil {
			return nil, errors.New("invalid price")
		}
		quantity, err := positiveWebSocketDecimal(row.Sz)
		if err != nil {
			return nil, errors.New("invalid quantity")
		}
		levels = append(levels, exchange.BookLevel{Price: price, Quantity: quantity})
	}
	return levels, nil
}

func hyperliquidBBOEvent(
	meta hyperliquidMarketMeta,
	raw hyperliquid.WsBbo,
) (exchange.BBOEvent, error) {
	if raw.Coin != meta.nativeCoin {
		return exchange.BBOEvent{}, errors.New("BBO product identity mismatch")
	}
	if raw.Time <= 0 || len(raw.Bbo) != 2 {
		return exchange.BBOEvent{}, errors.New("invalid BBO shape")
	}
	levels, err := hyperliquidWSLevels(raw.Bbo)
	if err != nil {
		return exchange.BBOEvent{}, err
	}
	return exchange.BBOEvent{
		Instrument: meta.instrument.Symbol,
		Bid:        levels[0],
		Ask:        levels[1],
		Time:       time.UnixMilli(raw.Time).UTC(),
	}, nil
}

func hyperliquidBBOFromBookEvent(
	meta hyperliquidMarketMeta,
	raw hyperliquid.WsL2Book,
) (exchange.BBOEvent, error) {
	book, err := hyperliquidBookEvent(meta, raw)
	if err != nil {
		return exchange.BBOEvent{}, err
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		return exchange.BBOEvent{}, errors.New("BBO requires a two-sided order book")
	}
	return exchange.BBOEvent{
		Instrument: book.Instrument,
		Bid:        book.Bids[0],
		Ask:        book.Asks[0],
		Time:       book.Time,
	}, nil
}

func hyperliquidTradeEvent(
	meta hyperliquidMarketMeta,
	raw hyperliquid.WsTrade,
) (exchange.PublicTradeEvent, error) {
	if raw.Coin != meta.nativeCoin {
		return exchange.PublicTradeEvent{}, errors.New("public trade product identity mismatch")
	}
	if raw.Time <= 0 || raw.Tid <= 0 {
		return exchange.PublicTradeEvent{}, errors.New("invalid public trade identity")
	}
	price, err := positiveWebSocketDecimal(raw.Px)
	if err != nil {
		return exchange.PublicTradeEvent{}, errors.New("invalid public trade price")
	}
	quantity, err := positiveWebSocketDecimal(raw.Sz)
	if err != nil {
		return exchange.PublicTradeEvent{}, errors.New("invalid public trade quantity")
	}
	var side exchange.Side
	switch raw.Side {
	case "B":
		side = exchange.SideBuy
	case "A":
		side = exchange.SideSell
	default:
		return exchange.PublicTradeEvent{}, errors.New("invalid public trade side")
	}
	return exchange.PublicTradeEvent{
		Instrument: meta.instrument.Symbol,
		TradeID:    strconv.FormatInt(raw.Tid, 10),
		Side:       side,
		Price:      price,
		Quantity:   quantity,
		Time:       time.UnixMilli(raw.Time).UTC(),
	}, nil
}

func hyperliquidReferenceEvent(
	meta hyperliquidMarketMeta,
	raw hyperliquid.WsActiveAssetCtx,
	receivedAt time.Time,
) (perpReferenceEvent, error) {
	if raw.Coin != meta.nativeCoin {
		return perpReferenceEvent{}, errors.New("reference product identity mismatch")
	}
	mark, err := positiveWebSocketDecimal(raw.Ctx.MarkPx)
	if err != nil {
		return perpReferenceEvent{}, errors.New("invalid mark price")
	}
	funding, err := decimal.NewFromString(raw.Ctx.Funding)
	if err != nil {
		return perpReferenceEvent{}, errors.New("invalid funding rate")
	}
	receivedAt = receivedAt.UTC()
	return perpReferenceEvent{
		MarkPrice: exchange.MarkPriceEvent{
			Instrument: meta.instrument.Symbol,
			Price:      mark,
			Time:       receivedAt,
		},
		MarkValid: true,
		FundingRate: exchange.FundingRateEvent{
			Instrument:  meta.instrument.Symbol,
			Rate:        funding,
			EffectiveAt: receivedAt,
		},
		FundingValid: true,
	}, nil
}

func hyperliquidCandleEvent(
	meta hyperliquidMarketMeta,
	interval string,
	raw hyperliquid.WsCandle,
	receivedAt time.Time,
) (exchange.CandleEvent, error) {
	if raw.S != meta.nativeCoin || raw.I != interval {
		return exchange.CandleEvent{}, errors.New("candle product identity mismatch")
	}
	if raw.T <= 0 || raw.TClose <= raw.T {
		return exchange.CandleEvent{}, errors.New("invalid candle time range")
	}
	open, err := positiveWebSocketDecimal(raw.O)
	if err != nil {
		return exchange.CandleEvent{}, errors.New("invalid candle open")
	}
	high, err := positiveWebSocketDecimal(raw.H)
	if err != nil {
		return exchange.CandleEvent{}, errors.New("invalid candle high")
	}
	low, err := positiveWebSocketDecimal(raw.L)
	if err != nil {
		return exchange.CandleEvent{}, errors.New("invalid candle low")
	}
	closePrice, err := positiveWebSocketDecimal(raw.C)
	if err != nil {
		return exchange.CandleEvent{}, errors.New("invalid candle close")
	}
	volume, err := nonNegativeWebSocketDecimal(raw.V)
	if err != nil {
		return exchange.CandleEvent{}, errors.New("invalid candle volume")
	}
	if high.LessThan(low) ||
		open.GreaterThan(high) || open.LessThan(low) ||
		closePrice.GreaterThan(high) || closePrice.LessThan(low) {
		return exchange.CandleEvent{}, errors.New("inconsistent candle prices")
	}
	openTime := time.UnixMilli(raw.T).UTC()
	closeTime := time.UnixMilli(raw.TClose).UTC()
	return exchange.CandleEvent{
		Instrument: meta.instrument.Symbol,
		Interval:   interval,
		Candle: exchange.Candle{
			OpenTime:  openTime,
			CloseTime: closeTime,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closePrice,
			Volume:    volume,
			Complete:  !receivedAt.Before(closeTime),
		},
	}, nil
}

func positiveWebSocketDecimal(value string) (decimal.Decimal, error) {
	parsed, err := decimal.NewFromString(value)
	if err != nil || !parsed.IsPositive() {
		return decimal.Zero, errors.New("value must be a positive decimal")
	}
	return parsed, nil
}

func nonNegativeWebSocketDecimal(value string) (decimal.Decimal, error) {
	parsed, err := decimal.NewFromString(value)
	if err != nil || parsed.IsNegative() {
		return decimal.Zero, errors.New("value must be a non-negative decimal")
	}
	return parsed, nil
}

func hyperliquidWebSocketMalformed(meta clientMeta, operation, message string) error {
	return websocketError(meta, operation, exchange.KindMalformedResponse, message)
}
