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

func (c *marketDataClient) ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error) {
	if c.rest == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("okx: rest client not configured: %w", errs.ErrNotSupported)
	}
	instID, err := c.instID(id)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	funding, err := c.rest.GetFundingRate(ctx, instID)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	mark, err := c.rest.GetMarkPrice(ctx, instTypeSwap, instID)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	index, err := c.rest.GetIndexTicker(ctx, instIDToNeutral(instID))
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	return referenceFromOKX(id, funding, mark, index, c.clk.Now()), nil
}

func (c *marketDataClient) SubscribeReference(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(id, func(instID string) error {
		indexInstID := instIDToNeutral(instID)
		if err := c.ws.SubscribeFundingRate(instID, func(f *okx.FundingRate) {
			c.emitWithMeta(
				contract.ReferenceDataEvent{Snapshot: referenceFromOKX(id, f, nil, nil, c.clk.Now())},
				contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream},
			)
		}); err != nil {
			return err
		}
		if err := c.ws.SubscribeMarkPrice(instID, func(m *okx.MarkPrice) {
			c.emitWithMeta(
				contract.ReferenceDataEvent{Snapshot: referenceFromOKX(id, nil, m, nil, c.clk.Now())},
				contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream},
			)
		}); err != nil {
			return err
		}
		return c.ws.SubscribeIndexTicker(indexInstID, func(i *okx.IndexTicker) {
			c.emitWithMeta(
				contract.ReferenceDataEvent{Snapshot: referenceFromOKX(id, nil, nil, i, c.clk.Now())},
				contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream},
			)
		})
	})
}

func (c *marketDataClient) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	if c.rest == nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("okx: rest client not configured: %w", errs.ErrNotSupported)
	}
	instID, err := c.instID(id)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	oi, err := c.rest.GetOpenInterest(ctx, instID)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	return openInterestFromOKX(id, oi, c.clk.Now()), nil
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

func (c *marketDataClient) emitWithMeta(ev contract.MarketEvent, meta contract.EventMeta) {
	c.stream.Emit(contract.NewMarketEnvelopeWithMeta(ev, meta))
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

func referenceFromOKX(id model.InstrumentID, funding *okx.FundingRate, mark *okx.MarkPrice, index *okx.IndexTicker, receivedAt time.Time) model.DerivativeReferenceSnapshot {
	s := model.DerivativeReferenceSnapshot{InstrumentID: id, ReceivedAt: receivedAt}
	if funding != nil {
		ts := firstNonZeroTime(parseMillis(funding.Ts), parseMillis(funding.FundingTime), receivedAt)
		if funding.FundingRate != "" {
			s.FundingRate = dec(funding.FundingRate)
			s.Fields = s.Fields.With(model.ReferenceHasFundingRate)
			setOKXReferenceFieldTime(&s, model.ReferenceFieldFundingRate, ts, receivedAt)
		}
		if funding.NextFundingTime != "" {
			s.NextFundingTime = parseMillis(funding.NextFundingTime)
			s.Fields = s.Fields.With(model.ReferenceHasNextFundingTime)
			setOKXReferenceFieldTime(&s, model.ReferenceFieldNextFundingTime, ts, receivedAt)
		}
		if funding.Premium != "" {
			s.Premium = dec(funding.Premium)
			s.Fields = s.Fields.With(model.ReferenceHasPremium)
			setOKXReferenceFieldTime(&s, model.ReferenceFieldPremium, ts, receivedAt)
		}
		s.Timestamp = latestOKXReferenceTime(s.Timestamp, ts)
	}
	if mark != nil {
		ts := firstNonZeroTime(parseMillis(mark.Ts), receivedAt)
		if mark.MarkPx != "" {
			s.MarkPrice = dec(mark.MarkPx)
			s.Fields = s.Fields.With(model.ReferenceHasMarkPrice)
			setOKXReferenceFieldTime(&s, model.ReferenceFieldMarkPrice, ts, receivedAt)
		}
		s.Timestamp = latestOKXReferenceTime(s.Timestamp, ts)
	}
	if index != nil {
		ts := firstNonZeroTime(parseMillis(index.Ts), receivedAt)
		if index.IdxPx != "" {
			s.IndexPrice = dec(index.IdxPx)
			s.Fields = s.Fields.With(model.ReferenceHasIndexPrice)
			setOKXReferenceFieldTime(&s, model.ReferenceFieldIndexPrice, ts, receivedAt)
		}
		s.Timestamp = latestOKXReferenceTime(s.Timestamp, ts)
	}
	if s.Timestamp.IsZero() {
		s.Timestamp = receivedAt
	}
	return s
}

func setOKXReferenceFieldTime(s *model.DerivativeReferenceSnapshot, field model.ReferenceField, venueTime, receivedAt time.Time) {
	if venueTime.IsZero() {
		venueTime = receivedAt
	}
	s.FieldTimes.Set(field, model.FieldFreshness{Venue: venueTime, Received: receivedAt})
}

func latestOKXReferenceTime(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.After(a)) {
		return b
	}
	return a
}

func openInterestFromOKX(id model.InstrumentID, oi *okx.OpenInterest, receivedAt time.Time) model.OpenInterestSnapshot {
	s := model.OpenInterestSnapshot{InstrumentID: id, Timestamp: receivedAt, ReceivedAt: receivedAt}
	if oi == nil {
		return s
	}
	if ts := parseMillis(oi.Ts); !ts.IsZero() {
		s.Timestamp = ts
	}
	if oi.OI != "" {
		s.OpenInterest = dec(oi.OI)
		s.Fields = s.Fields.With(model.OpenInterestHasQuantity)
	}
	if oi.OIUsd != "" {
		s.OpenInterestNotional = dec(oi.OIUsd)
		s.Fields = s.Fields.With(model.OpenInterestHasNotional)
	}
	if oi.OI != "" || oi.OICcy != "" {
		s.Unit = "contracts"
		s.Fields = s.Fields.With(model.OpenInterestHasUnit)
	}
	return s
}
