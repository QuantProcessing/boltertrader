package perp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

// marketDataClient implements contract.MarketDataClient over the OKX REST + ws.
// The public ws is connected lazily on first subscribe (OKX's Subscribe writes
// directly to the connection and would panic if not connected).
type marketDataClient struct {
	rest     *okx.Client
	ws       *okx.WSClient // may be nil (REST-only)
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]

	connOnce sync.Once
	connErr  error
}

func newMarketDataClient(rest *okx.Client, ws *okx.WSClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	return &marketDataClient{
		rest:     rest,
		ws:       ws,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEnvelope](1024),
	}
}

// The market client exposes connection health and a manual reconnect so the
// runtime can drive node.Reconnect (force the link up, then reconcile the gap).
var _ contract.Reconnectable = (*marketDataClient)(nil)

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) instID(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("okx: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst.VenueSymbol, nil
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	instID, err := c.instID(id)
	if err != nil {
		return nil, err
	}
	var szp *int
	if depth > 0 {
		szp = &depth
	}
	books, err := c.rest.GetOrderBook(ctx, instID, szp)
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return &model.OrderBook{InstrumentID: id, Timestamp: c.clk.Now()}, nil
	}
	b := books[0]
	return &model.OrderBook{
		InstrumentID: id,
		Bids:         okxLevels(b.Bids),
		Asks:         okxLevels(b.Asks),
		Timestamp:    parseMillis(b.Ts),
	}, nil
}

// okxLevels parses OKX book levels ([price, size, _, _]).
func okxLevels(raw [][]string) []model.BookLevel {
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
	instID, err := c.instID(id)
	if err != nil {
		return nil, err
	}
	bar := interval
	var lp *int
	if limit > 0 {
		lp = &limit
	}
	candles, err := c.rest.GetCandles(ctx, instID, &bar, nil, nil, lp)
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(candles))
	for _, cd := range candles {
		// OKX candle: [ts, o, h, l, c, vol, volCcy, volCcyQuote, confirm]
		out = append(out, model.Bar{
			InstrumentID: id,
			Interval:     interval,
			Open:         dec(cd[1]),
			High:         dec(cd[2]),
			Low:          dec(cd[3]),
			Close:        dec(cd[4]),
			Volume:       dec(cd[5]),
			OpenTime:     parseMillis(cd[0]),
		})
	}
	return out, nil
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(instID string) error {
		return c.ws.SubscribeOrderBook(instID, func(b *okx.OrderBook, _ string) {
			c.emit(contract.BookEvent{Book: model.OrderBook{
				InstrumentID: id,
				Bids:         okxLevels(b.Bids),
				Asks:         okxLevels(b.Asks),
				Timestamp:    parseMillis(b.Ts),
			}})
		})
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	// OKX exposes top-of-book via the tickers channel.
	return c.subscribe(id, func(instID string) error {
		return c.ws.SubscribeTicker(instID, func(t *okx.Ticker) {
			c.emit(contract.QuoteEvent{Quote: model.QuoteTick{
				InstrumentID: id,
				BidPrice:     dec(t.BidPx),
				BidSize:      dec(t.BidSz),
				AskPrice:     dec(t.AskPx),
				AskSize:      dec(t.AskSz),
				Timestamp:    parseMillis(t.Ts),
			}})
		})
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(instID string) error {
		return c.ws.SubscribeTrades(instID, func(tr *okx.PublicTrade) {
			c.emit(contract.TradeEvent{Trade: model.TradeTick{
				InstrumentID:  id,
				Price:         dec(tr.Px),
				Quantity:      dec(tr.Sz),
				AggressorSide: sideFromOKX(tr.Side),
				TradeID:       tr.TradeId,
				Timestamp:     parseMillis(tr.Ts),
			}})
		})
	})
}

func (c *marketDataClient) subscribe(id model.InstrumentID, fn func(instID string) error) error {
	if c.ws == nil {
		return fmt.Errorf("okx: market websocket not configured: %w", errs.ErrNotSupported)
	}
	// OKX's Subscribe writes directly to the connection; connect once first.
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("okx: market websocket connect: %w", c.connErr)
	}
	instID, err := c.instID(id)
	if err != nil {
		return err
	}
	return fn(instID)
}

// Connected reports whether the public market websocket is currently up. A
// REST-only client (nil ws) reports false.
func (c *marketDataClient) Connected() bool {
	return c.ws != nil && c.ws.IsConnected()
}

// Reconnect ensures the public market websocket is connected, blocking until it
// is up or ctx is cancelled. The OKX transport auto-reconnects and re-subscribes
// on its own; this drives the initial connect (when subscriptions have not
// started yet) and waits out a transient drop, so the runtime can reconcile once
// the link is back. Returns ErrNotSupported for a REST-only client.
func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if c.ws == nil {
		return fmt.Errorf("okx: market websocket not configured: %w", errs.ErrNotSupported)
	}
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("okx: market websocket connect: %w", c.connErr)
	}
	return waitConnected(ctx, c.ws.IsConnected)
}

// emit blocks under backpressure, no-op after Close.
func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }

func (c *marketDataClient) Close() error {
	c.stream.Close()
	return nil
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
