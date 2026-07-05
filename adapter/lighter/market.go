package lighter

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type marketDataClient struct {
	rest     *sdk.Client
	provider *registry
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]
}

func newMarketDataClient(rest *sdk.Client, provider *registry, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &marketDataClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEnvelope](256),
	}
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: venueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Market: true, Trading: true, Account: true},
			{Kind: enums.KindPerp, Market: true, Trading: true, Account: true},
		},
		Reports:   contract.ReportCapabilities{},
		Streaming: contract.StreamCapabilities{Market: false},
	}
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return nil, fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	if depth <= 0 {
		depth = 10
	}
	got, err := c.rest.GetOrderBookOrders(ctx, *inst.AssetIndex, int64(depth))
	if err != nil {
		return nil, err
	}
	book := &model.OrderBook{
		InstrumentID: id,
		Bids:         aggregateLighterBookLevels(got.Bids, true, depth),
		Asks:         aggregateLighterBookLevels(got.Asks, false, depth),
		Timestamp:    c.clk.Now(),
	}
	return book, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return nil, fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	if limit <= 0 {
		limit = 100
	}
	dur, ok := intervalDuration(interval)
	if !ok {
		return nil, fmt.Errorf("lighter: unsupported interval %q: %w", interval, errs.ErrNotSupported)
	}
	end := c.clk.Now()
	start := end.Add(-dur * time.Duration(limit+1))
	res, err := c.rest.GetCandlesticks(ctx, *inst.AssetIndex, interval, start.UnixMilli(), end.UnixMilli(), int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(res.Candlesticks))
	for _, candle := range res.Candlesticks {
		openTime := time.UnixMilli(candle.Timestamp)
		out = append(out, model.Bar{
			InstrumentID: id,
			Interval:     interval,
			Open:         decimal.NewFromFloat(candle.Open),
			High:         decimal.NewFromFloat(candle.High),
			Low:          decimal.NewFromFloat(candle.Low),
			Close:        decimal.NewFromFloat(candle.Close),
			Volume:       decimal.NewFromFloat(candle.Volume),
			OpenTime:     openTime,
			CloseTime:    openTime.Add(dur),
		})
	}
	return out, nil
}

func aggregateLighterBookLevels[T interface{ sdk.Ask | sdk.Bid }](orders []T, bids bool, depth int) []model.BookLevel {
	levels := make(map[string]decimal.Decimal)
	for _, order := range orders {
		var price, size string
		switch v := any(order).(type) {
		case sdk.Ask:
			price, size = v.Price, v.RemainingBaseAmount
		case sdk.Bid:
			price, size = v.Price, v.RemainingBaseAmount
		}
		if price == "" || size == "" {
			continue
		}
		levels[price] = levels[price].Add(dec(size))
	}
	out := make([]model.BookLevel, 0, len(levels))
	for price, size := range levels {
		if !size.IsPositive() {
			continue
		}
		out = append(out, model.BookLevel{Price: dec(price), Quantity: size})
	}
	sort.Slice(out, func(i, j int) bool {
		if bids {
			return out[i].Price.GreaterThan(out[j].Price)
		}
		return out[i].Price.LessThan(out[j].Price)
	})
	if depth > 0 && len(out) > depth {
		out = out[:depth]
	}
	return out
}

func intervalDuration(interval string) (time.Duration, bool) {
	if len(interval) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(interval[:len(interval)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch interval[len(interval)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	return fmt.Errorf("lighter: market websocket subscriptions are not wired yet: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	return fmt.Errorf("lighter: quote websocket subscriptions are not wired yet: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return fmt.Errorf("lighter: trade websocket subscriptions are not wired yet: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }

func (c *marketDataClient) Close() error {
	c.stream.Close()
	return nil
}
