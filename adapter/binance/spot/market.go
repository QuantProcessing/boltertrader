package spot

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
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

type marketDataClient struct {
	rest     *sdkspot.Client
	ws       *sdkspot.WsMarketClient
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]

	connOnce sync.Once
	connErr  error
}

func newMarketDataClient(rest *sdkspot.Client, ws *sdkspot.WsMarketClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	return &marketDataClient{
		rest:     rest,
		ws:       ws,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEnvelope](1024),
	}
}

var _ contract.Reconnectable = (*marketDataClient)(nil)

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) venueSymbol(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("binance spot: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
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
		Timestamp:    c.clk.Now(),
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

func barFromKline(id model.InstrumentID, interval string, row sdkspot.KlineResponse) (model.Bar, bool) {
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
		return c.ws.SubscribeLimitOrderBook(symbol, 20, "100ms", func(e *sdkspot.DepthEvent) error {
			c.emit(contract.BookEvent{Book: bookFromDepthEvent(id, e, c.clk.Now())})
			return nil
		})
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeBookTicker(symbol, func(e *sdkspot.BookTickerEvent) error {
			c.emit(contract.QuoteEvent{Quote: model.QuoteTick{
				InstrumentID: id,
				BidPrice:     dec(e.BestBidPrice),
				BidSize:      dec(e.BestBidQty),
				AskPrice:     dec(e.BestAskPrice),
				AskSize:      dec(e.BestAskQty),
				Timestamp:    c.clk.Now(),
			}})
			return nil
		})
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeAggTrade(symbol, func(e *sdkspot.AggTradeEvent) error {
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
		return fmt.Errorf("binance spot: market websocket not configured: %w", errs.ErrNotSupported)
	}
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("binance spot: market websocket connect: %w", c.connErr)
	}
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	return fn(symbol)
}

func (c *marketDataClient) Connected() bool {
	return c.ws != nil && c.ws.IsConnected()
}

func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if c.ws == nil {
		return fmt.Errorf("binance spot: market websocket not configured: %w", errs.ErrNotSupported)
	}
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("binance spot: market websocket connect: %w", c.connErr)
	}
	return waitConnected(ctx, c.ws.IsConnected)
}

func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }

func (c *marketDataClient) Close() error {
	if c.ws != nil {
		c.ws.Close()
	}
	c.stream.Close()
	return nil
}

func bookFromDepthEvent(id model.InstrumentID, e *sdkspot.DepthEvent, ts time.Time) model.OrderBook {
	return model.OrderBook{
		InstrumentID: id,
		Bids:         bookLevels(e.Bids),
		Asks:         bookLevels(e.Asks),
		Sequence:     e.FinalUpdateID,
		Timestamp:    ts,
	}
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
