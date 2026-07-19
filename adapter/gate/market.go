package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

type marketDataClient struct {
	rest      *gatesdk.Client
	spotWS    *gatesdk.WSClient
	futuresWS *gatesdk.WSClient
	provider  *instrumentProvider
	clk       clock.Clock
	scope     []enums.InstrumentKind
	stream    *wsstream.Stream[contract.MarketEnvelope]
}

func newMarketDataClient(rest *gatesdk.Client, spotWS, futuresWS *gatesdk.WSClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &marketDataClient{rest: rest, spotWS: spotWS, futuresWS: futuresWS, provider: provider, clk: clk, scope: gateTradingKinds(), stream: wsstream.New[contract.MarketEnvelope](1024)}
}

func (c *marketDataClient) withScope(scope []enums.InstrumentKind) *marketDataClient {
	c.scope = gateKinds(scope)
	return c
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("gate: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	if inst.ID.Kind == enums.KindPerp {
		book, err := c.rest.GetFuturesOrderBook(ctx, gatesdk.SettleUSDT, inst.VenueSymbol, depth, true)
		if err != nil {
			return nil, err
		}
		return &model.OrderBook{InstrumentID: id, Bids: futuresBookLevels(book.Bids), Asks: futuresBookLevels(book.Asks), Sequence: book.ID, Timestamp: timeFromSecondsString(string(book.Update))}, nil
	}
	book, err := c.rest.GetSpotOrderBook(ctx, inst.VenueSymbol, depth, true)
	if err != nil {
		return nil, err
	}
	return &model.OrderBook{InstrumentID: id, Bids: bookLevels(book.Bids), Asks: bookLevels(book.Asks), Sequence: book.ID, Timestamp: timeFromMillis(book.Update)}, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	if inst.ID.Kind == enums.KindPerp {
		rows, err := c.rest.ListFuturesCandlesticks(ctx, gatesdk.SettleUSDT, inst.VenueSymbol, interval, limit)
		if err != nil {
			return nil, err
		}
		out := make([]model.Bar, 0, len(rows))
		for _, row := range rows {
			if bar, ok := barFromGateFuturesCandle(id, interval, row); ok {
				out = append(out, bar)
			}
		}
		return out, nil
	}
	rows, err := c.rest.ListSpotCandlesticks(ctx, inst.VenueSymbol, interval, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(rows))
	for _, row := range rows {
		if bar, ok := barFromGateSpotCandle(id, interval, row); ok {
			out = append(out, bar)
		}
	}
	return out, nil
}

func (c *marketDataClient) ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	if inst.ID.Kind != enums.KindPerp {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("gate: reference data only supported for perps: %w", errs.ErrNotSupported)
	}
	if c.rest == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("gate: rest client not configured: %w", errs.ErrNotSupported)
	}
	_, settle, err := productForInstrument(inst)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	tickers, err := c.rest.ListFuturesTickers(ctx, settle, inst.VenueSymbol)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	if len(tickers) == 0 {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("gate: empty futures ticker response for %s", inst.VenueSymbol)
	}
	contract, err := c.rest.GetFuturesContract(ctx, settle, inst.VenueSymbol)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	return referenceFromGateFutures(id, &tickers[0], contract, c.clk.Now()), nil
}

func (c *marketDataClient) SubscribeReference(ctx context.Context, id model.InstrumentID) error {
	snapshot, err := c.ReferenceSnapshot(ctx, id)
	if err != nil {
		return err
	}
	c.emitWithMeta(
		contract.ReferenceDataEvent{Snapshot: snapshot},
		contract.EventMeta{Source: contract.SourceAdapterREST, Flags: contract.EventFlagFromSnapshot},
	)
	return nil
}

func (c *marketDataClient) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	if inst.ID.Kind != enums.KindPerp {
		return model.OpenInterestSnapshot{}, fmt.Errorf("gate: open interest only supported for perps: %w", errs.ErrNotSupported)
	}
	if c.rest == nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("gate: rest client not configured: %w", errs.ErrNotSupported)
	}
	_, settle, err := productForInstrument(inst)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	tickers, err := c.rest.ListFuturesTickers(ctx, settle, inst.VenueSymbol)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	if len(tickers) == 0 {
		return model.OpenInterestSnapshot{}, fmt.Errorf("gate: empty futures ticker response for %s", inst.VenueSymbol)
	}
	return openInterestFromGateFutures(id, &tickers[0], c.clk.Now()), nil
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	channel := gatesdk.ChannelSpotOrderBook
	ws := c.spotWS
	if inst.ID.Kind == enums.KindPerp {
		channel = gatesdk.ChannelFuturesOrderBook
		ws = c.futuresWS
	}
	return c.subscribe(ctx, ws, channel, []string{inst.VenueSymbol, "100ms"}, func(payload []byte) {
		book, ok := orderBookFromPayload(id, payload)
		if ok {
			c.emit(contract.BookEvent{Book: book})
		}
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	channel := "spot.tickers"
	ws := c.spotWS
	if inst.ID.Kind == enums.KindPerp {
		channel = "futures.tickers"
		ws = c.futuresWS
	}
	return c.subscribe(ctx, ws, channel, []string{inst.VenueSymbol}, func(payload []byte) {
		if quote, ok := quoteFromTickerPayload(id, payload, c.clk.Now()); ok {
			c.emit(contract.QuoteEvent{Quote: quote})
		}
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	channel := gatesdk.ChannelSpotTrade
	ws := c.spotWS
	if inst.ID.Kind == enums.KindPerp {
		channel = gatesdk.ChannelFuturesTrade
		ws = c.futuresWS
	}
	return c.subscribe(ctx, ws, channel, []string{inst.VenueSymbol}, func(payload []byte) {
		for _, trade := range tradesFromPayload(id, payload, c.clk.Now()) {
			c.emit(contract.TradeEvent{Trade: trade})
		}
	})
}

func (c *marketDataClient) subscribe(ctx context.Context, ws *gatesdk.WSClient, channel string, payload []string, handler func([]byte)) error {
	if ws == nil {
		return fmt.Errorf("gate: public ws not configured: %w", errs.ErrNotSupported)
	}
	return ws.Subscribe(ctx, channel, payload, func(payload json.RawMessage) { handler(payload) })
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	products := make([]contract.ProductCapability, 0, len(c.scope))
	hasPerp := false
	for _, kind := range c.scope {
		if kind == enums.KindPerp {
			hasPerp = true
		}
		products = append(products, contract.ProductCapability{Kind: kind, Market: true})
	}
	reference := contract.ReferenceDataCapabilities{}
	if hasPerp {
		reference = contract.ReferenceDataCapabilities{
			CurrentFunding:      true,
			CurrentMarkPrice:    true,
			CurrentIndexPrice:   true,
			ReferencePolling:    true,
			CurrentOpenInterest: true,
		}
	}
	return contract.Capabilities{
		Venue:         VenueName,
		Products:      products,
		Streaming:     contract.StreamCapabilities{Market: c.spotWS != nil || c.futuresWS != nil},
		ReferenceData: reference,
	}
}

func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }
func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}
func (c *marketDataClient) emitWithMeta(ev contract.MarketEvent, meta contract.EventMeta) {
	c.stream.Emit(contract.NewMarketEnvelopeWithMeta(ev, meta))
}
func (c *marketDataClient) Close() error {
	if c.spotWS != nil {
		_ = c.spotWS.Close()
	}
	if c.futuresWS != nil {
		_ = c.futuresWS.Close()
	}
	c.stream.Close()
	return nil
}

func bookLevels(raw [][]gatesdk.NumberString) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, level := range raw {
		if len(level) < 2 {
			continue
		}
		out = append(out, model.BookLevel{Price: dec(string(level[0])), Quantity: dec(string(level[1]))})
	}
	return out
}

func futuresBookLevels(raw []gatesdk.FuturesOrderBookItem) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, level := range raw {
		out = append(out, model.BookLevel{Price: dec(level.Price), Quantity: decimal.NewFromInt(level.Size).Abs()})
	}
	return out
}

func barFromGateSpotCandle(id model.InstrumentID, interval string, row gatesdk.Candlestick) (model.Bar, bool) {
	if len(row) < 6 || row[0] == "" {
		return model.Bar{}, false
	}
	openTime := timeFromSecondsString(string(row[0]))
	return model.Bar{
		InstrumentID: id,
		Interval:     interval,
		Open:         dec(string(row[5])),
		High:         dec(string(row[3])),
		Low:          dec(string(row[4])),
		Close:        dec(string(row[2])),
		Volume:       dec(string(row[1])),
		OpenTime:     openTime,
	}, true
}

func barFromGateFuturesCandle(id model.InstrumentID, interval string, row gatesdk.FuturesCandlestick) (model.Bar, bool) {
	if row.Time == "" {
		return model.Bar{}, false
	}
	return model.Bar{
		InstrumentID: id,
		Interval:     interval,
		Open:         dec(string(row.Open)),
		High:         dec(string(row.High)),
		Low:          dec(string(row.Low)),
		Close:        dec(string(row.Close)),
		Volume:       dec(string(row.Volume)),
		OpenTime:     timeFromSecondsString(string(row.Time)),
	}, true
}

func orderBookFromPayload(id model.InstrumentID, payload []byte) (model.OrderBook, bool) {
	env, err := gatesdk.DecodeWSEnvelope(payload)
	if err != nil || len(env.Result) == 0 {
		return model.OrderBook{}, false
	}
	var book gatesdk.OrderBook
	if err := json.Unmarshal(env.Result, &book); err == nil && (len(book.Bids) > 0 || len(book.Asks) > 0) {
		return model.OrderBook{InstrumentID: id, Bids: bookLevels(book.Bids), Asks: bookLevels(book.Asks), Sequence: firstNonZeroInt64(book.ID, book.Update), Timestamp: timeFromMillis(book.Update)}, true
	}
	var futuresBook gatesdk.FuturesOrderBook
	if err := json.Unmarshal(env.Result, &futuresBook); err != nil {
		return model.OrderBook{}, false
	}
	return model.OrderBook{InstrumentID: id, Bids: futuresBookLevels(futuresBook.Bids), Asks: futuresBookLevels(futuresBook.Asks), Sequence: firstNonZeroInt64(futuresBook.ID, parseGateTimestampSeconds(string(futuresBook.Update))), Timestamp: timeFromSecondsString(string(futuresBook.Update))}, true
}

func quoteFromTickerPayload(id model.InstrumentID, payload []byte, fallback time.Time) (model.QuoteTick, bool) {
	env, err := gatesdk.DecodeWSEnvelope(payload)
	if err != nil || len(env.Result) == 0 {
		return model.QuoteTick{}, false
	}
	ticker, ok := firstTicker(env.Result)
	if !ok {
		return model.QuoteTick{}, false
	}
	return model.QuoteTick{
		InstrumentID: id,
		BidPrice:     dec(ticker.HighestBid),
		AskPrice:     dec(ticker.LowestAsk),
		Timestamp:    firstNonZeroTime(timeFromMillis(env.TimeMS), timeFromSeconds(env.Time), fallback),
	}, true
}

func tradesFromPayload(id model.InstrumentID, payload []byte, fallback time.Time) []model.TradeTick {
	env, err := gatesdk.DecodeWSEnvelope(payload)
	if err != nil || len(env.Result) == 0 {
		return nil
	}
	switch env.Channel {
	case gatesdk.ChannelSpotTrade:
		return spotTradesFromPayload(id, env, fallback)
	case gatesdk.ChannelFuturesTrade:
		return futuresTradesFromPayload(id, env, fallback)
	}
	if out := spotTradesFromPayload(id, env, fallback); len(out) > 0 {
		return out
	}
	return futuresTradesFromPayload(id, env, fallback)
}

func spotTradesFromPayload(id model.InstrumentID, env *gatesdk.WSEnvelope, fallback time.Time) []model.TradeTick {
	var trades []gatesdk.Trade
	if err := json.Unmarshal(env.Result, &trades); err != nil {
		var single gatesdk.Trade
		if err := json.Unmarshal(env.Result, &single); err != nil || single.ID == "" {
			return nil
		}
		trades = []gatesdk.Trade{single}
	}
	out := make([]model.TradeTick, 0, len(trades))
	for _, row := range trades {
		if row.ID == "" && row.Price == "" {
			continue
		}
		out = append(out, model.TradeTick{
			InstrumentID:  id,
			Price:         dec(row.Price),
			Quantity:      dec(row.Amount),
			AggressorSide: sideFromGate(row.Side),
			TradeID:       row.ID,
			Timestamp:     firstNonZeroTime(timeFromMillisString(row.CreateTimeMS), timeFromSecondsString(row.CreateTime), timeFromMillis(env.TimeMS), timeFromSeconds(env.Time), fallback),
		})
	}
	return out
}

func futuresTradesFromPayload(id model.InstrumentID, env *gatesdk.WSEnvelope, fallback time.Time) []model.TradeTick {
	var trades []gatesdk.FuturesTrade
	if err := json.Unmarshal(env.Result, &trades); err != nil {
		var single gatesdk.FuturesTrade
		if err := json.Unmarshal(env.Result, &single); err != nil || single.ID == 0 {
			return nil
		}
		trades = []gatesdk.FuturesTrade{single}
	}
	out := make([]model.TradeTick, 0, len(trades))
	for _, row := range trades {
		size := dec(string(row.Size))
		out = append(out, model.TradeTick{
			InstrumentID:  id,
			Price:         dec(row.Price),
			Quantity:      size.Abs(),
			AggressorSide: sideFromSignedDecimal(size),
			TradeID:       strconv.FormatInt(row.ID, 10),
			Timestamp:     firstNonZeroTime(timeFromSecondsString(string(row.CreateTime)), timeFromMillis(env.TimeMS), timeFromSeconds(env.Time), fallback),
		})
	}
	return out
}

func sideFromSignedDecimal(value decimal.Decimal) enums.OrderSide {
	switch value.Sign() {
	case -1:
		return enums.SideSell
	case 1:
		return enums.SideBuy
	default:
		return enums.SideUnknown
	}
}

func referenceFromGateFutures(id model.InstrumentID, ticker *gatesdk.FuturesTicker, contract *gatesdk.Contract, receivedAt time.Time) model.DerivativeReferenceSnapshot {
	s := model.DerivativeReferenceSnapshot{InstrumentID: id, Timestamp: receivedAt, ReceivedAt: receivedAt}
	if ticker != nil {
		if ticker.FundingRate != "" {
			s.FundingRate = dec(ticker.FundingRate)
			s.Fields = s.Fields.With(model.ReferenceHasFundingRate)
			setGateReferenceFieldTime(&s, model.ReferenceFieldFundingRate, receivedAt, receivedAt)
		}
		if ticker.MarkPrice != "" {
			s.MarkPrice = dec(ticker.MarkPrice)
			s.Fields = s.Fields.With(model.ReferenceHasMarkPrice)
			setGateReferenceFieldTime(&s, model.ReferenceFieldMarkPrice, receivedAt, receivedAt)
		}
		if ticker.IndexPrice != "" {
			s.IndexPrice = dec(ticker.IndexPrice)
			s.Fields = s.Fields.With(model.ReferenceHasIndexPrice)
			setGateReferenceFieldTime(&s, model.ReferenceFieldIndexPrice, receivedAt, receivedAt)
		}
	}
	if contract != nil {
		if !s.Fields.Has(model.ReferenceHasFundingRate) && contract.FundingRate != "" {
			s.FundingRate = dec(contract.FundingRate)
			s.Fields = s.Fields.With(model.ReferenceHasFundingRate)
			setGateReferenceFieldTime(&s, model.ReferenceFieldFundingRate, receivedAt, receivedAt)
		}
		if contract.FundingInterval > 0 {
			s.FundingInterval = gateFundingInterval(contract.FundingInterval)
			s.Fields = s.Fields.With(model.ReferenceHasFundingInterval)
			setGateReferenceFieldTime(&s, model.ReferenceFieldFundingInterval, receivedAt, receivedAt)
		}
		if contract.FundingNextApply > 0 {
			s.NextFundingTime = time.Unix(int64(contract.FundingNextApply), 0)
			s.Fields = s.Fields.With(model.ReferenceHasNextFundingTime)
			setGateReferenceFieldTime(&s, model.ReferenceFieldNextFundingTime, receivedAt, receivedAt)
		}
	}
	return s
}

func setGateReferenceFieldTime(s *model.DerivativeReferenceSnapshot, field model.ReferenceField, venueTime, receivedAt time.Time) {
	if venueTime.IsZero() {
		venueTime = receivedAt
	}
	s.FieldTimes.Set(field, model.FieldFreshness{Venue: venueTime, Received: receivedAt})
}

func gateFundingInterval(value int64) time.Duration {
	if value <= 0 {
		return 0
	}
	if value <= 24 {
		return time.Duration(value) * time.Hour
	}
	return time.Duration(value) * time.Second
}

func openInterestFromGateFutures(id model.InstrumentID, ticker *gatesdk.FuturesTicker, receivedAt time.Time) model.OpenInterestSnapshot {
	s := model.OpenInterestSnapshot{InstrumentID: id, Timestamp: receivedAt, ReceivedAt: receivedAt}
	if ticker == nil {
		return s
	}
	if ticker.TotalSize != "" {
		s.OpenInterest = dec(ticker.TotalSize)
		s.Fields = s.Fields.With(model.OpenInterestHasQuantity)
	}
	s.Unit = "contracts"
	s.Fields = s.Fields.With(model.OpenInterestHasUnit)
	return s
}

func firstTicker(raw json.RawMessage) (gatesdk.Ticker, bool) {
	var ticker gatesdk.Ticker
	if err := json.Unmarshal(raw, &ticker); err == nil && (ticker.CurrencyPair != "" || ticker.HighestBid != "" || ticker.LowestAsk != "") {
		return ticker, true
	}
	var tickers []gatesdk.Ticker
	if err := json.Unmarshal(raw, &tickers); err == nil && len(tickers) > 0 {
		return tickers[0], true
	}
	return gatesdk.Ticker{}, false
}

func timeFromSeconds(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

func timeFromMillisString(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return timeFromMillis(ms)
}
