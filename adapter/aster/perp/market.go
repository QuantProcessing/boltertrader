package perp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
)

type marketDataClient struct {
	rest     *sdkperp.Client
	ws       perpMarketWebsocket
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]
}

type perpMarketWebsocket interface {
	Connect() error
	Close()
	IsConnected() bool
	SubscribeLimitOrderBook(symbol string, levels int, interval string, handler func(*sdkperp.WsDepthEvent) error) error
	SubscribeBookTicker(symbol string, handler func(*sdkperp.WsBookTickerEvent) error) error
	SubscribeAggTrade(symbol string, handler func(*sdkperp.WsAggTradeEvent) error) error
	SubscribeMarkPrice(symbol string, interval string, handler func(*sdkperp.WsMarkPriceEvent) error) error
}

func newMarketDataClient(rest *sdkperp.Client, ws any, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	marketWS, _ := ws.(perpMarketWebsocket)
	return &marketDataClient{rest: rest, ws: marketWS, provider: provider, clk: clk, stream: wsstream.New[contract.MarketEnvelope](1024)}
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindPerp, Market: true}},
		Reports:   contract.ReportCapabilities{},
		Streaming: contract.StreamCapabilities{Market: c.ws != nil},
		ReferenceData: contract.ReferenceDataCapabilities{
			CurrentFunding:      true,
			CurrentMarkPrice:    true,
			CurrentIndexPrice:   true,
			ReferenceStream:     c.ws != nil,
			ReferencePolling:    true,
			FundingHistory:      true,
			CurrentOpenInterest: true,
		},
	}
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	book, err := c.rest.Depth(ctx, inst.VenueSymbol, depth)
	if err != nil {
		return nil, mapAsterError(err)
	}
	bids, err := levels(book.Bids)
	if err != nil {
		return nil, fmt.Errorf("aster perp: bid levels: %w", err)
	}
	asks, err := levels(book.Asks)
	if err != nil {
		return nil, fmt.Errorf("aster perp: ask levels: %w", err)
	}
	return &model.OrderBook{InstrumentID: id, Bids: bids, Asks: asks, Sequence: book.LastUpdateID, Timestamp: firstNonZeroTime(timeFromMillis(book.TransactionTime), timeFromMillis(book.EventTime))}, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	_, err := c.provider.instrument(id)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("aster perp: bar conversion is not implemented in Story 5: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error) {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	if c.rest == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	ref, err := c.rest.GetFundingRate(ctx, inst.VenueSymbol)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, mapAsterError(err)
	}
	return referenceFromFundingRateData(id, inst.VenueSymbol, ref, c.clk.Now())
}

func (c *marketDataClient) SubscribeReference(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return err
	}
	snapshot, err := c.ReferenceSnapshot(ctx, id)
	if err != nil {
		return err
	}
	c.emitReference(snapshot, contract.SourceAdapterREST, contract.EventFlagFromSnapshot)
	if c.ws == nil {
		return nil
	}
	if err := c.ws.Connect(); err != nil {
		return fmt.Errorf("aster perp: market websocket connect: %w", err)
	}
	if err := waitConnected(ctx, c.ws.IsConnected); err != nil {
		return err
	}
	return c.ws.SubscribeMarkPrice(inst.VenueSymbol, "1s", func(e *sdkperp.WsMarkPriceEvent) error {
		snapshot, err := referenceFromMarkPriceEvent(id, inst.VenueSymbol, e, c.clk.Now())
		if err != nil {
			return err
		}
		c.emitReference(snapshot, contract.SourceAdapterStream, contract.EventFlagFromStream)
		return nil
	})
}

func referenceFromFundingRateData(id model.InstrumentID, symbol string, ref *sdkperp.FundingRateData, recv time.Time) (model.DerivativeReferenceSnapshot, error) {
	if ref == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot is required")
	}
	if ref.Symbol != symbol {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot symbol mismatch %q for %q", ref.Symbol, symbol)
	}
	if ref.Time <= 0 {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot timestamp is required")
	}
	ts := timeFromMillis(ref.Time)
	fresh := model.FieldFreshness{Venue: ts, Received: recv}
	var times model.ReferenceFieldTimes
	snapshot := model.DerivativeReferenceSnapshot{InstrumentID: id, Timestamp: ts, ReceivedAt: recv}
	if strings.TrimSpace(ref.LastFundingRate) != "" {
		value, err := parseRequiredSDKDecimal("lastFundingRate", ref.LastFundingRate)
		if err != nil {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot: %w", err)
		}
		snapshot.FundingRate = value
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasFundingRate)
		times.Set(model.ReferenceFieldFundingRate, fresh)
	}
	if ref.NextFundingTime > 0 {
		snapshot.NextFundingTime = timeFromMillis(ref.NextFundingTime)
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasNextFundingTime)
		times.Set(model.ReferenceFieldNextFundingTime, fresh)
	}
	if strings.TrimSpace(ref.MarkPrice) != "" {
		value, err := parseRequiredSDKDecimal("markPrice", ref.MarkPrice)
		if err != nil {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot: %w", err)
		}
		if !value.IsPositive() {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot: invalid markPrice %q", ref.MarkPrice)
		}
		snapshot.MarkPrice = value
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasMarkPrice)
		times.Set(model.ReferenceFieldMarkPrice, fresh)
	}
	if strings.TrimSpace(ref.IndexPrice) != "" {
		value, err := parseRequiredSDKDecimal("indexPrice", ref.IndexPrice)
		if err != nil {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot: %w", err)
		}
		if !value.IsPositive() {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot: invalid indexPrice %q", ref.IndexPrice)
		}
		snapshot.IndexPrice = value
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasIndexPrice)
		times.Set(model.ReferenceFieldIndexPrice, fresh)
	}
	if snapshot.Fields == 0 {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: reference snapshot contains no supported fields")
	}
	snapshot.FieldTimes = times
	return snapshot, nil
}

func referenceFromMarkPriceEvent(id model.InstrumentID, symbol string, e *sdkperp.WsMarkPriceEvent, recv time.Time) (model.DerivativeReferenceSnapshot, error) {
	if e == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: mark price event is required")
	}
	if e.Symbol != symbol {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: mark price event symbol mismatch %q for %q", e.Symbol, symbol)
	}
	if e.EventTime <= 0 {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: mark price event timestamp is required")
	}
	ts := timeFromMillis(e.EventTime)
	fresh := model.FieldFreshness{Venue: ts, Received: recv}
	var times model.ReferenceFieldTimes
	snapshot := model.DerivativeReferenceSnapshot{InstrumentID: id, Timestamp: ts, ReceivedAt: recv}
	if strings.TrimSpace(e.FundingRate) != "" {
		value, err := parseRequiredSDKDecimal("fundingRate", e.FundingRate)
		if err != nil {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: mark price event: %w", err)
		}
		snapshot.FundingRate = value
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasFundingRate)
		times.Set(model.ReferenceFieldFundingRate, fresh)
	}
	if e.NextFundingTime > 0 {
		snapshot.NextFundingTime = timeFromMillis(e.NextFundingTime)
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasNextFundingTime)
		times.Set(model.ReferenceFieldNextFundingTime, fresh)
	}
	if strings.TrimSpace(e.MarkPrice) != "" {
		value, err := parseRequiredSDKDecimal("markPrice", e.MarkPrice)
		if err != nil || !value.IsPositive() {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: mark price event: invalid markPrice %q", e.MarkPrice)
		}
		snapshot.MarkPrice = value
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasMarkPrice)
		times.Set(model.ReferenceFieldMarkPrice, fresh)
	}
	if strings.TrimSpace(e.IndexPrice) != "" {
		value, err := parseRequiredSDKDecimal("indexPrice", e.IndexPrice)
		if err != nil || !value.IsPositive() {
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: mark price event: invalid indexPrice %q", e.IndexPrice)
		}
		snapshot.IndexPrice = value
		snapshot.Fields = snapshot.Fields.With(model.ReferenceHasIndexPrice)
		times.Set(model.ReferenceFieldIndexPrice, fresh)
	}
	if snapshot.Fields == 0 {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("aster perp: mark price event contains no supported fields")
	}
	snapshot.FieldTimes = times
	return snapshot, nil
}

func (c *marketDataClient) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	if c.rest == nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	oi, err := c.rest.GetOpenInterest(ctx, inst.VenueSymbol)
	if err != nil {
		return model.OpenInterestSnapshot{}, mapAsterError(err)
	}
	if oi == nil || oi.Symbol != inst.VenueSymbol || oi.Time <= 0 {
		return model.OpenInterestSnapshot{}, fmt.Errorf("aster perp: open interest payload is invalid")
	}
	value, err := parseRequiredSDKDecimal("openInterest", oi.OpenInterest)
	if err != nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("aster perp: open interest: %w", err)
	}
	if value.IsNegative() {
		return model.OpenInterestSnapshot{}, fmt.Errorf("aster perp: open interest: invalid negative openInterest %q", oi.OpenInterest)
	}
	return model.OpenInterestSnapshot{InstrumentID: id, OpenInterest: value, Unit: inst.Base, Timestamp: timeFromMillis(oi.Time), ReceivedAt: c.clk.Now(), Fields: model.OpenInterestHasQuantity | model.OpenInterestHasUnit}, nil
}

func (c *marketDataClient) FundingHistory(ctx context.Context, id model.InstrumentID, query model.FundingRateHistoryQuery) ([]model.FundingRateHistoryEntry, error) {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	if query.Limit < 0 {
		return nil, fmt.Errorf("aster perp: funding history limit must be non-negative")
	}
	if !query.Start.IsZero() && !query.End.IsZero() && query.End.Before(query.Start) {
		return nil, fmt.Errorf("aster perp: funding history end before start")
	}
	rows, err := c.rest.GetFundingRateHistory(ctx, inst.VenueSymbol, millisOrZero(query.Start), millisOrZero(query.End), query.Limit)
	if err != nil {
		return nil, mapAsterError(err)
	}
	if query.Limit > 0 && len(rows) > query.Limit {
		return nil, fmt.Errorf("aster perp: funding history returned %d rows beyond limit %d", len(rows), query.Limit)
	}
	out := make([]model.FundingRateHistoryEntry, 0, len(rows))
	for i, row := range rows {
		entry, err := fundingHistoryEntryFromSDK(id, inst.VenueSymbol, row, query)
		if err != nil {
			return nil, fmt.Errorf("aster perp: funding history row %d: %w", i, err)
		}
		out = append(out, entry)
	}
	return out, nil
}

func (c *marketDataClient) OpenInterestHistory(context.Context, model.InstrumentID, model.OpenInterestHistoryQuery) ([]model.OpenInterestHistoryEntry, error) {
	return nil, fmt.Errorf("aster perp: open-interest history is not supported: %w", errs.ErrNotSupported)
}

func fundingHistoryEntryFromSDK(id model.InstrumentID, symbol string, row sdkperp.FundingRateHistoryEntry, query model.FundingRateHistoryQuery) (model.FundingRateHistoryEntry, error) {
	if row.Symbol != symbol {
		return model.FundingRateHistoryEntry{}, fmt.Errorf("symbol mismatch %q for %q", row.Symbol, symbol)
	}
	if row.FundingTime <= 0 {
		return model.FundingRateHistoryEntry{}, fmt.Errorf("fundingTime is required")
	}
	ts := timeFromMillis(row.FundingTime)
	if !query.Start.IsZero() && ts.Before(query.Start) {
		return model.FundingRateHistoryEntry{}, fmt.Errorf("fundingTime %s before query start %s", ts, query.Start)
	}
	if !query.End.IsZero() && ts.After(query.End) {
		return model.FundingRateHistoryEntry{}, fmt.Errorf("fundingTime %s after query end %s", ts, query.End)
	}
	rate, err := parseRequiredSDKDecimal("fundingRate", row.FundingRate)
	if err != nil {
		return model.FundingRateHistoryEntry{}, err
	}
	return model.FundingRateHistoryEntry{
		InstrumentID: id,
		FundingRate:  rate,
		Timestamp:    ts,
		Fields:       model.ReferenceHasFundingRate,
	}, nil
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(ctx, id, func(symbol string) error {
		return c.ws.SubscribeLimitOrderBook(symbol, 20, "250ms", func(e *sdkperp.WsDepthEvent) error {
			if e == nil || e.Symbol != symbol {
				return fmt.Errorf("aster perp: depth event symbol mismatch %q for %q", perpDepthSymbol(e), symbol)
			}
			book, err := bookFromDepthEvent(id, e)
			if err != nil {
				return err
			}
			c.emit(contract.BookEvent{Book: book})
			return nil
		})
	})
}
func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(ctx, id, func(symbol string) error {
		return c.ws.SubscribeBookTicker(symbol, func(e *sdkperp.WsBookTickerEvent) error {
			if e == nil || e.Symbol != symbol {
				return fmt.Errorf("aster perp: book ticker symbol mismatch %q for %q", perpBookTickerSymbol(e), symbol)
			}
			quote, err := quoteFromBookTicker(id, e)
			if err != nil {
				return err
			}
			c.emitQuote(quote, e)
			return nil
		})
	})
}
func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(ctx, id, func(symbol string) error {
		return c.ws.SubscribeAggTrade(symbol, func(e *sdkperp.WsAggTradeEvent) error {
			if e == nil || e.Symbol != symbol {
				return fmt.Errorf("aster perp: aggregate trade symbol mismatch %q for %q", perpAggTradeSymbol(e), symbol)
			}
			trade, err := tradeFromAggTradeEvent(id, e)
			if err != nil {
				return err
			}
			c.emit(contract.TradeEvent{Trade: trade})
			return nil
		})
	})
}
func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }
func (c *marketDataClient) Close() error {
	if c.ws != nil {
		c.ws.Close()
	}
	c.stream.Close()
	return nil
}

func (c *marketDataClient) subscribe(ctx context.Context, id model.InstrumentID, fn func(symbol string) error) error {
	if c.ws == nil {
		return fmt.Errorf("aster perp: market websocket not configured: %w", errs.ErrNotSupported)
	}
	inst, err := c.provider.instrument(id)
	if err != nil {
		return err
	}
	if err := c.ws.Connect(); err != nil {
		return fmt.Errorf("aster perp: market websocket connect: %w", err)
	}
	if err := waitConnected(ctx, c.ws.IsConnected); err != nil {
		return err
	}
	return fn(inst.VenueSymbol)
}

func (c *marketDataClient) Connected() bool { return c.ws != nil && c.ws.IsConnected() }

func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if c.ws == nil {
		return fmt.Errorf("aster perp: market websocket not configured: %w", errs.ErrNotSupported)
	}
	if err := c.ws.Connect(); err != nil {
		return fmt.Errorf("aster perp: market websocket connect: %w", err)
	}
	return waitConnected(ctx, c.ws.IsConnected)
}

func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}

func (c *marketDataClient) emitReference(snapshot model.DerivativeReferenceSnapshot, source contract.EventSource, flags contract.EventFlags) {
	c.stream.Emit(contract.NewMarketEnvelopeWithMeta(contract.ReferenceDataEvent{Snapshot: snapshot}, contract.EventMeta{
		Source:        source,
		Flags:         flags,
		TsVenue:       snapshot.Timestamp,
		TsAdapterRecv: snapshot.ReceivedAt,
		TsAdapterEmit: c.clk.Now(),
	}))
}

func (c *marketDataClient) emitQuote(quote model.QuoteTick, e *sdkperp.WsBookTickerEvent) {
	c.stream.Emit(contract.NewMarketEnvelopeWithMeta(contract.QuoteEvent{Quote: quote}, contract.EventMeta{
		EventID:  model.EventID(fmt.Sprintf("market|quote|%s|%s|%d", quote.InstrumentID.String(), e.Symbol, e.UpdateID)),
		Sequence: uint64(e.UpdateID),
		Source:   contract.SourceAdapterStream,
		Flags:    contract.EventFlagFromStream,
	}))
}

func bookFromDepthEvent(id model.InstrumentID, e *sdkperp.WsDepthEvent) (model.OrderBook, error) {
	if e == nil {
		return model.OrderBook{}, fmt.Errorf("aster perp: depth event is required")
	}
	bids, err := levels(e.Bids)
	if err != nil {
		return model.OrderBook{}, fmt.Errorf("aster perp: depth event bids: %w", err)
	}
	asks, err := levels(e.Asks)
	if err != nil {
		return model.OrderBook{}, fmt.Errorf("aster perp: depth event asks: %w", err)
	}
	return model.OrderBook{InstrumentID: id, Bids: bids, Asks: asks, Sequence: e.FinalUpdateID, Timestamp: timeFromMillis(e.EventTime)}, nil
}

func quoteFromBookTicker(id model.InstrumentID, e *sdkperp.WsBookTickerEvent) (model.QuoteTick, error) {
	if e == nil {
		return model.QuoteTick{}, fmt.Errorf("aster perp: book ticker event is required")
	}
	bidPrice, err := parseRequiredSDKDecimal("bidPrice", e.BestBidPrice)
	if err != nil || !bidPrice.IsPositive() {
		return model.QuoteTick{}, fmt.Errorf("aster perp: invalid bid price %q", e.BestBidPrice)
	}
	bidSize, err := parseRequiredSDKDecimal("bidSize", e.BestBidQty)
	if err != nil || bidSize.IsNegative() {
		return model.QuoteTick{}, fmt.Errorf("aster perp: invalid bid size %q", e.BestBidQty)
	}
	askPrice, err := parseRequiredSDKDecimal("askPrice", e.BestAskPrice)
	if err != nil || !askPrice.IsPositive() {
		return model.QuoteTick{}, fmt.Errorf("aster perp: invalid ask price %q", e.BestAskPrice)
	}
	askSize, err := parseRequiredSDKDecimal("askSize", e.BestAskQty)
	if err != nil || askSize.IsNegative() {
		return model.QuoteTick{}, fmt.Errorf("aster perp: invalid ask size %q", e.BestAskQty)
	}
	return model.QuoteTick{InstrumentID: id, BidPrice: bidPrice, BidSize: bidSize, AskPrice: askPrice, AskSize: askSize, Timestamp: timeFromMillis(e.EventTime)}, nil
}

func tradeFromAggTradeEvent(id model.InstrumentID, e *sdkperp.WsAggTradeEvent) (model.TradeTick, error) {
	if e == nil {
		return model.TradeTick{}, fmt.Errorf("aster perp: aggregate trade event is required")
	}
	if e.AggTradeID == 0 || e.TradeTime <= 0 {
		return model.TradeTick{}, fmt.Errorf("aster perp: aggregate trade id and timestamp are required")
	}
	price, err := parseRequiredSDKDecimal("price", e.Price)
	if err != nil || !price.IsPositive() {
		return model.TradeTick{}, fmt.Errorf("aster perp: invalid trade price %q", e.Price)
	}
	qty, err := parseRequiredSDKDecimal("quantity", e.Quantity)
	if err != nil || !qty.IsPositive() {
		return model.TradeTick{}, fmt.Errorf("aster perp: invalid trade quantity %q", e.Quantity)
	}
	side := enums.SideBuy
	if e.IsBuyerMaker {
		side = enums.SideSell
	}
	return model.TradeTick{InstrumentID: id, Price: price, Quantity: qty, AggressorSide: side, TradeID: strconv.FormatInt(e.AggTradeID, 10), Timestamp: timeFromMillis(e.TradeTime)}, nil
}

func perpDepthSymbol(e *sdkperp.WsDepthEvent) string {
	if e == nil {
		return ""
	}
	return e.Symbol
}

func perpBookTickerSymbol(e *sdkperp.WsBookTickerEvent) string {
	if e == nil {
		return ""
	}
	return e.Symbol
}

func perpAggTradeSymbol(e *sdkperp.WsAggTradeEvent) string {
	if e == nil {
		return ""
	}
	return e.Symbol
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

func levels(rows [][]string) ([]model.BookLevel, error) {
	out := make([]model.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("book level requires price and quantity")
		}
		price, err := parseRequiredSDKDecimal("price", row[0])
		if err != nil || !price.IsPositive() {
			return nil, fmt.Errorf("invalid book price %q", row[0])
		}
		qty, err := parseRequiredSDKDecimal("quantity", row[1])
		if err != nil || qty.IsNegative() {
			return nil, fmt.Errorf("invalid book quantity %q", row[1])
		}
		out = append(out, model.BookLevel{Price: price, Quantity: qty})
	}
	return out, nil
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return time.Time{}
}

func millisOrZero(ts time.Time) int64 {
	if ts.IsZero() {
		return 0
	}
	return ts.UnixMilli()
}
