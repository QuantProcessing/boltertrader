package spot

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

type marketDataClient struct {
	rest     *okx.Client
	ws       *okx.WSClient
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEvent]

	connOnce sync.Once
	connErr  error
}

func newMarketDataClient(rest *okx.Client, ws *okx.WSClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	return &marketDataClient{
		rest:     rest,
		ws:       ws,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEvent](1024),
	}
}

var _ contract.Reconnectable = (*marketDataClient)(nil)

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) instID(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("okx spot: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
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
		if len(cd) < 7 {
			continue
		}
		out = append(out, model.Bar{
			InstrumentID: id,
			Interval:     interval,
			Open:         dec(cd[1]),
			High:         dec(cd[2]),
			Low:          dec(cd[3]),
			Close:        dec(cd[4]),
			Volume:       dec(cd[5]),
			OpenTime:     parseMillis(cd[0]),
			CloseTime:    parseMillis(cd[6]),
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
		return fmt.Errorf("okx spot: market websocket not configured: %w", errs.ErrNotSupported)
	}
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("okx spot: market websocket connect: %w", c.connErr)
	}
	instID, err := c.instID(id)
	if err != nil {
		return err
	}
	return fn(instID)
}

func (c *marketDataClient) Connected() bool {
	return c.ws != nil && c.ws.IsConnected()
}

func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if c.ws == nil {
		return fmt.Errorf("okx spot: market websocket not configured: %w", errs.ErrNotSupported)
	}
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("okx spot: market websocket connect: %w", c.connErr)
	}
	return waitConnected(ctx, c.ws.IsConnected)
}

func (c *marketDataClient) emit(ev contract.MarketEvent) { c.stream.Emit(ev) }

func (c *marketDataClient) Events() <-chan contract.MarketEvent { return c.stream.C() }

func (c *marketDataClient) Close() error {
	c.stream.Close()
	return nil
}

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
