package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	ws       *sdk.WebsocketClient
	provider *registry
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]

	connOnce sync.Once
	connErr  error
}

func newMarketDataClient(rest *sdk.Client, ws *sdk.WebsocketClient, provider *registry, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &marketDataClient{
		rest:     rest,
		ws:       ws,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.MarketEnvelope](256),
	}
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	reference := contract.ReferenceDataCapabilities{}
	if lighterProviderHasKind(c.provider, enums.KindPerp) {
		reference = contract.ReferenceDataCapabilities{
			CurrentFunding:      true,
			CurrentMarkPrice:    c.ws != nil,
			CurrentIndexPrice:   c.ws != nil,
			ReferenceStream:     c.ws != nil,
			ReferencePolling:    c.ws == nil,
			CurrentOpenInterest: true,
		}
	}
	return contract.Capabilities{
		Venue: venueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Market: true, Trading: true, Account: true},
			{Kind: enums.KindPerp, Market: true, Trading: true, Account: true},
		},
		Reports:       contract.ReportCapabilities{},
		Streaming:     contract.StreamCapabilities{Market: c.ws != nil},
		ReferenceData: reference,
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

func (c *marketDataClient) ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error) {
	inst, marketID, err := c.perpInstrument(id)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	if c.rest == nil {
		return model.DerivativeReferenceSnapshot{}, fmt.Errorf("lighter: rest client not configured: %w", errs.ErrNotSupported)
	}
	rate, err := c.rest.GetFundingRate(ctx, marketID)
	if err != nil {
		return model.DerivativeReferenceSnapshot{}, err
	}
	return referenceFromLighterFunding(inst.ID, rate, c.clk.Now()), nil
}

func (c *marketDataClient) SubscribeReference(ctx context.Context, id model.InstrumentID) error {
	inst, marketID, err := c.perpInstrument(id)
	if err != nil {
		return err
	}
	if c.ws == nil {
		return fmt.Errorf("lighter: market websocket not configured: %w", errs.ErrNotSupported)
	}
	if err := c.connectWS(); err != nil {
		return err
	}
	return c.ws.SubscribeMarketStats(marketID, func(payload []byte) {
		snapshot, ok := referenceFromLighterMarketStats(inst.ID, payload, c.clk.Now())
		if !ok {
			return
		}
		c.stream.Emit(contract.NewMarketEnvelopeWithMeta(
			contract.ReferenceDataEvent{Snapshot: snapshot},
			contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream},
		))
	})
}

func (c *marketDataClient) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	inst, marketID, err := c.perpInstrument(id)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	if c.rest == nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("lighter: rest client not configured: %w", errs.ErrNotSupported)
	}
	details, err := c.rest.GetOrderBookDetails(ctx, &marketID, nil)
	if err != nil {
		return model.OpenInterestSnapshot{}, err
	}
	for _, detail := range details.OrderBookDetails {
		if detail != nil && detail.MarketId == marketID {
			return openInterestFromLighterDetail(inst.ID, detail, c.clk.Now()), nil
		}
	}
	return model.OpenInterestSnapshot{}, fmt.Errorf("lighter: open interest not found for market id %d", marketID)
}

func (c *marketDataClient) perpInstrument(id model.InstrumentID) (*model.Instrument, int, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return nil, 0, fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	if inst.ID.Kind != enums.KindPerp {
		return nil, 0, fmt.Errorf("lighter: reference data only supported for perps: %w", errs.ErrNotSupported)
	}
	return inst, *inst.AssetIndex, nil
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
		parsedPrice, err := decimal.NewFromString(strings.TrimSpace(price))
		if err != nil || !parsedPrice.IsPositive() {
			continue
		}
		parsedSize, err := decimal.NewFromString(strings.TrimSpace(size))
		if err != nil || !parsedSize.IsPositive() {
			continue
		}
		key := parsedPrice.String()
		levels[key] = levels[key].Add(parsedSize)
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
	if c.ws != nil {
		c.ws.Close()
	}
	c.stream.Close()
	return nil
}

func (c *marketDataClient) connectWS() error {
	c.connOnce.Do(func() { c.connErr = c.ws.Connect() })
	if c.connErr != nil {
		return fmt.Errorf("lighter: market websocket connect: %w", c.connErr)
	}
	return nil
}

func referenceFromLighterFunding(id model.InstrumentID, rate *sdk.FundingRate, receivedAt time.Time) model.DerivativeReferenceSnapshot {
	s := model.DerivativeReferenceSnapshot{InstrumentID: id, Timestamp: receivedAt, ReceivedAt: receivedAt}
	if rate != nil {
		s.FundingRate = decimal.NewFromFloat(rate.Rate)
		s.Fields = s.Fields.With(model.ReferenceHasFundingRate)
		setLighterReferenceFieldTime(&s, model.ReferenceFieldFundingRate, receivedAt, receivedAt)
		s.FundingInterval = time.Hour
		s.Fields = s.Fields.With(model.ReferenceHasFundingInterval)
		setLighterReferenceFieldTime(&s, model.ReferenceFieldFundingInterval, receivedAt, receivedAt)
	}
	return s
}

func referenceFromLighterMarketStats(id model.InstrumentID, payload []byte, receivedAt time.Time) (model.DerivativeReferenceSnapshot, bool) {
	var event sdk.WsMarketStatsEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return model.DerivativeReferenceSnapshot{}, false
	}
	ts := firstNonZeroTime(parseMillisOrMicros(firstNonZeroInt64(event.Timestamp, event.MarketStats.FundingTimestamp)), receivedAt)
	s := model.DerivativeReferenceSnapshot{InstrumentID: id, Timestamp: ts, ReceivedAt: receivedAt}
	if rate := firstNonEmpty(event.MarketStats.CurrentFundingRate, event.MarketStats.FundingRate); rate != "" {
		s.FundingRate = dec(rate)
		s.Fields = s.Fields.With(model.ReferenceHasFundingRate)
		setLighterReferenceFieldTime(&s, model.ReferenceFieldFundingRate, ts, receivedAt)
		s.FundingInterval = time.Hour
		s.Fields = s.Fields.With(model.ReferenceHasFundingInterval)
		setLighterReferenceFieldTime(&s, model.ReferenceFieldFundingInterval, ts, receivedAt)
	}
	if event.MarketStats.MarkPrice != "" {
		s.MarkPrice = dec(event.MarketStats.MarkPrice)
		s.Fields = s.Fields.With(model.ReferenceHasMarkPrice)
		setLighterReferenceFieldTime(&s, model.ReferenceFieldMarkPrice, ts, receivedAt)
	}
	if event.MarketStats.IndexPrice != "" {
		s.IndexPrice = dec(event.MarketStats.IndexPrice)
		s.Fields = s.Fields.With(model.ReferenceHasIndexPrice)
		setLighterReferenceFieldTime(&s, model.ReferenceFieldIndexPrice, ts, receivedAt)
	}
	return s, s.Fields != 0
}

func setLighterReferenceFieldTime(s *model.DerivativeReferenceSnapshot, field model.ReferenceField, venueTime, receivedAt time.Time) {
	if venueTime.IsZero() {
		venueTime = receivedAt
	}
	s.FieldTimes.Set(field, model.FieldFreshness{Venue: venueTime, Received: receivedAt})
}

func openInterestFromLighterDetail(id model.InstrumentID, detail *sdk.OrderBookDetail, receivedAt time.Time) model.OpenInterestSnapshot {
	s := model.OpenInterestSnapshot{InstrumentID: id, Timestamp: receivedAt, ReceivedAt: receivedAt}
	if detail == nil {
		return s
	}
	s.OpenInterest = decimal.NewFromFloat(detail.OpenInterest)
	s.Fields = s.Fields.With(model.OpenInterestHasQuantity)
	s.Unit = "contracts"
	s.Fields = s.Fields.With(model.OpenInterestHasUnit)
	return s
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func lighterProviderHasKind(provider *registry, kind enums.InstrumentKind) bool {
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
