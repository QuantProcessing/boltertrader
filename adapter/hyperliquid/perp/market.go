package perp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

type marketDataClient struct {
	rest     *sdkperp.Client
	ws       *sdkperp.WebsocketClient
	provider *instruments.Registry
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]

	connOnce sync.Once
	connErr  error
}

func newMarketDataClient(rest *sdkperp.Client, ws *sdkperp.WebsocketClient, provider *instruments.Registry, clk clock.Clock) *marketDataClient {
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
		return "", fmt.Errorf("hyperliquid perp: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
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

func hlLevels(levels [][]sdkperp.L2Level, side int) []model.BookLevel {
	if side >= len(levels) {
		return nil
	}
	out := make([]model.BookLevel, 0, len(levels[side]))
	for _, lvl := range levels[side] {
		out = append(out, model.BookLevel{Price: dec(lvl.Px), Quantity: dec(lvl.Sz)})
	}
	return out
}

func wsHLLevels(levels [][]sdk.WsLevel, side int) []model.BookLevel {
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
		return nil, fmt.Errorf("hyperliquid perp: unsupported interval %q: %w", interval, errs.ErrNotSupported)
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

type FundingRateSnapshot struct {
	InstrumentID model.InstrumentID
	Rate         decimal.Decimal
	MarkPrice    decimal.Decimal
	OraclePrice  decimal.Decimal
	Premium      decimal.Decimal
	Timestamp    time.Time
}

func (c *marketDataClient) FundingRate(ctx context.Context, id model.InstrumentID) (*FundingRateSnapshot, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	dex, coin := dexAndCoin(symbol)
	meta, err := c.rest.GetMetaAndAssetCtxsForDex(ctx, dex)
	if err != nil {
		return nil, err
	}
	for i, uni := range meta.Meta.Universe {
		if uni.Name != coin && uni.Name != symbol {
			continue
		}
		if i >= len(meta.AssetCtxs) {
			return nil, fmt.Errorf("hyperliquid perp: asset context not found for %s", symbol)
		}
		ctx := meta.AssetCtxs[i]
		return &FundingRateSnapshot{
			InstrumentID: id,
			Rate:         dec(ctx.Funding),
			MarkPrice:    dec(ctx.MarkPx),
			OraclePrice:  dec(ctx.OraclePx),
			Premium:      dec(ctx.Premium),
			Timestamp:    c.clk.Now(),
		}, nil
	}
	return nil, fmt.Errorf("hyperliquid perp: funding rate not found for %s", symbol)
}

func (c *marketDataClient) FundingHistory(ctx context.Context, id model.InstrumentID, start, end time.Time) ([]FundingRateSnapshot, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	dex, _ := dexAndCoin(symbol)
	rows, err := c.rest.GetFundingRateHistoryForDex(ctx, dex, symbol, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]FundingRateSnapshot, 0, len(rows))
	for _, row := range rows {
		out = append(out, FundingRateSnapshot{
			InstrumentID: id,
			Rate:         dec(row.FundingRate),
			Premium:      dec(row.Premium),
			Timestamp:    parseMillis(row.Time),
		})
	}
	return out, nil
}

func dexAndCoin(venueSymbol string) (string, string) {
	dex, coin, ok := strings.Cut(venueSymbol, ":")
	if !ok {
		return "", venueSymbol
	}
	return dex, coin
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeL2Book(symbol, func(book sdk.WsL2Book) {
			c.emit(contract.BookEvent{Book: model.OrderBook{
				InstrumentID: id,
				Bids:         wsHLLevels(book.Levels, 0),
				Asks:         wsHLLevels(book.Levels, 1),
				Timestamp:    parseMillis(book.Time),
			}})
		})
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeBbo(symbol, func(bbo sdk.WsBbo) {
			quote := model.QuoteTick{InstrumentID: id, Timestamp: parseMillis(bbo.Time)}
			if len(bbo.Bbo) > 0 {
				quote.BidPrice = dec(bbo.Bbo[0].Px)
				quote.BidSize = dec(bbo.Bbo[0].Sz)
			}
			if len(bbo.Bbo) > 1 {
				quote.AskPrice = dec(bbo.Bbo[1].Px)
				quote.AskSize = dec(bbo.Bbo[1].Sz)
			}
			c.emit(contract.QuoteEvent{Quote: quote})
		})
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(symbol string) error {
		return c.ws.SubscribeTrades(symbol, func(trades []sdk.WsTrade) {
			for _, tr := range trades {
				c.emit(contract.TradeEvent{Trade: model.TradeTick{
					InstrumentID:  id,
					Price:         dec(tr.Px),
					Quantity:      dec(tr.Sz),
					AggressorSide: sideFromHL(tr.Side),
					TradeID:       fmt.Sprint(tr.Tid),
					Timestamp:     parseMillis(tr.Time),
				}})
			}
		})
	})
}

func (c *marketDataClient) subscribe(id model.InstrumentID, fn func(symbol string) error) error {
	if c.ws == nil {
		return fmt.Errorf("hyperliquid perp: market websocket not configured: %w", errs.ErrNotSupported)
	}
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("hyperliquid perp: market websocket connect: %w", c.connErr)
	}
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	return fn(symbol)
}

func (c *marketDataClient) Connected() bool {
	if c.ws == nil || c.ws.WebsocketClient == nil {
		return false
	}
	c.ws.Mu.RLock()
	defer c.ws.Mu.RUnlock()
	return c.ws.Conn != nil
}

func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if c.ws == nil {
		return fmt.Errorf("hyperliquid perp: market websocket not configured: %w", errs.ErrNotSupported)
	}
	if c.Connected() {
		return nil
	}
	if err := c.ws.Connect(); err != nil {
		return err
	}
	return nil
}

func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }

func (c *marketDataClient) Close() error {
	c.stream.Close()
	return nil
}
