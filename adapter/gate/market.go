package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

type marketDataClient struct {
	rest      *gatesdk.Client
	spotWS    *gatesdk.WSClient
	futuresWS *gatesdk.WSClient
	provider  *instrumentProvider
	clk       clock.Clock
	scope     []enums.InstrumentKind
	stream    *wsstream.Stream[contract.MarketEnvelope]
}

func newMarketDataClient(rest *gatesdk.Client, spotWS, futuresWS *gatesdk.WSClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &marketDataClient{rest: rest, spotWS: spotWS, futuresWS: futuresWS, provider: provider, clk: clk, scope: gateTradingKinds(), stream: wsstream.New[contract.MarketEnvelope](1024)}
}

func (c *marketDataClient) withScope(scope []enums.InstrumentKind) *marketDataClient {
	c.scope = gateKinds(scope)
	return c
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("gate: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	if inst.ID.Kind == enums.KindPerp {
		book, err := c.rest.GetFuturesOrderBook(ctx, gatesdk.SettleUSDT, inst.VenueSymbol, depth, true)
		if err != nil {
			return nil, err
		}
		return &model.OrderBook{InstrumentID: id, Bids: futuresBookLevels(book.Bids), Asks: futuresBookLevels(book.Asks), Sequence: book.ID, Timestamp: timeFromSecondsString(string(book.Update))}, nil
	}
	book, err := c.rest.GetSpotOrderBook(ctx, inst.VenueSymbol, depth, true)
	if err != nil {
		return nil, err
	}
	return &model.OrderBook{InstrumentID: id, Bids: bookLevels(book.Bids), Asks: bookLevels(book.Asks), Sequence: book.ID, Timestamp: timeFromMillis(book.Update)}, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	var rows []gatesdk.Candlestick
	if inst.ID.Kind == enums.KindPerp {
		rows, err = c.rest.ListFuturesCandlesticks(ctx, gatesdk.SettleUSDT, inst.VenueSymbol, interval, limit)
	} else {
		rows, err = c.rest.ListSpotCandlesticks(ctx, inst.VenueSymbol, interval, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(rows))
	for _, row := range rows {
		if bar, ok := barFromGateCandle(id, interval, row); ok {
			out = append(out, bar)
		}
	}
	return out, nil
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	channel := gatesdk.ChannelSpotOrderBook
	ws := c.spotWS
	if inst.ID.Kind == enums.KindPerp {
		channel = gatesdk.ChannelFuturesOrderBook
		ws = c.futuresWS
	}
	return c.subscribe(ctx, ws, channel, []string{inst.VenueSymbol, "100ms"}, func(payload []byte) {
		book, ok := orderBookFromPayload(id, payload)
		if ok {
			c.emit(contract.BookEvent{Book: book})
		}
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	channel := "spot.tickers"
	ws := c.spotWS
	if inst.ID.Kind == enums.KindPerp {
		channel = "futures.tickers"
		ws = c.futuresWS
	}
	return c.subscribe(ctx, ws, channel, []string{inst.VenueSymbol}, func(payload []byte) {
		if quote, ok := quoteFromTickerPayload(id, payload, c.clk.Now()); ok {
			c.emit(contract.QuoteEvent{Quote: quote})
		}
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	channel := gatesdk.ChannelSpotTrade
	ws := c.spotWS
	if inst.ID.Kind == enums.KindPerp {
		channel = gatesdk.ChannelFuturesTrade
		ws = c.futuresWS
	}
	return c.subscribe(ctx, ws, channel, []string{inst.VenueSymbol}, func(payload []byte) {
		for _, trade := range tradesFromPayload(id, payload, c.clk.Now()) {
			c.emit(contract.TradeEvent{Trade: trade})
		}
	})
}

func (c *marketDataClient) subscribe(ctx context.Context, ws *gatesdk.WSClient, channel string, payload []string, handler func([]byte)) error {
	if ws == nil {
		return fmt.Errorf("gate: public ws not configured: %w", errs.ErrNotSupported)
	}
	return ws.Subscribe(ctx, channel, payload, func(payload json.RawMessage) { handler(payload) })
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	products := make([]contract.ProductCapability, 0, len(c.scope))
	for _, kind := range c.scope {
		products = append(products, contract.ProductCapability{Kind: kind, Market: true})
	}
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  products,
		Streaming: contract.StreamCapabilities{Market: c.spotWS != nil || c.futuresWS != nil},
	}
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }
func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}
func (c *marketDataClient) Close() error {
	if c.spotWS != nil {
		_ = c.spotWS.Close()
	}
	if c.futuresWS != nil {
		_ = c.futuresWS.Close()
	}
	c.stream.Close()
	return nil
}

func bookLevels(raw [][]gatesdk.NumberString) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, level := range raw {
		if len(level) < 2 {
			continue
		}
		out = append(out, model.BookLevel{Price: dec(string(level[0])), Quantity: dec(string(level[1]))})
	}
	return out
}

func futuresBookLevels(raw []gatesdk.FuturesOrderBookItem) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, level := range raw {
		out = append(out, model.BookLevel{Price: dec(level.Price), Quantity: decimal.NewFromInt(level.Size).Abs()})
	}
	return out
}

func barFromGateCandle(id model.InstrumentID, interval string, row gatesdk.Candlestick) (model.Bar, bool) {
	if len(row) < 6 || row[0] == "" {
		return model.Bar{}, false
	}
	openTime := timeFromSecondsString(string(row[0]))
	return model.Bar{
		InstrumentID: id,
		Interval:     interval,
		Open:         dec(string(row[5])),
		High:         dec(string(row[3])),
		Low:          dec(string(row[4])),
		Close:        dec(string(row[2])),
		Volume:       dec(string(row[1])),
		OpenTime:     openTime,
	}, true
}

func orderBookFromPayload(id model.InstrumentID, payload []byte) (model.OrderBook, bool) {
	env, err := gatesdk.DecodeWSEnvelope(payload)
	if err != nil || len(env.Result) == 0 {
		return model.OrderBook{}, false
	}
	var book gatesdk.OrderBook
	if err := json.Unmarshal(env.Result, &book); err == nil && (len(book.Bids) > 0 || len(book.Asks) > 0) {
		return model.OrderBook{InstrumentID: id, Bids: bookLevels(book.Bids), Asks: bookLevels(book.Asks), Sequence: firstNonZeroInt64(book.ID, book.Update), Timestamp: timeFromMillis(book.Update)}, true
	}
	var futuresBook gatesdk.FuturesOrderBook
	if err := json.Unmarshal(env.Result, &futuresBook); err != nil {
		return model.OrderBook{}, false
	}
	return model.OrderBook{InstrumentID: id, Bids: futuresBookLevels(futuresBook.Bids), Asks: futuresBookLevels(futuresBook.Asks), Sequence: firstNonZeroInt64(futuresBook.ID, parseGateTimestampSeconds(string(futuresBook.Update))), Timestamp: timeFromSecondsString(string(futuresBook.Update))}, true
}

func quoteFromTickerPayload(id model.InstrumentID, payload []byte, fallback time.Time) (model.QuoteTick, bool) {
	env, err := gatesdk.DecodeWSEnvelope(payload)
	if err != nil || len(env.Result) == 0 {
		return model.QuoteTick{}, false
	}
	ticker, ok := firstTicker(env.Result)
	if !ok {
		return model.QuoteTick{}, false
	}
	return model.QuoteTick{
		InstrumentID: id,
		BidPrice:     dec(ticker.HighestBid),
		AskPrice:     dec(ticker.LowestAsk),
		Timestamp:    firstNonZeroTime(timeFromMillis(env.TimeMS), timeFromSeconds(env.Time), fallback),
	}, true
}

func tradesFromPayload(id model.InstrumentID, payload []byte, fallback time.Time) []model.TradeTick {
	env, err := gatesdk.DecodeWSEnvelope(payload)
	if err != nil || len(env.Result) == 0 {
		return nil
	}
	if out := spotTradesFromPayload(id, env, fallback); len(out) > 0 {
		return out
	}
	return futuresTradesFromPayload(id, env, fallback)
}

func spotTradesFromPayload(id model.InstrumentID, env *gatesdk.WSEnvelope, fallback time.Time) []model.TradeTick {
	var trades []gatesdk.Trade
	if err := json.Unmarshal(env.Result, &trades); err != nil {
		var single gatesdk.Trade
		if err := json.Unmarshal(env.Result, &single); err != nil || single.ID == "" {
			return nil
		}
		trades = []gatesdk.Trade{single}
	}
	out := make([]model.TradeTick, 0, len(trades))
	for _, row := range trades {
		if row.ID == "" && row.Price == "" {
			continue
		}
		out = append(out, model.TradeTick{
			InstrumentID:  id,
			Price:         dec(row.Price),
			Quantity:      dec(row.Amount),
			AggressorSide: sideFromGate(row.Side),
			TradeID:       row.ID,
			Timestamp:     firstNonZeroTime(timeFromMillisString(row.CreateTimeMS), timeFromSecondsString(row.CreateTime), timeFromMillis(env.TimeMS), timeFromSeconds(env.Time), fallback),
		})
	}
	return out
}

func futuresTradesFromPayload(id model.InstrumentID, env *gatesdk.WSEnvelope, fallback time.Time) []model.TradeTick {
	var trades []gatesdk.FuturesTrade
	if err := json.Unmarshal(env.Result, &trades); err != nil {
		var single gatesdk.FuturesTrade
		if err := json.Unmarshal(env.Result, &single); err != nil || single.ID == 0 {
			return nil
		}
		trades = []gatesdk.FuturesTrade{single}
	}
	out := make([]model.TradeTick, 0, len(trades))
	for _, row := range trades {
		out = append(out, model.TradeTick{
			InstrumentID:  id,
			Price:         dec(row.Price),
			Quantity:      decimal.NewFromInt(row.Size).Abs(),
			AggressorSide: sideFromSignedSize(row.Size),
			TradeID:       strconv.FormatInt(row.ID, 10),
			Timestamp:     firstNonZeroTime(timeFromSeconds(row.CreateTime), timeFromMillis(env.TimeMS), timeFromSeconds(env.Time), fallback),
		})
	}
	return out
}

func firstTicker(raw json.RawMessage) (gatesdk.Ticker, bool) {
	var ticker gatesdk.Ticker
	if err := json.Unmarshal(raw, &ticker); err == nil && (ticker.CurrencyPair != "" || ticker.HighestBid != "" || ticker.LowestAsk != "") {
		return ticker, true
	}
	var tickers []gatesdk.Ticker
	if err := json.Unmarshal(raw, &tickers); err == nil && len(tickers) > 0 {
		return tickers[0], true
	}
	return gatesdk.Ticker{}, false
}

func timeFromSeconds(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

func timeFromMillisString(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return timeFromMillis(ms)
}
