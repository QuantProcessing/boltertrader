package factoryclient

import (
	"context"
	"encoding/json"
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
	gateContractMultiplier(context.Context, string, string) (decimal.Decimal, error)
}

type gateSpotWSBackend struct {
	ws   *gate.WSClient
	rest gateSpotREST
}

type gatePerpWSBackend struct {
	*gateSpotWSBackend
	rest     gatePerpREST
	userIDMu sync.Mutex
	userID   string
}

func newGateSpotWSBackend(ws *gate.WSClient, rest gateSpotREST) *gateSpotWSBackend {
	return &gateSpotWSBackend{ws: ws, rest: rest}
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

func gateSpotOrderBookEvent(raw json.RawMessage, instrument string) (exchange.BookEvent, error) {
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
	return exchange.BookEvent{
		Kind:       exchange.EventSnapshot,
		Instrument: coalesce(message.Result.S, instrument),
		Sequence:   strconv.FormatInt(message.Result.LastUpdateID, 10),
		Bids:       gateBookLevels(message.Result.Bids),
		Asks:       gateBookLevels(message.Result.Asks),
		Time:       gateUnixMilli(message.Result.T),
	}, nil
}

func gatePerpOrderBookEvent(raw json.RawMessage, instrument string, multiplier decimal.Decimal) (exchange.BookEvent, error) {
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
	return exchange.BookEvent{
		Kind:       exchange.EventSnapshot,
		Instrument: coalesce(message.Result.Contract, instrument),
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

func gateSpotCandleEvent(raw json.RawMessage, instrument, interval string) (exchange.CandleEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	var message struct {
		Result gateWSCandlestick `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return exchange.CandleEvent{}, gateNormalizeErr(meta, "WatchCandles", err)
	}
	return gateWSCandleEvent(meta, message.Result, instrument, interval)
}

func gatePerpCandleEvents(raw json.RawMessage, instrument, interval string) ([]exchange.CandleEvent, error) {
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
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, gateMalformed(meta, "WatchCandles", "gate candle update is empty")
	}
	return events, nil
}

func gateWSCandleEvent(meta clientMeta, row gateWSCandlestick, instrument, interval string) (exchange.CandleEvent, error) {
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
		Instrument: gateWSCandleInstrument(row.N, interval, instrument),
		Interval:   interval,
		Candle:     candle,
	}, nil
}

func gateWSCandleInstrument(name, interval, fallback string) string {
	prefix := interval + "_"
	if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
		return name[len(prefix):]
	}
	return fallback
}

func (backend *gateSpotWSBackend) StartOrderBook(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	return backend.subscribe(ctx, gate.ChannelSpotOrderBook, gateSpotOrderBookSubscription(instrument), func(raw json.RawMessage) {
		event, err := gateSpotOrderBookEvent(raw, instrument)
		if err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(event)
	})
}

func (backend *gateSpotWSBackend) StartBBO(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
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

func (backend *gatePerpWSBackend) StartBBO(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
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

func (backend *gateSpotWSBackend) StartPublicTrades(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	return backend.subscribe(ctx, gate.ChannelSpotTrade, []string{instrument}, func(raw json.RawMessage) {
		var message struct {
			Result gate.Trade `json:"result"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			callbacks.Error(err)
			return
		}
		if err := gateValidateSpotPublicTrades(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchPublicTrades", []gate.Trade{message.Result}); err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(exchange.PublicTradeEvent{Instrument: coalesce(message.Result.CurrencyPair, instrument), TradeID: message.Result.ID, Side: gateSide(message.Result.Side), Price: gateDecimal(message.Result.Price), Quantity: gateDecimal(message.Result.Amount), Time: gateTimeMS(message.Result.CreateTimeMS)})
	})
}

func (backend *gateSpotWSBackend) StartCandles(ctx context.Context, instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	return backend.subscribe(ctx, "spot.candlesticks", []string{interval, instrument}, func(raw json.RawMessage) {
		event, err := gateSpotCandleEvent(raw, instrument, interval)
		if err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(event)
	})
}

func (backend *gateSpotWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	return backend.subscribe(ctx, gate.ChannelSpotOrder, []string{instrument}, func(raw json.RawMessage) {
		events, err := gateSpotOrderEvents(raw)
		if err != nil {
			callbacks.Error(err)
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gateSpotWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	return backend.subscribe(ctx, gate.ChannelSpotUserTrade, []string{instrument}, func(raw json.RawMessage) {
		events, err := gateSpotFillEvents(raw)
		if err != nil {
			callbacks.Error(err)
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
			callbacks.Error(err)
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
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

func (backend *gatePerpWSBackend) StartOrderBook(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	multiplier, err := backend.rest.gateContractMultiplier(ctx, "WatchOrderBook", instrument)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesOrderBook, gatePerpOrderBookSubscription(instrument), func(raw json.RawMessage) {
		event, err := gatePerpOrderBookEvent(raw, instrument, multiplier)
		if err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(event)
	})
}

func (backend *gatePerpWSBackend) StartPublicTrades(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	multiplier, err := backend.rest.gateContractMultiplier(ctx, "WatchPublicTrades", instrument)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesTrade, []string{instrument}, func(raw json.RawMessage) {
		events, err := gatePerpPublicTradeEvents(raw, instrument, multiplier)
		if err != nil {
			callbacks.Error(err)
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func gatePerpPublicTradeEvents(raw json.RawMessage, instrument string, multiplier decimal.Decimal) ([]exchange.PublicTradeEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, gateNormalizeErr(meta, "WatchPublicTrades", err)
	}
	var rows []gate.FuturesTrade
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
		side := exchange.SideBuy
		size := gateDecimal(string(row.Size))
		if size.IsNegative() {
			side = exchange.SideSell
		}
		events = append(events, exchange.PublicTradeEvent{Instrument: coalesce(row.Contract, instrument), TradeID: gateOrderID(row.ID), Side: side, Price: gateDecimal(row.Price), Quantity: size.Abs().Mul(multiplier), Time: gateUnixSecondDecimalString(string(row.CreateTime))})
	}
	if len(events) == 0 {
		return nil, gateMalformed(meta, "WatchPublicTrades", "gate futures trade update is empty")
	}
	return events, nil
}

func (backend *gatePerpWSBackend) StartCandles(ctx context.Context, instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	return backend.subscribe(ctx, "futures.candlesticks", []string{interval, instrument}, func(raw json.RawMessage) {
		events, err := gatePerpCandleEvents(raw, instrument, interval)
		if err != nil {
			callbacks.Error(err)
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	multiplier, err := backend.rest.gateContractMultiplier(ctx, "WatchOrders", instrument)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesOrder, gatePerpPrivateInstrumentPayload(userID, instrument), func(raw json.RawMessage) {
		events, err := gateFuturesOrderEvents(raw, multiplier)
		if err != nil {
			callbacks.Error(err)
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	multiplier, err := backend.rest.gateContractMultiplier(ctx, "WatchFills", instrument)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesUserTrade, gatePerpPrivateInstrumentPayload(userID, instrument), func(raw json.RawMessage) {
		events, err := gateFuturesFillEvents(raw, multiplier)
		if err != nil {
			callbacks.Error(err)
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
			callbacks.Error(err)
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
}

func (backend *gatePerpWSBackend) StartPositions(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	userID, err := backend.privateUserID(ctx)
	if err != nil {
		return nil, err
	}
	multiplier, err := backend.rest.gateContractMultiplier(ctx, "WatchPositions", instrument)
	if err != nil {
		return nil, err
	}
	return backend.subscribe(ctx, gate.ChannelFuturesPosition, gatePerpPrivateInstrumentPayload(userID, instrument), func(raw json.RawMessage) {
		events, err := gateFuturesPositionEvents(raw, instrument, multiplier)
		if err != nil {
			callbacks.Error(err)
			return
		}
		for _, event := range events {
			callbacks.Event(event)
		}
	})
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

func gatePerpReferenceEvents(raw json.RawMessage, instrument string) ([]perpReferenceEvent, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	var message struct {
		Result []gate.FuturesTicker `json:"result"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, gateNormalizeErr(meta, "WatchMarkPrice", err)
	}
	events := make([]perpReferenceEvent, 0, len(message.Result))
	for _, row := range message.Result {
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
			MarkPrice:    exchange.MarkPriceEvent{Instrument: coalesce(row.Contract, instrument), Price: gateDecimal(row.MarkPrice), Time: at},
			FundingValid: row.FundingRate != "",
			FundingRate:  exchange.FundingRateEvent{Instrument: coalesce(row.Contract, instrument), Rate: gateDecimal(row.FundingRate), EffectiveAt: at},
		})
	}
	if len(events) == 0 {
		return nil, gateMalformed(meta, "WatchMarkPrice", "gate futures ticker update is empty")
	}
	return events, nil
}

func (backend *gatePerpWSBackend) StartReference(ctx context.Context, instrument string, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	return backend.subscribe(ctx, "futures.tickers", []string{instrument}, func(raw json.RawMessage) {
		events, err := gatePerpReferenceEvents(raw, instrument)
		if err != nil {
			callbacks.Error(err)
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

func coalesce(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func gateSpotOrderEvents(raw []byte) ([]exchange.OrderEvent, error) {
	message, err := gate.DecodeSpotOrderMessage(raw)
	if err != nil {
		return nil, gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchOrders", err.Error())
	}
	if err := gateValidateSpotOrders(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchOrders", message.Orders); err != nil {
		return nil, err
	}
	events := make([]exchange.OrderEvent, 0, len(message.Orders))
	for _, row := range message.Orders {
		events = append(events, exchange.OrderEvent{Kind: exchange.EventDelta, Order: gateSpotOrder(row)})
	}
	return events, nil
}

func gateFuturesOrderEvents(raw []byte, multiplier decimal.Decimal) ([]exchange.OrderEvent, error) {
	message, err := gate.DecodeFuturesOrderMessage(raw)
	if err != nil {
		return nil, gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchOrders", err.Error())
	}
	if err := gateValidateFuturesOrders(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchOrders", message.Orders); err != nil {
		return nil, err
	}
	events := make([]exchange.OrderEvent, 0, len(message.Orders))
	for _, row := range message.Orders {
		events = append(events, exchange.OrderEvent{Kind: exchange.EventDelta, Order: gateFuturesOrder(row, multiplier)})
	}
	return events, nil
}

func gateSpotFillEvents(raw []byte) ([]exchange.FillEvent, error) {
	message, err := gate.DecodeSpotUserTradeMessage(raw)
	if err != nil {
		return nil, gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchFills", err.Error())
	}
	if err := gateValidateSpotFills(clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}, "WatchFills", message.Trades); err != nil {
		return nil, err
	}
	fills := gateSpotFills(message.Trades)
	events := make([]exchange.FillEvent, 0, len(fills))
	for _, fill := range fills {
		events = append(events, exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
	}
	return events, nil
}

func gateFuturesFillEvents(raw []byte, multiplier decimal.Decimal) ([]exchange.FillEvent, error) {
	message, err := gate.DecodeFuturesUserTradeMessage(raw)
	if err != nil {
		return nil, gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchFills", err.Error())
	}
	if err := gateValidateFuturesFills(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchFills", message.Trades); err != nil {
		return nil, err
	}
	fills := gateFuturesFills(message.Trades, multiplier)
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

func gateFuturesPositionEvents(raw []byte, instrument string, multiplier decimal.Decimal) ([]exchange.PositionEvent, error) {
	message, err := gate.DecodeFuturesPositionMessage(raw)
	if err != nil {
		return nil, gateMalformed(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchPositions", err.Error())
	}
	if err := gateValidatePositions(clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}, "WatchPositions", message.Positions); err != nil {
		return nil, err
	}
	positions := make([]exchange.Position, 0, len(message.Positions))
	for _, row := range message.Positions {
		if instrument != "" && row.Contract != instrument {
			continue
		}
		side := exchange.SideBuy
		size := row.Size
		if size < 0 {
			side = exchange.SideSell
			size = -size
		}
		positions = append(positions, exchange.Position{Instrument: row.Contract, Side: side, Quantity: decimal.NewFromInt(size).Mul(multiplier), EntryPrice: gateDecimal(row.EntryPrice), MarkPrice: gateDecimal(row.MarkPrice), UnrealizedPnL: gateDecimal(row.UnrealisedPNL), LiquidationPrice: gateOptionalDecimal(row.LiqPrice), Leverage: gateOptionalDecimal(row.Leverage), MarginUsed: gateOptionalDecimal(row.Margin)})
	}
	return []exchange.PositionEvent{{Kind: exchange.EventDelta, Positions: positions, Time: time.Now().UTC()}}, nil
}
