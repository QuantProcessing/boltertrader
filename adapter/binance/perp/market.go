package perp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

// marketDataClient implements contract.MarketDataClient over the Binance REST +
// market WebSocket. Streaming subscriptions require a non-nil ws client; REST
// snapshot methods do not. The market ws is connected lazily on the first
// subscribe (Binance's Subscribe is a no-op until the route manager is started).
type marketDataClient struct {
	rest     *sdkperp.Client
	ws       *sdkperp.WsMarketClient // may be nil (REST-only construction)
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEvent]

	connOnce sync.Once
	connErr  error
}

func newMarketDataClient(rest *sdkperp.Client, ws *sdkperp.WsMarketClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	return &marketDataClient{
		rest:     rest,
		ws:       ws,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEvent](1024),
	}
}

// The market client exposes connection health and a manual reconnect so the
// runtime can drive node.Reconnect (force the link up, then reconcile the gap).
var _ contract.Reconnectable = (*marketDataClient)(nil)

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) venueSymbol(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("binance: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst.VenueSymbol, nil
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	resp, err := c.rest.Depth(ctx, symbol, depth)
	if err != nil {
		return nil, err
	}
	return &model.OrderBook{
		InstrumentID: id,
		Bids:         bookLevels(resp.Bids),
		Asks:         bookLevels(resp.Asks),
		Sequence:     resp.LastUpdateID,
		Timestamp:    time.UnixMilli(resp.T),
	}, nil
}

func bookLevels(raw [][]string) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, lvl := range raw {
		if len(lvl) < 2 {
			continue
		}
		out = append(out, model.BookLevel{Price: dec(lvl[0]), Quantity: dec(lvl[1])})
	}
	return out
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	rows, err := c.rest.Klines(ctx, symbol, interval, limit, 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(rows))
	for _, row := range rows {
		bar, ok := barFromKline(id, interval, row)
		if ok {
			out = append(out, bar)
		}
	}
	return out, nil
}

// barFromKline parses one Binance kline row ([]any) into a Bar. Binance
// returns: [openTime, open, high, low, close, volume, closeTime, ...].
func barFromKline(id model.InstrumentID, interval string, row sdkperp.KlineResponse) (model.Bar, bool) {
	if len(row) < 7 {
		return model.Bar{}, false
	}
	return model.Bar{
		InstrumentID: id,
		Interval:     interval,
		Open:         decAny(row[1]),
		High:         decAny(row[2]),
		Low:          decAny(row[3]),
		Close:        decAny(row[4]),
		Volume:       decAny(row[5]),
		OpenTime:     msAny(row[0]),
		CloseTime:    msAny(row[6]),
	}, true
}

// decAny parses a kline cell that may be a JSON string or number into a decimal.
func decAny(v any) decimal.Decimal {
	switch x := v.(type) {
	case string:
		return dec(x)
	case float64:
		return decimal.NewFromFloat(x)
	default:
		return decimal.Zero
	}
}

// msAny parses a kline timestamp cell (JSON number or string ms) into a time.
func msAny(v any) time.Time {
	switch x := v.(type) {
	case float64:
		return time.UnixMilli(int64(x))
	case string:
		return time.UnixMilli(dec(x).IntPart())
	default:
		return time.Time{}
	}
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeLimitOrderBook(symbol, 20, "250ms", func(e *sdkperp.WsDepthEvent) error {
			c.emit(contract.BookEvent{Book: bookFromDepthEvent(id, e)})
			return nil
		})
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeBookTicker(symbol, func(e *sdkperp.WsBookTickerEvent) error {
			c.emit(contract.QuoteEvent{Quote: model.QuoteTick{
				InstrumentID: id,
				BidPrice:     dec(e.BestBidPrice),
				BidSize:      dec(e.BestBidQty),
				AskPrice:     dec(e.BestAskPrice),
				AskSize:      dec(e.BestAskQty),
				Timestamp:    time.UnixMilli(e.EventTime),
			}})
			return nil
		})
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeAggTrade(symbol, func(e *sdkperp.WsAggTradeEvent) error {
			side := enums.SideBuy
			if e.IsBuyerMaker {
				side = enums.SideSell
			}
			c.emit(contract.TradeEvent{Trade: model.TradeTick{
				InstrumentID:  id,
				Price:         dec(e.Price),
				Quantity:      dec(e.Quantity),
				AggressorSide: side,
				TradeID:       itoa(e.AggTradeID),
				Timestamp:     time.UnixMilli(e.TradeTime),
			}})
			return nil
		})
	})
}

func (c *marketDataClient) subscribe(id model.InstrumentID, fn func(symbol string) error) error {
	if c.ws == nil {
		return fmt.Errorf("binance: market websocket not configured: %w", errs.ErrNotSupported)
	}
	// Binance's route-manager Subscribe is a no-op until Connect starts the
	// route, so connect once before the first subscription actually streams.
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("binance: market websocket connect: %w", c.connErr)
	}
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	return fn(symbol)
}

// Connected reports whether the public market websocket is currently up. A
// REST-only client (nil ws) reports false.
func (c *marketDataClient) Connected() bool {
	return c.ws != nil && c.ws.IsConnected()
}

// Reconnect ensures the public market websocket is connected, blocking until it
// is up or ctx is cancelled. The underlying transport auto-reconnects and
// re-subscribes on its own; this drives the initial connect (when subscriptions
// have not started yet) and waits out a transient drop, so the runtime can
// reconcile once the link is back. Returns ErrNotSupported for a REST-only
// client.
func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if c.ws == nil {
		return fmt.Errorf("binance: market websocket not configured: %w", errs.ErrNotSupported)
	}
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("binance: market websocket connect: %w", c.connErr)
	}
	return waitConnected(ctx, c.ws.IsConnected)
}

// emit pushes a market event. It blocks under backpressure (no silent drop) and
// is a no-op after Close.
func (c *marketDataClient) emit(ev contract.MarketEvent) { c.stream.Emit(ev) }

func (c *marketDataClient) Events() <-chan contract.MarketEvent { return c.stream.C() }

func (c *marketDataClient) Close() error {
	if c.ws != nil {
		c.ws.Close()
	}
	c.stream.Close()
	return nil
}

func bookFromDepthEvent(id model.InstrumentID, e *sdkperp.WsDepthEvent) model.OrderBook {
	return model.OrderBook{
		InstrumentID: id,
		Bids:         bookLevelsAny(e.Bids),
		Asks:         bookLevelsAny(e.Asks),
		Sequence:     e.FinalUpdateID,
		Timestamp:    time.UnixMilli(e.EventTime),
	}
}

// waitConnected blocks until isUp reports true or ctx is cancelled, polling at a
// short interval. Used by Reconnect to wait out the transport's background
// reconnect without racing its loop.
func waitConnected(ctx context.Context, isUp func() bool) error {
	if isUp() {
		return nil
	}
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if isUp() {
				return nil
			}
		}
	}
}

// bookLevelsAny parses depth levels delivered as [][]any ("price","qty").
func bookLevelsAny(raw [][]any) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, lvl := range raw {
		if len(lvl) < 2 {
			continue
		}
		out = append(out, model.BookLevel{Price: decAny(lvl[0]), Quantity: decAny(lvl[1])})
	}
	return out
}
