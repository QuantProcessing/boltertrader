package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

type gateSpotREST interface {
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
	CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error)
}

type gatePerpREST interface {
	gateSpotREST
	gateFuturesUserID(context.Context) (string, error)
	gateContractMultiplier(string, exchange.Instrument) (decimal.Decimal, error)
}

type gateSpotWSBackend struct {
	ws        *gate.WSClient
	rest      gateSpotREST
	lifecycle *backendLifecycle
}

type gatePerpWSBackend struct {
	*gateSpotWSBackend
	rest     gatePerpREST
	userIDMu sync.Mutex
	userID   string
}

func newGateSpotWSBackend(ws *gate.WSClient, rest gateSpotREST) *gateSpotWSBackend {
	backend := &gateSpotWSBackend{ws: ws, rest: rest, lifecycle: newBackendLifecycle()}
	if ws != nil {
		ws.SetReconnectHooks(backend.lifecycle.Started, func() { backend.lifecycle.Recovered("gate private subscriptions restored") })
	}
	return backend
}

func newGatePerpWSBackend(ws *gate.WSClient, rest gatePerpREST) *gatePerpWSBackend {
	return &gatePerpWSBackend{gateSpotWSBackend: newGateSpotWSBackend(ws, rest), rest: rest}
}

func gateSpotOrderBookSubscription(instrument string) []string {
	return []string{instrument, "5", "100ms"}
}

func gatePerpOrderBookSubscription(instrument string) []string {
	return []string{instrument, "20", "0"}
}

func gatePerpPrivateInstrumentPayload(userID, instrument string) []string {
	return []string{userID, instrument}
}

func gatePerpPrivateAccountPayload(userID string) []string {
	return []string{userID}
}

func gatePerpPrivateAllPayload(userID string) []string {
	return []string{userID, "!all"}
}

func gateSpotOrderBookEvent(raw json.RawMessage, instrument exchange.Instrument) (exchange.BookEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	var message struct {
		Result struct {
			T            int64                 `json:"t"`
			S            string                `json:"s"`
			LastUpdateID int64                 `json:"lastUpdateId"`
			Bids         [][]gate.NumberString `json:"bids"`
			Asks         [][]gate.NumberString `json:"asks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return exchange.BookEvent{}, gateNormalizeErr(meta, "WatchOrderBook", err)
	}
	book := gate.OrderBook{Bids: message.Result.Bids, Asks: message.Result.Asks}
	if err := gateValidateSpotOrderBook(meta, "WatchOrderBook", book); err != nil {
		return exchange.BookEvent{}, err
	}
	if message.Result.S != instrument.Identity.NativeSymbol {
		return exchange.BookEvent{}, errGateForeignInstrument
	}
	return exchange.BookEvent{
		Kind:       exchange.EventSnapshot,
		Instrument: instrument,
		Sequence:   strconv.FormatInt(message.Result.LastUpdateID, 10),
		Bids:       gateBookLevels(message.Result.Bids),
		Asks:       gateBookLevels(message.Result.Asks),
		Time:       gateUnixMilli(message.Result.T),
	}, nil
}

func gatePerpOrderBookEvent(raw json.RawMessage, instrument exchange.Instrument, multiplier decimal.Decimal) (exchange.BookEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	var message struct {
		Result struct {
			T        int64                       `json:"t"`
			Contract string                      `json:"contract"`
			ID       int64                       `json:"id"`
			Bids     []gate.FuturesOrderBookItem `json:"bids"`
			Asks     []gate.FuturesOrderBookItem `json:"asks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return exchange.BookEvent{}, gateNormalizeErr(meta, "WatchOrderBook", err)
	}
	book := gate.FuturesOrderBook{Bids: message.Result.Bids, Asks: message.Result.Asks}
	if err := gateValidateFuturesOrderBook(meta, "WatchOrderBook", book); err != nil {
		return exchange.BookEvent{}, err
	}
	if message.Result.Contract != instrument.Identity.NativeSymbol {
		return exchange.BookEvent{}, errGateForeignInstrument
	}
	return exchange.BookEvent{
		Kind:       exchange.EventSnapshot,
		Instrument: instrument,
		Sequence:   strconv.FormatInt(message.Result.ID, 10),
		Bids:       gateFuturesBookLevels(message.Result.Bids, multiplier),
		Asks:       gateFuturesBookLevels(message.Result.Asks, multiplier),
		Time:       gateUnixMilli(message.Result.T),
	}, nil
}

type gateWSCandlestick struct {
	T gate.NumberString `json:"t"`
	V gate.NumberString `json:"v"`
	C gate.NumberString `json:"c"`
	H gate.NumberString `json:"h"`
	L gate.NumberString `json:"l"`
	O gate.NumberString `json:"o"`
	N string            `json:"n"`
	W bool              `json:"w"`
}

func gateSpotCandleEvent(raw json.RawMessage, instrument exchange.Instrument, interval string) (exchange.CandleEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	var message struct {
		Result gateWSCandlestick `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return exchange.CandleEvent{}, gateNormalizeErr(meta, "WatchCandles", err)
	}
	return gateWSCandleEvent(meta, message.Result, instrument, interval)
}

func gatePerpCandleEvents(raw json.RawMessage, instrument exchange.Instrument, interval string) ([]exchange.CandleEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	var message struct {
		Result []gateWSCandlestick `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, gateNormalizeErr(meta, "WatchCandles", err)
	}
	events := make([]exchange.CandleEvent, 0, len(message.Result))
	for _, row := range message.Result {
		event, err := gateWSCandleEvent(meta, row, instrument, interval)
		if errors.Is(err, errGateForeignInstrument) {
			continue
		}
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 && len(message.Result) > 0 {
		return nil, errGateForeignInstrument
	}
	if len(events) == 0 {
		return nil, gateMalformed(meta, "WatchCandles", "gate candle update is empty")
	}
	return events, nil
}

func gateWSCandleEvent(meta clientMeta, row gateWSCandlestick, instrument exchange.Instrument, interval string) (exchange.CandleEvent, error) {
	if gateWSCandleInstrument(row.N, interval) != instrument.Identity.NativeSymbol {
		return exchange.CandleEvent{}, errGateForeignInstrument
	}
	candlestick := gate.Candlestick{row.T, row.V, row.C, row.H, row.L, row.O}
	if err := gateValidateCandles(meta, "WatchCandles", []gate.Candlestick{candlestick}); err != nil {
		return exchange.CandleEvent{}, err
	}
	duration, err := gateIntervalDuration(interval)
	if err != nil {
		return exchange.CandleEvent{}, gateMalformed(meta, "WatchCandles", err.Error())
	}
	page := gateCandlePage(
		[]gate.Candlestick{candlestick},
		exchange.CandlesRequest{Instrument: instrument, Interval: interval, Limit: 1},
		duration,
	)
	if len(page.Candles) != 1 {
		return exchange.CandleEvent{}, gateMalformed(meta, "WatchCandles", "gate candle update is empty")
	}
	candle := page.Candles[0]
	candle.Complete = row.W
	return exchange.CandleEvent{
		Instrument: instrument,
		Interval:   interval,
		Candle:     candle,
	}, nil
}

func gateWSCandleInstrument(name, interval string) string {
	prefix := interval + "_"
	if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
		return name[len(prefix):]
	}
	return ""
}

func (backend *gateSpotWSBackend) StartOrderBook(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	first := true
	return backend.subscribe(ctx, gate.ChannelSpotOrderBook, gateSpotOrderBookSubscription(native), func(raw json.RawMessage) {
		event, err := gateSpotOrderBookEvent(raw, instrument)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		if !first {
			event.Kind = exchange.EventFull
		}
		first = false
		callbacks.Event(event)
	})
}

func (backend *gateSpotWSBackend) StartBBO(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	return backend.StartOrderBook(ctx, instrument, streamCallbacks[exchange.BookEvent]{
		Event: func(event exchange.BookEvent) {
			var bbo exchange.BBOEvent
			bbo.Instrument = event.Instrument
			bbo.Time = event.Time
			if len(event.Bids) > 0 {
				bbo.Bid = event.Bids[0]
			}
			if len(event.Asks) > 0 {
				bbo.Ask = event.Asks[0]
			}
			callbacks.Event(bbo)
		},
		Status: callbacks.Status,
		Error:  callbacks.Error,
	})
}

func (backend *gatePerpWSBackend) StartBBO(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	return backend.StartOrderBook(ctx, instrument, streamCallbacks[exchange.BookEvent]{
		Event: func(event exchange.BookEvent) {
			var bbo exchange.BBOEvent
			bbo.Instrument = event.Instrument
			bbo.Time = event.Time
			if len(event.Bids) > 0 {
				bbo.Bid = event.Bids[0]
			}
			if len(event.Asks) > 0 {
				bbo.Ask = event.Asks[0]
			}
			callbacks.Event(bbo)
		},
		Status: callbacks.Status,
		Error:  callbacks.Error,
	})
}

func (backend *gateSpotWSBackend) StartPublicTrades(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	return backend.subscribe(ctx, gate.ChannelSpotTrade, []string{native}, func(raw json.RawMessage) {
		event, err := gateSpotPublicTradeEvent(raw, instrument)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		callbacks.Event(event)
	})
}

func (backend *gateSpotWSBackend) StartCandles(ctx context.Context, instrument exchange.Instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	return backend.subscribe(ctx, "spot.candlesticks", []string{interval, native}, func(raw json.RawMessage) {
		event, err := gateSpotCandleEvent(raw, instrument, interval)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		callbacks.Event(event)
	})
}

func (backend *gateSpotWSBackend) StartOrders(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	return backend.subscribe(ctx, gate.ChannelSpotOrder, []string{native}, func(raw json.RawMessage) {
		events, err := gateSpotOrderEvents(raw, instrument)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gateSpotWSBackend) StartFills(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	return backend.subscribe(ctx, gate.ChannelSpotUserTrade, []string{native}, func(raw json.RawMessage) {
		events, err := gateSpotFillEvents(raw, instrument)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gateSpotWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	return backend.subscribe(ctx, gate.ChannelSpotBalance, []string{}, func(raw json.RawMessage) {
		events, err := gateSpotBalanceEvents(raw)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gateSpotWSBackend) StartAccountEvents(ctx context.Context, index accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	const operation = "WatchAccountEvents"
	if backend == nil || backend.ws == nil {
		return nil, exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{Venue: exchange.VenueGate, Product: exchange.ProductSpot, Operation: operation, SafeMessage: "gate websocket client is not configured"})
	}
	removeLifecycle := backend.lifecycle.Register("account-events", func(value backendStatus) {
		if callbacks.Status != nil {
			callbacks.Status(accountStreamStatus(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, value))
		}
	}, nil)
	stops := make([]func() error, 0, 3)
	fail := func(err error) (func() error, error) {
		_ = closeAccountStops(stops...)()
		removeLifecycle()
		return nil, err
	}
	orders, err := backend.subscribe(ctx, gate.ChannelSpotOrder, []string{"!all"}, func(raw json.RawMessage) {
		message, decodeErr := gate.DecodeSpotOrderMessage(raw)
		if decodeErr != nil {
			callbacks.Error(gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, operation, decodeErr.Error()))
			return
		}
		if validateErr := gateValidateSpotOrders(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, operation, message.Orders); validateErr != nil {
			callbacks.Error(validateErr)
			return
		}
		for _, row := range message.Orders {
			item, ok := index.resolve(row.CurrencyPair, "")
			if !ok {
				callbacks.Error(fmt.Errorf("gate account order instrument is unknown"))
				return
			}
			event := exchange.OrderEvent{Kind: exchange.EventDelta, Order: gateSpotOrder(row, item.Instrument)}
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventOrder, Order: &event})
		}
	})
	if err != nil {
		return fail(err)
	}
	stops = append(stops, orders)
	fills, err := backend.subscribe(ctx, gate.ChannelSpotUserTrade, []string{"!all"}, func(raw json.RawMessage) {
		message, decodeErr := gate.DecodeSpotUserTradeMessage(raw)
		if decodeErr != nil {
			callbacks.Error(gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, operation, decodeErr.Error()))
			return
		}
		if validateErr := gateValidateSpotFills(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, operation, message.Trades); validateErr != nil {
			callbacks.Error(validateErr)
			return
		}
		for _, row := range message.Trades {
			item, ok := index.resolve(row.CurrencyPair, "")
			if !ok {
				callbacks.Error(fmt.Errorf("gate account fill instrument is unknown"))
				return
			}
			fill := exchange.Fill{NativeSymbol: row.CurrencyPair, NativeID: item.Instrument.Identity.NativeID, OrderID: row.OrderID, ClientOrderID: gatePortableClientOrderID(row.Text), FillID: row.ID, Side: gateSide(row.Side), Price: gateDecimal(row.Price), Quantity: gateDecimal(row.Amount), Fee: gateDecimal(row.Fee), FeeAsset: row.FeeCurrency, Liquidity: gateLiquidity(row.Role), Time: gateTimeMS(row.CreateTimeMS)}
			event := exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventFill, Fill: &event})
		}
	})
	if err != nil {
		return fail(err)
	}
	stops = append(stops, fills)
	balances, err := backend.subscribe(ctx, gate.ChannelSpotBalance, nil, func(raw json.RawMessage) {
		events, convertErr := gateSpotBalanceEvents(raw)
		if convertErr != nil {
			callbacks.Error(convertErr)
			return
		}
		for _, event := range events {
			copyOf := event
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventBalance, Balance: &copyOf})
		}
	})
	if err != nil {
		return fail(err)
	}
	stops = append(stops, balances)
	return func() error {
		removeLifecycle()
		return closeAccountStops(stops...)()
	}, nil
}

func (backend *gateSpotWSBackend) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.rest.PlaceOrder(ctx, request)
}

func (backend *gateSpotWSBackend) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.rest.CancelOrder(ctx, request)
}

func (backend *gateSpotWSBackend) Close() error {
	if backend == nil || backend.ws == nil {
		return nil
	}
	return backend.ws.Close()
}

func (backend *gatePerpWSBackend) StartOrderBook(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	multiplier, err := backend.rest.gateContractMultiplier("WatchOrderBook", instrument)
	if err != nil {
		return nil, err
	}
	first := true
	return backend.subscribe(ctx, gate.ChannelFuturesOrderBook, gatePerpOrderBookSubscription(native), func(raw json.RawMessage) {
		event, err := gatePerpOrderBookEvent(raw, instrument, multiplier)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		if !first {
			event.Kind = exchange.EventFull
		}
		first = false
		callbacks.Event(event)
	})
}

func (backend *gatePerpWSBackend) StartPublicTrades(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	multiplier, err := backend.rest.gateContractMultiplier("WatchPublicTrades", instrument)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesTrade, []string{native}, func(raw json.RawMessage) {
		events, err := gatePerpPublicTradeEvents(raw, instrument, multiplier)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func gatePerpPublicTradeEvents(raw json.RawMessage, instrument exchange.Instrument, multiplier decimal.Decimal) ([]exchange.PublicTradeEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, gateNormalizeErr(meta, "WatchPublicTrades", err)
	}
	var rows []gate.FuturesTrade
	// Both arms are load-bearing. Gate documents `result` as one object, and
	// its testnet sent an array; that environment is gone, but nothing proved
	// the array was the sandbox's rather than the endpoint's, so dropping
	// either arm would be a guess made against a live stream.
	if len(envelope.Result) > 0 && envelope.Result[0] == '[' {
		if err := json.Unmarshal(envelope.Result, &rows); err != nil {
			return nil, gateNormalizeErr(meta, "WatchPublicTrades", err)
		}
	} else {
		var row gate.FuturesTrade
		if err := json.Unmarshal(envelope.Result, &row); err != nil {
			return nil, gateNormalizeErr(meta, "WatchPublicTrades", err)
		}
		rows = []gate.FuturesTrade{row}
	}
	if err := gateValidateFuturesPublicTrades(meta, "WatchPublicTrades", rows); err != nil {
		return nil, err
	}
	events := make([]exchange.PublicTradeEvent, 0, len(rows))
	for _, row := range rows {
		if row.Contract != instrument.Identity.NativeSymbol {
			continue // another instrument multiplexed on this channel
		}
		side := exchange.SideBuy
		size := gateDecimal(string(row.Size))
		if size.IsNegative() {
			side = exchange.SideSell
		}
		events = append(events, exchange.PublicTradeEvent{Instrument: instrument, TradeID: gateOrderID(row.ID), Side: side, Price: gateDecimal(row.Price), Quantity: size.Abs().Mul(multiplier), Time: gateUnixSecondDecimalString(string(row.CreateTime))})
	}
	if len(events) == 0 && len(rows) > 0 {
		return nil, errGateForeignInstrument
	}
	if len(events) == 0 {
		return nil, gateMalformed(meta, "WatchPublicTrades", "gate futures trade update is empty")
	}
	return events, nil
}

func (backend *gatePerpWSBackend) StartCandles(ctx context.Context, instrument exchange.Instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	native := instrument.Identity.NativeSymbol
	multiplier, err := backend.rest.gateContractMultiplier("WatchCandles", instrument)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, "futures.candlesticks", []string{interval, native}, func(raw json.RawMessage) {
		events, err := gatePerpCandleEvents(raw, instrument, interval)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			event.Candle.Volume = event.Candle.Volume.Mul(multiplier)
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartOrders(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	multiplier, err := backend.rest.gateContractMultiplier("WatchOrders", instrument)
	if err != nil {
		return nil, err
	}
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesOrder, gatePerpPrivateInstrumentPayload(userID, instrument.Identity.NativeSymbol), func(raw json.RawMessage) {
		events, err := gateFuturesOrderEvents(raw, instrument, multiplier)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartFills(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	multiplier, err := backend.rest.gateContractMultiplier("WatchFills", instrument)
	if err != nil {
		return nil, err
	}
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesUserTrade, gatePerpPrivateInstrumentPayload(userID, instrument.Identity.NativeSymbol), func(raw json.RawMessage) {
		events, err := gateFuturesFillEvents(raw, instrument, multiplier)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesBalance, gatePerpPrivateAccountPayload(userID), func(raw json.RawMessage) {
		events, err := gateFuturesBalanceEvents(raw)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartPositions(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	multiplier, err := backend.rest.gateContractMultiplier("WatchPositions", instrument)
	if err != nil {
		return nil, err
	}
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesPosition, gatePerpPrivateInstrumentPayload(userID, instrument.Identity.NativeSymbol), func(raw json.RawMessage) {
		events, err := gateFuturesPositionEvents(raw, instrument, multiplier)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartAccountEvents(ctx context.Context, index accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	const operation = "WatchAccountEvents"
	type routeInstrument struct {
		instrument exchange.Instrument
		multiplier decimal.Decimal
	}
	metas := make(map[string]routeInstrument, len(index.byNative))
	for _, item := range index.instruments() {
		multiplier, err := backend.rest.gateContractMultiplier(operation, item.Instrument)
		if err != nil {
			return nil, err
		}
		metas[item.Instrument.Identity.NativeSymbol] = routeInstrument{instrument: item.Instrument, multiplier: multiplier}
	}
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("account-events", func(value backendStatus) {
		if callbacks.Status != nil {
			callbacks.Status(accountStreamStatus(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, value))
		}
	}, nil)
	payload := gatePerpPrivateAllPayload(userID)
	stops := make([]func() error, 0, 4)
	fail := func(err error) (func() error, error) {
		_ = closeAccountStops(stops...)()
		removeLifecycle()
		return nil, err
	}
	orders, err := backend.subscribe(ctx, gate.ChannelFuturesOrder, payload, func(raw json.RawMessage) {
		message, decodeErr := gate.DecodeFuturesOrderMessage(raw)
		if decodeErr != nil {
			callbacks.Error(gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, operation, decodeErr.Error()))
			return
		}
		if validateErr := gateValidateFuturesOrders(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, operation, message.Orders); validateErr != nil {
			callbacks.Error(validateErr)
			return
		}
		for _, row := range message.Orders {
			meta, ok := metas[row.Contract]
			if !ok {
				callbacks.Error(fmt.Errorf("gate account order instrument is unknown"))
				return
			}
			event := exchange.OrderEvent{Kind: exchange.EventDelta, Order: gateFuturesOrder(row, meta.instrument, meta.multiplier)}
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventOrder, Order: &event})
		}
	})
	if err != nil {
		return fail(err)
	}
	stops = append(stops, orders)
	fills, err := backend.subscribe(ctx, gate.ChannelFuturesUserTrade, payload, func(raw json.RawMessage) {
		message, decodeErr := gate.DecodeFuturesUserTradeMessage(raw)
		if decodeErr != nil {
			callbacks.Error(gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, operation, decodeErr.Error()))
			return
		}
		if validateErr := gateValidateFuturesFills(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, operation, message.Trades); validateErr != nil {
			callbacks.Error(validateErr)
			return
		}
		for _, row := range message.Trades {
			meta, ok := metas[row.Contract]
			if !ok {
				callbacks.Error(fmt.Errorf("gate account fill instrument is unknown"))
				return
			}
			converted := gateFuturesFills([]gate.MyFuturesTrade{row}, meta.instrument, meta.multiplier)
			for _, fill := range converted {
				event := exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}
				callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventFill, Fill: &event})
			}
		}
	})
	if err != nil {
		return fail(err)
	}
	stops = append(stops, fills)
	balances, err := backend.subscribe(ctx, gate.ChannelFuturesBalance, gatePerpPrivateAccountPayload(userID), func(raw json.RawMessage) {
		events, convertErr := gateFuturesBalanceEvents(raw)
		if convertErr != nil {
			callbacks.Error(convertErr)
			return
		}
		for _, event := range events {
			copyOf := event
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventBalance, Balance: &copyOf})
		}
	})
	if err != nil {
		return fail(err)
	}
	stops = append(stops, balances)
	positions, err := backend.subscribe(ctx, gate.ChannelFuturesPosition, payload, func(raw json.RawMessage) {
		message, decodeErr := gate.DecodeFuturesPositionMessage(raw)
		if decodeErr != nil {
			callbacks.Error(gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, operation, decodeErr.Error()))
			return
		}
		if validateErr := gateValidatePositions(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, operation, message.Positions); validateErr != nil {
			callbacks.Error(validateErr)
			return
		}
		for _, row := range message.Positions {
			meta, ok := metas[row.Contract]
			if !ok {
				callbacks.Error(fmt.Errorf("gate account position instrument is unknown"))
				return
			}
			side, size := exchange.SideBuy, row.Size
			if size < 0 {
				side, size = exchange.SideSell, -size
			}
			position := exchange.Position{NativeSymbol: row.Contract, NativeID: meta.instrument.Identity.NativeID, Side: side, Quantity: decimal.NewFromInt(size).Mul(meta.multiplier), EntryPrice: gateDecimal(row.EntryPrice), MarkPrice: gateDecimal(row.MarkPrice), UnrealizedPnL: gateDecimal(row.UnrealisedPNL), LiquidationPrice: gateOptionalDecimal(row.LiqPrice), Leverage: gateOptionalDecimal(row.Leverage), MarginUsed: gateOptionalDecimal(row.Margin)}
			event := exchange.PositionEvent{Kind: exchange.EventDelta, Positions: []exchange.Position{position}, Time: time.Now().UTC()}
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventPosition, NativeSymbol: row.Contract, Position: &event})
		}
	})
	if err != nil {
		return fail(err)
	}
	stops = append(stops, positions)
	return func() error {
		removeLifecycle()
		return closeAccountStops(stops...)()
	}, nil
}

func (backend *gatePerpWSBackend) privateUserID(ctx context.Context) (string, error) {
	if backend == nil || backend.rest == nil {
		return "", exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{Venue: exchange.VenueGate, Product: exchange.ProductPerp, Operation: "Watch", SafeMessage: "gate futures REST client is not configured"})
	}
	backend.userIDMu.Lock()
	defer backend.userIDMu.Unlock()
	if backend.userID != "" {
		return backend.userID, nil
	}
	userID, err := backend.rest.gateFuturesUserID(ctx)
	if err != nil {
		return "", err
	}
	backend.userID = userID
	return userID, nil
}

func gatePerpReferenceEvents(raw json.RawMessage, instrument exchange.Instrument) ([]perpReferenceEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	var message struct {
		Result []gate.FuturesTicker `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, gateNormalizeErr(meta, "WatchMarkPrice", err)
	}
	events := make([]perpReferenceEvent, 0, len(message.Result))
	for _, row := range message.Result {
		if row.Contract != instrument.Identity.NativeSymbol {
			continue // another instrument multiplexed on this channel
		}
		if row.MarkPrice != "" {
			if err := gateValidateDecimal(meta, "WatchMarkPrice", "mark price", row.MarkPrice); err != nil {
				return nil, err
			}
		}
		if row.FundingRate != "" {
			if err := gateValidateDecimal(meta, "WatchFundingRate", "funding rate", row.FundingRate); err != nil {
				return nil, err
			}
		}
		at := time.Now().UTC()
		events = append(events, perpReferenceEvent{
			MarkValid:    row.MarkPrice != "",
			MarkPrice:    exchange.MarkPriceEvent{Instrument: instrument, Price: gateDecimal(row.MarkPrice), Time: at},
			FundingValid: row.FundingRate != "",
			FundingRate:  exchange.FundingRateEvent{Instrument: instrument, Rate: gateDecimal(row.FundingRate), EffectiveAt: at},
		})
	}
	if len(events) == 0 && len(message.Result) > 0 {
		return nil, errGateForeignInstrument
	}
	if len(events) == 0 {
		return nil, gateMalformed(meta, "WatchMarkPrice", "gate futures ticker update is empty")
	}
	return events, nil
}

func (backend *gatePerpWSBackend) StartReference(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	return backend.subscribe(ctx, "futures.tickers", []string{instrument.Identity.NativeSymbol}, func(raw json.RawMessage) {
		events, err := gatePerpReferenceEvents(raw, instrument)
		if err != nil {
			if !errors.Is(err, errGateForeignInstrument) {
				callbacks.Error(err)
			}
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gateSpotWSBackend) subscribe(ctx context.Context, channel string, payload []string, handler func(json.RawMessage)) (func() error, error) {
	if backend == nil || backend.ws == nil {
		return nil, exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{Venue: exchange.VenueGate, Operation: "Watch", SafeMessage: "gate websocket client is not configured"})
	}
	if err := backend.ws.Subscribe(ctx, channel, payload, handler); err != nil {
		return nil, err
	}
	return func() error { return backend.ws.Unsubscribe(context.Background(), channel, payload) }, nil
}

func gateSpotPublicTradeEvent(raw json.RawMessage, instrument exchange.Instrument) (exchange.PublicTradeEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	var message struct {
		Result gate.Trade `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return exchange.PublicTradeEvent{}, gateNormalizeErr(meta, "WatchPublicTrades", err)
	}
	if err := gateValidateSpotPublicTrades(meta, "WatchPublicTrades", []gate.Trade{message.Result}); err != nil {
		return exchange.PublicTradeEvent{}, err
	}
	if message.Result.CurrencyPair != instrument.Identity.NativeSymbol {
		return exchange.PublicTradeEvent{}, errGateForeignInstrument
	}
	return exchange.PublicTradeEvent{Instrument: instrument, TradeID: message.Result.ID, Side: gateSide(message.Result.Side), Price: gateDecimal(message.Result.Price), Quantity: gateDecimal(message.Result.Amount), Time: gateTimeMS(message.Result.CreateTimeMS)}, nil
}

func gateSpotOrderEvents(raw []byte, instrument exchange.Instrument) ([]exchange.OrderEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	message, err := gate.DecodeSpotOrderMessage(raw)
	if err != nil {
		return nil, gateMalformed(meta, "WatchOrders", err.Error())
	}
	if err := gateValidateSpotOrders(meta, "WatchOrders", message.Orders); err != nil {
		return nil, err
	}
	events := make([]exchange.OrderEvent, 0, len(message.Orders))
	for _, row := range message.Orders {
		if row.CurrencyPair != instrument.Identity.NativeSymbol {
			continue // another instrument multiplexed on this channel
		}
		events = append(events, exchange.OrderEvent{Kind: exchange.EventDelta, Order: gateSpotOrder(row, instrument)})
	}
	return events, nil
}

func gateFuturesOrderEvents(raw []byte, instrument exchange.Instrument, multiplier decimal.Decimal) ([]exchange.OrderEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	message, err := gate.DecodeFuturesOrderMessage(raw)
	if err != nil {
		return nil, gateMalformed(meta, "WatchOrders", err.Error())
	}
	if err := gateValidateFuturesOrders(meta, "WatchOrders", message.Orders); err != nil {
		return nil, err
	}
	events := make([]exchange.OrderEvent, 0, len(message.Orders))
	for _, row := range message.Orders {
		if row.Contract != instrument.Identity.NativeSymbol {
			continue // another instrument multiplexed on this channel
		}
		events = append(events, exchange.OrderEvent{Kind: exchange.EventDelta, Order: gateFuturesOrder(row, instrument, multiplier)})
	}
	return events, nil
}

func gateSpotFillEvents(raw []byte, instrument exchange.Instrument) ([]exchange.FillEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	message, err := gate.DecodeSpotUserTradeMessage(raw)
	if err != nil {
		return nil, gateMalformed(meta, "WatchFills", err.Error())
	}
	if err := gateValidateSpotFills(meta, "WatchFills", message.Trades); err != nil {
		return nil, err
	}
	fills := make([]exchange.Fill, 0, len(message.Trades))
	for _, row := range message.Trades {
		if row.CurrencyPair != instrument.Identity.NativeSymbol {
			continue // another instrument multiplexed on this channel
		}
		fills = append(fills, exchange.Fill{NativeSymbol: row.CurrencyPair, NativeID: instrument.Identity.NativeID, OrderID: row.OrderID, ClientOrderID: gatePortableClientOrderID(row.Text), FillID: row.ID, Side: gateSide(row.Side), Price: gateDecimal(row.Price), Quantity: gateDecimal(row.Amount), Fee: gateDecimal(row.Fee), FeeAsset: row.FeeCurrency, Liquidity: gateLiquidity(row.Role), Time: gateTimeMS(row.CreateTimeMS)})
	}
	events := make([]exchange.FillEvent, 0, len(fills))
	for _, fill := range fills {
		events = append(events, exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
	}
	return events, nil
}

func gateFuturesFillEvents(raw []byte, instrument exchange.Instrument, multiplier decimal.Decimal) ([]exchange.FillEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	message, err := gate.DecodeFuturesUserTradeMessage(raw)
	if err != nil {
		return nil, gateMalformed(meta, "WatchFills", err.Error())
	}
	if err := gateValidateFuturesFills(meta, "WatchFills", message.Trades); err != nil {
		return nil, err
	}
	for _, row := range message.Trades {
		if row.Contract != instrument.Identity.NativeSymbol {
			continue // another instrument multiplexed on this channel
		}
	}
	fills := gateFuturesFills(message.Trades, instrument, multiplier)
	events := make([]exchange.FillEvent, 0, len(fills))
	for _, fill := range fills {
		events = append(events, exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
	}
	return events, nil
}

func gateSpotBalanceEvents(raw []byte) ([]exchange.BalanceEvent, error) {
	message, err := gate.DecodeSpotBalanceMessage(raw)
	if err != nil {
		return nil, gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchBalances", err.Error())
	}
	balances := make([]exchange.Balance, 0, len(message.Balances))
	for _, row := range message.Balances {
		if err := gateValidateDecimal(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchBalances", "balance total", row.Total); err != nil {
			return nil, err
		}
		if err := gateValidateDecimal(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchBalances", "balance available", row.Available); err != nil {
			return nil, err
		}
		total := gateDecimal(row.Total)
		available := gateDecimal(row.Available)
		balances = append(balances, exchange.Balance{Asset: row.Currency, Available: available, Locked: total.Sub(available), Total: total})
	}
	return []exchange.BalanceEvent{{Kind: exchange.EventDelta, Balances: balances, Time: time.Now().UTC()}}, nil
}

func gateFuturesBalanceEvents(raw []byte) ([]exchange.BalanceEvent, error) {
	message, err := gate.DecodeFuturesBalanceMessage(raw)
	if err != nil {
		return nil, gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchBalances", err.Error())
	}
	balances := make([]exchange.Balance, 0, len(message.Balances))
	for _, row := range message.Balances {
		if err := gateValidateDecimal(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchBalances", "balance total", row.Total); err != nil {
			return nil, err
		}
		total := gateDecimal(row.Total)
		balances = append(balances, exchange.Balance{Asset: row.Currency, Total: total, Available: total})
	}
	return []exchange.BalanceEvent{{Kind: exchange.EventDelta, Balances: balances, Time: time.Now().UTC()}}, nil
}

func gateFuturesPositionEvents(raw []byte, instrument exchange.Instrument, multiplier decimal.Decimal) ([]exchange.PositionEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	message, err := gate.DecodeFuturesPositionMessage(raw)
	if err != nil {
		return nil, gateMalformed(meta, "WatchPositions", err.Error())
	}
	if err := gateValidatePositions(meta, "WatchPositions", message.Positions); err != nil {
		return nil, err
	}
	positions := make([]exchange.Position, 0, len(message.Positions))
	for _, row := range message.Positions {
		if row.Contract != instrument.Identity.NativeSymbol {
			continue // another instrument multiplexed on this channel
		}
		side := exchange.SideBuy
		size := row.Size
		if size < 0 {
			side = exchange.SideSell
			size = -size
		}
		positions = append(positions, exchange.Position{NativeSymbol: row.Contract, NativeID: instrument.Identity.NativeID, Side: side, Quantity: decimal.NewFromInt(size).Mul(multiplier), EntryPrice: gateDecimal(row.EntryPrice), MarkPrice: gateDecimal(row.MarkPrice), UnrealizedPnL: gateDecimal(row.UnrealisedPNL), LiquidationPrice: gateOptionalDecimal(row.LiqPrice), Leverage: gateOptionalDecimal(row.Leverage), MarginUsed: gateOptionalDecimal(row.Margin)})
	}
	return []exchange.PositionEvent{{Kind: exchange.EventDelta, Positions: positions, Time: time.Now().UTC()}}, nil
}

// errGateForeignInstrument marks a message for another instrument. Gate
// multiplexes every subscription of a channel on one connection, so each
// handler sees its siblings' messages and must ignore them.
var errGateForeignInstrument = errors.New("gate message for another instrument")

// gateEnsureNativeInstrument is the strict check for REST responses, which
// answer exactly the instrument requested.
func gateEnsureNativeInstrument(meta clientMeta, operation string, instrument exchange.Instrument, native string) error {
	if strings.TrimSpace(native) == "" || native != instrument.Identity.NativeSymbol {
		return meta.catalogMetadataResolution(operation, "gate native instrument does not match requested instrument")
	}
	return nil
}
