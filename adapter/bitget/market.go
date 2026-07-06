package bitget

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

type marketDataClient struct {
	rest     *bitgetsdk.Client
	ws       *bitgetsdk.PublicWSClient
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]
}

func newMarketDataClient(rest *bitgetsdk.Client, ws *bitgetsdk.PublicWSClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &marketDataClient{rest: rest, ws: ws, provider: provider, clk: clk, stream: wsstream.New[contract.MarketEnvelope](1024)}
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) instrument(id model.InstrumentID) (*model.Instrument, string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, "", fmt.Errorf("bitget: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return nil, "", err
	}
	return inst, category, nil
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, category, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	book, err := c.rest.GetOrderBook(ctx, category, inst.VenueSymbol, depth)
	if err != nil {
		return nil, err
	}
	return &model.OrderBook{InstrumentID: id, Bids: bookLevels(book.Bids), Asks: bookLevels(book.Asks), Timestamp: timeFromMillisString(string(book.TS))}, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	inst, category, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	rows, err := c.rest.GetCandles(ctx, category, inst.VenueSymbol, interval, "", 0, 0, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(rows))
	for _, row := range rows {
		if bar, ok := barFromBitgetCandle(id, interval, row); ok {
			out = append(out, bar)
		}
	}
	return out, nil
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, bitgetWSArg(category, "books", inst.VenueSymbol), func(payload []byte) {
		msg, err := bitgetsdk.DecodeOrderBookMessage(payload)
		if err != nil || len(msg.Data) == 0 {
			return
		}
		row := msg.Data[0]
		c.emit(contract.BookEvent{Book: model.OrderBook{InstrumentID: id, Bids: bookLevels(row.Bids), Asks: bookLevels(row.Asks), Sequence: row.Seq, Timestamp: timeFromMillisString(row.TS)}})
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, bitgetWSArg(category, "ticker", inst.VenueSymbol), func(payload []byte) {
		if quote, ok := quoteFromTickerPayload(id, payload, c.clk.Now()); ok {
			c.emit(contract.QuoteEvent{Quote: quote})
		}
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, bitgetWSArg(category, "trade", inst.VenueSymbol), func(payload []byte) {
		for _, trade := range tradesFromPayload(id, payload, c.clk.Now()) {
			c.emit(contract.TradeEvent{Trade: trade})
		}
	})
}

func (c *marketDataClient) subscribe(ctx context.Context, arg bitgetsdk.WSArg, handler func([]byte)) error {
	if c.ws == nil {
		return fmt.Errorf("bitget: public ws not configured: %w", errs.ErrNotSupported)
	}
	return c.ws.Subscribe(ctx, arg, func(payload json.RawMessage) { handler(payload) })
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Market: true},
			{Kind: enums.KindPerp, Market: true},
		},
		Streaming: contract.StreamCapabilities{Market: c.ws != nil},
	}
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }
func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}
func (c *marketDataClient) Close() error {
	if c.ws != nil {
		_ = c.ws.Close()
	}
	c.stream.Close()
	return nil
}

func bitgetWSArg(category, channel, symbol string) bitgetsdk.WSArg {
	return bitgetsdk.WSArg{InstType: category, Channel: channel, InstID: symbol}
}

func bookLevels(raw [][]bitgetsdk.NumberString) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, level := range raw {
		if len(level) < 2 {
			continue
		}
		out = append(out, model.BookLevel{Price: dec(string(level[0])), Quantity: dec(string(level[1]))})
	}
	return out
}

func barFromBitgetCandle(id model.InstrumentID, interval string, row bitgetsdk.Candle) (model.Bar, bool) {
	if row[0] == "" {
		return model.Bar{}, false
	}
	openTime := timeFromMillisString(string(row[0]))
	return model.Bar{InstrumentID: id, Interval: interval, Open: dec(string(row[1])), High: dec(string(row[2])), Low: dec(string(row[3])), Close: dec(string(row[4])), Volume: dec(string(row[5])), OpenTime: openTime}, true
}

func quoteFromTickerPayload(id model.InstrumentID, payload []byte, fallback time.Time) (model.QuoteTick, bool) {
	var msg struct {
		Data []struct {
			Bid1Price string `json:"bid1Price"`
			Bid1Size  string `json:"bid1Size"`
			Ask1Price string `json:"ask1Price"`
			Ask1Size  string `json:"ask1Size"`
			TS        string `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Data) == 0 {
		return model.QuoteTick{}, false
	}
	row := msg.Data[0]
	return model.QuoteTick{InstrumentID: id, BidPrice: dec(row.Bid1Price), BidSize: dec(row.Bid1Size), AskPrice: dec(row.Ask1Price), AskSize: dec(row.Ask1Size), Timestamp: firstNonZeroTime(timeFromMillisString(row.TS), fallback)}, true
}

func tradesFromPayload(id model.InstrumentID, payload []byte, fallback time.Time) []model.TradeTick {
	var msg struct {
		Data []struct {
			ExecID string `json:"execId"`
			Price  string `json:"price"`
			Size   string `json:"size"`
			Side   string `json:"side"`
			TS     string `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil
	}
	out := make([]model.TradeTick, 0, len(msg.Data))
	for _, row := range msg.Data {
		out = append(out, model.TradeTick{InstrumentID: id, Price: dec(row.Price), Quantity: dec(row.Size), AggressorSide: sideFromBitget(row.Side), TradeID: row.ExecID, Timestamp: firstNonZeroTime(timeFromMillisString(row.TS), fallback)})
	}
	return out
}
