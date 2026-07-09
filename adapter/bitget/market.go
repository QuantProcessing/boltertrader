package bitget

import (
	"context"
	"encoding/json"
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
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

type marketDataClient struct {
	rest     *bitgetsdk.Client
	ws       *bitgetsdk.PublicWSClient
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]
}

func newMarketDataClient(rest *bitgetsdk.Client, ws *bitgetsdk.PublicWSClient, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &marketDataClient{rest: rest, ws: ws, provider: provider, clk: clk, stream: wsstream.New[contract.MarketEnvelope](1024)}
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) instrument(id model.InstrumentID) (*model.Instrument, string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, "", fmt.Errorf("bitget: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return nil, "", err
	}
	return inst, category, nil
}

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, category, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	book, err := c.rest.GetOrderBook(ctx, category, inst.VenueSymbol, depth)
	if err != nil {
		return nil, err
	}
	return &model.OrderBook{InstrumentID: id, Bids: bookLevels(book.Bids), Asks: bookLevels(book.Asks), Timestamp: timeFromMillisString(string(book.TS))}, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	inst, category, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	rows, err := c.rest.GetCandles(ctx, category, inst.VenueSymbol, interval, "", 0, 0, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.Bar, 0, len(rows))
	for _, row := range rows {
		if bar, ok := barFromBitgetCandle(id, interval, row); ok {
			out = append(out, bar)
		}
	}
	return out, nil
}

func (c *marketDataClient) ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error) {
	inst, category, err := c.instrument(id)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	if inst.ID.Kind != enums.KindPerp {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("bitget: reference data only supported for perps: %w", errs.ErrNotSupported)
	}
	if c.rest == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("bitget: rest client not configured: %w", errs.ErrNotSupported)
	}
	ticker, err := c.rest.GetTicker(ctx, category, inst.VenueSymbol)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	return referenceFromBitgetTicker(id, ticker, c.clk.Now()), nil
}

func (c *marketDataClient) SubscribeReference(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	if inst.ID.Kind != enums.KindPerp {
		return fmt.Errorf("bitget: reference data only supported for perps: %w", errs.ErrNotSupported)
	}
	snapshot, err := c.ReferenceSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if snapshot.Fields != 0 {
		c.emitWithMeta(
			contract.ReferenceDataEvent{Snapshot: snapshot},
			contract.EventMeta{Source: contract.SourceAdapterREST, Flags: contract.EventFlagFromSnapshot},
		)
	}
	return c.subscribe(ctx, bitgetWSArg(category, "ticker", inst.VenueSymbol), func(payload []byte) {
		if snapshot, ok := referenceFromBitgetTickerPayload(id, payload, c.clk.Now()); ok {
			c.emitWithMeta(
				contract.ReferenceDataEvent{Snapshot: snapshot},
				contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream},
			)
		}
	})
}

func (c *marketDataClient) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	inst, category, err := c.instrument(id)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	if inst.ID.Kind != enums.KindPerp {
		return model.OpenInterestSnapshot{}, fmt.Errorf("bitget: open interest only supported for perps: %w", errs.ErrNotSupported)
	}
	if c.rest == nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("bitget: rest client not configured: %w", errs.ErrNotSupported)
	}
	oi, err := c.rest.GetOpenInterest(ctx, inst.VenueSymbol, category)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	return openInterestFromBitget(id, inst.VenueSymbol, oi, c.clk.Now(), firstNonEmpty(inst.Base, "contracts")), nil
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, bitgetWSArg(category, "books", inst.VenueSymbol), func(payload []byte) {
		msg, err := bitgetsdk.DecodeOrderBookMessage(payload)
		if err != nil || len(msg.Data) == 0 {
			return
		}
		row := msg.Data[0]
		c.emit(contract.BookEvent{Book: model.OrderBook{InstrumentID: id, Bids: bookLevels(row.Bids), Asks: bookLevels(row.Asks), Sequence: row.Seq, Timestamp: timeFromMillisString(row.TS)}})
	})
}

func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, bitgetWSArg(category, "ticker", inst.VenueSymbol), func(payload []byte) {
		if quote, ok := quoteFromTickerPayload(id, payload, c.clk.Now()); ok {
			c.emit(contract.QuoteEvent{Quote: quote})
		}
	})
}

func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	inst, category, err := c.instrument(id)
	if err != nil {
		return err
	}
	return c.subscribe(ctx, bitgetWSArg(category, "trade", inst.VenueSymbol), func(payload []byte) {
		for _, trade := range tradesFromPayload(id, payload, c.clk.Now()) {
			c.emit(contract.TradeEvent{Trade: trade})
		}
	})
}

func (c *marketDataClient) subscribe(ctx context.Context, arg bitgetsdk.WSArg, handler func([]byte)) error {
	if c.ws == nil {
		return fmt.Errorf("bitget: public ws not configured: %w", errs.ErrNotSupported)
	}
	return c.ws.Subscribe(ctx, arg, func(payload json.RawMessage) { handler(payload) })
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	streaming := c.ws != nil
	reference := contract.ReferenceDataCapabilities{}
	if bitgetProviderHasKind(c.provider, enums.KindPerp) {
		reference = contract.ReferenceDataCapabilities{
			CurrentFunding:      true,
			CurrentMarkPrice:    true,
			CurrentIndexPrice:   true,
			ReferenceStream:     streaming,
			ReferencePolling:    !streaming,
			CurrentOpenInterest: true,
		}
	}
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Market: true},
			{Kind: enums.KindPerp, Market: true},
		},
		Streaming:     contract.StreamCapabilities{Market: streaming},
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
	if c.ws != nil {
		_ = c.ws.Close()
	}
	c.stream.Close()
	return nil
}

func bitgetWSArg(category, channel, symbol string) bitgetsdk.WSArg {
	return bitgetsdk.WSArg{InstType: category, Channel: channel, InstID: symbol}
}

func bookLevels(raw [][]bitgetsdk.NumberString) []model.BookLevel {
	out := make([]model.BookLevel, 0, len(raw))
	for _, level := range raw {
		if len(level) < 2 {
			continue
		}
		out = append(out, model.BookLevel{Price: dec(string(level[0])), Quantity: dec(string(level[1]))})
	}
	return out
}

func barFromBitgetCandle(id model.InstrumentID, interval string, row bitgetsdk.Candle) (model.Bar, bool) {
	if row[0] == "" {
		return model.Bar{}, false
	}
	openTime := timeFromMillisString(string(row[0]))
	return model.Bar{InstrumentID: id, Interval: interval, Open: dec(string(row[1])), High: dec(string(row[2])), Low: dec(string(row[3])), Close: dec(string(row[4])), Volume: dec(string(row[5])), OpenTime: openTime}, true
}

func quoteFromTickerPayload(id model.InstrumentID, payload []byte, fallback time.Time) (model.QuoteTick, bool) {
	var msg struct {
		Data []struct {
			Bid1Price string `json:"bid1Price"`
			Bid1Size  string `json:"bid1Size"`
			Ask1Price string `json:"ask1Price"`
			Ask1Size  string `json:"ask1Size"`
			TS        string `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Data) == 0 {
		return model.QuoteTick{}, false
	}
	row := msg.Data[0]
	return model.QuoteTick{InstrumentID: id, BidPrice: dec(row.Bid1Price), BidSize: dec(row.Bid1Size), AskPrice: dec(row.Ask1Price), AskSize: dec(row.Ask1Size), Timestamp: firstNonZeroTime(timeFromMillisString(row.TS), fallback)}, true
}

func tradesFromPayload(id model.InstrumentID, payload []byte, fallback time.Time) []model.TradeTick {
	var msg struct {
		Data []struct {
			ExecID string `json:"execId"`
			Price  string `json:"price"`
			Size   string `json:"size"`
			Side   string `json:"side"`
			TS     string `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil
	}
	out := make([]model.TradeTick, 0, len(msg.Data))
	for _, row := range msg.Data {
		out = append(out, model.TradeTick{InstrumentID: id, Price: dec(row.Price), Quantity: dec(row.Size), AggressorSide: sideFromBitget(row.Side), TradeID: row.ExecID, Timestamp: firstNonZeroTime(timeFromMillisString(row.TS), fallback)})
	}
	return out
}

func referenceFromBitgetTicker(id model.InstrumentID, ticker *bitgetsdk.Ticker, receivedAt time.Time) model.DerivativeReferenceSnapshot {
	s := model.DerivativeReferenceSnapshot{InstrumentID: id, ReceivedAt: receivedAt}
	if ticker == nil {
		s.Timestamp = receivedAt
		return s
	}
	ts := firstNonZeroTime(timeFromMillisString(ticker.Timestamp), receivedAt)
	s.Timestamp = ts
	if ticker.FundingRate != "" {
		s.FundingRate = dec(ticker.FundingRate)
		s.Fields = s.Fields.With(model.ReferenceHasFundingRate)
		setBitgetReferenceFieldTime(&s, model.ReferenceFieldFundingRate, ts, receivedAt)
	}
	if ticker.NextFundingTime != "" {
		s.NextFundingTime = timeFromMillisString(ticker.NextFundingTime)
		s.Fields = s.Fields.With(model.ReferenceHasNextFundingTime)
		setBitgetReferenceFieldTime(&s, model.ReferenceFieldNextFundingTime, ts, receivedAt)
	}
	if interval := bitgetFundingInterval(ticker.FundingRateInterval); interval > 0 {
		s.FundingInterval = interval
		s.Fields = s.Fields.With(model.ReferenceHasFundingInterval)
		setBitgetReferenceFieldTime(&s, model.ReferenceFieldFundingInterval, ts, receivedAt)
	}
	if ticker.MarkPrice != "" {
		s.MarkPrice = dec(ticker.MarkPrice)
		s.Fields = s.Fields.With(model.ReferenceHasMarkPrice)
		setBitgetReferenceFieldTime(&s, model.ReferenceFieldMarkPrice, ts, receivedAt)
	}
	if ticker.IndexPrice != "" {
		s.IndexPrice = dec(ticker.IndexPrice)
		s.Fields = s.Fields.With(model.ReferenceHasIndexPrice)
		setBitgetReferenceFieldTime(&s, model.ReferenceFieldIndexPrice, ts, receivedAt)
	}
	return s
}

func referenceFromBitgetTickerPayload(id model.InstrumentID, payload []byte, receivedAt time.Time) (model.DerivativeReferenceSnapshot, bool) {
	var msg struct {
		Data []bitgetsdk.Ticker `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Data) == 0 {
		return model.DerivativeReferenceSnapshot{}, false
	}
	snapshot := referenceFromBitgetTicker(id, &msg.Data[0], receivedAt)
	return snapshot, snapshot.Fields != 0
}

func setBitgetReferenceFieldTime(s *model.DerivativeReferenceSnapshot, field model.ReferenceField, venueTime, receivedAt time.Time) {
	if venueTime.IsZero() {
		venueTime = receivedAt
	}
	s.FieldTimes.Set(field, model.FieldFreshness{Venue: venueTime, Received: receivedAt})
}

func bitgetFundingInterval(value string) time.Duration {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), "h")
	if value == "" {
		return 0
	}
	hours, err := strconv.ParseFloat(value, 64)
	if err != nil || hours <= 0 {
		return 0
	}
	return time.Duration(hours * float64(time.Hour))
}

func openInterestFromBitget(id model.InstrumentID, venueSymbol string, oi *bitgetsdk.OpenInterest, receivedAt time.Time, unit string) model.OpenInterestSnapshot {
	s := model.OpenInterestSnapshot{InstrumentID: id, Timestamp: receivedAt, ReceivedAt: receivedAt}
	if oi == nil {
		return s
	}
	if ts := timeFromMillisString(oi.TS); !ts.IsZero() {
		s.Timestamp = ts
	}
	for _, row := range oi.List {
		if row.Symbol != "" && !strings.EqualFold(row.Symbol, venueSymbol) {
			continue
		}
		if row.Size != "" {
			s.OpenInterest = dec(row.Size)
			s.Fields = s.Fields.With(model.OpenInterestHasQuantity)
		}
		break
	}
	if unit != "" {
		s.Unit = unit
		s.Fields = s.Fields.With(model.OpenInterestHasUnit)
	}
	return s
}

func bitgetProviderHasKind(provider *instrumentProvider, kind enums.InstrumentKind) bool {
	if provider == nil {
		return false
	}
	for _, inst := range provider.All() {
		if inst != nil && inst.ID.Kind == kind {
			return true
		}
	}
	return false
}
