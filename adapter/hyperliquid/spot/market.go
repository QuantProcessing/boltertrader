package spot

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

type marketDataClient struct {
	rest     *sdkspot.Client
	ws       *sdkspot.WebsocketClient
	provider *instruments.Registry
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]
}

func newMarketDataClient(rest *sdkspot.Client, provider *instruments.Registry, clk clock.Clock) *marketDataClient {
	return &marketDataClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEnvelope](1024),
	}
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) venueSymbol(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("hyperliquid spot: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst.VenueSymbol, nil
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	book, err := c.rest.L2Book(ctx, symbol)
	if err != nil {
		return nil, err
	}
	return &model.OrderBook{
		InstrumentID: id,
		Bids:         hlLevels(book.Levels, 0),
		Asks:         hlLevels(book.Levels, 1),
		Timestamp:    parseMillis(book.Time),
	}, nil
}

func hlLevels(levels [][]sdkspot.L2Level, side int) []model.BookLevel {
	if side >= len(levels) {
		return nil
	}
	out := make([]model.BookLevel, 0, len(levels[side]))
	for _, lvl := range levels[side] {
		out = append(out, model.BookLevel{Price: dec(lvl.Px), Quantity: dec(lvl.Sz)})
	}
	return out
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	dur, ok := intervalDuration(interval)
	if !ok {
		return nil, fmt.Errorf("hyperliquid spot: unsupported interval %q: %w", interval, errs.ErrNotSupported)
	}
	if limit <= 0 {
		limit = 100
	}
	end := c.clk.Now()
	start := end.Add(-dur * time.Duration(limit))
	candles, err := c.rest.CandleSnapshot(ctx, symbol, interval, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(candles))
	for _, cd := range candles {
		out = append(out, model.Bar{
			InstrumentID: id,
			Interval:     interval,
			Open:         dec(cd.O),
			High:         dec(cd.H),
			Low:          dec(cd.L),
			Close:        dec(cd.C),
			Volume:       dec(cd.V),
			OpenTime:     parseMillis(cd.T),
			CloseTime:    parseMillis(cd.TClose),
		})
	}
	return out, nil
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	return fmt.Errorf("hyperliquid spot: market websocket subscriptions are not wired yet: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	return fmt.Errorf("hyperliquid spot: quote websocket subscriptions are not wired yet: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return fmt.Errorf("hyperliquid spot: trade websocket subscriptions are not wired yet: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }

func (c *marketDataClient) Close() error {
	c.stream.Close()
	return nil
}
