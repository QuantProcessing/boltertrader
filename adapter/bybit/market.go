package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

type marketDataClient struct {
	rest     *bybitsdk.Client
	ws       map[string]*bybitsdk.PublicWSClient
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]
}

func newMarketDataClient(rest *bybitsdk.Client, ws map[string]*bybitsdk.PublicWSClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &marketDataClient{
		rest:     rest,
		ws:       ws,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEnvelope](1024),
	}
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) instrument(id model.InstrumentID) (*model.Instrument, string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, "", fmt.Errorf("bybit: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
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
	return &model.OrderBook{
		InstrumentID: id,
		Bids:         bookLevels(book.Bids),
		Asks:         bookLevels(book.Asks),
		Sequence:     book.U,
		Timestamp:    millisOrNow(book.TS, c.clk.Now()),
	}, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	inst, category, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	rows, err := c.rest.GetKlines(ctx, category, inst.VenueSymbol, interval, 0, 0, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(rows))
	for _, row := range rows {
		if bar, ok := barFromBybitCandle(id, interval, row); ok {
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
	return c.subscribe(ctx, category, "orderbook.50."+inst.VenueSymbol, func(payload []byte) {
		msg, err := bybitsdk.DecodeOrderBookMessage(payload)
		if err != nil {
			return
		}
		c.emit(contract.BookEvent{Book: model.OrderBook{
			InstrumentID: id,
			Bids:         bookLevels(msg.Data.Bids),
			Asks:         bookLevels(msg.Data.Asks),
			Sequence:     msg.Data.UpdateID,
			Timestamp:    millisOrNow(firstNonZeroInt64(msg.Data.CTS, msg.TS), c.clk.Now()),
		}})
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, category, "tickers."+inst.VenueSymbol, func(payload []byte) {
		quote, ok := quoteFromTickerPayload(id, payload, c.clk.Now())
		if ok {
			c.emit(contract.QuoteEvent{Quote: quote})
		}
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, category, "publicTrade."+inst.VenueSymbol, func(payload []byte) {
		for _, trade := range tradesFromPayload(id, payload, c.clk.Now()) {
			c.emit(contract.TradeEvent{Trade: trade})
		}
	})
}

func (c *marketDataClient) subscribe(ctx context.Context, category, topic string, handler func([]byte)) error {
	if c.ws == nil || c.ws[category] == nil {
		return fmt.Errorf("bybit: public ws for %s not configured: %w", category, errs.ErrNotSupported)
	}
	return c.ws[category].Subscribe(ctx, topic, func(payload json.RawMessage) {
		handler(payload)
	})
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Market: true},
			{Kind: enums.KindPerp, Market: true},
		},
		Reports:   contract.ReportCapabilities{OpenOrders: true, OpenOnlyNotFoundAmbiguous: true},
		Streaming: contract.StreamCapabilities{Market: len(c.ws) > 0},
	}
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }

func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}

func (c *marketDataClient) Close() error {
	for _, ws := range c.ws {
		_ = ws.Close()
	}
	c.stream.Close()
	return nil
}

func bookLevels(raw [][]bybitsdk.NumberString) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, level := range raw {
		if len(level) < 2 {
			continue
		}
		out = append(out, model.BookLevel{Price: dec(string(level[0])), Quantity: dec(string(level[1]))})
	}
	return out
}

func barFromBybitCandle(id model.InstrumentID, interval string, row bybitsdk.Candle) (model.Bar, bool) {
	if row[0] == "" {
		return model.Bar{}, false
	}
	openTime := timeFromMillisString(string(row[0]))
	return model.Bar{
		InstrumentID: id,
		Interval:     interval,
		Open:         dec(string(row[1])),
		High:         dec(string(row[2])),
		Low:          dec(string(row[3])),
		Close:        dec(string(row[4])),
		Volume:       dec(string(row[5])),
		OpenTime:     openTime,
	}, true
}

func quoteFromTickerPayload(id model.InstrumentID, payload []byte, fallback time.Time) (model.QuoteTick, bool) {
	var msg struct {
		TS   int64 `json:"ts"`
		Data struct {
			Bid1Price string `json:"bid1Price"`
			Bid1Size  string `json:"bid1Size"`
			Ask1Price string `json:"ask1Price"`
			Ask1Size  string `json:"ask1Size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return model.QuoteTick{}, false
	}
	if msg.Data.Bid1Price == "" && msg.Data.Ask1Price == "" {
		return model.QuoteTick{}, false
	}
	return model.QuoteTick{
		InstrumentID: id,
		BidPrice:     dec(msg.Data.Bid1Price),
		BidSize:      dec(msg.Data.Bid1Size),
		AskPrice:     dec(msg.Data.Ask1Price),
		AskSize:      dec(msg.Data.Ask1Size),
		Timestamp:    millisOrNow(msg.TS, fallback),
	}, true
}

func tradesFromPayload(id model.InstrumentID, payload []byte, fallback time.Time) []model.TradeTick {
	var msg struct {
		TS   int64 `json:"ts"`
		Data []struct {
			TradeID string `json:"i"`
			Price   string `json:"p"`
			Size    string `json:"v"`
			Side    string `json:"S"`
			Time    string `json:"T"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil
	}
	out := make([]model.TradeTick, 0, len(msg.Data))
	for _, row := range msg.Data {
		ts := timeFromMillisString(row.Time)
		if ts.IsZero() {
			ts = millisOrNow(msg.TS, fallback)
		}
		out = append(out, model.TradeTick{
			InstrumentID:  id,
			Price:         dec(row.Price),
			Quantity:      dec(row.Size),
			AggressorSide: sideFromBybit(row.Side),
			TradeID:       row.TradeID,
			Timestamp:     ts,
		})
	}
	return out
}

func millisOrNow(ms int64, fallback time.Time) time.Time {
	if ms > 0 {
		return time.UnixMilli(ms)
	}
	return fallback
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
