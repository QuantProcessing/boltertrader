package nado

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

type marketDataClient struct {
	rest             *sdk.Client
	provider         *instrumentProvider
	clk              clock.Clock
	productKind      enums.InstrumentKind
	stream           *wsstream.Stream[contract.MarketEnvelope]
	streamBackend    nadoMarketStreamBackend
	snapshotBackend  nadoMarketSnapshotBackend
	referenceBackend nadoReferenceBackend
	bookMu           sync.Mutex
	books            map[int64]*nadoBookRebuilder
}

func newMarketDataClient(rest *sdk.Client, provider *instrumentProvider, clk clock.Clock, kind enums.InstrumentKind) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	c := &marketDataClient{rest: rest, provider: provider, clk: clk, productKind: kind, stream: wsstream.New[contract.MarketEnvelope](256), books: make(map[int64]*nadoBookRebuilder)}
	if rest != nil {
		c.snapshotBackend = rest
		c.referenceBackend = rest
		if ws, err := sdk.NewWsMarketClient(context.Background(), rest.Profile()); err == nil {
			c.streamBackend = ws
		}
	}
	return c
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	caps := contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{{
			Kind:   selectedKind(c.productKind),
			Market: true,
		}},
		Streaming: contract.StreamCapabilities{Market: c.streamBackend != nil},
	}
	if selectedKind(c.productKind) == enums.KindPerp && c.referenceBackend != nil {
		caps.ReferenceData = contract.ReferenceDataCapabilities{
			CurrentFunding:      true,
			CurrentMarkPrice:    true,
			CurrentIndexPrice:   true,
			CurrentOraclePrice:  true,
			ReferenceStream:     c.streamBackend != nil,
			ReferencePolling:    true,
			CurrentOpenInterest: true,
		}
	}
	return caps
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider {
	return scopedInstrumentProvider{provider: c.provider, kind: c.productKind}
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, productID, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("nado: rest client not configured: %w", contract.ErrNotSupported)
	}
	if depth <= 0 {
		depth = 20
	}
	book, err := c.rest.GetOrderBook(ctx, inst.VenueSymbol, depth)
	if err != nil {
		return nil, err
	}
	if book == nil || book.ProductId != productID {
		return nil, fmt.Errorf("nado: order book product identity mismatch")
	}
	return orderBookFromV2(id, book)
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	return nil, fmt.Errorf("nado: bars are not part of Story 5 adapter foundations: %w", contract.ErrNotSupported)
}

func (c *marketDataClient) ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error) {
	_, productID, err := c.perpReferenceInstrument(id)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	if c.referenceBackend == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: reference backend not configured: %w", contract.ErrNotSupported)
	}

	funding, err := c.referenceBackend.GetFundingRate(ctx, productID)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	price, err := c.referenceBackend.GetPerpPrice(ctx, productID)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	oracles, err := c.referenceBackend.GetOraclePrices(ctx, []int64{productID})
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}

	received := c.clk.Now()
	return nadoReferenceSnapshot(id, productID, funding, price, oracles, received)
}

func (c *marketDataClient) SubscribeReference(ctx context.Context, id model.InstrumentID) error {
	_, productID, err := c.perpReferenceInstrument(id)
	if err != nil {
		return err
	}
	snapshot, err := c.ReferenceSnapshot(ctx, id)
	if err != nil {
		return err
	}
	c.emitReference(snapshot, contract.SourceAdapterREST, contract.EventFlagFromSnapshot)
	if c.streamBackend == nil {
		return nil
	}
	if err := c.streamBackend.SubscribeFundingRate(&productID, func(rate *sdk.FundingRate) {
		partial, err := nadoFundingRateSnapshot(id, productID, rate, c.clk.Now())
		if err == nil {
			c.emitReference(partial, contract.SourceAdapterStream, contract.EventFlagFromStream)
		}
	}); err != nil {
		return err
	}
	return c.streamBackend.Connect()
}

func (c *marketDataClient) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	inst, productID, err := c.perpReferenceInstrument(id)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	if c.referenceBackend == nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("nado: reference backend not configured: %w", contract.ErrNotSupported)
	}
	products, err := c.referenceBackend.GetAllProducts(ctx)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	product, err := exactNadoPerpProduct(products, productID)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	quantity, err := parseX18Required(product.State.OpenInterest, "open interest")
	if err != nil || quantity.IsNegative() {
		return model.OpenInterestSnapshot{}, fmt.Errorf("nado: invalid open interest %q", product.State.OpenInterest)
	}
	received := c.clk.Now()
	return model.OpenInterestSnapshot{
		InstrumentID: id,
		OpenInterest: quantity,
		Unit:         inst.Base,
		Timestamp:    received,
		ReceivedAt:   received,
		Fields:       model.OpenInterestHasQuantity.With(model.OpenInterestHasUnit),
	}, nil
}

func (c *marketDataClient) OpenInterestHistory(context.Context, model.InstrumentID, model.OpenInterestHistoryQuery) ([]model.OpenInterestHistoryEntry, error) {
	return nil, fmt.Errorf("nado: open-interest history is not supported: %w", contract.ErrNotSupported)
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inst, productID, err := c.instrument(id)
	if err != nil {
		return err
	}
	if c.streamBackend == nil {
		return fmt.Errorf("nado: market stream backend not configured: %w", contract.ErrNotSupported)
	}
	if c.snapshotBackend == nil {
		return fmt.Errorf("nado: market snapshot backend not configured: %w", contract.ErrNotSupported)
	}
	rebuilder := c.bookRebuilder(inst.ID, productID, 100)
	if err := c.streamBackend.SubscribeOrderBook(productID, func(book *sdk.OrderBook) {
		cbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		event, err := rebuilder.Apply(cbCtx, book)
		if err == nil {
			c.stream.Emit(event)
		}
	}); err != nil {
		return err
	}
	if err := c.streamBackend.Connect(); err != nil {
		return err
	}
	event, err := rebuilder.Bootstrap(ctx)
	if err == nil {
		c.stream.Emit(event)
	}
	return err
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inst, productID, err := c.instrument(id)
	if err != nil {
		return err
	}
	if c.streamBackend == nil {
		return fmt.Errorf("nado: market stream backend not configured: %w", contract.ErrNotSupported)
	}
	if err := c.streamBackend.SubscribeTicker(productID, func(ticker *sdk.Ticker) {
		event, err := c.quoteEvent(inst.ID, productID, ticker)
		if err == nil {
			c.stream.Emit(event)
		}
	}); err != nil {
		return err
	}
	return c.streamBackend.Connect()
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inst, productID, err := c.instrument(id)
	if err != nil {
		return err
	}
	if c.streamBackend == nil {
		return fmt.Errorf("nado: market stream backend not configured: %w", contract.ErrNotSupported)
	}
	if err := c.streamBackend.SubscribeTrades(productID, func(trade *sdk.Trade) {
		event, err := c.tradeEvent(inst.ID, productID, trade)
		if err == nil {
			c.stream.Emit(event)
		}
	}); err != nil {
		return err
	}
	return c.streamBackend.Connect()
}

type nadoMarketStreamBackend interface {
	Connect() error
	Close()
	IsConnected() bool
	SubscribeOrderBook(int64, func(*sdk.OrderBook)) error
	SubscribeTicker(int64, func(*sdk.Ticker)) error
	SubscribeTrades(int64, func(*sdk.Trade)) error
	SubscribeFundingRate(*int64, func(*sdk.FundingRate)) error
}

type nadoMarketSnapshotBackend interface {
	GetMarketLiquidity(context.Context, int64, int) (*sdk.MarketLiquidity, error)
}

type nadoReferenceBackend interface {
	GetAllProducts(context.Context) (*sdk.AllProductsResponse, error)
	GetFundingRate(context.Context, int64) (*sdk.FundingRateResponse, error)
	GetPerpPrice(context.Context, int64) (*sdk.PerpPriceResponse, error)
	GetOraclePrices(context.Context, []int64) ([]sdk.OraclePriceResponse, error)
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }
func (c *marketDataClient) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.streamBackend == nil {
		return nil
	}
	return c.streamBackend.Connect()
}

func (c *marketDataClient) Close() error {
	if c.streamBackend != nil {
		c.streamBackend.Close()
	}
	c.stream.Close()
	return nil
}

func (c *marketDataClient) Connected() bool {
	return c.streamBackend != nil && c.streamBackend.IsConnected()
}

func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.streamBackend == nil {
		return fmt.Errorf("nado: market stream backend not configured: %w", contract.ErrNotSupported)
	}
	return c.streamBackend.Connect()
}

func (c *marketDataClient) instrument(id model.InstrumentID) (*model.Instrument, int64, error) {
	if id.Kind != selectedKind(c.productKind) {
		return nil, 0, fmt.Errorf("nado: product %s is outside adapter scope %s: %w", id.Kind, selectedKind(c.productKind), contract.ErrNotSupported)
	}
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrUnknownInstrument, id)
	}
	productID, ok := c.provider.ProductID(id)
	if !ok {
		return nil, 0, fmt.Errorf("%w: missing product identity for %s", ErrUnknownInstrument, id)
	}
	return inst, productID, nil
}

func (c *marketDataClient) perpReferenceInstrument(id model.InstrumentID) (*model.Instrument, int64, error) {
	if selectedKind(c.productKind) != enums.KindPerp || id.Kind != enums.KindPerp {
		return nil, 0, fmt.Errorf("nado: derivative reference data is only supported for perp: %w", contract.ErrNotSupported)
	}
	return c.instrument(id)
}

func nadoReferenceSnapshot(id model.InstrumentID, productID int64, funding *sdk.FundingRateResponse, price *sdk.PerpPriceResponse, oracles []sdk.OraclePriceResponse, received time.Time) (model.DerivativeReferenceSnapshot, error) {
	if funding == nil || funding.ProductID != productID {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: funding product identity mismatch")
	}
	fundingTime := timeFromString(funding.UpdateTime)
	if fundingTime.IsZero() {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: funding source timestamp is required")
	}
	fundingRate, err := parseX18Required(funding.FundingRateX18, "funding rate")
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	if price == nil || price.ProductID != productID {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: perp price product identity mismatch")
	}
	priceTime := timeFromString(price.UpdateTime)
	if priceTime.IsZero() {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: perp price source timestamp is required")
	}
	mark, err := parseX18Required(price.MarkPriceX18, "mark price")
	if err != nil || !mark.IsPositive() {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: invalid mark price %q", price.MarkPriceX18)
	}
	index, err := parseX18Required(price.IndexPriceX18, "index price")
	if err != nil || !index.IsPositive() {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: invalid index price %q", price.IndexPriceX18)
	}
	if len(oracles) != 1 || oracles[0].ProductID != productID {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: oracle price product identity mismatch")
	}
	oracleTime := timeFromString(oracles[0].UpdateTime)
	if oracleTime.IsZero() {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: oracle source timestamp is required")
	}
	oracle, err := parseX18Required(oracles[0].OraclePriceX18, "oracle price")
	if err != nil || !oracle.IsPositive() {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: invalid oracle price %q", oracles[0].OraclePriceX18)
	}

	snapshot := model.DerivativeReferenceSnapshot{
		InstrumentID:    id,
		FundingRate:     fundingRate,
		FundingInterval: 24 * time.Hour,
		MarkPrice:       mark,
		IndexPrice:      index,
		OraclePrice:     oracle,
		Timestamp:       latestNadoTime(fundingTime, priceTime, oracleTime),
		ReceivedAt:      received,
		Fields: model.ReferenceHasFundingRate.
			With(model.ReferenceHasFundingInterval).
			With(model.ReferenceHasMarkPrice).
			With(model.ReferenceHasIndexPrice).
			With(model.ReferenceHasOraclePrice),
	}
	snapshot.FieldTimes.Set(model.ReferenceFieldFundingRate, model.FieldFreshness{Venue: fundingTime, Received: received})
	snapshot.FieldTimes.Set(model.ReferenceFieldFundingInterval, model.FieldFreshness{Venue: fundingTime, Received: received})
	snapshot.FieldTimes.Set(model.ReferenceFieldMarkPrice, model.FieldFreshness{Venue: priceTime, Received: received})
	snapshot.FieldTimes.Set(model.ReferenceFieldIndexPrice, model.FieldFreshness{Venue: priceTime, Received: received})
	snapshot.FieldTimes.Set(model.ReferenceFieldOraclePrice, model.FieldFreshness{Venue: oracleTime, Received: received})
	return snapshot, nil
}

func nadoFundingRateSnapshot(id model.InstrumentID, productID int64, rate *sdk.FundingRate, received time.Time) (model.DerivativeReferenceSnapshot, error) {
	if rate == nil || rate.ProductId != productID || rate.Type != "funding_rate" {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: funding stream product identity mismatch")
	}
	timestamp := timeFromString(rate.UpdateTime)
	if timestamp.IsZero() {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("nado: funding stream source timestamp is required")
	}
	value, err := parseX18Required(rate.FundingRateX18, "funding stream rate")
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	snapshot := model.DerivativeReferenceSnapshot{
		InstrumentID: id,
		FundingRate:  value,
		Timestamp:    timestamp,
		ReceivedAt:   received,
		Fields:       model.ReferenceHasFundingRate,
	}
	snapshot.FieldTimes.Set(model.ReferenceFieldFundingRate, model.FieldFreshness{Venue: timestamp, Received: received})
	return snapshot, nil
}

func exactNadoPerpProduct(products *sdk.AllProductsResponse, productID int64) (sdk.PerpProduct, error) {
	if products == nil {
		return sdk.PerpProduct{}, fmt.Errorf("nado: all_products response is required")
	}
	var found *sdk.PerpProduct
	for i := range products.PerpProducts {
		if products.PerpProducts[i].ProductID != productID {
			continue
		}
		if found != nil {
			return sdk.PerpProduct{}, fmt.Errorf("nado: duplicate perp product %d", productID)
		}
		found = &products.PerpProducts[i]
	}
	if found == nil {
		return sdk.PerpProduct{}, fmt.Errorf("nado: perp product %d missing from all_products", productID)
	}
	return *found, nil
}

func latestNadoTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
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

func (c *marketDataClient) bookRebuilder(id model.InstrumentID, productID int64, depth int) *nadoBookRebuilder {
	c.bookMu.Lock()
	defer c.bookMu.Unlock()
	if rebuilder := c.books[productID]; rebuilder != nil {
		return rebuilder
	}
	rebuilder := &nadoBookRebuilder{market: c, id: id, productID: productID, depth: depth, maxQueue: 128}
	c.books[productID] = rebuilder
	return rebuilder
}

func orderBookFromV2(id model.InstrumentID, book *sdk.OrderBookV2) (*model.OrderBook, error) {
	bids, err := floatLevels(book.Bids, true)
	if err != nil {
		return nil, err
	}
	asks, err := floatLevels(book.Asks, false)
	if err != nil {
		return nil, err
	}
	ts := time.UnixMilli(book.Timestamp)
	if book.Timestamp <= 0 {
		return nil, fmt.Errorf("nado: order book timestamp is required")
	}
	return &model.OrderBook{
		InstrumentID: id,
		Bids:         bids,
		Asks:         asks,
		Timestamp:    ts,
	}, nil
}

type nadoBookRebuilder struct {
	mu        sync.Mutex
	market    *marketDataClient
	id        model.InstrumentID
	productID int64
	depth     int
	bids      map[string]decimal.Decimal
	asks      map[string]decimal.Decimal
	lastMax   string
	lastTS    time.Time
	ready     bool
	queue     []*sdk.OrderBook
	maxQueue  int
	overflow  bool
}

func (b *nadoBookRebuilder) Reset(ctx context.Context, useSnapshotContinuity bool) error {
	snapshot, err := b.market.snapshotBackend.GetMarketLiquidity(ctx, b.productID, b.depth)
	if err != nil {
		return err
	}
	if snapshot == nil || snapshot.ProductID != b.productID {
		return fmt.Errorf("nado: market liquidity product identity mismatch")
	}
	ts := timeFromString(snapshot.Timestamp)
	if ts.IsZero() {
		return fmt.Errorf("nado: market liquidity timestamp is required")
	}
	bids, err := x18LevelMap(snapshot.Bids)
	if err != nil {
		return err
	}
	asks, err := x18LevelMap(snapshot.Asks)
	if err != nil {
		return err
	}
	b.bids = bids
	b.asks = asks
	if useSnapshotContinuity {
		b.lastMax = snapshot.Timestamp
	} else {
		b.lastMax = ""
	}
	b.lastTS = ts
	b.ready = true
	return nil
}

func (b *nadoBookRebuilder) Bootstrap(ctx context.Context) (contract.MarketEnvelope, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ready {
		return b.eventLocked(), nil
	}
	if err := b.Reset(ctx, false); err != nil {
		return contract.MarketEnvelope{}, err
	}
	if b.overflow {
		b.queue = nil
		b.overflow = false
		return b.eventLocked(), nil
	}
	for _, queued := range b.queue {
		queuedMax := timeFromString(queued.MaxTimestamp)
		if queuedMax.IsZero() {
			continue
		}
		if !queuedMax.After(b.lastTS) {
			b.lastMax = queued.MaxTimestamp
			continue
		}
		if b.lastMax != "" && queued.LastMaxTimestamp != b.lastMax {
			b.queue = nil
			if err := b.Reset(ctx, true); err != nil {
				return contract.MarketEnvelope{}, err
			}
			return b.eventLocked(), nil
		}
		if err := b.applyDiffLocked(queued); err != nil {
			b.queue = nil
			return contract.MarketEnvelope{}, err
		}
	}
	b.queue = nil
	return b.eventLocked(), nil
}

func (b *nadoBookRebuilder) Apply(ctx context.Context, diff *sdk.OrderBook) (contract.MarketEnvelope, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if diff == nil || diff.ProductId != b.productID {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: order book stream product identity mismatch")
	}
	minTS := timeFromString(diff.MinTimestamp)
	maxTS := timeFromString(diff.MaxTimestamp)
	if minTS.IsZero() || maxTS.IsZero() {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: order book diff timestamp is required")
	}
	if !b.ready {
		b.enqueueLocked(diff)
		return contract.MarketEnvelope{}, fmt.Errorf("nado: order book rebuilder queued pre-snapshot diff")
	}
	if !maxTS.After(b.lastTS) {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: duplicate or stale order book diff")
	}
	if b.lastMax != "" && diff.LastMaxTimestamp != b.lastMax {
		if err := b.Reset(ctx, true); err != nil {
			return contract.MarketEnvelope{}, err
		}
		return b.eventLocked(), nil
	}
	if err := b.applyDiffLocked(diff); err != nil {
		return contract.MarketEnvelope{}, err
	}
	return b.eventLocked(), nil
}

func (b *nadoBookRebuilder) enqueueLocked(diff *sdk.OrderBook) {
	if b.maxQueue <= 0 {
		b.maxQueue = 128
	}
	cp := *diff
	cp.Bids = append([][2]string(nil), diff.Bids...)
	cp.Asks = append([][2]string(nil), diff.Asks...)
	if len(b.queue) >= b.maxQueue {
		b.queue = nil
		b.overflow = true
		return
	}
	b.queue = append(b.queue, &cp)
}

func (b *nadoBookRebuilder) applyDiffLocked(diff *sdk.OrderBook) error {
	if err := applyX18DiffLevels(b.bids, diff.Bids); err != nil {
		return err
	}
	if err := applyX18DiffLevels(b.asks, diff.Asks); err != nil {
		return err
	}
	b.lastMax = diff.MaxTimestamp
	b.lastTS = timeFromString(diff.MaxTimestamp)
	return nil
}

func (b *nadoBookRebuilder) eventLocked() contract.MarketEnvelope {
	book := model.OrderBook{
		InstrumentID: b.id,
		Bids:         levelsFromMap(b.bids, true, b.depth),
		Asks:         levelsFromMap(b.asks, false, b.depth),
		Timestamp:    b.lastTS,
	}
	return contract.NewMarketEnvelopeWithMeta(contract.BookEvent{Book: book}, nadoEventMeta("market", "book", b.id.String(), fmt.Sprint(b.productID), b.lastMax))
}

func (c *marketDataClient) quoteEvent(id model.InstrumentID, productID int64, ticker *sdk.Ticker) (contract.MarketEnvelope, error) {
	if ticker == nil || ticker.ProductId != productID {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: quote stream product identity mismatch")
	}
	bidPrice, err := parseX18Required(ticker.BidPrice, "bid price")
	if err != nil {
		return contract.MarketEnvelope{}, err
	}
	bidQty, err := parseX18Required(ticker.BidQty, "bid quantity")
	if err != nil {
		return contract.MarketEnvelope{}, err
	}
	askPrice, err := parseX18Required(ticker.AskPrice, "ask price")
	if err != nil {
		return contract.MarketEnvelope{}, err
	}
	askQty, err := parseX18Required(ticker.AskQty, "ask quantity")
	if err != nil {
		return contract.MarketEnvelope{}, err
	}
	if bidPrice.IsNegative() || bidQty.IsNegative() || askPrice.IsNegative() || askQty.IsNegative() {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: quote stream prices and sizes must be non-negative")
	}
	if bidQty.IsPositive() && !bidPrice.IsPositive() {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: quote bid price must be positive when bid size is positive")
	}
	if askQty.IsPositive() && !askPrice.IsPositive() {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: quote ask price must be positive when ask size is positive")
	}
	ts := timeFromString(ticker.Timestamp)
	if ts.IsZero() {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: quote stream timestamp is required")
	}
	quote := model.QuoteTick{InstrumentID: id, BidPrice: bidPrice, BidSize: bidQty, AskPrice: askPrice, AskSize: askQty, Timestamp: ts}
	return contract.NewMarketEnvelopeWithMeta(contract.QuoteEvent{Quote: quote}, nadoEventMeta("market", "quote", id.String(), fmt.Sprint(productID), ticker.Timestamp)), nil
}

func (c *marketDataClient) tradeEvent(id model.InstrumentID, productID int64, trade *sdk.Trade) (contract.MarketEnvelope, error) {
	if trade == nil || trade.ProductId != productID {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: trade stream product identity mismatch")
	}
	price, err := parseX18Required(trade.Price, "trade price")
	if err != nil {
		return contract.MarketEnvelope{}, err
	}
	qty, err := parseX18Required(trade.TakerQty, "trade quantity")
	if err != nil {
		return contract.MarketEnvelope{}, err
	}
	if !price.IsPositive() || !qty.IsPositive() {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: trade stream price and quantity must be positive")
	}
	ts := timeFromString(trade.Timestamp)
	if ts.IsZero() {
		return contract.MarketEnvelope{}, fmt.Errorf("nado: trade stream timestamp is required")
	}
	side := enums.SideSell
	if trade.IsTakerBuyer {
		side = enums.SideBuy
	}
	tick := model.TradeTick{InstrumentID: id, Price: price, Quantity: qty.Abs(), AggressorSide: side, Timestamp: ts}
	return contract.NewMarketEnvelopeWithMeta(contract.TradeEvent{Trade: tick}, nadoEventMeta("market", "trade", id.String(), fmt.Sprint(productID), trade.Timestamp, trade.Price, trade.TakerQty)), nil
}

func x18StringLevels(levels [][2]string, bids bool) ([]model.BookLevel, error) {
	out := make([]model.BookLevel, 0, len(levels))
	for _, level := range levels {
		price, err := parseX18Required(level[0], "book level price")
		if err != nil {
			return nil, err
		}
		qty, err := parseX18Required(level[1], "book level quantity")
		if err != nil {
			return nil, err
		}
		if !price.IsPositive() || qty.IsNegative() {
			return nil, fmt.Errorf("nado: invalid order book level")
		}
		out = append(out, model.BookLevel{Price: price, Quantity: qty})
	}
	sortBookLevels(out, bids)
	return out, nil
}

func x18LevelMap(levels [][2]string) (map[string]decimal.Decimal, error) {
	out := make(map[string]decimal.Decimal, len(levels))
	for _, level := range levels {
		price, err := parseX18Required(level[0], "book level price")
		if err != nil {
			return nil, err
		}
		qty, err := parseX18Required(level[1], "book level quantity")
		if err != nil {
			return nil, err
		}
		if !price.IsPositive() || qty.IsNegative() {
			return nil, fmt.Errorf("nado: invalid order book level")
		}
		if qty.IsZero() {
			continue
		}
		out[price.String()] = qty
	}
	return out, nil
}

func applyX18DiffLevels(book map[string]decimal.Decimal, levels [][2]string) error {
	for _, level := range levels {
		price, err := parseX18Required(level[0], "book diff price")
		if err != nil {
			return err
		}
		qty, err := parseX18Required(level[1], "book diff quantity")
		if err != nil {
			return err
		}
		if !price.IsPositive() || qty.IsNegative() {
			return fmt.Errorf("nado: invalid order book diff level")
		}
		key := price.String()
		if qty.IsZero() {
			delete(book, key)
			continue
		}
		book[key] = qty
	}
	return nil
}

func levelsFromMap(values map[string]decimal.Decimal, bids bool, depth int) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(values))
	for priceString, qty := range values {
		price, err := decimal.NewFromString(priceString)
		if err != nil || qty.IsZero() {
			continue
		}
		out = append(out, model.BookLevel{Price: price, Quantity: qty})
	}
	sortBookLevels(out, bids)
	if depth > 0 && len(out) > depth {
		out = out[:depth]
	}
	return out
}

func floatLevels(levels [][2]float64, bids bool) ([]model.BookLevel, error) {
	out := make([]model.BookLevel, 0, len(levels))
	for _, level := range levels {
		if math.IsNaN(level[0]) || math.IsInf(level[0], 0) || math.IsNaN(level[1]) || math.IsInf(level[1], 0) || level[0] <= 0 || level[1] < 0 {
			return nil, fmt.Errorf("nado: invalid order book level")
		}
		out = append(out, model.BookLevel{
			Price:    decimal.NewFromFloat(level[0]),
			Quantity: decimal.NewFromFloat(level[1]),
		})
	}
	sortBookLevels(out, bids)
	return out, nil
}

func sortBookLevels(levels []model.BookLevel, bids bool) {
	sort.Slice(levels, func(i, j int) bool {
		if bids {
			return levels[i].Price.GreaterThan(levels[j].Price)
		}
		return levels[i].Price.LessThan(levels[j].Price)
	})
}

func selectedKind(kind enums.InstrumentKind) enums.InstrumentKind {
	if kind == enums.KindSpot || kind == enums.KindPerp {
		return kind
	}
	return enums.KindPerp
}
